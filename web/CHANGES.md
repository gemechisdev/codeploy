# Panel / API fix pack (2026-11-09)

## Crashes and null safety
- QueryBoundary treats null/undefined data as empty (Go nil slices → JSON null).
- Backups, notifications, and list pages use optional chaining on `.items` / `.points`.
- `duration()` returns "—" for non-finite inputs; DeploymentsTab guarded.
- TotpStep guards missing enroll payload before rendering QR/secret.
- Monitoring chart Y-scale ignores non-finite highs.

## Terminal host shell
- `codeplo-pty.socket` uses RuntimeDirectory + group `codeplo`.
- Installer ensures `codeplo` group/user membership and chowns `/run/codeplo-pty`.
- Clearer permission and ticket-after-restart hints.

## Sessions and clock
- Active sessions slide `expires_at` with `last_seen_at` (no hard 12h kick while in use).
- Client syncs server time from HTTP `Date` on every successful API response.

## Notifications
- Email channel form includes SMTP host/from/port/username/password/tls.
- Alert rules can assign channels in the UI; amber warning when enabled with no channels.
- Failed deliveries use a short retry window instead of burning the full cooldown.

## Runtime / extensions / setup
- Docker Resolve picks newest container by `Created` during blue/green.
- Extensions CLI: list shows events; dead-letter shows extension name.
- Setup operator step no longer offers "create user" (panel is unprivileged).


# CHANGES

Frontend rebuilt from the prototype per `docs/implementation-plan.md` and `docs/FRONTEND_AGENT_BRIEF.md`.

## Done
- OpenAPI 3.1 contract (`../api/openapi.yaml`) and generated types (`src/api/schema.d.ts`).
- API layer: typed client, CSRF, automatic step-up retry, single SSE connection, TanStack Query hooks.
- Mock backend (`src/mock/`): auth/TOTP, hash-chained audit, hardening with server-side dead-man timer, 12-state deployments, edge, terminal PTY simulator, git/CI, modules/flags/settings. Not present in `live` builds.
- Pages: login, setup, dashboard, projects, resource detail (all tabs), security, terminal, proxy, monitoring, templates, notifications, backups, settings (users, roles, tokens, git, modules and flags, audit, system).
- Checks: typecheck, lint guards, 94 tests, build in mock and live modes, no external URLs, no mock code in live bundle.

## Judgment calls
- Token creation in Settings defaults to the `resource:read` scope only (scope picker deferred).
- Git-repo placeholder in SourceTab avoids an email-like string to satisfy the lint guard.

## Deferred
- TanStack Table, CodeMirror (per brief), token scope picker, project/environment management beyond create, `docs/CONTRACT.md` is a summary only.
