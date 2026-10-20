# TEST REPORT — codeplo backend, P0 / P1 / P2a tester pass

Date: 2026-10-20. Scope: verify the written-but-never-run tree against the P0/P1/P2a
test mandate: build, tests, authz matrix, mock-vs-implementation diff, security tests,
binary behaviours, frontend contract run, RSS, spikes, installer, exit criteria.
Priorities: plan > `api/openapi.yaml` > mock > frontend. Fixes were the smallest that
make the verified behaviour correct; no test was weakened to pass.

## 1. Environment

| Component | Value / notes |
|---|---|
| Host | Windows 10/11 (10.0.26300), Git Bash; user commits manually |
| Go | 1.27.1 windows/amd64 (`go.mod` requires `go 1.22`) |
| Node | 20.20.2 (npm ci / vite); 22.23.3 for vitest (undici needs ≥22.15 for zstd); vitest run with `--fileParallelism=false` on this host |
| Python | 3.14.7 + PyYAML (`scripts/gen_stubs.py`) |
| make | GNU Make 3.81 (GnuWin32) |
| C compiler (race detector) | WinLibs MinGW-W64 gcc 16.2.0 (`C:/tools/winlibs/mingw64`) via `CC` — the earlier `C:\tools\llvm-mingw` install is broken (missing headers) and was not used |
| Linux lab | WSL2 Ubuntu (systemd, kernel 6.6.87.2-microsoft-standard-WSL2), x86_64 |
| Docker | docker.io 29.1.3, storage driver overlayfs, daemon active in WSL; spike runs as root over `/var/run/docker.sock` |
| Caddy | v2.11.7 at `/home/hp/bin/caddy` (spike A) |
| sqlite3 CLI | Android SDK platform-tools build, used for the tamper injection |

Race-detector caveat: `-race` runs on Windows only through the WinLibs gcc above; all
`go test -race` results below were produced that way. The spikes were cross-compiled
`GOOS=linux GOARCH=amd64` (`go test -c -tags spike`) and executed inside WSL, because
Docker and Caddy are Linux-only on this machine.

## 2. Mandate steps

