# Release notes (development)

**The first public tag has not been pushed yet.** `.github/workflows/release.yml` has never executed on a tag, so no
signed artefact exists at this commit. Everything below describes code in the tree and the release that is about to be
published, not a shipped download. The published result is recorded in `PROGRESS.md` and
`docs/site/verification-matrix.md` once the tag run finishes.

## Known measurement gaps (do not silent-tune)

- **Idle RSS (T30):** Profile-A budget is 50 MB; measured idle RSS was **78.8 MB**. Re-measure on the reference VM in CI before publishing a number operators will rely on.
- **Caddy zero-drop spike (T31):** Spike A remains **unmet** (flaky reload timing). The assertion stays strict.
- **Profile B/C (T50):** 1 vCPU/2 GB and 2 vCPU/4 GB reference VMs have never been measured.

## Migrations

- Head: **0008_extensions** (extension registry, tokens, subscriptions, deliveries).
- Migrations 0001–0008 apply from an empty data dir through the real `store.Migrate` (goose) path; proven by
  `TestMigrateAllToV8` / `make migrate-check`. `TestMigrateSeeds` asserts the seeded settings, feature flags and
  alert presets; `TestPreviewNoFK` proves the no-FK preview design.
- Migrations are **forward-only**. `codeplo rollback` restores a pre-update SQLite snapshot, it never reverses a
  migration. The daemon re-applies pending migrations on its next boot.

## Public install

The official repository is **`gemechisdev/codeploy`**. The one-command install is:

```bash
curl -fsSL https://github.com/gemechisdev/codeploy/releases/latest/download/install.sh | sudo bash
```

`install.sh` is itself a release asset, fetched from the same release as every artefact it downloads. It is not in
`checksums.txt`: it is the verifier, not a payload, and the installer's strict seven-entry assertion stays intact.
`CODEPLO_VERSION` pins a `v*` tag, `CODEPLO_REPO` selects a fork and `CODEPLO_BASE_URL` selects an https mirror.
`codeplo update` uses the same official/latest source by default; `codeplo rollback` restores a `VACUUM INTO`
snapshot and the previous binaries.

## Release pipeline (implemented, not yet run on a tag)

- `.goreleaser.yaml` builds raw `codeplod_linux_{amd64,arm64}` and `codeplo_linux_{amd64,arm64}` binaries
  directly into `dist/` (`no_unique_dist_dir: true`), with no archives. Its `before.hooks` build the SPA with
  `VITE_API_MODE=live` explicitly and stage it for `go:embed`; the Makefile `web` target does the same.
- `release.yml` runs `goreleaser build --clean`, then `make dist-pack DIST_FROM_GORELEASER=1`, which **reuses** those
  binaries (it refuses if one is missing rather than silently rebuilding), adds the five static assets, copies
  `install.sh`, and writes the 9-line `checksums.txt`.
- `checksums.txt` is keyless-signed with cosign using the workflow's OIDC identity, then verified inside the same
  job. Both `deploy/install.sh` and `codeplo update` pin that identity by default and verify automatically when
  cosign is installed; `CODEPLO_COSIGN_IDENTITY` overrides it for a fork and `CODEPLO_REQUIRE_COSIGN=1` refuses a
  SHA-256-only downgrade.
- The final workflow step verifies an explicit 13-asset list and uploads it, rather than `dist/*`, so GoReleaser's
  metadata files are not presented as operator downloads. A workflow re-run replaces existing assets.
- `deploy/install.sh` verifies all seven architecture-specific payloads against `checksums.txt` before writing
  anything, then waits up to 30 s for `/api/v1/setup/state` to answer.
- `ci.yml` has a `go-arm64` job on `ubuntu-24.04-arm` (T66). CI run `42343047804` on `0178c65` is green on `go`,
  `go-arm64` and `web`.

## Toolchain

- **Node 22.22.2** is pinned in `web/.nvmrc` and read by `ci.yml` and `release.yml` via `node-version-file`.
  `jsdom@30.1.3` / `undici@8.12.2` / `vitest@5.0.5` do not load on Node 20. `web/package.json` and the lockfile
  declare the same `engines.node`, and CI fails loudly if `process.versions.node` differs from `.nvmrc`.
- `make gen-check` compares MD5s of `web/src/api/schema.d.ts`, `internal/api/stubs_gen.go` and
  `internal/api/oapigen/models.gen.go` before and after two generation runs. It caught real drift in CI; the
  regenerated files are committed. A change to `api/openapi.yaml` requires `make gen`.

## Extensions / MCP

- No OpenAPI routes (see `docs/DECISIONS.md` T53).
- CLI: `codeplo extensions …` and `codeplo mcp check [--json]`.
- Feature flags: `extensions.outbound`, `mcp.server`; the `mcp` module row must also be enabled.
- Environment variables: `CODEPLO_MCP_SOCKET`, `CODEPLO_MCP_TOKEN` (see `docs/ENV.md`). The token is never logged
  or printed by the CLI check.

## Still owed before calling the host matrix complete

T44–T52 host matrix, T62 live-mode frontend pass against a real daemon, and a real tagged `release.yml` run with
cosign verification end to end. See `docs/site/verification-matrix.md`.
