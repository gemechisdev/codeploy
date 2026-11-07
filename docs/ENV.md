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

## Extensions / MCP (daemon-local, off by default)

There is no HTTP route for any of this (see `docs/DECISIONS.md` T53). The MCP endpoint is a unix socket owned by the
daemon; both the `mcp` module row and the `mcp.server` feature flag must be enabled before a connection is served,
and the gate is re-read at accept time, so switching it off takes effect on the next connection without a restart.

| Variable | Default | Purpose |
|---|---|---|
| `CODEPLO_MCP_SOCKET` | `$DATA_DIR/mcp.sock` | Override the MCP unix socket path; created mode 0600 |
| `CODEPLO_MCP_TOKEN` | _(empty)_ | A `cpl_` API token whose scopes bind each MCP connection to a principal. Read at accept time, never logged, never printed |

Rotate `CODEPLO_MCP_TOKEN` by restarting the daemon with the new value; the change applies to the next connection.
`codeplo mcp check [--json]` reports the flag state, the resolved socket path, whether the socket exists and
**whether** a token is set — never the token itself. Outbound extension delivery is gated by the
`extensions.outbound` feature flag and needs no environment variable.

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

| Variable | Default | Purpose |
|---|---|---|
| `CODEPLO_REPO` | `gemechisdev/codeploy` | Official public release repository; set `owner/repo` for a fork |
| `CODEPLO_VERSION` | `latest` | `releases/latest/download`, or a pinned `v*` tag; also labels a `codeplo update` backup |
| `CODEPLO_BASE_URL` | _(empty)_ | Override the whole release artefact base URL (https only) |
| `CODEPLO_REQUIRE_COSIGN` | `0` | Require cosign verification when `1` |
| `CODEPLO_COSIGN_IDENTITY` | _(pinned to the official release workflow)_ | Override the certificate identity regexp for a fork |
| `CODEPLO_COSIGN_ISSUER` | `https://token.actions.githubusercontent.com` | OIDC issuer for cosign |

The official repo's latest release is currently `v1.2.0` (published 2026-11-07, `release.yml` run `42349115778`,
13 verified downloadable assets). Both `install.sh` and `codeplo update` default to it. When `cosign` is
installed, both verify `checksums.txt` against the pinned workflow identity automatically; without cosign they
warn and still enforce every SHA-256 checksum.

Hardening spool: `CODEPLO_REVERT_DIR` (default `$DATA_DIR/revert`).
