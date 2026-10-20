#!/usr/bin/env bash
# codeplo verified installer. Usage: CODEPLO_VERSION=v0.1.0 sh install.sh
# Downloads release binaries, verifies the SHA-256 checksum list, installs, starts the service and prints
# the SSH-tunnel setup URL. cosign verification of checksums.txt runs when cosign is installed (set CODEPLO_REQUIRE_COSIGN=1 to make it mandatory).
set -euo pipefail

VERSION="${CODEPLO_VERSION:?set CODEPLO_VERSION, e.g. v0.1.0}"
BASE="${CODEPLO_BASE_URL:-https://github.com/codeplo/codeplo/releases/download/$VERSION}"
[ "$(id -u)" -eq 0 ] || { echo "run as root" >&2; exit 1; }
command -v systemctl >/dev/null || { echo "systemd is required" >&2; exit 1; }
case "$(uname -m)" in x86_64) ARCH=amd64 ;; aarch64|arm64) ARCH=arm64 ;; *) echo "unsupported arch" >&2; exit 1 ;; esac

tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
cd "$tmp"
for f in "codeplod_linux_$ARCH" "codeplo_linux_$ARCH" checksums.txt codeplod.service; do
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
# Verify every downloaded artefact, including the unit file; abort on any mismatch.
grep -E " (codeplod_linux_$ARCH|codeplo_linux_$ARCH|codeplod.service)\$" checksums.txt | sha256sum -c -
[ "$(grep -cE " (codeplod_linux_$ARCH|codeplo_linux_$ARCH|codeplod.service)\$" checksums.txt)" -eq 3 ] || { echo "checksums.txt is missing an entry" >&2; exit 1; }

id codeplo >/dev/null 2>&1 || useradd --system --home /var/lib/codeplo --shell /usr/sbin/nologin codeplo
install -m 0755 "codeplod_linux_$ARCH" /usr/local/bin/codeplod
install -m 0755 "codeplo_linux_$ARCH" /usr/local/bin/codeplo
install -m 0644 codeplod.service /etc/systemd/system/codeplod.service
install -d -m 0700 -o codeplo -g codeplo /var/lib/codeplo

systemctl daemon-reload
systemctl enable --now codeplod
for _ in $(seq 1 30); do
  curl -fsS http://127.0.0.1:7443/api/v1/setup/state >/dev/null 2>&1 && break
  sleep 1
done

echo
echo "codeplo is running (listening on 127.0.0.1 only)."
echo "Open the panel through an SSH tunnel from your computer:"
echo "  ssh -L 7443:127.0.0.1:7443 $(whoami)@$(hostname -f 2>/dev/null || hostname)"
echo "Your one-time setup token (valid 1 hour, single use) is in the service log, or mint a new one:"
echo "  sudo -u codeplo codeplo setup-token"
