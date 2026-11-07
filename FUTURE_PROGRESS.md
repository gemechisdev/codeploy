# FUTURE PROGRESS — where codeplo stands and what "finished" means

Snapshot: **2026-11-07** (finalization pass). Companion files: `PROGRESS.md` (what is built, per phase, with the
evidence tables), `docs/DECISIONS.md` (why each judgment call went the way it did), `FUTURE_TASKS.md` (the
exhaustive T1–T74 checklist), `docs/HANDOFF_NOTES.md` (observations and the patterns this codebase expects),
`docs/site/verification-matrix.md` (per-row proof status for hardware/runner-gated work), `PROMPT.md` (the brief for
the next agent).

This file was originally written by the agent that built P0–P7's backend under a "code only, compile-checked, no
tests" instruction. That instruction no longer holds: local gates have been run, P4–P7 have a real-router test
suite, CI has executed, and `v1.2.0` has been published by `release.yml` on a real tag. Where a claim below says
"proven", a named test or command is given. Where it says "NOT RUN", no result is being claimed.

## 1. State of the build, honestly

| Phase | Code | Proof |
|---|---|---|
| P0 Foundations | complete | threat model + ADRs merged; Docker spike **run and green**; Caddy zero-drop spike **flaky/unmet** |
| P1 Control plane | complete | installer verified on a real systemd host; auth/2FA/audit/authz verified; **idle RSS budget unmet** (78.8 MB vs 50 MB) |
| P2 Edge + hardening | complete (P2a/P2b/P2c code) | P2a verified on WSL (real Caddy, 15/15 installer assertions); Tier-1 apply verified; **P2b/P2c VM fault-injection matrix still NOT RUN** |
| P3 Terminal + logs | complete | race-tested; container half runtime-verified against real Docker 29.1.3; **host-shell run (spike E, T44) still NOT RUN** |
| P4 Projects + deploys | complete | **handler layer proven**: `internal/api/handlers_phase_test.go` runs the real chi router, real guards, real services and a migrated SQLite DB (57 P4–P7 routes, audit chain, refusal-leaves-nothing, write-only secrets, contract shapes). Engine unit tests cover path confinement, git URL validation, BuildKit secrets, `DOCKER_CONFIG`, redaction, image classification. **§3.8 fault-injection Run matrix (T6/T7) still owed** |
| P5 Git + CI/CD | complete | unit proofs for HMAC signature verification, image matching, canonical repo URL and `[skip deploy]`. The full webhook delivery/replay path against a real forge and the reusable Actions workflow (T51) are **NOT RUN** |
| P6 Operations | complete | migrations apply; alert rules, channels, backup schedules and metrics now exercised through the real router (create/patch/delete + audit rows + cross-module channel cleanup); unwired-service 500 behaviour proven. **Live-mode frontend pass still NOT RUN** |
| P7 Extensibility | complete — templates, GitHub App, commit status, PR previews, **J4 extensions and J7 MCP landed** | migration 0008 applies (`TestMigrateAllToV8`); `TestPreviewNoFK` proves the no-FK design; extensions register/enqueue/deliver/authorize, MCP serves flag-gated tools, and `codeplo mcp check --json` ran against a scratch DB. Real GitHub App install (T52) and real-DNS preview lifecycle (T47) **NOT RUN** |
| P8 Scale/advanced access | not started (deliberately out of scope, T74) | — |
| P9 Release hardening | **published**: `.goreleaser.yaml` (amd64 + arm64 raw binaries), `release.yml` with keyless cosign, `codeplo update` / `rollback`, `docs/ENV.md`, `docs/site/operator-guide.md`, `docs/RELEASE_NOTES.md`, ADR 0007, arm64 CI job, `README.md` | **`v1.2.0` is public** — `release.yml` run `42349115778` succeeded on the tag, 13 assets are uploaded and downloadable, `checksums.txt` is keyless-signed and verified in-job, and `releases/latest/download/install.sh` byte-matches the repo script. The one-line installer has **NOT RUN** on a fresh VPS in this pass |

The sentence that used to matter — *"every phase from P4 onward was built under an instruction not to run tests, so
P4–P7's backend is unproven"* — is **no longer true at the handler and module level**. What remains unproven is the
host-facing half: real Docker/VPS/DNS/CI-runner behaviour. Compilation was never evidence; neither is a green unit
suite a substitute for a host run.

## 2. The contract is why extensions/MCP have no routes

`api/openapi.yaml` is contract authority and `test/authz/routes_test.go` asserts the Go route set equals it exactly —
no extra route, none missing, one `x-permission` per operation. The contract has **no route surface** for
extensions, outbound event webhooks or MCP. T53 decided (see `docs/DECISIONS.md`) to deliver J4/J7 behind the CLI,
the daemon's job/delivery loops and a flag-gated unix socket rather than expanding the contract. That decision is
now implemented, not pending. Adding a browser UI for extensions later is a deliberate contract expansion that must
re-run `make gen`, the authz matrix and the mock parity tests in one commit.

## 3. Milestones — what is left of each

