#!/usr/bin/env bash
# codeplo verified installer.
#
# One command on a fresh VPS (installs the latest release; cosign signature is verified whenever
# cosign and the signed checksum assets are available):
#   curl -fsSL https://github.com/gemechisdev/codeploy/releases/latest/download/install.sh | sudo bash
#
# Pin a version instead:
#   curl -fsSL .../install.sh | sudo CODEPLO_VERSION=v1.2.0 bash
#
# Downloads the release artefacts (both binaries, the systemd units and the Caddy bootstrap config),
# verifies the SHA-256 checksum list, installs, starts the services and prints the SSH-tunnel setup URL.
# checksums.txt is keyless-cosign signed by this repo's release workflow; when cosign is installed the
# signature is checked automatically against that exact identity (set CODEPLO_REQUIRE_COSIGN=1 to make
# it mandatory even when cosign is absent).
# Slow or offline host: put every artefact in a directory and run `sudo CODEPLO_LOCAL_DIR=/path bash install.sh`.
# This script needs bash (arrays + pipefail). The guard runs under dash too, so `sh install.sh` says why it stops.
[ -n "${BASH_VERSION:-}" ] || { echo "this installer needs bash: bash install.sh" >&2; exit 2; }
set -euo pipefail

# CODEPLO_REPO lets a fork serve its own releases; CODEPLO_BASE_URL overrides the whole URL.
REPO="${CODEPLO_REPO:-gemechisdev/codeploy}"
VERSION="${CODEPLO_VERSION:-latest}"
# Validate the two path fragments before they reach curl. A GitHub repo is owner/name only, and a tag
# is either "latest" or a v-prefixed semver-ish string; this also keeps the cosign identity below exact.
[[ "$REPO" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ && "$REPO" != *".."* ]] || {
  echo "invalid CODEPLO_REPO (expected owner/repo)" >&2; exit 1;
}
if [ -z "${CODEPLO_BASE_URL:-}" ] && [ "$VERSION" != "latest" ]; then
  [[ "$VERSION" =~ ^v[0-9][A-Za-z0-9._+-]*$ && "$VERSION" != *".."* ]] || {
    echo "invalid CODEPLO_VERSION (use latest or a v-prefixed tag)" >&2; exit 1;
  }
fi
if [ -n "${CODEPLO_BASE_URL:-}" ]; then
  BASE="$CODEPLO_BASE_URL"
elif [ "$VERSION" = "latest" ]; then
  # GitHub redirects /releases/latest/download/<asset> to the newest non-draft, non-prerelease release,
  # so every artefact below comes from one and the same release without needing the API or a version number.
  BASE="https://github.com/$REPO/releases/latest/download"
else
  BASE="https://github.com/$REPO/releases/download/$VERSION"
fi
# The release workflow signs checksums.txt with this OIDC identity. Pinning it here means an operator who
# already has cosign gets signature verification by default, not silently skipped. Override it for a fork.
REPO_RE="${REPO//./\\.}"
COSIGN_IDENTITY_DEFAULT='^https://github\.com/'"$REPO_RE"'/\.github/workflows/release\.yml@refs/tags/v[0-9].*$'
COSIGN_IDENTITY="${CODEPLO_COSIGN_IDENTITY:-$COSIGN_IDENTITY_DEFAULT}"

echo "codeplo installer: repo=$REPO version=$VERSION arch=$(uname -m)" >&2
echo "  release URL: $BASE" >&2
[ "$(id -u)" -eq 0 ] || { echo "run as root (try: curl ... | sudo bash)" >&2; exit 1; }
for tool in curl sha256sum systemctl; do
  command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 1; }
done
case "$(uname -m)" in x86_64) ARCH=amd64 ;; aarch64|arm64) ARCH=arm64 ;; *) echo "unsupported arch" >&2; exit 1 ;; esac
# Caddy is the platform's edge (docs/adr/0003) and the unit starts /usr/bin/caddy. Checked before anything is
# downloaded so a host is never left half-configured; codeplo does not pull a third-party apt repo silently.
if ! [ -x /usr/bin/caddy ] && ! command -v caddy >/dev/null 2>&1; then
  echo "caddy is required (the codeplo-caddy unit runs /usr/bin/caddy)." >&2
  echo "Install it from the official Caddy repository first, then re-run this installer." >&2
  exit 1
