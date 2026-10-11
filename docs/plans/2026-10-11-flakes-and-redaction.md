# Plan: two flaky tests and bugreport redaction (slug: flakes-and-redaction)

Base: `origin/main 19e25095` (verified `git rev-parse HEAD`). CLAUDE.md rules apply throughout: no history in code or tests (no issue numbers, test names say what they pin), comments say why only, functions ≤70 lines, non-test files ≤600 lines, no new lint exclusions, coverage baseline is not lowered (`sh scripts/check-coverage.sh --write` only if code moves packages, which this plan does not).

## 1. Findings with file:line evidence

**#1093 — test-only flake, production has no hazard.**
`internal/db/sync_exchange_test.go:408-410` (`drainUnderTwoRivals:338-412`) does the post-drain proof write with `exchangeWrite(failFast, ...)` where `failFast` is `openExchangeRival(t, path, 1ms)` (`:340, :557-565`, `BusyTimeout 20ms, BeginRetry 1ms`). Production writers use `d.Tx` with `beginRetryFor 30s` + `busy_timeout 5000ms` (`internal/db/db.go:31-36, :157-164, :369-425`, retry loop `:403-425`, busy error `db: tx begin: %w` `:422`). `DrainOutbox` itself is one `Tx` holding the write lock across entry read + row reads (`internal/db/sync_exchange.go:302-310, :312-341`). A 1ms-retry handle gives up after ~2 attempts (first busy, `25+rand76ms` sleep `:424`, second busy exceeds window) while a production handle would wait out the lock. The in-drain fail-fast check (`:377-387`, expects `ErrBusy` + body never ran) correctly pins the lock; the after-drain write misuses the same fail-fast handle for a liveness proof. No change to `internal/db/db.go` or `internal/db/sync_exchange.go` is planned. The existing `20ms` sleep to let samplers reach the lock (`:394`) is also non-deterministic; the deterministic gate below replaces it.

**#1086 — test-only race, client contract intends reply-may-win.**
`internal/db/wire/client/verb_test.go:330-351` races an immediate reply against cancel: `scriptedVerbOwner.serve` (`:50-125`) writes `SyncResult{OK:true}` (`:119-124`) right after closing `ready` (`:90`), while the test goroutine does `<-ready; cancel()` (`:336-339`). When the client's `Read` (`internal/db/wire/client/session.go:167`) wins before `cancel()`, `await`'s `select` takes the reply branch with `ctx.Err()==nil` (`:176-185`) and `SyncVerb` returns OK (`:400-406`) — the observed `verb_test.go:346` failure. The contract prefers cancellation only when it already happened (`await :178-180` returns `ctx.Err()` if set on reply arrival; `SyncVerb :389-398` marks a post-write cancel as `ErrAwaitingReply` + context error). A reply completed before cancel may therefore win; the client needs no change. Fix the test to block the server until after cancel. No change to `internal/db/wire/client/session.go` or `client.go` is planned.

**#1101 (part) — partial git-identity redaction confirmed.**
Doctor renders `Detail: Name + " <" + Email + ">"` (`internal/doctor/identity.go:26`, projected by `internal/bugreport/project.go:113-131`). `Redactor.Text` (`internal/bugreport/redact.go:40-49`) only knows `Home/User/Host` and replaces `User` by whole-token (`replaceToken :143-167`, token bytes `:130-138` exclude `. @ < >`). An email `user.surname@domain` with `User=user` therefore becomes `<user>.surname@domain`: the `user` run matches, `.`/`@` split the rest, surname + domain leak; the display name (`First Last` vs `USER=first`) misses entirely due to case/whole-string mismatch. `Redactor` has no `GitName/GitEmail` fields (`:22-27`), and `runBugreport` never supplies git identity (`cmd/relevo/bugreport.go:155-157`), though the same process already gathers it for doctor (`cmd/relevo/doctor.go:334-344` via `internal/git/repo.go:48-73`). Fix in `internal/bugreport` + populate in `cmd/relevo/bugreport.go`.

## 2. Seams

