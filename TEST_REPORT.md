# TEST REPORT — codeplo backend, P0 / P1 / P2a tester pass

> **Status note (2026-11-07):** this is a historical 2026-10-20 report of a deliberately scoped tester pass. Its
> "CI pipeline NOT RUN" row and P2a NOT RUN rows describe that moment. CI, the full race tree, the generated-drift
> check and the frontend gates have since been executed; see `PROGRESS.md` "Finalization pass (2026-11-07)" and
> `docs/site/verification-matrix.md`. Nothing below has been silently upgraded to a pass.

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
| 12 | Installer on clean VM | ~~PASS~~ → **REOPENED and FAIL(fixed)** | This row was **wrong as reported**. The four abort cases did pass, but the "clean install" case never asserted that the services *ran* — `systemctl enable --now` returns 0 for a crash-looping unit and the wait loop only slept. Re-running the fixture in P2 surfaced three real boot-time defects (see §11): Caddy exited 1 on an empty ACME placeholder in the bootstrap, `codeplod` died on `codeplo.sock: bind: permission denied` because the root-run Caddy unit re-owned `/var/lib/codeplo`, and the installer still reported success. All three fixed; the installer now asserts panel readiness and `exit 1`s otherwise |

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

## 11. Phase 2 addendum (edge + hardening, 2026-10-20)

Everything in §2 was a P0/P1 verdict. Phase 2 added the edge module, the hardening engine with
apply/confirm/revert, the daemon-independent dead-man switch, and the Caddy deployment units.

### 11.1 Gate, re-run after the merged Phase 2 tree

| Command | Result |
|---|---|
| `gofmt -l .` | Empty |
| `go build ./...` | Pass |
| `go vet ./...`, `go vet -tags spike ./...` | Pass |
| `CC=<WinLibs gcc> go test -race -count=1 ./...` | Pass — 11 packages `ok` (`api` 90.0 s, `modules/edge` 4.2 s, `modules/hardening` 4.4 s, `platform`, `test/authz`, core), 0 FAIL, **0 skipped tests** |
| `make gen` chain (`npm run gen:api`, `gen_stubs.py`, `go generate`) run twice, md5 compared | Identical — no drift |
| Frontend `npm run lint` (tsc + guards), `npm run build`, `check:mock-free`, `check:external`, `check:size`, `npm test` | Pass — guards 104 files/8 rules, 94/94 vitest (Node 22.23.3, `--fileParallelism=false`) |
| Spike C (`TestCaddyEdgeRenderLoadNoDrift`, real Caddy v2.11.7 in WSL) | **PASS 3.54 s** |

### 11.2 Defects Phase 2 verification found, and their status

| # | Defect | Where | Status |
|---|---|---|---|
| 1 | Installer reported success on a crash-looping panel (no readiness assertion) | `deploy/install.sh` | FIXED — readiness loop now `exit 1`s with `journalctl` guidance |
| 2 | Bootstrap Caddy config carried `{env.CODEPLO_ACME_EMAIL}`; Caddy exited 1 at boot ("evaluated placeholder is empty") | `deploy/caddy/bootstrap.json` | FIXED — bootstrap is admin-only; the reconciler injects the `tls` policy when the operator sets the address |
| 3 | Root-run Caddy unit declared `StateDirectory=codeplo`, re-owning `/var/lib/codeplo` → panel fatal `codeplo.sock: bind: permission denied` | `deploy/caddy/codeplo-caddy.service` | FIXED — dropped, `ReadWritePaths` only |
| 4 | Documented `sh install.sh` usage could never run (`set -o pipefail` + arrays) | `deploy/install.sh` | FIXED — dash-visible guard, usage corrected to `bash install.sh` |
| 5 | `updateHardeningChange` dropped the `WHERE id=?` bind → confirm/revert 500 (`missing argument with index 2`) and silent no-op updates | `internal/api/handlers_hardening_apply.go` | FIXED — helper appends the id and refuses a 0-row update |
| 6 | fail2ban `ignoreip = a b` parsed the `=` as an address; a jail that would ban its own operator passed verification | `internal/modules/hardening/drivers.go` | FIXED — separator handling + operator-IP refusal |
| 7 | fail2ban Verify reported jail contents before daemon state (claimed protection while fail2ban was stopped) | `internal/modules/hardening/drivers.go` | FIXED — daemon state first |
| 8 | Apply recorded the live-state revert *after* mutating; confirm disarmed the timer before persisting boot files; revert ignored live state | `internal/api/handlers_hardening_apply.go` | FIXED — ordering now snapshot → revert-plan → mutate; persist → disarm → DB; revert restores files **then** kernel state, idempotent when the table is already gone |
| 9 | All four `web/scripts/*.mjs` used `new URL().pathname`, which breaks on Windows (`C:\C:\Users\...`); `lint-guards.mjs` also matched `mock/` prefixes against backslash `relative()` output, so its exemptions were inoperative | `web/scripts/*.mjs` | FIXED — `fileURLToPath` + separator normalization. The guards had never actually run on this host before |
| 10 | Spike C failed with 200-empty-body probes because the installer fixture left `codeplo-caddy` **enabled**, holding `:80`/`:443` | test environment | FIXED (harness) — fixture cleans up; recorded because a running service is shared state |

