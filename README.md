# codeplo

A single-binary, self-hosted control plane for a VPS. It deploys containers, terminates HTTPS through a managed
Caddy, hardens the host reversibly, and gives the owner a browser terminal — all from one panel with a signed-in,
TOTP-gated, audited API.

- Daemon `codeplod` + CLI `codeplo`, both static Linux binaries (`amd64` and `arm64`)
- React/TypeScript SPA embedded in the daemon binary — no Node or web server needed on the host
- Loopback-only listener (`127.0.0.1:7443`); public HTTPS is added by Caddy once you set a panel domain
- SQLite with forward-only migrations, an append-only hash-chained audit log, write-only secrets

**Latest public release:** [`v1.2.0`](https://github.com/gemechisdev/codeploy/releases/tag/v1.2.0), published
2026-11-07 by `release.yml` with a keyless-signed `checksums.txt` — amd64/arm64 binaries, systemd units and the
installer, all downloadable from the release.

## Install on a VPS — one command

```bash
curl -fsSL https://github.com/gemechisdev/codeploy/releases/latest/download/install.sh | sudo bash
```

That command downloads the installer **from the release itself**, so the script and every binary/unit it fetches
come from the same published version. It installs, verifies, configures and starts everything, then prints your
SSH-tunnel setup URL. Nothing is cloned or compiled on the VPS.

To pin a specific version (recommended for reproducible fleets):

```bash
curl -fsSL https://github.com/gemechisdev/codeploy/releases/latest/download/install.sh \
  | sudo env CODEPLO_VERSION=v1.2.0 bash
```

Or fetch a version-pinned installer directly:

```bash
curl -fsSL https://github.com/gemechisdev/codeploy/releases/download/v1.2.0/install.sh | sudo bash
```

To install the current development `main` instead of a release:

```bash
curl -fsSL https://raw.githubusercontent.com/gemechisdev/codeploy/main/deploy/install.sh | sudo bash
```

### Requirements

| | |
|---|---|
| OS | Linux with **systemd** (Ubuntu/Debian/Fedora/RHEL family) |
| Arch | `x86_64` (amd64) or `aarch64` (arm64) |
| Access | root (`sudo bash` above) |
| Tools | `curl`, `sha256sum` (coreutils) |
| Optional | `cosign` for signature verification; Docker for container deploys; a domain for public HTTPS |

**Caddy** (the panel edge at `/usr/bin/caddy`) is installed automatically by the installer when missing —
official static binary from [caddyserver.com](https://caddyserver.com/docs/install), no third-party apt repo.
Skip with `CODEPLO_SKIP_CADDY_INSTALL=1` if you already manage Caddy yourself. Docker is optional at install
time — the panel boots without it, and container features appear once Docker is present.

### What the one command actually does

1. Ensures `/usr/bin/caddy` exists (downloads the official static binary when missing). Picks the latest
   published release (or `CODEPLO_VERSION`), downloads `checksums.txt`, both Linux binaries for your
   architecture, the systemd units, the Caddy bootstrap config and the PTY socket/unit.
2. Verifies the keyless-[cosign](https://docs.sigstore.dev/) signature over `checksums.txt` against
   `https://github.com/gemechisdev/codeploy/.github/workflows/release.yml@refs/tags/v*` when cosign is installed,
   then checks **all seven** downloaded artefacts against the SHA-256 list. A failed signature is fatal; every
   download is over HTTPS/TLS 1.2+.
3. Creates the `codeplo` system user and `/var/lib/codeplo`, installs the binaries to `/usr/local/bin`, the units
   to `/etc/systemd/system`, and the Caddy config to `/etc/codeplo/`.
4. Enables and starts `codeplo-caddy`, `codeplo-pty.socket` and `codeplod`, then waits up to 30 s for
   `/api/v1/setup/state` to answer — it only reports success when the panel is really up.
5. Prints the SSH tunnel command and how to mint the one-time setup token.

Every file written is listed in the [installer](deploy/install.sh); there is no hidden post-install network call.

## First-time setup

```bash
# On your laptop — the panel listens on loopback only:
ssh -L 7443:127.0.0.1:7443 root@YOUR_VPS
```

Open `http://127.0.0.1:7443` in your browser. Mint a setup token on the VPS if the printed one expired:

```bash
sudo -u codeplo codeplo setup-token
```

The setup wizard creates the owner account and **requires TOTP enrollment** before any other action. Once a
panel domain is configured, Caddy obtains the certificate and the panel is reachable over HTTPS; the loopback
listener and SSH tunnel remain as break-glass access.

## Verify the download yourself

```bash
# cosign is optional but recommended; the installer uses it automatically when present
curl -fsSL https://github.com/gemechisdev/codeploy/releases/latest/download/checksums.txt -o checksums.txt
curl -fsSL https://github.com/gemechisdev/codeploy/releases/latest/download/checksums.txt.sig -o checksums.txt.sig
curl -fsSL https://github.com/gemechisdev/codeploy/releases/latest/download/checksums.txt.pem -o checksums.txt.pem
cosign verify-blob --certificate checksums.txt.pem --signature checksums.txt.sig \
  --certificate-identity-regexp '^https://github\.com/gemechisdev/codeploy/\.github/workflows/release\.yml@refs/tags/v[0-9].*$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt
```

The installer and `codeplo update` both pin this identity by default, so cosign verification happens automatically
when cosign is installed. Set `CODEPLO_REQUIRE_COSIGN=1` to refuse an install that cannot verify the signature.
Override the release source with `CODEPLO_VERSION`, `CODEPLO_BASE_URL` (an https URL) or `CODEPLO_REPO`
(`owner/repo`) for a pinned version, mirror or fork; use `CODEPLO_COSIGN_IDENTITY` for a fork's own workflow.

## Upgrade and rollback

```bash
codeplo update --from /srv/release                # verified local artefact directory
codeplo update                                   # downloads the latest official release over https
CODEPLO_VERSION=v1.2.0 codeplo update            # pins a published tag
codeplo rollback                                 # restores the most recent backup
codeplo rollback --stamp 20261107T093012.123456789
```

`update` checks every SHA-256 (and cosign when installed) **before writing anything**, takes a `VACUUM INTO`
SQLite snapshot plus a `manifest.json`, and replaces both binaries atomically. Migrations are applied by the
daemon on its next boot, not by the CLI. `rollback` restores the snapshot and the previous binaries — migrations
are forward-only, so it restores rather than reverses. See the [operator guide](docs/site/operator-guide.md).

## Configuration

All daemon configuration is `CODEPLO_*`, placed in `/etc/default/codeplod`. The full list, including the
extensions/MCP variables, is in [docs/ENV.md](docs/ENV.md). Common ones:

| Variable | Default | Purpose |
|---|---|---|
| `CODEPLO_LISTEN` | `127.0.0.1:7443` | Loopback listener (keep it loopback-only) |
| `CODEPLO_DATA_DIR` | `/var/lib/codeplo` | SQLite, templates, work, volumes |
| `CODEPLO_ACME_EMAIL` | _(empty)_ | ACME account contact |
| `CODEPLO_PREVIEW_DOMAIN` | _(empty)_ | Base domain for PR preview hostnames |
| `CODEPLO_REPO` | `gemechisdev/codeploy` | Official release repository; override for a fork |
| `CODEPLO_VERSION` | `latest` | Pin a release tag for install or `codeplo update` |
| `CODEPLO_BASE_URL` | _(empty)_ | Override the whole https release asset URL |
| `CODEPLO_REQUIRE_COSIGN` | `0` | Require cosign verification during install or `update` |
| `CODEPLO_COSIGN_IDENTITY` | _(pinned to the release workflow)_ | Override the certificate identity regexp for a fork |

Useful checks once running: `codeplo doctor --verify-audit`, `codeplo status`, `codeplo mcp check --json`,
`codeplo previews check`, `codeplo github-app check`.

## Build from source

Requires Go (see `go.mod`) and Node **22.22.2** (`web/.nvmrc`, also used by CI and the release workflow).

```bash
make gen-check          # api/openapi.yaml <-> generated TS/Go types must be drift-free
make test               # go test ./... + the vitest suite
make build              # builds the SPA, embeds it, produces bin/codeplod and bin/codeplo
make dist               # produces the full release artefact set in dist/
```

`make dist` is also what the release pipeline packages; see [docs/RELEASE_NOTES.md](docs/RELEASE_NOTES.md).

## Documentation

| | |
|---|---|
| [Operator guide](docs/site/operator-guide.md) | install, panel domain, previews, GitHub App, upgrade/rollback, MCP |
| [Environment variables](docs/ENV.md) | complete `CODEPLO_*` reference |
| [Release notes](docs/RELEASE_NOTES.md) | pipeline, migration head, known measurement gaps |
| [Verification matrix](docs/site/verification-matrix.md) | exactly what has and has not been run |
| [Progress / proof map](FUTURE_PROGRESS.md) | phase-by-phase implementation and test evidence |
| [Decisions](docs/DECISIONS.md) | the judgment calls and why |
| [Threat model](docs/threat-model.md) | security model |
| [API contract](api/openapi.yaml) | the authoritative OpenAPI spec |

### Honest status

`v1.2.0` is published from a signed, automated release workflow: 13 downloadable assets, all verified as uploaded
and byte-range retrievable, with `releases/latest/download/install.sh` byte-matching the repository script. Local
gates pass (`go build`, `go vet`, `go test ./...`, the full race tree, `make gen-check`, migrations to v8, and the
frontend suite), and CI has green runs with `go`, `go-arm64` and `web` jobs. Some checks still require real
hardware — running this published installer on a fresh VPS, a systemd PTY spike, VM firewall/SSH fault injection,
real-DNS preview lifecycle, public HTTPS issuance, and a real GitHub App installation. Those are recorded as
**NOT RUN** in the [verification matrix](docs/site/verification-matrix.md); nothing here claims they passed.

## Uninstall

```bash
sudo codeplo uninstall --dry-run     # show exactly what would be removed
sudo codeplo uninstall               # asks you to type "uninstall"
```

Removes the services and unit files, `/etc/codeplo`, the data directory (`/var/lib/codeplo`), the `codeplo` service
user, the binaries, and the `codeplo-ops` operator account when the installer created it. Never removes an account
that already existed. Flags: `--keep-data` (keep data and users so a reinstall resumes), `--remove-workloads`
(also remove containers, networks and volumes labelled `codeplo.managed=true`), `--revert-hardening` (undo pending
hardening changes first), `--purge-config` (also remove `/etc/default/codeplod`), `--yes`, `--force`.
Applied firewall/sshd hardening is not undone. Hosts installed before this command existed can `codeplo update` first.
