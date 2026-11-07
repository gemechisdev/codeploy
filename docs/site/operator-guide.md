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

## Upgrade and rollback

`codeplo update` and `codeplo rollback` are break-glass host commands (run as root or the service user). There is no
HTTP route for either.

Upgrade from a local release directory (deterministic, preferred):

    codeplo update --from /srv/release

The directory must hold the full artefact set `install.sh` verifies: the two Linux binaries for the host
architecture, the two systemd units, the Caddy bootstrap config, the PTY socket and unit, `checksums.txt`, and (for
cosign) `checksums.txt.sig` + `checksums.txt.pem`. Without `--from`, artefacts are downloaded over https from
`CODEPLO_BASE_URL` / `CODEPLO_VERSION` (`CODEPLO_VERSION` also labels the backup). Both binaries are installed to
`/usr/local/bin` by default; override with `--bin-dir`.

Before anything is written to the host, every asset's SHA-256 is checked against `checksums.txt` (all seven entries,
including the ones this command does not install), and unsafe filenames are refused. When the `cosign` binary and
`CODEPLO_COSIGN_IDENTITY` are present, `checksums.txt` is signature-verified; set `CODEPLO_REQUIRE_COSIGN=1` to make
that mandatory — without it, a missing cosign or identity is a loud warning, never a silent downgrade. Any
verification failure leaves the host exactly as it was.

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

## Environment reference

See `docs/ENV.md`.
