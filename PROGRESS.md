# PROGRESS: codeplo backend, Phase 0 and Phase 1

Status: **P0 met except the Caddy zero-drop spike and CI-runner execution; P1 met except the idle-RSS budget; the P2a audit slice is not implemented and its checks are not run.**
The tree now compiles, vets, is gofmt-clean, passes `go test -race ./...`, and has been
runtime-verified (daemon, CLI, live curl contract run, spikes in WSL, installer in WSL).
See `TEST_REPORT.md` for the full evidence table.

## Done in the tester pass (2026-10-20) — compiled, tested, runtime-verified

- **Build/toolchain**: `go mod tidy` (real `go.sum`), `go build ./...`, `go vet ./...`
  (also `-tags spike`), `gofmt -l .` clean — all on the real toolchain, no stubs.
- **Generation**: `make gen` fixed and idempotent — oapi-codegen pinned v2.4.1→v2.8.0
  (2.4.1 cannot parse the OpenAPI 3.1 nullable forms), `output:` path made
  package-relative, `gen_stubs.py` now writes LF. `internal/api/oapigen/models.gen.go`
  generated and committed; TS schema and stubs regenerate byte-identical.
- **Tests**: `go test -race ./...` all green (two test-only fixes: contract role enum
  in `api_test.go`, Windows path in the `operators_test.go` passwd fixture).
  New `test/authz/routes_test.go` proves the Go route set equals the OpenAPI
  path/method set exactly (110 routes) and every route's permission equals its
  `x-permission`. New `internal/api/security_test.go` (12 tests) plus a config
  loopback-mandatory test.
- **Mock alignment**: `/system/status` net fields, settings and module seed data,
  settings audit shape + bus event, post-commit `audit.appended` on
  `PATCH /feature-flags`, SSE wire format (counter `id:` lines, `server_now`,
  500-event ring + `?last_event_id` replay). OpenAPI amended where the mock and the
  frontend were right (setup 401s, 501s, modules 409, cursor params, `/events`);
  amendments documented in `web/docs/CONTRACT.md`, full diff table in
  `docs/DECISIONS.md`. Frontend: `tsc` clean, vitest 94/94 (Node 22, serial forks).
- **Binary**: `make build` → `bin/codeplod`, `bin/codeplo`. Verified live: one-time
  setup token, `setup-token`, `doctor [--verify-audit]`, tamper detection
  (`first_invalid_seq=2`, exit 1), `reset-password`, `disable-2fa`.
- **Contract run**: `scripts/contract-test.sh` (documented in
  `docs/contract-test.md`) replays all 17 frontend calls against a live daemon —
  every expected status matched; vite live-mode proxy verified. Not browser-driven:
  the wizard's operator step needs a Linux sudo user (host limitation);
  `/setup/complete` itself does not require an operator.
- **RSS**: idle `codeplod` = **78.8 MB** vs the ≤ 50 MB Profile A budget → **over,
  unmet**. Measured on Windows; the plan's budget is a Linux-lab number. Reported,
  not tuned.
- **Spikes**: Spike B **PASS** (pull → create → health-wait → blue/green swap over
  the Docker Engine API socket; fixture needed `CapAdd [CHOWN, NET_BIND_SERVICE,
  SETGID, SETUID]` because nginx:alpine cannot run under `CapDrop ALL` alone — see
  DECISIONS "Spike findings"; 19.91 s, re-verified 16.82 s). Spike A **flaky, kept
  strict**: the same binary and host give 0 failures in some runs (0/2082 in the
  final run) and 1–2 resets in others (2/216, 2697, 1638 requests) → zero-drop not
  certifiable; mitigations recorded (keep-alive clients ride the swap; pace reloads).
- **Installer**: all 4 cases PASS in WSL Ubuntu with systemd — clean install over
  HTTPS (service active, runs as `codeplo`, tunnel endpoint answers, setup token
  works), tampered binary / tampered unit file / missing checksum entry each abort.
  `checksums.txt` must be GNU two-space LF format.

## Not done / open

- **CI never executed on a runner.** `ci.yml`, `release.yml`, `memory-budget.yml` are
  wired (lint, test, authz matrix, drift check, build) but this host only ran them
  locally.
- **Browser-driven contract run** (wizard operator step) — needs a Linux host's
  `/etc/passwd` sudo user; curl fallback used.
- **Idle RSS over budget** (78.8 > 50 MB, Windows-host number).
- **P2a audit slice not implemented**: `POST /hardening/audit` is a stub only
  (404/501 `module_disabled`); its exit checks are NOT RUN, not pass.
- **Caddy zero-downtime under rapid reloads** not proven (Spike A flaky); mitigations
  recorded (keep-alive clients unaffected; pace reloads).
- Release pipeline: no signed release exists; cosign path untested (installer path
  with self-signed CA verified in WSL).
- sqlc scaffolded but P1 uses hand-written `database/sql` (DECISIONS).
- TOTP replay guard is in-memory; persisted last-used step deferred (schema change).

## Historical: what earlier passes wrote (now superseded on verification status)

Passes 1–4 produced the tree described below. Their "unverified" caveats no longer
apply — everything listed was compiled, tested under `-race`, and runtime-verified in
the tester pass; the judgment calls they record still stand (see `docs/DECISIONS.md`).