- `internal/db/sync_exchange_test.go`: `TestOutboxDrainIsOneSnapshot :308-330`, `drainUnderTwoRivals :338-412` (fail-fast rival `:340`, samplers `:346-349`, hold/release/stop/drainDone `:351-400`, post-drain write `:405-410`), `spinRivals :419-437`, `openExchangeRival :557-565`, `exchangeWrite :567-573`, `openExchangeDB :543-552`.
- `internal/db/sync_exchange.go`: `DrainOutbox :302-341`, `DeleteDrainedOutbox :384-396` (read-only evidence, not touched).
- `internal/db/db.go`: `Tx :373-438`, `mapBusy :468-482`, `Options :89-104` (read-only evidence, not touched).
- `internal/db/wire/client/verb_test.go`: `scriptedVerbOwner :28-125` (`serve :50-125`, `verbSocket :127-134`), `TestClientSyncVerbHonoursACancelledCaller :330-351`, `TestClientSyncVerbMarksADeliveredRequestAwaitingItsReply :397-440` (reference for `silent:true` + `ErrAwaitingReply`).
- `internal/db/wire/client/session.go`: `await :159-196`, `SyncVerb :380-414`, `cancelGrace :45-50` (read-only evidence, not touched).
- `internal/bugreport/redact.go`: `Redactor :22-27`, `NewRedactor :32-36`, `Text :40-49`, `Redact/section :54-97`, `privacy :102-112`, `replaceToken/tokenByte :128-167`, `remoteRe/redactRemotes :169-181`, `secretRe/runRe/redactSecrets :185-219` (251 lines total, stays ≤600).
- `internal/bugreport/project.go`: `DoctorDoc/DoctorCheck/DoctorSection :89-131` (read-only evidence, not touched).
- `internal/bugreport/redact_test.go`: `baseRedactor :12`, `redactCases :16-205`, `TestRedactRules :210-218`, `TestRedactMarksItsWork :223-245`, `TestRedactBundleRewritesEveryString :250-271`.
- `internal/bugreport/fixture_test.go`: `fixtureRedactor :82-84`, `fixtureDoctor :100-112`.
- `cmd/relevo/bugreport.go`: `runBugreport :126-167` (`NewRedactor :155`, `Redact :157`), `bugreportSources` wiring in `cmd/relevo/bugreport_sources.go:116-136, :176-212` (read-only except the one population site).
- `cmd/relevo/doctor.go:334-344`, `internal/doctor/identity.go:17-43`, `internal/git/repo.go:48-73` (identity source, read-only evidence, not touched).

## 3. Scope (builder may touch only these)

- `internal/db/sync_exchange_test.go`
- `internal/db/wire/client/verb_test.go`
- `internal/bugreport/redact.go`
- `internal/bugreport/redact_test.go`
- `cmd/relevo/bugreport.go` (populate the redactor only; no new `cmd/relevo` tests — see step 6)
- `docs/plans/2026-10-11-flakes-and-redaction.md` (this plan, committed per step 9)
- Conditionally `internal/bugreport/testdata/bundle.golden.md` only if the new redaction changes fixture rendering (fixtures carry no git identity, so expect no change; any diff must be justified in the report, never hand-edited to green).
- Must not touch: `internal/sync` (non-test), `cmd/relevo/db_sync.go`, `internal/relevo/*`, `internal/serve`, `internal/remote`, `cmd/relevo/daemon.go`, nor production `internal/db/db.go`, `internal/db/sync_exchange.go`, `internal/db/wire/client/session.go`, `internal/db/wire/client/client.go` (no production hazard per §1; any claim otherwise halts the round with evidence).

## 4. Ordered steps (one line each: deliverable + how to know it worked)

