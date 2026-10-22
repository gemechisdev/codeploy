# Operator guide (draft)

## Install

1. Install Caddy from the official repository.
2. Run the verified installer: `CODEPLO_VERSION=vX.Y.Z bash install.sh`.
3. Tunnel: `ssh -L 7443:127.0.0.1:7443 <host>` and open the printed setup URL.

## Panel domain

- Set the panel hostname during setup; issuance is via managed Caddy.
- Loopback listener stays available as break-glass.

## Preview base domain

- Set `CODEPLO_PREVIEW_DOMAIN` to a zone with a wildcard A/AAAA at this host.
- Enable feature flag `git.preview_deploys`.
- Verify: `codeplo previews check`.

## GitHub App

- Set `CODEPLO_GITHUB_APP_*` (see `docs/ENV.md`).
- Verify: `codeplo github-app check`.

## Environment reference

See `docs/ENV.md`.
