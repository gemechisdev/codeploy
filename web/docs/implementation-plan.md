# codeplo — Implementation Plan

**A lightweight, secure, self-hosted deployment platform for a single VPS**

| | |
|---|---|
| **Document type** | Implementation Plan (architecture, scope, build sequence) |
| **Project** | `codeplo` |
| **Audience** | Engineering team and AI engineering agents building the system |
| **Status** | Draft v1.1 — revised after independent review |
| **Reference analysis** | Coolify v4 source (Appendix B) |

### Revision history

| Version | Change |
|---|---|
| 1.0 | Initial plan |
| 1.1 | Incorporated the substantive findings of an independent review. **Host hardening** is now staged by risk tier with a daemon-independent dead-man switch, runtime-first firewall rules, and SSH preconditions (§3.10, P2). Added **workload isolation policy** (§3.11), **bootstrap access** design (§3.12), and **deployment failure semantics** (§3.8). Resolved inconsistencies: secrets are strictly write-only (no reveal), the audit log is append-only and hash-chained rather than "immutable", the host terminal depends on a defined operator identity. Replaced the 512 MB claim with **support tiers** and three measurement profiles. Simplified the module interface. Appendix B now cites source evidence. |

---

## Table of Contents

0. [Clarifications & Decisions](#0-clarifications--decisions)
1. [Executive Summary](#1-executive-summary)
2. [Product Scope & Prioritization](#2-product-scope--prioritization)
3. [Target Architecture](#3-target-architecture)
4. [Project Structure](#4-project-structure)
5. [UI/UX Foundations](#5-uiux-foundations)
6. [Phased Roadmap](#6-phased-roadmap)
7. [Detailed Build Plan (per phase)](#7-detailed-build-plan-per-phase)
8. [Data Bootstrap & Seed Strategy](#8-data-bootstrap--seed-strategy)
9. [Testing, Quality & Rollout](#9-testing-quality--rollout)
10. [Risks & Mitigations](#10-risks--mitigations)
- [Appendix A — Requirements Checklist](#appendix-a--requirements-checklist)
- [Appendix B — Coolify Codebase Analysis](#appendix-b--coolify-codebase-analysis)
- [Appendix C — Complete File Overview](#appendix-c--complete-file-overview)

> **Planning note.** This plan sequences work by **dependency and exit criteria**, not by calendar time. Delivery speed is determined by the team and tooling executing it; no duration estimates are included by request.

---

## 0. Clarifications & Decisions

### 0.1 Answered questions and how they shape the design

| # | Client statement / answer | Decision | Design impact |
|---|---|---|---|
| 1 | Coolify requires ≥ 2 vCPU / 4 GB RAM (4 vCPU / 8 GB recommended) and lags small VPSes | **Resource footprint is a first-class requirement** | Single Go binary, SQLite, no Redis/Postgres/queue workers; idle-RSS budget enforced in CI |
| 2 | Build a *new* version, modern and lightweight | **Greenfield rewrite**, not a fork | Reuse concepts (not code) from Coolify; Go-native Docker integration instead of SSH + shell strings |
| 3 | Remove rarely needed features or make them pluggable | **Module system + explicit triage** | `Module` interface, feature flags, 12 features explicitly out of scope (§2) |
| 4 | Priority 1: set up a VPS with firewall and security using Caddy or something free and secure | **Host-hardening module + managed Caddy edge** | Phase 2 delivers audit, firewall, SSH hardening, fail2ban, auto-HTTPS before any deployment feature |
| 5 | Manage projects: deploy, connect GitHub, use it as CI/CD (if lightweight) | **Webhook push-to-deploy + registry-pull deploy hook** | Two CI/CD paths: build on the VPS (light) or build in GitHub Actions and only pull on the VPS (lightest) |
| 6 | Terminal that uses the VPS directly | **In-process PTY**, not SSH loopback | Panel runs as a host service; xterm.js ↔ WebSocket ↔ PTY; container shells via Docker exec |
| 7 | Secure it similarly | **Security is a cross-cutting requirement** | Threat model (Phase 0), 2FA enforced, step-up auth for root shell, audit log, panel never exposed raw |
| 8 | Lightweight languages; Go for backend; frontend is my choice (React OK?) | **Go backend; React + TypeScript SPA embedded in the binary** | React's cost lands in the browser, not on the VPS; `go:embed` removes any separate web server |
| 9 | Analyze Coolify's features and codebase before planning | **Analysis completed** | Findings in Appendix B drive the triage in §2 |
| 10 | Deliver phase by phase, as a professional file, no time estimates | **This document** | Dependency-ordered phases with exit criteria; file overview in §4 and Appendix C |
| 11 | Host hardening must not become a way to lock yourself out (raised in review) | **Hardening is staged by risk tier and gated** | Audit → safe fixes → reversible firewall → guarded SSH; each stage has its own VM-lab gate (§3.10) |
| 12 | Review suggested deferring hardening and Git/CI/CD entirely | **Not adopted as stated** — both are stated priorities | Instead: staged delivery, a smaller first slice (release ladder §1.6), image-deploy validated before Git |

### 0.2 Working assumptions (change here if wrong)

| ID | Assumption | If it changes |
|---|---|---|
| AS-1 | First release targets **one VPS** (control plane and workloads on the same host) | Multi-server is designed for (agent mode, Phase 8) but not built in MVP |
| AS-2 | Supported hosts: **Debian 12+ and Ubuntu 22.04+**, amd64 first, arm64 shortly after | Other distros need new hardening drivers (check definitions are data, drivers are code) |
| AS-3 | **Docker Engine** is the container runtime | Podman support would need a runtime adapter behind `internal/runtime` |
| AS-4 | A domain name is **recommended but not required**; without one, the panel is reachable only via SSH tunnel or an access provider | First-run wizard offers both paths |
| AS-5 | Small team of users (≈ 1–10) in a **single tenant** | Multi-tenancy is out of scope (B10) |
| AS-6 | Product is open source; license chosen before first public release | Does not affect architecture |
| AS-7 | No telemetry is collected by default | Any opt-in telemetry would be a module |
| AS-8 | The operator has VPS-provider **console or rescue access** (acknowledged in the wizard before any firewall/SSH change) | Without it, hardening stays in audit/plan-only mode |
| AS-9 | Untrusted code is **not** built on the production host by default | CI-built images are the recommended path for public or third-party repos |

### 0.3 Remaining open questions

None are blocking. Defaults are chosen so building can start immediately.

| # | Question | Default if unanswered |
|---|---|---|
| OQ-1 | Final license (Apache-2.0 / AGPL / MIT)? | Apache-2.0 |
| OQ-2 | Should the first release include GitHub App (G5) or only deploy keys + webhooks? | Deploy keys + webhooks first; GitHub App in the "soon after" tier |
| OQ-3 | Is non-Docker app hosting (plain systemd services) wanted? | No — containers only |
| OQ-4 | Preferred notification channel to ship first? | Telegram + webhook |

---

## 1. Executive Summary

### 1.1 Product summary

`codeplo` is a single-binary control plane that turns a fresh VPS into a hardened deployment platform. It secures the host (firewall, SSH, brute-force protection, automatic updates), fronts everything with Caddy for automatic HTTPS, deploys containerized applications from Git or a registry with health-checked rolling updates and one-click rollback, wires into GitHub as a CI/CD trigger, and exposes a secure in-browser terminal — all within a resource budget that fits a 1 GB VPS. Optional capabilities (databases, backups, notifications, templates, previews, multi-server) are modules that can be switched on or extended without touching the core.

### 1.2 Conceptual architecture

```
                         Internet
                            │ 80 / 443
                    ┌───────▼────────┐
                    │     Caddy      │  auto-HTTPS, routing, headers
                    │ (managed proc) │◄──── admin API (unix socket) ────┐
                    └───────┬────────┘                                  │
          panel domain      │      app domains                          │
                    ┌───────▼───────────────────────────────────────────┴──┐
                    │                    codeplod (Go)                      │
                    │  ┌────────┐ ┌─────────┐ ┌──────────┐ ┌────────────┐ │
                    │  │  API   │ │  Auth   │ │ Deploy   │ │ Hardening  │ │
                    │  │ REST + │ │ sessions│ │ engine + │ │ audit/apply│ │
                    │  │ SSE/WS │ │ 2FA/RBAC│ │ job queue│ │ + revert   │ │
                    │  └────────┘ └─────────┘ └──────────┘ └────────────┘ │
                    │  ┌────────┐ ┌─────────┐ ┌──────────┐ ┌────────────┐ │
                    │  │Terminal│ │ Git /   │ │ Modules  │ │ Embedded   │ │
                    │  │  PTY   │ │ webhooks│ │ registry │ │ React SPA  │ │
                    │  └────────┘ └─────────┘ └──────────┘ └────────────┘ │
                    │               SQLite (WAL) + master key              │
                    └───────┬─────────────────────────┬────────────────────┘
                            │ Docker Engine API       │ nftables / sshd / apt
                    ┌───────▼────────┐        ┌───────▼────────┐
                    │ App containers │        │   Host OS      │
                    └────────────────┘        └────────────────┘
```

**Terminology:** `codeplo` is a single-binary **control plane**. The running platform is three cooperating processes — `codeplod`, Caddy, and Docker Engine — plus systemd. "Single binary" refers to the one artifact you install, not to one process.

**Key idea:** the database is the single source of truth. Caddy's configuration, firewall rules, and container state are all *derived* from it and reconciled — never hand-edited.

### 1.3 Recommended tech stack

| Layer | Choice | Justification |
|---|---|---|
| Language | **Go** | Static binary, low memory, excellent concurrency for log/terminal streaming, first-class Docker SDK |
| HTTP | `net/http` + `chi` | Minimal, stable, no framework overhead |
| Database | **SQLite (WAL)** via `modernc.org/sqlite` | Zero-ops, pure Go (no CGO, simple cross-compilation), ample for one host |
| Data access | `sqlc` + `goose` migrations | Type-safe queries generated from SQL; forward-only migrations |
| Containers | Docker Engine API (`docker/docker` client) | No shell strings, no injection surface from string building |
| Proxy | **Caddy** (separate process, admin API on unix socket) | Automatic HTTPS, small footprint, JSON config API |
| Realtime | SSE for logs/status; WebSocket for terminal (`coder/websocket`, `creack/pty`) | Simple, proxy-friendly |
| Jobs | In-process queue persisted in SQLite; `robfig/cron/v3` | No Redis, survives restarts |
| Crypto | argon2id, XChaCha20-Poly1305 (secrets), `pquerna/otp` | Modern primitives, standard libraries |
| API contract | OpenAPI 3.1 → `oapi-codegen` (Go) + generated TS client | One contract, no drift |
| Frontend | **React 18 + TypeScript + Vite**, TanStack Router/Query/Table, Tailwind CSS, shadcn/ui, xterm.js, CodeMirror 6 | Mature ecosystem; build output embedded into the binary, so it costs the VPS nothing at runtime |
| Release | GoReleaser, cosign signatures | Reproducible, verifiable artifacts |

### 1.4 Resource budget, support tiers and measurement

**Idle targets (control plane only — not a capacity claim):** `codeplod` ≤ 50 MB RSS, Caddy ≤ 40 MB RSS. These show the platform's own overhead is small; they do **not** say a host is big enough, because the OS, Docker, containers, filesystem cache, and especially image builds dominate real usage.

| Host size | Support level | Constraints |
|---|---|---|
| 512 MB | **Experimental** | Prebuilt images only (CI builds), a few tiny containers, swap recommended; no on-host builds |
| 1 vCPU / 1 GB | **Supported (limited)** | Prebuilt images / registry-pull; small workloads |
| **1 vCPU / 2 GB** | **Primary target** | Modest workloads; on-host Dockerfile builds allowed with enforced limits and warnings |
| 2 vCPU / 4 GB | **Comfortable** | Heavier builds and more concurrent resources |
| 4 vCPU / 8 GB | Headroom | Larger workloads and parallel operations |

Tiers are product positioning, not guarantees; application memory needs, disk size, and swap still matter. The UI shows RAM-aware warnings before an on-host build and recommends the registry-pull path on small hosts.

**Three measurement profiles (all run in CI/VM lab):**

| Profile | Scenario | Measured |
|---|---|---|
| **A — Idle** | Dashboard open, no deployments | RSS, CPU, file descriptors, wakeups |
| **B — Normal** | Several running apps, log streaming, HTTPS traffic, periodic deploys | Total host RAM, CPU contention, latency, disk growth |
| **C — Pressure** | Build failure, disk/memory pressure, daemon restart, concurrent deploy triggers | Existing apps stay available; recovery succeeds without manual cleanup |

### 1.5 Team shape

Work parallelizes along five independent tracks (see §6.3): **Core/API**, **Edge & Hardening**, **Deploy Engine**, **Frontend**, **QA & Security**. Each track has well-defined interfaces (OpenAPI contract, `Module` interface, `runtime` and `edge` ports), so tracks can proceed concurrently with a small, senior-led team or a coordinated set of AI agents.

### 1.6 Release ladder and MVP slice

Delivery is gated, not big-bang. Each rung must pass its exit criteria (§6.2) before the next is declared.

| Rung | Contents | Purpose |
|---|---|---|
| **R0 — Walking skeleton** | P0 + P1 + P2a (edge + audit + safe fixes) + P4 with **image deploys** | Prove the core: install → login → deploy an image on an HTTPS domain → roll back → measure memory |
| **R1 — Trial** | R0 + P2b (reversible firewall) + P3 (terminal & logs) + P5 (Git & CI/CD) | The full MVP below |
| **R2 — Beta** | P2c (guarded SSH apply), P6, parts of P7 | Operations and extensibility |
| **R3 — 1.0** | P8 selections, P9 | Release hardening |

**MVP (= R1)** is every 🟢 feature (50 items):

> A user installs `codeplo` on a fresh Debian/Ubuntu VPS, opens the panel over an SSH tunnel using a one-time token, enrolls 2FA, moves the panel to an HTTPS domain, runs the security audit and applies the **safe baseline and reversible firewall**, deploys an app from an image with a custom HTTPS domain and health-gated rollout, rolls it back, opens a browser terminal as an operator user, and sets up GitHub push-to-deploy or a registry-pull deploy hook. **Guarded SSH automation** (D3) follows in R2; until then the audit generates the exact SSH change plan for manual review.

Everything 🟡 follows in Phases 6–7; 🔵 is deferred to Phase 8 and later.

## 2. Product Scope & Prioritization

**Legend:** 🟢 Must have in MVP · 🟡 Should have soon after · 🔵 Nice to have / later · ⚪ Explicitly out of scope

| Domain | 🟢 | 🟡 | 🔵 | ⚪ | Total |
|---|---:|---:|---:|---:|---:|
| A. Installation & Platform | 5 | 2 | 1 | 0 | 8 |
| B. Authentication & Access Control | 6 | 1 | 2 | 1 | 10 |
| C. Edge & Reverse Proxy (Caddy) | 4 | 2 | 2 | 1 | 9 |
| D. Host Hardening | 7 | 4 | 1 | 1 | 13 |
| E. Terminal & Logs | 5 | 2 | 1 | 0 | 8 |
| F. Deployments | 11 | 4 | 2 | 1 | 18 |
| G. Git Integration & CI/CD | 4 | 4 | 2 | 1 | 11 |
| H. Data Services & Backups | 0 | 3 | 2 | 0 | 5 |
| I. Observability & Notifications | 1 | 3 | 2 | 1 | 7 |
| J. Extensibility & Platform API | 3 | 3 | 1 | 0 | 7 |
| K. Scheduled Tasks & Maintenance | 1 | 1 | 0 | 0 | 2 |
| L. Web UI | 3 | 2 | 1 | 0 | 6 |
| M. Multi-Server & Scale | 0 | 0 | 2 | 1 | 3 |
| N. Commercial / Cloud Features From Coolify | 0 | 0 | 0 | 5 | 5 |
| **Total** | **50** | **31** | **19** | **12** | **112** |

### A. Installation & Platform

| ID | Feature | Status | Notes |
|---|---|---|---|
| A1 | Single-binary installer with checksum verification | 🟢 | `curl | sh` fetches binary, verifies SHA-256 + signature |
| A2 | Hardened systemd unit | 🟢 | Sandboxing directives where compatible with PTY/Docker needs |
| A3 | Self-update with pre-migration DB snapshot | 🟡 | `codeplo update`, channel `stable`/`edge` |
| A4 | Recovery CLI (`codeplo`) | 🟢 | reset-password, disable-2fa, status, doctor, rollback |
| A5 | Debian 12+/Ubuntu 22.04+ support | 🟢 | Primary supported targets |
| A6 | arm64 builds (Raspberry Pi, ARM VPS) | 🟡 | Same codebase, cross-compiled |
| A7 | RHEL-family / Alpine host support | 🔵 | Hardening checks need per-distro drivers |
| A8 | Bootstrap access mode: SSH tunnel + one-time setup token | 🟢 | No public exposure before HTTPS is configured; loopback break-glass path always remains |

### B. Authentication & Access Control

| ID | Feature | Status | Notes |
|---|---|---|---|
| B1 | Local users with argon2id passwords | 🟢 |  |
| B2 | Server-side sessions + CSRF protection | 🟢 | HttpOnly, Secure, SameSite=Strict |
| B3 | TOTP 2FA with recovery codes | 🟢 | Enforced for owner in first-run wizard |
| B4 | Login rate limiting and lockout | 🟢 | Per-IP and per-account |
| B5 | Scoped API tokens | 🟢 | Hashed at rest, scopes from the permission registry |
| B6 | Append-only, hash-chained audit log | 🟢 | Tamper-evident (not tamper-proof); periodic checkpoints to journald / notification channel |
| B7 | Roles: owner / admin / deployer / viewer | 🟡 | Data-driven role → permission mapping |
| B8 | Passkeys (WebAuthn) | 🔵 |  |
| B9 | OIDC single sign-on | 🔵 | GitHub, Google, generic OIDC |
| B10 | Multi-team / organization model | ⚪ | Single-instance, single-tenant by design |

### C. Edge & Reverse Proxy (Caddy)

| ID | Feature | Status | Notes |
|---|---|---|---|
| C1 | Managed Caddy driven via admin API on a unix socket | 🟢 | Config derived from DB, reconciled on change and at boot |
| C2 | Automatic HTTPS (Let's Encrypt / ZeroSSL) | 🟢 |  |
| C3 | Per-resource domains, www/apex redirects, force-HTTPS | 🟢 | Hostname uniqueness enforced |
| C4 | Panel domain with security headers (HSTS, CSP, etc.) | 🟢 |  |
| C5 | Per-route basic auth and IP allowlist | 🟡 |  |
| C6 | Custom Caddy snippets (advanced) | 🟡 | Validated with `caddy validate` before apply |
| C7 | Wildcard certificates via DNS-01 | 🔵 | Provider plugins |
| C8 | Cloudflare Tunnel / Tailscale access mode | 🔵 | Pluggable access providers |
| C9 | Traefik / Nginx alternatives | ⚪ | Caddy only |

### D. Host Hardening

| ID | Feature | Status | Notes |
|---|---|---|---|
| D1 | Read-only security audit with reviewed change plan | 🟢 | Tier 0: reports and generates exact diffs (incl. SSH) without changing anything |
| D2 | Reversible default-deny firewall (dedicated nftables table) | 🟢 | Runtime-first, persisted only on confirm; never flushes existing rules |
| D3 | Guarded SSH hardening apply | 🟡 | Preconditions (proven key login, console-access acknowledgement) + daemon-independent dead-man revert; port changes are manual-only |
| D4 | Port-exposure policy: codeplo containers publish no ports | 🟢 | Caddy reaches containers over internal networks; audit flags any other published port |
| D5 | Brute-force protection (fail2ban or CrowdSec) | 🟢 | Admin IPs on the ignore list so the operator cannot be banned |
| D6 | Unattended security upgrades | 🟢 |  |
| D7 | Security score dashboard with one-click fixes | 🟢 | Pass / warn / fail per check |
| D8 | Kernel / sysctl baseline | 🟡 |  |
| D9 | Hardening checks defined as data (YAML) | 🟡 | New check = new file, no code deploy |
| D10 | Operator identity (select existing sudo user or create one) | 🟢 | Hard prerequisite for the host terminal |
| D11 | Rootless Docker mode | 🔵 |  |
| D12 | Malware / rootkit scanning | ⚪ | Out of scope; integrate externally if wanted |
| D13 | `DOCKER-USER` enforcement for third-party published ports | 🟡 | Optional hard block for containers not managed by codeplo |

### E. Terminal & Logs

| ID | Feature | Status | Notes |
|---|---|---|---|
| E1 | Host terminal in browser (direct PTY) | 🟢 | xterm.js over WebSocket |
| E2 | Container terminal (Docker exec attach) | 🟢 |  |
| E3 | Step-up re-auth (TOTP) for root shell | 🟢 | Valid for a short, configurable window |
| E4 | Terminal session metadata + optional recording | 🟡 | Start/stop/user/IP always; byte recording opt-in |
| E5 | Live container log streaming | 🟢 | SSE with backpressure |
| E6 | Deployment / build log streaming and storage | 🟢 |  |
| E7 | Log retention and size caps | 🟡 |  |
| E8 | Log drains to external collectors | 🔵 | Extension |

### F. Deployments

| ID | Feature | Status | Notes |
|---|---|---|---|
| F1 | Prebuilt image deploy (any registry) | 🟢 |  |
| F2 | Dockerfile build from Git source | 🟢 | BuildKit via Docker Engine |
| F3 | Docker Compose deploy | 🟡 |  |
| F4 | Static site hosting via Caddy | 🟡 | Build output served directly, no extra container |
| F5 | Health-check-gated rolling update | 🟢 | Route switches only after new container is healthy |
| F6 | One-click rollback to a retained image | 🟢 | Last N images retained by digest |
| F7 | Environment variables and encrypted secrets | 🟢 | Secrets write-only (replace/rotate, no reveal); build secrets via BuildKit secret mounts |
| F8 | Persistent volume management | 🟢 |  |
| F9 | CPU / memory limits per resource | 🟢 | Defaults protect small VPSes |
| F10 | Build queue (concurrency 1 default) + cancel | 🟢 | Prevents builds starving the host; RAM-aware warnings on small hosts |
| F11 | Private registry credentials | 🟡 |  |
| F12 | Railpack / Nixpacks builder plugins | 🔵 | Behind the `Builder` interface |
| F13 | Remote build server | 🔵 |  |
| F14 | Docker Swarm | ⚪ |  |
| F15 | Workload isolation policy enforcement | 🟢 | cap-drop ALL, no-new-privileges, no privileged/host-network/docker.sock, mount allowlist; applies to Compose too |
| F16 | Durable deployment state machine with crash/reboot recovery | 🟢 | Interrupted deployments cleaned up; active route only changes in one transaction |
| F17 | Post-switch observation window with automatic rollback | 🟡 | Previous version retained until window ends |
| F18 | Pre-flight disk and memory checks | 🟢 | Refuse builds that would exhaust the host |

### G. Git Integration & CI/CD

| ID | Feature | Status | Notes |
|---|---|---|---|
| G1 | Public repository clone | 🟢 |  |
| G2 | SSH deploy keys for private repos | 🟢 | Per-resource generated keypair |
| G3 | Push-to-deploy webhooks (HMAC-verified) | 🟢 | Replay protection via delivery ID |
| G4 | Registry-pull deploy hook for GitHub Actions | 🟢 | CI builds the image; VPS only pulls and runs |
| G5 | GitHub App integration | 🟡 | Repo picker, installation tokens |
| G6 | Branch and path filters, `[skip deploy]` convention | 🟡 |  |
| G7 | Commit status / check-run feedback to GitHub | 🟡 |  |
| G8 | Pull-request preview deployments | 🟡 | Subdomain per PR, cleanup on close |
| G9 | GitLab / Gitea / Bitbucket providers | 🔵 | Provider interface |
| G10 | GitHub self-hosted runners | ⚪ |  |
| G11 | Image signature verification (cosign) before deploy | 🔵 | Policy per resource |

### H. Data Services & Backups

| ID | Feature | Status | Notes |
|---|---|---|---|
| H1 | One-click PostgreSQL / MySQL / Redis | 🟡 |  |
| H2 | Additional engines (MariaDB, MongoDB, others) | 🔵 | Via templates |
| H3 | Scheduled backups to local disk / S3-compatible | 🟡 |  |
| H4 | Guided restore workflow | 🟡 |  |
| H5 | Automated backup verification | 🔵 |  |

### I. Observability & Notifications

| ID | Feature | Status | Notes |
|---|---|---|---|
| I1 | Host and container status dashboard | 🟢 | CPU, RAM, disk, container states |
| I2 | Metrics history (SQLite ring buffer) | 🟡 |  |
| I3 | Notification channels: Telegram, Discord, email, webhook | 🟡 | Channel drivers |
| I4 | Alert rules: disk, container down, deploy failed, cert expiry | 🟡 | Rules stored as data |
| I5 | Uptime checks | 🔵 |  |
| I6 | Prometheus metrics endpoint | 🔵 |  |
| I7 | Error-tracking SaaS integrations | ⚪ |  |

### J. Extensibility & Platform API

| ID | Feature | Status | Notes |
|---|---|---|---|
| J1 | Minimal internal module interface | 🟢 | Routes, jobs, migrations, permissions only; no dynamic UI manifest |
| J2 | Feature flags | 🟢 | Stored in DB, overridable via config |
| J3 | OpenAPI-first API + generated TypeScript client | 🟢 | Single source of truth for contract |
| J4 | Outbound event webhooks | 🟡 |  |
| J5 | Out-of-process extension API | 🟡 | Extensions run as containers |
| J6 | Template catalog importer (compose templates) | 🟡 | Format compatible with Coolify-style templates |
| J7 | MCP server module for AI agents | 🔵 | Same tokens and permissions as the API |

### K. Scheduled Tasks & Maintenance

| ID | Feature | Status | Notes |
|---|---|---|---|
| K1 | Docker cleanup (images, build cache, stopped containers) | 🟢 | Protects small disks |
| K2 | Cron jobs executed inside resources | 🟡 | Execution history |

### L. Web UI

| ID | Feature | Status | Notes |
|---|---|---|---|
| L1 | Embedded React dashboard (served by the binary) | 🟢 | `go:embed`, no separate web server |
| L2 | First-run setup wizard | 🟢 | Admin, 2FA, domain, security audit |
| L3 | Light / dark theme | 🟢 |  |
| L4 | Mobile-responsive layouts | 🟡 |  |
| L5 | Command palette (Ctrl+K) | 🟡 |  |
| L6 | Internationalization | 🔵 |  |

### M. Multi-Server & Scale

| ID | Feature | Status | Notes |
|---|---|---|---|
| M1 | Agent mode (same binary, outbound connection) | 🔵 | Control plane manages remote nodes |
| M2 | Multi-server resource placement | 🔵 |  |
| M3 | Kubernetes | ⚪ |  |

### N. Commercial / Cloud Features From Coolify

| ID | Feature | Status | Notes |
|---|---|---|---|
| N1 | Billing and subscriptions | ⚪ |  |
| N2 | Cloud-provider server provisioning | ⚪ | Hetzner, DigitalOcean, etc. |
| N3 | Managed DNS provider integrations | ⚪ |  |
| N4 | Built-in traffic analytics | ⚪ |  |
| N5 | Sysbox runtime support | ⚪ |  |

---

## 3. Target Architecture

### 3.1 Design principles

1. **Simple over clever.** One binary, one database file, one proxy process. Add moving parts only when a feature demands it.
2. **Database is the source of truth.** Caddy config, firewall rules, and deployed containers are *derived state*, reconciled at boot and after every change. Drift is detected and repaired, never trusted.
3. **Data-driven configuration.** Roles → permissions, hardening checks, notification rules, templates, and feature flags are rows or files, not `if` statements. New check, role, or template = new data, no code deploy.
4. **Additive change.** Features are modules registered against stable interfaces. Adding one must not require editing core packages.
5. **No shell string building for container operations.** Use the Docker Engine API. Where host commands are unavoidable (`nft`, `sshd -t`, `apt`), use `exec.Command` with fixed argv and validated inputs — never `sh -c` with interpolation.
6. **Safe by default, reversible by design.** Every host-changing action supports dry-run, records a before-state, and (for lockout-capable changes) auto-reverts unless confirmed.
7. **Secure the panel as if it were root** — because it effectively is (§3.9).
8. **Small footprint is a tested property**, not an aspiration: CI fails when idle memory exceeds budget.
9. **Failure is a first-class state.** Crashes, reboots, cancellations, and full disks are modeled explicitly (§3.8) and tested by fault injection.
10. **Abstract only where a second implementation or a test fake exists.** `runtime.Runtime` and `edge.Proxy` qualify; a general plugin framework does not (deferred to Phase 7).

### 3.2 Core shared concepts

| Concept | Meaning | Where it lives |
|---|---|---|
| **Resource** | Anything deployable: `app`, `compose`, `static`, `database`, `service` | `resources` table; behavior by `type` handler |
| **Project / Environment** | Grouping (`production`, `staging`) for resources and shared variables | `projects`, `environments` |
| **Module** | Self-contained feature (hardening, git, backups…) with routes, migrations, jobs, permissions | `internal/modules/*` |
| **Permission registry** | Canonical list of permission strings (`deploy:create`, `terminal:root`) used by roles, tokens, and route guards | `internal/authz/permissions.go` + seeded |
| **Reconciler** | Idempotent function: *desired state (DB) → actual state (Caddy / nftables / Docker)* | `internal/edge`, `internal/hardening`, `internal/deploy` |
| **Check** | A hardening rule: `detect()`, `plan()`, `apply()`, `revert()`, defined by YAML + driver | `hardening/checks/*.yaml` |
| **Job** | Durable unit of background work (deploy, backup, cleanup) | `jobs` table + worker pool |
| **Event** | Immutable fact emitted on the in-process bus (`deployment.succeeded`) → notifications, webhooks, extensions | `internal/events` |

### 3.3 Data models (key entities)

SQLite types shown; all IDs are ULIDs (`TEXT`), timestamps are UTC ISO-8601 (`TEXT`).

```sql
-- Identity & access
users(id, email UNIQUE, password_hash, role_id, totp_secret_enc, totp_enabled,
      recovery_codes_hash, disabled, created_at, last_login_at)
roles(id, key UNIQUE, name, builtin)                       -- owner|admin|deployer|viewer
role_permissions(role_id, permission)                      -- data-driven RBAC
sessions(id, user_id, token_hash, ip, user_agent, step_up_until, expires_at, created_at)
api_tokens(id, user_id, name, token_hash, scopes_json, last_used_at, expires_at, revoked_at)
audit_events(id, seq UNIQUE, at, actor_type, actor_id, ip, action, target_type, target_id, meta_json,
             prev_hash, hash)                              -- hash = SHA-256(prev_hash || canonical row)

-- Platform
settings(key PRIMARY KEY, value_json, updated_at)          -- panel domain, retention, etc.
feature_flags(key PRIMARY KEY, enabled, rollout_json)
modules(key PRIMARY KEY, enabled, version, config_json)
jobs(id, kind, payload_json, status, attempts, run_at, locked_by, last_error, created_at)
secrets_meta(id, key_id, created_at)                       -- master-key rotation support

-- Edge
domains(id, resource_id, hostname UNIQUE, force_https, redirect_www, auth_json, created_at)
edge_snippets(id, resource_id, caddy_json, validated_at)

-- Hardening
hardening_runs(id, mode, started_at, finished_at, actor_id, score)
hardening_results(run_id, check_key, status, detail_json)  -- pass|warn|fail|skipped
hardening_changes(id, run_id, check_key, tier, before_json, after_json, snapshot_path, revert_unit,
                  ack_json, confirm_deadline, confirmed_at, reverted_at)

-- Workloads
projects(id, name, slug UNIQUE)
environments(id, project_id, name)
resources(id, environment_id, type, name, slug, config_json, desired_state,
          active_deployment_id, created_at)       -- the ONLY pointer the edge routes from
resource_env_vars(id, resource_id, key, value_enc, scope, is_secret)   -- scope: build|runtime|both
volumes(id, resource_id, name, mount_path, kind)
deployments(id, resource_id, status, step, trigger, commit_sha, image_ref, image_digest,
            heartbeat_at, observe_until, started_at, finished_at, rollback_of, error)
deployment_logs(deployment_id, seq, at, stream, line)

-- Git & CI/CD
git_connections(id, kind, name, config_enc)                -- deploy key / github_app
resource_sources(resource_id, kind, repo_url, branch, build_path, dockerfile, path_filters_json, deploy_key_id)
webhook_deliveries(id, provider, delivery_id UNIQUE, received_at, result)
deploy_hooks(id, resource_id, token_hash, last_used_at)

-- Operations
notification_channels(id, kind, name, config_enc, enabled)
alert_rules(id, key, condition_json, channel_ids_json, cooldown_s, enabled)
backup_schedules(id, resource_id, cron, destination_id, retention_json)
backup_runs(id, schedule_id, status, size_bytes, location, started_at, finished_at)
metrics_samples(at, scope, ref_id, cpu, mem, disk, net_rx, net_tx)      -- ring buffer
terminal_sessions(id, user_id, target, started_at, ended_at, recorded)
```

**`resources.config_json` shape (app):**
```json
{
  "image": {"mode": "build", "dockerfile": "Dockerfile", "context": "."},
  "port": 3000,
  "healthcheck": {"path": "/healthz", "interval_s": 5, "retries": 12, "start_period_s": 5},
  "limits": {"memory_mb": 256, "cpus": 0.5},
  "restart": "unless-stopped",
  "retain_images": 5
}
```

### 3.4 API surface overview

REST + JSON under `/api/v1`, defined in `api/openapi.yaml`. Streams use SSE (`text/event-stream`) and the terminal uses WebSocket.

| Domain | Routes (representative) |
|---|---|
| **Auth** | `POST /auth/login` · `POST /auth/logout` · `POST /auth/totp/verify` · `POST /auth/step-up` · `GET /auth/me` |
| **Setup** | `GET /setup/state` · `POST /setup/admin` · `POST /setup/verify-domain` · `POST /setup/panel-domain` |
| **Users & tokens** | `GET/POST /users` · `PATCH /users/{id}` · `GET/POST/DELETE /tokens` · `GET /roles` · `GET /permissions` |
| **Audit** | `GET /audit?actor=&action=&from=&to=` |
| **Hardening** | `GET /hardening/checks` · `POST /hardening/audit` · `POST /hardening/plan` · `POST /hardening/preflight` · `POST /hardening/apply` · `POST /hardening/confirm/{changeId}` · `POST /hardening/revert/{changeId}` · `GET /hardening/runs` |
| **Edge** | `GET /edge/status` · `POST /edge/reconcile` · `GET/POST/DELETE /resources/{id}/domains` · `GET /edge/certificates` |
| **Projects** | `GET/POST /projects` · `GET/POST /projects/{id}/environments` |
| **Resources** | `GET/POST /resources` · `GET/PATCH/DELETE /resources/{id}` · `POST /resources/{id}/{start,stop,restart}` |
| **Env & volumes** | `GET/PUT /resources/{id}/env` · `GET/POST /resources/{id}/volumes` |
| **Deployments** | `POST /resources/{id}/deployments` · `GET /resources/{id}/deployments` · `GET /deployments/{id}` · `GET /deployments/{id}/logs` (SSE) · `POST /deployments/{id}/cancel` · `POST /deployments/{id}/rollback` |
| **Logs** | `GET /resources/{id}/logs` (SSE) |
| **Terminal** | `GET /terminal/host` (WS) · `GET /terminal/resources/{id}` (WS) · `GET /terminal/sessions` |
| **Git** | `GET/POST /git/connections` · `POST /resources/{id}/deploy-key` · `POST /webhooks/github` · `POST /hooks/deploy/{token}` |
| **System** | `GET /system/status` · `GET /system/metrics` · `POST /system/cleanup` · `GET /system/version` |
| **Modules & flags** | `GET /modules` · `PATCH /modules/{key}` · `GET/PATCH /feature-flags` |
| **Notifications** | `GET/POST /notifications/channels` · `GET/POST /alerts/rules` |
| **Backups** | `GET/POST /resources/{id}/backups` · `POST /backups/{id}/restore` |
| **Templates** | `GET /templates` · `POST /templates/{key}/deploy` |
| **Events (SSE)** | `GET /events` — live resource, deployment, and hardening state for the UI |

### 3.5 Authentication & authorization model

| Aspect | Design |
|---|---|
| Passwords | argon2id (tuned for low memory: m=32 MiB, t=3, p=1), minimum length enforced, breached-password check optional |
| Sessions | Opaque 256-bit token, stored hashed (SHA-256) server-side; cookie `__Host-codeplo` (`HttpOnly; Secure; SameSite=Strict`) over HTTPS; a clearly marked bootstrap session variant applies only on the loopback listener (§3.12); idle + absolute expiry |
| CSRF | SameSite=Strict + per-session CSRF header for state-changing requests |
| 2FA | TOTP (RFC 6238) + single-use recovery codes; mandatory for `owner` |
| Step-up | Sensitive actions (`terminal:root`, hardening apply, secret replacement/rotation, restore, user admin) require a TOTP re-check within `step_up_until` (default 5 min) |
| API tokens | `cpl_` prefix, shown once, hashed at rest, scoped, optionally expiring, revocable, usage-stamped |
| Authorization | Every route declares a required permission string; middleware checks session role or token scopes. A generated **authorization matrix test** fails if any route lacks a guard |
| Roles (seeded, data-driven) | `owner` (all) · `admin` (all except owner management) · `deployer` (deploy, env, logs, restart) · `viewer` (read-only, no secrets, no terminal) |
| Rate limiting | Token bucket per IP and per account on `/auth/*`, `/hooks/*`, `/webhooks/*` |
| Panel exposure | Listens on a unix socket / `127.0.0.1` only; reachable externally solely through Caddy (HTTPS) or an SSH tunnel (bootstrap and break-glass: §3.12) |

### 3.6 Multi-tenancy / role model

`codeplo` is **single-tenant** (AS-5). Projects and environments are organizational, not isolation boundaries. Role-per-project scoping is a possible later extension; the `role_permissions` table already supports adding scoped permission strings (`project:{id}:deploy`) without schema change.

### 3.7 Edge (Caddy) approach

- Caddy runs as a managed child process or system service with `admin unix//run/codeplo/caddy-admin.sock`; the admin endpoint is never TCP-exposed.
- `internal/edge` renders a complete Caddy JSON config from `domains` + `resources` + panel settings and applies it with `POST /load` (atomic). Individual routes use `@id` tags for targeted `PATCH`/`DELETE`.
- Upstreams are container IPs/ports on the per-environment Docker network; the old upstream remains until the new container's health check passes (zero-downtime switch).
- Static sites are served by Caddy `file_server` directly from the build output directory.
- Reconciliation runs at startup, on every domain/resource change, and on a periodic drift check.

### 3.8 Deployment engine approach

```
trigger (manual | webhook | deploy hook | rollback)
   └► enqueue job ► [build queue, concurrency=1]
        0. pre-flight      (free disk ≥ max(2 GiB, 10%), RAM-aware build guard, policy validation)
        1. resolve source  (clone via deploy key | pull image from registry)
        2. build           (Dockerfile via BuildKit | skip if prebuilt image)
        3. start new container  (policy-enforced spec, labels, limits, env, volumes)
        4. verify healthy  (HTTP/CMD check, retries, start period)
        5. switch          (DB transaction sets resources.active_deployment_id → edge reconcile)
        6. observe         (window, default 60 s: unhealthy → automatic rollback)
        7. finalize        (stop old container, retain image by digest, emit events, commit status)
   failure at 0–4 ► old container untouched, deployment marked failed
```

**State machine:** `queued → preparing → building → starting → verifying → switching → observing → succeeded`, with terminal side-states `failed`, `cancelled`, `interrupted`, `rolled_back`. Each running deployment writes `step` and a `heartbeat_at` timestamp.

**Failure semantics (all covered by fault-injection tests):**

| Scenario | Defined behavior |
|---|---|
| **Daemon crashes mid-deploy** | On boot, non-terminal deployments are marked `interrupted`. Containers labeled `codeplo.deployment=<id>` that are not the resource's `active_deployment_id` are removed. Traffic never moved, because routing derives from `active_deployment_id` only |
| **VPS reboots after new container created, before switch** | Same recovery. Old version restarts via its restart policy; the orphaned new container is cleaned up |
| **Cancel after resources were created** | State `cancelling`; every step checks its context; build containers are killed through the runtime; cleanup is idempotent and label-driven |
| **Healthy at start, unhealthy shortly after switch** | The observation window keeps the previous container available. Consecutive failed checks trigger automatic rollback (`rolled_back`) and a notification. A "stop-old-first" mode exists for very small hosts, trading this safety for memory |
| **Database migration incompatible with previous version** | Application rollback is **not** database rollback. An optional *release command* (one-off container before the switch) can run migrations; failure aborts the deploy. Documentation recommends expand/contract migrations. Pre-deploy backups arrive with the backups module (P6) |
| **Disk fills during build or logging** | Pre-flight refuses to start under the threshold; the build step monitors free space; log writers are size-capped; `ENOSPC` marks the deployment `failed` with an actionable error and raises an alert |
| **Memory pressure** | Build containers get an explicit memory limit (≤ 50% of available RAM); OOM kill → `failed`; running apps keep their own limits |
| **Hung steps** | Per-step timeouts; timeout → `failed` plus cleanup |

**Idempotency:** container and artifact names embed the deployment ID (`cpl-<resource>-<deployment>`); every handler can be re-run safely after a restart.

### 3.9 Security model — what is and isn't enforceable

Being honest about guarantees:

| Topic | Reality | Mitigation in `codeplo` |
|---|---|---|
| **The panel is root-equivalent** | Anyone who controls the control plane controls Docker, hence the host. Splitting into a second privileged daemon would not remove this, since the Docker socket is already root-equivalent — it would only add a process | One process by design; never exposed raw; 2FA enforced; step-up for sensitive actions; narrow `platform` layer for host changes (fixed argv, validated inputs, no `sh -c`); the Docker socket is never exposed to the browser or mounted into workloads; documented in the threat model |
| **Firewall protects only what it covers** | Docker-published ports are DNAT'd through the FORWARD path, so host-INPUT rules do not protect them | `codeplo` containers publish **no** ports (§3.11); audit flags any others; optional `DOCKER-USER` enforcement (D13) |
| **Host hardening can lock you out** | A wrong firewall or SSH change can sever the only access path | Staged risk tiers and a daemon-independent dead-man switch (§3.10); residual risk acknowledged below |
| **Secrets at rest** | Encrypted with a master key on the same host; protects DB leaks and backups, **not** a root compromise | Key file `0600` root-owned, key-ID rotation, optional external key source later |
| **Secrets in workloads** | Runtime env vars are visible to anyone with Docker access | Secrets are **write-only**: replace or rotate, never read back; there is no reveal endpoint or permission. File-mount delivery option |
| **Audit log integrity** | A SQLite table can be altered by a privileged process; "immutable" would be a false claim | Append-only application semantics + hash chain (`prev_hash`, `hash`) so edits and deletions are detectable; hourly checkpoints written to journald and, when configured, a notification channel; `codeplo doctor --verify-audit`. A full-root attacker can still rewrite local state — external checkpoints are what raise the bar |
| **Webhooks** | Spoofable without verification | HMAC-SHA256 with constant-time compare, delivery-ID replay protection, rate limits |
| **Build isolation** | Building untrusted code on the host is risky; containers are not a perfect boundary | Build policy (§3.11); CI-build + registry-pull recommended for untrusted repos |
| **Terminal** | Highest-risk feature | Origin checks, single-use tickets, step-up for root, root disabled by default, idle timeout, session metadata always logged |
| **Updates** | Supply-chain risk | Checksums + signatures verified by installer and self-update |
| **Payments / content protection** | Not applicable to this product | — |

### 3.10 Host hardening safety model

Hardening is the highest-risk subsystem because it can remove the operator's only way in. It is therefore delivered in **risk tiers**, each with its own gate.

| Tier | Examples | Can it cut access? | Delivery |
|---|---|---|---|
| **0 — Audit** | Detect state, score, generate exact diffs and plans (including SSH) | No — read-only | P2a |
| **1 — Safe fixes** | Unattended upgrades, sysctl baseline, fail2ban/CrowdSec (with ignore list), operator identity | No | P2a |
| **2 — Reversible firewall** | Default-deny inbound via dedicated nftables table | Yes (inbound) | P2b |
| **3 — Guarded SSH** | Key-only auth, no root password login | Yes (SSH) | P2c (R2) |

**Safety mechanisms for Tiers 2–3:**

1. **Daemon-independent dead-man switch.** Before applying, `codeplo` writes a snapshot of the previous state to `/var/lib/codeplo/revert/<id>/` and creates a **systemd transient timer** (`systemd-run --on-active=… codeplo revert <id>`). The revert is a small CLI subcommand that needs neither the daemon, the database, nor the network. A daemon crash cannot defeat it.
2. **Runtime-first firewall.** Rules are loaded into the running kernel only. They are written to the persistent ruleset (`/etc/nftables.d/codeplo.nft`) and enabled at boot **only after confirmation** — so a reboot also returns to the previous state.
3. **Confirmation proves connectivity.** *Confirm* is a new authenticated request from the browser after the change took effect. If the operator is locked out, no confirmation arrives and the timer reverts.
4. **Own namespace.** Rules live in `table inet codeplo`; the existing ruleset is never flushed. Docker's rules and other tools are untouched; revert = delete the table. IPv4 and IPv6 are both covered.
5. **Preconditions (blocking).**
   - *Firewall:* every port `sshd` is listening on is allowed; 80/443 allowed when the edge is enabled; established/related and loopback allowed.
   - *SSH:* `sshd -t` passes; the target account has valid `authorized_keys` with sane permissions; **key-based login is proven** by a recent `Accepted publickey` entry for that account in the auth log (otherwise apply is blocked with instructions); the provider-console acknowledgement (AS-8) is recorded in the audit log.
   - **Port changes are never automated.**
6. **Correct sshd configuration mechanics.** sshd uses the **first** value it reads, and cloud images ship `50-cloud-init.conf` with `PasswordAuthentication yes`. The drop-in is therefore named **`00-codeplo.conf`**, the main config must contain an `Include` for `sshd_config.d` (otherwise apply refuses and never edits the main file), the **effective** configuration is checked with `sshd -T` against the expected values *before* reload, and the change is applied with **reload**, not restart, so existing sessions survive.
7. **Brute-force tooling cannot ban the operator.** fail2ban/CrowdSec are installed with loopback and the current admin source IPs on the ignore list.
8. **Fault-injection tests (VM lab, release gate).** Kill the daemon after apply; reboot; block the confirm request; `sshd -t` failure; cloud-init drop-in ordering; IPv6-only path; Docker with an externally published port present; fail2ban triggered by the operator's own typos.

**Residual risk (stated plainly).** The dead-man switch cannot help if systemd itself is broken, or if a *provider-level* firewall is misconfigured outside the host. That is why provider console/rescue access is a documented prerequisite, why `codeplo doctor --restore-network` exists, and why Tier 3 apply stays behind a feature flag (plan-only by default) until its VM matrix passes.

### 3.11 Workload isolation policy

Container limits are not a complete security boundary, and builds execute arbitrary code. The policy below is **enforced in code** by `internal/policy` (validated before any container is created, including every Compose service) and **verified by tests that attempt each violation**.

| Control | Default | Override |
|---|---|---|
| Linux capabilities | `cap_drop: ALL`; per-resource allowlist of additions (e.g. `NET_BIND_SERVICE`) | Owner, audited |
| `no-new-privileges` | Enabled | None |
| Seccomp / AppArmor | Docker default seccomp profile; `docker-default` AppArmor where available; `unconfined` rejected | None |
| User | Image's `USER`; running as root triggers a warning; optional `user:` setting | Resource config |
| `privileged`, `pid: host`, `ipc: host`, `network_mode: host`, `devices` | **Rejected** | None in MVP |
| Docker socket mount | **Rejected** | Resource marked `trusted` by owner, audited, loud warning |
| Host bind mounts | Only under `/var/lib/codeplo/volumes/<resource>/`; `/`, `/etc`, `/proc`, `/sys`, `/dev`, `/var/run`, Docker data dir always rejected | Owner, audited, outside forbidden paths |
| Published ports | **None.** Caddy reaches containers over internal networks | Explicit opt-in, audit-flagged |
| Resources | Memory, CPU, and `pids-limit` defaults applied | Resource config |
| Networks | Per-environment bridge networks | — |
| Compose files | Parsed and checked against this same table; unsupported or dangerous keys rejected | — |

**Build and secret handling**
- Build contexts are confined to the clone directory (symlinks resolved and checked); Git submodules disabled by default; `.dockerignore` honored.
- Secret build variables are passed as BuildKit **secret mounts**, not `ARG`s, so they do not land in image layers or `docker history`.
- Log redaction replaces exact secret values (minimum length enforced) in build and runtime logs.
- Registry credentials live in an ephemeral `DOCKER_CONFIG` directory (`0700`) removed after use; Git tokens go through a credential helper, never URLs or command-line arguments.
- No crash-report telemetry; panic handlers scrub request data.

### 3.12 Bootstrap access and panel exposure

Before a domain and certificate exist, the panel must still be reachable safely.

1. **First run.** `codeplod` listens on `127.0.0.1:<port>` and the unix socket only. The installer prints an SSH tunnel command and a URL containing a **one-time setup token** (256-bit, expires on first admin creation or after 1 hour; reissue with `codeplo setup-token`).
2. **Bootstrap session.** Over the loopback listener, session cookies use the plain `codeplo_session` name (Safari does not store `Secure` cookies for `http://localhost`) and are flagged `bootstrap`. Over HTTPS via Caddy the stricter `__Host-` cookie is used. Trust for `X-Forwarded-Proto` is limited to the Caddy upstream connection.
3. **Transition to HTTPS.** The wizard verifies the domain resolves to this host (`POST /setup/verify-domain`), adds the Caddy route, waits for certificate issuance, and shows the new URL. **Nothing is switched until issuance succeeds**; a failure leaves the tunnel path intact.
4. **Break-glass.** The loopback listener can never be disabled, so an SSH tunnel always works even if DNS or certificates break.
5. Caddy proxies to the panel over the unix socket/loopback, so no panel port is ever bound to a public interface.

---

## 4. Project Structure

### 4.1 Repository strategy

A **single monorepo** (`codeplo/`) — one Go module, one web app, one OpenAPI contract. There is no separate admin panel and client app: the dashboard *is* the admin surface, role-gated. Shared contracts flow one way: `api/openapi.yaml` → generated Go server stubs and generated TypeScript client.

### 4.2 Directory layout

```
codeplo/
├── api/
│   └── openapi.yaml                    # API contract (single source of truth)
├── cmd/
│   ├── codeplod/main.go                # daemon entrypoint (server + workers)
│   └── codeplo/main.go                 # CLI: doctor, setup-token, revert, reset-password, disable-2fa, update, rollback
├── internal/
│   ├── app/                            # wiring, config load, module registration, lifecycle
│   ├── config/                         # config.toml + env parsing, validation
│   ├── store/                          # SQLite open/pragmas, tx helpers
│   │   ├── migrations/                 # goose *.sql (forward-only)
│   │   ├── queries/                    # sqlc *.sql
│   │   └── gen/                        # sqlc output (generated)
│   ├── api/                            # chi router, middleware, handlers (generated stubs impl)
│   │   ├── middleware/                 # auth, csrf, ratelimit, audit, recover, requestid
│   │   └── handlers/                   # per-domain handler files
│   ├── authn/                          # passwords, sessions, totp, recovery codes, step-up
│   ├── authz/                          # permission registry, role loader, route guards
│   ├── audit/                          # append-only writer, hash chain, checkpoints, verify, query
│   ├── policy/                         # workload isolation policy (container + compose validation)
│   ├── crypto/                         # secrets (XChaCha20), master key, token hashing
│   ├── jobs/                           # durable queue, worker pool, cron scheduler
│   ├── events/                         # in-process bus, SSE fan-out
│   ├── modules/                        # Module interface + registry
│   │   ├── hardening/                  # checks engine, drivers, apply/revert, score
│   │   │   ├── drivers/                # nftables, ufw, sshd, fail2ban, crowdsec, apt, sysctl
│   │   │   └── checks/                 # *.yaml check definitions (embedded)
│   │   ├── edge/                       # Caddy supervisor, config renderer, reconciler
│   │   ├── terminal/                   # PTY host shell, docker exec attach, WS tickets
│   │   ├── deploy/                     # engine, builders, rollout, retention
│   │   │   └── builders/               # dockerfile, image, compose, static (+ plugins later)
│   │   ├── git/                        # clone, deploy keys, webhooks, providers
│   │   │   └── providers/              # github (v1), gitlab/gitea (later)
│   │   ├── runtime/                    # Docker Engine adapter (port + implementation)
│   │   ├── monitor/                    # host/container sampling, alert evaluation
│   │   ├── notify/                     # channel drivers: telegram, discord, email, webhook
│   │   ├── databases/                  # one-click DB definitions + backup/restore
│   │   ├── templates/                  # template parser/importer
│   │   ├── extensions/                 # out-of-process extension API
│   │   └── mcp/                        # MCP server module
│   ├── webui/                          # go:embed of web/dist + SPA fallback handler
│   └── platform/                       # OS detection, exec helpers, systemd, apt wrappers
├── web/
│   ├── index.html
│   ├── vite.config.ts · tailwind.config.ts · tsconfig.json · package.json
│   └── src/
│       ├── main.tsx · router.tsx
│       ├── api/                        # generated client + query hooks
│       ├── components/
│       │   ├── ui/                     # shadcn/ui primitives
│       │   └── app/                    # StatusBadge, LogViewer, TerminalPane, ScoreRing, DiffView
│       ├── features/
│       │   ├── setup/                  # first-run wizard
│       │   ├── auth/                   # login, 2FA, step-up dialog
│       │   ├── dashboard/
│       │   ├── projects/ · resources/ · deployments/
│       │   ├── security/               # audit, score, apply, confirm-revert
│       │   ├── terminal/ · logs/
│       │   ├── domains/ · git/ · env/
│       │   ├── monitoring/ · notifications/ · backups/
│       │   └── settings/               # users, tokens, modules, flags, audit log
│       ├── lib/                        # sse, ws, formatters, permission helpers
│       └── styles/
├── hardening/                          # (also embedded) reference docs per check
├── templates/                          # bundled compose templates (data)
├── deploy/
│   ├── install.sh                      # verified installer
│   ├── codeplod.service                # systemd unit
│   ├── caddy/                          # base Caddy bootstrap config
│   └── github-actions/codeplo-deploy.yml   # reusable workflow for registry-pull deploys
├── scripts/                            # gen (openapi/sqlc), lint, vm-e2e helpers
├── test/
│   ├── integration/                    # real Docker / SQLite tests
│   ├── e2e/                            # Playwright specs
│   ├── vm/                             # disposable-VM hardening/install tests (Lima/QEMU)
│   └── authz/                          # authorization matrix generator + tests
├── docs/                               # architecture, ADRs, threat model, runbooks
├── .github/workflows/                  # ci.yml, release.yml, memory-budget.yml
├── .goreleaser.yaml · Makefile · go.mod · sqlc.yaml · LICENSE
└── README.md
```

### 4.3 Module contract

```go
// internal/modules/module.go
type Module interface {
    Key() string                              // "hardening"
    Migrations() fs.FS                        // module-owned SQL migrations
    Permissions() []authz.Permission          // registered into the permission registry
    Routes(r chi.Router, d Deps)              // mounted under /api/v1
    Jobs() []jobs.Handler                     // background job kinds
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
}
```

Modules are enabled in the `modules` table / config. A disabled module registers no routes and no jobs; the SPA hides its navigation using the enabled-module list from `GET /modules`. The interface is deliberately minimal: dynamic UI manifests, event subscriptions, and a general plugin framework are deferred to Phase 7, when a real extension exists to justify them.

### 4.4 Shared libraries strategy

| Need | Approach |
|---|---|
| API types | Generated from OpenAPI into `internal/api/gen` (Go) and `web/src/api` (TS); never hand-edited |
| Permissions | Defined once in Go; exposed via `GET /permissions`; UI consumes it — no duplicated lists |
| Hardening checks | YAML embedded via `go:embed`; schema validated at build time |
| Runtime port | `runtime.Runtime` interface lets deploy engine be tested with a fake; Docker implementation is the only production one |
| Edge port | `edge.Proxy` interface (Caddy implementation), keeps future access providers pluggable |

### 4.5 Key configuration and environment files

| File / variable | Purpose |
|---|---|
| `/etc/codeplo/config.toml` | Listen socket/port, data dir, Caddy binary path, log level, module defaults |
| `/var/lib/codeplo/codeplo.db` | SQLite database (WAL) |
| `/var/lib/codeplo/master.key` | Secrets master key (`0600`, root-owned) |
| `/var/lib/codeplo/caddy/` | Caddy data (certificates, ACME state) |
| `/var/lib/codeplo/builds/` | Build workspaces (pruned) |
| `/run/codeplo/codeplo.sock` | Panel unix socket |
| `/run/codeplo/caddy-admin.sock` | Caddy admin API socket |
| `CODEPLO_CONFIG`, `CODEPLO_DATA_DIR`, `CODEPLO_LOG_LEVEL` | Environment overrides |
| `CODEPLO_ENV=dev` | Enables dev-only behavior (relaxed cookie `Secure`, Vite proxy) — refused in production builds |

---

## 5. UI/UX Foundations

### 5.1 Navigation structure

```
Sidebar
├── Dashboard            overall health, recent deployments, security score, alerts
├── Projects             project → environment → resources
├── Server
│   ├── Security         audit, score, apply, change history
│   ├── Terminal         host shell
│   ├── Proxy & Domains  Caddy status, certificates, routes
│   └── Monitoring       host/container metrics   (module)
├── Templates            one-click catalog         (module)
├── Notifications        channels, alert rules     (module)
├── Backups              schedules, runs, restore  (module)
└── Settings             users & roles · API tokens · Git connections · Modules & flags · Audit log · Updates
Top bar: environment breadcrumb · Ctrl+K command palette · user menu
```

Module-owned items appear only when the module is enabled (driven by `GET /modules`; navigation itself is static in the SPA).

### 5.2 Key interaction patterns

| Pattern | Behavior |
|---|---|
| **Command palette (Ctrl+K)** | Jump to any resource, run common actions (deploy, restart, open terminal) |
| **Live state** | One SSE connection (`/events`) updates status badges app-wide; no polling |
| **Plan → confirm → apply** | Hardening and other host-changing actions show a diff/plan first; apply starts the revert countdown |
| **Revert countdown** | Persistent banner with timer and **Confirm** button after risky changes; the revert itself runs from a systemd timer, so it fires even if the daemon is gone |
| **Step-up dialog** | Re-prompts TOTP inline for sensitive actions, then resumes the action |
| **Write-only secrets** | Secret values are masked after save; users can **replace or rotate, never read back** — there is no reveal action |
| **Deployment view** | Streaming log, step timeline, health-check attempts, commit info, Rollback button on previous successes |
| **Permission-aware UI** | Controls hidden/disabled using the same permission strings the API enforces |

### 5.3 Most important screen flows

1. **First-run wizard:** open the SSH-tunnel URL with the one-time token → create owner → enroll TOTP → *(optional)* move the panel to an HTTPS domain (DNS check → certificate → new URL) → select the operator user → run the security audit → review the plan → apply Tier 1 → *(with console-access acknowledgement)* apply the firewall → confirm.
2. **Harden the server:** Security → *Run audit* → score + failing checks → *Plan fixes* → diff view and preflight results → *Apply* → countdown banner → *Confirm* (or let it auto-revert) → score updates.
3. **Deploy from image:** Projects → New resource → Image → image ref, port, domain → Deploy → log stream → healthy → live URL.
4. **Deploy from Git:** New resource → Git → paste repo URL → generate/copy deploy key → choose branch + Dockerfile → copy webhook URL + secret → Deploy.
5. **CI/CD via GitHub Actions:** Resource → Deploy hook → copy generated workflow snippet and secret → push → Actions builds and pushes image → hook triggers pull + rolling update.
6. **Rollback:** Resource → Deployments → pick previous success → Rollback → health-checked switch back.
7. **Terminal:** Server → Terminal (or resource → Shell) → root requires step-up → session audited.

### 5.4 Design system

| Choice | Rationale |
|---|---|
| **shadcn/ui + Radix primitives + Tailwind** | Accessible, unstyled-by-default components copied into the repo (no heavy runtime dependency) |
| **Lucide icons** | Small, consistent |
| **TanStack Table** | Resource and deployment lists with filtering/sorting |
| **xterm.js (+ fit and web-links addons)** | Terminal and log rendering |
| **CodeMirror 6** | Lightweight editor for compose files and Caddy snippets (instead of Monaco) |
| **Recharts or uPlot** | uPlot preferred for small, fast metric charts |
| Theming | CSS variables, light/dark, WCAG AA contrast targets, keyboard-navigable everywhere |

---

## 6. Phased Roadmap

Phases are ordered by **dependency**. No durations are given; a phase is finished only when its **exit criteria** pass.

### 6.1 Phase overview

| Phase | Name | Goal | Depends on | Tier |
|---|---|---|---|---|
| **P0** | Foundations | Lock decisions, threat model, repo skeleton, de-risk two spikes | — | MVP |
| **P1** | Control-plane core | Installable daemon with auth, 2FA, audit, API contract, embedded UI shell, module system | P0 | MVP |
| **P2** | Secure edge & host hardening (staged) | **P2a** edge + audit + safe fixes · **P2b** reversible firewall · **P2c** guarded SSH apply | P1 | P2a, P2b MVP · P2c Beta |
| **P3** | Terminal & logs | Secure host/container terminal and live log streaming | P1, P2a (operator identity, HTTPS) | MVP |
| **P4** | Projects & deployments | Image deploys first, then Dockerfile; durable state machine, isolation policy, rollback, env/secrets, volumes | P1, P2a | MVP |
| **P5** | Git & CI/CD | Deploy keys, HMAC webhooks, registry-pull deploy hook | P4 | MVP |
| **P6** | Operations | Metrics history, alerts, notifications, databases, backups, RBAC roles | P4 | Soon after |
| **P7** | Extensibility | Extension API, event webhooks, template importer, MCP module, GitHub App, PR previews | P5, P6 | Soon after |
| **P8** | Scale & advanced access | Agent mode, SSO, passkeys, access providers, more Git providers | P7 | Later |
| **P9** | Release hardening | Signed releases, self-update, arm64, docs, polish | continuous; gate before 1.0 | Cross-cutting |

### 6.2 Phase goals and exit criteria

| Phase | Exit criteria ("done" means all true) |
|---|---|
| **P0** | Threat model and ADRs merged · Caddy admin-API spike adds/removes a route with no dropped connections · Docker spike runs start → health-wait → swap · CI pipeline green (lint, test, build, memory-budget job scaffolded) |
| **P1** | `install.sh` yields a running service on a clean VM · owner opens the panel through the SSH tunnel with a one-time token and logs in with TOTP · every API route has a declared permission and the authz-matrix test passes · audit log is hash-chained and `doctor --verify-audit` passes (and detects a tampered row) · idle RSS ≤ budget (Profile A) · OpenAPI → Go/TS generation is part of CI |
| **P2a** | Panel reachable over automatic HTTPS on a real domain, with the tunnel path still working · audit mode makes **zero** changes (verified by filesystem/ruleset diff) · Tier 1 fixes applied idempotently · fail2ban ignore list contains current admin IP · operator identity selectable |
| **P2b** | Firewall applies runtime-first, then persists only on confirm · killing the daemon after apply still reverts on schedule · reboot before confirm returns to the previous state · external scan shows only SSH/80/443 on IPv4 and IPv6 · existing Docker rules untouched · all VM fault-injection cases pass |
| **P2c** | SSH apply blocked without proven key login or without console acknowledgement · cloud-init drop-in ordering handled (`sshd -T` effective values verified) · reload (not restart) keeps sessions · lockout simulations auto-revert · feature flag flips from plan-only to apply only after the VM matrix passes |
| **P3** | Host shell and container shell work in the browser · host shell refuses to start until an operator identity is configured · root shell disabled by default and blocked without step-up when enabled · closing the tab ends the PTY · every session has an audit row · logs stream with backpressure and survive reconnects |
| **P4** | Image deploys (then Dockerfile) reach a custom HTTPS domain · every policy violation in §3.11 is rejected by a test · failed health check leaves old version serving · daemon-kill and reboot mid-deploy recover with no orphans and no traffic change · rollback restores previous digest · secrets are never returned by the API or appear in logs/layers · pre-flight refuses builds under the disk threshold · Profile B and C pass |
| **P5** | Push to configured branch triggers a deploy · invalid or replayed webhook rejected · registry-pull hook deploys an image pushed by GitHub Actions · private repo clones via deploy key |
| **P6** | Metrics history visible · an alert fires to a configured channel on container-down/deploy-fail/disk-low · Postgres/MySQL/Redis deployable from a template · scheduled backup lands in S3-compatible storage and a restore is demonstrated · four roles enforced |
| **P7** | A sample out-of-process extension receives events and invokes an action with a token · templates import and deploy · MCP client can list resources and trigger a deploy · GitHub App installs and reports commit status · PR preview created and cleaned up on close |
| **P8** | A second node joins via agent mode and receives a deployment · OIDC login works · passkey registration/login works |
| **P9** | Release artifacts signed and verifiable · `codeplo update` upgrades with automatic DB snapshot and can roll back · arm64 passes the full suite · docs complete |

### 6.3 Parallel tracks

| Track | Owns | Can start when | Interface it depends on |
|---|---|---|---|
| **A — Core/API** | store, authn/authz, audit, jobs, events, module registry, OpenAPI | P0 | — |
| **B — Edge & Hardening** | Caddy supervisor/renderer/reconciler, hardening engine, drivers, YAML checks | P1 skeleton merged (`Module`, `platform` exec helpers) | `edge.Proxy`, `Module` |
| **C — Deploy Engine** | runtime adapter, builders, rollout, retention, git, webhooks | `runtime.Runtime` interface frozen | `runtime`, `edge.Proxy` (fakes first) |
| **D — Frontend** | SPA shell, features per domain | OpenAPI contract draft + generated client | `api/openapi.yaml` |
| **E — QA & Security** | threat model, authz matrix, VM e2e, memory budget, fuzzing, release pipeline | P0 | all |

```
P0 ─► P1 ─┬─► P2 (B) ──┐
          ├─► P3 (A/D) ─┼─► P4 (C) ─► P5 (C) ─► P6 ─► P7 ─► P8
          └─ frontend shell (D) runs alongside every phase     P9 (E) continuous
```

### 6.4 MVP option and recommended cuts

**MVP = R1 (see §1.6).** If scope must tighten, cut in this order without breaking the product story:

| Cut (in order) | What the trial loses | Safe because |
|---|---|---|
| 1. Firewall (P2b) stays plan-only, delivered with R2 | Automated firewall | Audit generates the exact plan for manual use; no lockout risk |
| 2. Terminal session recording (E4) | Byte-level replay | Metadata audit remains |
| 3. Dockerfile build on host (keep image + registry-pull) | On-host builds | Registry-pull is the lighter path on small hosts anyway |
| 4. Webhook push-to-deploy (keep registry-pull hook) | Direct Git-triggered builds | Hook covers CI/CD |
| 5. Static hosting (F4), Compose (F3) | Those resource types | Already 🟡 |

**Never cut:** read-only audit, the dead-man switch for anything that touches firewall or SSH, 2FA, audit log, workload isolation policy, health-gated rollout, durable deployment recovery, rollback.

### 6.5 Recommended execution order

Dependencies allow more than one order; this one validates the riskiest assumptions first.

```
P0 ─► P1 ─► P2a ─► P4 (image deploys) ───► [ R0 walking skeleton: measure Profiles A/B ]
                         │
                         ├─► P2b (firewall, VM lab gate) ─┐
                         ├─► P3  (operator identity → terminal)  ├─► P5 ─► [ R1 trial ]
                         └─► P4 (Dockerfile, compose, static)    ┘
R2: P2c, P6, parts of P7        R3: P8 selections, P9
```

### 6.6 Deliberately deferred

Multi-server agents, SSO, passkeys, wildcard certs, additional Git providers, alternative builders (Railpack/Nixpacks), remote build servers, log drains, Prometheus endpoint, uptime checks, i18n, rootless Docker, RHEL/Alpine support — plus everything in the ⚪ list.

---

## 7. Detailed Build Plan (per phase)

### P0 — Foundations

**Backend**
- Repo, Go module, `Makefile` targets (`gen`, `lint`, `test`, `build`, `run`), CI workflows.
- Spike A: `internal/modules/edge` proof — start Caddy with unix admin socket, `POST /load`, `PATCH /id/<route>`, verify no connection drops.
- Spike B: `internal/modules/runtime` — pull image, create container, wait for health, swap.

**Docs:** `docs/threat-model.md`, `docs/adr/0001-single-binary.md`, `0002-sqlite.md`, `0003-caddy-admin-api.md`, `0004-db-as-source-of-truth.md`, `0005-module-system.md`.

**Key files:** `go.mod`, `Makefile`, `.github/workflows/{ci,release,memory-budget}.yml`, `docs/threat-model.md`, `docs/adr/*.md`, `test/spikes/*`.

**Business rules:** none yet — this phase defines the rules below.

---

### P1 — Control-plane core

**Backend**
- Tables: `users, roles, role_permissions, sessions, api_tokens, audit_events, settings, feature_flags, modules, jobs`.
- Services: password hashing, session issue/validate/revoke, TOTP enroll/verify/recovery, step-up, token mint/verify, audit writer, durable job queue, event bus, module registry, secrets crypto.
- Endpoints: `/setup/*`, `/auth/*`, `/users`, `/tokens`, `/roles`, `/permissions`, `/audit`, `/system/status`, `/modules`, `/feature-flags`, `/events` (SSE).
- CLI: `codeplo doctor | reset-password | disable-2fa | status`.
- Installer + systemd unit.

**Admin/Frontend**
- SPA shell, router, generated API client, login + TOTP, first-run wizard (steps 1–2 here), dashboard skeleton, settings (users, tokens, audit log), permission-aware helpers, theme.

**Key files**
```
cmd/codeplod/main.go            cmd/codeplo/main.go
internal/app/{app,wire}.go      internal/config/config.go
internal/store/{db,tx}.go       internal/store/migrations/0001_core.sql
internal/authn/{password,session,totp,stepup}.go
internal/authz/{permissions,guard}.go
internal/crypto/{secrets,masterkey,tokenhash}.go
internal/audit/{audit,chain,verify}.go   internal/jobs/{queue,worker,cron}.go
internal/events/bus.go          internal/modules/{module,registry}.go
internal/api/{router,sse}.go    internal/api/middleware/*.go
internal/webui/embed.go         api/openapi.yaml
deploy/{install.sh,codeplod.service}
web/src/{main,router}.tsx       web/src/features/{setup,auth,dashboard,settings}/*
test/authz/matrix_test.go
```

**Business rules**
- First-run setup endpoint works **once** and only while no user exists; it is disabled afterward.
- `owner` must enroll TOTP before any other action is permitted.
- Every route declares exactly one required permission; routes without one fail CI.
- Tokens and recovery codes are shown once and stored hashed.
- All state-changing actions write an `audit_events` row in the same transaction.
- Login: per-IP and per-account rate limits; generic error messages (no user enumeration).
- Panel binds to unix socket / loopback only; the loopback listener can never be disabled (break-glass).
- Setup endpoint requires the one-time setup token; it expires on first admin creation or after 1 hour.
- `audit_events` rows are chained (`prev_hash`/`hash`); hourly checkpoints go to journald; there is no code path that updates or deletes audit rows.

---

### P2 — Secure edge & host hardening (staged)

Hardening is the riskiest subsystem, so it is delivered in three gated stages (tier definitions and safety mechanisms: §3.10). No stage's *apply* capability is enabled by default until its VM-lab matrix passes.

| Stage | Scope | Risk |
|---|---|---|
| **P2a** | Caddy supervisor/reconciler, panel-domain transition, hardening engine in **audit mode**, Tier 1 safe fixes, operator identity | Cannot cut access |
| **P2b** | Reversible default-deny firewall with the dead-man switch | Can cut inbound access — fully reversible |
| **P2c** | Guarded SSH apply with preconditions | Can cut SSH — guarded, flag-gated, R2 |

**Backend**
- Tables: `domains, edge_snippets, hardening_runs, hardening_results, hardening_changes`.
- `edge`: Caddy supervisor (health, restart), JSON renderer, reconciler, certificate status reader, panel-domain transition (§3.12).
- `hardening`: engine (`detect → plan → preflight → snapshot → apply → confirm | revert`), drivers, score calculator, **revert subsystem** (snapshot writer + `systemd-run` transient timer + `codeplo revert <id>` CLI), preflight evaluators (ssh listeners, authorized_keys, `Accepted publickey` evidence, `sshd -T` effective-config comparison).
- Endpoints: `/edge/*`, `/resources/{id}/domains` (stub until P4), `/hardening/*` (incl. `preflight`), `/setup/verify-domain`.

**Baseline checks (YAML-defined)**

| Key | Tier | Detects | Applies |
|---|---|---|---|
| `upd.unattended` | 1 | No automatic security updates | `unattended-upgrades` configuration |
| `kern.sysctl` | 1 | Missing network hardening (rp_filter, syncookies, etc.) | `/etc/sysctl.d/60-codeplo.conf` |
| `bf.protection` | 1 | No fail2ban/CrowdSec active | Install + enable sshd jail with admin IPs on the ignore list |
| `acct.operator` | 1 | No usable operator identity | Select existing sudo user, or create one with key + sudo policy |
| `net.exposed_services` | 0 | Unexpected listeners on public interfaces | Report with owning process |
| `net.docker_published` | 0 | Published ports reachable externally | Report; optional `DOCKER-USER` enforcement (D13, 🟡) |
| `fw.default_deny` | 2 | Inbound policy not default-deny | `table inet codeplo`: established/related, loopback, ICMP, all sshd listener ports, 80, 443 |
| `ssh.no_password` | 3 | `PasswordAuthentication yes` (effective value) | `/etc/ssh/sshd_config.d/00-codeplo.conf` (**00**, first-match-wins) |
| `ssh.no_root_password` | 3 | Root password login | `PermitRootLogin prohibit-password` |
| `ssh.limit_auth` | 3 | Weak `MaxAuthTries`, empty passwords | Conservative values |

SSH *port* changes are intentionally absent: automation never changes the port.

**Admin/Frontend:** wizard steps for domain transition, operator selection, audit, Tier 1, firewall; Security page (score ring, checks by tier, plan diff, preflight results, console-access acknowledgement, apply, confirm countdown, history); Proxy & Domains page.

**Key files**
```
internal/modules/edge/{module,supervisor,render,reconcile,certs,bootstrap}.go
internal/modules/hardening/{module,engine,plan,preflight,score,tiers}.go
internal/modules/hardening/revert/{snapshot,timer,restore}.go     # also invoked by `codeplo revert`
internal/modules/hardening/drivers/{nftables,ufw,sshd,fail2ban,crowdsec,apt,sysctl,users}.go
internal/modules/hardening/checks/*.yaml
internal/platform/{exec,distro,systemd,apt}.go
internal/store/migrations/0002_edge_hardening.sql
deploy/caddy/bootstrap.json
web/src/features/security/*  web/src/features/domains/*
test/vm/hardening_{audit,tier1,firewall,ssh,faults}_test.go
```

**Business rules**
- **Audit mode never mutates the system** (verified by before/after filesystem and ruleset diffs in tests).
- Tier 2–3 changes follow §3.10 in full: snapshot → timer → runtime-first apply → confirm → persist. No shortcut path exists.
- SSH apply requires: valid effective config via `sshd -T`, `sshd -t`, an `Include` for `sshd_config.d`, proven key login, and the recorded console-access acknowledgement. Port changes are never automated.
- The firewall plan must allow every port `sshd` listens on, loopback, and established/related traffic, and must cover IPv6; it must never flush or edit rules outside `table inet codeplo`.
- fail2ban/CrowdSec must never be enabled without the current admin IP(s) on the ignore list.
- Hostnames are unique across `domains`; the panel domain cannot be claimed by a resource.
- Caddy admin API is unix-socket only; refuse to start if it would listen on TCP.
- Checks are idempotent: re-applying a satisfied check is a no-op.

---

### P3 — Terminal & logs

**Backend**
- Table: `terminal_sessions`.
- `terminal`: host PTY (`creack/pty`) as a configurable unix user (default: operator user; `root` requires step-up), container shell via Docker exec attach, short-lived single-use WS tickets, resize, idle timeout, concurrency limit.
- Logs: container log streaming (SSE) with backpressure and tail/since params.
- Endpoints: `GET /terminal/host` (WS), `GET /terminal/resources/{id}` (WS), `GET /terminal/sessions`, `POST /terminal/ticket`.

**Frontend:** `TerminalPane` (xterm.js), terminal page with tabs, `LogViewer` (virtualized, search, follow), step-up dialog integration.

**Key files**
```
internal/modules/terminal/{module,host,container,ticket,limits}.go
internal/modules/runtime/{logs,exec}.go
web/src/components/app/{TerminalPane,LogViewer}.tsx
web/src/features/terminal/*   web/src/features/logs/*
```

**Business rules**
- **The host shell is unavailable until an operator identity is configured (D10).** It runs as that user; a root shell is disabled by default (`terminal.allow_root = false`) and, when enabled, requires `terminal:root` plus a fresh step-up.
- WebSocket upgrade requires: valid session, `Origin` match, valid single-use ticket (≤ 30 s), and `terminal:host` (or `terminal:root` + fresh step-up).
- Idle timeout (default 15 min) and max concurrent sessions per user enforced server-side.
- Every session writes `terminal_sessions` (user, IP, target, start/end); recording is opt-in per setting.
- PTY process group is killed when the socket closes.
- `viewer` role never receives terminal or secret access.

---

### P4 — Projects & deployments

**Backend**
- Tables: `projects, environments, resources, resource_env_vars, volumes, deployments, deployment_logs`.
- `runtime`: Docker adapter (networks per environment, containers, volumes, images, pruning).
- `deploy`: engine implementing §3.8; builders `image`, `dockerfile` (then `compose`, `static` as 🟡); rollout and retention; build queue; cancel; cleanup job; resource limits defaults.
- `edge` integration: resource domains reconcile into Caddy.
- Endpoints: `/projects`, `/environments`, `/resources/*`, `/resources/{id}/env`, `/volumes`, `/deployments/*`, `/system/cleanup`.

**Frontend:** Projects tree, resource creation flows (image / Dockerfile), env var editor (scopes, secret toggle), domains tab, deployments timeline with log stream, rollback, basic dashboard status cards (I1).

**Key files**
```
internal/modules/runtime/{docker,network,image,volume,prune}.go
internal/modules/deploy/{module,engine,states,recover,preflight,rollout,retention,queue,cancel}.go
internal/policy/{policy,compose,mounts,caps}.go    internal/policy/policy_test.go
internal/modules/deploy/builders/{builder,image,dockerfile,compose,static}.go
internal/store/migrations/0003_workloads.sql
web/src/features/{projects,resources,deployments,env}/*
```

**Business rules**
- Deployments follow the durable state machine in §3.8; on boot, non-terminal deployments become `interrupted` and orphans (label `codeplo.deployment`) are removed.
- **Every container and Compose service passes the isolation policy (§3.11) before creation.** Violations fail the deployment with a precise message.
- Pre-flight refuses to start when free disk is below `max(2 GiB, 10%)` or an on-host build would exceed the RAM guard (override logged).
- One active deployment per resource; a newer trigger supersedes queued (not running) ones.
- **Route switches only after the new container is healthy.** Otherwise the old container continues to serve and the deployment fails with logs attached.
- Default resource limits applied unless overridden (protects small hosts); build queue concurrency = 1.
- Retain the last `retain_images` (default 5) image digests per resource; rollback redeploys a retained digest, never rebuilds.
- Secrets: encrypted at rest, **write-only via API (replace/rotate; no reveal endpoint exists)**, redacted from logs by exact value, scope-aware (`build`, `runtime`, `both`); build-scope secrets use BuildKit secret mounts, never `ARG`.
- `codeplo` containers publish no ports; Caddy reaches them over internal networks. Opting in to a published port is explicit and audit-flagged.
- Names/slugs validated against a strict pattern before use in Docker or Caddy config.

---

### P5 — Git & CI/CD

**Backend**
- Tables: `git_connections, resource_sources, webhook_deliveries, deploy_hooks`.
- `git`: clone/fetch via bundled Git or `go-git` with per-resource deploy key; webhook receiver (GitHub push events); registry-pull deploy hook; delivery dedupe.
- Endpoints: `/git/connections`, `POST /resources/{id}/deploy-key`, `POST /webhooks/github`, `POST /hooks/deploy/{token}`.

**Frontend:** Git source form, deploy-key display (public key + copy), webhook URL/secret panel, deploy-hook panel with copy-paste GitHub Actions workflow, delivery history.

**Key files**
```
internal/modules/git/{module,clone,deploykey,webhook,hook}.go
internal/modules/git/providers/github.go
deploy/github-actions/codeplo-deploy.yml
web/src/features/git/*
```

**Business rules**
- Webhook accepted only if HMAC-SHA256 matches (constant-time) and `delivery_id` is unseen.
- Only pushes to the **configured branch** (and matching path filters, when set) deploy; `[skip deploy]` in the head commit message is honored.
- Deploy-hook tokens are per resource, hashed at rest, rotatable, rate-limited; the hook may only deploy an image from the resource's configured repository/tag policy.
- Deploy keys are generated per resource (ed25519), private half encrypted, never returned after creation.
- Clone runs with a restricted environment and timeouts; build contexts are confined to the clone directory.

---

### P6 — Operations

**Backend:** `monitor` (host/container sampler writing `metrics_samples` ring buffer, alert evaluator), `notify` (Telegram, Discord, email, webhook drivers), `databases` (Postgres/MySQL/Redis definitions), backups (dump via Docker exec → local/S3-compatible, retention, restore), role management UI backing (`B7`), cron jobs for resources (`K2`), log retention (`E7`).

**Frontend:** Monitoring charts, Notifications page (channels, rules), Databases (create, connection info), Backups (schedules, runs, restore wizard), Roles editor.

**Key files**
```
internal/modules/monitor/{sampler,alerts,ring}.go
internal/modules/notify/{module,telegram,discord,email,webhook}.go
internal/modules/databases/{postgres,mysql,redis,backup,restore}.go
web/src/features/{monitoring,notifications,backups}/*
```

**Also in P6:** post-switch observation window with automatic rollback (F17) and pre-deploy backups for resources with databases.

**Business rules:** Alert rules have cooldowns and dedupe; backups are verified by size/checksum before marked successful; restores require step-up and create a pre-restore snapshot; databases bind to the internal network only unless explicitly exposed.

---

### P7 — Extensibility

**Backend:** `extensions` (registration, scoped tokens, event delivery with retries, action endpoints), outbound event webhooks (`J4`), `templates` importer (parse compose + metadata header, generate secrets/URLs), `mcp` module, GitHub App provider (`G5`), commit status reporting (`G7`), PR previews (`G8`), path filters (`G6`).

**Frontend:** Extensions page, Templates catalog with search, GitHub App connect flow, preview environment views.

**Key files**
```
internal/modules/extensions/{module,registry,deliver,actions}.go
internal/modules/templates/{parser,catalog,deploy}.go
internal/modules/mcp/{module,tools}.go
internal/modules/git/providers/githubapp.go
internal/modules/git/previews.go
web/src/features/{extensions,templates}/*
```

**Business rules:** Extensions receive only events they subscribed to and can call only actions their token scopes allow; template-generated secrets are created server-side; PR previews have TTL and resource limits, and are removed when the PR closes; MCP tools use the same permission checks as the REST API.

---

### P8 — Scale & advanced access (later)

Agent mode (outbound-initiated, mTLS or token-bound channel; same binary with `--agent`), multi-server placement, OIDC SSO, passkeys, access providers (Cloudflare Tunnel, Tailscale), wildcard certs, GitLab/Gitea providers, Railpack/Nixpacks builder plugins, remote build servers, RHEL/Alpine hardening drivers, rootless Docker.

### P9 — Release hardening (cross-cutting)

GoReleaser multi-arch builds, cosign signatures, `codeplo update` with pre-migration SQLite snapshot and `codeplo rollback`, arm64 CI, documentation site, accessibility pass, performance tuning, mobile layouts, command palette, i18n scaffolding.

---

## 8. Data Bootstrap & Seed Strategy

### 8.1 Initial seed data

Seeds are applied by idempotent migrations and `go:embed`ded files; re-running never duplicates or overwrites admin edits (`INSERT … ON CONFLICT DO NOTHING`, with `builtin = 1` flags for protected rows).

| Seed | Content | Source |
|---|---|---|
| Permissions | Full registry (`resource:read`, `deploy:create`, `env:write`, `terminal:host`, `terminal:root`, `hardening:apply`, `users:manage`, … — deliberately **no** secret-reveal permission) | Generated from `internal/authz/permissions.go` |
| Roles | `owner`, `admin`, `deployer`, `viewer` with `role_permissions` | `0001_core.sql` |
| Settings | Session TTLs, step-up window (5 min), terminal idle timeout (15 min), `terminal.allow_root` (false), retention defaults, build concurrency (1), `retain_images` (5), observation window (60 s), disk pre-flight threshold, hardening revert window | `settings` defaults |
| Feature flags | One per module and per 🟡/🔵 capability, default off for non-MVP | `feature_flags` |
| Modules | `hardening, edge, terminal, deploy, git` enabled; `monitor, notify, databases, templates, extensions, mcp` disabled until their phase | `modules` |
| Hardening checks | The baseline YAML set with tiers (§7 P2) | `internal/modules/hardening/checks/*.yaml` |
| Alert rule presets | `disk_low`, `container_down`, `deploy_failed`, `cert_expiring` (created disabled) | Phase 6 seed |
| Templates | Bundled catalog (Postgres, MySQL, Redis first) | `templates/*.yaml` |

### 8.2 Adding things later without a code deploy

| Want to add… | Do this | Code change? |
|---|---|---|
| A new **role** | Create role + pick permissions in Settings → Roles | No |
| A new **hardening check** | Drop a YAML file using existing drivers (or upload via Settings → Hardening) | No (new *driver* needs code) |
| A new **alert rule** | Settings → Alerts: pick condition, channels, cooldown | No |
| A new **notification channel** | Add Telegram/Discord/webhook/email config | No |
| A new **one-click service** | Add a template file (compose + metadata header) to the catalog | No |
| A new **feature toggle** | Flip in Settings → Modules & Flags | No |
| A new **module** | Implement `Module` and register | Yes (additive only) |
| A third-party **extension** | Register a container + token via the extension API | No |

### 8.3 Admin tools needed on day 1

1. **First-run wizard** (one-time-token bootstrap, owner, TOTP, optional HTTPS domain, operator identity, audit, Tier 1, firewall).
2. **Security page** (audit/plan/apply/confirm/history).
3. **Recovery CLI** (`codeplo setup-token`, `revert`, `reset-password`, `disable-2fa`, `doctor [--restore-network] [--verify-audit]`) for lock-out situations.
4. **Settings → Users, Tokens, Audit log, Modules & Flags.**
5. **Proxy page** with Caddy status, certificate list, and a manual *Reconcile* action.
6. **System page** showing version, disk usage, and a *Cleanup now* action.

---

## 9. Testing, Quality & Rollout

### 9.1 Test strategy

| Layer | Scope | Tooling |
|---|---|---|
| **Unit** | crypto, token hashing, TOTP, permission evaluation, Caddy renderer (golden JSON), hardening plan generation, webhook verification, config validation | `go test`, table-driven, golden files |
| **Integration** | Store + migrations (real SQLite), deploy engine against **real Docker** and a real Caddy, job queue durability across restarts, SSE/WS behavior | `go test -tags integration`, testcontainers-style helpers |
| **Authorization matrix** | Generated from the route table: for every route × role × token scope, assert allow/deny matches the permission registry; fails on any unguarded route | `test/authz` |
| **E2E (UI)** | First-run wizard, login + 2FA, deploy from image, rollback, terminal open, hardening plan/confirm | Playwright |
| **VM tests** | Install on clean Debian/Ubuntu VMs (including cloud-init images); apply each hardening tier; verify firewall via external scan on IPv4 and IPv6; reboot persistence; **hardening fault injection** (kill daemon after apply, reboot before confirm, block confirm, `sshd -t` failure, cloud-init drop-in ordering, Docker with published ports, fail2ban self-ban); **deployment fault injection** (kill daemon mid-deploy, reboot between start and switch, cancel mid-build, disk full, OOM) | Lima/QEMU in CI (nightly + release); release gate for P2b/P2c |
| **Policy tests** | One test per row of the §3.11 policy table, each attempting the violation (privileged, host network, docker.sock, forbidden bind mounts, extra caps, Compose smuggling) and asserting rejection | `internal/policy` + integration |
| **Audit integrity** | Tamper with, delete, and reorder rows; `doctor --verify-audit` must detect each | Unit + VM |
| **Security** | Fuzz webhook/JSON parsers, path-traversal and command-argument validation tests, dependency and container scanning (`govulncheck`, `npm audit`, Trivy), secret scanning | CI jobs |
| **Resource profiles** | Profile A (idle), B (normal operation), C (pressure) from §1.4, run on the 1 vCPU / 2 GB and 2 vCPU / 4 GB reference VMs; idle RSS budget fails the build when exceeded; B and C record total host memory, CPU, latency and recovery | `memory-budget.yml` + VM lab |

### 9.2 Performance and load considerations

- SQLite: WAL mode, `busy_timeout`, single writer connection plus a read pool; batch log inserts (`deployment_logs`) in transactions.
- Log and event streams use bounded channels with drop/disconnect policies so a slow browser cannot grow memory.
- Build queue concurrency of 1 and default container limits protect the host under load.
- Metrics ring buffer is size-capped; pruning runs on a schedule.
- Target scenario: tens of resources and a handful of concurrent UI users; no horizontal scaling is designed for in the single-node product.

### 9.3 Feature-flag strategy

- Flags live in `feature_flags` and are exposed through `GET /feature-flags`; config can force values (`config.toml`).
- Every module and every 🟡/🔵 capability ships behind a flag, default **off** until its phase's exit criteria pass.
- The SPA reads flags and module manifests to show or hide navigation; the server independently refuses disabled endpoints (`404`).
- Experimental hardening checks carry `experimental: true` and are audit-only until promoted.
- **Apply capabilities for Tier 2 (firewall) and Tier 3 (SSH) are separate flags, default plan-only**, flipped only after their VM matrix passes in CI for the release being shipped.

### 9.4 Deployment and environments

| Environment | Purpose |
|---|---|
| **dev** | Local: Vite dev server proxying to `codeplod`, Docker locally, `CODEPLO_ENV=dev` |
| **ci** | Ephemeral runners: unit, integration, authz, e2e |
| **vm-lab** | Disposable Debian/Ubuntu VMs for install/hardening/upgrade tests |
| **edge channel** | Nightly builds for early adopters (`channel=edge`) |
| **stable channel** | Tagged, signed releases |

Release flow: tag → GoReleaser builds (linux/amd64, linux/arm64) → checksums + cosign signatures → GitHub Release → installer and `codeplo update` verify before applying.

### 9.5 Rollback approach

| Layer | Rollback mechanism |
|---|---|
| **Application deployments** | One-click rollback to retained image digest; failed health checks never switch traffic; post-switch observation window auto-rolls back. Database changes are **not** rolled back |
| **Host hardening** | Snapshot of previous state + systemd transient timer running `codeplo revert <id>` independently of the daemon; firewall rules are runtime-only until confirmed (reboot also reverts); manual *Revert* per change |
| **Edge configuration** | Last known-good Caddy config kept; failed `POST /load` leaves Caddy on the previous config; reconcile re-derives from DB |
| **codeplo itself** | Pre-migration SQLite snapshot + previous binary kept; `codeplo rollback` restores both. Migrations are forward-only, so rollback restores the snapshot rather than reversing SQL |
| **Total access loss** | Recovery CLI over SSH/console (`codeplo doctor`, `reset-password`, `disable-2fa`) |

---

## 10. Risks & Mitigations

| # | Risk | Type | Likelihood | Impact | Mitigation |
|---|---|---|---|---|---|
| R1 | Hardening change locks the owner out of the VPS | Technical | Medium | Critical | Staged tiers (§3.10); audit first; daemon-independent systemd revert timer; runtime-first firewall; proven-key-login and console-ack preconditions; port changes never automated; fault-injection VM matrix as release gate; flags default plan-only |
| R2 | Docker bypasses firewall, exposing services unexpectedly | Technical | High | High | `codeplo` containers publish no ports; audit flags any published port; optional `DOCKER-USER` enforcement (D13) |
| R3 | Panel compromise = host compromise | Security | Low | Critical | Not publicly exposed raw, mandatory 2FA, step-up, audit log, signed releases, minimal dependencies, documented threat model |
| R4 | Terminal feature abused (XSS/CSWSH/session theft) | Security | Medium | Critical | Origin checks, single-use tickets, strict CSP, SameSite=Strict cookies, root shell step-up |
| R5 | Building untrusted code on the host harms the server | Technical | Medium | High | Concurrency 1, resource limits, CI-build + registry-pull path, clear UI warnings |
| R6 | Small-VPS builds exhaust memory/disk | Technical | High | Medium | Pre-flight disk/RAM guard, build memory limits, RAM-aware UI warnings, registry-pull recommended on small hosts, cleanup job, disk alerts, Profile C tests |
| R7 | Caddy and DB drift apart | Technical | Medium | Medium | Reconciler at boot, on change, and periodically; drift reported in UI |
| R8 | SQLite write contention under log bursts | Technical | Low | Medium | Single-writer design, batched inserts, bounded queues |
| R9 | Scope creep toward Coolify's full feature set | Product | High | High | §2 triage is binding; new features enter as modules behind flags |
| R10 | Distro differences break hardening drivers | Technical | Medium | Medium | Debian/Ubuntu only at launch; driver interface; VM matrix in CI |
| R11 | Supply-chain compromise of dependencies or releases | Security | Low | Critical | Pinned deps, `govulncheck`, signed artifacts, minimal third-party surface, reproducible builds |
| R12 | Webhook spoofing / replay | Security | Medium | High | HMAC verification, delivery-ID dedupe, branch/repo allowlists, rate limits |
| R13 | Master-key loss makes secrets unrecoverable | Operational | Low | High | Key-ID rotation, documented backup procedure, `doctor` warns when no key backup acknowledged |
| R14 | Upgrade corrupts state | Operational | Low | High | Pre-migration snapshot, `codeplo rollback`, migration tests on real prior-version databases |
| R15 | Frontend bundle grows heavy, hurting low-bandwidth use | Product | Medium | Low | Route-level code splitting, bundle-size budget in CI, CodeMirror/uPlot over heavier alternatives |
| R16 | Revert mechanism itself unavailable (systemd broken, provider firewall at fault) | Operational | Low | Critical | Documented provider-console prerequisite (AS-8) recorded in audit; `doctor --restore-network`; reboot-reverts property of runtime-first rules |
| R17 | fail2ban/CrowdSec bans the operator | Technical | Medium | Medium | Admin IPs on ignore list at install; test in VM lab; unban via console/CLI documented |
| R18 | Interrupted deployments leave orphans or wrong routing | Technical | Medium | High | Durable state machine; label-based orphan cleanup; routing derives only from `active_deployment_id`; fault-injection tests |
| R19 | Untrusted code escapes container isolation during builds/runtime | Security | Low | Critical | Enforced isolation policy (§3.11) with violation tests; CI-build recommended for untrusted repos; documented that containers are not a perfect boundary |
| R20 | Tampering with the local audit log goes unnoticed | Security | Low | Medium | Hash chain + hourly external checkpoints + `doctor --verify-audit`; claim limited to *tamper-evident* |

---

## Appendix A — Requirements Checklist

Status: 🟢 MVP · 🟡 soon after · 🔵 later · ⚪ out of scope. "Delivered in" references phases in §6.

| ID | Requirement | Domain | Status | Delivered in |
|---|---|---|---|---|
| A1 | Single-binary installer with checksum verification | Installation & Platform | 🟢 | P1 |
| A2 | Hardened systemd unit | Installation & Platform | 🟢 | P1 |
| A3 | Self-update with pre-migration DB snapshot | Installation & Platform | 🟡 | P9 |
| A4 | Recovery CLI (`codeplo`) | Installation & Platform | 🟢 | P1 |
| A5 | Debian 12+/Ubuntu 22.04+ support | Installation & Platform | 🟢 | P1 |
| A6 | arm64 builds (Raspberry Pi, ARM VPS) | Installation & Platform | 🟡 | P9 |
| A7 | RHEL-family / Alpine host support | Installation & Platform | 🔵 | P8 |
| A8 | Bootstrap access mode: SSH tunnel + one-time setup token | Installation & Platform | 🟢 | P1 |
| B1 | Local users with argon2id passwords | Authentication & Access Control | 🟢 | P1 |
| B2 | Server-side sessions + CSRF protection | Authentication & Access Control | 🟢 | P1 |
| B3 | TOTP 2FA with recovery codes | Authentication & Access Control | 🟢 | P1 |
| B4 | Login rate limiting and lockout | Authentication & Access Control | 🟢 | P1 |
| B5 | Scoped API tokens | Authentication & Access Control | 🟢 | P1 |
| B6 | Append-only, hash-chained audit log | Authentication & Access Control | 🟢 | P1 |
| B7 | Roles: owner / admin / deployer / viewer | Authentication & Access Control | 🟡 | P6 |
| B8 | Passkeys (WebAuthn) | Authentication & Access Control | 🔵 | P8 |
| B9 | OIDC single sign-on | Authentication & Access Control | 🔵 | P8 |
| B10 | Multi-team / organization model | Authentication & Access Control | ⚪ | — |
| C1 | Managed Caddy driven via admin API on a unix socket | Edge & Reverse Proxy (Caddy) | 🟢 | P2 |
| C2 | Automatic HTTPS (Let's Encrypt / ZeroSSL) | Edge & Reverse Proxy (Caddy) | 🟢 | P2 |
| C3 | Per-resource domains, www/apex redirects, force-HTTPS | Edge & Reverse Proxy (Caddy) | 🟢 | P2 |
| C4 | Panel domain with security headers (HSTS, CSP, etc.) | Edge & Reverse Proxy (Caddy) | 🟢 | P2 |
| C5 | Per-route basic auth and IP allowlist | Edge & Reverse Proxy (Caddy) | 🟡 | P2 |
| C6 | Custom Caddy snippets (advanced) | Edge & Reverse Proxy (Caddy) | 🟡 | P2 |
| C7 | Wildcard certificates via DNS-01 | Edge & Reverse Proxy (Caddy) | 🔵 | P8 |
| C8 | Cloudflare Tunnel / Tailscale access mode | Edge & Reverse Proxy (Caddy) | 🔵 | P8 |
| C9 | Traefik / Nginx alternatives | Edge & Reverse Proxy (Caddy) | ⚪ | — |
| D1 | Read-only security audit with reviewed change plan | Host Hardening | 🟢 | P2 |
| D2 | Reversible default-deny firewall (dedicated nftables table) | Host Hardening | 🟢 | P2 |
| D3 | Guarded SSH hardening apply | Host Hardening | 🟡 | P2 |
| D4 | Port-exposure policy: codeplo containers publish no ports | Host Hardening | 🟢 | P2 |
| D5 | Brute-force protection (fail2ban or CrowdSec) | Host Hardening | 🟢 | P2 |
| D6 | Unattended security upgrades | Host Hardening | 🟢 | P2 |
| D7 | Security score dashboard with one-click fixes | Host Hardening | 🟢 | P2 |
| D8 | Kernel / sysctl baseline | Host Hardening | 🟡 | P2 |
| D9 | Hardening checks defined as data (YAML) | Host Hardening | 🟡 | P2 |
| D10 | Operator identity (select existing sudo user or create one) | Host Hardening | 🟢 | P2 |
| D11 | Rootless Docker mode | Host Hardening | 🔵 | P8 |
| D12 | Malware / rootkit scanning | Host Hardening | ⚪ | — |
| D13 | `DOCKER-USER` enforcement for third-party published ports | Host Hardening | 🟡 | P2 |
| E1 | Host terminal in browser (direct PTY) | Terminal & Logs | 🟢 | P3 |
| E2 | Container terminal (Docker exec attach) | Terminal & Logs | 🟢 | P3 |
| E3 | Step-up re-auth (TOTP) for root shell | Terminal & Logs | 🟢 | P3 |
| E4 | Terminal session metadata + optional recording | Terminal & Logs | 🟡 | P3 |
| E5 | Live container log streaming | Terminal & Logs | 🟢 | P3 |
| E6 | Deployment / build log streaming and storage | Terminal & Logs | 🟢 | P3 |
| E7 | Log retention and size caps | Terminal & Logs | 🟡 | P6 |
| E8 | Log drains to external collectors | Terminal & Logs | 🔵 | P7 |
| F1 | Prebuilt image deploy (any registry) | Deployments | 🟢 | P4 |
| F2 | Dockerfile build from Git source | Deployments | 🟢 | P4 |
| F3 | Docker Compose deploy | Deployments | 🟡 | P4 |
| F4 | Static site hosting via Caddy | Deployments | 🟡 | P4 |
| F5 | Health-check-gated rolling update | Deployments | 🟢 | P4 |
| F6 | One-click rollback to a retained image | Deployments | 🟢 | P4 |
| F7 | Environment variables and encrypted secrets | Deployments | 🟢 | P4 |
| F8 | Persistent volume management | Deployments | 🟢 | P4 |
| F9 | CPU / memory limits per resource | Deployments | 🟢 | P4 |
| F10 | Build queue (concurrency 1 default) + cancel | Deployments | 🟢 | P4 |
| F11 | Private registry credentials | Deployments | 🟡 | P4 |
| F12 | Railpack / Nixpacks builder plugins | Deployments | 🔵 | P4 |
| F13 | Remote build server | Deployments | 🔵 | P8 |
| F14 | Docker Swarm | Deployments | ⚪ | — |
| F15 | Workload isolation policy enforcement | Deployments | 🟢 | P4 |
| F16 | Durable deployment state machine with crash/reboot recovery | Deployments | 🟢 | P4 |
| F17 | Post-switch observation window with automatic rollback | Deployments | 🟡 | P6 |
| F18 | Pre-flight disk and memory checks | Deployments | 🟢 | P4 |
| G1 | Public repository clone | Git Integration & CI/CD | 🟢 | P5 |
| G2 | SSH deploy keys for private repos | Git Integration & CI/CD | 🟢 | P5 |
| G3 | Push-to-deploy webhooks (HMAC-verified) | Git Integration & CI/CD | 🟢 | P5 |
| G4 | Registry-pull deploy hook for GitHub Actions | Git Integration & CI/CD | 🟢 | P5 |
| G5 | GitHub App integration | Git Integration & CI/CD | 🟡 | P5 |
| G6 | Branch and path filters, `[skip deploy]` convention | Git Integration & CI/CD | 🟡 | P5 |
| G7 | Commit status / check-run feedback to GitHub | Git Integration & CI/CD | 🟡 | P5 |
| G8 | Pull-request preview deployments | Git Integration & CI/CD | 🟡 | P5 |
| G9 | GitLab / Gitea / Bitbucket providers | Git Integration & CI/CD | 🔵 | P8 |
| G10 | GitHub self-hosted runners | Git Integration & CI/CD | ⚪ | — |
| G11 | Image signature verification (cosign) before deploy | Git Integration & CI/CD | 🔵 | P8 |
| H1 | One-click PostgreSQL / MySQL / Redis | Data Services & Backups | 🟡 | P6 |
| H2 | Additional engines (MariaDB, MongoDB, others) | Data Services & Backups | 🔵 | P6 |
| H3 | Scheduled backups to local disk / S3-compatible | Data Services & Backups | 🟡 | P6 |
| H4 | Guided restore workflow | Data Services & Backups | 🟡 | P6 |
| H5 | Automated backup verification | Data Services & Backups | 🔵 | P6 |
| I1 | Host and container status dashboard | Observability & Notifications | 🟢 | P4 |
| I2 | Metrics history (SQLite ring buffer) | Observability & Notifications | 🟡 | P6 |
| I3 | Notification channels: Telegram, Discord, email, webhook | Observability & Notifications | 🟡 | P6 |
| I4 | Alert rules: disk, container down, deploy failed, cert expiry | Observability & Notifications | 🟡 | P6 |
| I5 | Uptime checks | Observability & Notifications | 🔵 | P6 |
| I6 | Prometheus metrics endpoint | Observability & Notifications | 🔵 | P6 |
| I7 | Error-tracking SaaS integrations | Observability & Notifications | ⚪ | — |
| J1 | Minimal internal module interface | Extensibility & Platform API | 🟢 | P1 |
| J2 | Feature flags | Extensibility & Platform API | 🟢 | P1 |
| J3 | OpenAPI-first API + generated TypeScript client | Extensibility & Platform API | 🟢 | P1 |
| J4 | Outbound event webhooks | Extensibility & Platform API | 🟡 | P7 |
| J5 | Out-of-process extension API | Extensibility & Platform API | 🟡 | P7 |
| J6 | Template catalog importer (compose templates) | Extensibility & Platform API | 🟡 | P7 |
| J7 | MCP server module for AI agents | Extensibility & Platform API | 🔵 | P7 |
| K1 | Docker cleanup (images, build cache, stopped containers) | Scheduled Tasks & Maintenance | 🟢 | P4 |
| K2 | Cron jobs executed inside resources | Scheduled Tasks & Maintenance | 🟡 | P6 |
| L1 | Embedded React dashboard (served by the binary) | Web UI | 🟢 | P1 |
| L2 | First-run setup wizard | Web UI | 🟢 | P1 |
| L3 | Light / dark theme | Web UI | 🟢 | P1 |
| L4 | Mobile-responsive layouts | Web UI | 🟡 | P9 |
| L5 | Command palette (Ctrl+K) | Web UI | 🟡 | P9 |
| L6 | Internationalization | Web UI | 🔵 | P9 |
| M1 | Agent mode (same binary, outbound connection) | Multi-Server & Scale | 🔵 | P8 |
| M2 | Multi-server resource placement | Multi-Server & Scale | 🔵 | P8 |
| M3 | Kubernetes | Multi-Server & Scale | ⚪ | — |
| N1 | Billing and subscriptions | Commercial / Cloud Features From Coolify | ⚪ | — |
| N2 | Cloud-provider server provisioning | Commercial / Cloud Features From Coolify | ⚪ | — |
| N3 | Managed DNS provider integrations | Commercial / Cloud Features From Coolify | ⚪ | — |
| N4 | Built-in traffic analytics | Commercial / Cloud Features From Coolify | ⚪ | — |
| N5 | Sysbox runtime support | Commercial / Cloud Features From Coolify | ⚪ | — |

---

## Appendix B — Coolify Codebase Analysis

Findings from inspecting the Coolify source that shaped this plan.

### B.1 How Coolify works

Evidence column cites paths in the inspected source tree so each claim can be verified independently.

| Area | Observation | Evidence (path) | Implication for `codeplo` |
|---|---|---|---|
| **Stack** | Laravel 12 / PHP 8.4, Livewire (~250 components), Postgres, Redis, Horizon, Reverb, Node terminal server; ~185k lines of PHP, 444 migrations, 64 job classes, ~320 API route declarations | `composer.json`, `TECH_STACK.md`, `app/Livewire/`, `database/migrations/`, `app/Jobs/`, `routes/api.php` | A single Go binary with SQLite removes most moving parts |
| **Process model** | Production image supervises 8 services (db-migration, horizon, init-script, init-seeder, reverb, scheduler-worker, terminal-server, user) beside Postgres and Redis; Traefik and the Sentinel agent are separate containers | `docker/production/etc/s6-overlay/s6-rc.d/`, `docker-compose.prod.yml`, `app/Actions/Server/StartSentinel.php` | This is the source of the 2 vCPU / 4 GB requirement |
| **Execution** | Commands are shell strings run over SSH with ControlMaster multiplexing, even for localhost; host-key checking disabled in SSH options | `app/Helpers/SshMultiplexingHelper.php` | Use the Docker Engine API and fixed-argv exec; no SSH loopback |
| **Deployments** | `ApplicationDeploymentJob` (~6,100 lines): clone → build (Nixpacks, Railpack, Dockerfile, Compose, static, image) → write env + compose with proxy labels → start new container → poll `docker inspect` health → stop old | `app/Jobs/ApplicationDeploymentJob.php` (`rolling_update`, `health_check`) | Keep the health-gated rolling model with a far smaller, interface-based engine and an explicit state machine |
| **Proxy** | Traefik default; Caddy supported via Docker labels; config generated from labels | `app/Enums/ProxyTypes.php`, `bootstrap/helpers/docker.php` (`fqdnLabelsForCaddy`), `bootstrap/helpers/proxy.php` | Drive Caddy through its admin API from DB state |
| **Git / CI** | Webhook controllers for GitHub, GitLab, Bitbucket, Gitea; signature checks; branch matching; PR previews | `app/Http/Controllers/Webhook/` | GitHub first; provider interface for others |
| **Terminal** | Node `ws` + `node-pty` spawning `ssh`, authenticating by calling back to Laravel with the session cookie | `docker/coolify-terminal/terminal-server.js` | In-process PTY and WebSocket tickets |
| **Templates** | ~380 compose YAML templates with metadata headers and magic variables | `templates/compose/*.yaml` | Importable as data in Phase 7 (confirm license/attribution) |
| **Backups** | `docker exec` dump tools → local/S3 | `app/Jobs/DatabaseBackupJob.php` | Same approach in the `databases` module |
| **Host security** | No firewall, SSH-config, or fail2ban management found: searches for `ufw`, `fail2ban`, `iptables`, `nftables`, `sshd_config` match only dev-VM scripts and SSH client options | repository-wide search over `app/`, `bootstrap/`, `scripts/` | `codeplo`'s hardening module is a genuine differentiator |

### B.2 Feature triage summary

| Coolify capability | `codeplo` decision |
|---|---|
| Servers/destinations/SSH keys | Replace with local host (multi-node later via agent) |
| Applications (5 build packs), compose, databases | Image + Dockerfile in MVP; compose/static soon after; Railpack/Nixpacks as builder plugins |
| Traefik / Caddy / Nginx | Caddy only |
| Sentinel metrics | Built-in sampler (module) |
| Teams, Stripe subscriptions, cloud provisioning, DNS providers, traffic analytics, Sysbox, Swarm, GitHub runners, Sentry | Out of scope |
| Notifications (many channels) | Four channel drivers first |
| S3 backups, scheduled tasks, shared variables | Modules / later |
| 2FA, SSO | 2FA in MVP; SSO later |
| MCP server | Module (Phase 7) |

> **License note:** Coolify's `composer.json` declares Apache-2.0. Reusing templates or documentation requires preserving attribution and notices; confirm against its `LICENSE` file before importing any content.

---

## Appendix C — Complete File Overview

Overview of everything that will be created, by area. Individual files per phase are listed in §7; the full directory tree is in §4.2.

| Area | Path | What it contains | Phase |
|---|---|---|---|
| API contract | `api/openapi.yaml` | Full REST contract, source for generated code | P1+ |
| Entrypoints | `cmd/codeplod/`, `cmd/codeplo/` | Daemon and recovery/ops CLI | P1 |
| Wiring & config | `internal/app/`, `internal/config/` | Dependency wiring, config loading/validation | P1 |
| Persistence | `internal/store/` | SQLite setup, goose migrations (`0001_core` … per module), sqlc queries | P1+ |
| Auth | `internal/authn/`, `internal/authz/` | Passwords, sessions, TOTP, step-up, permissions, guards | P1 |
| Security primitives | `internal/crypto/`, `internal/audit/` | Secret encryption, token hashing, audit log | P1 |
| Runtime plumbing | `internal/jobs/`, `internal/events/`, `internal/modules/module.go`, `registry.go` | Durable queue, cron, event bus, module system | P1 |
| HTTP layer | `internal/api/`, `internal/api/middleware/`, `internal/webui/` | Router, middleware, SSE, embedded SPA | P1 |
| Edge | `internal/modules/edge/` | Caddy supervisor, renderer, reconciler, certificate status | P2 |
| Hardening | `internal/modules/hardening/` (+ `drivers/`, `checks/*.yaml`) | Check engine, drivers, plan/apply/revert, score | P2 |
| Platform helpers | `internal/platform/` | Safe exec, distro detection, systemd, apt | P2 |
| Terminal | `internal/modules/terminal/` | Host PTY, container exec, tickets, limits | P3 |
| Runtime adapter | `internal/modules/runtime/` | Docker Engine integration, logs, exec, prune | P3–P4 |
| Deployments | `internal/modules/deploy/` (+ `builders/`) | Engine, rollout, retention, queue, builders | P4 |
| Git & CI/CD | `internal/modules/git/` (+ `providers/`) | Clone, deploy keys, webhooks, deploy hook | P5 |
| Operations | `internal/modules/{monitor,notify,databases}/` | Sampling, alerts, channels, DB services, backups | P6 |
| Extensibility | `internal/modules/{extensions,templates,mcp}/` | Extension API, template catalog, MCP | P7 |
| Frontend | `web/src/{api,components,features,lib,styles}/` | SPA: one feature folder per domain | P1+ |
| Packaging | `deploy/` | Installer, systemd unit, Caddy bootstrap, reusable GitHub Actions workflow | P1, P5 |
| Bundled data | `templates/`, `hardening/` | Template catalog, check reference docs | P2, P7 |
| Tests | `test/{integration,e2e,vm,authz}/` | Integration, Playwright, VM, authorization matrix | P1+ |
| Docs | `docs/` | ADRs, threat model, runbooks, user docs | P0+ |
| CI/CD | `.github/workflows/` | `ci`, `release`, `memory-budget` pipelines | P0 |
| Release config | `.goreleaser.yaml`, `Makefile`, `sqlc.yaml` | Build, generation, release automation | P0 |

---

## Closing statement

**No critical questions remain open.** The items in §0.3 (license, GitHub App timing, non-Docker apps, first notification channel) have safe defaults and do not block starting Phase 0. The recommended starting point is the release ladder in §1.6: Phase 0 → Phase 1 → P2a → P4 (image deploys) as the **R0 walking skeleton**, measuring Profiles A and B before the firewall, terminal, and Git stages are added. Tracks A, B, D, and E run in parallel as soon as the minimal `Module` interface and OpenAPI skeleton are merged.
