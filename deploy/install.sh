#!/usr/bin/env bash
# codeplo verified installer. Usage: CODEPLO_VERSION=v0.1.0 bash install.sh
# Downloads the release artefacts (both binaries, the two systemd units and the Caddy bootstrap config),
# verifies the SHA-256 checksum list, installs, starts the services and prints the SSH-tunnel setup URL.
# cosign verification of checksums.txt runs when cosign is installed (set CODEPLO_REQUIRE_COSIGN=1 to make it mandatory).
# This script needs bash (arrays + pipefail). The guard runs under dash too, so `sh install.sh` says why it stops.
[ -n "${BASH_VERSION:-}" ] || { echo "this installer needs bash: bash install.sh" >&2; exit 2; }
set -euo pipefail

VERSION="${CODEPLO_VERSION:?set CODEPLO_VERSION, e.g. v0.1.0}"
BASE="${CODEPLO_BASE_URL:-https://github.com/codeplo/codeplo/releases/download/$VERSION}"
[ "$(id -u)" -eq 0 ] || { echo "run as root" >&2; exit 1; }
command -v systemctl >/dev/null || { echo "systemd is required" >&2; exit 1; }
case "$(uname -m)" in x86_64) ARCH=amd64 ;; aarch64|arm64) ARCH=arm64 ;; *) echo "unsupported arch" >&2; exit 1 ;; esac
# Caddy is the platform's edge (docs/adr/0003) and the unit starts /usr/bin/caddy. Checked before anything is
# downloaded so a host is never left half-configured; codeplo does not pull a third-party apt repo silently.
if ! [ -x /usr/bin/caddy ] && ! command -v caddy >/dev/null 2>&1; then
  echo "caddy is required (the codeplo-caddy unit runs /usr/bin/caddy)." >&2
  echo "Install it from the official Caddy repository first, then re-run this installer." >&2
  exit 1
fi
# Every artefact this installer fetches and verifies, in the order the checksum list is expected to carry them.
ASSETS=("codeplod_linux_$ARCH" "codeplo_linux_$ARCH" checksums.txt codeplod.service codeplo-caddy.service caddy-bootstrap.json)
ASSET_RE=" (codeplod_linux_$ARCH|codeplo_linux_$ARCH|codeplod.service|codeplo-caddy.service|caddy-bootstrap.json)\$"

tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
cd "$tmp"
for f in "${ASSETS[@]}"; do
  curl -fsSL --proto '=https' --tlsv1.2 -o "$f" "$BASE/$f"
done
# Signature over the checksum list (keyless cosign; identity must be pinned by the caller or the release pipeline).
if command -v cosign >/dev/null 2>&1 && [ -n "${CODEPLO_COSIGN_IDENTITY:-}" ]; then
  for f in checksums.txt.sig checksums.txt.pem; do curl -fsSL --proto '=https' --tlsv1.2 -o "$f" "$BASE/$f"; done
  cosign verify-blob --certificate checksums.txt.pem --signature checksums.txt.sig \
    --certificate-identity-regexp "$CODEPLO_COSIGN_IDENTITY" \
    --certificate-oidc-issuer "${CODEPLO_COSIGN_ISSUER:-https://token.actions.githubusercontent.com}" checksums.txt
elif [ "${CODEPLO_REQUIRE_COSIGN:-0}" = "1" ]; then
  echo "cosign and CODEPLO_COSIGN_IDENTITY are required when CODEPLO_REQUIRE_COSIGN=1" >&2; exit 1
else
  echo "warning: cosign signature not verified (cosign or CODEPLO_COSIGN_IDENTITY missing); only SHA-256 checks apply" >&2
fi
# Verify every downloaded artefact, including both unit files and the bootstrap config; abort on any mismatch.
grep -E "$ASSET_RE" checksums.txt | sha256sum -c -
[ "$(grep -cE "$ASSET_RE" checksums.txt)" -eq 5 ] || { echo "checksums.txt is missing an entry" >&2; exit 1; }

id codeplo >/dev/null 2>&1 || useradd --system --home /var/lib/codeplo --shell /usr/sbin/nologin codeplo
install -m 0755 "codeplod_linux_$ARCH" /usr/local/bin/codeplod
install -m 0755 "codeplo_linux_$ARCH" /usr/local/bin/codeplo
install -m 0644 codeplod.service /etc/systemd/system/codeplod.service
install -m 0644 codeplo-caddy.service /etc/systemd/system/codeplo-caddy.service
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

systemctl daemon-reload
# Caddy first: codeplod's reconciler pushes routes into its admin socket, and a missing edge must not look like
# a panel failure. A failed start is reported, not fatal — the SSH tunnel path (§3.12) still reaches the panel.
if ! systemctl enable --now codeplo-caddy; then
  echo "warning: codeplo-caddy did not start; the panel is still reachable over the SSH tunnel below." >&2
  echo "  inspect with: systemctl status codeplo-caddy; journalctl -u codeplo-caddy -n 50" >&2
fi
systemctl enable --now codeplod
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
