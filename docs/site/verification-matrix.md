# Verification matrix — hardware / runner-gated work

Status of this document: **UNRUN**. The operator asked us to skip running any test suite; every item below is a
deliberately unverified artefact. No command in this file has been executed, no VM, Docker daemon, DNS record, CI
runner, public domain or live frontend was touched, and no result is reported. "NOT RUN" is the only true status
for each row. Sources: `FUTURE_TASKS.md` (Groups C/D/E), `PROGRESS.md` ("Honest remaining" and Group E table),
`docs/HANDOFF_NOTES.md`. Each section names the missing machine/gate, then concrete commands and steps taken from
repo artefacts. All rows: NOT RUN.

## T43 — full race tree

Gate: the Go race tree needs a working C compiler. `docs/HANDOFF_NOTES.md` §1 says `go test -race` needs
`CC="C:/tools/winlibs/mingw64/bin/gcc.exe"` on this host and has not run since P3's gate.

| Step / command (from repo) | Status |
|---|---|
| `CC="C:/tools/winlibs/mingw64/bin/gcc.exe" go test -race -count=1 ./...` (FUTURE_TASKS T43) | NOT RUN |
| Fix everything the run reports; `.github/workflows/ci.yml` runs `go test -race ./...` on a runner | NOT RUN |

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

Gate: a GitHub Actions runner; `ci.yml`, `memory-budget.yml` and `release.yml` have never executed (FUTURE_TASKS T49).

| Step / command (from repo) | Status |
|---|---|
| Push / PR: `.github/workflows/ci.yml` → `make lint`, `go test -race ./...`, authz matrix, OpenAPI/generated drift (`make gen` + `git diff --exit-code`), `make build` | NOT RUN |
| `workflow_dispatch` `.github/workflows/memory-budget.yml` (idle RSS, report-only) | NOT RUN |
| `.github/workflows/release.yml` | NOT RUN |
| Fix whatever the first real run reports | NOT RUN |

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

Gate: a real daemon with `VITE_API_MODE=live`; everything P4–P7 built mock-only (FUTURE_TASKS T62, PROGRESS
"Honest remaining").

| Step / command (from repo) | Status |
|---|---|
| `VITE_API_MODE=live` against a real daemon | NOT RUN |
| `cd web && npm run lint`, `npm run build`, `check:mock-free`, `check:external`, `check:size`, `test` | NOT RUN |

## T70 — leftover WSL fixture state (NOT RUN)

`FUTURE_TASKS.md` T70: clean up state left on the WSL host by previous sessions (`/usr/bin/caddy` copy,
`/root/spikes`, `/root/spikeC.log`, `/root/codeplo-fixture`) **or** document it as owned by the fixtures.
`PROGRESS.md:234` already records these as "Left on WSL from the P2/P3 sessions (owned by that work, not the
repo)". Ownership is therefore explicit: they are session residue, **not** owned by any committed fixture in this
repo — no `deploy/`, `test/spikes/` or `scripts/` artefact declares them, and no cleanup script exists. The spike
headers only reference `/root` for logs (`test/spikes/p3_hostpty_spike_test.go` runs `/root/spikee`; HANDOFF_NOTES
§2 says to put logs under `/root`). T70 remains open: either remove the four paths on the WSL host or add a
committed fixture manifest that owns them. Neither the cleanup nor that decision was executed here — status NOT RUN.