1. Deterministic #1093 repro hook in `internal/db/sync_exchange_test.go` (rival-hold gate + sampler-first-attempt signal replacing the `20ms` sleep) — fails with `db: tx begin: busy` on the fail-fast handle before the fix, passes on signal ordering alone.
2. #1093 fix in `internal/db/sync_exchange_test.go` (post-drain proof write uses a production-retry handle, in-drain fail-fast assertion unchanged) — `go test ./internal/db -run 'TestOutboxDrainIsOneSnapshot|TestDrainOutboxIsOneTransaction|TestOutboxDrainIsOrderedAndDeletable' -count=1` green plus mutation (restore fail-fast handle for the post-drain write) red.
3. #1093 race evidence via `go test ./internal/db -run 'TestOutboxDrainIsOneSnapshot|TestDrainOutboxIsOneTransaction' -count=50 -race` — zero failures, reported in the round report.
4. Deterministic #1086 repro in `internal/db/wire/client/verb_test.go` (cancelled-caller test blocks the fake owner until after cancel via the existing `silent` hold, no sleeps) — fails before the fix by reporting OK, deterministically returns `context.Canceled` after.
5. #1086 fix in `internal/db/wire/client/verb_test.go` (cancelled-caller test uses the holding owner; client untouched per contract §1) — `go test ./internal/db/wire/client -run 'TestClientSyncVerbHonoursACancelledCaller|TestClientSyncVerbMarksADeliveredRequestAwaitingItsReply' -count=1` green plus mutation (drop the `ctx.Err()`-on-reply preference in `await`) red, then `go test ./internal/db/wire/client -count=50 -race` clean and reported.
6. Git-identity redaction in `internal/bugreport/redact.go` (redactor carries the exact git name/email and `Text` removes each whole occurrence wherever the bundle prints it, ordered before the substring user/host pass) plus population in `cmd/relevo/bugreport.go` (same identity source doctor uses) — new `internal/bugreport/redact_test.go` rows (whole `Name <email>` masked, surname/domain gone, fix-example and project URL untouched) green plus mutations (revert to substring-only user replacement; empty the new identity fields at the call site) red; tests live in `internal/bugreport`, never `cmd/relevo`, because CI runners have no harness binary and no network and a `cmd/relevo` test must not spawn a harness or reach the network.
7. Focused suites `go test ./internal/bugreport -count=1` and `go test ./internal/db ./internal/db/wire/client -count=1` — all green with no function over 70 lines and no non-test file over 600 lines.
8. Full verification `make check` — passes with no new lint exclusions and no coverage-baseline lowering.
9. Save this plan as `docs/plans/2026-10-11-flakes-and-redaction.md` and commit code plus plan as three new commits on top of the base (one per issue, messages naming the issue with `Fixes #N`), never amending or rebasing a commit already on a remote binding's branch.

## 5. What is deleted (closed list)

1. The post-drain liveness proof using a 1ms-retry handle (replaced by a production-retry write; the in-drain `ErrBusy` proof is kept).
2. The `20ms` sleep synchronising samplers with the drain (replaced by a first-attempt signal gate).
3. The immediate-reply cancelled-caller arrangement that let a reply win before cancel (replaced by a server that holds until after cancel; the OK-result and refusal paths are kept).
4. The partial git-identity rendering (`<user>.surname@domain` plus unmasked display name) wherever the bundle prints it (replaced by whole name/email removal; the `<user>`/`<host>`/`<remote>`/`<redacted>` placeholders and the short-name guard are kept).

## 6. Report must include

Focused commands and their results, `go test -count=50 -race` results for `internal/db` and `internal/db/wire/client`, the exact mutation per fix and its kill result, `make check` result, `git diff --stat` against the §3 scope, whether `testdata/bundle.golden.md` changed and why, and any halt with file:line evidence if a premise proves wrong on current main.

## Round 2 decision

- #1093 is test-local: production opens one handle per database path (the flock in `internal/db/handles.go`), so the test closes every rival handle before the post-drain proof write and makes that write on a default handle; the sleep is replaced by a sampler-first-attempt signal. The in-drain `ErrBusy` assertion is unchanged and no production file in `internal/db` changes.
- #1086: the server holds until after cancel; the client is untouched.
- Redaction also removes the git identity by value in every bundle section.
- `TestDrainOutboxIsOneTransaction` is out of scope.
