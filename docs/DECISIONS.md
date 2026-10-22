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

## Phase 5 — Git & CI/CD (2026-10-21)

**The git rules live in `internal/modules/git`, not in the handlers.** Every domain decision — validation message
wording, HMAC comparison, gate order, token formats, tag-policy matching — is a function in that package, and the
handlers only translate between HTTP and it. Two copies of a security rule is how one of them stops being enforced,
and the clone step in the deploy engine needed the same limits the panel writes.

**A deploy key is replaced, not added, and only the public half is ever readable again.** `deploy_keys` has
`UNIQUE(resource_id)`, matching the mock and the UI: two keys on one resource would leave no way to say which one
the source uses, and the panel's "copy this into GitHub" step becomes ambiguous. The private half is written once
into `resource_credentials` (kind `deploy_key`, XChaCha20-Poly1305 under the master key) and no endpoint returns it
— the contract has no reveal path, and adding one would undo the sealing. `GetDeployKey` reads the row that carries
`public_key` and `fingerprint` only.

**The webhook secret is stored sealed, the hook token is stored hashed.** They protect different things. Verifying a
GitHub signature requires *recomputing* the HMAC, so the receiver must be able to open the secret; a deploy-hook
token only needs to be recognised, so it is stored as SHA-256 and looked up by hash — a database dump then yields
nothing usable, and a token compare happens in the index instead of a constant-time loop over every resource's
secret. Both expose only a 9-character prefix.

**Replay protection is a unique index claimed inside the deployment's transaction.**
`webhook_deliveries UNIQUE(provider, delivery_id)` plus `ClaimDelivery` means two concurrent retries of the same
delivery race in the index and the loser rolls back and answers 409 — neither deploys. Check-then-act ("has this id
been seen?") would let both through, and GitHub does retry. The claim and the `deployments` insert commit together,
so a delivery can never be marked consumed by a deployment that did not happen.

**Anonymous garbage does not get to write delivery rows.** The mock records a `rejected` delivery for every bad-signature
post, including one that names no configured repository. This endpoint is public and rate-limited to 30 rps, so
recording unattributable posts turns the delivery history into a disk-filling target and a flood that hides the real
rejections. A post naming a repository this panel *does* serve still records `Invalid HMAC-SHA256 signature`, and an
unknown deploy-hook token still answers 404 without a row.

**The image policy boundary is the colon, not a string prefix.** `ghcr.io/org/app:*` must not accept
`ghcr.io/org/app-evil:latest`, which a naive `strings.HasPrefix(image, policy[:-1])` does. `MatchImage` requires the
repository to be exactly the policy's repository, then a tag with no `/` and no further `:` (an optional `@digest` is
allowed). Same reasoning as `ConfinePath` in P4: a policy that is 95% right is not a policy.

**A webhook deploy builds on the host; it never takes an image reference from the payload.** The gate that decides
*whether* to deploy reads the payload; the *what* comes from this host's own configuration — the resource's source,
branch and Dockerfile, producing `codeplo/<slug>:<deployment-id>`. The mock puts `cpl-<slug>:<sha>` in the request;
honouring an image name supplied by the caller would let a validly-signed push for any branch pick which bytes run.
Registry-pull deploys are what the *deploy hook* is for, where the CI built the image and the policy check is the
only thing deciding whether it is allowed.

**One delivery deploys one resource.** `webhook_deliveries.resource_id` is a single column, so the schema — not the
code — says a delivery belongs to one service. When several resources share a repository the lowest id wins
deterministically. Monorepo fan-out (one push, N services, one row each) is a schema change and belongs with the
GitHub App work in P7.

**Saving a source checks that the source can be built, not only that the request is well formed.** `VerifySource` is
a production-only pre-flight: a `deploy_key_git` source whose sealed key is missing, unopenable by this master key,
or not parseable by `ssh.ParsePrivateKey` is refused with `This source cannot be built: …` when it is saved, instead
of failing a minute into a build with a git error nobody can read. The mock has no such check because it has no
builder.

**A source write patches `config.image` and nothing else.** `resource_sources` and the engine's `config_json` are two
views of the same decision (build vs pull, which Dockerfile, which context), and they must not drift. `syncImageConfig`
rewrites exactly that subtree, so setting a Git source cannot silently reset a port, a health check or an environment
the operator configured.

**Clone bounds are enforced by the builder and read from one place.** The engine calls `git.ReadCloneLimits` for
`git.clone_timeout_s` and `git.max_clone_mb`, gives the clone its own `context.WithTimeout` (so a stalled connection
fails *this* deployment after the timeout rather than holding the worker until the build budget is gone), and
`CLI.Clone` measures the checked-out tree afterwards and refuses it with `ErrSourceTooLarge` if it is over budget.
`--depth 1` bounds history, not size: a repository of large binaries clones fast and then fills the disk at the
build, which is the failure the setting exists to prevent.

