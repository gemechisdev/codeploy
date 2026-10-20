# HANDOFF — codeplo P0/P1/P2a tester pass (continue from here)

You are taking over an autonomous test-engineering task on `C:\Users\hp\projects\codeploy`
(the codeplo backend, Go + embedded TS frontend). A previous agent did most of the work;
this document is the complete state so you can finish it. Read `docs/DECISIONS.md`
("Tester pass" section), `docs/contract-test.md`, and `PROGRESS.md` first, then do the
**Remaining work** section, in order.

## Original mandate (abridged)

Verify P0 + P1 + the P2a audit slice of a repo that was written but **never compiled or run**.
Ground rules: plan > openapi.yaml > mock > frontend; smallest fixes; never weaken a test;
never report a skipped check as a pass (write "not run" + reason); record judgment calls in
`docs/DECISIONS.md`; deliver `TEST_REPORT.md` (environment, per-step PASS/FAIL(fixed)/FAIL(open)/
NOT-RUN table, defects with file+fix, mock-vs-impl differences, RSS number, P0/P1 exit criteria
from plan §6.2 marked met/unmet with evidence); update `PROGRESS.md`; leave the repo building.

## Environment (this host: Windows 10/11 + Git Bash; user commits manually with git)

| Tool | Where / notes |
|---|---|
| Go | 1.27.1 (`C:\Program Files\Go\bin\go`); go.mod says `go 1.22` — fine |
| Node | 20.20.2 = `C:\Users\hp\.config\herd\bin\v20.20.2` (npm ci / vite). 22.23.3 same dir (vitest; undici needs Node ≥22.15 for zstd). nvm not in bash; use `C:/Users/hp/.config/herd/bin/nvm/nvm.exe` |
| Python | 3.14.7 with PyYAML; sqlite3 CLI at `C:/Users/hp/AppData/Local/Android/Sdk/platform-tools/sqlite3` |
| make | GnuWin32 3.81 → add `"C:/Program Files (x86)/GnuWin32/bin"` to PATH |
| C compiler (for `go test -race`) | WinLibs gcc: `export CC="C:/tools/winlibs/mingw64/bin/gcc.exe"`. (The earlier `C:\tools\llvm-mingw` install is broken — missing headers; don't use it.) |
| WSL Ubuntu | `wsl -d Ubuntu`, systemd running, x86_64. sudo password: **Hunde123**. Docker installed (`docker.io`, daemon **active**, `/var/run/docker.sock` present; run docker as root). Caddy v2.11.7 at `/home/hp/bin/caddy` |

### WSL invocation gotcha (burned many turns)

Never run `wsl -d Ubuntu -- bash -lc '...'` or `bash -c '...'` with `$(...)`/`$VAR` content —
the outer Git Bash mangles it. Instead write a script to `C:\Users\hp\.zcode\tmp\X.sh`
(the Write tool writes LF; keep it LF) and run:

```
tr -d '\r' < "C:/Users/hp/.zcode/tmp/X.sh" | wsl -d Ubuntu -- bash
```

`tr -d '\r'` is insurance; the reliable form is the pipe. Windows paths in WSL:
`C:\Users\hp\AppData\Local\Temp` = `/mnt/c/Users/hp/AppData/Local/Temp`
(**Git Bash `/tmp` = AppData\Local\Temp, NOT `C:\tmp`** — this caused a release-staging miss).
Sudo in WSL: `echo Hunde123 | sudo -S -p '' <cmd>` (or prefix each command).

## What is DONE (verified, with evidence)

1. **Toolchain/build**: `go mod tidy` (go.sum created; go.mod gained the indirect block +
   `gopkg.in/yaml.v3` for the new routes test). `go build ./...`, `go vet ./...`, `gofmt -l .`
   all clean, also with `-tags spike`.
2. **make gen**: fixed two generator defects — oapi-codegen pin v2.4.1→**v2.8.0**
   (2.4.1/2.6.0 cannot parse this OpenAPI 3.1 spec: `oneOf [string,null]`, issue #373;
   2.8.0 works), and `api/oapi-codegen.yaml` `output: models.gen.go` (was a nested double
   `internal/api/oapigen/internal/...` path because `go generate` runs in the package dir).
   `internal/api/oapigen/models.gen.go` generated & committed (user's commit 36202c0).
   `make gen` twice → no diff. `scripts/gen_stubs.py` fixed to write LF (`newline='\n'`);
   CRLF on Windows broke `gofmt -l` and the CI drift check.
3. **Tests**: `go test -race ./...` ALL PASS (with CC=WinLibs gcc). Two test-only fixes
   (code was right, tests were wrong guesses — recorded in DECISIONS.md):
   - `internal/api/api_test.go`: `role:"developer"` → `"deployer"` (contract enum owner|admin|deployer|viewer).
   - `internal/platform/operators_test.go`: embedded an absolute Windows path (contains `:`)
     into a colon-delimited passwd line → rewrote fixture with relative home dir + chdir.
4. **Authz matrix**: `test/authz/matrix_test.go` passes; NEW `test/authz/routes_test.go`
   proves Go router route set == OpenAPI path/method set (110 routes, exact; also checks
   each route's Perm == x-permission).
5. **Mock-vs-implementation**: full diff table in `docs/DECISIONS.md` ("Tester pass").
   Code fixes applied: `systemStatus` now returns `net_rx_kbps`/`net_tx_kbps` (HostMetrics
   fields, stubbed 0); settings seed 300/2 + `build_concurrency` dropped; module seeds
   `version 1.2.0`, templates P7, 3 descriptions aligned to mock; settings audit action
   `settings.updated` (target `settings`/`server`, meta `{keys,values}`) + post-commit
   `flag.changed {key:settings}` bus event; `patchFlags` publishes `audit.appended`
   post-commit (one per row, like the mock); SSE now matches the mock bus: monotonic
   counter `id:` lines, `server_now` on non-metrics payloads, 500-event ring history +
   `?last_event_id` replay (`internal/events/bus.go` gained ID/history/Since).
   OpenAPI amendments applied + listed in `web/docs/CONTRACT.md` ("tester pass"):
   401 on the 7 setup routes after /setup/admin; new E501 + 501 on panel-domain/operator;
   409 on PATCH /modules/{key}; limit/cursor params on /users + /tokens; /events wire
   format documented (EventEnvelope schema kept because the generated TS types use its
   `type` enum). `make gen` re-run after the spec edits; tsc clean; vitest 94/94
   (run with Node 22.23.3 + `--fileParallelism=false`; default parallel forks time out on this host).
6. **Security tests** (new `internal/api/security_test.go`, 12 tests, all pass with -race):
   setup token 1h TTL, second /setup/admin 403, API-token lifecycle (cpl_+40hex, prefix,
   SHA-256-only storage, never in list, non-GET without CSRF via Bearer), session non-GET
   requires CSRF, login timing anonymity (<2s delta), audit trigger existence, deleted-row
   and broken-link `first_invalid_seq`, handler+audit same-transaction atomicity (injected
   failure via trigger → neither commits), owner-gate branch (crafted session; me/logout
   allowed, rest 403), recovery-code single use, SSE id/server_now/replay.
   Also `internal/config` gained `TestLoadYieldsOnlyLoopbackOrFails` (loopback mandatory).
7. **Binary (step 7)**: `make build` → `bin/codeplod` + `bin/codeplo` (no .exe suffix).
   Fresh daemon prints the setup token **once**; `codeplo setup-token` reissues;
   `doctor` / `doctor --verify-audit` pass on a clean DB; dropping `audit_no_update` +
   flipping `audit_events` seq=2 via the sqlite3 CLI makes doctor exit 1 with
   `first_invalid_seq=2`; `reset-password` (reads new pw from stdin, min 12 chars,
   revokes sessions) and `disable-2fa` verified (owner `totp_enabled`→0, sessions revoked).
   Full setup wizard + login run live via curl (also step 8 below).
8. **Contract run (step 8)**: `scripts/contract-test.sh` (new, executable) replays all 17
   frontend calls against a live daemon; every expected code matched (201/200/403/204/
   501/422/501…). Vite dev server with `VITE_API_MODE=live VITE_PROXY_TARGET=…:7443`
   proxies `/api` correctly (public `/setup/state` returns live data; auth routes 401).
   Documented in `docs/contract-test.md`. **Not browser-driven**: the wizard's operator
   step needs a Linux sudo user from `/etc/passwd` (this dev host is Windows → empty
   candidate list); recorded as a host limitation, and `/setup/complete` does not require
   an operator. No browser was driven (the permitted curl fallback).
9. **RSS (step 9)**: fresh daemon, one full setup+login, idle 60 s → **RSS 78.8 MB**
   (WorkingSet64 82,591,744 B; PowerShell `Get-Process`), vs Profile A budget
   **codeplod ≤ 50 MB** → **over budget** (Windows host; plan's Profile A is a
   CI/VM-lab i.e. Linux measurement — do not "tune to pass", report the number).
10. **P2a audit slice (step 10)**: **NOT implemented in this tree** — `POST
    /hardening/audit` exists only as the generated stub (404/501 module_disabled); there
    is no audit-mode filesystem/nftables behaviour to diff. Recorded in DECISIONS.md as
    not-run, not pass.
11. **Spikes (step 11) — PARTIAL, see Remaining work**:
    - Spike A (Caddy): ran in WSL with Caddy v2.11.7. The strict spike
      (`go test -tags spike` cross-compiled to Linux, run in WSL) is **flaky**:
      0 failures in 2 runs, 1–2 "connection reset by peer" in the others (2697/1638
      requests, etc.). Investigation matrix via a throwaway probe (NOT in the repo,
      at `C:\Users\hp\.zcode\tmp\spikeprobe\main.go`; rebuilds were fiddly — see below):
      keep-alive ON + fast reloads → 0/0 failures; keep-alive OFF → 57–71 EOF failures;
      `grace_period` (JSON: `apps.http.grace_period`; Caddyfile: global option `{ ... }`)
      at 5s had no clear effect; gap=1s between reloads → 2–8 failures out of ~176k
      requests (≈0.004%). Finding: keep-alive clients ride the zero-downtime swap;
      fresh connections race the swap; pace reloads. (Caddyfile `grace_period` needs the
      `{ }` global-options block form; root-level JSON `grace_period` is an unknown field.)
    - Spike B (Docker): WSL2 + Docker 29.1.3 (overlayfs). First run **FAILED** at the
      health-wait: the spike's `HostConfig.CapDrop:["ALL"]` makes `nginx:alpine` abort at
      start (`chown("/var/cache/nginx/client_temp",101) failed (EPERM)` → nginx [emerg]
      → exited; a control run without CapDrop becomes healthy in ~12 s, proving Docker
      healthchecks work here). **Fixture fixed in `test/spikes/docker_spike_test.go`**:
      added `CapAdd: ["CHOWN","NET_BIND_SERVICE"]` (nginx's entrypoint chown + serving
      :80). **The re-run is still pending** — that's your first job.
12. **Installer (step 12)**: all four cases PASS in WSL Ubuntu (systemd):
    - fake release served over HTTPS (self-signed CA trusted via update-ca-certificates):
      `deploy/install.sh` (CODEPLO_VERSION + CODEPLO_BASE_URL) → installs, service
      **active**, runs as **codeplo**, tunnel endpoint `http://127.0.0.1:7443/api/v1/setup/state`
      answers, `sudo -u codeplo codeplo setup-token` works.
    - tampered binary → `sha256sum -c` FAILED, installer aborts (exit 1).
    - tampered `codeplod.service` (User=root) → FAILED, aborts.
    - checksums.txt missing an entry → "is missing an entry", aborts.
    Fixture gotcha worth noting in the report: the installer's
    `grep -E " (name)$" | sha256sum -c -` only matches **GNU two-space, LF** checksum
    lines; CRLF or `*name` (binary marker) files make it reject everything.

## Remaining work, in order

### 1. Finish Spike B (repo test was just fixed, binary not yet rebuilt)

```
cd C:/Users/hp/projects/codeploy
go vet -tags spike ./...                                   # compiles the spike tests
GOOS=linux GOARCH=amd64 go test -tags spike -c -o "C:/Users/hp/AppData/Local/Temp/spikes.test" ./test/spikes
```
(`go build -tags spike ./test/spikes` fails with "no non-test Go files" — do NOT use it.)
Then in WSL (docker daemon is running; run as root for socket access):
```
tr -d '\r' < "C:/Users/hp/.zcode/tmp/spike-b-final.sh" | wsl -d Ubuntu -- bash
```
Script content (write it yourself if missing): `cp /mnt/c/Users/hp/AppData/Local/Temp/spikes.test /home/hp/spikes.test`,
`echo Hunde123 | sudo -S -p '' /home/hp/spikes.test -test.v -test.run TestDockerPullCreateHealthSwap`,
then clean up: `echo Hunde123 | sudo -S -p '' bash -c 'docker rm -f $(docker ps -aq) 2>/dev/null'`
(note: `docker ls -q` is INVALID — it is `docker ps -aq`).
If it still fails: capture `docker logs` + `docker inspect --format '{{json .State.Health}}'`
and report honestly (don't weaken the test further; the fixture fix is already justified
in the in-code comment + will go into DECISIONS.md).

### 2. Re-run Spike A once more (record, don't fix)

Same binary; `PATH=/home/hp/bin` so `caddy` resolves. Expect 0 or 1–2 failures (flaky).
Report the flakiness + the matrix findings (item 11 above) as the Spike A result.

### 3. Write `TEST_REPORT.md` at the repo root

Structure (per the mandate): environment (host, Go/Node/Python/make/gcc/WSL/Caddy/Docker
versions, race-detector caveats); a table of every mandate step (1–12) with
PASS / FAIL(fixed) / FAIL(open) / NOT RUN + reason; each defect found with file + fix;
the mock-vs-implementation differences (reference the DECISIONS.md table, list the FIXED
ones inline); the RSS number (78.8 MB vs 50 MB budget → over, unmet, not tuned);
plan §6.2 P0 and P1 exit criteria, each marked met/unmet with evidence:
- P0: threat model+ADRs written (met on paper; "merged to main" = not applicable locally);
  Caddy spike zero-drops = UNMET (flaky, drops observed at rapid reload; mitigations found);
  Docker spike = met/unmet depending on your re-run; CI green = NOT RUN (workflows exist,
  never executed on a CI runner).
- P1: install.sh clean-VM = MET (WSL Ubuntu, all 4 cases); owner via tunnel + TOTP login =
  MET (curl-driven; browser not driven — say so); every route has a permission + matrix =
  MET (incl. the new route-set equality test, 110 routes); audit chain + doctor + tamper =
  MET; idle RSS ≤ budget = UNMET (78.8 > 50, Windows host); OpenAPI→Go/TS gen in CI =
  generated artifacts exist + drift check wired in ci.yml but runner not executed (partial).
- P2a slice: NOT RUN — not implemented in this tree (stub route only).

### 4. Update `docs/DECISIONS.md` — add a "Spike findings" entry

Spike A flakiness + keep-alive/grace/reload-rate findings (grace_period location: JSON
`apps.http.grace_period`, Caddyfile global `{}` block; `v2.11.7` still resets a small
number of connections during rapid reloads). Spike B fixture defect (CapDrop ALL vs
nginx entrypoint chown; CapAdd CHOWN+NET_BIND_SERVICE; control run healthy ~12 s proves
environment healthchecks). Installer checksums.txt GNU-format requirement.

### 5. Update `PROGRESS.md` to match reality

Replace the "written, unverified" framing: what is now compiled, tested (-race), and
runtime-verified; the two test fixes; the generator fixes; the mock-alignment fixes;
remaining unmet items (RSS, P2a slice, CI runner execution, browser-driven contract run,
Caddy zero-drop on rapid reloads, Spike B pending your re-run). Keep it factual, no time
estimates, use "tamper-evident" (never "immutable").

### 6. Final gate before declaring done

```
cd C:/Users/hp/projects/codeploy
export CC="C:/tools/winlibs/mingw64/bin/gcc.exe"
go test -race -count=1 ./...        # everything, incl. new tests
go vet -tags spike ./...
gofmt -l .                           # must be empty
export PATH="$PATH:/c/Users/hp/.config/herd/bin/v20.20.2:/c/Program Files (x86)/GnuWin32/bin"
make gen && make gen                # idempotent
go build ./...
```
Then tell the user exactly which files changed (so they can commit, as before):
modified — `api/openapi.yaml`, `api/oapi-codegen.yaml`, `scripts/gen_stubs.py`,
`internal/api/{handlers_admin.go,api_test.go,security_test.go(new)},internal/events/bus.go`,
`internal/store/migrations/0001_core.sql`, `internal/config/config_test.go`,
`internal/platform/operators_test.go`, `test/authz/routes_test.go(new)`,
`test/spikes/docker_spike_test.go`, `web/docs/CONTRACT.md`, `web/src/api/schema.d.ts`
(regen), `internal/api/oapigen/models.gen.go` (regen), `go.mod`, `go.sum`,
`docs/DECISIONS.md`, `PROGRESS.md`; new — `internal/api/security_test.go`,
`test/authz/routes_test.go`, `scripts/contract-test.sh`, `docs/contract-test.md`,
`TEST_REPORT.md`. Stop any stray processes (WSL docker/caddy/codeplod/vite) and remove
`C:\var\lib\codeplo` if it exists (a doctor run once created it by accident on Windows —
it was removed; verify).

## Failure catalog (what was broken and how it was fixed — keep for the report)

| # | Defect | Fix |
|---|---|---|
| 1 | oapi-codegen 2.4.1 can't parse OpenAPI 3.1 spec | pin v2.8.0 (`internal/api/oapigen/doc.go`) |
| 2 | oapi-codegen output path nested under package dir | `api/oapi-codegen.yaml` `output: models.gen.go` |
| 3 | `scripts/gen_stubs.py` CRLF on Windows → gofmt/drift break | `open(..., 'w', newline='\n')` |
| 4 | `api_test.go` invalid role `developer` | → `deployer` |
| 5 | `operators_test.go` Windows absolute path in passwd fixture | relative home + chdir |
| 6 | `/system/status` missing `net_rx_kbps`/`net_tx_kbps` | stubbed 0 (P6 data) |
| 7 | settings seed 120/5 vs mock 300/2; stray `build_concurrency` | seeds aligned, key dropped |
| 8 | settings audit `settings.changed`{keys} vs mock `settings.updated`{keys,values}; no bus event | aligned + post-commit `flag.changed {key:settings}` |
| 9 | `patchFlags` published audit.appended inside the tx | post-commit, one per row |
| 10 | SSE: no `id:` lines, no `server_now`, no resume; OpenAPI said envelope | bus counter IDs + 500-ring + `Since`; handler emits id/server_now; OpenAPI amended to the real wire format (mock+frontend match) |
| 11 | OpenAPI gaps: setup 401, 501, modules 409, /users+/tokens cursor params | spec amended + CONTRACT.md |
| 12 | spike B fixture: `CapDrop ALL` + nginx:alpine can't start | `CapAdd [CHOWN, NET_BIND_SERVICE]` (re-run pending) |
| 13 | installer checksum grep only accepts GNU two-space LF lines | documented (release pipeline must emit that format; fixture produced it) |

## Things that look scary but are NOT defects (don't "fix")

- 404 instead of 405 for wrong methods on known paths (mock-only cosmetic diff, documented).
- keyset cursors vs mock's offset cursors (deliberate, DECISIONS line 19, now documented in OpenAPI).
- Go being stricter than the mock (role enum on PATCH user, token `expires_at` validation,
  seq-gap audit check) — Go matches OpenAPI; the mock is looser.
- `GET /system/version` defaulting to `0.1.0-dev`/`none`/`unknown` when built without
  ldflags (normal `make build` always injects values).
- WSL/RSS: 78.8 MB measured on Windows; the plan's Profile A is a Linux CI-lab number.
  Report, don't tune.

## Open questions for the user (ask at the end, not during)

- Should `bin/`, `internal/webui/dist/*` (embedded build output) and the `C:\tmp`/
  `C:\var` strays be gitignored? (`.gitignore` exists for web/; repo root may want entries.)
- CI runner execution (P0/P1 criteria) needs a real CI system — out of scope here.
- Browser-driven contract run on a Linux host (operator step) is the only unexecuted P1
  slice.