### 11.3 Phase 2 runtime evidence (WSL Ubuntu, systemd, real Caddy)

Installer run against an HTTPS-served fake release: **15/15 assertions** — installer exits 0;
`codeplo-caddy` active; `codeplod` active 20 s later with `NRestarts=0`; admin socket present
and `660 root codeplo`; `/var/lib/codeplo` is `700 codeplo codeplo`; the unprivileged `codeplo`
user reads the admin API; the running config contains the reconciler's `:80`/`:443` servers
(which the on-disk bootstrap does not have, proving the push happened); panel answers on both
loopback TCP and its unix socket; installer log, panel journal and Caddy journal contain no
error, panic, placeholder or permission-denied lines; `/etc/default/codeplod` template created.
Plus the abort cases: tampered binary, tampered Caddy unit, missing checksum entry, missing
Caddy prerequisite — each exits non-zero before writing anything.

### 11.4 What Phase 2 did NOT verify (open, by mandate not by choice)

- **VM fault-injection matrix (§3.10.8)**: kill the daemon after apply and let the systemd timer
  revert; reboot before confirm; external port scan on IPv4+IPv6; fail2ban self-ban drill.
  The *semantics* are asserted at the API seam (`systemd-run` argv recorded for Tier ≥ 2 and
  absent for Tier 1; boot persistence ordered before disarm; live-table drop on revert), but no
  disposable VM has run them. P2c apply therefore stays default-off, as the plan requires.
- **Automatic HTTPS on a real public domain** — needs DNS to this host; only Caddy's internal
  issuer path was exercised.
- CI never executed on a runner; RSS still 78.8 MB vs the 50 MB Profile A budget (Windows number).
- No signed release: `make dist` produces and checksums the artefact set, cosign path untested.

## 12. Phase 3 addendum (terminal & logs, 2026-10-21)

### 12.1 Gates run on this host

- `gofmt -l .` — empty.
- `go build ./...` (Windows) and `CGO_ENABLED=0 GOOS=linux go build ./...` + `GOOS=linux go vet ./...` — clean.
  The Linux vet matters: the PTY helper is `//go:build linux` and never compiles in the Windows gate.
- `CC=gcc go test -race -count=1 ./...` — **13 packages ok, 0 FAIL, 0 skips**; 69 test functions pass in
  `internal/api`, `internal/modules/terminal` and `internal/modules/runtime`.
- `go test -race -count=6 -run TestTerminal ./internal/api/` — stable. The first version of the bridge teardown
  was flaky here (3 of 6 runs failed on Windows file cleanup) and that flake is what exposed defect 4 below.
- `test/authz/routes_test.go` — still proves the Go route set equals OpenAPI, now including the four terminal
  routes and `/resources/{id}/logs` as implemented rather than stubbed.

### 12.2 Defects found by testing, and their fixes