**M1 — Make the database real. DONE.** `store.Migrate` (goose) reaches version 8 from an empty dir;
`TestMigrateAllToV8`, `TestMigrateSeeds`, `TestQueryPlans` and `make migrate-check` all pass. `TestPreviewNoFK`
proves the no-FK survival case under `foreign_keys=1`.

**M2 — Turn P4–P7 into evidence. SUBSTANTIALLY DONE, not complete.** The real-router suite covers guards, module
flags, audit-chain writes, refusals, write-only secrets, unwired services and list/read shapes across all 57
later-phase routes. Still owed: engine `Run` fault injection over every §3.8 row (T6/T7), preview build/teardown
unit tests (T14/T15), and a live mock-vs-handler contract conformance run (T25).

**M3 — Run what only a machine can run. MOSTLY NOT RUN.** Spike E (T44), the P2b/P2c VM matrix (T45), real-DNS
previews (T47), public HTTPS (T48), Profile B/C measurements (T50), the reusable deploy workflow (T51) and a real
GitHub App install (T52) all need host resources. CI **has** run: the drift job and the frontend job both failed and
both were fixed; a later run on `0178c65` (`42343047804`) is green on `go`, `go-arm64` and `web`.
`docs/site/verification-matrix.md` is the authoritative per-row status.

**M4 — Finish P7's code. DONE (code), NOT RUN (host).** Extensions, MCP, previews UI and Templates search exist.
The frontend's static/unit gates pass in live build mode, but no click-through against a real daemon (T62).

**M5 — P9. RELEASE PUBLISHED, HOST INSTALL NOT RUN.** `v1.2.0` is public: `release.yml` run `42349115778`
succeeded on the tag, all 13 assets are uploaded and downloadable, and `checksums.txt` is keyless-signed. What is
still owed is running the published one-line installer on a fresh systemd VPS and verifying the resulting daemon.

## 4. What is likely to break, ranked (current)

1. **The one-line installer on a real VPS** — the published script passed `bash -n`, URL/identity branch simulation
   and an asset-download check, but has not installed and booted a daemon on a systemd host. This is the first
   end-to-end check left for P9.
2. **Engine `Run` fault injection (T6/T7)** — the largest untested code path. The handler suite deliberately uses
   `deploy_now=false`, so it proves the HTTP/DB layer, not a container deploy.
3. **Live-mode frontend (T62)** — WS/SSE, step-up and every P4–P7 screen against a running daemon.
4. **Host/VM matrix (T44–T52)** — systemd PTY helper, firewall/SSH applies, DNS, public TLS and reference VMs.
5. **Idle RSS 78.8 MB vs the 50 MB budget (T30)** and **Caddy zero-drop spike (T31)** — reported, not tuned;
   Profile B/C never measured. Do not silent-tune either.

A green CI re-run and the tagged release pipeline are no longer on this list: CI run `42343047804` passed `go`,
`go-arm64` and `web`, and release run `42349115778` passed every step on `v1.2.0`.

Already closed from the old list: migrations 0006/0007 unapplied; zero P4–P7 tests; the `/terminal/ticket`
step-up inconsistency (T28); `POST /setup/operator` `mode:"create"` returning 501 (T27); the missing J4/J7
decision (T53).

## 5. Standing instructions that do not expire

- **No credentials in this repo, ever.** Secrets stay write-only through the API; build-time secrets are BuildKit
  secret mounts, never `ARG`; dump credentials go in the exec environment, never argv; git tokens never appear in
  URLs or argv; registry credentials live in an ephemeral `0700` `DOCKER_CONFIG`.
- Smallest fix. Never delete or weaken a test to make it pass. Never report a skipped or unrun check as a pass.
- A defect you can name gets fixed, not parked.
- Judgment calls go in `docs/DECISIONS.md`; `PROGRESS.md` is updated before every commit; one commit per unit of
  work; push when the local gates pass so CI runs the slow ones.
- A running service is shared state. Nothing destructive or write-bearing against a live or shared system without
  explicit per-action approval; no force-push, no history rewrite, no bare `git stash` / `git reset --hard`.
- Verify the state, not the artifact — a compile says the code type-checks, nothing more.
- **Node is pinned by `web/.nvmrc`.** CI, release and `engines.node` all read it; changing the frontend toolchain
  means changing that file, not a workflow's `node-version` literal.

## Finalization 2026-11-07

Fixed from real CI failures: generated-file drift, the Windows `$(MAKE)` recursion in `gen-check`, the Node 20 /
`undici` engine mismatch, the generated-contract lint false positive, the Windows POSIX-mode race assertion, the
GoReleaser output deletion in `dist-pack`, the missing `codeplo mcp` dispatch, and the alert-rule module gate.
Added: whole-route-table P4–P7 proof, `make test` including vitest, `web/.nvmrc`, `.gitignore` build-output rules.
Still hardware/runner-gated: T44–T52, T62 (live daemon), T70, and the published one-line installer on a fresh VPS.
T63–T66's tagged release pipeline has now run and passed (`42349115778`, `v1.2.0`, 13 assets). See the verification
matrix.
