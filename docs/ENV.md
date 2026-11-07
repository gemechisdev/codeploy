# Operator environment variables

All daemon configuration is `CODEPLO_*`. Defaults match a typical install under `/var/lib/codeplo`.

## Core

| Variable | Default | Purpose |
|---|---|---|
| `CODEPLO_DATA_DIR` | `/var/lib/codeplo` | SQLite, templates, work, volumes |
| `CODEPLO_LISTEN` | `127.0.0.1:7443` | Loopback HTTP only |
| `CODEPLO_SOCKET` | `$DATA_DIR/codeplo.sock` | Unix socket for Caddy → panel |
| `CODEPLO_TEMPLATES_DIR` | `$DATA_DIR/templates` | Catalog root |
| `CODEPLO_VOLUMES_DIR` | `$DATA_DIR/volumes` | Bind-mount root for resources |
| `CODEPLO_DOCKER_DATA_DIR` | `/var/lib/docker` | Forbidden bind source |

## Edge / TLS

| Variable | Default | Purpose |
|---|---|---|
| `CODEPLO_CADDY_ADMIN_SOCKET` | `/run/codeplo/caddy-admin.sock` | Caddy admin API |
| `CODEPLO_CADDY_DATA_DIR` | `$DATA_DIR/caddy` | Cert storage |
| `CODEPLO_CADDY_UNIT` | `codeplo-caddy` | systemd unit name |
| `CODEPLO_ACME_EMAIL` | _(empty)_ | ACME account email |

## Terminal

| Variable | Default | Purpose |
|---|---|---|
| `CODEPLO_PTY_SOCKET` | `/run/codeplo-pty/pty.sock` | Privileged PTY helper |

## Previews / GitHub App

| Variable | Default | Purpose |
|---|---|---|
| `CODEPLO_PREVIEW_DOMAIN` | _(empty)_ | Base domain for PR hostnames |
| `CODEPLO_GITHUB_APP_ID` | | App id |
| `CODEPLO_GITHUB_APP_INSTALLATION_ID` | | Installation id |
| `CODEPLO_GITHUB_APP_PRIVATE_KEY_FILE` | | Path to `.pem` |
| `CODEPLO_GITHUB_APP_PRIVATE_KEY` | | Inline PEM (alt) |
| `CODEPLO_GITHUB_APP_WEBHOOK_SECRET` | | App webhook secret |

## Object store (backups)

| Variable | Purpose |
|---|---|
| `CODEPLO_S3_ENDPOINT` | S3-compatible endpoint URL |
| `CODEPLO_S3_BUCKET` | Bucket name |
| `CODEPLO_S3_REGION` | Region (SigV4) |
| `CODEPLO_S3_ACCESS_KEY` | Access key |
| `CODEPLO_S3_SECRET_KEY` | Secret key |
| `CODEPLO_S3_PREFIX` | Optional key prefix |

## Installer / release

Shared by `install.sh` and `codeplo update` / `codeplo rollback`. `codeplo update` also reads `CODEPLO_DATA_DIR`
for the backup directory `<data-dir>/update-backups/<stamp>/`.

| Variable | Purpose |
|---|---|
| `CODEPLO_VERSION` | Release tag for `install.sh`; also labels a `codeplo update` backup |
| `CODEPLO_BASE_URL` | Override the release artefact base URL (https only) |
| `CODEPLO_REQUIRE_COSIGN` | Require cosign verification when `1` |
| `CODEPLO_COSIGN_IDENTITY` | Certificate identity regexp for cosign |
| `CODEPLO_COSIGN_ISSUER` | OIDC issuer for cosign |

Hardening spool: `CODEPLO_REVERT_DIR` (default `$DATA_DIR/revert`).