1. **The package did not compile**: `Server.moduleOn` was declared twice (`handlers_terminal.go` and
   `handlers_hardening.go`). Deleted the duplicate; the hardening copy is the one every module gate uses.
2. **A lost helper connection looked like a clean logout** (`terminal/host.go`): a failed frame read returned
   exit code 0, so the panel would report "process exited with code 0" for a shell whose socket broke mid-line.
   Now -1.
3. **An unbounded `Docker.Resize`** (`runtime/docker.go`): the runtime client deliberately has no overall
   timeout (a followed log stream is long-lived), so a resize riding on it could stall the browser's input
   goroutine behind a hung daemon forever. Now a 5 s deadline on that call alone.
4. **The bridge said goodbye after hanging up** (`api/handlers_terminal.go`): `coder/websocket` closes the raw
   socket the instant the context a read was issued on is cancelled, so the exit/error frame and the close frame
   written after `cancel()` never reached the browser — it saw an anonymous 1006 and no exit code. Found because
   the new idle-timeout test failed, then confirmed by reading the library's `setupReadTimeout`. Fixed by
   ordering the end: claim the reason once, write the reason in band, close the shell, close the connection with
   a bounded wait, cancel last; and by skipping both writes when the peer's read has already failed (that was
   what made teardown take seconds).
5. **An idle-killed shell was reported as a clean exit**: three goroutines raced to name the end reason, so the
   pump could overwrite "idle" with "exit". `claim` now lets the first reason win, and only that winner speaks.

### 12.3 What the P3 tests prove (and how)

Tickets: single use, expiry, wrong user/target/resource/Origin, unknown-id sweep, and — at the HTTP seam — that
a ticket minted for `https://panel.example` is refused from `https://evil.example` *and* leaves no session row.
Policy: operator-only authorisation, root needing the host flag file; frames: round trip and oversize rejection.
Runtime: demux of split/partial lines, backpressure stopping the reader, an absurd frame length being refused,
timestamp-less lines still being delivered, id validation before anything reaches a URL.
Handlers: the whole ticket rule set (409 `operator_missing`, 422s, 403 root disabled, 403 step-up), replay and
cross-user refusal, the per-user session limit, a host bridge that moves bytes both ways and ends the session
row when the tab closes, a container bridge that pushes keystrokes into the exec stdin and turns a text resize
frame into a tty resize, the idle timeout ending shell + row, audit rows per ticket and per session, and the log
stream's ids, `Last-Event-ID` resume with no tail replay, drop-oldest accounting and `event: end`.

### 12.4 What Phase 3 did NOT verify

- **Real PTY, real Docker, real systemd.** Every test above uses fakes at the port. Untested against a live
  host: the helper's `SO_PEERCRED` check under socket activation, whether the socket's mode actually lets the
  unprivileged `codeplo` daemon traverse `/run/codeplo-pty`, that closing a Docker attach ends the exec (the
  adapter relies on pty hang-up and never calls `/exec/{id}/kill`), and whether Docker accepts
  `AttachStderr:true` together with `Tty:true` (it may reject the combination).
- **The §6.2 P3 runtime matrix** — host shell as a real sudo operator, refusal with no operator, root off by
  default and step-up-gated when on, close-the-tab leaves no process, container shell, log tail/follow/resume and
  the dropped-line notice under a genuinely slow reader.
- **Installer end to end with seven artefacts.** `install.sh` and `make dist` agree in the tree; the WSL
  installer fixture still serves five and must be updated before that test can pass.
- **The frontend in live mode.** `TerminalPane` already matches the frame contract and the Vite proxy forwards
  WebSockets, but no browser has yet driven a real shell. Also unresolved by choice: OpenAPI marks
  `/terminal/ticket` `x-step-up: true` while the server (per the plan and the mock) requires step-up only for
  `as_root`; the UI must not start prompting for a code on every ordinary shell without a decision here.
- **Session-limit + audit in one transaction.** `Store.Open` and the audit write are two transactions; the
  handler fails closed if the audit write fails, but a crash between them can leave a session row with no audit
  event. Deferred deliberately, recorded in `docs/DECISIONS.md`.

