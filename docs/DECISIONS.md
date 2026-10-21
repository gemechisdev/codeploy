# Decisions

Judgment calls made while implementing Phase 0 and Phase 1. The plan outranks the contract, the contract outranks the mock.

## Process
- **Build verification was partial.** Go 1.22.2 was installed from apt. The module proxy, `golang.org` and `modernc.org` are unreachable from the sandbox, so `go mod tidy` and a real build could not run. Instead everything was compiled and vetted in a scratch copy with GitHub mirrors for `golang.org/x/*`, `go.uber.org/multierr` and `gopkg.in/yaml.v3`, and a stub in place of `modernc.org/sqlite`. That proves the code type-checks; it does not prove the SQLite code paths work, and **no deployable binary was produced**. `go.mod` was lowered from `go 1.23` to `go 1.22` to match the available toolchain; there is no `go.sum`.
- **Spike A ran and FAILED** the zero-drop criterion (see ADR 0003): Caddy v2.8.4 downloaded from GitHub, route add and remove works, but requests were reset during reloads. The spike stays strict; it was not loosened to pass.
- **Spike B has not run:** no Docker daemon in the sandbox. It skips (`t.Skip`), which is not a pass.
- **OpenAPI to Go:** `oapi-codegen` is not wired. Handlers are hand-written against `openapi.yaml`; only the later-phase route stubs are generated (`scripts/gen_stubs.py`). Drift is checked for the TS schema and the stubs.
- **sqlc** is scaffolded (`sqlc.yaml`) but P1 uses hand-written `database/sql`.

## Behaviour
- Setup is bound to a setup-scoped session cookie issued by `/setup/admin`, so later setup steps are not anonymous. Those steps skip CSRF and rely on that cookie plus SameSite=Strict.
- `/setup/panel-domain`, `/setup/operator-candidates` and `/setup/operator` return 501 until P2 (they need Caddy and host hardening).
- Routes of an enabled but unimplemented module return 501 with code `module_disabled`; a disabled module returns 404 `module_disabled`. Both declare their permission.
- Users without TOTP log in with a password only (`totp_required: false`). The owner is gated until TOTP is enrolled.
- Idle session timeout is a fixed 2 hours. The absolute lifetime is the `session_ttl_h` setting.
- `role_permissions` are seeded from Go (`authz.RolePermissions`) on first run only, so later admin edits survive; roles, settings, flags and modules are seeded in SQL with `INSERT OR IGNORE`.
- List cursors for `/audit`, `/users`, `/tokens` are keyset (seq or id), not the mock's offsets.
- Defaults chosen without a source: `hardening_revert_window_s=120`, `disk_preflight_threshold_gb=5`, `session_ttl_h=12`. Response shapes `{items}` for `PATCH /feature-flags` and `{values}` for `PATCH /settings` follow the mock's pattern but were not checked against its return statements.
- Tests use a temp-file SQLite database rather than `:memory:` because the pool uses several connections.

## Deviations from the mock
- Real TOTP and recovery codes replace the mock's fixed demo code. Recovery codes are single use.
- Login rate limiting is a token bucket, not the mock's 5-failures lockout.

## Third pass

- API tests use a temporary SQLite file instead of `:memory:`. The connection pool opens several connections, and each would see its own empty in-memory database.
- TOTP replay: an in-memory `authn.ReplayGuard` rejects a code already accepted for the same subject within 90 s. It is lost on restart, which is bounded by the 30 s step plus skew. A persisted last-used step would close that gap and needs a schema change; deferred.
- The audit tamper test drops the `audit_no_update` trigger first, because the database refuses the UPDATE. This is the intended meaning of tamper-evident: the trigger is defence in depth, and the hash chain is what detects an attacker who has database access.
- `RouteDef.NoCSRF` is still declared and unused (public routes already skip CSRF). Left in place so the setup routes keep documenting intent.
- oapi-codegen runs in `make gen` and CI with `models: true` only, writing `internal/api/oapigen/models.gen.go`. Server stubs are not generated because the route table in `internal/api/handlers_*.go` carries one permission per route and the authz matrix reads it; a generated strict server would hide that. The drift check covers the TS schema, `stubs_gen.go` and the generated models. The generated file must be committed once `make gen` has been run.
- Installer: `codeplod.service` is now covered by the checksum check (it was downloaded but not verified). cosign verification of `checksums.txt` runs when `cosign` and `CODEPLO_COSIGN_IDENTITY` are set; `CODEPLO_REQUIRE_COSIGN=1` makes it mandatory. The certificate identity is not hard-coded because no release workflow exists yet.
- Setup operator step: only existing sudo users are accepted in P1. The operator is chosen from `/etc/passwd` (uid 1000 to 65533, login shell, validated name) and must be in the `sudo` or `wheel` group. User creation needs root and is part of P2 hardening, so `create` mode is refused with a contract error instead of simulated.

## Tester pass (2026-10-20)

Judgment calls and fixes made while making the written code build and run:

