# PROGRESS: codeplo — P0–P3 met (P3 host-shell run owed), P4 implemented and compiling

Status: **Phase 4 (projects, resources, deployments) is implemented end to end in the backend** — migration
`0004_workloads.sql`, the isolation policy, the container `Manager` port, the §3.8 engine, the REST handlers and
the daemon wiring. `go build ./...`, `go vet ./...` (which compiles the test files too) and `gofmt -l .` are clean.
**No test has been run for Phase 4**: the instruction for this stretch of work is development only, with compile
checks, and the test-and-fix pass is deliberately held to the end of all phases. Phase 2 is deployed and
gate-verified on a real systemd host; Phase 3's foundation and container runtime are verified against real Docker,
while its host-shell run is still owed.
Evidence tables below; full detail in `TEST_REPORT.md`, judgment calls in `docs/DECISIONS.md`.

## Phase 2 — what was built

| Area | Files | What it does |
|---|---|---|
| Migration | `internal/store/migrations/0002_edge_hardening.sql` | `domains`, `edge_snippets`, `hardening_runs`, `hardening_results`, `hardening_changes` |
| Edge core | `internal/modules/edge/{client,render,reconcile,certs}.go` | Caddy admin client over the unix socket only (refuses TCP), deterministic whole-config JSON renderer + SHA-256 drift key, reconciler (`Reconcile`, `Status`, `Certificates`, `Loop` supervision with `systemctl restart`), panel-domain row helpers |
| Edge API | `internal/api/handlers_edge.go`, `handlers_domains.go` | `/edge/status`, `/edge/reconcile`, `/edge/certificates`, `GET /domains`, `GET/POST /resources/{id}/domains`, `DELETE /resources/{id}/domains/{domainId}`, `POST /setup/panel-domain` (replaces the P1 501 stub) |
| Daemon wiring | `cmd/codeplod/main.go` | when the `edge` module is enabled: build the reconciler, run one reconcile at startup (non-fatal), supervise in a 30 s loop tied to the daemon context |
| Hardening engine | `internal/modules/hardening/{audit,plan,preflight,apply,score,tiers,revert,drivers}.go` | `detect → plan → preflight → snapshot → apply → confirm \| revert`; ten baseline checks with tier + apply-flag metadata; `sshd -T` effective-config verification; nftables runtime-first plan; fail2ban jail with the operator's IPs ignored; snapshot spool + live-state `revert.json` |
| Hardening API | `internal/api/handlers_hardening.go`, `handlers_hardening_apply.go` | `/hardening/checks` (with `plan_diff` + `apply_enabled`), `/audit`, `/plan`, `/preflight`, `/apply`, `/confirm/{id}`, `/revert/{id}`, `/runs`, `/changes` |
| Dead-man switch | `hardening.ScheduleRevertTimer` + `cmd/codeplo/main.go` `revert <id>` | `systemd-run --on-active=<window>s codeplo revert <id>`; the CLI needs neither daemon, database nor network |
| Platform | `internal/platform/{exec,systemd,distro,apt,host_*}.go` | fixed-argv runner, `SystemdActive`, distro item paths |
| Deploy | `deploy/install.sh`, `deploy/caddy/{codeplo-caddy.service,bootstrap.json}`, `deploy/codeplod.service`, `Makefile` `dist` | installer now ships and verifies five artefacts, requires Caddy as a hard prerequisite, asserts the panel answers, and templates `/etc/default/codeplod` |
| Tests | `internal/api/{edge_test,hardening_apply_test}.go`, `internal/modules/edge/*_test.go`, `internal/modules/hardening/*_test.go` | zero-mutation audit diff, Tier-1 idempotency, runtime-first ordering, dead-man arming and absence for Tier 1, live-state revert, operator-bannable jail refusal, preflight refusals |

## Phase 2 — verification (2026-10-20)