## Phase 6 — Operations: metrics, alerts, notifications, backups (2026-10-21)

**`metrics.max_samples` is enforced as a time window, not a row count.** "Keep the newest 20160 rows of each series"
costs a 20160-value subquery per series per tick, or an OFFSET scan of the same length. The same policy as an age —
cap × sample interval — is one ULID index seek to a bound and a range delete after it, so it costs what it removes and
nothing else. The consequence is stated rather than hidden: change the sample interval and the history window changes
with it, because the cap the operator configured is in samples. Window filtering is on the `id` column for the same
reason, which is why `crypto.ULIDFloor` exists — a range bound that is never stored.

**Rates are differences, and the first sample after a start has nothing to difference against.** CPU percentage and
network rate report zero for that sample. Carrying `/proc`'s load averages or the previous process's numbers in from
disk would put two different measurements in one series and make the chart's first point a lie.

**`net_rx_kbps` is kilobytes per second, not kilobits.** The contract names the field, the mock computes
`bytes / 1024 / seconds`, and a panel that quietly redefines a unit its own UI graphs is worse than a panel with a
slower graph. The name stays ambiguous and the code does not.

**A delivery failure is a result, not an error.** `notify.deliver` returns `ok:false` with the provider's sentence when
Telegram is down, because an alert that could not be sent still has to be recorded and shown — the incident happened,
the message did not. The exception is a `FieldErrors` from a driver: that means the stored configuration is unusable,
which is a defect to fix and not an outage to tolerate, so it propagates as an error.

**Alert rules are a code catalog, and three of the six numeric conditions refuse a non-numeric threshold.** The mock
does not check, and a rule that can never fire looks exactly like a healthy host. This is the one place the
implementation is stricter than the stub, and it is deliberate.

**There is no alert SSE event.** `api/openapi.yaml` lists the `x-events` stream and `alert` is not in it; the panel
learns about firings from `/system/status`'s `active_alerts` and from the notification channel that was configured.
Inventing an event the contract does not carry would make the live client depend on something the mock never had.

**The cron parser is 5-field and local to `internal/jobs`.** `robfig/cron` is not a dependency and pulling one in for
one file is not worth it. Vixie semantics are kept where they matter: day-of-month and day-of-week OR when both are
restricted, `7` folds to `0`, and `? L W #` and month names are refused with a message naming the token rather than
accepted and misread. A resource's job is an **argv array**, never a shell string, so `command_json` is what runs.

**A dump is compressed by the panel, not by the container.** The only way to pipe `pg_dumpall` or `mysqldump` through
gzip inside one argv array is to build a shell string, and the database name and user in that string are
operator-supplied variables. Interpolating them would be an injection point that fires on every scheduled backup, so
the stream is gzipped on its way to the file and the exec's command stays a plain argument list.

**The dump password goes in the exec's environment and never in argv.** `ExecWithEnv` exists for this: argv is what the
panel logs, what `ps` shows inside the container, and what the daemon's `Inspect` returns, while `PGPASSWORD`,
`MYSQL_PWD` and `REDISCLI_AUTH` are what the tools read by design. The same rule holds for the `codeplo cron` runner's
per-job environment.

**A restore takes a verified snapshot of the current state first, and refuses to start without one.** The plan says
restores create a pre-restore snapshot; the decision is that it is not skippable. The snapshot is written to the host
under `backups/pre-restore/` even when the schedule's destination is s3, because an operator standing at a broken
database needs a file on the machine they are typing on, not an object in a bucket.

**`backup.status` cannot be marked successful on intent.** A run succeeds only after the payload has been re-read from
its final path and its size and SHA-256 match what the write pass counted, and — for an s3 destination — after the
store's own `Stat` confirms the length. A dump truncated by a full disk has the intended size in memory and not on
disk; that is the failure this whole pipeline exists to prevent, so verification is the second read, not the first.

**Redis restores by replacing `/data/dump.rdb` and cycling the container.** A running Redis serves what it has in
memory and will not load a file placed beside it, and the panel cannot introspect `dir`/`dbfilename` on an arbitrary
image. The official Redis and Valkey images both start at `/data/dump.rdb`; a deployment that overrode it is not the
deployment this panel created from a template, and the post-restart wait for the container to report running turns
that into a stated failure instead of a silent success.