| Area | Files |
|---|---|
| Go module | `go.mod` + `go.sum` |
| Migration and seeds | `internal/store/migrations/0001_core.sql`: ten required tables plus `recovery_codes` and `setup_state`; seeds for roles, settings, flags, modules; triggers abort UPDATE/DELETE on `audit_events` |
| Store | `internal/store/db.go`: modernc SQLite, WAL, goose `Up`, idempotent `role_permissions` seed, `InTx` |
| Authz registry | `internal/authz/permissions.go`: full registry (no secret-reveal permission), role map, `public`/`session` pseudo-permissions |
| Crypto / Authn | `internal/crypto/crypto.go` (XChaCha20-Poly1305, SHA-256 token hashes, ULIDs), `internal/authn/authn.go` (argon2id, TOTP, recovery codes, replay guard) |
| Audit | `internal/audit/audit.go`: in-tx append, mock-compatible hash chain, `Verify{valid, verified_rows, first_invalid_seq}`, seq gap = tampering |
| Jobs, events, modules | `internal/jobs/queue.go`, `internal/events/bus.go` (IDs, history, `Since`), `internal/modules/module.go` |
| API | `internal/api/{router,session,handlers_auth,handlers_admin}.go`: one route table, one permission per route; guard order rate limit → authn → owner-TOTP gate → CSRF → permission; `__Host-codeplo` cookie on HTTPS, bootstrap cookie on loopback; per-IP and per-account token buckets; step-up on user create/patch |
| Endpoints | `/setup/*`, `/auth/*`, `/users`, `/tokens`, `/roles`, `/permissions`, `/audit`, `/audit/verify`, `/system/status`, `/system/version`, `/modules`, `/feature-flags`, `/settings`, `/events` (SSE) |
| Later-phase stubs | `internal/api/stubs_gen.go`: 77 routes from `openapi.yaml` `x-permission`; disabled module → 404, enabled-unimplemented → 501 `module_disabled` |
| Host and UI | `internal/platform/host_*.go`, `internal/webui/embed.go` (`go:embed all:dist`, SPA fallback) |
| Binaries | `cmd/codeplod` (config, migrate, first-start setup token, loopback + unix listeners, worker, graceful shutdown), `cmd/codeplo` (doctor, status, setup-token, reset-password, disable-2fa) |
| Config | `internal/config` refuses non-loopback binds; `internal/app` (open, setup-token issuance, 1 h TTL) |
| Tooling | `Makefile` (`gen`, `lint`, `test`, `build`, `run`), `scripts/gen_stubs.py`, CI workflows |
| Docs | `docs/threat-model.md`, ADRs 0001–0005, `docs/DECISIONS.md`, `docs/contract-test.md`, `TEST_REPORT.md` |
| Spikes | `test/spikes/{caddy,docker}_spike_test.go` behind the `spike` tag; run in WSL via a linux cross-compiled test binary |
| Authz tests | `test/authz/matrix_test.go`, `test/authz/routes_test.go` |
| Deploy | `deploy/install.sh` (checksum-verified download, service user, systemd, tunnel instructions), `deploy/codeplod.service` |

## Verification performed (tester pass)

| Check | Result |
|---|---|
| `go build ./...`, `go vet ./...` (+ `-tags spike`), `gofmt -l .` | Pass, real toolchain |
| `go test -race ./...` (CC=WinLibs gcc 16.2.0) | All packages pass |
| Authz matrix + route-set==OpenAPI equality (110 routes) | Pass |
| Security tests (12, new) + config loopback test | Pass |
| `make gen` ×2 | No diff; models/stubs/TS committed artifacts match |
| Binary: setup token, doctor, tamper→exit 1, reset-password, disable-2fa | Pass (real DB) |
| Contract run: 17 calls via curl + vite live proxy | Pass (browser not driven — host limitation, recorded) |
| Frontend `tsc` + vitest | Clean / 94 pass (Node 22, `--fileParallelism=false`) |
| Idle RSS vs Profile A ≤ 50 MB | **78.8 MB — FAIL(open)** |
| Spike A (Caddy v2.11.7) | **FAIL(open)** — flaky: 0 failures and 1–2 resets across runs of the same binary; kept strict |
| Spike B (Docker 29.1.3, WSL) | **Pass** after fixture fix (19.91 s; re-verified 16.82 s) |
| Installer 4 cases (WSL Ubuntu systemd) | Pass |
| CI pipelines on a runner | **NOT RUN** (workflows exist; never executed) |
| P2a audit-slice checks | **NOT RUN** (not implemented in this tree) |

## Exit criteria (plan §6.2) — details and evidence in TEST_REPORT.md §8

**P0**
- Threat model and ADRs: **met on paper** (docs exist; "merged to main" not applicable locally).
- Caddy spike zero dropped connections: **UNMET** (flaky; findings and mitigations recorded).
- Docker spike start → health-wait → swap: **MET** (PASS after fixture fix).
- CI green with memory-budget job scaffolded: **NOT RUN** (scaffolded and locally green; no runner).

**P1**
- `install.sh` on a clean VM: **MET** (WSL Ubuntu, 4/4 cases).
- Owner via tunnel with one-time token + TOTP login: **MET, curl-driven** (browser not driven).
- Every route declares a permission + authz matrix: **MET** (incl. route-set equality test).
- Audit hash chain + `doctor --verify-audit` + tamper detection: **MET**.
- Idle RSS ≤ budget: **UNMET** (78.8 > 50 MB, Windows host).
- OpenAPI → Go/TS generation in CI: **PARTIAL** (generation works, drift wired; runner never executed).

**P2a**: **NOT RUN** — slice not implemented; stub route only.

## Next steps, in order

1. Commit this pass's changes (file list in TEST_REPORT.md / HANDOFF §Remaining work 6).
2. Run the CI workflows on a real runner (lint/test/build/drift/memory-budget) — the
   RSS criterion must be re-measured there (Linux Profile A number).
3. Browser-driven contract run on a Linux host with a sudo user (wizard operator step).
4. Decide on Spike A: either accept paced reloads in the edge module design (ADR 0003
   note) or investigate Caddy reload resets further with keep-alive-aware load tools.
5. Then P2a for real: implement `hardening` audit mode so its exit checks can run.
