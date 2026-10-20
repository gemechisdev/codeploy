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
