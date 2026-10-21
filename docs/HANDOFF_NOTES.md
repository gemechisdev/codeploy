# Handoff notes — what I observed building P4 → P7, what worked, what cost time

Written 2026-10-21 by the agent that built P4–P7's backend under "code only, compile checks, no tests". Read
`FUTURE_PROGRESS.md` for the map, `FUTURE_TASKS.md` for the list, this file for the texture — the things that are not
visible from a diff and that would otherwise cost the next agent a day.

## 1. Gates you can trust, and what they do not prove

Reliable and fast on this machine (Windows, Git Bash):

```
gofmt -l internal cmd            # empty means formatted
go build ./internal/... ./cmd/... # type-checks the non-test tree
go vet  ./internal/... ./cmd/...  # ALSO compiles test files — a vet failure in a _test.go you did not touch is real
(cd web && npx tsc --noEmit)      # frontend types
```

Everything passed at `1cbe023`. **None of it runs a single assertion.** "Builds clean" is the weakest true statement
about this code, and it is the only statement P4–P7 currently deserve. `go test -race` needs
`CC="C:/tools/winlibs/mingw64/bin/gcc.exe"` on this host or it dies with a confusing linker error; it has not been run
since the P3 gate, so P4–P7 has never been race-checked.

## 2. Things that cost real time and will cost you time again

- **The module path is `github.com/codeplo/codeplo`, the repository directory is `codeploy`.** A new package importing
  `github.com/codeplo/codeploy/...` fails with `no required module provides package`, which reads exactly like a
  broken module cache. It is not.
- **Hand-written SQL that has never met a database.** Migrations 0006 and 0007 have never been applied anywhere — the
  code was written against the DDL by reading it. Every `INSERT`/`SELECT` in `previews`, `databases`, `monitor`,
  `notify`, `cron`, `git` was checked by eye against `internal/store/migrations/*.sql`, which is not a proof. Expect
  the first `goose up` (T1) to find something, and expect it to be a column name or a CHECK constraint, not a
  catastrophe.
- **SQLite ordering rule that the design leans on.** The DSN is
  `…?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate`
  with `SetMaxOpenConns(4)`. Writers serialise at `BEGIN`, which is what makes "claim the delivery row **last** in the
  transaction" correct: a duplicate post waits, fails on the unique index, and unwinds the preview work it had already
  done. If you change `_txlock` or the pool size, that reasoning needs re-deriving.
- **`gofmt` reflows whole struct blocks.** Adding one field to a struct (`Server.Preview`) or one column to a table
  makes `gofmt -l` flag the file for alignment, and the diff then looks like unrelated churn. Run `gofmt -w` and say
  so in the commit message rather than hand-aligning.
- **Edit anchors must be the committed wording, not your memory of it.** I lost a cycle trying to rewrite a
  `PROGRESS.md` row using a phrase I remembered ("rather than a recursive delete") when the file said "instead of a
  recursive delete". Read the exact line first.
- **A `git status` "M" can be CRLF-only noise** on this host. Check `git diff --numstat` before staging, or you will
  commit line-ending churn as a change.
- **Full-tree greps time out.** Scope them with a `glob` or a path, or they background themselves and you wait.
- **Pipes do not kill programs and a backgrounded pipe emits nothing until it ends.** A `cmd | grep` you background
  returns no output for a long time; that is not a hang, and its exit status is not a verdict. Read the grep output.
- **Inventing a helper instead of grepping for one.** I wrote `errExit` and `yn` into `cmd/codeplo/previews.go`
  believing they existed in the package; they did not. The conventions that do exist are `templatesCheck`'s counted
  `fmt.Errorf` for a non-zero exit, and `onOff`-style local formatters. Grep `cmd/codeplo` before adding a helper.
- **WSL is a flaky test host.** The distro tears down after ~8 minutes idle and wipes `/tmp`. Run each test sequence as
  **one** command, or hold it open with a background `wsl -d Ubuntu -u root -- sleep 900`, and put logs under `/root`.
  Root access is supplied by the operator per session and is **never** written into this repo.
- **Never `pkill -f <pattern>` when the pattern also appears in your own command line** — it kills your shell mid-run.
- `PYTHONUTF8=1` before any tool that writes non-ASCII JSON on Windows, or it dies on charmap mid-write. Git Bash
  `/tmp` is invisible to Windows Python **and** to the Read tool — use `$LOCALAPPDATA/Temp`.

## 3. Facts about the Docker daemon that changed code, not just comments

Measured on real Docker **29.1.3** (API 1.52, containerd 2.2.1, runc 1.3.4) in WSL:

- Closing a hijacked exec attach does **not** end the exec. Half-closing the write side does not either (a tty exec has
  no stdin pipe, so there is no EOF to read). `POST /exec/{id}/kill` is **not registered at any API version** on this
  daemon (404 at 1.24/1.41/1.52, while `/exec/{id}/resize` on the same prefix answers 200).
