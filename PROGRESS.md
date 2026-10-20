# PROGRESS: codeplo — P0 and P1 met (with two recorded gaps), P2 implemented and gate-verified

Status: **Phase 2 (P2a edge + audit + safe fixes, P2b reversible firewall, P2c guarded SSH) is
implemented, compiled, race-tested and deployed on a real systemd host.** P2c stays behind
`hardening.apply_tier3`, default off, as the plan requires until its VM matrix passes.
Phase 3 (terminal & logs) is next and not started.
Evidence table below; full detail in `TEST_REPORT.md`, judgment calls in `docs/DECISIONS.md`.

## Phase 2 — what was built

| Area | Files | What it does |
|---|---|---|
| Migration | `internal/store/migrations/0002_edge_hardening.sql` | `domains`, `edge_snippets`, `hardening_runs`, `hardening_results`, `hardening_changes` |
| Edge core | `internal/modules/edge/{client,render,reconcile,certs}.go` | Caddy admin client over the unix socket only (refuses TCP), deterministic whole-config JSON renderer + SHA-256 drift key, reconciler (`Reconcile`, `Status`, `Certificates`, `Loop` supervision with `systemctl restart`), panel-domain row helpers |
| Edge API | `internal/api/handlers_edge.go`, `handlers_domains.go` | `/edge/status`, `/edge/reconcile`, `/edge/certificates`, `GET /domains`, `GET/POST /resources/{id}/domains`, `DELETE /resources/{id}/domains/{domainId}`, `POST /setup/panel-domain` (replaces the P1 501 stub) |
| Daemon wiring | `cmd/codeplod/main.go` | when the `edge` module is enabled: build the reconciler, run one reconcile at startup (non-fatal), supervise in a 30 s loop tied to the daemon context |
| Hardening engine | `internal/modules/hardening/{audit,plan,preflight,apply,score,tiers,revert,drivers}.go` | `detect → plan → preflight → snapshot → apply → confirm \| revert`; ten baseline checks with tier + apply-flag metadata; `sshd -T` effective-config verification; nftables runtime-first plan; fail2ban jail with the operator's IPs ignored; snapshot spool + live-state `revert.json` |
| Hardening API | `internal/api/handlers_hardening.go`, `handlers_hardening_apply.go` | `/hardening/checks` (with `plan_diff` + `apply_enabled`), `/audit`, `/plan`, `/preflight`, `/apply`, `/confirm/{id}`, `/revert/{id}`, `/runs`, `/changes` |
| Dead-man switch | `hardening.ScheduleRevertTimer` + `cmd/codeplo/main.go` `revert <id>` | `systemd-run --on-active=<window>s codeplo revert <id>`; the CLI needs neither daemon, database nor network |
| Platform | `internal/platform/{exec,systemd,distro,apt,host_*}.go` | fixed-argv runner, `SystemdActive`, distro item paths |
| Deploy | `deploy/install.sh`, `deploy/caddy/{codeplo-caddy.service,bootstrap.json}`, `deploy/codeplod.service`, `Makefile` `dist` | installer now ships and verifies five artefacts, requires Caddy as a hard prerequisite, asserts the panel answers, and templates `/etc/default/codeplod` |
| Tests | `internal/api/{edge_test,hardening_apply_test}.go`, `internal/modules/edge/*_test.go`, `internal/modules/hardening/*_test.go` | zero-mutation audit diff, Tier-1 idempotency, runtime-first ordering, dead-man arming and absence for Tier 1, live-state revert, operator-bannable jail refusal, preflight refusals |

## Phase 2 — verification (2026-10-20)

