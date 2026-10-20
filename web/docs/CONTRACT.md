# API contract summary
Source of truth: `../api/openapi.yaml`. Base `/api/v1`, JSON snake_case, errors `{code,message,details}`, lists `{items,next_cursor}`, CSRF via `X-CSRF-Token`, step-up on `403 step_up_required` (client retries once after the dialog).
Amendments to plan 3.4: `GET /hardening/changes`, `POST /audit/verify`, `POST /notifications/channels/{id}/test`, `GET /resources/{id}/policy`, setup wizard endpoints (`/setup/totp/*`, `/setup/operator*`), `GET/PATCH /settings`, extended `resources.config`.

## Amendments (backend P0/P1)

No change to `api/openapi.yaml` or to the frontend's behaviour was needed so far. Backend choices that are not contract changes are recorded in `docs/DECISIONS.md`:

- Auth and setup responses follow the mock's field names (`csrf_token`, `server_now`, `step_up_until`, `challenge`, `recovery_codes`).
- A TOTP code is single-use per subject (replay guard), so a step-up needs a fresh code, not the one just used to log in.

## Amendments (tester pass, 2026-10-20)

`api/openapi.yaml` edits (frontend and mock behaviour unchanged; the spec was out of sync with the mock+frontend):

- Setup routes after `/setup/admin` (`/setup/totp/*`, `/setup/verify-domain`, `/setup/panel-domain`, `/setup/operator*`, `/setup/complete`) document `401` — the wizard runs on a setup-scoped session cookie, so a missing cookie is `401 unauthenticated`, which the spec did not list.
- `/setup/panel-domain` and `/setup/operator` (`mode: "create"`) document `501 module_disabled` (new `E501` component) — these steps need the P2 edge/hardening helpers.
- `PATCH /modules/{key}` documents `409` (disabling a core module).
- `GET /users` and `GET /tokens` document the `limit` and `cursor` query parameters (keyset cursor: last item id; `/audit` already did).
- `GET /events` documents the actual SSE wire format used by the mock and the frontend: `event: <type>`, an `id:` line, `data:` = payload JSON extended with `server_now` (except `metrics.sample`), and `?last_event_id=` resume. The `EventEnvelope` schema stays (the TS types use its `type` enum); it is no longer the declared `data:` shape.
- `GET /system/status` now returns the full `HostMetrics` field set (`net_rx_kbps`, `net_tx_kbps` stubbed to 0 until the P6 monitor module).
- Settings and modules seed data and the settings audit action (`settings.updated`, target `server`) align with the mock; see `docs/DECISIONS.md`.

Afterwards `make gen` was re-run; `web/src/api/schema.d.ts` and the Go models track the spec.
