# Operator guide

> Status: written from the implemented code. The install/HTTPS/VM procedures below have **not** been re-verified on
> a fresh host in this session; `docs/site/verification-matrix.md` records exactly which host checks are still
> NOT RUN.

## Install

1. Install Caddy from the official repository.
2. Run the one-command installer from the official public release:
   `curl -fsSL https://github.com/gemechisdev/codeploy/releases/latest/download/install.sh | sudo bash`.
   Pin a tag with `sudo env CODEPLO_VERSION=v1.2.0 bash`, or serve a local/fork release with `CODEPLO_BASE_URL`
   / `CODEPLO_REPO`.
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

## Upgrade and rollback

`codeplo update` and `codeplo rollback` are break-glass host commands (run as root or the service user). There is no
HTTP route for either.

Upgrade from a local release directory (deterministic, preferred):

    codeplo update --from /srv/release

The directory must hold the full artefact set `install.sh` verifies: the two Linux binaries for the host
architecture, the two systemd units, the Caddy bootstrap config, the PTY socket and unit, `checksums.txt`, and (for
cosign) `checksums.txt.sig` + `checksums.txt.pem`. Without `--from`, artefacts are downloaded over https from the
official `gemechisdev/codeploy` latest release; `CODEPLO_VERSION` pins a tag, `CODEPLO_REPO` selects a fork and
`CODEPLO_BASE_URL` selects an https mirror (`CODEPLO_VERSION` also labels the backup). Both binaries are installed to
`/usr/local/bin` by default; override with `--bin-dir`.

Before anything is written to the host, every asset's SHA-256 is checked against `checksums.txt` (all seven entries,
including the ones this command does not install), and unsafe filenames are refused. When `cosign` is installed,
`checksums.txt` is signature-verified against the pinned official release-workflow identity by default;
`CODEPLO_COSIGN_IDENTITY` overrides it for a fork. Set `CODEPLO_REQUIRE_COSIGN=1` to make verification mandatory —
without it, a missing cosign is a loud warning, never a silent downgrade. Any verification failure leaves the host
exactly as it was.

On success a backup is written to `<data-dir>/update-backups/<stamp>/`: copies of the installed `codeplod` and
`codeplo` binaries, a SQLite snapshot taken with `VACUUM INTO` (works with WAL; no external `sqlite3` binary), and a
`manifest.json` (version, stamp, paths, sizes, sha256, db snapshot name). The binaries are then replaced atomically
(temp file + rename, mode 0755).

Migrations are applied by the daemon on its next boot, NOT by this command. Restart the service after an update
(`systemctl restart codeplod`); do not use this CLI as a migration step.

Rollback restores the most recent backup, or a specific one:

    codeplo rollback
    codeplo rollback --stamp 20261107T093012.123456789

It verifies the manifest hashes before any write, then restores the SQLite snapshot and both binaries. It refuses to
overwrite the database while `codeplod` is active (`systemctl is-active`); stop the daemon yourself — this command
never stops or kills anything. The backup is never deleted after a successful rollback.

Migrations are forward-only: rollback RESTORES a database snapshot, it never reverses a migration. A snapshot taken
before an update is consistent with the binaries that created it, and the daemon re-applies pending migrations on
boot.

## MCP endpoint (optional, off by default)

MCP is a daemon-local unix socket, not an HTTP route. To enable it, switch on the `mcp` module **and** the
`mcp.server` feature flag, and set `CODEPLO_MCP_TOKEN` to a `cpl_` API token whose scopes should bound every MCP
connection. The socket defaults to `<data-dir>/mcp.sock` (mode 0600) and can be moved with
`CODEPLO_MCP_SOCKET`. The gate is re-read at accept time, so disabling the flag takes effect on the next
connection without a restart; rotating the token takes effect the same way after the daemon's environment changes.

    codeplo mcp check
    codeplo mcp check --json

The check reports `enabled`, the resolved `socket`, `socket_present` and `token_set`. It **never** prints the token.
Outbound extension delivery is a separate feature flag, `extensions.outbound`, and needs no environment variable.

## Environment reference

See `docs/ENV.md` for the full `CODEPLO_*` list, including the two MCP variables.

## Build from source (for maintainers)

The SPA requires **Node 22.22.2** (`web/.nvmrc`); CI and release read that same file. `make test` runs both
`go test ./...` and the vitest suite. `make gen-check` must pass after any `api/openapi.yaml` edit, and
`make dist-pack DIST_FROM_GORELEASER=1` is the release packaging path.
