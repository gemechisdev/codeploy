# Verification matrix — hardware / runner-gated work

Status of this document: **mixed — see each row**. This file was originally written when no test suite had been
run; that is no longer true. Local gates, the generated-drift check and GitHub Actions have all executed since, and
two CI failures were found and fixed. CI run `42343047804` on `0178c65` is now green on `go`, `go-arm64` and `web`.
Rows that need a VM, a Docker daemon, DNS, a public domain, a GitHub App installation or a live daemon remain
**NOT RUN** and are labelled as such; nothing here claims a host measurement that did not happen.

What has actually been executed (2026-11-07, Windows dev host unless noted):
`go build ./...`, `go vet ./...`, `go test ./...`, `make gen-check`, `make migrate-check`'s
`TestMigrateAllToV8`, the P4–P7 real-router suite in `internal/api/handlers_phase_test.go`, the full frontend
gate chain (`lint`, live-mode `build`, `check:mock-free`, `check:external`, `check:size`, `npm test -- --run` —
9 files / 94 tests), and `codeplo mcp check --json`. GitHub Actions has also run: the generated-drift job and the
frontend vitest job both failed, and both were root-caused and fixed (`docs/DECISIONS.md`, "Finalization pass"); CI
run `42343047804` on `0178c65` then passed all three jobs.

Sources: `FUTURE_TASKS.md` (Groups C/D/E), `PROGRESS.md`, `docs/HANDOFF_NOTES.md`. Each section names the missing
machine/gate, then concrete commands and steps taken from repo artefacts.

## T43 — full race tree

Gate: the Go race tree needs a working C compiler; on this host that is
`CC="C:/tools/winlibs/mingw64/bin/gcc.exe"`.

| Step / command (from repo) | Status |
|---|---|
| `CC="C:/tools/winlibs/mingw64/bin/gcc.exe" go test -race -count=1 ./...` | **RAN, FAILED, FIXED, RE-RUN GREEN (2026-11-07)** — first run failed `TestDockerConfigIsEphemeralAndPrivate` because Windows reports `0777`/`0666` for `MkdirTemp`/`WriteFile`. Assertions are now `runtime.GOOS != "windows"` for POSIX bits and platform-independent for secret sealing. Re-run: every package `ok`, `RACE=0` (`internal/api` 343.7 s) |
| `.github/workflows/ci.yml` `go test -race ./...` on a runner | **CONFIRMED GREEN (2026-11-07)** — CI run `42343047804`'s `go` job passed the race tree, authz matrix, drift check and embedded-web build |

## T45 — P2b/P2c VM fault-injection matrix

Gate: disposable VMs with IPv4 and IPv6 and provider console / rescue access; Tier-2/Tier-3 applies stay plan-only
until the matrix passes (FUTURE_TASKS T45).

| Step / command (from repo) | Status |
|---|---|
| Kill the daemon after apply; confirm the dead-man revert still fires (`hardening_revert_window_s=300`, `internal/api/handlers_hardening_apply.go`) | NOT RUN |
| Reboot before confirm; confirm the previous state returns | NOT RUN |
| External scan on IPv4 and IPv6 showing only SSH/80/443; Docker's own rules untouched | NOT RUN |
| `sshd -t` failure refused; cloud-init drop-in ordering; fail2ban self-ban guarded (hardening preflight) | NOT RUN |
| `codeplo status --hardening` and `codeplo revert <changeId\|all> [--list]` (`cmd/codeplo/main.go`) | NOT RUN |
| Flip `hardening.apply_tier2` / `hardening.apply_tier3` from plan-only (`handlers_hardening_apply.go:456-462`) | NOT RUN |

## T46 — deploy engine against real Docker

Gate: a real Docker daemon at `/var/run/docker.sock` and the §3.8 fault injections.

| Step / command (from repo) | Status |
|---|---|
| `go test -tags spike ./test/spikes -run Docker -v` (`test/spikes/docker_spike_test.go` header) | NOT RUN |
| `CGO_ENABLED=0 GOOS=linux go test -c -tags spike -o bin/spiked_linux_amd64 ./test/spikes`, then `./bin/spiked_linux_amd64 -test.run SpikeD -test.v` (`test/spikes/p3_runtime_spike_test.go` header) | NOT RUN |
| Engine end-to-end: image deploy → health gate → HTTPS route → rollback to the retained digest (`internal/modules/deploy`) | NOT RUN |
| §3.8 faults: kill mid-deploy, reboot between start and switch, cancel mid-build, disk full, OOM | NOT RUN |