fi
# Every artefact this installer fetches and verifies, in the order the checksum list is expected to carry them.
ASSETS=("codeplod_linux_$ARCH" "codeplo_linux_$ARCH" checksums.txt codeplod.service codeplo-caddy.service caddy-bootstrap.json codeplo-pty.socket "codeplo-pty@.service")
ASSET_RE=" (codeplod_linux_$ARCH|codeplo_linux_$ARCH|codeplod.service|codeplo-caddy.service|caddy-bootstrap.json|codeplo-pty.socket|codeplo-pty@.service)\$"

# Offline / slow-link installs: CODEPLO_LOCAL_DIR points at a directory that already holds every release artefact
# (download them with a browser or `curl -C -`, then run this script). The same checksum and signature checks apply.
LOCAL_DIR=""
if [ -n "${CODEPLO_LOCAL_DIR:-}" ]; then
  LOCAL_DIR="$(cd "$CODEPLO_LOCAL_DIR" 2>/dev/null && pwd)" || { echo "CODEPLO_LOCAL_DIR is not a directory" >&2; exit 1; }
  echo "  using local artefacts from $LOCAL_DIR (no download)" >&2
fi
# fetch NAME: copy from CODEPLO_LOCAL_DIR, or download with a progress bar, retries that resume a partial file, a
# connect timeout, and an abort if the transfer stalls (under 1 byte/s for 60 s) so a dead link fails instead of hanging.
fetch() {
  local f="$1"
  if [ -n "$LOCAL_DIR" ]; then
    [ -f "$LOCAL_DIR/$f" ] || { echo "missing $LOCAL_DIR/$f" >&2; return 1; }
    cp "$LOCAL_DIR/$f" "$f"
    return
  fi
  curl -fL --proto '=https' --tlsv1.2 --retry 5 --retry-delay 3 --connect-timeout 20 \
    --speed-limit 1 --speed-time 60 -C - --progress-bar -o "$f" "$BASE/$f"
}

tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
cd "$tmp"
n=0
for f in "${ASSETS[@]}"; do
  n=$((n + 1))
  echo "[$n/${#ASSETS[@]}] $f" >&2
  fetch "$f" || { echo "download failed: $f (re-run to retry, or see CODEPLO_LOCAL_DIR in the header)" >&2; exit 1; }
done
# Signature over the checksum list (keyless cosign, signed by this repo's release workflow).
# A verification FAILURE is always fatal; missing signature assets only downgrade to a warning when the
# operator did not explicitly demand cosign. That keeps an unsigned-but-checksummed install possible
# without ever letting a bad signature pass silently.
signed=0
if command -v cosign >/dev/null 2>&1; then
  have_sig=1
  for f in checksums.txt.sig checksums.txt.pem; do
    fetch "$f" 2>/dev/null || have_sig=0
  done
  if [ "$have_sig" = 1 ]; then
    cosign verify-blob --certificate checksums.txt.pem --signature checksums.txt.sig \
      --certificate-identity-regexp "$COSIGN_IDENTITY" \
      --certificate-oidc-issuer "${CODEPLO_COSIGN_ISSUER:-https://token.actions.githubusercontent.com}" checksums.txt \
      || { echo "cosign verification FAILED for checksums.txt; refusing to install" >&2; exit 1; }
    echo "cosign: checksums.txt signature verified" >&2
    signed=1
  fi
fi
if [ "$signed" != 1 ]; then
  if [ "${CODEPLO_REQUIRE_COSIGN:-0}" = "1" ]; then
    echo "CODEPLO_REQUIRE_COSIGN=1 but cosign is unavailable or the signed checksum assets could not be verified" >&2
    exit 1
  fi
  echo "warning: cosign signature not verified; only SHA-256 checks apply (install cosign for signature verification)" >&2
fi
# Verify every downloaded artefact, including both unit files and the bootstrap config; abort on any mismatch.
grep -E "$ASSET_RE" checksums.txt | sha256sum -c -
[ "$(grep -cE "$ASSET_RE" checksums.txt)" -eq 7 ] || { echo "checksums.txt is missing an entry" >&2; exit 1; }