- **Tooling.** `oapi-codegen` was pinned to v2.4.1, which cannot parse this OpenAPI 3.1 spec (it dies on the `oneOf [string, null]` nullable form in 86 places, issue #373). The pin was raised to the latest stable, v2.8.0, and `api/oapi-codegen.yaml` `output` was made relative to the generating package (`models.gen.go`) because `go generate` runs from the package directory, so the old absolute-style path produced a nested `internal/api/oapigen/internal/api/oapigen/...` file. Nothing else in the spec was changed for the generator.
- **Windows CRLF in generated Go.** `scripts/gen_stubs.py` wrote with Python text-mode `open()`, which on Windows emits CRLF; `gofmt` then flags `stubs_gen.go` and the CI drift check would see a phantom diff on Windows builds. Fixed by opening the file with `newline="\n"`. The committed `schema.d.ts`/`stubs_gen.go` match the regenerated bytes exactly, so `make gen` twice yields no diff.
- **Two test-guess fixes** (code was right, tests were wrong guesses):
  - `internal/api/api_test.go` created a user with `role: "developer"`; the contract enum is `owner|admin|deployer|viewer` (OpenAPI `Role.key`/`User.role` and the mock's `ROLE_PERMISSIONS`). Changed the test to `deployer`.
  - `internal/platform/operators_test.go` embedded the temp dir (an absolute Windows path containing `:`) into a colon-delimited passwd line, so the home field split. Rewrote the fixture to use a relative home dir plus a `chdir` into the temp dir; assertions unchanged.
- **`/system/status`**: the Go metrics stub omitted `net_rx_kbps`/`net_tx_kbps`, which `HostMetrics` lists and the mock always sends. They are now stubbed to `0` (real values arrive with the P6 monitor module), same pattern as the existing `cpu_percent: 0` / `container_count: 0` stubs.
- **Settings and module seed data** now match the mock: `hardening_revert_window_s=300`, `disk_preflight_threshold_gb=2` (were "chosen without a source" 120/5), `build_concurrency` dropped (in the Go seed but not in the mock and not in the Go patch rules, so it was GET-only and patchable-by-no-one), module `version=1.2.0` (was 0.1.0), templates phase P7 and the databases/templates/mcp descriptions aligned to the mock.
- **Settings audit** now matches the mock: action `settings.updated` (was `settings.changed`), target `settings`/`server` (was `settings`/`settings`), meta `{keys, values}` (was `{keys}`), and a post-commit `flag.changed {key:"settings"}` bus event so SSE clients see settings changes like the mock's clients do.
- **`PATCH /feature-flags` publish timing**: the audit publish happened inside the transaction (pre-commit) while every other route publishes after commit, so an SSE client could see an `audit.appended` seq that a concurrent `/audit` page did not yet contain. The handler now records appended events and publishes them (one per row, like the mock) only after commit.
- **SSE `/events` now matches the mock bus**: each event carries a monotonic counter `id` (SSE `id:` line), a 500-event history, and `?last_event_id=` replay; non-metrics payloads are extended with `server_now`. The OpenAPI's declared `EventEnvelope` (`{id,type,data}` as the `data:` JSON) was out of sync with both the mock and the frontend (which parses `data:` as the payload directly and tracks `lastEventId`); the spec was amended to document the real wire format (CONTRACT.md "tester pass" amendments), the `EventEnvelope` schema stays because the generated TS types use its `type` enum.
- **OpenAPI amendments** (CONTRACT.md): setup routes after `/setup/admin` document the `401` the setup-scoped session cookie produces; `/setup/panel-domain` and `/setup/operator` (create) document the `501 module_disabled` P2 deferral (new `E501`); `PATCH /modules/{key}` documents `409` for disabling a core module; `GET /users` and `GET /tokens` document `limit`/`cursor`.
- **P2a audit slice**: not implemented in this tree — `POST /hardening/audit` exists only as the generated stub (404/501 `module_disabled`). There is no audit-mode filesystem/nftables behaviour to diff, so the "zero changes" check is reported as not run, not pass.

### Mock-vs-implementation difference table (P1 core endpoints)

Status: FIXED this pass / NOTED (no code change; recorded here) / OPEN.

| # | Endpoint / topic | Difference (mock ↔ Go) | Disposition |
|---|---|---|---|
| 1 | GET /setup/state | Go can 429 (IP bucket); the mock's fault injector skips it | NOTED — mock limitation; OpenAPI documents 429 |
| 2 | 405 handling | Mock: `405 not_found "method not allowed"`; Go: unknown method on a known path falls to 404 `not_found` | NOTED — cosmetic, OpenAPI documents neither; recorded |
| 3–7 | Setup routes (enroll/verify/verify-domain/panel-domain/operator-candidates/operator/complete) | Go returns `401` without the setup-scoped session cookie; mock keeps them public and reports pre-state via 404/409 | FIXED (contract): OpenAPI now documents 401; behaviour is the documented setup-session design, kept |
| 5 | /setup/verify-domain | Mock fakes DNS (substring match, hardcoded `host_ips`); Go does a real 5 s-bounded lookup | NOTED — mock-only simulation |
| 6, 8 | /setup/panel-domain; /setup/operator `create` | Mock implements both (200); Go returns `501 module_disabled` (P2: needs Caddy / root) | FIXED (contract): 501 documented via E501; behaviour kept (plan defers both to P2) |
| 7 | /setup/operator-candidates | Mock: 3 hardcoded users; Go: real `/etc/passwd` scan | NOTED — data source differs by design |
| 9 | /auth/login 429 | Mock: 5 consecutive failures → 30 s lockout; Go: per-IP + per-account token buckets on every attempt | NOTED — documented deviation (token bucket), OpenAPI lists 429 |
| 10, 32 | /auth/* 429 coverage | Mock never 429s `/auth/*`; Go buckets `/auth/` per IP plus TOTP per user | NOTED — mock limitation; OpenAPI lists 429 on these paths |
| 11, 16 | /auth/me, /roles `permissions` order | Mock: registry order; Go: sorted | NOTED — order not contract-pinned |
| 12 | /auth/step-up | Go-only: 403 for API-token principals; recovery-code acceptance; 429 bucket | NOTED — OpenAPI permits 403/429; mock has no token principals |
| 13, 17 | /users, /tokens, /audit cursors | Mock: offset cursor; Go: keyset (last id / last seq) | NOTED — deliberate decision (keyset, DECISIONS line 19); OpenAPI now documents the params |
| 14 | PATCH /users/{id} role | Mock accepts any role string; Go enforces the `admin\|deployer\|viewer` enum | NOTED — Go matches OpenAPI; mock is looser |
| 15 | POST /tokens `expires_at` | Go validates future ISO-8601; mock stores unchecked | NOTED — Go is stricter, OpenAPI-neutral |
| 18 | GET /system/status | Go metrics stub omitted `net_rx_kbps`/`net_tx_kbps` | FIXED — now stubbed to 0 |
| 19 | GET /system/version | Values are build-injected (`0.1.0-dev`/`none`/`unknown` without ldflags) vs the mock's pinned demo values | NOTED — normal `make build` always injects; `unknown` only occurs for hand-rolled builds |
| 20 | GET /modules seed | version 0.1.0 vs 1.2.0; templates P6 vs P7; 3 descriptions | FIXED — seeds aligned to the mock |
| 21 | PATCH /modules/{key} | 409 (core module) present in both, undocumented in OpenAPI | FIXED (contract): 409 documented |
| 22 | /feature-flags order | seed order vs `ORDER BY key` | NOTED — both deterministic |
| 23 | /feature-flags audit publish | Go published `audit.appended` inside the tx; mock after commit | FIXED — post-commit, one per row |
| 24 | /settings defaults | `hardening_revert_window_s` 120 vs 300; `disk_preflight_threshold_gb` 5 vs 2; extra `build_concurrency` | FIXED — seeds aligned; `build_concurrency` dropped |
| 25 | /settings audit + bus | `settings.changed` {keys} vs `settings.updated` {keys,values}; no bus event in Go | FIXED — aligned |
| 26 | /events wire format | Go sent raw data, no `id` lines, no resume; OpenAPI said envelope; mock+frontend use payload+`server_now`, counter ids, `last_event_id` | FIXED — code aligned to the mock, OpenAPI amended |
| 28 | /events extra fields | Mock adds `server_now` to data payloads | FIXED via #26 |
| 29 | 401 message text | "Sign in to continue" vs "Not signed in" | NOTED — not contract-pinned |
| 30 | Malformed JSON | Mock: `422 "Body is not valid JSON"` no details; Go: `422 "Validation failed"` + `details.fields.body` | NOTED — unreachable from the frontend client (always sends valid JSON) |
| 33 | Latency/fault hooks | Mock adds 60–200 ms latency and `slow`/`500`/`429` faults | NOTED — test hooks, intentionally absent in Go |
| 34 | /audit/verify | Go also fails on seq gaps; mock checks hash links only | NOTED — Go is stricter, same result on untampered data |
| 36 | /tokens storage | Go stores SHA-256; mock stores the plaintext demo secret | NOTED — Go is correct per the brief; the secret is still returned once |

### Spike findings (tester pass, Caddy v2.11.7 + Docker 29.1.3 in WSL Ubuntu)

- **Spike A stays flaky; the zero-drop criterion is UNMET and the test was not loosened.**
  Runs of `test/spikes/caddy_spike_test.go` (unmodified), same host, same binary:
  **0 failures** in some runs (2082 requests in the final verification run, plus two
  clean runs by the previous agent) and **1–2** "connection reset by peer"/EOF
  failures in others (2/216, 2697, 1638 requests). Caddy v2.11.7 therefore both passes
  and fails this strict test run-to-run, so zero-drop cannot be certified; because a
  flaky test cannot gate a release, the criterion is recorded unmet rather than the
  test adjusted.
- **What actually drops, and why:** an investigation matrix (throwaway probe, not in
  the repo) showed keep-alive clients ride the zero-downtime swap (0 failures with
  keep-alive ON under fast reloads), while a fresh connection per request races the
  swap (57–71 EOF failures with keep-alive OFF). Pacing reloads (1 s gap) reduced
  failures to ~0.004 % (2–8 per ~176k requests). Product consequence: real panel
  traffic is keep-alive, so reload exposure is low; batch or pace config reloads
  rather than hammering them.
- **`grace_period` placement:** it is not a root-level JSON field (unknown field). It
  lives at `apps.http.grace_period` in JSON config, and inside the global options
  `{ }` block in a Caddyfile. At 5 s it had no clear effect on the spike's fresh-
  connection failures.
- **Spike B fixture defect (twice over), now PASS.** `CapDrop:["ALL"]` alone cannot run
  the probe image `nginx:alpine`: its entrypoint's `chown` fails EPERM. Re-adding
  `CHOWN` + `NET_BIND_SERVICE` was not enough either — the nginx master cannot drop
  workers to the `nginx` user without `SETGID`/`SETUID`, so every worker died
  `setgid(101) failed (EPERM)`, the master exited, and the container could never
  become healthy (healthchecks themselves work: a no-CapDrop control reaches healthy
  in ~6 s). The fixture now uses `CapAdd: ["CHOWN","NET_BIND_SERVICE","SETGID","SETUID"]`
  — the minimum the probe image needs; the posture under test (drop ALL, re-add named
  caps, 128 MiB memory cap, pids-limit 128, blue/green healthy-before-stop) is
  unchanged, so this is a wrong fixture, not a weakened test. Re-run:
  `PASS TestDockerPullCreateHealthSwap (19.91s)`, confirmed by a second run against the
  gofmt-clean source (16.82s).
- **Installer `checksums.txt` format is load-bearing.** `deploy/install.sh` verifies
  with `grep -E " (name)$" | sha256sum -c -`, which only matches **GNU two-space, LF**
  checksum lines; CRLF line endings or `sha256sum`'s binary-mode `*name` marker make
  every entry fail. The release pipeline must emit GNU-format (LF) files; the WSL
  installer fixtures did.

## Phase 2 — edge + host hardening (2026-10-20)

Judgment calls and fixes made while implementing P2a/P2b/P2c and deploying them on a real systemd host:

- **Checks are Go-defined, not YAML** (D9 stays open). `internal/modules/hardening/audit.go`
  holds the detector/plan/apply triad for the ten baseline keys; `Env.Root` redirects every
  file read so the whole engine is testable in a temp tree. A YAML loader would only move the
  same code behind a parser until a third-party check actually needs it.
- **Module gating is the DB `enabled` flag plus route declarations in code**, not
  `modules.Registry.Register`. The registry exists and `srv.Reg.Enabled(ctx, key)` is what the
  daemon and every handler consult; nothing self-registers, because a module whose routes are
  in the OpenAPI contract cannot appear or disappear dynamically without breaking the
  route-set equality test. Registering edge/hardening in `Registry` would add an indirection
  with no consumer.
- **`www → https` redirect is emitted even when `force_https` is off** (ADR 0003 follow-up):
  the www alias exists only to reach the apex, and answering it over plain HTTP would serve
  the site on a host the operator never chose. `redirect_www` still controls alias-on.
- **Caddy has no `ExecReload`.** The file on disk is the admin-only bootstrap; a `systemctl
  reload` would load it and wipe every route the reconciler pushed. All config change goes
  through `POST /load` on the unix socket, and the reconciler supervises the unit with
  `systemctl restart` when the API stops answering.
- **The bootstrap config carries no `apps.tls` block.** The renderer injects the ACME `tls`
  policy only when `CODEPLO_ACME_EMAIL` is set (`internal/modules/edge/render.go:89`); a
  placeholder `{env.CODEPLO_ACME_EMAIL}` in the bootstrap made Caddy exit 1 on boot with
  "evaluated placeholder is empty". Operators set the address in `/etc/default/codeplod`,
  which the installer now templates.
- **`deploy/caddy/codeplo-caddy.service` must not declare `StateDirectory=codeplo`.** systemd
  chowns a state directory to the *declaring* unit's `User=`; this unit runs as root while
  `codeplod.service` (`User=codeplo`) claims the same path, so claiming it re-owned
  `/var/lib/codeplo` to root and the panel died on `codeplo.sock: bind: permission denied`.
  The unit only gets `ReadWritePaths=/var/lib/codeplo/caddy`.
- **Tier 1 never arms the dead-man switch.** It cannot cut access (§3.10), so
  `hardening_revert_window_s` applies to Tier ≥ 2 only; a Tier-1 apply records the row as
  `confirmed` immediately. `TestHardeningTier2PendingGuard` was rewritten to hold a Tier-2
  change pending (not weakened) to keep guarding the tiers that can lock the operator out.
- **Ordering rules, applied consistently:** apply = snapshot files → record live-state revert →
  mutate → arm timer → DB; confirm = persist for boot → disarm timer → DB; revert = stop timer
  → restore files → restore live state → DB. Confirm persists **before** disarming so a change
  that would vanish at reboot is never marked permanent, and a failed apply rolls back live
  state through the same `RuntimeRevert` path the timer uses.
- **A failed runtime probe means the state is already gone.** `RuntimeStep.Probe` runs first;
  if the probe fails, `RuntimeRevert` skips the destructive `Apply` argv rather than guessing
  (deleting a table another operator removed, or reloading sshd over nothing).
- **fail2ban Verify reports the daemon before the jail.** The check claims protection is
  *running*; a stopped fail2ban bans nobody, so "not active" outranks a jail-on-disk problem.
  Also `ignoreip = a b` — the `=` is fail2ban's separator, not an address, and the first
  parser treated it as one, which would have hidden a missing operator IP.
- **`updateHardeningChange` appends the change id itself.** Every statement it runs ends in
  `WHERE id=?`, so the id is the last placeholder; callers that forgot it returned a silent
  no-op and confirm/revert 500'd with `missing argument with index 2`. The helper now binds it
  and refuses a 0-row update, so a state-guard mismatch is an error instead of lost work.
- **`install.sh` requires bash and asserts readiness.** The documented `sh install.sh` could
  never work (`set -o pipefail` is not POSIX and arrays are bash-only), so a dash-visible guard
  now says so. The old wait loop only *slept*; a crash-looping panel still printed success. It
  now polls `/api/v1/setup/state` and `exit 1`s with `journalctl` guidance. Verified end-to-end
  in WSL: 15 assertions (unit active, no `NRestarts`, socket `660 root codeplo`, `codeplo` user
  reads the admin API, live config holds the reconciler's `:80`/`:443` servers, panel answers on
  TCP and unix socket, clean journals).
- **The earlier "Installer: all 4 cases PASS" claim was wrong** and is corrected here and in
  `TEST_REPORT.md`: those cases never asserted that the services *ran*, which is exactly why
  three boot-time defects (ACME placeholder, `StateDirectory`, no readiness assert) hid.
- **`make dist` is the only producer of the release artefact set** until the P9 publish job
  exists; it cross-builds amd64+arm64, copies the two units and the bootstrap as
  `caddy-bootstrap.json`, and writes GNU-format `checksums.txt` — the same file list the
  installer downloads, so a missing entry fails the installer rather than shipping a gap.
- **`/resources/{id}/domains` does not verify the resource exists yet** — the `resources` table
  arrives in P4. The route is real (hostname validation, uniqueness, panel-reserved rejection,
  in-tx audit, best-effort reconcile); P4 must add the existence check. Recorded rather than
  silently deferred.
- **Windows portability of the frontend gates.** All four `web/scripts/*.mjs` used
  `new URL(...).pathname`, which yields `/C:/...` on Windows and crashed `npm run lint`; and
  `lint-guards.mjs` compared `relative()` output against `'mock/'` prefixes, which never match
  Windows backslashes. Both fixed (`fileURLToPath`, separator normalization). Until now the
  guards had never actually run on this host, and their exemptions were inoperative when they did.
- **A running service is shared state.** Spike C failed once with 200-empty-body probes because
  the installer fixture had left `codeplo-caddy` *enabled* on the VM; it owned `:80`/`:443` and
  answered the spike's requests. The spike passes with the ports free — the fixture now cleans up
  after itself, and WSL's 8-minute idle teardown means a test run must not span two commands.

## Phase 3 — terminal & logs (2026-10-21)

- **`0003_terminal.sql` takes migration 0003.** The plan names P4's table set
  `0003_workloads.sql`; P3 is built first here, so P4 becomes `0004_workloads.sql`. Goose
  versions are sequential and irreversible-in-order — the file name is the only thing that moves.
- **`terminal_sessions` denormalizes `user_email` and carries no foreign key**, exactly like
  `audit_events`. Session history must survive the user it belongs to being removed; an FK would
  either block that removal or cascade-delete the history.
- **Two settings invented for the contract's "concurrency limit" and "opt-in recording"**:
  `terminal.max_sessions_per_user=3` and `terminal.record_sessions=false`. Three is enough for one
  browser plus a second device and bounds a fork of shells; recording defaults off because a
  transcript contains everything the operator types, including secrets, and no viewer exists yet.
- **The concurrency check is inside the insert transaction.** Two simultaneous WebSocket upgrades
  reading "2 of 3 open" then both inserting would silently exceed the limit; SQLite's
  `_txlock=immediate` makes the count-then-insert pair atomic, so the handler cannot get it wrong.
- **`Store.Close` keeps the first end reason.** Idle timeout, process exit and socket close all
  race to close a session; the truthful one is what actually ended it, and the later write would
  rewrite history.
- **Rows left open by a crash are closed at daemon start (`Store.Recover`).** Otherwise a killed
  daemon permanently spends that user's quota and the panel shows a shell nobody can close.
  Same spirit as P4's "non-terminal deployments become interrupted on boot".
- **`codeplo.resource=<id>` is the P3 container label.** The resources table arrives in P4, so
  the runtime resolves a resource to a container purely by label; P4 must stamp it (the plan
  already mandates `codeplo.deployment=<id>` alongside). Recorded so P4 does not "discover" a
  second convention.
- **`runtime.Runtime` is frozen at four methods** (`Available`, `Resolve`, `Logs`, `Exec`).
  `ErrUnavailable` and `ErrNoContainer` are distinct because they map to different statuses: a
  missing Docker socket is a host fault (500), a stopped container is a resource state (404/409).
- **`go` directive raised 1.22 → 1.23** because `coder/websocket` requires it. CI reads
  `go-version-file: go.mod`, so no workflow pin changes; the local toolchain is 1.27.1.

## Phase 3: handlers, helper and runtime adapter (written in a sandbox without a Go toolchain; unverified)

- **Privilege boundary.** The shipped unit cannot start a shell as another user and a shell from its tree could not
  `sudo` (NoNewPrivileges). Host shells therefore go through a root-side helper, see ADR 0006. This replaces the
  handoff's "setuid via `syscall.SysProcAttr.Credential`" in the daemon, which cannot work under the unit.
- **Root needs two switches.** Daemon side: `terminal.allow_root`, `terminal:root`, fresh step-up (re-checked at
  upgrade, not only at ticket time). Host side: `/etc/codeplo/allow-root`. The mock only models the first set.
- **Step-up only for `as_root`.** `POST /terminal/ticket` carries `x-step-up: true` in OpenAPI, but the plan and the
  executable mock require a fresh step-up only for a root shell. Followed the plan and the mock.
- **Failures after the ticket is spent close the socket, they do not return HTTP errors.** The handler completes the
  upgrade, sends `{type:"error",message}` and closes 1008, matching the mock; a plain HTTP error reaches the
  browser as an anonymous 1006. Failures before the ticket check (module disabled, runtime or helper not wired,
  unauthenticated) stay HTTP, as does the ticket endpoint itself (409 `operator_missing`, 409 container not
  running, 403 root disabled, 422 validation).
- **Session audit is fail-closed.** `Store.Open` and the `terminal.session_opened` audit event are two
  transactions, not one. If the audit write fails the session row is closed and no shell starts. A single
  transaction would need `Store.Open` to accept a `*sql.Tx`; deferred.
- **`recorded` is always false.** `terminal.record_sessions` exists as a setting, but no transcript writer exists,
  so the API never claims a recording.
- **Tickets are bound to Origin** as well as user, target and resource; a ticket burned by a failed binding check
  stays burned. Origin on the upgrade is checked twice: the ticket binding and `websocket.Accept`'s Host match.
- **Container shell command is a fixed constant** (`bash -l` if present, else `sh`); nothing from the request
  reaches it. `as_user` is reported as `container`; the mock shows `app`.
- **Logs.** Container resolved by the `codeplo.resource` label only (no resources table before P4); no running
  container is 404 `not_found` for the log stream and 409 for the terminal ticket. Per-line `id:` is the line's
  unix-nanosecond timestamp, `Last-Event-ID` resumes after it with no tail replay, and the first line after a
  resume is skipped if Docker repeats it. The stream never blocks the Docker reader: a 512-line queue drops the
  oldest line and emits one visible "N lines dropped" line. A clean runtime EOF sends `event: end`; a runtime
  error closes silently so the browser reconnects.
- **Docker adapter limits.** TTY exec only (non-tty returns an error); `Resolve` picks the newest running
  container when a blue/green swap briefly leaves two; `Wait` polls `/exec/{id}/json` every 150 ms; closing the
  attach connection relies on the pty hang-up to end the shell and does not call a kill endpoint.
- **Installer.** Now ships and verifies seven artefacts (adds `codeplo-pty.socket`, `codeplo-pty@.service`), enables
  the socket, and writes a `SupplementaryGroups=docker` drop-in only when the `docker` group exists. Fixtures that
  serve a fake release must carry seven checksum entries.
- **Not touched:** the frontend. `TerminalPane` already sends binary PTY bytes and text JSON control frames and the
  Vite proxy already forwards WebSockets.

## Phase 3 — integration, gate and review fixes (2026-10-21, toolchain present)

The second pass was compiled, formatted and race-tested here. One duplicate `Server.moduleOn`
(already provided by `handlers_hardening.go`) was deleted; `go mod tidy` moved `coder/websocket`
and `creack/pty` out of the indirect block; `gofmt -w` fixed the hand-aligned test files.

- **WebSocket teardown order is a protocol question, not a style one.** `coder/websocket` installs
  `context.AfterFunc(ctx, c.close)` on both the read and the write side, so the *instant* the bridge
  context is cancelled the raw socket is torn down — any close frame or control frame written after
  that never reaches the browser, which sees an anonymous 1006/EOF. The bridge therefore ends in a
  fixed order: claim the reason → write the in-band reason frame → close the shell → send the close
  frame → cancel. Cancelling is last, and `Close` (which cannot finish while the browser's parked
  read holds the library's read mutex) is bounded to 1 s so a browser that vanished cannot pin the
  handler.
- **Idle timeout: error frame plus normal close, not 1008.** The earlier note in this file claimed
  1008; the executable mock (`web/src/mock/install.ts:209-213`) sends
  `{type:"error","message":"Idle timeout: the session was closed"}` and closes 1000 "idle timeout",
  and `TerminalPane` renders exactly that frame. Since a close code is not dependable here (see
  above), the in-band frame is the contract and the close reason is a courtesy. `wsFail` still uses
  1008 — there the socket has no parked reader, and the UI keys "ticket refused" off that code.
- **One end reason wins, and only its winner speaks to the browser.** The idle timer, the shell exit
  and the browser's own close all race to end a session. Without the claim, a shell killed by the
  idle timeout was reported as `{type:"exit",code:0}` — the UI would say the process exited cleanly
  when the server murdered it.
- **Nothing is written to a browser that has already stopped reading.** After its read fails the
  bridge marks the peer gone and skips the reason frame and the close handshake; attempting them
  was what made teardown take seconds instead of returning.
- **Helper exit codes: a broken socket is not exit 0.** `terminal/host.go` now reports -1 when the
  frame read fails, so a lost helper connection cannot look like a clean logout in the panel.
- **`Docker.Resize` carries its own 5 s deadline.** The runtime client has no overall timeout (a log
  stream is long-lived by design), so an unbounded resize request could stall the browser's input
  goroutine behind a hung daemon forever.
- **Three new seam tests** (`internal/api/terminal_test.go`): the idle timeout really ends the shell,
  the process and the session row; the container bridge moves keystrokes into the exec stdin, turns
  a text resize frame into a tty resize and reports the exec exit code; a ticket minted for one
  Origin is refused from another and leaves no session row.
- **Settled later the same day, and it falsified the claim:** that closing the Docker attach ends the exec.
  It does not — measured on Docker 29.1.3, and neither does half-closing the write side, and this daemon has
  no `POST /exec/{id}/kill` route to fall back on. See "Docker exec teardown: measured, not assumed" below.
- **Known residual: no WS keepalive.** A browser that disappears without a close frame leaves the
  server's read parked until the idle timeout fires (default 900 s), so a shell can outlive its tab
  for that long. The idle timer is the current safety net; a ping/pong deadline is the fix if the
  window is judged too wide.

### Docker exec teardown: measured, not assumed (2026-10-21)

`docker.go` used to claim that closing the attach connection ends the exec, because with a tty the daemon is
expected to hang up the pty and the shell to take SIGHUP. Measured against Docker 29.1.3 (API 1.52, containerd
2.2.1, runc 1.3.4) that is **false**, and the alternatives are false too:

| Mechanism | Result on the real daemon |
|---|---|
| Close the attach socket | `/bin/sh` still running minutes later, still in `docker top` |
| Half-close the write side (`CloseWrite`) | still running — a tty exec has no stdin pipe, so EOF has nowhere to go |
| `POST /exec/{id}/kill` | `404 {"message":"page not found"}` at API 1.24, 1.41, 1.52 and unversioned, while `POST /exec/{id}/resize` on the same prefix answers 200. The route is not registered on this daemon at all, so the first fix (call kill) was a silent no-op — and the code read 404 as "benign", which is how a leak hides |
| `^U` then `^D` twice, in band | **exit 0, exec gone** |
| `exit\r\n`, in band | exit 0, exec gone |

**Decision:** `dockerExec.Close()` sends `ttyEOF` = `\x15\x04\x04` first, then half-closes, then asks the daemon
to kill (kept for runtimes that implement it), then hangs up. `^U` precedes the EOF characters because in
canonical mode a single `^D` on a *partly typed* line flushes that partial line — teardown must not execute a
command the operator never finished typing.

Why an in-band gesture rather than something stronger: the panel is unprivileged (`NoNewPrivileges`, ADR 0006)
and Docker offers no per-exec signal on this daemon. Killing the container is not an option (it would take the
workload with it), and `kill`-by-pid from inside the container needs a shell in the image and writes into the
workload's filesystem. An interactive shell is what the terminal runs, and the terminal's own EOF gesture is
exactly what a user closing a tab means to a shell.

What this does **not** fix: an exec that ignores stdin (a build step, a script that never reads input) will not
exit on EOF, and this daemon cannot be asked to kill it. The deploy engine in P4 must therefore not assume the
runtime can interrupt an exec — it needs its own cancellation (per-step timeout plus stopping the container it
created), and `runtime.Exec` should grow an explicit cancel path or a documented "cannot interrupt" limit
before P4 relies on it.

The behaviour is pinned without a daemon in `internal/modules/runtime/docker_exec_test.go` (a stub Docker Engine
API server on a unix socket that answers 404 for kill like 29.1.3 does), and proven with one on a real daemon in
`test/spikes/p3_runtime_spike_test.go`: the exec ends with code 0 and the container's process table afterwards
holds only its own command.

## Phase 4 — projects, resources and the deployment engine (2026-10-21)

**Resource status is derived, never stored.** `resources` has no `status` column, so `GET /resources` computes it
in SQL from `desired_state`, `active_deployment_id` and the existence of an in-progress deployment. A stored status
would be a second truth that can disagree with the routing pointer — and the disagreement would be the kind that
shows a green badge over a domain that is not serving. Every screen now reads the same derivation the edge reads.

**API-triggered container work is a durable job, not an inline call.** `stop`, `delete` and `cleanup` enqueue
`deploy.reap` *inside the same transaction* that changes the row. A request must not hang on a Docker daemon that
is slow or down, and a half-finished stop must be retried after a daemon restart; the queue gives both, and the
job is idempotent (label-driven, ignores `ErrNotFound`). Stop additionally calls `reconcileAfter()` immediately,
so the route drops without waiting for Docker to confirm the container is gone.

**Restart and rollback reuse bytes, they do not re-resolve a tag.** Starting a stopped resource or rolling back
creates a normal deployment whose image is the locally retained `codeplo/<slug>:<deployment-id>` of the deployment
that last served, and `Dep.ReusesImage()` is therefore keyed on that pointer (`RollbackOf != ""`) rather than on the
trigger. A mutable registry tag can have moved since the last deploy; reproducing "what actually ran" is the point
of both operations. The engine's acquire step keeps one branch for both by skipping pull and build entirely.

**A volume's directory is created when the volume is created.** If the host cannot prepare the path, the request
that asked for it fails — not a deployment minutes later, after the user has moved on. `EnsureVolumeDir` makes the
resource tree `0751` (no other tenant can traverse into it) and the leaf `0777`, because the container mounts it as
an arbitrary uid — the image's own user, or a remapped one — which has no relationship to the panel's user.
Deleting a resource never deletes volume data: a delete is a metadata operation, and the bytes are the part the
operator cannot recreate.

**The edge keeps a last-known-good upstream, and distinguishes "stopped" from "not yet known".**
`domains.upstream` is written in the *same* transaction that flips `resources.active_deployment_id`. `sites()` then
splits the two cases that both look like an empty derivation: a resource with `desired_state='stopped'` gets no
route (the operator stopped it deliberately — routing to it would be a 502 dressed as a site), while a running
resource whose derived lookup is momentarily empty keeps serving the cached address. A transient DB state that
drops a live route is an outage the panel caused itself. Related: Docker renumbers container addresses when the
daemon restarts, and the container that survives is exactly the one serving traffic, so boot recovery re-inspects
each running resource's active container and repairs the recorded address before the first reconcile.

**Deployment logs stream from the database, with a poll behind the event bus.** The bus is deliberately lossy
(a full subscriber channel drops). A UI tab that silently lost build lines is worse than a 2s poll, so
`GET /deployments/{id}/logs` reads `deployment_logs` and uses the bus only as a wake-up. It ends with
`event: end` once the status is terminal, so browsers stop reconnecting to a finished deployment.

**`cancelling` is not a status.** §3.8's prose names a `cancelling` state; the contract's enum does not contain it
and the mock never renders it. The implementation keeps the two facts that state was standing for —
`cancel_requested` plus an in-memory cancel of the running step — and the visible status stays `running` until the
step observes the cancel and writes `cancelled`. Inventing a status to match a paragraph would break the parity
test that exists to keep API and UI honest.

**The panel never claims a hardening control the host does not have.** `platform.AppArmorEnabled()` reads
`/sys/module/apparmor/parameters/enabled`; only when it says `Y` does the engine ask Docker for
`apparmor=docker-default`. On a host without AppArmor, claiming the profile produces a container that cannot start,
which is a worse outcome than the missing defence the claim pretended to add.

**Contract additions follow the implementation.** `ResourceConfig` in `api/openapi.yaml` gained
`release_command`, `rollout` and `published_ports` — all optional, all already honoured by the engine (release
container before the switch; `stop_first` rollout for hosts too small for blue/green; owner-only 127.0.0.1 port
publishing, audit-flagged). `make gen` was re-run; the route set is unchanged, so `stubs_gen.go` did not move.

**Daemon start-up order is load-bearing.** Queue handlers are registered before `Jobs.Run` (the queue reads the
handler map while running), and `Engine.Recover` runs before the first deployment is claimed, because "every handler
can be re-run safely after a restart" is only true once the crashed run's leftovers have been accounted for.