| Check | Result |
|---|---|
| `go build ./...`, `go vet ./...`, `go vet -tags spike ./...`, `gofmt -l .` | Pass |
| `go test -race -count=1 ./...` | Pass — 11 packages ok, 0 FAIL, **0 skips** |
| Route set == OpenAPI + `x-permission` parity (`test/authz`) | Pass |
| `make gen` chain run twice | Idempotent — generated TS schema, stubs and models match committed artifacts |
| Frontend: `tsc --noEmit`, lint guards, `vite build`, `check:mock-free`, `check:external`, `check:size`, vitest | Pass (guards 104 files / 8 rules; 94/94 tests, Node 22.23.3 serial) |
| Spike C — edge render → `POST /load` → no drift, real Caddy v2.11.7 in WSL | **PASS (3.54 s)** |
| Installer on WSL Ubuntu (systemd, real Caddy) | Pass — 15/15 assertions: clean install, both units active 20 s later, `NRestarts=0`, admin socket `660 root codeplo`, `codeplo` user reads the admin API, live config holds the reconciler's `:80`/`:443` servers, panel answers on TCP **and** unix socket, `/var/lib/codeplo` `700 codeplo`, clean journals; plus the four abort cases (tampered binary, tampered unit, missing checksum entry, missing Caddy) |
| Three boot-time defects found by that run | Fixed: ACME placeholder in the bootstrap, `StateDirectory` re-owning the panel's data dir, installer claiming success on a crash-looping panel |
| Spike A (Caddy zero-drop under rapid reloads) | **UNMET** — flaky, kept strict (see DECISIONS) |
| CI executed on a runner | **NOT RUN** — workflows wired, never executed |
| VM fault-injection matrix (§3.10.8) for P2b/P2c | **NOT RUN** — the *semantics* are test-asserted at the API seam; a disposable-VM run (kill daemon after apply, reboot before confirm, external port scan on IPv4+IPv6) is still owed |
| Panel HTTPS on a real public domain | **NOT RUN** — needs DNS pointing at a public host; cert issuance is exercised against Caddy's internal issuer in the spike |
| Idle RSS vs Profile A ≤ 50 MB | **UNMET** (78.8 MB measured on Windows; report, don't tune) |

## Exit criteria (§6.2)

**P0** — threat model + ADRs met; Docker spike met; **Caddy zero-drop unmet**; CI green on a
runner not run.
**P1** — installer on a clean VM met (now with real readiness assertions); tunnel + one-time
token + TOTP met (curl-driven); per-route permission + authz matrix met; audit chain +
`doctor --verify-audit` met; RSS unmet; generation-in-CI partial (works, runner never ran).
**P2a** — audit makes zero changes (tree-diff test) met; Tier 1 fixes idempotent met; fail2ban
ignore list contains the admin IPs met; operator identity selectable met; **panel reachable
over automatic HTTPS on a real domain: not verified** (no public DNS here), though the route,
the ACME policy and the break-glass loopback path are implemented and deployed.
**P2b** — runtime-first then persist-on-confirm met (asserted ordering: boot files written
before the timer is disarmed); revert restores files then drops the live table, and is
idempotent when the table is already gone; **daemon-kill-still-reverts and reboot-before-confirm
are test-asserted at the seam but not VM-verified**; external scan showing only SSH/80/443 not
run.
**P2c** — implemented: `sshd -T` effective comparison, `sshd -t`, `Include` requirement,
`authorized_keys` sanity, `Accepted publickey` proof, console acknowledgement, reload-not-restart,
port changes never automated. **Apply stays flag-gated (default plan-only) until the VM matrix
passes**, which is the plan's own gate.

## Open across the project

- VM lab (`test/vm/*`) for the hardening fault-injection matrix; release gate for P2b/P2c.
- CI has never run on a runner (also the only place the RSS budget is meaningful).
- No signed release; `make dist` is the only artefact producer until P9.
- `/resources/{id}/domains` does not check the resource exists (no `resources` table until P4).
- TOTP replay guard in-memory; sqlc scaffolded but unused; `RouteDef.NoCSRF` declared-unused.

## Next — Phase 3 (terminal & logs)

Host PTY as the operator user with step-up for root, container shell over Docker exec,
single-use WS tickets, `terminal_sessions` audit rows, SSE container + deployment logs with
backpressure. Exit criteria in §6.2 P3; tasks tracked per phase in the plan.