**Engine detection is a documented heuristic on the image's repository name.** Matching the last path segment against
alias sets (`postgres`/`postgresql`, `mysql`/`mariadb`, `redis`/`valkey`) reads `docker.io/library/postgres:16` and a
`mirror-postgres` correctly and does not read `postgresql-exporter` as a database. The panel cannot introspect an
arbitrary image's contents, and the alternative — a database resource whose engine is unknown — fails a backup
anyway, so the guess is made visible in the error that follows.

**`backup_schedules.enabled` was dropped from 0006 before it was ever applied.** No contract route can set or clear it,
so the column would have been a claim about a capability that does not exist. `ListSchedules` lists what is there.

**"Databases bind to the internal network only unless explicitly exposed" is already the runtime's policy, not a
databases-module rule.** Published ports are owner-gated, bind to `127.0.0.1`, and are recorded in `config_json` as
`published_ports`; a container with none is reachable only on `cpl-env-<env>`. Re-implementing it in the backup module
would give two places the same promise, and one of them would drift.

**`databases.Ops` and `monitor.Containers` are narrower than `runtime.Manager`.** Each port declares the methods that
package actually calls. It keeps the monitor wireable on a host where container creation is refused, and it means a
test fixture is five methods rather than a lifecycle.

**Three error mappers in the API layer, not one.** `notify`, `monitor` and `databases` each declare their own
`FieldErrors`/`NotFound`/`Conflict`, because a module must not import another module's vocabulary to say what failed.
The cost is three near-identical 15-line functions; the alternative — a shared `internal/apierrs` package the modules
import — would put HTTP semantics inside the modules, which is the dependency the phase plan refuses.

**A channel test answers 200 with `ok: false`.** A rejected bot token is information the operator acts on in the form,
and an error envelope would hide the provider's reason behind a generic failure. The same reasoning makes a disabled
channel a refusal in the detail rather than a 409.

**Deleting a channel rewrites the alert rules in the same transaction, through `PatchRule`.** `alert_rules` belongs to
the monitor, and the notify module's own docstring says the handler prunes it; writing the JSON column from the API
layer would let two owners hold one invariant. `Deliver` already skips a vanished channel, so this is hygiene rather
than a crash guard — and it is why a daemon built without the monitor can still delete channels.

**`POST /backups/{id}/run` returns 202 with the row, and the dump happens in a job.** A real `pg_dumpall` outlives any
sane HTTP timeout, and an operator who is watching the run list does not need a request held open to learn the answer.
Opening the run and enqueueing `databases.JobRun` share one transaction, so a daemon that dies between the two cannot
leave a promised backup unqueued; the payload is the run id because the row already names the schedule, the destination
and the start time.

**Restore reports an unknown run (404) before it asks for step-up.** The contract puts `x-step-up` on the route, but the
mock's order is lookup → status → step-up, and a client that was told "backup run not found" learns something it can act
on while one told `step_up_required` learns nothing. The module's own checks run again inside `Restore`, so the shortcut
is a message order, not a skipped guard.

**`/system/status` reads its metrics from the newest stored sample instead of taking a fresh reading.** CPU percentage
and network rate are differences between two `/proc` snapshots, so a live read has no denominator and would report zero
where the ring buffer has a real number. Before the sampler's first tick there is no sample, and the host's instantaneous
readings stand in — which is the honest fallback the all-zero placeholders were not. `security_score` and
`pending_changes` are two stored-state counts (last audit run, unconfirmed hardening changes) rather than a live
hardening run, because the status endpoint is on every page load and must never shell out.

**Operations services are built before the job queue starts, and before the deploy engine.** Two reasons, one order.
`databases` registers a queue handler and owns boot recovery (`Recover` closes attempts a dead process left
`running`), and both have to finish before a worker could pick up work; the deploy engine then needs the databases
service as its pre-deploy hook. Wiring them after `go srv.Jobs.Run(ctx)` would mean a crash-recovery race and a
nil hook, and neither shows up in a compile.

**The Docker gate grew to `monitor` and `databases`.** Sampling a host's containers and dumping a database both need
the socket, and neither needs permission to *create* a container — which is why `monitor.Containers` and
`databases.Ops` are two-method and five-method slices of `runtime.Manager` rather than the whole port. A panel with
monitoring on and deploys off still has to know how many containers are up.

**Every loop re-reads its own interval from `settings` on each pass.** A ticker built once from a setting is a
setting that needs a daemon restart, and the settings page is the only place an operator can change it. The cost is
one indexed `SELECT` per tick against a table the tick is going to write anyway.