- What works is an in-band terminal gesture: `^U` then `^D` twice (`\x15\x04\x04`) — `^U` first so a half-typed command
  line is discarded instead of executed by your own teardown. `runtime.Docker.Close` does that, then half-closes, then
  still asks for kill and reports a missing kill route as *unsupported*, never as success. It is pinned by a stub
  Docker API server test (`docker_exec_test.go`) that answers 404 for kill exactly like this daemon does.
- Cancellation of a build is container lifecycle plus context. Do not design anything that assumes an exec can be
  interrupted.

## 4. Patterns this codebase uses consistently — keep them

- **A refusal leaves nothing written.** Every service decides first (parent, type, image mode, slug, hostname) and then
  inserts. If a unique index still catches something, that is returned as an **error that rolls the transaction back**,
  never as a polite refusal — a refusal after an insert commits a stopped resource that no row describes. `previews.Build`
  is the current model of this.
- **Write + audit row in one transaction** (`s.tx(r, func(tx) (audit.Event, error))`, `s.rec(...)`). Never audit after
  commit.
- **Nil dependency means "not available", never a fake success.** `Server.Edge`, `Server.Runtime`, `Server.Preview`,
  `deploy.Deps.GitHubToken`, `Deps.ReportStatus`, `Deps.PreDeploy` are all nil-able and each handler says so with a
  real message. `go vet` clean + nil deref caught by a test is the standard, not a panic.
- **Secrets are write-only and never readable.** Sealed at rest (`crypto.Seal`), copied between rows by re-inserting
  the same bytes (`previews` holds **no master key** and therefore cannot open anything), redacted from build/runtime
  logs, in the exec **environment** rather than argv for dumps, in an askpass/credential helper rather than a URL for
  git, in an ephemeral 0700 `DOCKER_CONFIG` for registries, in the **environment file** rather than a settings table for
  the GitHub App key and S3. Do not add a reveal endpoint, ever; there is deliberately no reveal permission.
- **New capability goes behind an existing seam, not a new route.** `api/openapi.yaml` is contract authority and
  `test/authz/routes_test.go` fails on any route the contract does not declare. That is why cron (`codeplo cron`),
  GitHub App (`codeplo github-app`), templates (`codeplo templates`), previews (`codeplo previews`) and the sweep
  (daemon loop) reached their features without touching the route set. `POST /templates/{key}/deploy` and
  `GET /templates` are the only P7 routes the contract allowed.
- **Daemon loops use `everyMinute(ctx, name, pass)`** from `cmd/codeplod/ops.go`: clock-aligned, 55-second bounded pass.
  Use it for anything minute-resolution (`expires_at` is minute-resolution); a loop that starts mid-minute evaluates a
  different set of rows than the code that wrote them.
- **Module flags gate behaviour, feature flags gate capability, and both are re-read every pass** so a settings change
  takes effect on the next tick rather than at the next restart.
- **The mock is the specification for messages.** Status codes, `fields` keys and exact wording in
  `web/src/mock/handlers/*.ts` are what the frontend asserts; divergences have to be deliberate and recorded in
  `docs/DECISIONS.md` (the Phase 5 divergences are the precedent).

## 5. Where I would push back if I were you

- **Do not let the next agent add routes casually.** For extensions and MCP that decision is contract-level, it
  re-runs `make gen`, the authz matrix and the mock parity tests, and it should be one deliberate commit with a written
  decision (T53). It is not a side effect of "implementing J4".
- **Do not treat a compile as a phase being finished.** `PROGRESS.md` says "implemented, compiling, **not tested**" for
  P4–P7 and that wording is load-bearing. If you finish a task in group B, change the phase header, do not leave it
  claiming a pass it does not have.
- **Never weaken or delete a test to make it pass.** Twice in this build the honest answer was "the assertion is right
  and the code was wrong" (the `Store.List` cursor skipping a row; the WebSocket bridge writing its goodbye after
  cancelling its context). A test that only passes against the current implementation is not a test.
- **Do not park a defect you can name.** If you can say "X returns 501" or "X's step-up rule disagrees with the
  contract", that is a task, not a note — group C exists because those were named and left.
- **A running service is shared state.** The daemon, Caddy, Docker and the panel's database on a test host belong to
  whoever is using them; stop and disable fixture units before anything probes `:80`/`:443`, and get explicit approval
  per action for anything destructive against a live or shared system. No force-push, no history rewrite, no bare
  `git stash`/`git reset --hard`.
- **Trust but verify a subagent's report.** Under parallel work, an agent's summary describes intent. `git diff`
  describes fact. When a model switch or a context reset kills an agent mid-flight its completion report can be
  fabricated — verify with the diff before accepting, and re-run the gate after merging parallel scopes.

## 6. What I would have done differently

Built T1 (the scratch-DB migration run) on day one of P4 instead of at the end of P7. Every later phase's SQL was
written against a schema that had never been applied, and the cheapest possible check — `goose up` into a temp file —
would have caught drift while there was only one migration to fix. Also: name the `codeplo previews` and
`codeplo templates` CLI readers *before* writing the service, because the reader is what tells you whether a `Status`
type contains facts or verdicts (the fix at `1cbe023` removed a verdict field for exactly that reason).
