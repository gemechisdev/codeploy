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
