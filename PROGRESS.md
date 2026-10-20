# PROGRESS: codeplo backend, Phase 0 and Phase 1

Status: **Phase 0 finalized with one failed and one unrun spike; Phase 1 partial.** Code compiles and vets (with a stubbed SQLite driver), 9 test packages pass, no deployable binary yet.
See "Verification performed" below for exactly what was and was not checked.

## Done (written, unverified)

| Area | Files |
|---|---|
| Go module | `go.mod` (no `go.sum` yet; run `go mod tidy`) |
| Migration and seeds | `internal/store/migrations/0001_core.sql`: all ten required tables plus `recovery_codes` and `setup_state`. Seeds for roles, settings, flags and modules. DB triggers abort UPDATE and DELETE on `audit_events`. |
| Store | `internal/store/db.go`: modernc SQLite with WAL, goose `Up`, idempotent `role_permissions` seed, `InTx` helper. `sqlc.yaml` is scaffolded only. |
| Authz registry | `internal/authz/permissions.go`: full registry (no secret-reveal permission), role map, `public` and `session` pseudo-permissions. |
| Crypto | `internal/crypto/crypto.go`: master key file (0600), XChaCha20-Poly1305 seal/open, SHA-256 token hashing, ULIDs. |
| Authn | `internal/authn/authn.go`: argon2id (m=32 MiB, t=3, p=1), TOTP via `pquerna/otp`, recovery codes. |
| Audit | `internal/audit/audit.go`: append inside the caller's tx, hash chain matching the mock formula, `Verify` returning `{valid, verified_rows, first_invalid_seq}`. A seq gap counts as tampering. |
| Jobs, events, modules | `internal/jobs/queue.go` (durable queue, crash re-queue, retry backoff), `internal/events/bus.go`, `internal/modules/module.go` (plan §4.3 interface and registry). |
| API | `internal/api/{router,session,handlers_auth,handlers_admin}.go`. One route table with a declared permission per route. The guard order is rate limit, authn, owner-TOTP gate, CSRF, permission. Sessions use the `__Host-codeplo` cookie on HTTPS and `codeplo_session` (bootstrap) on loopback. Token buckets cover per-IP and per-account. Step-up is enforced on user create and patch. |
| Endpoints | `/setup/*` (state, admin, totp enroll and verify, verify-domain, complete), `/auth/*`, `/users`, `/tokens`, `/roles`, `/permissions`, `/audit`, `/audit/verify`, `/system/status`, `/system/version`, `/modules`, `/feature-flags`, `/settings`, `/events` (SSE). |
| Later-phase stubs | `internal/api/stubs_gen.go`: 77 routes generated from `openapi.yaml` `x-permission`. They return `404 module_disabled` when the module is off, otherwise `501`. |
| Host and UI | `internal/platform/host_*.go`, `internal/webui/embed.go` (`go:embed all:dist`, SPA fallback; `internal/webui/dist/.gitkeep` placeholder). |

## Done in the second pass (Phase 0 completion, unverified)

- `cmd/codeplod/main.go`: config, migrate, setup-token on first start, loopback and unix listeners, job worker, graceful shutdown.
- `cmd/codeplo/main.go`: `doctor [--verify-audit]`, `status`, `setup-token`, `reset-password`, `disable-2fa`. The last two revoke sessions and write an audit row in one transaction.
- `internal/config` (refuses non-loopback binds) and `internal/app` (open, setup-token issuance, 1 hour TTL).
- `Makefile` (`gen`, `lint`, `test`, `build`, `run`) and `scripts/gen_stubs.py`.
- CI: `ci.yml` (lint, test, authz matrix, drift check, build), `release.yml` and `memory-budget.yml` as scaffolds.
- Docs: `docs/threat-model.md`, `docs/adr/0001` to `0005`, `docs/DECISIONS.md`.
- Spikes: `test/spikes/caddy_spike_test.go` and `docker_spike_test.go`, behind the `spike` build tag. They skip when Caddy or Docker is absent. **Neither has been run.**
- `test/authz/matrix_test.go`: every route needs one valid permission, public routes come from a closed allow-list, and no secret-reveal permission exists.
- `deploy/install.sh` (checksum-verified download, service user, systemd, tunnel instructions) and `deploy/codeplod.service`.

## Done in the third pass (written, NOT compiled or run)

- `internal/api/api_test.go`: single-use setup, setup token expiry, CSRF rejection, step-up required and expiry, TOTP replay, owner TOTP gate, audit tamper detection (expects `first_invalid_seq=2`), rate limit with `Retry-After`, generic login errors.
- `authn.ReplayGuard` wired into `checkSecondFactor` and `/setup/totp/verify`.
- `CONTRACT.md` Amendments section and `docs/DECISIONS.md` third-pass entries.
- The sandbox was reset between passes, so I restored the tree from the uploaded zip. The earlier Caddy download and the Spike A experiments were lost with it; the ADR 0003 notes in the zip are what remains.

## Done in the fourth pass (written, NOT compiled or run)

