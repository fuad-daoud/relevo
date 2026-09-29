# Cleanup P2-4 -- extract `internal/consult` (one-shot consults and the verify consult)

Phase 2 of the codebase cleanup (spec `docs/specs/2026-09-25-codebase-cleanup-design.md`,
§5 "consult/"). Base: `main` at `3cf2ad3f`.

## 0. Rules

- **Run every command in the foreground and wait for it.** Never a background
  task, `&`, a scheduled wakeup, or "I'll wait for the notification": you are a
  headless process; when you end your turn the process exits and the round is
  lost. Do not end your turn until the report and done marker exist.
- **No behaviour change.** Declarations move and are requalified; names change
  only where this plan says. Stored formats (KV keys, JSON, DB rows) and every
  golden / `*contract_test.go` stay byte-identical.
- **golangci-lint v2.14.0 must actually run** (install with
  `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0` if
  `golangci-lint version` differs); use `--allow-parallel-runners`.
- **Touch only the files the plan names.** CI configuration, `scripts/`
  (other than the lint allow-lists), `Makefile` and `.github/` are out of scope
  even if a move seems to call for them; if one does, stop and report.
- **Keep every `t.Parallel()` call.**
- **Do not rebase or fetch.** This tree may have no `origin`; the planner rebases.
- Script mechanical requalification (`gofmt -w -r 'old.X -> new.Y' <files>`, then
  `~/go/bin/goimports -w`), then fix what the build reports.
- New packages are born clean: no lint exclusion, no allow-list entry; their
  comments follow CLAUDE.md "### Code style" (1-3 line package comment; *why*
  only; no issue numbers, `§` or history; no restating names); functions at most
  70 lines. Rewriting moved comments to that standard is allowed.
- Coverage: code moves between packages, so after `make check` run
  `sh scripts/check-coverage.sh --write`, then restore every line for a package
  the round did not touch. Report each changed line.
- If a step is impossible as written, or the code contradicts this plan, stop
  and report. Never add a lint exclusion, `//nolint` or allow-list entry.

## 1. Prerequisite: the report-tail parser becomes a leaf

`internal/relevo/reporttail.go` imports only the standard library. Move it whole
to a new package `internal/reporttail` (with `reporttail_test.go`), names
unchanged except drop the stutter: `ParseReportTail` -> `reporttail.Parse` (and
any other `ReportTail*`-prefixed exported name the same way; list them).
Requalify relevo's callers. consult uses its fence helpers
(`splitFenceLines`, `findRelevoBlock`, `unquoteScalar`, `parseListValue`):
export the ones consult needs.

## 2. What moves to `internal/consult`

From `ask.go`, `consult.go`, `verify.go`:
- the prompts and constants (`consultHeadlessPrompt`, `roundRole`,
  `roundAskPrompt`, `inlineAskMax`, `consultSpawnTimeout`, `consultTimeout`,
  `verifyRole`, the verdict constants, `verifyPrompt`);
- `askInlineBlock`, `inlinePrompt`, `reserveConsult`, `recordConsult`,
  `runningConsults` (exported as `Running`, status.go uses it), `consultCap`,
  `randomConsultID`, `strandError`;
- `reconcileConsults` (exported `Reconcile`), `finishConsult`;
- `verifyDiffCommand`, `verifyQuestion`, `parseVerdict`, `removeVerifyWorktree`,
  and the spawn-and-record half of `startVerifyConsult` (exported `StartVerify`);
- the error sentinels `ErrNotAConsultRole`, `ErrTreelessUnsupported`,
  `ErrConsultCap` -- keep `errors.Is` identity working for every caller
  (cmd/relevo, mcp); check how cmd/relevo maps them and update.

**Stays in relevo:** `Ask`, `AskOptions`, `AskResult` and `askRound`'s
resolution phase (planner check via `resolveVerbPlanner`, role and candidate
resolution via `resolveRole` / `Resolution`, `Gates`, tier resolution and the
tier cap, `spawn.HeadlessLaunch`, `roundSession`). After resolving, `Ask`
calls into consult with an already-resolved candidate, launch spec and tier
(for example `consult.Spawn(ctx, d, consult.Request{...})`) -- design that one
entry point from what `reserveConsult`/`askRound`'s spawn half need. `verifyTier`
stays in relevo; `StartVerify` receives the resolved reviewer through a func in
Deps (`ResolveReviewer func() (candidate.Candidate, harness.RoleSpec, harness.Tier, error)`),
which relevo builds from `resolveRole`, `RoleRegistry().Spec` and `verifyTier`.

## 3. Dependencies

`consult.Deps{Store *store.Store; Runner spawn.Runner; Git <narrow interface:
HeadCommit, AddDetachedWorktree, RemoveWorktree -- whichever the moved code
calls>; Now func() time.Time; NewID func() string; Seen func(pid int, st int64);
LostToRestart func(pid int, st int64) bool; Scope func(kind, unit string)
*spawn.ScopeSpec; Usage <func the moved code needs for recordUsage>;
Queue via internal/delivery directly (it is a leaf); ResolveReviewer as above}`.
Build it in relevo with one unexported `consultDeps(rt)` (nil-interface care as
in `captureDeps`). Callers: `headless.go` (`startVerifyConsult` ->
`consult.StartVerify`), `reconcile.go` (`reconcileConsults` ->
`consult.Reconcile`), `status.go` (`runningConsults` -> `consult.Running`),
cmd/relevo/ask.go (`relevo.Ask` unchanged).

## 4. Tests

Unit tests move with their code: `TestVerifyQuestionNamesEveryFile`,
`TestVerifyDiffCommand`, `TestParseVerdict`, `TestInlinePrompt`, `TestStrand*`.
Every other ask/consult/verify test is an integration test through relevo
(`Ask`, `Reconcile`, headless round close) and **stays in relevo**, using the
exported consult names where it referenced a moved private one (`verifyRole`
-> `consult.VerifyRole` etc.; export only what those tests need).

## 5. Steps

1. §1; `go build ./... && go test ./internal/reporttail/... -count=1`.
2. §2 + §3; `go build ./... && go vet ./...` (consult imports neither
   `internal/relevo` nor anything that imports it).
3. §4; `go test ./internal/consult/... ./internal/relevo/... ./cmd/relevo/... ./internal/mcp/... -count=1`.
4. `golangci-lint run --allow-parallel-runners ./...` 0 issues;
   `sh scripts/check-comments.sh`, `sh scripts/check-filesize.sh` ok.
5. Coverage (§0); `make check` (foreground) passes.
6. Copy this plan to `docs/plans/2026-09-26-cleanup-p2-consult.md`; commit once.

Report: the consult and reporttail APIs, the Ask/consult seam you chose and
why, what stayed in relevo, error-sentinel handling, tests moved vs stayed,
coverage lines changed.
