# PROGRESS: codeplo — P0 and P1 met (with two recorded gaps), P2 implemented and gate-verified

Status: **Phase 2 (P2a edge + audit + safe fixes, P2b reversible firewall, P2c guarded SSH) is
implemented, compiled, race-tested and deployed on a real systemd host.** P2c stays behind
`hardening.apply_tier3`, default off, as the plan requires until its VM matrix passes.
Phase 3 (terminal & logs): foundation built and gate-verified; tickets, host-shell helper, Docker
adapter, handlers, daemon wiring, units and installer changes are **written but never compiled or run**
(authored in a sandbox without a Go toolchain). See "Phase 3 — second pass" below.
Evidence table below; full detail in `TEST_REPORT.md`, judgment calls in `docs/DECISIONS.md`.

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

The second pass below is now compiled, gated and committed; what remains of Phase 3 is runtime
verification against real Docker + systemd and the frontend's live-mode pass.

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

**Owed:** the §6.2 P3 runtime verification on a real systemd host with Docker — host shell as a real
sudo operator, refusal with no operator, root off by default and step-up-gated when on (with and
without `/etc/codeplo/allow-root`), close-the-tab ends the PTY (no leftover process), idle timeout,
session limit, an audit row per ticket and per session, container shell, log tail/follow/resume across
a reconnect and the dropped-line notice under a slow reader, and the installer test with seven
artefacts. Everything above is proven against fakes at the seam, not against a real PTY or daemon.
Two claims that only that run can settle: closing the attach connection ends the Docker exec, and the
helper socket's mode lets the unprivileged daemon traverse `/run/codeplo-pty`.

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
- `/resources/{id}/domains` does not check the resource exists (no `resources` table until P4).
- TOTP replay guard in-memory; sqlc scaffolded but unused; `RouteDef.NoCSRF` declared-unused.

## Handoff brief — what Phase 3 still owes (for the next agent)

Phase 3 is **written, compiled, race-tested and committed**. Nothing in it needs re-doing at the code
level; `docs/DECISIONS.md` records every judgment call, including the four defects found in integration.
Read `internal/modules/terminal/terminal.go`, `internal/modules/runtime/runtime.go` and ADR 0006 before
touching this area — they are the types and the privilege boundary everything else compiles against.

**Tasks left, in dependency order**
1. Runtime verification in WSL against real Docker + systemd — the §6.2 P3 exit criteria, listed above and
   not yet met. Two claims in `docs/DECISIONS.md` are settled only by that run: that closing the attach
   connection ends a Docker exec, and that the helper socket's mode lets the unprivileged daemon reach
   `/run/codeplo-pty/pty.sock`.
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

## Next

Phase 3's code is in and green on every local gate. What Phase 3 still needs is the §6.2 P3 runtime
verification in WSL (host and container shells on a real systemd host with real Docker, refusal without
an operator identity, root off by default and step-up-gated when on, close-the-tab ends the PTY, an
audit row per session, logs with backpressure that survive reconnects) and the frontend's live-mode pass.

Phase 4 (projects & deployments) is next in the plan's order and owns migration `0004_workloads.sql`;
the `runtime.Runtime` port and the `codeplo.resource` label are the interfaces it builds on. Its
delegation prompt was handed to that agent on 2026-10-21.