- `/setup/operator-candidates` reads `/etc/passwd` and `/etc/group` (no command runs); `/setup/operator` accepts an existing sudo user, records it and writes an audit row. `mode: "create"` returns 501 `module_disabled` because it needs root (P2). `has_authorized_keys` is false when the service user cannot traverse the home directory. `/setup/panel-domain` still returns 501; the wizard has a "Skip, use the SSH tunnel" button, so setup can complete without it.
- `oapi-codegen` (models only) is wired into `make gen` and the CI drift check: `api/oapi-codegen.yaml`, `internal/api/oapigen/`. **The generated `models.gen.go` does not exist yet**; run `make gen` once and commit it, or the CI untracked-file check fails.
- Installer: `codeplod.service` is now checksum-verified (it was not before), the checksum list must contain all three entries, and cosign verification of `checksums.txt` runs when `cosign` and `CODEPLO_COSIGN_IDENTITY` are set (`CODEPLO_REQUIRE_COSIGN=1` makes it mandatory).
- Tests: `platform/operators_test.go`, `TestSetupOperatorValidation`.

## Not done

- **Running the database-backed tests.** They are written (`internal/api/api_test.go`) but I was told not to build or test, and they have never executed. Expect small mismatches, for example the status code of a second `/setup/admin` and whether login needs setup to be complete.
- **OpenAPI to Go generation:** wired for models only; handlers stay hand-written (docs/DECISIONS.md). The generated file has not been produced.
- **Frontend contract run** in `VITE_API_MODE=live`, and the idle RSS measurement against the Profile A budget.
- **Release pipeline:** `release.yml` is a failing scaffold; no signed release exists, so the installer's cosign path is untested.
- **Spike B result:** the Docker pull, health-wait and swap is written but unrun (no Docker in the sandbox).
- There is no `go.sum` and no real binary (see Verification performed).

## Judgment calls to record in docs/DECISIONS.md

- Handlers use hand-written `database/sql` instead of sqlc.
- The setup wizard is bound to a setup-scoped session cookie created by `/setup/admin`, so later setup steps are not anonymous. Those steps skip CSRF and rely on that cookie plus SameSite=Strict. The `NoCSRF` field on `RouteDef` is declared but unused, because public routes already skip CSRF.
- `/setup/panel-domain` and operator `create` mode return 501 until P2. The domain step is skippable in the UI; the operator step needs an existing sudo user on the host.
- Stub routes for enabled-but-unimplemented modules use status 501 with code `module_disabled`.
- Users without TOTP log in with a password only. The owner is always gated until TOTP is enrolled.
- Session idle timeout is a fixed 2 hours; the absolute lifetime comes from the `session_ttl_h` setting.
- Default values for `hardening_revert_window_s`, `disk_preflight_threshold_gb` and `session_ttl_h`, and the response shapes of `PATCH /feature-flags` (`{items}`) and `PATCH /settings` (`{values}`), are guesses taken from the mock and the openapi file. They are not verified against the mock's return statements.
- Audit and token list cursors are keyset (`seq` or `id`), not the mock's offsets.

## Known risks in the written code

- SQLite-dependent code (store, migrations, every handler) type-checks but has never executed.
- `guard` calls `attach` for public routes, which is redundant.
- A leftover `var _ = os.Getpid` in `handlers_admin.go` can be removed.
- Recovery-code redemption and TOTP replay protection are minimal; the same TOTP code can be reused within its window.

## Verification performed

| Check | Result |
|---|---|
| `go build ./...` and `go vet ./...` (also with `-tags spike`) | Pass, in a scratch copy with GitHub mirrors and a **stub** SQLite driver. Not equivalent to a real build. |
| `gofmt -l` | Clean |
| Unit tests: audit, crypto, authn, config, events, authz, api limiter | Pass (9 packages including `test/authz`) |
| Authz matrix (110 routes, every one declares a valid permission, closed public allow-list) | Pass |
| Spike A, Caddy v2.8.4 | **Fail**: route add and remove work, but requests are reset during reloads (ADR 0003) |
| Spike B, Docker | **Not run**: no Docker daemon |
| Real binary, migrations on real SQLite, any handler at runtime | **Not verified**: `modernc.org/sqlite` is unreachable here |
| Frontend contract run, RSS budget, clean-VM install | Not run |

## Exit criteria

**P0**
- Threat model and ADRs: written (`docs/threat-model.md`, ADRs 0001 to 0005). Not yet merged to a main branch.
- Caddy spike adds and removes a route with no dropped connections: **not met**.
- Docker spike start, health-wait, swap: **unverified**.
- CI green with the memory-budget job scaffolded: workflows written; never executed on a CI runner.

**P1**
- Met in code, unverified at runtime: every route declares a permission (the matrix test passes), audit hash chain and `doctor --verify-audit`, step-up and CSRF logic, installer.
- Unmet: clean-VM install, owner login through a tunnel with TOTP, tamper test against a real database, RSS budget, OpenAPI to Go generation in CI.

## Next steps, in order

1. On a machine with normal network access: `go mod tidy`, `make build`, then `go test ./...`. Expect any problems to be in the SQLite paths, the only code not executed so far.
2. Run `go test -tags spike ./test/spikes -v` on a real VM with Docker and Caddy. Spike A is expected to fail until the reload drops are understood (ADR 0003).
3. Write the audit, setup, step-up, CSRF and rate-limit tests.
4. Wire `oapi-codegen` and add its output to the drift check.
5. Run the frontend in live mode against the server and record the steps in `docs/`.

Then P2 (secure edge and host hardening) can start from the `internal/api/stubs_gen.go` entries for `edge` and `hardening`.

## Artifacts

A zip of the working directory is written to `/mnt/user-data/outputs/workspace.zip` by `change_watcher.py`, which is still running.
