# codeplo Frontend — Agent Task Brief

> **Audience:** an autonomous AI coding agent. **Goal:** take the existing frontend prototype and convert it into a production-ready SPA that matches the codeplo Implementation Plan exactly and can be integrated with the Go backend by changing one environment variable.
> **Read this whole file before writing code.** Decisions are already made here; do not ask the user questions — follow the plan, and record any judgment call in `CHANGES.md`.

---

## 0. Inputs and source-of-truth order

| Path (inside the workspace) | What it is |
|---|---|
| `proto/` | **Working copy** of the prototype (React + Vite + Tailwind 4). This is what you modify. |
| `proto-orig/` | Pristine copy of the prototype. Read-only reference for visual design and for diffing. |
| `proto/docs/implementation-plan.md` | **The plan (v1.1). Authoritative.** Key sections: §3.4 API surface, §3.5 auth, §3.8 deployment states, §3.10 hardening safety, §3.11 isolation policy, §3.12 bootstrap access, §4 structure, §5 UI/UX, §7 per-phase rules, Appendix A features. |
| `proto/docs/FRONTEND_AGENT_BRIEF.md` | This file. |

**When sources conflict, obey this order:** (1) the plan → (2) this brief → (3) the prototype. If the plan is silent and a UI needs an endpoint, add it as an **additive amendment** (§5.6 below), never silently.

Ignore `plan/`, `change_watcher.py`, and any `workspace.zip` — they are tooling artifacts.

---

## 1. Mission and non-negotiables

Deliver a frontend that:

1. **Looks and feels like the prototype.** Keep the brand tokens, layout, density, and component look from `src/index.css` and the existing views. This is a conformance and integration task, not a redesign.
2. **Contains no business logic that belongs to the server.** The prototype fakes hashing, TOTP, state machines, timers, a shell, and scoring inside the browser. All of that must move to a **mock backend** that implements the same HTTP contract as the future Go server (§7).
3. **Talks to a typed API.** All data access goes through a client generated from `api/openapi.yaml` (OpenAPI-first, per plan §1.3). The same UI code runs against mock and live backends.
4. **Enforces the plan's security UX** exactly: step-up auth, server-driven dead-man switch, write-only secrets, permission-aware controls, root terminal off by default, tiered/gated hardening.
5. **Works inside a Go binary.** Output is static files embedded via `go:embed`: no Node server at runtime, no external network requests, strict-CSP friendly.

Hard rules:

- **No secret reveal anywhere.** There is no endpoint, button, or permission for it (plan §3.9).
- **No client-side security decisions.** UI hides/disables by permission for UX only; the server is authoritative and the UI must handle 401/403 correctly.
- **No external hosts at runtime** (no Google Fonts, CDNs, analytics, remote images). A CI check must fail if `dist/` contains `http(s)://` references to third-party hosts (allow `localhost`, `127.0.0.1`, `example.com` in docs strings only).
- **Do not invent features** beyond the plan and the additive amendments in §5.6.
- **Do not add time estimates** to any document you produce.
- Keep strict TypeScript. No `any` in `src/api`, `src/features`, or `src/lib` except at documented boundaries.

---

## 2. Audit of the current prototype

### 2.1 Keep (it is good)

- Visual system: brand tokens and light/dark theme in `src/index.css`; card/table/badge styling patterns; responsive shell (sidebar drawer on mobile).
- The *concepts* already present: tiered hardening cards with diff viewer, dead-man banner, step-up modal, write-only secret list, isolation-policy tab, deployment timeline with log pane, command palette, setup wizard step list.
- Type names for the 12 deployment states (they match plan §3.8).

### 2.2 Defects and plan deviations (fix all)

References are to files in `proto/src/`.

**A. Tooling and packaging**

| # | Problem | Required fix |
|---|---|---|
| A1 | `npm install` **fails** (ERESOLVE): pinned `esbuild ^0.25` conflicts with Vite 8's peer range. | Remove the stray `esbuild` devDependency; verify `npm ci` succeeds from a clean checkout. |
| A2 | Unused/foreign deps: `express`, `@types/express`, `dotenv`, `@google/genai`, `tsx`, `esbuild`, `autoprefixer`, `motion` (no imports found). | Remove. Remove the `clean` script's `server.js` reference. |
| A3 | AI-Studio leftovers: `metadata.json`, Gemini `.env.example`, README banner, package name `react-example`, `vite.config.ts` `DISABLE_HMR` block, `@` alias pointing at repo root. | Delete/replace. Name the package `codeplo-web`. Alias `@` → `src`. |
| A4 | `index.html` loads Google Fonts from the network (violates embedded/CSP/offline). | Self-host with `@fontsource-variable/plus-jakarta-sans` and `@fontsource-variable/jetbrains-mono`; remove `preconnect` links. |
| A5 | State-based "routing" (`activeView` in context); no URLs, no deep links, back button broken. | TanStack Router (plan §1.3). See §4.3. |
| A6 | Required plan stack missing: TanStack Query, shadcn/Radix-style primitives, uPlot, generated API client. | Add (§4.1). |

**B. Architecture**