## T47 — PR preview lifecycle with real DNS

Gate: a real host with a wildcard A/AAAA record under `CODEPLO_PREVIEW_DOMAIN` and `git.preview_deploys` on
(`internal/store/migrations/0007_previews.sql`).

| Step / command (from repo) | Status |
|---|---|
| Set `CODEPLO_PREVIEW_DOMAIN`, enable `git.preview_deploys`; `codeplo previews check` (`cmd/codeplo/previews.go`, `docs/site/operator-guide.md`) | NOT RUN |
| Open a PR → preview up and reachable over HTTPS → push again → refresh keeps the URL | NOT RUN |
| Close the PR → containers, routes, `pr_previews` row and volume directory all gone (`internal/modules/previews/teardown.go`) | NOT RUN |
| Expire-by-TTL and the parent-deleted paths via `Sweep` (`internal/modules/previews/sweep.go`) | NOT RUN |

## T48 — panel HTTPS on a real public domain (P2a unmet criterion)

Gate: a public domain pointed at the host; the SSH tunnel path must still work at the same time.

| Step / command (from repo) | Status |
|---|---|
| `go test -tags spike ./test/spikes -run CaddyEdge -v` (`test/spikes/caddy_edge_spike_test.go` header; TLS/ACME deliberately NOT exercised there) | NOT RUN |
| Managed-Caddy ACME issuance for the panel hostname (`docs/adr/0003-caddy-admin-api.md`, `docs/site/operator-guide.md`) | NOT RUN |
| Break-glass tunnel `ssh -L 7443:127.0.0.1:7443 <host>` still reaches the loopback listener | NOT RUN |

## T49 — CI on a runner

Gate: a GitHub Actions runner. `ci.yml` **has now executed** on a runner and reported two real failures; both were
fixed. CI run `42343047804` on `0178c65` is a confirmed all-green re-run on `go`, `go-arm64` and `web`.

| Step / command (from repo) | Status |
|---|---|
| `# OpenAPI vs generated drift-check` job (`make gen-check` + `git diff --exit-code`) | **RAN, FAILED, FIXED, RE-RUN GREEN** — committed `schema.d.ts` / `models.gen.go` were stale after `67b8e6e`; regenerated, and `make gen-check` now passes locally and in the green CI run |
| Frontend `web` job (`npm test -- --run`) | **RAN, FAILED, FIXED, RE-RUN GREEN** — Node 20 vs `undici`/`jsdom` engine mismatch killed all 9 vitest forks workers; pinned Node 22.22.2 via `web/.nvmrc` + `node-version-file` + `engines.node` + a CI toolchain guard |
| `go` job: `make lint`, `go test -race ./...`, authz matrix, `make build` | **CONFIRMED GREEN** — run `42343047804` |
| `go-arm64` job (`go test ./...` + `GOARCH=arm64 go build ./...`) (T66) | **CONFIRMED GREEN** — run `42343047804` on `ubuntu-24.04-arm` |
| `workflow_dispatch` `.github/workflows/memory-budget.yml` (idle RSS, report-only) | NOT RUN |
| `.github/workflows/release.yml` — GoReleaser + cosign keyless signing on a real tag | NOT RUN — the first tag is being prepared; see `docs/RELEASE_NOTES.md` |

## T50 — Profile B and C measurements

Gate: the 1 vCPU/2 GB and 2 vCPU/4 GB reference VMs; recorded, not guessed (FUTURE_TASKS T50).

| Step / command (from repo) | Status |
|---|---|
| On each VM: idle RSS via `awk '/VmRSS/ {print $2}' /proc/$pid/status` (`.github/workflows/memory-budget.yml`; Profile A budget = plan §1.4) | NOT RUN |
| Record total host memory, CPU, latency and recovery for Profile B (1 vCPU/2 GB) and C (2 vCPU/4 GB) | NOT RUN |

## T51 — GH Actions deploy workflow

Gate: a test repository and a panel deploy hook; `deploy/github-actions/codeplo-deploy.yml` has never run.

