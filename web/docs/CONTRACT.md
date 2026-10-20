# API contract summary
Source of truth: `../api/openapi.yaml`. Base `/api/v1`, JSON snake_case, errors `{code,message,details}`, lists `{items,next_cursor}`, CSRF via `X-CSRF-Token`, step-up on `403 step_up_required` (client retries once after the dialog).
Amendments to plan 3.4: `GET /hardening/changes`, `POST /audit/verify`, `POST /notifications/channels/{id}/test`, `GET /resources/{id}/policy`, setup wizard endpoints (`/setup/totp/*`, `/setup/operator*`), `GET/PATCH /settings`, extended `resources.config`.

## Amendments (backend P0/P1)

No change to `api/openapi.yaml` or to the frontend's behaviour was needed so far. Backend choices that are not contract changes are recorded in `docs/DECISIONS.md`:

- Auth and setup responses follow the mock's field names (`csrf_token`, `server_now`, `step_up_until`, `challenge`, `recovery_codes`).
- A TOTP code is single-use per subject (replay guard), so a step-up needs a fresh code, not the one just used to log in.