## 13. Phase 3 runtime evidence — container runtime on real Docker (2026-10-21)

### 13.1 What was run

Spike D (`test/spikes/p3_runtime_spike_test.go`, `//go:build spike`) compiled with
`CGO_ENABLED=0 GOOS=linux go test -c -tags spike` and executed as root in WSL against a live daemon:
**Docker 29.1.3, API 1.52, MinAPI 1.44, containerd 2.2.1, runc 1.3.4, kernel 6.6.87.2-WSL2**. It drives the
production `runtime.Docker` type, not a reimplementation.

| Test | Result | Evidence |
|---|---|---|
| `TestSpikeDResolveAndLogs` | **PASS** | resolved `cpl-spike-d` by the `codeplo.resource` label; an unlabelled resource returns `ErrNoContainer`; 2 log lines demuxed with stdout and stderr separated; `since=now` replayed nothing |
| `TestSpikeDExecAttach` | **PASS** | daemon accepted `Tty` together with `AttachStderr`; `Resize(132,43)` then `stty size` inside the shell answered `43 132`; keystrokes and a marker round-tripped; after `Close()` the exit code arrived (0) and the container's process table held only `sleep 600` — no leftover shell |

### 13.2 The defect this run found (fixed, committed `3b41432`)

The adapter's `Close()` documented that hanging up the attach ends the exec because the pty closes and the
shell takes SIGHUP. **Measured false.** The three candidate mechanisms, side by side on the same daemon:

| Mechanism | Measured |
|---|---|
| close the attach socket | shell still running (still in `docker top`) after minutes |
| half-close the write side | still running — a tty exec has no stdin pipe for EOF |
| `POST /exec/{id}/kill` | `404 {"message":"page not found"}` at 1.24 / 1.41 / 1.52 / unversioned, while `POST /exec/{id}/resize` on the same prefix answers 200 → the route does not exist on this daemon, so the first fix was a silent no-op and the `404 = benign` switch hid it |
| in-band `^U` + `^D` `^D` | **exit 0, exec gone** |
| in-band `exit\r\n` | exit 0, exec gone |

Severity: every closed browser tab left a live shell running in the workload container — a security defect,
not a leak. Fix and reasoning: `internal/modules/runtime/docker.go` `ttyEOF`, and
`docs/DECISIONS.md` → "Docker exec teardown: measured, not assumed".

### 13.3 New deterministic test (no daemon required)

`internal/modules/runtime/docker_exec_test.go` runs a stub Docker Engine API server on a unix socket and
asserts the teardown contract: `Close()` writes exactly `"\x15\x04\x04"` to the attach, half-closes so the
daemon sees the session end, tolerates a daemon with no kill route, is idempotent, and the start body carries
`Detach:false` with `Tty:true`; non-tty execs are refused. Runs on Windows 11 (AF_UNIX) and Linux CI.

### 13.4 Gate after the change

`gofmt -l .` empty; `CGO_ENABLED=0 GOOS=linux go build ./...` and `go vet ./...` clean (plus
`go vet -tags spike`); `CC=... go test -race -count=1 ./...` → **13 packages ok, 0 FAIL, no skips**.

### 13.5 Still not verified (unchanged from 12.4, minus the Docker items now settled)

- **The host half of the §6.2 P3 matrix.** Spike E (`test/spikes/p3_hostpty_spike_test.go`) is written and
  compiles, and it asserts: socket/`DirectoryMode` traversal by the unprivileged service user, `SO_PEERCRED`
  refusal of a root peer, `Authorize` against real `/etc/passwd`+`/etc/group` (sudo member yes, non-sudo no,
  system accounts and injection-shaped names no), root only with `/etc/codeplo/allow-root`, the shell running
  as the target uid with its HOME and the resized tty, and close-the-socket killing the process group.
  **It has not been run** — no systemd socket activation has been exercised yet, so none of that is claimed.
- Installer end to end with seven artefacts; frontend in live mode; session-limit + audit in one transaction
  (all as recorded in 12.4).