| Check | Result |
|---|---|
| `go build ./...`, `go vet ./...`, `go vet -tags spike ./...`, `gofmt -l .` | Pass |
| `go test -race -count=1 ./...` | Pass — 11 packages ok, 0 FAIL, **0 skips** |
| Route set == OpenAPI + `x-permission` parity (`test/authz`) | Pass |
| `make gen` chain run twice | Idempotent — generated TS schema, stubs and models match committed artifacts |
| Frontend: `tsc --noEmit`, lint guards, `vite build`, `check:mock-free`, `check:external`, `check:size`, vitest | Pass (guards 104 files / 8 rules; 94/94 tests, Node 22.23.3 serial) |
| Spike C — edge render → `POST /load` → no drift, real Caddy v2.11.7 in WSL | **PASS (3.54 s)** |
| Installer on WSL Ubuntu (systemd, real Caddy) | Pass — 15/15 assertions: clean install, both units active 20 s later, `NRestarts=0`, admin socket `660 root codeplo`, `codeplo` user reads the admin API, live config holds the reconciler's `:80`/`:443` servers, panel answers on TCP **and** unix socket, `/var/lib/codeplo` `700 codeplo`, clean journals; plus the four abort cases (tampered binary, tampered unit, missing checksum entry, missing Caddy) |
| Three boot-time defects found by that run | Fixed: ACME placeholder in the bootstrap, `StateDirectory` re-owning the panel's data dir, installer claiming success on a crash-looping panel |
| Spike A (Caddy zero-drop under rapid reloads) | **UNMET** — flaky, kept strict (see DECISIONS) |
| CI executed on a runner | **NOT RUN** — workflows wired, never executed |
| VM fault-injection matrix (§3.10.8) for P2b/P2c | **NOT RUN** — the *semantics* are test-asserted at the API seam; a disposable-VM run (kill daemon after apply, reboot before confirm, external port scan on IPv4+IPv6) is still owed |
| Panel HTTPS on a real public domain | **NOT RUN** — needs DNS pointing at a public host; cert issuance is exercised against Caddy's internal issuer in the spike |
| Idle RSS vs Profile A ≤ 50 MB | **UNMET** (78.8 MB measured on Windows; report, don't tune) |

## Phase 3 — foundation (2026-10-21), built and gate-verified

| Area | Files | What it does |
|---|---|---|
| Deps | `go.mod` (`go` 1.23, CI reads the version from it) | `github.com/creack/pty` v1.1.24 (host PTY), `github.com/coder/websocket` v1.8.15 (WS upgrade/frames) |
| Migration | `internal/store/migrations/0003_terminal.sql` | `terminal_sessions` (+ recent/open indexes), seeds `terminal.max_sessions_per_user=3`, `terminal.record_sessions=false` |
| Runtime port | `internal/modules/runtime/runtime.go` | frozen P3 interface (`Available`, `Resolve`, `Logs`, `Exec`) plus `Container`, `LogOptions/LogLine/LogFunc`, `ExecStream`, `LabelResource`; the API layer never touches the Docker Engine API |
| Terminal core | `internal/modules/terminal/terminal.go` | `Config` from settings, `TicketTTL`, `Session` + contract-shaped `JSON()`, `Store` (`Open` with in-tx per-user concurrency check, `Close` keeping the first end reason, keyset `List`, `Recover` for rows left open by a crash) |
| API surface | `internal/api/router.go` | `Server.Runtime` / `Server.Terminals` fields, wired by the daemon like `Server.Edge`; nil means "not available", never a fake success |

Verified: `go build`, `go vet`, `gofmt -l`, `go test -race -count=1 ./...` — 12 packages ok,
0 skips; `test/authz` still proves the route set equals OpenAPI (terminal routes are stubs
until the handlers land). New tests: `internal/modules/terminal/terminal_test.go` (7 cases —
settings defaults/overrides, concurrency limit + slot release, first end reason wins, keyset
paging with no skipped row, crash recovery idempotence, contract object shape).
One real bug found and fixed while gating: `Store.List` returned the *next* page's first id as
the cursor, which skipped a row.

The second pass below is now compiled, gated and committed, and the container-runtime half of its runtime
verification is done against real Docker (see "Phase 3 — container runtime verified against real Docker").
What remains of Phase 3 is the host-shell run on a real systemd and the frontend's live-mode pass.

## Phase 3 — second pass: built, compiled and gate-verified (2026-10-21)

| Area | Files | What it does |
|---|---|---|
| Tickets | `internal/modules/terminal/ticket.go` | single-use, 30 s, bound to user + target + resource + Origin; a failed binding check still burns the ticket |
| Helper protocol | `terminal/frames.go`, `policy.go`, `host.go` | length-prefixed data/resize/exit frames; `Authorize` (sudo/wheel operator or root with a host flag file); `HelperSpawner` client |
| Helper (root side) | `terminal/pty_helper_linux.go`, `pty_helper_other.go`, `cmd/codeplo` `pty-helper` | `SO_PEERCRED` check, PTY via `creack/pty`, process-group kill on socket loss; non-Linux build refuses |
| Docker adapter | `internal/modules/runtime/docker.go` | `Available`, `Resolve` (label filter), `Logs` (demux or raw tty), `Exec` (hijacked attach, resize, poll-wait) |
| Handlers | `internal/api/handlers_terminal.go`, `handlers_logs.go` | ticket, sessions, host + container WebSockets, log SSE with drop-oldest queue and `Last-Event-ID` resume |
| Wiring | `internal/api/router.go`, `cmd/codeplod/main.go` | `Server.Tickets`, `Server.HostShells`; runtime wired when terminal or deploy is enabled; session recovery at start |
| Deploy | `deploy/codeplo-pty.socket`, `deploy/codeplo-pty@.service`, `install.sh`, `Makefile dist` | helper units, docker drop-in, seven verified artefacts |
| Docs | `docs/adr/0006-privileged-pty-helper.md`, `docs/DECISIONS.md` | why the helper exists, every judgment call, and the integration findings below |
| Tests | `terminal/{ticket,policy,helpers}_test.go`, `runtime/docker_test.go`, `internal/api/terminal_test.go` | tickets, policy, frames, demux/backpressure, ticket rules, WS bridge end to end with fakes, session limit, log ids + resume, drop queue, idle timeout, container bridge, Origin binding |

Verified on this host: `gofmt -l .` empty, `go build ./...` (Windows) and
`CGO_ENABLED=0 GOOS=linux go build ./...` + `GOOS=linux go vet ./...` clean, and
`go test -race -count=1 ./...` — **13 packages ok, 0 FAIL, no skips**. `test/authz` still proves the
route set equals OpenAPI with the four terminal routes and the log route now implemented. The
terminal tests were then run `-count=6` to confirm the new teardown path is not flaky.

Four defects found while integrating and fixed: a duplicate `Server.moduleOn` (the package did not
compile), the helper reporting exit code 0 for a broken socket, an unbounded Docker `resize` request,
and — the one that mattered — the WebSocket bridge writing its goodbye *after* cancelling the context,
which `coder/websocket` turns into an anonymous socket death. Details and reasoning are in
`docs/DECISIONS.md`, "Phase 3 — integration, gate and review fixes".

**Owed:** the §6.2 P3 runtime verification on a real systemd host — host shell as a real sudo operator,
refusal with no operator, root off by default and step-up-gated when on (with and without
`/etc/codeplo/allow-root`), close-the-tab ends the PTY (no leftover process), idle timeout, session limit,
an audit row per ticket and per session, the helper socket's mode letting the unprivileged daemon traverse
`/run/codeplo-pty`, the dropped-line notice under a slow reader, and the installer test with seven
artefacts. Spike E (`test/spikes/p3_hostpty_spike_test.go`) is written and compiles under the spike tag but
**has not been run yet** — it is committed as work-in-progress, not as a pass.

## Phase 3 — container runtime verified against real Docker (2026-10-21)

Spike D (`test/spikes/p3_runtime_spike_test.go`) now runs green against a real Docker **29.1.3** daemon
(API 1.52, containerd 2.2.1, runc 1.3.4) in WSL, and it changed the code rather than just confirming it:

| Claim | Measured result |
|---|---|
| `Resolve` by `codeplo.resource` label, absent label → `ErrNoContainer` | **true** — passed |
| Logs demux stdout/stderr into separate lines, `timestamps=1` parsed | **true** — 2 lines, both streams separated |
| `since=now` replays nothing (the SSE `Last-Event-ID` resume premise) | **true** — passed |
| Daemon accepts the exec create body (`Tty` **with** `AttachStderr`), attach moves bytes both ways, `resize` accepted | **true** — `stty size` answered `43 132` after the resize |
| **Closing the attach ends the exec** (the old `docker.go` comment asserted this) | **FALSE.** The container's `/bin/sh` was still running minutes later; `docker top` still listed it |
| Half-closing the write side ends it | **FALSE** — a tty exec has no stdin pipe, so there is no EOF for the shell to read |
| `POST /exec/{id}/kill` kills it | **The route is not registered at any API version** on this daemon (`{"message":"page not found"}` at 1.24/1.41/1.52, while `/exec/{id}/resize` on the same prefix answers 200). So the first fix — calling kill — was a silent no-op, and treating 404 as "benign" hid it |
| What *does* work | An **in-band terminal EOF**: `^U` then `^D` twice. Measured side by side: hang-up alone → still running; `^D^D` → exit 0; `exit\r\n` → exit 0 |

Fix in `internal/modules/runtime/docker.go`: `Close()` now writes `ttyEOF` (`\x15\x04\x04`) before hanging up
— `^U` first so a half-typed command line is discarded instead of executed by teardown — then half-closes,
then still asks the daemon to kill (for runtimes that implement it), and reports a missing kill route as
unsupported rather than success. Spike D's assertion is now "the exec ends **and** the container's process
table holds nothing but its own command", and it passes: `exit code 0`, `process table after the close:
[sleep 600]`.

Because the mechanism is a protocol behaviour, not a Docker version accident, it is pinned without a daemon
too: `internal/modules/runtime/docker_exec_test.go` runs a stub Docker Engine API server on a unix socket,
answers 404 for kill exactly like 29.1.3 does, and asserts the adapter writes precisely the EOF gesture,
half-closes so the daemon sees the session end, is idempotent on a second `Close()`, and never starts a
non-tty exec.

Gates after this change: `gofmt -l .` empty, `GOOS=linux go build ./...` + `go vet ./...` clean, and
`go test -race -count=1 ./...` — **13 packages ok, 0 FAIL, no skips** (the stub test needs AF_UNIX; it runs
on Windows 11 and on Linux CI).


## Security note: a local-VM credential is in git history

An earlier revision of this file carried the **local WSL test VM's root sudo password in plain text**
(`PROGRESS.md` in commits `fde1796`, `48315f5`, `baad040`). It is gone from the working copy and from
`origin/main`'s tip, but it is still in history. The credential only ever guarded a disposable local VM
and is being rotated, so the history is deliberately left as it is.

The rule going forward: **credentials are never written into this repo** — not passwords, tokens or
`EnvironmentFile` values. When a test host needs one, the operator supplies it for that session.

## Exit criteria (§6.2)

**P0** — threat model + ADRs met; Docker spike met; **Caddy zero-drop unmet**; CI green on a
runner not run.
**P1** — installer on a clean VM met (now with real readiness assertions); tunnel + one-time
token + TOTP met (curl-driven); per-route permission + authz matrix met; audit chain +
`doctor --verify-audit` met; RSS unmet; generation-in-CI partial (works, runner never ran).
**P2a** — audit makes zero changes (tree-diff test) met; Tier 1 fixes idempotent met; fail2ban
ignore list contains the admin IPs met; operator identity selectable met; **panel reachable
over automatic HTTPS on a real domain: not verified** (no public DNS here), though the route,
the ACME policy and the break-glass loopback path are implemented and deployed.
**P2b** — runtime-first then persist-on-confirm met (asserted ordering: boot files written
before the timer is disarmed); revert restores files then drops the live table, and is
idempotent when the table is already gone; **daemon-kill-still-reverts and reboot-before-confirm
are test-asserted at the seam but not VM-verified**; external scan showing only SSH/80/443 not
run.
**P2c** — implemented: `sshd -T` effective comparison, `sshd -t`, `Include` requirement,
`authorized_keys` sanity, `Accepted publickey` proof, console acknowledgement, reload-not-restart,
port changes never automated. **Apply stays flag-gated (default plan-only) until the VM matrix
passes**, which is the plan's own gate.

## Open across the project

- VM lab (`test/vm/*`) for the hardening fault-injection matrix; release gate for P2b/P2c.
- CI has never run on a runner (also the only place the RSS budget is meaningful).
- No signed release; `make dist` is the only artefact producer until P9.
- **Phase 4 has no executed test**: the engine, the handlers and the edge change compile but are unproven; the
  §3.8 fault-injection matrix and the mock-parity conformance run are the gate that is actually missing.
- TOTP replay guard in-memory; sqlc scaffolded but unused; `RouteDef.NoCSRF` declared-unused.

## Handoff brief — what Phase 3 still owes (for the next agent)

Phase 3 is **written, compiled, race-tested and committed**. Nothing in it needs re-doing at the code
level; `docs/DECISIONS.md` records every judgment call, including the four defects found in integration.
Read `internal/modules/terminal/terminal.go`, `internal/modules/runtime/runtime.go` and ADR 0006 before
touching this area — they are the types and the privilege boundary everything else compiles against.

**Tasks left, in dependency order**
1. Run spike E in WSL against real systemd (`tmp/setup_spike_e.sh` is not in the repo — it was never written
   down; recreate it from the header comment in `test/spikes/p3_hostpty_spike_test.go`): create the
   `codeplo` service user, an operator account in `sudo` and a non-operator account, install
   `/usr/local/bin/codeplo` + the two units, `systemctl restart codeplo-pty.socket`, then run the compiled
   binary **as the codeplo user** (`setpriv --reuid=... --clear-groups`) and once **as root** for the peer
   check, and with `/etc/codeplo/allow-root` present and absent. The Docker half of §6.2 P3 is done — see
   the "container runtime verified against real Docker" section above.
2. Installer test fixtures must serve **seven** artefacts now (adds `codeplo-pty.socket`,
   `codeplo-pty@.service`); the checksum guard is `-eq 7`.
3. Frontend: `TerminalPane`/`TerminalPage` and the log viewer already speak the right frames and are
   mock-driven; the work is running them in live mode against the real WS/SSE endpoints and re-running the
   frontend gates. Note that OpenAPI carries route-level `x-step-up: true` on `/terminal/ticket` while the
   server (per the plan and the mock) requires step-up only for `as_root` — decide deliberately whether the
   UI should prompt on every ticket, and make contract, server and UI say one thing.
4. Phase 2 residual: the P2b/P2c VM fault-injection matrix is still owed.
5. Phase 4 (projects & deployments) owns migration `0004_workloads.sql` and builds on `runtime.Runtime`
   plus the `codeplo.resource` label — see the Phase 4 prompt handed to that agent.

**Contract facts** (all verified in `api/openapi.yaml` + the executable mock)
- `POST /terminal/ticket`: `x-permission terminal:container`, `x-step-up: true`; request
  `{target: host|container, resource_id: string|null, as_root: bool}`; 201 `{ticket, expires_at, idle_timeout_s}`.
- `GET /terminal/host`, `GET /terminal/resources/{id}`: WS, `?ticket=`. **Binary frames = PTY bytes both
  ways; text frames = JSON control** — in `{type:"resize",cols,rows}`, out `{type:"exit",code}` |
  `{type:"error",message}`. Mock semantics to match: `web/src/mock/install.ts:215-262`, ticket rules and the
  operator-missing 409 in `web/src/mock/handlers/platform.ts:140-165`, sessions list at `:166`.
- `GET /terminal/sessions`: `terminal:host`, page `{items, next_cursor}`, row = `TerminalSession`
  (`id,user_email,ip,target,target_name,as_user,is_root,started_at,ended_at|null,recorded`) —
  `terminal.Session.JSON()` already matches it exactly (a test asserts the key set).
- `GET /resources/{id}/logs`: SSE, `resource:read`, params `tail`, `since`.
- Audit actions: `terminal.ticket_issued`, `terminal.session_opened` (target_type `terminal`).
- Permissions already registered and role-mapped (owner all, admin minus `terminal:root`, deployer/viewer
  none) — no `internal/authz` change needed; `requireStepUp` is at `internal/api/session.go:218`.

**Ground rules carried through P2, still binding:** smallest fix; never weaken a test; never report a skipped
check as a pass; "tamper-evident", not "immutable"; RSS is reported, not tuned; judgment calls go in
`docs/DECISIONS.md`; PROGRESS.md updated before every commit; one commit per unit of work; push when the local
gates pass so CI runs the slow ones. **No credentials in the repo, ever** — WSL root access is supplied by the
operator when a test needs it.

**Gates and host quirks on this machine**
- `gofmt -l .` empty; `go build ./...`; `go vet ./...` and `go vet -tags spike ./...`;
  `CC="C:/tools/winlibs/mingw64/bin/gcc.exe" go test -race -count=1 ./...` (12 packages ok as of the foundation).
- `make gen` must stay idempotent (md5 compare `models.gen.go`, `stubs_gen.go`, `schema.d.ts`); frontend gates
  with Node v20.20.2 (`npm run lint`, `vite build`, `check:mock-free`, `check:external`, `check:size`) and
  vitest under Node v22.23.3 with `--fileParallelism=false`; both under `/c/Users/hp/.config/herd/bin/`.
- WSL is a flaky test host: it tears the distro down after ~8 min idle, wiping `/tmp`. Run each test sequence as
  **one** command, or hold it open with a background `wsl -d Ubuntu -u root -- sleep 900`, and put logs under
  `/root`. WSL root access is provided by the operator per session and is not recorded in this repo.
  Never `pkill -f <pattern>` when the pattern also appears in your own
  command line — it kills your shell mid-run. A *running service is shared state*: stop and disable fixture
  units before running anything that probes `:80`/`:443`.
- Left on WSL from the P2/P3 sessions (owned by that work, not the repo): a `/usr/bin/caddy` copy, `/root/spikes`,
  `/root/spikeC.log`, `/root/codeplo-fixture`.

## Phase 4 — projects, resources, deployments (2026-10-21): implemented, compiling, **not tested**

The instruction for this stretch was development only — `go build`, `go vet` (which compiles the test files) and
`gofmt` are the gates that ran. **No Phase 4 test has been executed**, and none is claimed to pass; the test-and-fix
pass is held to the end of the phases. `go vet ./...` being clean means the package set including tests type-checks.

| Area | Files | What it does |
|---|---|---|
| Migration | `internal/store/migrations/0004_workloads.sql` | `projects`, `environments`, `resources` (no status column — status is derived), `resource_env_vars`, `volumes`, `resource_sources`, `resource_credentials`, `deployments` (digest + `retained` + `upstream` + `heartbeat_at` + `observe_until` + `rollback_of` + `preflight_json`), `deployment_logs`; seeds `deploy.build_timeout_s`, `deploy.release_timeout_s`, `deploy.max_log_lines`, `deploy.stop_old_first`; additive, 0001–0003 untouched |
| Isolation policy | `internal/policy/*.go` | every §3.11 control, and `Approved` is mintable **only** by `Enforce` — `CreateContainer` takes `Approved` and re-checks it, so a policy-violating container cannot be created through the port |
| Container port | `internal/modules/runtime/{manage,docker_manage}.go` | `Manager` extends the frozen `Runtime`: networks, create/start/stop/kill/remove, inspect, list by label, wait, image inspect/tag/remove, prune. Cancellation is container lifecycle + context, never an exec interrupt (measured: this daemon cannot kill one) |
| Engine | `internal/modules/deploy/{engine,steps,observe,recover,preflight,retention,logs,redact,config,db,service}.go` | the full §3.8 state machine including `observing` → `succeeded`, automatic rollback to the previous container when the window fails, per-step timeouts, disk watch, RAM-capped builds, release-command container, label-driven idempotent cleanup, boot recovery (`interrupted` + orphan sweep + **refresh of the active container's address after a runtime restart**), image retention, secret redaction from logs |
| API | `internal/api/handlers_{projects,resources,deployments,env}.go` | `/projects` (+ tree, `POST`), `/projects/{id}/environments`, `/resources` CRUD with derived status, `start`/`stop`/`restart`, `/resources/{id}/policy`, `/resources/{id}/deployments`, `/deployments/{id}` + `/logs` (SSE) + `/cancel` + `/rollback`, `/resources/{id}/env` (write-only secrets, step-up to replace a secret), `/resources/{id}/volumes`, `POST /system/cleanup` |
| Durable side effects | `deploy.reap` job in `service.go` | stop/delete enqueue the container work in the same transaction that changes the row, so a down Docker daemon cannot strand a half-stopped resource |
| Edge | `internal/modules/edge/reconcile.go` | `domains.upstream` becomes a last-known-good written with the switch; stopped resources get no route, running-but-unresolved ones keep serving the cached address |
| Domains API | `handlers_domains.go` | the debt P2 recorded is now paid: `GET`/`POST /resources/{id}/domains` 404 when the resource does not exist (fixtures in `edge_test.go` create real resources) |
| Wiring | `cmd/codeplod/main.go`, `internal/api/router.go` | engine built when the `deploy` module is enabled, registered on the queue **before** `Jobs.Run`, `Recover` before the first claim, bus publish + edge reconcile injected; `platform.AppArmorEnabled()` decides whether the profile is claimed |
| Contract | `api/openapi.yaml`, generated `models.gen.go` / `schema.d.ts` | `ResourceConfig` gained `release_command`, `rollout`, `published_ports` (the engine already honoured them); generators re-run, route stubs unchanged |



## Phase 5 — Git & CI/CD (2026-10-21): implemented, compiling, **not tested**

Same instruction as Phase 4: development only. The gates that ran are `gofmt -l internal cmd` (empty),
`go build ./...` and `go vet ./...` (both clean, and `vet` type-checks the test files). **No Phase 5 test has been
executed and none is claimed to pass.** One toolchain note that cost real time and is now recorded: the module path is
`github.com/codeplo/codeplo` (the repository is `codeploy`); a new package that imports
`github.com/codeplo/codeploy/...` fails with `no required module provides package`, which looks exactly like a broken
module cache and is not one.

| Area | Files | What it does |
|---|---|---|
| Migration | `internal/store/migrations/0005_git.sql` | `git_connections`, `deploy_keys` (public half, one per resource), `deploy_hooks` (token hashed), `webhook_secrets` (sealed), `webhook_deliveries` with `UNIQUE(provider, delivery_id)` as the replay guard + two indexes; seeds `git.clone_timeout_s=300`, `git.max_clone_mb=2048`. Additive — 0001–0004 untouched. `resource_sources`/`resource_credentials` already existed from 0004 and are reused, not duplicated |
| Git module | `internal/modules/git/{module,source,deploykey,hook,webhook,clone}.go` | every P5 rule as a function with no HTTP in it: connection list/create, source read/validate/store (mock-exact validation messages), ed25519 deploy keys (private half sealed into `resource_credentials`, never returned), hook tokens (prefix-only reads, create refuses when one exists, rotate keeps the id), webhook secrets, `VerifySignature` over the exact bytes with `hmac.Equal`, the push gate in contract order (event → branch → `[skip deploy]` → path filters), delivery claim/record/page, clone limits, and `VerifySource` — a pre-flight that refuses a source the engine could never build |
| Tag policy | `hook.go` `MatchImage` | `ghcr.io/org/app:*` accepts `app:<tag>` and `app:<tag>@sha256:…`, and rejects `app-evil:latest` — the boundary is the `:` after the repository, not a string prefix |
| Provider | `internal/modules/git/providers/github.go` | GitHub push payload → `git.Push` (ssh_url/clone_url, `refs/heads/` stripped, short SHA, flattened added/modified/removed), and `CanonicalRepo` so `git@host:o/r.git` and `https://host/o/r` compare equal (host case-insensitive, path case-sensitive) |
| API | `internal/api/handlers_git.go` | all 14 git routes implemented (no stubs left in this module): `/git/connections`, `/resources/{id}/source` GET+PUT, `/deploy-key` POST+GET, `/deploy-hook` GET+POST+`/rotate`, `/webhook` GET+POST, `/webhooks/deliveries`, public `POST /webhooks/github` and `POST /hooks/deploy/{token}`. Writes and their audit row commit in one transaction; the receivers claim the delivery id in the same transaction as the deployment they trigger |
| Receiver security | same file | signature verified before the payload is trusted to name a repository; unattributable posts (unknown repo, unknown token) are refused **without** writing a delivery row, because the endpoint is public and rate-limited to 30 rps; a replay loses the transaction and answers 409 |
| Engine wiring | `internal/modules/deploy/{steps,engine}.go`, `builders/{builder,cli}.go` | `buildFromSource` now reads `git.ReadCloneLimits`, gives the clone its own deadline, and passes `GitSource.MaxBytes`; `CLI.Clone` sizes the checkout and fails with the new `ErrSourceTooLarge`, which the engine reports with the settings key that would raise it |
| CI | `deploy/github-actions/codeplo-deploy.yml` | reusable `workflow_call` workflow: builds and pushes to a registry, then calls the resource's deploy hook. `permissions: contents:read, packages:write`, per-image concurrency group, 3 attempts with backoff, JSON built by `jq`, honours `[skip deploy]`, and never echoes the token |
| Wiring | `cmd/codeplod/main.go`, `internal/api/router.go` | `srv.Git = git.New(db, key)` when the `git` module is enabled (database + master key only, so it wires with no Docker daemon present) |

**Deliberate divergences from the mock** are in `docs/DECISIONS.md` under Phase 5: the two no-write rules above, the
webhook deploying a host-built image rather than a payload-named one, and `VerifySource`. Everything the frontend
asserts — status codes, `fields` keys, message wording, response shapes including explicit `null`s — matches.

## Phase 6 — Operations (2026-10-21): in progress, compiling, **not tested**

Nothing in this phase is wired into the HTTP layer yet: `GET /system/metrics`, the channel and rule routes and the
backup routes are the handlers task, and the sampler/evaluator/backup ticks on the daemon are the wiring task. The
packages are written so those two are wiring, not design.

Same instruction as Phases 4 and 5: development only. The gates that ran for every unit below are
`gofmt -l internal cmd` (empty), `go build ./internal/... ./cmd/...` and
`go vet ./internal/... ./cmd/...` (all clean). **No Phase 6 test has been executed and none is claimed to pass, and
migration 0006 has not been applied to a database yet** — a `goose up` against a scratch DB is the first thing Phase
6's test pass owes, because a bad migration blocks daemon boot for every later phase. That is also why 0006 could
still be corrected in place: `backup_schedules.enabled` was dropped, since no contract route can set it and a column
nothing writes is a claim about a capability that does not exist.

| Area | Files | What it does |
|---|---|---|
| Migration | `internal/store/migrations/0006_operations.sql` | `metrics_samples` (the ring buffer: host and per-resource points in one table, `scope`/`ref_id` windowed by an index, `id` a ULID so ordering, pruning and range filtering share one key), `notification_channels` (sealed config, plaintext hint), `alert_rules` + `alert_firings` (dedupe per `(rule, subject)` with cooldown from `last_notified_at`), `backup_schedules` + `backup_runs` (`trigger` includes `pre_restore` and `pre_deploy`, which only internal code writes), `resource_cron_jobs` + `cron_job_runs`; four alert presets seeded **disabled**; seeds `metrics.*`, `alerts.eval_interval_s`, `notify.timeout_s`, `logs.*`, `cron.history_per_job`, `backup.timeout_s`. Additive — 0001–0005 untouched |
| Host counters | `internal/platform/stats_{linux,other}.go` | `Sample()` reads cumulative CPU ticks from `/proc/stat` and rx/tx bytes from `/proc/net/dev` (excluding `lo`). These are counters, not rates: the caller differences two samples. Non-Linux builds return zeroes so the panel still runs where Docker does not |
| Container counters | `internal/modules/runtime/docker_stats.go` | `Docker.Stats` with `stream=false` — one HTTP round trip that returns the same CPU/memory fields the streaming API does, so the sampler never holds a connection open per container |
| Non-tty exec | `internal/modules/runtime/docker_exec_env.go` | `ExecWithEnv`: the create-and-attach sequence with a caller-supplied environment, so `PGPASSWORD`/`MYSQL_PWD`/`REDISCLI_AUTH` reach the container as env and never appear in argv (which the panel logs and the daemon exposes via `Inspect`). NUL-guarded on both lists, and the guard's error names the entry index rather than the value — an env entry is a secret |
| Stream framing | `internal/modules/runtime/stdcopy.go` | `CopyDemuxed`, the binary-safe sibling of `demux`: `demux` reassembles log lines and strips timestamps, which is right for text and corrupts a dump. Frames are copied in 64 KiB chunks with a 4 MiB per-frame sanity bound, and a first byte that is not 1 or 2 is reported as "not multiplexed" rather than silently treated as payload |
| Archive copy | `internal/modules/runtime/docker_archive.go` | `CopyFileOut` (exactly one regular file or an error; size bound checked on the header before reading; a short read is an error, not a half dump) and `CopyFileIn` (single-file tar upload, `mode.Perm()` only — a backup write cannot plant a setuid binary). Raw GET/PUT helpers because `get` decodes JSON and `call` marshals it |
| Object store | `internal/objectstore/s3.go` | S3-compatible Put/Get/Stat/Delete over Signature V4, path style, no SDK: streaming upload of a pre-computed SHA-256 (a dump cannot be rewound), per-segment URI encoding so a key's slashes stay namespaces, endpoint refused if it carries `@`, and the XML error body (which is the only place `SignatureDoesNotMatch`/`NoSuchBucket` are stated) kept in the error up to 512 bytes |
| Cron | `internal/jobs/cron.go` | 5-field parser and evaluator — `robfig/cron` is not a dependency and pulling one in for one file is not worth it. Vixie day-of-month/day-of-week OR semantics, `dow` 7 wraps to 0, `? L W #` and names refused, `Next` strictly after the instant at minute resolution with a four-year horizon so a never-matching expression terminates |
| Notify | `internal/modules/notify/{module,telegram,discord,email,webhook}.go` | channel CRUD with the mock's exact field messages, sealed configuration that no route reads back (`destination_hint` is computed at creation and is all the API ever shows), and the four drivers behind one `Send`. `Test` composes the mock's wording from the hint; `Deliver` fans a rule's message out and counts what arrived. A delivery failure is a returned result, not an error, because an alert that cannot be sent still has to be recorded. A `FieldErrors` from a driver is the exception and stays an error: it means the stored configuration is unusable, which is a defect to fix, not a outage to tolerate |
| Notify hard edges | same package | no redirects followed (a webhook body is an alert and a signature derived from a secret, and a redirect would send both to a host nobody typed); Discord gets `allowed_mentions: {parse: []}` so an alert containing `@everyone` cannot ping the channel, and is truncated by runes not bytes; Telegram's `{"ok":false}`-with-200 is treated as a failure; SMTP defaults to STARTTLS, `plain` must be written explicitly, and `smtp.PlainAuth` is what refuses to ship a password to a server that has not upgraded |
| Metrics ring | `internal/modules/monitor/ring.go` | `Metric` is the contract's `HostMetrics` field for field, and the table's columns are those fields, so a chart is a SELECT. `SeriesSince` returns a window oldest-first and filters on the ULID id rather than the timestamp column — a bound at the window's opening instant is an index seek, a timestamp filter is a scan of the series to work out what to drop. `Prune` enforces `metrics.max_samples` as a time window (cap × interval) because "keep the newest 20160 rows of each series" costs a 20160-value subquery per series per tick. `crypto.ULIDFloor` was added to make that bound. `PruneOrphans` is the expensive sweep and is called at boot, not on the tick |
| Sampler | `internal/modules/monitor/sampler.go` | one reading per tick: host CPU percentage and network rates differenced from the previous cumulative counters, RAM/disk/daemon RSS from `platform.Host`, container count and per-resource CPU/RAM from `Docker.Stats`. The host point and its container points share a stamp so a chart can overlay them. First sample after a start reports zero rates — it has nothing to difference against, and `/proc` load averages are not the same measurement. Release-role containers are excluded from resource series: a one-off migration container spiking a resource's chart is not the app. Everything measurable is injected (`Host`, `Sample`, `Containers`, `Notify`), so the whole module is testable without a daemon. `Containers` is the two methods measuring needs, not `runtime.Manager` |
| Alert rules | `internal/modules/monitor/alerts.go` | the six-condition catalog is code, so no stored row can invent a condition; rules come back in catalog order and a row whose key is not in the catalog is listed last with its key as its title rather than hidden. Mock-exact messages for the cases the mock names (`Unknown rule. Available: …`, `A rule for "disk_low" already exists`, `Cooldown must be at least 60 s`, `Unknown channel`), and one addition it does not: the three numeric conditions refuse a non-numeric threshold, because a rule that can never fire looks like a healthy host. Disabling a rule clears its firings, so switching it back on does not sit out a cooldown inherited from the previous run |
| Evaluator | same file | disk_low/memory_high read the newest host sample and skip it if it is older than three intervals; container_down compares desired state against running app containers; deploy_failed and backup_failed scan a 24-hour lookback so enabling a rule cannot notify a year of history; cert_expiring uses the edge module's own `domains.cert_expires_at`. Dedupe is per `(rule, subject)`, cooldown from `last_notified_at`, and an incident that stops being detected is deleted so a recurrence after recovery notifies again. `last_triggered_at` moves on detection, not only on delivery — that is what `/system/status`'s `active_alerts` counts, and a rule with no channel still has to show that something is wrong |
| Engine detection | `internal/modules/databases/engines.go` | `Detect` reads the image repository's last path segment against alias sets, so `docker.io/library/postgres:16`, `postgres:16-alpine` and a `mirror-postgres` all read as Postgres while `postgresql-exporter` does not; mariadb belongs to MySQL because `mysqldump` speaks to it and valkey to Redis because the RDB contract is the same. It is a heuristic and documented as one — the alternative is a database resource whose engine is unknown, and that fails a backup anyway. The same file holds `Ops` (the five container methods a dump needs, not all of `runtime.Manager`), `Lookup` (row + config + engine + decrypted vars, refusing an image it cannot name), and `runExec`, which closes the exec stream when the context expires because nothing in the runtime port aborts a hijacked attach — without it a hung dump would outlive its own timeout |
| Dump plans | `internal/modules/databases/{postgres,mysql,redis}.go` | three engines, two shapes. Postgres is `pg_dumpall --clean --if-exists` over loopback TCP with `PGPASSWORD` in the env (a socket connection authenticates with peer credentials, which match only when the exec happens to run as the socket owner) and restores with `psql -v ON_ERROR_STOP=1` so a half-applied script fails instead of reporting success; MySQL is `mysqldump --all-databases --single-transaction --quick --routines --triggers` with `MYSQL_PWD`, using only the flags both Oracle MySQL and MariaDB accept, and avoids the vendor-specific ones by running as the account the image was initialized with; Redis has no stdout dump at all — `redis-cli --rdb` performs the replica transfer into `/tmp`, the file is copied out, and the restore puts it at `/data/dump.rdb` and cycles the container, because a running Redis serves what it has in memory and will not load a file over the top of it. A resource deployed without the password variable its engine needs is refused at schedule creation with that named, rather than failing as "authentication failed" at 03:00 |
| Backup pipeline | `internal/modules/databases/backup.go` | schedule CRUD with the form's exact field messages, `StartRun` (202-shaped: the row opens with its final location already chosen, and a second concurrent attempt on a schedule is refused because two `mysqldump`s of one database on a small host is a memory incident), then `Execute` → dump → **verify** → publish → prune. Compression happens in the panel, not the container: piping a dump through gzip inside argv means a shell string built out of operator-supplied names, which is an injection point on every scheduled backup. Success requires a second read of the closed file — size and SHA-256 from disk, compared with what the write pass counted — and for an s3 destination the store's own `Stat` size is confirmed before the staged local copy is deleted. `Execute` returns nil once the row has a final status even when the backup failed, because the queue retries errored jobs and a second attempt against a database that was down reads as two backups in the history. Retention sweeps delete the payload before the row, and age out failed attempts on the same window so the history cannot grow without bound |
| Restore | `internal/modules/databases/restore.go` | a **verified** pre-restore snapshot first, always to the host's `backups/pre-restore/` whatever the schedule's destination says, and if it cannot be taken the restore does not start — an operator who wants a backup applied over a state they cannot recover from has to say so deliberately, and this panel does not offer it. The payload is read back from disk or the bucket (the recorded size is the read bound: an object that grew past what was verified is not the backup being claimed), gunzipped only if its name says `.gz`, and streamed into the engine's restore step. A file restore ends with stop → start → a bounded wait for the container to report running, so "the RDB was from a newer version and the server refuses to boot" is reported instead of called a success |
| Supporting exports | `internal/objectstore/s3.go`, `internal/modules/deploy/db.go` | `Client.Prefix()` because Put/Get/Stat/Delete apply the configured prefix themselves, so a stored full URI has to be reduced back to the name before it is used (a location outside the prefix is refused rather than silently re-prefixed); `EnvVar.Runtime()`/`Build()` were unexported predicates that `databases` legitimately needs to tell a dump credential from a build secret |

## Next

**P6 — Operations** continues: `monitor` (sampler → `metrics_samples`, alert evaluator), the API handlers for the 14
routes, `databases` + backups (dump via `ExecWithEnv` → `CopyFileOut` → gzip → local or S3, retention, restore), the
daemon loops, and the log-retention pass. Then P7 (extensibility) and P9 (release hardening); P8 is explicitly later.


P5's own leftovers, none of them blocking: the frontend's Source & CI/CD tab (backend parity for it is what the mock
defines, and `web/src/features/git/*` does not exist yet), monorepo fan-out per delivery (schema-level, belongs with
the P7 GitHub App work), and the delivery-row retention question — the history is append-only until P6's log
retention decides how long any row lives.

Still owed before any of this can be *claimed*, in the order the plan puts them:
1. **Phase 4 and Phase 5 tests** — fault-injection over every §3.8 row (crash mid-deploy, reboot before the switch,
   cancel after creation, healthy-then-unhealthy → auto rollback, release-command failure, disk/RAM refusal, hung
   step), plus the P5 security paths that only a test can prove: a wrong HMAC, a replayed delivery id, a
   `[skip deploy]` push, a hook deploying a sibling repository, a clone over `git.max_clone_mb`, and a source whose
   deploy key does not open. Contract conformance against `web/src/mock`, then the frontend's live-mode pass.
2. **Phase 3's host-shell run** on a real systemd (spike E) and its live-mode frontend pass.
3. **P2b/P2c VM matrix**, CI on a runner, signed release artefacts (P9).