**The minute loops align to the clock instead of starting a 60-second ticker.** `backup_schedules.cron` and
`resource_cron_jobs.cron` are minute-resolution, and a ticker that drifts to 12:00:59 evaluates minute 12:00 while
the database means 12:01. Each pass gets a 55-second context so a hung sweep cannot outlive its own minute or hold
shutdown.

**A half-configured `CODEPLO_S3_*` set refuses to boot.** The alternative is a daemon that comes up "successfully"
and starts writing dumps to a destination the operator thinks is off-host. The error names the specific variables
missing rather than the values present, so it is safe to paste into a ticket.

**Pre-deploy backups run before the release command, not before the switch.** A release command is where migrations
run, and the snapshot's purpose is that the data the *previous* version was serving reaches disk before anything new
can change it. Putting the hook in the switch step would have made every `--dangerous`-style migration an
unrecoverable event, which is the accident this feature exists to prevent.

**`PreDeploy` is a function field on `deploy.Deps`, and nil is a logged pass.** Making the deploy module import
`databases` would invert the dependency for a safety net, and refusing to deploy when an optional module is switched
off is a worse failure than deploying without the snapshot. The step writes which of the two happened into the
deployment log, because "was my database backed up before that deploy?" has to be answerable from the log alone.

**Log retention deletes whole deployments, never lines inside one.** A truncated middle would be read as "the build
printed nothing after line 400". Active and retained deployments are exempt from both passes: they are the two logs
an operator is actually pointed at, and if the budget cannot be met without touching them the pass warns instead of
cutting into a log still in use.

**A cron job's output is redacted before it is stored, and stdout and stderr are merged.** `cron_job_runs` is a
plaintext table and a maintenance command sees its container's environment, so the resource's own secret values are
masked out of the tail on the way in. Merging the two streams is what a history row needs: the lines of one failure
in the order the process wrote them, not two columns a reader has to interleave by hand.

**`codeplo cron` is dispatched before the shared CLI flag set parses anything.** `cron add` carries its own flags and
an argv after `--`, and the flag package would stop at `find` and then exit on `-mtime`. One early dispatch keeps
the command word intact, the same reason `pty-helper` is dispatched there.

**P7's contract surface is two routes, so the rest of the phase is not routes.** `api/openapi.yaml` gives templates
`GET /templates` and `POST /templates/{key}/deploy` and gives extensions, the MCP server, PR previews, commit status
and the GitHub App provider nothing. Adding routes for them would mean regenerating the stub table, the authorization
matrix and the mock parity tests to match a contract nobody wrote. They are built the way K2's cron was: behind the
CLI, the daemon's own loops, and outbound calls to services that asked to be called.

**A template is listed only when this panel can deploy it.** One resource is one container here, and a resource
config carries an image, a port, limits and an environment — not a `command:`, an `entrypoint:` or a `depends_on:`.
A multi-service file, or a single-service file that relies on any of the three, is refused with its own words rather
than listed and half-run: an operator who clicks Deploy on a card that produces a container which is not the one the
template describes has been shown a lie, and the catalog has no field for "not yet". This is why the shipped catalog
has adminer where the mock's demo data had Plausible — Plausible Community Edition is a ClickHouse cluster wearing
one logo, and it will come back with a Compose runner.

**A template body is checked by the isolation policy, not by a copy of its rules.** `policy.ParseCompose` and
`policy.Enforce` are what a hand-written compose file already goes through, so importing a third-party template
cannot bring a privileged container, a host bind, the Docker socket or a published port in through the catalog, and
the refusal an operator reads is the policy's own sentence rather than a paraphrase written twice.

**Every credential a template needs comes from the panel, and a template that supplies its own is refused.** The
deploy request carries a project, an environment and a name — no fields for template values — so the only honest
answers for `${POSTGRES_PASSWORD}` are "the panel generates it" or "this template cannot be deployed here". The
parser therefore refuses an empty credential placeholder, a credential with a literal value in the file, and a
generated name that also carries a default; it strips placeholders before looking, so `hunter2${CODEPLO_PASSWORD}`
still reads as a shipped secret. Coolify's `SERVICE_PASSWORD*` names are recognised as the same magic, which is what
makes an imported template's secrets arrive already generated.

**The catalog is re-read on every list, and a drop-in file replaces the bundled template with the same key.**
Importing a template is writing a file into `<DataDir>/templates`; a panel that needs a restart to notice has
already answered "not found" to the operator who added it, and the catalog is five small files rather than something
to cache. Replacement rather than collision is how an operator pins an image tag without forking the panel.

