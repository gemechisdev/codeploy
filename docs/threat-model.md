# codeplo threat model

Scope: the control-plane daemon (`codeplod`), its SQLite database, the embedded SPA and its host integrations.
Security claims below are what the code enforces. Nothing here claims the audit log is immutable: it is **tamper-evident**.

## Assets
Admin credentials and TOTP secrets, session tokens, API tokens, the master key, workload secrets (later phases), host root via terminal and hardening, the audit log.

## Trust boundaries
1. Browser to panel: loopback (SSH tunnel) or HTTPS through Caddy over the unix socket. The panel never binds a public interface.
2. Caddy to panel: only the unix listener trusts `X-Forwarded-Proto` and `X-Forwarded-For`.
3. Panel to host: fixed-argv `exec.Command` only; no shell strings.
4. Panel to workloads: Docker Engine API (later phases).

## Threats and controls (P1)
| Threat | Control |
|---|---|
| Takeover of a fresh install | One-time 256-bit setup token, stored hashed, valid 1 hour, consumed on first admin creation; setup disabled once an owner exists. |
| Credential stuffing, user enumeration | Generic login error, dummy argon2 work for unknown users, per-IP and per-account token buckets with `Retry-After`. |
| Stolen database | argon2id passwords; sessions, API tokens, recovery codes and setup tokens stored as SHA-256; TOTP secrets sealed with XChaCha20-Poly1305 under a 0600 master key. |
| Stolen database and key | Not mitigated: the master key sits on the same host. Documented limit. |
| Session theft or fixation | Opaque 256-bit token, `HttpOnly; Secure; SameSite=Strict`, `__Host-` prefix on HTTPS, session id rotated after TOTP, idle and absolute expiry. |
| CSRF | SameSite=Strict plus per-session `X-CSRF-Token` on every non-GET request. |
| Privilege escalation | Every route declares exactly one permission; the authz-matrix test fails CI otherwise. Tokens never exceed their owner's current role. No permission reveals secret values. |
| Sensitive actions by a hijacked session | Step-up TOTP re-check (default 300 s) for user administration; later for restore, hardening apply, root terminal. |
| Audit tampering | Hash chain over every row, written in the same transaction as the change; DB triggers abort UPDATE and DELETE. A privileged attacker with direct database access can rewrite the whole chain; hourly journald checkpoints (later) bound that. |
| Lockout | The loopback listener cannot be disabled; `codeplo reset-password`, `disable-2fa` and `setup-token` work offline. |
| Webhook abuse | Rate limiting on `/hooks/*` and `/webhooks/*`; signature checks arrive with the git module (P5). |

## Out of scope for P1
Workload isolation policy, Caddy configuration injection, host hardening lock-out risks, terminal escape. Each has its own section in the phase that ships it.
