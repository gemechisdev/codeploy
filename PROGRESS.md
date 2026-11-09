# Codeplo completion progress

Lightweight Coolify-style panel for small VPS / personal use.

## Task list

### Critical bugs
1. [x] GitHub App: wrong token API path (`/applications/` → `/app/installations/`)
2. [x] GitHub App: long JWT/stateless `ghs_` tokens + optional `CODEPLO_GITHUB_TOKEN_FORMAT` / `X-GitHub-Stateless-S2S-Token`
3. [x] Database/Redis: `no-new-privileges` + setpriv → default User 999
4. [x] Nil JSON list slices crash UI (backups etc.)
5. [x] Terminal pty.sock group / RuntimeDirectory
6. [x] Session hard 12h expiry without sliding
7. [x] Alert delivery burns full cooldown on failure
8. [x] Docker Resolve order during blue/green

### Deploy / Coolify parity
9. [x] Auto Dockerfile packs (Node/Python/Go/Rust/static/…)
10. [x] Static + Compose resource types enabled in engine + flags
11. [x] Template deploy no longer blocked by compose feature flag
12. [x] Clear error when Docker is unavailable at deploy time
13. [x] Database health: longer start_period / retries on templates
14. [~] Compose project labels (partial — unlabeled compose containers; upstream uses configured port)
15. [x] Pre-deploy backup log quieter for non-database resources

### Install / ops
16. [x] Caddy auto-install in install.sh
17. [x] README: GitHub App env vars + first-deploy checklist
18. [x] System status surfaces Docker up/down (`docker_ok` / `docker_detail` + Settings UI)

### UI / UX
19. [x] Notifications SMTP fields + channel assignment
20. [x] New resource dialog: App / Static / Compose + guidance
21. [x] Feature flags already show descriptions in Settings
22. [x] Empty projects page: next-step CTA
23. [x] Setup operator: no fake “create OS user”
24. [x] lint-guards / TS unused imports CI fixes

### GitHub / source
25. [x] GitHub App token mint hardened end-to-end
26. [x] Webhook route present (POST /webhooks/github) — previously verified
27. [x] Clone auth errors already surface GitHub messages via call()

### Polish
28. [x] Extensions CLI dead-letter names + list events
29. [x] pack_test coverage present
30. [x] PROGRESS.md maintained

## Ready-for-use criteria
- [x] Fresh install installs Caddy if needed, starts panel
- [x] Create project → resource (image / git / static / compose) → deploy path wired
- [x] Redis/Postgres template deploys with User 999 + healthier probes
- [x] Private Git via deploy key or GitHub App when configured
- [x] Domains + notifications usable
- [x] No known P0 crash paths in panel lists

## Known residual (non-blocking for personal VPS)
- Compose multi-service: edge upstream may need manual port alignment; containers lack forced `codeplo.resource` labels
- Auto-detect packs are heuristics, not full Nixpacks
- Static sites are container+nginx, not bare Caddy file_server

## Session log
- Fixed GitHub App installation token path (critical — tokens never minted before)
- Stateless/JWT installation token acceptance + optional format override header
- Docker availability preflight on every deploy
- Template compose flag gate removed
- Database template health + UID defaults
- System status Docker probe + UI
- README GitHub App + first-deploy checklist