| Step / command (from repo) | Status |
|---|---|
| Call the `workflow_call` with `image`/`tag` and the `CODEPLO_DEPLOY_URL`, `REGISTRY_USERNAME`, `REGISTRY_PASSWORD` secrets (`deploy/github-actions/codeplo-deploy.yml` header) | NOT RUN |
| Build → push → `curl --fail-with-body` the deploy hook; honour `[skip deploy]`; never echo the token (no `set -x`, body built with `jq`) | NOT RUN |
| Per-image `concurrency: group: codeplo-deploy-${{ inputs.image }}` is exercised | NOT RUN |

## T52 — GitHub App install flow

Gate: a real GitHub App installation and a private repository.

| Step / command (from repo) | Status |
|---|---|
| `codeplo github-app check [--repo URL]` and `codeplo github-app repos` (`cmd/codeplo/githubapp.go`) | NOT RUN |
| Private repo clones via installation token only when the resource carries no credential of its own (`internal/modules/git/providers/githubapp.go`, `DecidePullRequest`) | NOT RUN |
| Delivery signed with the App secret verifies for a repository the panel never configured; commit status under one context on redeploy | NOT RUN |

## T62 — live-mode frontend pass

Gate: a **running daemon** to click through P4–P7 against. The static/unit gate chain has passed locally; that is
not the same thing as a live-mode pass, and this row does not claim it is.

| Step / command (from repo) | Status |
|---|---|
| `cd web && npm run lint`, `VITE_API_MODE=live npm run build`, `check:mock-free`, `check:external`, `check:size`, `npm test -- --run` | **PASS (local, 2026-11-07)** — 9 test files / 94 tests; also part of `make test` now (T69) |
| `VITE_API_MODE=live` against a real daemon: projects/resources/deployments, Source tab, monitoring charts, notifications, backups + restore wizard, templates, terminal and logs | **NOT RUN** — needs a live systemd host; WS/SSE and step-up flows are still unverified end to end |

## T71 — `make gen` idempotence in CI

| Step / command (from repo) | Status |
|---|---|
| `.github/workflows/ci.yml` → `make gen-check` (md5 of `schema.d.ts`, `stubs_gen.go`, `models.gen.go` before/after two generation runs) | **RAN (CI), FAILED, FIXED** — caught real drift; regenerated files committed |
| `make gen-check` locally | **PASS** — `gen: idempotent and no drift (web/src/api/schema.d.ts internal/api/stubs_gen.go internal/api/oapigen/models.gen.go)` |
| A green CI re-run of the drift job after the fix | **CONFIRMED GREEN** — run `42343047804`'s `go` job passed `make gen-check` and `git diff --exit-code` |

## T44 — Spike E: host PTY on real systemd

Gate: a real systemd host with the privileged `codeplo-pty@.service` helper and an owner session.
`test/spikes/p3_hostpty_spike_test.go` is `//go:build spike` and `tmp/setup_spike_e.sh` is explicitly an unrun
artefact. `TestTerminalAndRunning` in `internal/modules/terminal` is a unit test, not a host PTY run.

| Step / command (from repo) | Status |
|---|---|
| `tmp/setup_spike_e.sh` on a systemd host, then the spike test with `-tags spike` | NOT RUN |
| Root shell via step-up ticket, PTY resize, audit row, session teardown | NOT RUN |

## T70 — leftover WSL fixture state (NOT RUN)

`FUTURE_TASKS.md` T70: clean up state left on the WSL host by previous sessions (`/usr/bin/caddy` copy,
`/root/spikes`, `/root/spikeC.log`, `/root/codeplo-fixture`) **or** document it as owned by the fixtures.
`PROGRESS.md:234` already records these as "Left on WSL from the P2/P3 sessions (owned by that work, not the
repo)". Ownership is therefore explicit: they are session residue, **not** owned by any committed fixture in this
repo — no `deploy/`, `test/spikes/` or `scripts/` artefact declares them, and no cleanup script exists. The spike
headers only reference `/root` for logs (`test/spikes/p3_hostpty_spike_test.go` runs `/root/spikee`; HANDOFF_NOTES
§2 says to put logs under `/root`). T70 remains open: either remove the four paths on the WSL host or add a
committed fixture manifest that owns them. Neither the cleanup nor that decision was executed here — status NOT RUN.