**`codeplo templates check` exists because an issue has no route.** The contract's `Template` object is closed, so
"why is this file not in the catalog?" cannot be answered over HTTP, and a template that is not deployable is not
listed to explain itself. The CLI prints the reason for every refused file and exits non-zero when anything was
refused, which makes an import reviewable from CI. The bundled data sits in a root `templates` package for a
similarly mundane reason: `go:embed` cannot reach up out of its own directory, and the plan puts the data there.

**A template that asks for its own address is refused, not listed.** `${CODEPLO_URL}` and Coolify's `${SERVICE_FQDN}`
mean "the https address the panel routes this resource to", and a template deploy has none to give: the contract's
deploy request carries a project, an environment and a name, and a resource only gets an address after a domain row
exists and a deployment has given it an upstream. Listing such a template would show a Deploy button that can only
fail, so `classify` refuses it with the sentence an operator can act on ("write `${SERVICE_FQDN:-https://…}` with the
address you route it to"). Pull-request previews (G8) need the same missing thing — a base domain — and when that
setting exists these two placeholder families become fillable again from the same code path: `Plan` already accepts
the value, and the refusal is one branch.

**A compose-*kind* template becomes an app resource.** `resources.type=compose` is the column the multi-service
Compose runner owns, gated by `deploy.compose`; a compose-kind catalog file that survived the parser is one
container, and calling it `compose` would promise a runner that nothing is running. The route still refuses it when
the flag is off, because the operator switched Compose support off and this entry came from a Compose file.

**Everything a template deploy creates is created in one transaction.** `POST /resources` and the template route now
share `insertResource`: resource row, source, first domain, the template's named volumes, its generated credentials
sealed into `resource_env_vars`, the audit row and the queued first deployment. A database that comes up without its
password, or with a volume row missing while its directory exists, is a workload the operator cannot repair from the
panel — and the two routes cannot diverge while each has its own copy of the insert. The `template.deployed` audit
row is deliberately the one thing outside it: it is evidence about where a workload came from, and losing it must not
unmake a deployed database.

**A database is observed by connecting to its port, and it is deployed with nothing else running.** Postgres, MySQL
and Redis answer protocols the panel does not speak, so `run.probe` uses `Config.ProbeTCP` for `type=database` and
the health path for everything else; it also returns the phrase the deployment record shows, so a database reads
"accepting connections" rather than the "200 OK" it never sent. `prepare` used to gate every non-app resource at
preflight ("only app resources are supported in this build"), which would have failed every catalog database minutes
after one-click deploy said it was working, so `database` joins `app` there. Its rollout is `stop_first`, because two
servers writing one data directory is corruption, not a blue/green deploy.

**The GitHub App is environment configuration, not a `git_connections` row.** The contract's `GitConnection` enum
already admits `github_app`, so a row would have been legal — and wrong. An App is the daemon's identity, not a
connection an operator names: its private key is the file GitHub hands out, and any route that could write one after
boot is a way in for whoever holds `panel:admin`. `CODEPLO_GITHUB_APP_*` is the same shape the S3 destination already
took, it fails at boot where an operator is watching, and it keeps the `/git/connections` list meaning "things a
human created here" instead of mixing in a process-level credential. The App therefore has no route of its own either:
its repository listing is `codeplo github-app repos`, built where `codeplo cron` and `codeplo templates` live.

**An installation token lives in memory and nowhere else.** GitHub's token is a real credential with an hour to
live, and sealing it into `resource_credentials` would give every reader of the database and the master key a way to
clone every repository in the installation — and a copy that keeps working after someone revokes the installation in
GitHub's UI. The in-memory cache dies with the daemon; a restart costs one API call, and the JWT that buys the token
is signed per exchange and never written down. The CLI reports a token's length and expiry and never its value,
because a credential in a terminal transcript is a credential in a shell history file and a support ticket.

**The App is the last clone credential, not the first.** Precedence in the build step is deploy key, then the
resource's stored token, then the App — and only for a repository on the App's own forge. An App installation is an
organisation-wide grant, so letting it win over a credential the operator pinned to one repository would silently
change which identity reaches that code, and a clone that succeeds against the wrong identity is a build of the wrong
thing rather than a failure. A repository outside the installation is answered "not mine" and clones as it always
did; refusing it would break public repositories that need no credential at all.

**App deliveries are verified with the App's secret, second.** One installed App posts every repository's events to
one URL, signed with one secret, so a resource that has no webhook of its own is not evidence that nothing signed the
payload. `/webhooks/github` tries the per-resource secret first and the App's secret after, over the same exact bytes
and the same HMAC rule — nothing about signature verification weakens, only which held secret may satisfy it, and a
panel without `CODEPLO_GITHUB_APP_WEBHOOK_SECRET` behaves exactly as it did before the App existed.

**A commit status never contains the deployment's error text.** The panel's own failure message is git output,
compiler stderr, a registry address, sometimes a line a build script echoed — and a commit status is readable by
everyone who can read the repository, including whoever forks it. So `status.go` publishes a fixed sentence per state
with the resource's slug in it, and points at the deployment log the panel already redacts. The slug is re-checked
against the API's own name rule before it crosses the network because it is free text in a public page and its row may
predate that rule; the resource *id* is never published at all — it identifies a private panel, and a repository's
status list is not where it belongs.

**Commit statuses are read from the deployment row, not handed down from the engine.** Every caller (`finish`, the
build step's pending marker, boot recovery) re-reads `status`, `commit_sha` and the source's `repo_url` before it
reports. In-memory state would be a guess about a row another path may have finalized: observe writes the success, the
API writes the cancel, and recovery writes the interrupt — often while the engine still believes it is mid-build. A
status that disagrees with the panel's own record is worse than no status, and a deploy that has already finished does
not need the engine's opinion of it.

**An interrupted deployment reports GitHub `error`, not `failure`.** The distinction is GitHub's own and it matters:
`failure` says the commit did not work, and a branch-protection rule will act on that. `error` says the system that
was checking the commit stopped, which is exactly what a crash or a daemon shutdown mid-deploy is. This is why
`interrupt()` reports — a check left `pending` after a crash means a "still running" spinner in the repository
forever, and nothing but boot recovery can tell a slow build from a dead process.

**The report is best effort and bounded, in that order.** Ten seconds on a `WithoutCancel` context, one line in the
deployment log either way, and no error path back into the deployment: by the time the panel reports, the deploy has
already happened, and a GitHub outage that failed the deployment would be the panel blaming the commit for the forge
being down. `Deps.ReportStatus` is nil when the daemon was started without an App, so a panel that has no App makes
no outbound call and no attempt to explain one.

## Phase 7 — pull-request previews (G8)

**A preview is an ordinary resource, and its bookkeeping row has no foreign key on purpose.** Every other table here
references `resources`, because a dangling child row is normally a bug. `pr_previews` is the exception: a CASCADE on
the parent would delete the row that says the preview is disposable while the container it describes keeps running,
and a RESTRICT would make deleting the real service fail with a constraint error that reads as a broken delete rather
than as a preview that has to go first. An FK on the preview side is the same trap from the other direction — an
operator who deletes the preview from its own resource page is entitled to, and the surviving row is the one that
still has the container to reap. So the sweep reconciles against reality (parent gone, resource gone, clock out)
instead of trusting a pointer.

**A pull request from a fork never gets a preview.** A preview is built from the parent service's own configuration —
its environment and its sealed credentials copied, because that is the only way the branch runs as it would in
production — so running outside contributors' code with them would hand them the database password for the service
they are proposing to change. The refusal is what makes the copy acceptable, which is why it is checked in the gate
rather than left to a policy note: GitHub's own `fork` flag and a canonical comparison of head against base, either
one enough. A preview shows a *reviewer* their own team's branch; it is not a sandbox for third-party code.

**A draft is refused, and teardown is not.** An author who has not asked for a review has not asked for the branch to
be run. But `converted_to_draft` and `closed` take the preview down whatever the request said before, because "this
branch is not safe to run" is never a reason to leave something running. So the gate orders its checks so the
teardown branch returns before the build refusals, and takes nothing that the build cares about (base branch, fork,
title marker) as a precondition for cleaning up.

**An unusable head is cleared at parse, not refused.** ParsePullRequest refuses only a delivery with no pull request
object and no number — those describe nothing the panel could key a row by. A head ref that is not a legal branch
name, and a sha that is not a sha, are set to empty and the gate refuses the *build* with a reason: a payload whose
branch name is garbage cannot become a clone argument or a commit-status URL segment, and a `closed` delivery that
carries one still has to reach the teardown path. Refusing at parse would strand the container until its TTL expired.

**The switch is a feature flag; the limits are settings.** `git.preview_deploys` is off by default and not
release-gated, because the thing that makes previews dangerous is not the code path, it is running unreviewed code on
this host — and an operator who turns it on has said they want that. The three caps (`git.preview_ttl_hours`,
`git.preview_max`, `git.preview_memory_mb`) are settings because they are tuned against one repository's traffic, not
decided once: the same reason the engine's build timeout is a setting, and the reason the API validates each range.
Neither does anything without `CODEPLO_PREVIEW_DOMAIN`, because a hostname under a base domain that has no wildcard
record is a route that answers nothing.

**A preview's volume directory goes with it. A service's does not.** Deleting a resource has always left its bind
mounts under `<VolumesRoot>/<slug>/` alone — an operator who removes a service and re-adds it expects their data to
survive, and the panel has no verdict on whether a directory full of uploads is trash. A preview is different in kind
rather than in degree: the directory was created by a branch that no longer exists, it holds whatever an unmerged
build wrote into it, and nothing on this host will ever name it again. So `ReapRequest` carries one flag for the
difference, the delete route never sets it, and only a preview's teardown does — and it sets it only while no other
preview row still claims the same hostname, because a pull request closed and reopened gets the same slug and the
same directory, and destroying that would be this transaction deleting a workload that exists now.

**A preview copies its parent's secrets without ever reading them.** `resource_env_vars` and `resource_credentials`
store bytes the master key sealed, and a preview is the same daemon with the same key, so duplicating them is an
insert of the same blob — which is why `previews.Deps` has no key field and why nothing in the package can open a
value. It is also the decision the fork and draft refusals exist to make safe: a preview holds the parent's database
password and its deploy key in the only form this panel keeps them, so the code it runs has to be the team's own
branch, not a stranger's proposal. The three things taken out of the copied configuration are what keeps the copy
narrow: the release command (unreviewed code must not migrate the real database), the published ports (the parent
already holds those loopback bindings), and image retention above one generation of a branch nobody merged.

**The cap evicts instead of refusing.** A service at its limit that gets a new pull request takes its least-live
preview down rather than telling the person who just opened the request that they cannot have one. The panel's own
measure of least-live is the expiry, which activity moves forward, so the branch that goes is the one nobody has
pushed to in longest; a preview whose resource row is already gone is closed first, since that is bookkeeping rather
than a teardown. Refusing would make the cap a limit on the *number of people who can use the feature*, and the whole
reason the feature exists is the person who just opened the request.

**The sweep runs with the feature flag off.** `git.preview_deploys` being off means previews should stop happening,
and a preview container holding the parent's database password that keeps running under a flag saying it should not
exist is the difference between a switch and a suggestion. So the reconciliation pass is unconditional, and it
reports "Preview environments are disabled" as its reason — which is also why turning the base domain off or away
does not stop it either: a hostname that no longer resolves still describes a workload with real credentials in it.

**A preview refusal means nothing was written, and the delivery row is claimed last because of it.** The push
receiver claims its delivery row first, because the reason it may record is known before it does anything; the
preview receiver cannot know its reason until the preview service has answered, and a row that recorded `success` for
a refused build is exactly the silence this endpoint is not allowed to produce. So the row is the transaction's last
statement, and the service's rule is what makes that safe: `Build` decides every refusal — the parent, its type, its
image mode, the slug, the hostname — before it inserts, and what is left to a unique index is returned as an error
that unwinds the eviction it had already done. The alternative was letting a refusal arrive after an insert, which
commits a stopped resource with no preview row, no deployment and nothing in the sweep that can see it.

**A preview never answers a push.** A preview resource carries the same repository URL as its parent, and the webhook
receiver resolves a delivery to the lowest resource id that matches — so a preview would only lose every push to that
repository by the accident that its id was minted later. Accidents are not a rule, and the failure when it breaks is
subtle: a preview built from the base branch, deployed under a name that says it is a pull request. The resolution
query now excludes resources that a preview row claims, and a preview's head ref only ever arrives through a
`pull_request` delivery, which is the only thing that knows which branch it is supposed to be.

**The sweep runs on the daemon, at boot and then on the minute — never from the webhook receiver.** A teardown needs
the engine and the queue, and the only process that has both is `codeplod`, so the receiver's job ends at the
transaction that says what was decided. The first pass runs before the loop sleeps, because the rows it exists to
reconcile are the ones a stopped daemon left behind: a preview whose lifetime ended while nothing was running keeps
holding its parent's credentials, and "the sweep will get it in a minute" from a process that just booted is a minute
of unnecessary exposure. The loop is the same `everyMinute` helper the operations loops use — aligned to the clock
boundary, 55-second bounded pass — because `expires_at` is minute-resolution in the database and a pass that starts
mid-minute evaluates a different set of rows than the one that wrote them.

**`codeplo previews` reads, and `check` reports facts with no verdict.** The command opens the database and reads the
environment, so it can say what the base domain is, whether it parses, what the flag and the three caps are, and how
many previews are live — but it has no deployment engine, and a status object that answered `enabled: false` from a
process that could never have built a preview would be a statement about the reader rather than the panel. So
`Status` carries no verdict field, the daemon composes its own boot line from the same status plus `Enabled`, and the
CLI's non-zero exit comes from the flag being on with something missing (`git.preview_deploys` off is the panel doing
what it was told, which is never a failure). `list` gained a state column for the same reason it needed the parent's
name: a hostname alone cannot say whether the container behind it still exists, and a row whose resource is gone is
the sweep's bookkeeping to finish, which an operator who deleted it by hand needs to see rather than infer.

## T27 — setup operator create mode is real (2026-10-22)

`POST /setup/operator` with `mode:"create"` was a 501 that the first-run UI still offered. Create is now implemented
in `platform.CreateOperator`: `useradd -m`, sudo/wheel membership, and a single-line `authorized_keys` write. It
requires root (installer/setup path); when the panel is not root the handler returns 403 with guidance to pick an
existing sudo user. Invalid keys and usernames are 422. The mock already accepted create; the server matches it.

## T32 — keep unreachable stub() as laterPhase safety net (2026-10-22)

All 110 contract operations have real handlers, so `Server.stub` is currently unreachable. It is kept on purpose:
generated `laterPhase` rows without an overriding handler still register through it and answer 501/module_disabled
instead of a generic 404. Deleting it would change the failure mode of the next unfinished route. Comment on
`stub` records this; no behaviour change.

## T34 — remove unread RouteDef.NoCSRF (2026-10-22)

`NoCSRF` was set only on setup routes that are already `authz.Public`. The guard skips CSRF for Public
routes entirely, so the flag was never read. Removed the field and its assignments. CSRF remains required on
every non-GET session request that is not an API token (existing behaviour, covered by TestSessionNonGETRequiresCSRF).

## T28 — terminal ticket step-up only for as_root (2026-10-22)

OpenAPI `x-step-up: true` on `POST /terminal/ticket` means "may require step-up", not "always".
The server and mock require step-up only when `as_root` is true. Contract description updated to match.

## T29 — TerminalSession.recorded always false until E4 (2026-10-22)

Recording is not implemented. The API continues to return `recorded: false` and the OpenAPI field documents
that fact. Implementing byte-level recording is deferred; removing the field would break clients for no gain.

## T30 — Idle RSS budget unmet (2026-10-22)

Measured idle RSS was 78.8 MB vs Profile-A 50 MB. Do not tune silently. Report in release notes; re-measure on
the reference VM in CI (T49) is the authoritative number.

## T31 — Caddy zero-drop spike remains unmet (2026-10-22)

Spike A stays strict and unmet (flaky reload timing). Record honestly in release notes; do not weaken the assertion.

## T33 — Module interface unused; keep ADR 0005 as aspirational (2026-10-22)

`internal/modules/module.go` Registry.Register is never called; routes are wired by `internal/api` tables.
Keep the types for future adoption rather than deleting them mid-build. ADR 0005 describes the target shape;
a follow-up can either adopt Register or rewrite the ADR when P8 starts.

## T38 — single resource per webhook delivery (2026-10-22)

Monorepo fan-out (one push → several path-filtered resources) is schema-ready and unbuilt. Until it is,
`findSourceByRepo` deploys at most one resource per delivery (lowest resource_id wins). Path filters still
decide *whether* that resource builds, not *which peers* also build. Contract and handler comments state the
single-target rule explicitly.

## Open gaps index (T42, 2026-10-22)

Decisions already recorded for Group C items so they are not re-litigated:

| ID | Decision |
|---|---|
| T27 | Operator create mode implemented (root required) |
| T28 | Ticket step-up only when `as_root` |
| T29 | `recorded` always false until E4 |
| T30 | RSS 78.8 MB vs 50 MB — report, do not silent-tune |
| T31 | Caddy zero-drop spike unmet — keep strict |
| T32 | Keep unreachable `stub()` as laterPhase safety net |
| T33 | Module interface kept; Register unused until adopted |
| T34 | Removed unread `NoCSRF` |
| T35 | In-memory TOTP replay; 90s window is the bound |
| T36 | sqlc scaffold removed (hand-written SQL) |
| T37 | webhook_deliveries pruned with logs.retention_days |
| T38 | Single resource per delivery until monorepo fan-out |
| T39 | Installer already verifies seven artefacts |
| T40 | Keep CODEPLO_URL refusal; preview domain is not a template address |
| T41 | Source UI is SourceTab/GitTab, not features/git/* |

## T40 — keep ${CODEPLO_URL} refusal at parse time (2026-10-22)

`CODEPLO_PREVIEW_DOMAIN` serves PR preview hostnames, not template catalog deploys. A template still has no
address to hand over at deploy time (the operator adds a domain after the resource exists). Keep the
`classify` FillURL refusal; lifting it waits on a deliberate panel-domain-for-templates decision.

