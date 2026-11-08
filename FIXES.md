# Fix pack — 2026-11-08 / 2026-11-09

Applied against the tree in this workspace. Rebuild panel assets and binaries, then redeploy units.

## Host apply (after rebuild/install)

```bash
# Units + group safety
sudo cp deploy/codeplo-pty.socket /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl restart codeplo-pty.socket codeplod

# If the terminal still says permission denied:
sudo chown root:codeplo /run/codeplo-pty
sudo chmod 0750 /run/codeplo-pty
sudo -u codeplo test -r /run/codeplo-pty/pty.sock && echo OK

# CLI as service user
sudo -u codeplo codeplo extensions list
sudo -u codeplo codeplo extensions dead
```

## Fixed

### Crashes / null safety
- QueryBoundary: null data → empty state
- Backups + broad frontend `isEmpty` optional chaining
- `duration()` non-finite → "—"
- TotpStep missing enroll data
- Monitoring chart finite Y-scale

### Terminal
- Socket unit: `RuntimeDirectory=codeplo-pty`, group `codeplo`
- Installer: explicit group/user, post-start chown of runtime dir
- Better permission + post-restart ticket messages

### Sessions
- Sliding `expires_at` on activity
- Server clock from HTTP `Date` on every OK response

### Notifications
- Full email SMTP form fields
- Channel assignment on alert rules
- Amber warning when rule enabled with zero channels
- Failed delivery: ~60s retry instead of full cooldown burn

### Runtime / setup / extensions
- Docker Resolve: newest by `Created`
- Setup: removed non-working “create OS user”
- Extensions CLI: events on list; names on dead letters
- Non-nil empty slices in several list paths (JSON `[]` not `null`)

## Intentionally unchanged
- Extension **HTTP** surface (T53 CLI-only)
- In-memory terminal tickets (30s single-use by design)
- Caddy: installer auto-installs official static binary when missing (`CODEPLO_SKIP_CADDY_INSTALL=1` to skip)

### Installer
- Auto-installs Caddy static binary to `/usr/bin/caddy` when not present.


## Deploy types (app / static / compose) — 2026-11-09

- **App / static**: build from Git with Dockerfile **or auto-detect** (Node/Next/Vite, Python, Go, Rust, PHP, Ruby, static index.html) via Dockerfile.codegen.
- **Compose**: docker compose up -d --build for multi-service stacks.
- Feature flags deploy.static / deploy.compose enabled by default (migration 0009_deploy_types.sql).
- New resource dialog offers App, Static site, and Compose again with guidance.