| # | Step | Result | Evidence / notes |
|---|---|---|---|
| 1 | Toolchain: build + vet + gofmt | PASS | `go build ./...`, `go vet ./...` (also `-tags spike`), `gofmt -l .` empty after `go mod tidy` (go.sum created) |
| 2 | `make gen` works and is idempotent | FAIL(fixed) → PASS | Two generator defects (oapi-codegen pin, output path) + CRLF generator defect; `make gen` twice → no diff; models committed |
| 3 | `go test -race ./...` | FAIL(fixed) → PASS | All packages green; two test-only fixes (invalid role enum; Windows path in passwd fixture) — code was right, tests were wrong guesses |
| 4 | Authz matrix + route/permission coverage | PASS | `test/authz/matrix_test.go`; NEW `test/authz/routes_test.go` proves Go router route set == OpenAPI path/method set exactly (110 routes) and each route's `Perm` == its `x-permission` |
| 5 | Mock-vs-implementation diff, P1 core endpoints | FAIL(fixed) → PASS | Full difference table in `docs/DECISIONS.md` ("Tester pass"); 7 code/spec defects fixed, remainder recorded as NOTED (deliberate or Go-stricter); frontend `tsc` clean, vitest 94/94 |
| 6 | Security tests | PASS | NEW `internal/api/security_test.go` (12 tests) + `internal/config` loopback test, all under `-race`; see §5 |
| 7 | Binary behaviours | PASS | `make build` → `bin/codeplod` + `bin/codeplo`; one-time setup token printed once; `setup-token` reissues; `doctor` / `doctor --verify-audit` pass clean; tampered row → exit 1 with `first_invalid_seq=2`; `reset-password` (stdin, min 12, revokes sessions) and `disable-2fa` verified against a real DB |
| 8 | Frontend contract run (live mode) | PASS (curl-driven) | `scripts/contract-test.sh` replays all 17 frontend calls against a live daemon; every expected status matched; vite `VITE_API_MODE=live` proxy verified. **Not browser-driven** — the wizard's operator step needs a Linux `/etc/passwd` sudo user, absent on this Windows dev host (recorded; `/setup/complete` does not require an operator). No check skipped and reported as pass; this row is the curl fallback, documented in `docs/contract-test.md` |
| 9 | Idle RSS vs Profile A budget | FAIL(open) — UNMET | Fresh daemon, full setup+login, idle 60 s → **RSS 78.8 MB** (WorkingSet64 82,591,744 B) vs budget **≤ 50 MB** → over by 28.8 MB. Reported, not tuned: the plan's Profile A is a Linux CI/VM-lab number and this is a Windows host measurement |
| 10 | P2a audit-slice diff | NOT RUN | Not implemented in this tree: `POST /hardening/audit` is the generated stub only (404/501 `module_disabled`); no audit-mode filesystem/nftables behaviour exists to diff. Recorded as not-run, not pass |
| 11 | Spike A (Caddy) | FAIL(open) — flaky | See §6.1: zero-drop criterion not reliably met — runs alternate between 0 failures (incl. 2082/0 in the final verification run) and 1–2 resets (216/2 in this pass's earlier run) |
| 11 | Spike B (Docker) | FAIL(fixed) → **PASS** | See §6.2: fixture defect fixed twice (CHOWN+NET_BIND_SERVICE, then SETGID+SETUID); final re-run with the gofmt-clean source: `--- PASS: TestDockerPullCreateHealthSwap (16.82s)` |
| 12 | Installer on clean VM | PASS | All 4 cases in WSL Ubuntu (systemd): HTTPS-served fake release installs and service is active as `codeplo` over the tunnel endpoint; tampered binary, tampered unit file, and missing checksum entry each abort. Fixture gotcha: `checksums.txt` must be GNU two-space LF format |

## 3. Defects found and fixed (file → fix)

| # | Defect | File | Fix |
|---|---|---|---|
| 1 | oapi-codegen 2.4.1 cannot parse this OpenAPI 3.1 spec (`oneOf [string,null]`, issue #373) | `internal/api/oapigen/doc.go` | pin raised to v2.8.0 |
| 2 | Generated output path nested under the package dir (double path) | `api/oapi-codegen.yaml` | `output: models.gen.go` (relative to package) |
| 3 | Stub generator emitted CRLF on Windows → gofmt + CI drift phantom diffs | `scripts/gen_stubs.py` | `open(..., 'w', newline='\n')` |
| 4 | Test used role `developer` (not in contract enum) | `internal/api/api_test.go` | → `deployer` |
| 5 | Test fixture embedded a Windows absolute path (contains `:`) into a colon-delimited passwd line | `internal/platform/operators_test.go` | relative home dir + `chdir`; assertions unchanged |
| 6 | `/system/status` missing `net_rx_kbps`/`net_tx_kbps` that `HostMetrics` and the mock always send | `internal/api/handlers_admin.go` | stubbed `0` (real values arrive with the P6 monitor module) |
| 7 | Settings seed 120/5 vs mock 300/2; stray `build_concurrency` key | `internal/store/migrations/0001_core.sql` | seeds aligned; key dropped |
| 8 | Settings audit action/target/meta diverged from mock; no bus event | `internal/api/handlers_admin.go` | `settings.updated`, target `settings`/`server`, meta `{keys,values}`, post-commit `flag.changed {key:settings}` |
| 9 | `PATCH /feature-flags` published `audit.appended` inside the tx (pre-commit) | `internal/api/handlers_admin.go` | publish after commit, one per row |
| 10 | SSE had no `id:` lines, no `server_now`, no resume; OpenAPI described an envelope neither mock nor frontend use | `internal/events/bus.go`, `internal/api/handlers_admin.go`, `api/openapi.yaml` | monotonic counter IDs, 500-event ring + `?last_event_id=` replay, `server_now` on non-metrics payloads; spec amended to the real wire format |
| 11 | OpenAPI under-documented: 401 on post-`/setup/admin` setup routes, 501 on panel-domain/operator, 409 on `PATCH /modules/{key}`, `limit`/`cursor` on `/users`+`/tokens` | `api/openapi.yaml`, `web/docs/CONTRACT.md` | spec amended (new `E501`), amendments listed in CONTRACT.md |
| 12 | Spike B fixture: `CapDrop:["ALL"]` alone (and later CHOWN+NET_BIND_SERVICE) cannot run `nginx:alpine` — workers die `setgid(101) EPERM`, container never healthy | `test/spikes/docker_spike_test.go` | `CapAdd: ["CHOWN","NET_BIND_SERVICE","SETGID","SETUID"]` — minimal set the probe image needs; isolation posture under test (CapDrop ALL, memory + pids limits) unchanged; PASS on re-run |
| 13 | Installer checksum verification only accepts GNU two-space, LF `checksums.txt` lines (`grep -E " (name)$"`) | `deploy/install.sh` (documented, behaviour kept) | release pipeline must emit GNU format; recorded in DECISIONS.md |

Not defects (checked and left alone): 404-vs-405 for wrong methods on known paths
(mock cosmetic, documented); keyset cursors vs the mock's offsets (deliberate,
DECISIONS); Go being stricter than the mock (role enum, token `expires_at`, seq-gap
audit); `0.1.0-dev/none/unknown` version defaults outside ldflags; the Windows-host
RSS number vs the Linux-lab budget.

## 4. Mock-vs-implementation differences

Full disposition table (36 rows: FIXED / NOTED) in `docs/DECISIONS.md`, "Tester pass"
section. Fixed this pass: `/system/status` net fields, module + settings seed data,
settings audit shape + bus event, `/feature-flags` publish timing, `/events` wire
format (code aligned to mock, OpenAPI amended), plus contract documentation fixes
(401/501/409/cursor params). The NOTED rows are deliberate designs (keyset cursors,
token-bucket rate limiting, real TOTP/recovery codes, SHA-256 token storage, real DNS
lookup, `/etc/passwd` scan) or Go-stricter-than-mock behaviour matching OpenAPI.

## 5. Security tests added

`internal/api/security_test.go` (12 tests, all pass under `-race`): setup-token 1 h
TTL; second `/setup/admin` → 403; API-token lifecycle (`cpl_` + 40 hex, prefix
displayed, SHA-256-only storage, secret never in list, non-GET without CSRF refused
via Bearer); session non-GET requires CSRF; login timing anonymity (<2 s delta between
unknown-user and wrong-password); audit append triggers exist; deleted-row and
broken-link tamper → `first_invalid_seq`; handler+audit same-transaction atomicity
(injected trigger failure → neither commits); owner gate (crafted session: `me`/`logout`
allowed, rest 403 until TOTP); recovery-code single use; SSE id/server_now/resume
replay. `internal/config` gained `TestLoadYieldsOnlyLoopbackOrFails` (non-loopback bind
refused at load).

## 6. Spikes

### 6.1 Spike A — Caddy admin-API add/remove route with zero drops: FAIL (flaky)

Caddy v2.11.7, WSL Ubuntu, repo test `test/spikes/caddy_spike_test.go` (untouched),
cross-compiled and run as root. The result is **run-to-run flaky**, which is exactly
why the criterion cannot be certified:

| Run (this pass unless noted) | Requests | Failures | Verdict |
|---|---:|---:|---|
| 1st re-run | 216 | 2 (`first_error=EOF`) | FAIL |
| Final verification run (gofmt-clean binary) | 2082 | 0 | PASS |
| Earlier runs by the previous agent (2) | — | 0 | PASS |
| Earlier runs by the previous agent | 2697, 1638 | 1–2 (`connection reset by peer`) | FAIL |

So v2.11.7 both passes and fails the same strict test on the same host: the
zero-drop criterion is **not reliably met** → recorded UNMET, and the spike was not
loosened.

Investigation matrix (throwaway probe, not in the repo): keep-alive ON + fast reloads
→ 0 failures; keep-alive OFF → 57–71 EOF failures; `grace_period` (JSON
`apps.http.grace_period`; Caddyfile: global `{}` options block — root-level JSON
`grace_period` is an unknown field) at 5 s had no clear effect; 1 s gap between
reloads → 2–8 failures per ~176 k requests (≈0.004 %).
Findings: keep-alive clients (the product's real traffic) ride the zero-downtime
swap; the spike's fresh-connection-per-request hammer is what races the swap; pacing
reloads reduces failures to noise. Recorded in DECISIONS.md "Spike findings".

### 6.2 Spike B — Docker pull/create/health-wait/swap: PASS after fixture fix

Docker 29.1.3 / overlayfs in WSL. First runs failed in `waitHealthy`: with
`CapDrop:["ALL"]` (and `HostConfig` memory + pids limits) `nginx:alpine` cannot run —
its entrypoint needs CHOWN, and, as this pass proved with `docker logs`, every worker
then died `setgid(101) failed (EPERM)` because the master cannot drop privileges
without SETGID/SETUID; the master exits and nothing ever listens, so the healthcheck
could not pass regardless of the daemon. A no-CapDrop control reaches `healthy` in
~6 s, proving the environment's healthcheck plumbing works.
Fixture fixed in `test/spikes/docker_spike_test.go`: `CapAdd:
["CHOWN","NET_BIND_SERVICE","SETGID","SETUID"]` — the minimum the probe image needs;
the isolation posture under test (drop ALL, re-add named caps, 128 MiB memory,
pids-limit 128, blue/green healthy-before-stop) is unchanged. Re-runs:
`--- PASS: TestDockerPullCreateHealthSwap (19.91s)`, then re-verified against the
gofmt-clean source `(16.82s)` — pull, create+start, health-wait
to healthy, second (new) container healthy **before** the old one stopped, swap and
cleanup all over the Docker Engine API unix socket with no shell and no SDK.

## 7. Memory

Idle RSS after a fresh setup + login, 60 s idle: **78.8 MB** (82,591,744 B,
PowerShell `Get-Process` WorkingSet64) vs Profile A budget `codeplod ≤ 50 MB` →
**over budget, exit criterion unmet**. Not tuned: measured on a Windows host while the
plan's Profile A budget is a Linux CI/VM-lab number; the honest report is the number,
re-measurement belongs on a Linux runner.

## 8. Exit criteria (plan §6.2)

### P0

| Criterion | Status | Evidence |
|---|---|---|
| Threat model and ADRs merged | MET on paper | `docs/threat-model.md`, ADRs 0001–0005 exist; "merged to main" not applicable locally (single repo, user commits) |
| Caddy spike adds/removes a route with no dropped connections | **UNMET** | §6.1 — flaky: same binary and host alternates between 0 failures (0/2082 in the final run) and 1–2 resets (2/216 this pass; 1–2 at 2697 and 1638 previously); spike kept strict; mitigations identified (keep-alive clients unaffected; pace reloads) |
| Docker spike start → health-wait → swap | **MET** | §6.2 — PASS 19.91 s after fixing a wrong probe-image fixture (not a test weakening) |
| CI pipeline green (lint, test, build, memory-budget scaffolded) | **NOT RUN** | `ci.yml`, `release.yml`, `memory-budget.yml` exist and the gates pass locally, but no CI runner ever executed them |

### P1

| Criterion | Status | Evidence |
|---|---|---|
| `install.sh` yields a running service on a clean VM | **MET** | §2 step 12 — WSL Ubuntu systemd: installs, service active, runs as `codeplo`, tunnel endpoint answers, setup token works; 3 tamper cases abort correctly |
| Owner opens the panel through the SSH tunnel with a one-time token and logs in with TOTP | **MET (curl-driven)** | §2 step 8 — full setup wizard + TOTP enrollment + login replayed via curl over the loopback/tunnel path; **browser not driven** (Windows host cannot provide the Linux operator step); honest gap recorded |
| Every API route has a declared permission and the authz-matrix test passes | **MET** | §2 step 4 — matrix + new route-set==OpenAPI equality test, 110 routes exact |
| Audit log hash-chained; `doctor --verify-audit` passes and detects a tampered row | **MET** | §2 step 7 — clean DB verify passes; sqlite3-injected flip → doctor exit 1 `first_invalid_seq=2`; triggers block UPDATE/DELETE |
| Idle RSS ≤ budget (Profile A) | **UNMET** | §7 — 78.8 MB > 50 MB, Windows host; reported, not tuned |
| OpenAPI → Go/TS generation part of CI | **PARTIAL** | `make gen` (oapi-codegen models + TS schema + stubs) works and is idempotent; drift check wired in `ci.yml`; never executed on a runner |

### P2a (audit slice in scope)

| Criterion | Status | Evidence |
|---|---|---|
| Audit mode makes zero changes (filesystem/ruleset diff) | **NOT RUN** | Step 10 — not implemented in this tree; `POST /hardening/audit` is a stub (404/501 `module_disabled`); nothing to diff |
| Remaining P2a criteria (HTTPS domain reachability, Tier 1 fixes, fail2ban ignore list, operator selectable) | **NOT RUN** | P2a modules are not implemented at P1 skeleton stage; out of this pass's scope |

## 9. Final gate (run on this host after all edits)

| Command | Result |
|---|---|
| `CC=<WinLibs gcc> go test -race -count=1 ./...` | Pass — `internal/api` 64.2 s, `audit`, `authn`, `authz`, `config`, `crypto`, `events`, `platform`, `test/authz` all `ok`; `cmd/*`, `app`, `jobs`, `modules`, `store`, `webui`, `oapigen` have no test files |
| `go vet -tags spike ./...` | Pass (spikes compile) |
| `gofmt -l .` | Empty |
| `make gen` ×3, md5 of `models.gen.go` / `stubs_gen.go` / `schema.d.ts` before and after | Identical — generation is idempotent |
| `go build ./...` | Pass |
| Spike binaries rebuilt `GOOS=linux` and re-run in WSL | Spike B PASS 16.82 s; Spike A PASS 0/2082 (flaky, see §6.1) |
| Cleanup | spike containers removed by label; no `caddy` or `spikes.test` processes left in WSL; no `codeplod`/vite left on Windows; `C:\var\lib\codeplo` absent |

## 10. Honest gaps / limitations

- No browser was driven (curl fallback for the contract run; the UI was verified via
  `tsc` + vitest 94/94 + the live proxy check, not visual clicks).
- No CI runner executed any pipeline; all gates ran on this host or in WSL.
- RSS and the spikes were measured on WSL2/Windows, not the plan's clean Linux VM lab.
- P2a slice: not implemented → reported NOT RUN everywhere it appears.
- Spike A flakiness was recorded, not fixed — the repo test stays strict by mandate.
