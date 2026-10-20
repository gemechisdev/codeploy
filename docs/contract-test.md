# Frontend contract run (live mode)

Replays the frontend's own sequence against a **real** `codeplod` (`VITE_API_MODE=live`),
not the in-browser mock. Run on 2026-10-20 (Windows dev host).

## How it was run

1. Build the binaries: `make build` (embeds `web/dist` into `codeplod`).
2. Start a live daemon against a temp data dir (so the setup token is fresh):
   ```
   CODEPLO_DATA_DIR=/tmp/ct-data CODEPLO_LISTEN=127.0.0.1:7443 \
   CODEPLO_SOCKET=/tmp/ct-data/codeplo.sock ./bin/codeplod
   ```
   On first run it prints the one-time setup token (`.../setup?token=<64-hex>`).
3. The full endpoint sequence is `scripts/contract-test.sh` (curl). Run it:
   ```
   CODEPLO_DAEMON_LOG=/tmp/ct-daemon.log bash scripts/contract-test.sh
   ```
4. To exercise the frontend's live wiring, start the dev server in live mode so `/api`
   proxies to the daemon:
   ```
   cd web && VITE_API_MODE=live VITE_PROXY_TARGET=http://127.0.0.1:7443 npm run dev
   ```
   Then open `http://127.0.0.1:3000` in a browser.

## The frontend sequence, replayed

`scripts/contract-test.sh` replays, in order, the same calls the SPA makes and records the
exact expected status codes:

| # | Call | Expected | Observed |
|---|---|---|---|
| 1 | `GET /setup/state` | `setup_required=true, step=token` | ✓ |
| 2 | `POST /setup/admin` | `201` + setup session cookie | ✓ 201 |
| 3 | `POST /setup/totp/enroll` | TOTP secret shown once | ✓ |
| 4 | `POST /setup/totp/verify` (real authenticator code) | 8 recovery codes shown once | ✓ |
| 5 | `GET /setup/operator-candidates` | `200` (empty on a host without `/etc/passwd`) | ✓ 200 |
| 6 | `POST /setup/operator` (`mode=existing`) | `422` (no sudo candidate on this host) | ✓ 422 |
| 7 | `POST /setup/panel-domain` | `501 module_disabled` (P2), skippable in UI | ✓ 501 |
| 8 | `POST /setup/complete` | `204` (backend does not require an operator) | ✓ 204 |
| 9 | `POST /auth/login` | `{totp_required:true, challenge}` | ✓ |
| 10 | `POST /auth/totp/verify` (challenge + code) | `Me` + session + `csrf_token` | ✓ |
| 11 | `POST /users` without step-up | `403 step_up_required` | ✓ 403 |
| 12 | `POST /auth/step-up` (fresh TOTP code) | `step_up_until` | ✓ |
| 13 | `POST /users` again | `201` | ✓ 201 |
| 14 | `POST /audit/verify` | `{valid:true}` | ✓ |
| 15 | `PATCH /settings` | `200` (audit `settings.updated`) | ✓ 200 |
| 16 | `PATCH /feature-flags` | `200` (audit `flag.changed`) | ✓ 200 |
| 17 | `POST /auth/logout` | `204` | ✓ 204 |

**Note on the step-up code (step 12):** the login consumed the current TOTP step, and the
replay guard makes that code single-use, so the step-up must use a *fresh* code (the next
30 s step). `contract-test.sh` generates the step-up code from the next step to model the
real step-up dialog, where the user re-reads their authenticator.

## What was not browser-driven, and why

- The live-mode proxy was confirmed mechanically: with `VITE_API_MODE=live` and
  `VITE_PROXY_TARGET=http://127.0.0.1:7443`, `curl http://127.0.0.1:3000/api/v1/setup/state`
  returns the daemon's real data, and auth-required routes return `401` through the proxy.
- The **operator** step of the setup wizard was **not** completed in a browser: it lists
  existing sudo users from the host `/etc/passwd` (read-only, no command runs). This dev
  host is Windows, so the candidate list is empty and the UI's "Continue" stays disabled.
  That is a host-property limitation, not a code defect; on the target Linux host the same
  wizard step works. `/setup/complete` does not require an operator, so the rest of the
  contract runs to completion.
- No full browser session was driven (the SPA was loaded in live mode and the proxy verified,
  but the in-browser wizard/step-up dialog clicks were not performed). The equivalent call
  sequence was replayed with curl (the permitted fallback when the wizard can't be driven to
  completion on the host). All status codes match the contract.

## Exact commands (from a clean shell)

```
make build
CODEPLO_DATA_DIR=/tmp/ct-data CODEPLO_LISTEN=127.0.0.1:7443 \
  CODEPLO_SOCKET=/tmp/ct-data/codeplo.sock ./bin/codeplod &        # prints the setup token
CODEPLO_DAEMON_LOG=/tmp/ct-daemon.log bash scripts/contract-test.sh
cd web && VITE_API_MODE=live VITE_PROXY_TARGET=http://127.0.0.1:7443 npm run dev
# browser: http://127.0.0.1:3000  (dev server proxies /api to the daemon)
```