| # | Problem | Required fix |
|---|---|---|
| B1 | `context/CodeploContext.tsx` (947 lines) is a mock backend inside React state. | Delete. Replace with API layer + TanStack Query + a separate `src/mock/` server (§7). Only tiny UI-state contexts remain (theme, sidebar). |
| B2 | `data/mockData.ts` holds fixtures coupled to UI types. | Move to `src/mock/seed.ts`, shaped like **API responses**, excluded from the production bundle. |
| B3 | `types/index.ts` hand-written camelCase types. | Delete; use types generated from OpenAPI (snake_case, per plan's JSON examples such as `interval_s`, `retain_images`). |
| B4 | No auth at all: no login, no session, no `401` handling, no CSRF. | Implement §6.1. |

**C. Behavior that contradicts the plan**

| # | Where | Problem | Plan reference |
|---|---|---|---|
| C1 | `StepUpModal`, `verifyStepUp` | Accepts **any 6 digits**; step-up pre-active for 30 min; elevation 15 min; text says 5. | §3.5: server-validated TOTP, default window 5 min, `step_up_until` from the session. |
| C2 | `DeadManBanner`, context timer | Countdown and auto-revert run **in the browser**. | §3.10: revert is a daemon-independent systemd timer; UI only renders the **server** `confirm_deadline`. |
| C3 | `SecurityView` | Tier 0 (read-only) checks show an **Apply** button; Tier 1 applies with no plan review; Tier 2/3 skip preflight; console-ack checkbox is **pre-checked and never used**; Tier 3 apply is always enabled; tier summary cards are hard-coded text ("Compliant", "Active"). | §3.10 tiers, preflight, console-access acknowledgement, plan-only flags. |
| C4 | `CodeploContext.appendAudit` | Fake 32-bit hash; client writes audit rows. | §3.9: server writes append-only, hash-chained audit rows; the UI never creates them. |
| C5 | `SettingsView` | Chain "verification" runs in the browser; role matrix hard-coded (and incomplete). | §3.9 + §3.5: server verification; roles/permissions from `GET /roles` and `GET /permissions`. |
| C6 | `rollbackDeployment` | Requires step-up. | §3.5 does **not** list rollback as step-up. Remove. (Secret replacement/rotation, restore, root terminal, hardening apply, user admin **do** require it.) |
| C7 | `triggerDeploy` | After the last step sets `status:'succeeded'` while `step:'observing'`; `observing` is not a terminal state. UI colors every non-succeeded, non-building state red. | §3.8 state machine; render all 12 states (§6.3). |
| C8 | `addResource` | New resource is instantly `running` with a fake digest. | A new resource is `stopped` with `active_deployment_id = null` until its first deployment. |
| C9 | `DeployModal` | Dockerfile mode has **no repo URL/branch/deploy key**; compose/static always offered; `pidsLimit` hard-coded 64; no domain field; creates but does not deploy. | §7 P4/P5: `resource_sources`; compose/static are 🟡 (flag-gated); deploy flow §5.3.3–4. |
| C10 | `ResourceDetailView` | Env vars are local component state (never persisted); no secret rotate; volumes tab static; "Live Logs" shows deployment logs not container logs; Overview shows hard-coded "200 OK (Attempt 1/5)", "60s", "Pre-flight Passed"; only the first domain is shown; no start/stop/restart/delete; isolation tab prints hard-coded "PASS". | §3.4 routes; §3.11; §7 P4. |
| C11 | `XTerminal.tsx` (843 lines) | A **fake shell implemented in the browser** (command table, local line editing, tab-completion, fake `su` flow). Cannot work with a real PTY. | §7 P3: thin xterm.js bound to a WebSocket with a single-use ticket; the PTY does line editing. |
| C12 | `TerminalView` | Offers root toggle regardless of `terminal.allow_root`; "ops-deployer" hard-coded; container exec lives on the host terminal page. | §7 P3: host shell needs an operator identity; root disabled by default; container shells live in the resource view. |
| C13 | `GitCiCdView` | Top-level page (plan puts per-resource Git/CI in the resource view, connections in Settings); hard-coded deploy key; **"Simulate webhook" triggers a deploy from the client**; all deliveries rendered green regardless of status; claims "nonce + 300 s timestamp window" (plan: delivery-ID dedupe). | §5.1, §7 P5. |
| C14 | Actions YAML snippet | Uses `steps.build.outputs.digest` but no step has `id: build`; no registry login step; sends the token in **both** the URL and an `Authorization` header; hard-coded domain/token. | §7 P5: token is in the URL path (`/hooks/deploy/{token}`), hashed at rest, shown **once**. |
| C15 | `DeployHook` type | Stores a full `url` (impossible if the token is hashed). | Show `token_prefix` always; full URL/token only in the create/rotate response. |
| C16 | `ProxyDomainsView` / `addDomainRoute` | `cert_status:'issued'` instantly; no hostname validation/uniqueness; panel domain not protected; no delete; no DNS/verification feedback. | §3.12, §7 P2 rules. |
| C17 | `SetupWizardModal` | A modal reachable from the header any time; bootstrap token is pasted from a file path; **owner password and TOTP code are pre-filled**; recovery codes are a hard-coded list; no QR/secret; domain step has no verification and is not optional; operator is free text; completion silently "activates Tier 1". | §3.12, §5.3.1, AS-4. |
| C18 | `Sidebar`/routes | IA differs from plan §5.1 (merged "Metrics & Backups", top-level Git page, "Workloads"). Module key `backups` does not exist in the plan. | §5.1; module keys `hardening, edge, terminal, deploy, git, monitor, notify, databases, templates, extensions, mcp`. |
| C19 | Seeds | `monitor`, `notify`, `templates`, `mcp` modules enabled by default; flags `ssh_guarded_apply`, `terminal_root`, `docker_user_isolation` default **on**; `retain_images` 3 in places; system SQLite "backup" with `resource_id: 'codeplo_system'` (not a resource). | §8.1: non-MVP modules default off; Tier 2/3 apply flags default off; `terminal.allow_root=false`; `retain_images=5`. |
| C20 | Many views | Status always green, no loading/empty/error states, no permission gating, no a11y on modals (no focus trap/ESC/`aria-modal`), clipboard errors unhandled. | §5.2 "permission-aware UI". |

---

## 3. Deliverable layout

Produce a monorepo-shaped output so it drops into the project (plan §4.2):

```
codeplo/
├── api/openapi.yaml                    # NEW — the contract (OpenAPI 3.1), single source of truth
├── web/                                # the SPA (was proto/)
│   ├── package.json  vite.config.ts  tsconfig.json  index.html
│   ├── .env.example  .gitignore  README.md  CHANGES.md
│   ├── scripts/                        # check-no-external.mjs, size-budget.mjs
│   ├── src/
│   │   ├── main.tsx  router.tsx
│   │   ├── api/                        # schema.d.ts (generated), client.ts, transport.ts, events.ts,
│   │   │                               #   hooks/ (query + mutation hooks per domain), errors.ts
│   │   ├── components/
│   │   │   ├── ui/                     # shadcn-style primitives (Radix + cva)
│   │   │   └── app/                    # StatusBadge, LogViewer, TerminalPane, ScoreRing, DiffView,
│   │   │                               #   DeadManBanner, StepUpDialog, ConfirmDialog, PermissionGate, CopyButton
│   │   ├── features/                   # one folder per domain (see §4.3)
│   │   ├── lib/                        # permissions.ts, format.ts, time.ts, theme.ts
│   │   ├── mock/                       # mock backend — dev/demo only, never in prod bundle
│   │   └── styles/index.css            # keep existing tokens
│   └── docs/                           # implementation-plan.md, FRONTEND_AGENT_BRIEF.md, CONTRACT.md
└── (backend repo parts are not your concern)
```

Also write `docs/CONTRACT.md`: a human-readable summary of conventions in §5 plus the **list of amendments** (§5.6) so the backend team can implement exactly what the UI expects.

---

## 4. Target stack and structure

### 4.1 Dependencies (pin exact working versions after `npm install`; do not use `^` for security-relevant libs)

| Purpose | Package |
|---|---|
| UI runtime | `react`, `react-dom` (19 is acceptable; plan says "18+") |
| Routing | `@tanstack/react-router` |
| Server state | `@tanstack/react-query` |
| Typed API | `openapi-fetch` (runtime), `openapi-typescript` (dev, generates `schema.d.ts`) |
| Primitives | `@radix-ui/react-dialog`, `@radix-ui/react-tabs`, `@radix-ui/react-switch`, `@radix-ui/react-tooltip`, `class-variance-authority`, `clsx`, `tailwind-merge` |
| Icons | `lucide-react` (keep existing major) |
| Terminal | `@xterm/xterm`, `@xterm/addon-fit`, `@xterm/addon-web-links` |
| Charts | `uplot` (small, fast; plan §5.4) |
| Fonts | `@fontsource-variable/plus-jakarta-sans`, `@fontsource-variable/jetbrains-mono` |
| Build | `vite`, `@vitejs/plugin-react`, `tailwindcss`, `@tailwindcss/vite`, `typescript` |
| Tests | `vitest`, `@testing-library/react`, `jsdom` |

Not required now (document as deliberate deferrals in `CHANGES.md`): TanStack Table (tables are small; add when lists grow), CodeMirror 6 (needed only when compose editing is enabled; use read-only `<pre>` for now).

### 4.2 Environment modes

`web/.env.example`:
```
VITE_API_MODE=mock          # mock | live
VITE_API_BASE=/api/v1       # live API base
VITE_PROXY_TARGET=http://127.0.0.1:7443   # dev proxy target (loopback bootstrap listener)
```
- `mock`: `src/api/transport.ts` swaps `fetch`, `EventSource`, and `WebSocket` for in-memory implementations from `src/mock/`. The mock code is loaded with a dynamic `import()` guarded by `import.meta.env.VITE_API_MODE === 'mock'` so a `live` production build contains **zero** mock code (verify in CI by grepping `dist/`).
- `live`: native `fetch`/`EventSource`/`WebSocket`, same-origin, cookies included.
- Dev server proxies `/api` (including WebSocket upgrade) to `VITE_PROXY_TARGET`.

### 4.3 Routes and navigation (plan §5.1 — must match)

TanStack Router, code-based routes. Static navigation; module-owned items are hidden when `GET /modules` says disabled.

```
/login                              pre-auth
/setup                              pre-auth, only while GET /setup/state says setup_required
/                                   Dashboard
/projects                           project list (+ create)
/projects/$projectId?env=           environments tabs + resources (+ New resource)
/resources/$resourceId?tab=         resource detail (tabs below)
/server/security                    Host Hardening
/server/terminal                    Host terminal (+ session history)
/server/proxy                       Proxy & Domains
/server/monitoring                  (module: monitor)
/templates                          (module: templates)
/notifications                      (module: notify)
/backups                            (module: databases)
/settings?tab=                      users · roles · tokens · git · modules · audit · system
```

Sidebar groups: **Dashboard · Projects · Server (Security, Terminal, Proxy & Domains, Monitoring) · Templates · Notifications · Backups · Settings**. Top bar: breadcrumb (project / environment / resource), `Ctrl+K` palette, theme toggle, user menu (logout), elevation badge (shown only while the server-reported `step_up_until` is in the future). **Remove** the "Setup Wizard" button and the hard-coded user chip.

Resource tabs: **Deployments · Overview · Source & CI/CD · Environment & Secrets · Domains · Volumes · Limits · Isolation · Logs · Shell**.

Feature folders: `setup, auth, dashboard, projects, resources, security, terminal, domains, monitoring, notifications, backups, templates, settings`.

---

## 5. Contract decisions

### 5.1 Conventions

- Base path `/api/v1`. JSON, **snake_case** fields (matches plan §3.3 examples). Timestamps ISO-8601 UTC. IDs are opaque strings (ULIDs).
- Errors: `{ "code": "string", "message": "string", "details": { } }` with HTTP status. Codes the UI must handle: `unauthenticated` (401), `forbidden` (403), `step_up_required` (403), `validation_failed` (422, `details.fields` map), `conflict` (409), `not_found` (404), `rate_limited` (429, `Retry-After`), `module_disabled` (404), `preflight_failed` (409, `details.checks[]`).
- Lists: `?limit=&cursor=`; response `{ items: [], next_cursor: string|null }`. Audit also supports `actor, action, from, to`.
- `server_now` (ISO) is included in responses that carry deadlines (`/hardening/changes`, `/auth/me`, `/system/status`) so the UI computes countdowns from **server time offset**, not the client clock.

### 5.2 Auth, CSRF, step-up

- Session cookie is HttpOnly; JS never sees it. `GET /auth/me` returns `{ user, role, permissions[], step_up_until|null, csrf_token, server_now, bootstrap: boolean }`. Send `X-CSRF-Token` on every non-GET request.
- Login: `POST /auth/login {email,password}` → `{ totp_required: true, challenge }` or session; `POST /auth/totp/verify {challenge, code}`.
- Step-up: `POST /auth/step-up {code}` → `{ step_up_until }`.
- **Auto step-up in the client** (`src/api/client.ts` middleware): on `403 step_up_required`, open the step-up dialog, then **retry the original request once**; reject if cancelled. Do not pre-check elevation client-side to *skip* the server — the server decides.
- `401` → clear caches, redirect to `/login?next=`. A first-run server (`setup_required`) redirects to `/setup`.
- Cookies/CSRF in bootstrap mode (plan §3.12): the loopback listener uses a non-`__Host-` cookie; the UI needs no special handling beyond `bootstrap: true` showing a banner "Bootstrap session (SSH tunnel) — configure HTTPS in Settings".

### 5.3 Permission registry (canonical copy lives in Go; UI reads `GET /permissions`)

The UI references these strings via a typed `can('...')` helper and `<PermissionGate>`:

`resource:read resource:write resource:delete resource:control deploy:create deploy:cancel deploy:rollback env:read env:write domain:write project:write terminal:host terminal:root terminal:container hardening:read hardening:apply edge:read edge:reconcile git:write hook:manage audit:read users:manage tokens:manage modules:manage flags:manage system:read system:metrics system:cleanup notify:manage backup:manage backup:restore template:deploy`

There is **no** `secret:reveal`. Hide or disable controls the user lacks; never hide server errors.

### 5.4 Real-time (single SSE connection)

`GET /events` (SSE). One connection app-wide, created after login, auto-reconnect with backoff, `Last-Event-ID` supported. Event `type` values and the Query keys they invalidate:

| Event | Payload | Effect |
|---|---|---|
| `resource.updated` | `{resource_id}` | invalidate resource, project tree |
| `deployment.updated` | `{deployment_id, resource_id, status, step}` | patch/invalidate deployments + resource |
| `deployment.log` | `{deployment_id, seq, at, stream, line}` | append to open log viewers only |
| `hardening.changed` | `{change_id?}` | invalidate checks, changes, score |
| `edge.changed` | `{domain_id?}` | invalidate domains, edge status |
| `audit.appended` | `{seq}` | invalidate audit list head |
| `metrics.sample` | `HostMetrics` | update header/dashboard gauges (no refetch) |
| `session.step_up` | `{step_up_until}` | update `me` |
| `module.changed` / `flag.changed` | `{key}` | invalidate modules/flags |

Container logs use their own SSE: `GET /resources/{id}/logs?tail=&since=` (backpressure-safe: UI caps buffer at 5,000 lines).

### 5.5 Endpoint catalog the UI consumes

Everything in plan §3.4, plus the amendments in §5.6. Operation IDs should be `camelCase` verbs (`listResources`, `createDeployment`, …) so generated hooks read naturally.

| Domain | Operations (method path → permission) |
|---|---|
| Setup | `GET /setup/state` · `POST /setup/admin` (needs `token`) · `POST /setup/totp/enroll` → `{secret, otpauth_uri}` · `POST /setup/totp/verify` → `{recovery_codes[]}` · `POST /setup/verify-domain` · `POST /setup/panel-domain` · `GET /setup/operator-candidates` · `POST /setup/operator` |
| Auth | `POST /auth/login` · `POST /auth/totp/verify` · `POST /auth/step-up` · `POST /auth/logout` · `GET /auth/me` |
| Users/tokens/roles | `GET/POST /users` · `PATCH /users/{id}` (`users:manage`, step-up) · `GET/POST/DELETE /tokens` (`tokens:manage`; secret returned **once**) · `GET /roles` · `GET /permissions` |
| Audit | `GET /audit` (`audit:read`) · `POST /audit/verify` (amendment) |
| Hardening | `GET /hardening/checks` · `POST /hardening/audit` · `POST /hardening/plan {check_keys[]}` · `POST /hardening/preflight {check_key}` · `POST /hardening/apply {check_key, ack?}` (step-up) · `POST /hardening/confirm/{changeId}` · `POST /hardening/revert/{changeId}` · `GET /hardening/runs` · `GET /hardening/changes` (amendment) |
| Edge | `GET /edge/status` · `POST /edge/reconcile` · `GET/POST/DELETE /resources/{id}/domains` · `GET /edge/certificates` |
| Projects | `GET/POST /projects` · `GET/POST /projects/{id}/environments` |
| Resources | `GET/POST /resources` · `GET/PATCH/DELETE /resources/{id}` · `POST /resources/{id}/{start,stop,restart}` · `GET /resources/{id}/policy` (amendment) |
| Env/volumes | `GET /resources/{id}/env` · `PUT /resources/{id}/env` (secret values write-only; replacing a secret needs step-up) · `GET/POST /resources/{id}/volumes` |
| Deployments | `POST /resources/{id}/deployments` · `GET /resources/{id}/deployments` · `GET /deployments/{id}` · `GET /deployments/{id}/logs` (SSE) · `POST /deployments/{id}/cancel` · `POST /deployments/{id}/rollback` |
| Terminal | `POST /terminal/ticket {target, resource_id?, as_root?}` → `{ticket, expires_at}` · `GET /terminal/host` (WS) · `GET /terminal/resources/{id}` (WS) · `GET /terminal/sessions` |
| Git/CI | `GET/POST /git/connections` · `GET/PUT /resources/{id}/source` · `POST /resources/{id}/deploy-key` (public key only) · `GET/POST /resources/{id}/deploy-hook` (token **once**; `POST …/rotate`) · `GET /resources/{id}/webhook` (URL + secret shown once on create) · `GET /webhooks/deliveries` · `POST /webhooks/github` and `POST /hooks/deploy/{token}` are **server-side only** |
| System | `GET /system/status` · `GET /system/metrics?range=` · `POST /system/cleanup` · `GET /system/version` |
| Modules/flags/settings | `GET /modules` · `PATCH /modules/{key}` · `GET/PATCH /feature-flags` · `GET/PATCH /settings` |
| Notifications | `GET/POST /notifications/channels` · `PATCH/DELETE /notifications/channels/{id}` · `POST /notifications/channels/{id}/test` (amendment) · `GET/POST /alerts/rules` · `PATCH /alerts/rules/{id}` |
| Backups | `GET/POST /resources/{id}/backups` · `GET /backups/runs` · `POST /backups/{id}/run` · `POST /backups/{id}/restore` (step-up) |
| Templates | `GET /templates` · `POST /templates/{key}/deploy {project_id, environment, name}` |

### 5.6 Additive amendments to the plan (record them; do not hide)

These endpoints/fields are not in plan §3.4 but are required for correct UX. Add them to `openapi.yaml`, list them in `docs/CONTRACT.md`, and append a short "v1.2 amendments" note to `docs/implementation-plan.md` §3.4:

1. `GET /hardening/changes` — pending/recent changes with `confirm_deadline` and `server_now` so the banner survives reloads.
2. `POST /audit/verify` — server-side chain verification (UI mirror of `codeplo doctor --verify-audit`).
3. `POST /notifications/channels/{id}/test`.
4. `GET /resources/{id}/policy` — effective isolation policy result for the resource (controls, values, violations).
5. `GET /setup/operator-candidates`, `POST /setup/operator`, `POST /setup/totp/enroll|verify` — wizard steps the plan implies (§5.3.1, D10).
6. `GET/PATCH /settings` — key/value settings (e.g. `terminal.allow_root`, `step_up_window_s`, `observation_window_s`, `retain_images`).
7. `resources.config` shape (extends plan §3.3 example):
```json
{
  "image": { "mode": "pull|build", "ref": "ghcr.io/org/app:tag", "dockerfile": "Dockerfile", "context": "." },
  "port": 3000,
  "healthcheck": { "path": "/healthz", "interval_s": 5, "retries": 12, "start_period_s": 5, "timeout_s": 3 },
  "limits": { "memory_mb": 256, "cpus": 0.5, "pids_limit": 512 },
  "isolation": { "cap_add": [], "user": null, "trusted": false },
  "restart": "unless-stopped",
  "retain_images": 5
}
```
Resource `type` ∈ `app|compose|static|database|service`; `status` ∈ `running|deploying|stopped|failed|degraded`.

---

## 6. Behavior rules per domain (must match the plan)

### 6.1 Auth, setup, first run (plan §3.12, §5.3.1)

- `/setup` is a full-page flow (not a modal), reachable only while `GET /setup/state` reports `setup_required`. Token comes from the URL (`?token=`); no pasted file path, **no prefilled credentials**.
- Steps: token check → owner email/password (policy hints) → **TOTP enrollment** (render `otpauth_uri` as a QR code — generate client-side with a tiny dependency-free QR encoder or `qrcode` — plus the secret text; verify a real code; then show **server-issued** recovery codes once, require an explicit "I saved them" check) → *optional* panel domain (`verify-domain` shows DNS/target status; certificate issuance progress; **nothing switches until issuance succeeds**; "Skip — use SSH tunnel only") → **operator identity** (choose from `operator-candidates` or create) → run audit → show plan → apply Tier 1 → optional firewall with console-access acknowledgement → confirm.
- After completion the wizard is unreachable. Remove all prefilled demo values from components (mock may display a demo hint, §7).

### 6.2 Host hardening (plan §3.10) — the most safety-critical screen

- Data comes only from `GET /hardening/checks` (`tier`, `status`, `detect_detail`, `plan_diff`, `driver`, `apply_enabled`, `requires_ack`). Score is server-provided.
- **Tier 0:** informational only. No Apply button.
- **Tier 1:** `Plan` → diff modal → `Apply`.
- **Tier 2/3:** `Plan` → diff → **Preflight** (list each check pass/fail with detail; any failure disables Apply and shows why) → **console-access acknowledgement checkbox, unchecked by default, required, sent in the apply request** → step-up (automatic via client middleware) → Apply. If `apply_enabled` is false (feature flag `hardening.apply_tier2` / `hardening.apply_tier3`, default **off**), show a clear "Plan-only" state with the exact diff and a copyable command set, and no Apply button.
- After a Tier 2/3 apply the **server** returns a change with `confirm_deadline`. `DeadManBanner` renders the countdown from `confirm_deadline − server_now`, offers **Confirm connectivity** (`POST /hardening/confirm/{id}`) and **Revert now** (`POST /hardening/revert/{id}`), and survives page reloads (`GET /hardening/changes`). The UI **never** reverts anything on its own when the timer hits zero — it shows "Reverting…" and refetches. Tell the user plainly that if they lose connectivity the system reverts itself.
- SSH port changes are not offered anywhere (plan §3.10.5).
- Tier summary cards derive from checks; no hard-coded status strings.

### 6.3 Deployments (plan §3.8)

- Status badge component maps all 12 states: `queued, preparing, building, starting, verifying, switching, observing` (in-progress, animated, "Cancel" available for `queued…verifying`), `succeeded` (green), `failed` (red), `cancelled` (grey), `interrupted` (amber, with "recovered after restart" hint), `rolled_back` (amber, shows auto-rollback reason).
- Show `step`, pre-flight result (disk/RAM guard), health-check attempts, observation window remaining (`observe_until`), image digest, trigger, commit.
- Rollback: available on previous `succeeded` deployments that retain an image digest; **no step-up**; confirm dialog stating "application rollback does not roll back database changes".
- Redeploy/Deploy-now sends the correct mode; compose/static creation options only render when their flags are enabled.
- New-resource dialog collects: name (strict slug `^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`), type/mode, image ref **or** Git source (repo URL, branch, dockerfile, build path), port, health path, optional domain, memory/CPU/pids limits with **RAM-aware warning** (compare with host total RAM from `/system/status`; warn on on-host builds below 2 GB and recommend the registry-pull path), then **creates and starts the first deployment** and navigates to the live log.

### 6.4 Environment and secrets (plan §3.9)

- Non-secret values are shown; secret values are **never** returned or displayed — only "write-only" with `updated_at`. Actions: add, replace/rotate (step-up), delete. Validate `KEY` against `^[A-Za-z_][A-Za-z0-9_]*$`; do not silently uppercase. Scope: `build | runtime | both`; explain that build-scope secrets use BuildKit secret mounts.

### 6.5 Terminal (plan §7 P3)

- `TerminalPane` is a thin xterm.js view: `onData` → send; incoming bytes → `term.write`; resize → control message; **no local echo, no local line editing, no command table**.
- Connect flow: `POST /terminal/ticket` (single-use, ≤30 s) → open WebSocket with the ticket (query param `ticket=`), `binaryType='arraybuffer'`. Frame protocol: binary frames = raw PTY bytes; text frames = JSON control `{"type":"resize","cols":N,"rows":N}` (client→server) and `{"type":"exit","code":N}` / `{"type":"error","message":"…"}` (server→client).
- Host shell is **unavailable until an operator identity exists**: show a blocking explanation with a link to Security (operator check). Root option appears only if `settings['terminal.allow_root']` is true **and** the user has `terminal:root`; selecting it triggers step-up via the ticket request (server decides).
- Show idle-timeout countdown and reconnect UI (new ticket each time). Closing the tab/route closes the socket.
- Container shells live in the resource **Shell** tab (`terminal:container`). Viewer role never sees terminal controls.
- Session history table from `GET /terminal/sessions`.

### 6.6 Git and CI/CD (plan §7 P5)

- Resource → **Source & CI/CD** tab: Git source form (repo URL, branch, dockerfile, build path, path filters, `[skip deploy]` note); per-resource ed25519 **deploy key** (generate → show public key + fingerprint; private half never shown); **webhook panel** (URL + secret shown once at creation; explain HMAC-SHA256 and delivery-ID dedupe); **deploy-hook panel** (token shown once, rotate, `last_used_at`) with a generated GitHub Actions workflow; delivery history with distinct colors for `success | ignored | rejected` and the rejection reason.
- Generated workflow (copy-paste correct):
```yaml
name: Deploy to codeplo
on: { push: { branches: [main] } }
permissions: { contents: read, packages: write }
jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: docker/login-action@v3
        with: { registry: ghcr.io, username: ${{ github.actor }}, password: ${{ secrets.GITHUB_TOKEN }} }
      - id: build
        uses: docker/build-push-action@v6
        with: { push: true, tags: "ghcr.io/${{ github.repository }}:${{ github.sha }}" }
      - name: Trigger codeplo
        run: |
          curl -fsS -X POST "${{ secrets.CODEPLO_DEPLOY_HOOK_URL }}" \
            -H "Content-Type: application/json" \
            -d '{"image":"ghcr.io/${{ github.repository }}:${{ github.sha }}","digest":"${{ steps.build.outputs.digest }}"}'
```
  The secret `CODEPLO_DEPLOY_HOOK_URL` holds the full hook URL (token in the path). Do not send a second auth header. Build the URL from `window.location.origin` + the one-time token.
- Settings → **Git connections** lists `GET /git/connections` (GitHub App arrives later; show empty state explaining deploy keys).
- Remove any client-side "simulate webhook". (The mock may expose a dev-only "simulate delivery" in a `DemoToolbar`, §7.)

### 6.7 Domains and edge (plan §7 P2)

- Add-domain form validates hostname syntax and shows server `conflict` (uniqueness) and `validation_failed` (panel domain reserved) inline. Certificate lifecycle shown (`issuing → issued | error`, expiry date); **Reconcile** button; resource domains are also managed from the resource **Domains** tab (list/add/delete, multiple allowed). Panel domain row is read-only and marked "Panel".

### 6.8 Modules, flags, settings

- Navigation visibility and tab availability come from `GET /modules`. Core modules cannot be disabled. Flag list shows key, description, current value; Tier 2/3 apply flags show a warning that they are release-gated. Settings tab edits server settings (`terminal.allow_root`, step-up window, observation window, retain images, disk pre-flight threshold) — owner/admin only.

### 6.9 Audit

- Table with filters (actor, action, date range), cursor pagination, hash column (truncated, copy), **Verify chain** calls `POST /audit/verify` and renders `{valid, verified_rows, first_invalid_seq?}`. Describe the guarantee honestly: **tamper-evident**, not tamper-proof (plan §3.9). Never call it "immutable".

### 6.10 Quality-of-UI rules

- Every query-driven view has **loading skeleton, empty state, and error state** (with retry). Mutations show pending state and toast success/failure. Destructive actions use `ConfirmDialog`.
- All modals use Radix Dialog (focus trap, ESC, `aria-modal`, return focus). All icon-only buttons have `aria-label`. Color is never the only status signal.
- Copy buttons use a shared `CopyButton` that handles clipboard failure.
- Respect `prefers-reduced-motion`. Keyboard: `Ctrl/⌘+K` palette; palette is built from routes + resources + permitted actions (no "Setup wizard" entry).
- Responsive down to 360 px; tables scroll inside their own container.

---

## 7. Mock backend specification (`src/mock/`)

A **fake Go server** living in the browser, honoring the OpenAPI contract exactly. It exists so the UI can be built and demoed without the backend. Do not leak mock logic into UI code.

- `transport.ts` installs `fetch` (routes `/api/v1/*`), `EventSource` (`/api/v1/events`, `/resources/{id}/logs`, `/deployments/{id}/logs`), and `WebSocket` (`/api/v1/terminal/*`) replacements.
- In-memory DB seeded from `seed.ts`. Latency 60–200 ms random. A `?mock_fault=` query option (or `localStorage.mock_fault`) can force `500`, `429`, `step_up_required`, and slow responses for UI resilience testing.
- **Auth:** demo owner `owner@example.com` / `demo-password-change-me`, TOTP code `123456` (accepted only in mock; the login page shows a visible **"Demo mode"** hint only when `VITE_API_MODE=mock`). Step-up window default 300 s (from settings). Enforce CSRF header presence. Enforce permissions per role (seed 4 roles + a viewer user for testing).
- **Audit:** every mutating handler appends a row in the same handler. Real **SHA-256 via `crypto.subtle`**: `hash = SHA256(prev_hash || seq || at || actor || action || target || canonical_json(meta))`. `POST /audit/verify` recomputes the chain; expose a dev hook to tamper a row to demo detection.
- **Hardening:** checks seeded per plan tiers; `apply` for Tier 2/3 returns a change with `confirm_deadline = server_now + settings.hardening_revert_window_s (default 300)`; a **server-side timer** in the mock reverts the change if not confirmed and emits `hardening.changed`; confirm/revert endpoints work; `apply_enabled` false for tier 2/3 unless the flag is on; preflight returns realistic pass/fail items (including a failing "no proven key login" case for a toggled scenario); console-ack required.
- **Deployments:** `createDeployment` runs the state machine with timers: `queued → preparing → building (skipped for image mode) → starting → verifying → switching → observing → succeeded`, emitting `deployment.updated` and `deployment.log`; supports `cancel`, failure injection (bad image ref ⇒ `failed`), `interrupted` seed example, auto `rolled_back` example during observation; pre-flight blocks when mock disk < threshold. Rollback creates a new `rollback` deployment pointing at a retained digest. A resource only gets `active_deployment_id` at the `switching` step.
- **Edge:** adding a domain starts `cert_status: issuing` then flips to `issued` after ~3 s (or `error` for hostnames ending `.invalid`); enforce hostname uniqueness and reserved panel domain.
- **Terminal:** `POST /terminal/ticket` issues a single-use ticket (30 s); `MockWebSocket` provides a tiny fake PTY (prompt, a handful of canned commands, echo handled **by the mock shell**, resize noted, idle timeout). It lives only under `src/mock/` and may reuse ideas from the prototype's `XTerminal.tsx` — but none of that code may remain in `src/components` or `src/features`.
- **Secrets:** env list returns `value: null` for secrets with `is_secret: true`; replacing a secret requires step-up; no reveal route exists.
- **Hooks/keys:** token/secret values are returned once at creation, then only prefixes.
- **Modules/flags/settings:** seed per plan §8.1 — enabled: `hardening, edge, terminal, deploy, git`; disabled: `monitor, notify, databases, templates, extensions, mcp`; flags `hardening.apply_tier2=false`, `hardening.apply_tier3=false`, `deploy.compose=false`, `deploy.static=false`, `registry_pull_hook=true`; settings `terminal.allow_root=false`, `retain_images=5`, `observation_window_s=60`, `step_up_window_s=300`.
- **Demo toolbar** (only in mock mode, bottom-left, collapsible): switch persona (owner/admin/deployer/viewer), fast-forward timers, tamper audit row, trigger simulated webhook delivery, toggle fault injection. Never rendered in `live`.
- Seed data mirrors the prototype's domain content (storefront, core-api, job-worker, staging) but in API-response shape and plan-correct defaults.

---

## 8. Work packages (execute in order; each ends with a green build)

After **every** package run: `npm run typecheck && npm run lint && npm run test && npm run build`. Commit-style notes go into `CHANGES.md`.

**WP0 — Toolchain repair and cleanup**
Fix A1–A4. Rename to `codeplo-web`. Add scripts: `dev`, `build`, `preview`, `typecheck` (`tsc --noEmit`), `lint`, `test`, `gen:api` (`openapi-typescript ../api/openapi.yaml -o src/api/schema.d.ts`), `check:external`, `check:size`. Self-host fonts. `npm ci` must pass on a clean clone.
*Accept:* clean install, `npm run build` passes, `dist/` has no external URLs.

**WP1 — Contract**
Write `api/openapi.yaml` (3.1) covering §5.5 + §5.6 with schemas, enums (12 deployment states, tiers 0–3, statuses), error schema, SSE event schemas (as `components.schemas.Event*` + `x-events` doc), security scheme (cookie + CSRF header). Validate with `npx @redocly/cli lint`. Generate `schema.d.ts`. Write `docs/CONTRACT.md`.
*Accept:* lint passes with no errors; every endpoint in §5.5 present with permission noted in `x-permission`.

**WP2 — API layer**
`src/api/client.ts` (openapi-fetch + middleware: CSRF, error normalization to `ApiError`, 401 redirect, **step-up retry**), `transport.ts` (mode switch), `events.ts` (single SSE with backoff + query invalidation per §5.4), `hooks/*` (query keys factory, one hook per operation group). `lib/permissions.ts` with `useMe`, `can`, `<PermissionGate>`.
*Accept:* unit tests for error mapping, step-up retry-once, CSRF header, SSE reconnect/invalidate (fake timers).

**WP3 — Mock backend**
Implement §7 fully. Tests (vitest) drive the mock over its `fetch`: auth/CSRF/permissions matrix, audit chain verify + tamper, deployment state machine (all 12 states reachable), dead-man timer revert, domain uniqueness/cert flow, ticket single-use, secret write-only, module gating (`404 module_disabled`).
*Accept:* ≥ 90% of handlers covered; production-mode build contains no `src/mock` code (test greps `dist/`).

**WP4 — UI foundation**
Port tokens; build `components/ui/*` (button, badge, card, dialog, input, select, switch, tabs, table, skeleton, toast) with cva; `components/app/*` (StatusBadge covering all enums, ScoreRing, DiffView, LogViewer with virtualization/auto-follow/pause/filter, CopyButton, ConfirmDialog, PermissionGate, EmptyState, ErrorState). Router, AppShell, Sidebar/Header per §4.3, theme provider. Global `StepUpDialog` and `DeadManBanner` (server-driven).
*Accept:* every route renders a skeleton; keyboard navigation and ESC/focus-return verified in tests.

**WP5 — Auth and setup**
`/login` (password → TOTP), `/setup` wizard per §6.1, `bootstrap` banner, logout, 401/403 handling, elevation badge.
*Accept:* fresh mock (`setup_required`) routes to `/setup`; completing it lands on dashboard; no prefilled secrets in source (`grep -R "SecurePassw0rd\|123456" src --include=*.tsx` returns nothing outside `mock/`).

**WP6 — Security (hardening) screen**
Implement §6.2 completely, including preflight, ack, plan-only mode, server-driven banner.
*Accept:* tests: Tier 0 has no Apply; Tier 2 apply disabled until preflight passes **and** ack checked; plan-only when flag off; banner countdown derived from `server_now`; revert at zero is performed by the mock server, not the UI.

**WP7 — Projects, resources, deployments**
Projects list/detail, New-resource dialog, resource detail tabs (Deployments, Overview, Environment & Secrets, Domains, Volumes, Limits, Isolation, Logs) per §6.3, §6.4, §6.7. Isolation tab reads `/resources/{id}/policy` and also shows the plan §3.11 defaults table as reference text (not as fake "PASS").
*Accept:* create → auto-deploy → observe → succeeded; cancel; failed deploy; rollback; secret rotate needs step-up; viewer role sees read-only UI.

**WP8 — Terminal**
Replace `XTerminal.tsx` with `TerminalPane` + `useTerminal` hook (ticket, WS, resize, reconnect, idle timer). Host terminal page and resource Shell tab per §6.5. Delete the fake shell from components.
*Accept:* tests with `MockWebSocket` verify ticket single-use, resize message, no local echo in UI code; operator-missing state; root hidden unless allowed.

**WP9 — Git & CI/CD**
Source & CI/CD tab per §6.6; Settings → Git connections.
*Accept:* hook/token shown once; generated workflow matches §6.6 text exactly (snapshot test); deliveries show distinct statuses.

**WP10 — Proxy & domains** per §6.7. **WP11 — Monitoring, notifications, backups, templates** (module-gated): uPlot charts from `GET /system/metrics?range=`; channels CRUD + test; alert rules toggles; backups schedules/runs/restore (step-up, pre-restore snapshot note); templates catalog + deploy via API (compose flag gating).

**WP12 — Settings** (users create/disable with step-up, roles matrix from API, tokens create/revoke with one-time secret, modules & flags, settings, audit with filters/verify, system/cleanup/version).

**WP13 — Command palette, dashboard, polish** (dashboard uses real queries: health, recent deployments, score, alerts; palette per §6.10; a11y pass; reduced motion).

**WP14 — Hardening the build**
`scripts/check-no-external.mjs` (fails on third-party URLs in `dist/`), `scripts/size-budget.mjs` (initial JS ≤ 250 KB gzip, each lazy route chunk ≤ 120 KB gzip; xterm and uPlot loaded lazily), CSP guidance in `docs/CONTRACT.md` (`default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; frame-ancestors 'none'`), `go:embed` notes (`web/dist` is the artifact; SPA fallback to `index.html` for non-`/api` paths).
*Accept:* live-mode build passes all checks.

---

## 9. Quality gates (all must pass at the end)

1. `npm ci && npm run typecheck && npm run lint && npm run test && npm run build` (both `VITE_API_MODE=mock` and `live`).
2. `npx @redocly/cli lint ../api/openapi.yaml` clean; `npm run gen:api` produces no diff.
3. `npm run check:external` and `npm run check:size` pass on the **live** build; `dist/` (live) contains no strings from `src/mock/`.
4. Grep guards (add as a test): no `reveal`, no `localStorage` except the theme key, no `dangerouslySetInnerHTML`, no `eval`/`new Function`, no `fetch(` outside `src/api`, no `appendAudit`, no hard-coded user names/emails/IPs outside `src/mock`.
5. Manual walkthrough in mock mode (record results in `CHANGES.md`): first-run wizard → login → create resource → deploy → rollback → add domain → apply firewall plan with ack → let the dead-man timer expire → verify audit chain → open host terminal (operator required) → switch persona to viewer and confirm controls disappear.
6. Accessibility smoke: no unlabeled buttons/inputs (script or `@testing-library` queries), dialogs trap focus.

---

## 10. Final report format (`CHANGES.md`)

1. Summary of what changed (≤ 15 lines).
2. Table: prototype defect ID (A1…C20) → resolved / partially / deferred, with file paths.
3. Amendments made to the contract (mirrors §5.6) and where they are documented.
4. Judgment calls and deliberate deferrals (TanStack Table, CodeMirror, anything else) with reasons.
5. Commands to run (mock and live), env variables, how the Go server should serve the build.
6. Known gaps/risks the backend team must watch.

Do **not** include time estimates anywhere.

---

## 11. Out of scope (do not build)

Multi-server/agent UI, SSO/passkeys, GitHub App flows, Railpack/Nixpacks selectors, wildcard certs, billing, cloud provisioning, DNS-provider integrations, traffic analytics, i18n, Prometheus, a general plugin marketplace UI. Features marked ⚪ in the plan stay absent. Extensions and MCP modules show only their module toggle and an informational panel when enabled.

---

## 12. Appendix

### 12.1 Old → new screen map

| Prototype | Becomes |
|---|---|
| `DashboardView` | `/` (real queries; no hard-coded cards) |
| `ProjectsView` | `/projects`, `/projects/$projectId` |
| `ResourceDetailView` | `/resources/$resourceId` (tabbed; split per tab into `features/resources/*`) |
| `SecurityView` | `/server/security` |
| `TerminalView` + `XTerminal` | `/server/terminal` + resource Shell tab (`TerminalPane`) |
| `ProxyDomainsView` | `/server/proxy` + resource Domains tab |
| `GitCiCdView` | resource **Source & CI/CD** tab + Settings → Git |
| `OperationsView` | `/server/monitoring`, `/notifications`, `/backups` |
| `TemplatesView` | `/templates` |
| `SettingsView` | `/settings?tab=` |
| `SetupWizardModal` | `/setup` |
| `StepUpModal`, `DeadManBanner`, `CommandPalette` | `components/app/*` (server-driven / permission-aware) |

### 12.2 Field-rename rule

Mechanically convert camelCase → snake_case (`memoryMb→memory_mb`, `intervalS→interval_s`, `retainImages→retain_images`, `activeDeploymentId→active_deployment_id`, `confirmDeadline→confirm_deadline`, `detectDetail→detect_detail`, `planDiff→plan_diff`, `certStatus→cert_status`). Structural changes beyond renaming are listed in §5.6 item 7. Do **not** write a camel↔snake mapping layer; use generated types as-is.

### 12.3 Status → presentation

| State | Tone | Icon | Notes |
|---|---|---|---|
| queued, preparing, building, starting, verifying, switching | brand, animated | spinner | Cancel allowed (not after `switching`) |
| observing | brand/teal | eye | shows window countdown |
| succeeded | green | check | |
| failed | red | x-circle | show `error` |
| cancelled | grey | slash | |
| interrupted | amber | alert | "recovered after restart" |
| rolled_back | amber | rotate-ccw | show reason |

### 12.4 Definition of done

All WPs accepted, all gates in §9 green, `CHANGES.md` complete, no item from §2.2 left unaddressed without a recorded reason, and a backend developer can implement `api/openapi.yaml` and flip `VITE_API_MODE=live` with no UI code changes.