# An existing install is upgraded in place: data, users, keys and setup state are left alone.
UPGRADE=0
[ -f /var/lib/codeplo/codeplo.db ] && UPGRADE=1
WAS_ACTIVE=0
systemctl is-active --quiet codeplod 2>/dev/null && WAS_ACTIVE=1
# Ensure the system group exists first so SocketGroup=codeplo and the service User= can resolve it.
getent group codeplo >/dev/null 2>&1 || groupadd --system codeplo
id codeplo >/dev/null 2>&1 || useradd --system --gid codeplo --home /var/lib/codeplo --shell /usr/sbin/nologin codeplo
# Make sure the service user is in its own group (covers older installs that created the user differently).
id -nG codeplo | grep -qw codeplo || usermod -aG codeplo codeplo
install -m 0755 "codeplod_linux_$ARCH" /usr/local/bin/codeplod
install -m 0755 "codeplo_linux_$ARCH" /usr/local/bin/codeplo
install -m 0644 codeplod.service /etc/systemd/system/codeplod.service
install -m 0644 codeplo-caddy.service /etc/systemd/system/codeplo-caddy.service
install -m 0644 codeplo-pty.socket /etc/systemd/system/codeplo-pty.socket
install -m 0644 "codeplo-pty@.service" "/etc/systemd/system/codeplo-pty@.service"
# Container shells and resource logs need the Docker socket. Docker group membership is root-equivalent, so it is
# granted only when Docker is installed, and only through a drop-in the owner can read and remove.
if getent group docker >/dev/null 2>&1; then
  install -d -m 0755 /etc/systemd/system/codeplod.service.d
  printf '[Service]\nSupplementaryGroups=docker\n' > /etc/systemd/system/codeplod.service.d/docker.conf
fi
install -d -m 0755 /etc/codeplo
install -m 0644 caddy-bootstrap.json /etc/codeplo/caddy-bootstrap.json
# Operator-tunable daemon environment. Shipped commented out: certificate issuance works without a contact
# address, and setting CODEPLO_ACME_EMAIL is the operator's choice, not something to invent for them.
[ -f /etc/default/codeplod ] || {
  install -d -m 0755 /etc/default
  printf '# codeplo control plane environment (see `systemctl cat codeplod`).\n# Certificate contact address for ACME issuance:\n# CODEPLO_ACME_EMAIL=admin@example.com\n' > /etc/default/codeplod
}
install -d -m 0700 -o codeplo -g codeplo /var/lib/codeplo
# Caddy writes the ACME account and certificates here (XDG_DATA_HOME in its unit) and runs as root with
# group codeplo, so codeplod can read certificate status without being able to change the edge.
install -d -m 0750 -o codeplo -g codeplo /var/lib/codeplo/caddy

# Default host operator (docs/SETUP_FRICTION_AND_PLAN.md C1). The installer already runs as root, so it creates the
# named non-root login user, a panel-owned ed25519 key pair and the authorized_keys entry here; the unprivileged
# daemon cannot (it has no root and ProtectHome hides /home). The setup wizard pre-selects this user.
# Idempotent: an existing user and key are reused. Skip entirely with CODEPLO_NO_OPERATOR=1.
OPERATOR_USER="${CODEPLO_OPERATOR_USER:-codeplo-ops}"
OPERATOR_DIR=/var/lib/codeplo/operator
# Only on a first install: an upgrade must not add a sudo-capable account to a host that finished setup long ago.
if [ "$UPGRADE" = 1 ]; then
  echo "existing install detected: keeping your data, users and operator as they are." >&2
elif [ "${CODEPLO_NO_OPERATOR:-0}" != 1 ]; then
  [[ "$OPERATOR_USER" =~ ^[a-z_][a-z0-9_-]{0,31}$ ]] || { echo "invalid CODEPLO_OPERATOR_USER" >&2; exit 1; }
  operator_created=0
  if ! id "$OPERATOR_USER" >/dev/null 2>&1; then
    # useradd without -p leaves the password locked: key login only.
    useradd -m -s /bin/bash "$OPERATOR_USER"
    operator_created=1
  fi
  for g in sudo wheel; do
    if getent group "$g" >/dev/null 2>&1; then usermod -aG "$g" "$OPERATOR_USER"; break; fi
  done
  getent group docker >/dev/null 2>&1 && usermod -aG docker "$OPERATOR_USER"
  install -d -m 0700 -o codeplo -g codeplo "$OPERATOR_DIR"
  if [ ! -f "$OPERATOR_DIR/id_ed25519" ]; then
    if command -v ssh-keygen >/dev/null 2>&1; then
      ssh-keygen -q -t ed25519 -N "" -C "codeplo-operator" -f "$OPERATOR_DIR/id_ed25519"
      chown codeplo:codeplo "$OPERATOR_DIR/id_ed25519" "$OPERATOR_DIR/id_ed25519.pub"
      chmod 0600 "$OPERATOR_DIR/id_ed25519"; chmod 0644 "$OPERATOR_DIR/id_ed25519.pub"
    else
      echo "warning: ssh-keygen not found; skipping operator key (install openssh-client and re-run)." >&2
    fi
  fi
  if [ -f "$OPERATOR_DIR/id_ed25519.pub" ]; then
    op_home="$(getent passwd "$OPERATOR_USER" | cut -d: -f6)"
    install -d -m 0700 -o "$OPERATOR_USER" -g "$(id -gn "$OPERATOR_USER")" "$op_home/.ssh"
    ak="$op_home/.ssh/authorized_keys"
    touch "$ak"
    grep -qxF -f "$OPERATOR_DIR/id_ed25519.pub" "$ak" || cat "$OPERATOR_DIR/id_ed25519.pub" >> "$ak"
    chown "$OPERATOR_USER:$(id -gn "$OPERATOR_USER")" "$ak"; chmod 0600 "$ak"
  fi
  # `codeplo uninstall` deletes this account only when the marker below exists, i.e. this installer created it.
  # An account that was already there is never removed. The marker is kept across re-runs.
  [ "$operator_created" = 1 ] && printf '1\n' > "$OPERATOR_DIR/created"
  printf '%s\n' "$OPERATOR_USER" > "$OPERATOR_DIR/username"
  chown codeplo:codeplo "$OPERATOR_DIR/username"; chmod 0644 "$OPERATOR_DIR/username"
  [ -f "$OPERATOR_DIR/created" ] && { chown codeplo:codeplo "$OPERATOR_DIR/created"; chmod 0644 "$OPERATOR_DIR/created"; }
  echo "operator: $OPERATOR_USER ready (sudo group, key in $OPERATOR_DIR); the setup wizard will pre-select it." >&2
fi

systemctl daemon-reload
# Caddy first: codeplod's reconciler pushes routes into its admin socket, and a missing edge must not look like
# a panel failure. A failed start is reported, not fatal — the SSH tunnel path (§3.12) still reaches the panel.
if ! systemctl enable --now codeplo-caddy; then
  echo "warning: codeplo-caddy did not start; the panel is still reachable over the SSH tunnel below." >&2
  echo "  inspect with: systemctl status codeplo-caddy; journalctl -u codeplo-caddy -n 50" >&2
fi
# Host shell helper: socket-activated, one root process per shell, only the codeplo user may connect.
systemctl enable --now codeplo-pty.socket || echo "warning: codeplo-pty.socket did not start; the host shell stays unavailable." >&2
# Guarantee the runtime directory is group-accessible even if a previous install left it root:root.
if [ -d /run/codeplo-pty ]; then
  chown root:codeplo /run/codeplo-pty 2>/dev/null || true
  chmod 0750 /run/codeplo-pty 2>/dev/null || true
fi
systemctl enable --now codeplod
# `enable --now` leaves a running service on its old binary; an upgrade has to restart it to take the new one.
if [ "$WAS_ACTIVE" = 1 ]; then
  echo "restarting codeplod to run the new version (migrations apply on this boot)..." >&2
  systemctl restart codeplod
fi
# The install only counts as done when the panel actually answers. A service that is "active" for a second and
# then crash-looping would otherwise be reported as installed (systemctl --now does not wait for the failure).
ready=0
for _ in $(seq 1 30); do
  if curl -fsS http://127.0.0.1:7443/api/v1/setup/state >/dev/null 2>&1; then ready=1; break; fi
  sleep 1
done
if [ "$ready" != 1 ]; then
  echo "codeplod is not answering on 127.0.0.1:7443; the install is incomplete." >&2
  echo "  inspect with: systemctl status codeplod; journalctl -u codeplod -n 50" >&2
  exit 1
fi

echo
echo "codeplo is running (listening on 127.0.0.1 only)."
echo "Edge: codeplo-caddy is enabled; the panel adds its routes through the admin socket as domains are created."
echo "Open the panel through an SSH tunnel from your computer:"
echo "  ssh -L 7443:127.0.0.1:7443 $(whoami)@$(hostname -f 2>/dev/null || hostname)"
echo "Your one-time setup token (valid 1 hour, single use) is in the service log, or mint a new one:"
echo "  sudo -u codeplo codeplo setup-token"
