# Round diff and consult findings go straight into round_file

Date: 2026-09-24. Base: origin/main b66c6fcc (#441).
Follows the file-writes spike (docs/specs/2026-09-24-file-writes-spike.md, §6 B1).
There is one round in this plan. `builder.log`, `NNN-<id>-ask.md`, `NNN-plan.md` and
`NNN-review-plan.md` are **out of scope**. Do not touch them.

**Stop rather than improvise.** If a step is impossible as written, if the code contradicts
a fact stated here, or if a test outside the fenced list (§7.3) fails, halt and report
what you found. Do not bend a test to make it pass.

## 1. System overview

relevo writes two round artifacts to `$STATE/<name>/` only for itself: the round's captured
patch, `NNN-diff.patch`, and a consult's findings, `NNN-<id>-findings.md`. The daemon later
seals both into the `round_file` table and deletes them. Every relevo reader already reads
them through `Store.ReadFile` or `Store.StatFile`, and both fall back to `round_file`.

This round makes both writers call `Tx.PutRoundFile`, as drift already does
(`internal/relevo/drift.go:63-66`). The two files then never touch disk.

There are two readers outside relevo, and this round replaces each with something that
needs no file:

- **The verify reviewer** is handed the diff's path in its question (`verify.go:268-271`).
  It gets a git command instead: `git diff <baseline-tree> <closed-tree>`. Both trees are
  objects in the repository's shared object store, so the command works inside the
  reviewer's throwaway worktree. It also covers diffs over the 4 MiB cap, which today
  have no file at all.
- **`relevo ask`** prints `findings will appear at: <path>` (`cmd/relevo/main.go:1912-1919`).
  It prints the `relevo show … --findings <id>` command instead.

What stays the same:

- `LogEntry.Path` of a diff or findings entry stays the canonical path
  (`Store.DiffPath` / `Consult.FindingsPath`). The path is now a key that
  `Store.ReadFile` resolves from `round_file`.
- `push.go` `findingsIDOf` and `logRef` keep working, because they parse only the basename.

## 2. File structure

Only existing files are modified. No new files except this plan.

```
internal/relevo/capture.go       CaptureRoundDiff takes tx; PutRoundFile replaces os.WriteFile
internal/relevo/reconcile.go     queueReport passes tx to CaptureRoundDiff
internal/relevo/remote.go        catchUp stores the downloaded diff with PutRoundFile
internal/relevo/verify.go        verifyDiffCommand (new, pure); verifyQuestion/startVerifyConsult take the diff command
internal/relevo/headless.go      markerClose computes the diff command from the pre-close baseline
internal/relevo/consult.go       findings via PutRoundFile
internal/relevo/push.go          findingsCommand -> FindingsCommand (exported, 5 uses)
internal/relevo/ask.go           AskResult comment only
cmd/relevo/main.go               cmdAsk prints the show command, not the path
internal/store/store.go          DiffPath / FindingsPath doc comments
internal/store/types.go          Consult.FindingsPath, Verdict.Findings doc comments
internal/store/roundfile_put.go  doc comment: list diff and findings beside drift
README.md                        consult findings passages (~1902-1912, ~2089)
tests: see §7.3
```

## 3. Data structures

There are no schema, struct-field or JSON changes. `store/testdata/binding-shape.golden`
must not change. If it does, halt.

- `DiffResult` (`capture.go:18-30`): same fields. `Path` is still set only when a patch was
  stored, and it is still `Store.DiffPath(name, round)`.
- `Consult.FindingsPath` (`store/types.go:623`): same field and value. Its meaning changes
  from "file on disk" to "round_file key read through Store.ReadFile".
- `AskResult` (`ask.go:68-80`): unchanged. Fix its comment: the CLI prints a show
  command, not a path.

## 4. Interfaces and contracts

### 4.1 `CaptureRoundDiff` (internal/relevo/capture.go:158-196)

The new signature mirrors `CaptureDrift`:
`func CaptureRoundDiff(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding) DiffResult`

- Replace the write at `capture.go:190-195`,
  `os.WriteFile(patchPath, diff.Patch, 0o644)`,
  with `tx.PutRoundFile(b.Name, b.Round, patchPath, diff.Patch)`.
- On error, return exactly what the write failure returns today:
  `DiffResult{Available: false, Reason: brief(err), EndTree: end}`.
- Everything above line 190 is unchanged: the guards, snapshot, empty skip and truncated
  skip.
- Postconditions:
  - When a patch was stored, `rt.Store.ReadFile(Store.DiffPath(name, round))` returns it.
  - Nothing exists on disk at that path.
  - An empty or truncated diff stores nothing.
- Update the doc comment above the function to say it stores a round_file row.
- If `os` becomes unused in capture.go, drop the import.
- Caller: `reconcile.go:436` in `queueReport`, which already has `tx`. Change it to
  `CaptureRoundDiff(ctx, rt, tx, b)`. There are no other non-test callers.

### 4.2 `catchUp` diff download (internal/relevo/remote.go:1110-1127)

- Replace `writeTempAndRename(rt.Store.DiffPath(name, n), rcDiff)` with:
  1. Read `rcDiff` into memory through `io.LimitReader(rcDiff, git.DefaultMaxPatchBytes+1)`.
     The constant is at `internal/git/types.go:36`. Import `internal/git` if remote.go does
     not already.
  2. If the read errors: `slog.Warn("read diff failed", …)` and `return b, nil`. This is
     the same outcome as today's write failure.
  3. If more than `git.DefaultMaxPatchBytes` bytes were read: `slog.Warn("remote diff over
     cap; not stored", …)`. Leave `diffDownloaded` false and continue. The server never
     serves a diff above its own cap, so this is a guard, not a path.
  4. Otherwise `tx.PutRoundFile(name, n, rt.Store.DiffPath(name, n), body)`. On error:
     `slog.Warn("store diff failed", …)` and `return b, nil`. On success,
     `diffDownloaded = true`.
- Lines 1262-1274 (the diff log entry) are unchanged: `Path` is still `DiffPath(name, n)`
  when a diff was stored.
- Report, log and stream downloads (`writeTempAndRename` for those kinds) are **unchanged**.

### 4.3 Verify: the reviewer gets a git command (verify.go, headless.go)

- **New pure function** in verify.go:
  `func verifyDiffCommand(baselineTree, closedTree string) string`
  - Returns "none" when either argument is "".
  - Otherwise returns exactly `"git diff " + baselineTree + " " + closedTree`.
  - Its doc comment says: both are tree ids from the round's snapshots; the objects live in
    the repository's shared object store, so the command runs in the verify worktree; and
    it shows uncommitted edits the builder left.
- `verifyQuestion` (verify.go:53-58): rename the parameter `diffPath` to `diff`. Formatting
  is unchanged.
- `verifyPrompt` (verify.go:35-47):
  - Keep the `Diff:   %s` field.
  - Add one sentence after the Plan/Report/Diff/Gate block: `The Diff line is a git
    command: run it in this worktree to see the round's change, including edits the
    builder did not commit.`
  - Nothing else in the prompt changes.
- `startVerifyConsult` (verify.go:196): add a parameter after `round`. The new signature is
  `startVerifyConsult(ctx, rt, tx, b, round int, diff string, gateLog string)`.
  At verify.go:268-271, pass `diff` where `rt.Store.DiffPath(b.Name, round)` is today.
- `markerClose` (headless.go:842-870):
  - **Before** calling `closeOnMarker`, capture `base := b.RoundBaselineTree`.
    `queueReport` clears `RoundBaselineTree` on the binding it returns
    (reconcile.go:553-555), so the value must be read from the pre-close `b`.
  - At the call on headless.go:866, pass
    `verifyDiffCommand(base, next.RoundClosedTree)`.
  - There is exactly one non-test caller.
- **Fact to confirm before editing:** `closeOnMarker` returns `closed == true` only on the
  tick where `queueReport` ran. That means `b.RoundBaselineTree` at markerClose's entry is
  the round's baseline, including on the tick after a gate passes.
  - If a close path reaches `startVerifyConsult` with the baseline already cleared on
    `b`, the reviewer would read `Diff: none`. That is a regression. Halt and report it.
  - The new test N3 pins the plain close.

### 4.4 Consult findings (internal/relevo/consult.go:156-165)

- Replace `os.WriteFile(c.FindingsPath, []byte(text+"\n"), 0o644)` with
  `tx.PutRoundFile(b.Name, c.Round, c.FindingsPath, []byte(text+"\n"))`.
- The round argument is `c.Round`, **not** `b.Round`: the binding may have advanced since
  the consult started.
- Error: keep the existing `ConsultSilent` branch. Change the note text from
  `"could not write findings: "` to `"could not record findings: "`.
- Update the comment above it: relevo records the final message as the round file the
  planner is sent to.
- The completion decision (Alive, then exit trailer, then non-empty FinalText) is
  unchanged. Nothing stats the findings file today, and nothing may start to.
- If `os` becomes unused in consult.go, drop the import.

### 4.5 `relevo ask` output (cmd/relevo/main.go, cmdAsk ~1862-1925; re-grep, lines move)

- Rename `findingsCommand` to `FindingsCommand` in internal/relevo/push.go (5 occurrences:
  push.go and consult.go). Keep its doc comment, adjusted to the exported name.
- Round ask (~1912): replace `\nfindings will appear at: %s\n` / `res.Consult.FindingsPath`
  with `\nfindings: %s\n` /
  `relevo.FindingsCommand(res.Binding, res.Consult.Round, res.Consult.ID)`.
- Role ask (~1918): the same replacement.
- Everything else printed there is unchanged: gated note, pick line.

### 4.6 Comments and README (documentation only)

- `store.go` DiffPath (~214):
  "DiffPath names a round's captured patch. It is a round_file key: relevo stores the
  patch with Tx.PutRoundFile and never writes it to disk; read it with Store.ReadFile."
- `store.go` FindingsPath (~282):
  - Replace the stale "Its existence is the entire completion gate" text.
  - New text: "FindingsPath names a consult's findings: relevo records the consult's final
    message under it with Tx.PutRoundFile; it is never a file on disk. Read it with
    Store.ReadFile."
- `store/types.go:623` Consult.FindingsPath and `:553-555` Verdict.Findings: say "round
  file key", not "file".
- `store/roundfile_put.go` doc (lines ~10-13): "today the drift patch" becomes "the drift
  patch, the round's diff patch and a consult's findings".
- `README.md` ~1902-1912:
  - The passage still claims the consult writes a file and replies with a path, and that
    the file's existence is the completion gate. Both are already false.
  - Rewrite it as follows, keeping the surrounding prose:
    - the consult's final message is the findings;
    - relevo records them as `NNN-<id>-findings.md` in its database;
    - `relevo ask` prints the `relevo show <name> --round N --findings <id>` command that
      shows them;
    - they are queued to the planner like any other report.
- `README.md` ~2089: "which relevo writes to the usual `NNN-<id>-findings.md`" becomes
  "which relevo records as the usual `NNN-<id>-findings.md`".
- Do not edit docs/specs, docs/plans (except this file), CLAUDE.md, or
  `internal/harness/agents/*.md`. The reviewer role files still say "write findings to a
  path". That is a known stale instruction left for a later round; list it under `not_done`.

## 5. Pseudocode

```
queueReport(tx, b):                                   # reconcile.go:376
  if no KindDiff entry for b.Round:
    result = CaptureRoundDiff(ctx, rt, tx, b)
      ... snapshot, diff (unchanged)
      if empty or truncated: return without storing
      tx.PutRoundFile(b.Name, b.Round, DiffPath, patch)  -> error: Available=false, Reason
    append KindDiff entry {Path: result.Path, ...}       # unchanged

markerClose(tx, b):                                   # headless.go:842
  base = b.RoundBaselineTree                          # read BEFORE the close clears it
  next, closed, gating, rec = closeOnMarker(...)      # runs queueReport
  ...
  if wantVerify:
    startVerifyConsult(..., next, closedRound, verifyDiffCommand(base, next.RoundClosedTree), gateLogPath)
      question = verifyQuestion(name, round, planPath, reportPath, diffCmd, gateLog)
      write askPath (unchanged; ask.md stays a file)

catchUp(tx, b, n):                                    # remote.go:1079
  rc = Remote.RoundFile(n, "diff")
  404 -> no diff; other error -> warn, return
  body = readAll(limit cap+1); err -> warn, return; over cap -> warn, no diff
  tx.PutRoundFile(name, n, DiffPath(name,n), body); err -> warn, return
  diffDownloaded = true

reconcileConsults(tx, b):                             # consult.go
  ... exited with trailer, text = FinalText(stream)
  text == "" -> Silent (unchanged)
  tx.PutRoundFile(b.Name, c.Round, c.FindingsPath, text+"\n")
     err -> Silent "could not record findings: ..."
  finishConsult(Done)                                 # unchanged; reads findings via Store.ReadFile
```

## 6. Error handling

- A `PutRoundFile` failure has the same outcome as today's `os.WriteFile` failure at each
  site:
  - diff: `Available=false` with the reason, and the round still closes;
  - remote: warn and return, and the next tick retries;
  - findings: the consult goes Silent with a note.
- No new error types.
- `PutRoundFile` validation errors (wrong dir, round mismatch, no live record) are
  programming errors. Every call here passes the Store's own path helper for the same
  name and round, so none should occur. A test that trips one means a fixture uses a
  foreign path. See §7.3 F1.

## 7. Ordered implementation steps

### 7.0 Working efficiently

- Read these in one parallel batch, at the line ranges above, and do not re-search for
  what this plan locates:
  - capture.go:150-200
  - reconcile.go:430-475
  - remote.go:1105-1130
  - verify.go:30-60 and 190-340
  - headless.go:835-880
  - consult.go:125-175
  - push.go:15-66
  - main.go (grep `findings will appear`)
  - store.go:210-290
  - roundfile_put.go:1-60
- Make every change to one file in one edit call.
- Rename `findingsCommand` with one scripted edit:
  `sed -i 's/\bfindingsCommand\b/FindingsCommand/g' internal/relevo/push.go internal/relevo/consult.go`.
- Iterate with the focused command
  `go test ./internal/relevo/ -run 'CaptureRoundDiff|Verify|Consult|CatchUp|AskRound|Reconcile|Seal' -count=1`,
  fixing every reported error before the next run.
- Then `go test ./internal/store/ ./internal/serve/ ./cmd/relevo/ -count=1`.
- Run `make check` once at the end. Also run `make e2e` once.
- Test rule for this repo: a cmd/relevo test must not run a subcommand that spawns a
  harness or reaches the network. So there is **no** CLI test for the `relevo ask` output
  change (cmdAsk spawns a consult). The rename is covered by the relevo package tests.

### 7.1 Steps

1. **Diff capture writes a row.**
   - Change: §4.1, plus `reconcile.go:436`.
   - Port the capture_test.go call sites (F-D1).
   - Done when the focused capture and reconcile tests pass.
2. **Remote diff writes a row.**
   - Change: §4.2.
   - Extend `TestCatchUpWritesDiffEntryFromView` (F-D3).
   - Done when it passes.
3. **Verify gets a git command.**
   - Change: §4.3.
   - Add N2 and N3, and port `TestVerifyQuestionNamesEveryFile` (F-D2).
   - Done when the verify tests pass, including `TestVerifyRoundStartsOnHeadlessClose`
     unchanged.
4. **Findings write a row.**
   - Change: §4.4.
   - Port the consult and ask tests (F1-F4).
   - Done when the consult and ask tests pass.
5. **Ask output and rename.**
   - Change: §4.5.
   - Done when the build is clean.
6. **Comments and README.**
   - Change: §4.6.
7. **Full check and mutations.**
   - Run `make check` and `make e2e`.
   - Run the mutation checks in §7.4, one at a time, restoring after each, and report
     each result.
8. **Commit.**
   - One commit: `refactor(diff,consult): the round diff and consult findings go straight
     into round_file; verify gets a git diff command`.
   - Push the branch and open a PR against main. Put the §7.4 mutation results in the PR
     body.

### 7.2 New tests

- **N2 `TestVerifyDiffCommand`** (verify_test.go):
  - table cases:
    - ("tree-a", "tree-b") gives "git diff tree-a tree-b";
    - ("", "tree-b") gives "none";
    - ("tree-a", "") gives "none".
- **N3 `TestVerifyRoundHandsTheReviewerAGitDiff`** (headless_test.go, next to
  `TestVerifyRoundStartsOnHeadlessClose` at ~919). Build it as a copy of that test's setup:
  - set `b.RoundBaselineTree = "tree-base"` before `Save`;
  - set `fakeGit{headCommitID: "head1", snapshotTreeID: "tree-end", diffResult: git.Diff{Stat: git.Stat{FilesChanged: 1, Insertions: 1}, Patch: []byte("PATCH\n")}}`.
  - After the reconcile, assert:
    - (a) the verify consult's question, read with `rt.Store.ReadFile(consult.AskPath)`
      (the ask may already be sealed or still on disk; ReadFile handles both), contains
      `git diff tree-base tree-end`, and does not contain `diff.patch`;
    - (b) `os.Stat(rt.Store.DiffPath("webshop", 1))` is not-exist;
    - (c) `rt.Store.ReadFile(rt.Store.DiffPath("webshop", 1))` returns `PATCH\n`.
  - If the fixture shape makes (c) impossible, for example because the fakeGit field names
    differ, adapt the field names only and say so in the report.

### 7.3 Fenced test list: the only existing tests you may change

Any test failing that is not in this list means halt and report. Do not port it.

- **F-D1** `internal/relevo/capture_test.go`:
  - Every `CaptureRoundDiff(ctx, rt, b)` call (lines 21, 37, 53, 76, 105, 142, 223, 351)
    is wrapped in `s.WithLock(func(tx *store.Tx) error { res = CaptureRoundDiff(ctx, rt, tx, b); return nil })`.
    This is the drift_test.go:291-297 pattern. The variable holding the Store may be named
    `s` or `rt.Store`.
  - Assertion ports:
    - `TestCaptureRoundDiff_NormalDiff` (~146-157): replace `os.ReadFile(expectedPath)`
      with `s.ReadFile(expectedPath)`, and add `os.Stat(expectedPath)` must be not-exist.
    - `TestCaptureRoundDiff_EmptyDiff` (~83) and `TestCaptureRoundDiff_TruncatedDiff`
      (~115): keep the not-exist stat, and add `s.ReadFile(s.DiffPath(...))` must return
      an error (no row either).
- **F-D2** `internal/relevo/verify_test.go` `TestVerifyQuestionNamesEveryFile` (~22-47):
  - The diff argument becomes `"git diff tree-a tree-b"`, and that string is the expected
    substring in place of `"/state/webshop/001-diff.patch"`.
  - Rename the doc comment's "the diff … paths" to "the diff command".
- **F-D3** `internal/relevo/remote_test.go` `TestCatchUpWritesDiffEntryFromView` (~3530-3600):
  - Add assertions: `os.Stat(st.DiffPath("api", 1))` is not-exist, and
    `st.ReadFile(st.DiffPath("api", 1))` returns the body the fake serves for `"diff"`.
  - If the fixture has no live record for "api", PutRoundFile fails and the diff entry
    disappears. In that case halt; do not seed a record.
- **F1** `internal/relevo/consult_test.go` `seedSpawning` (~120-140):
  - The `FindingsPath` literal `/repo/.relevo/consults/7f2a3c1d-findings.md` becomes
    `rt.Store.FindingsPath("webshop", 1, "7f2a3c1d")`.
  - Leave `AskPath` alone.
  - Leave `TestReconcileAbandonsLegacyPaneConsult`'s own literal (~217) alone unless it
    fails.
- **F2** `consult_test.go` `TestHeadlessConsultFinalMessageBecomesFindings` (~270-300):
  - Replace `os.ReadFile(c.FindingsPath)` with `rt.Store.ReadFile(c.FindingsPath)`.
  - Add `os.Stat(c.FindingsPath)` must be not-exist.
  - The comment "Deleting the WriteFile" becomes "Deleting the PutRoundFile".
- **F3** `consult_test.go` `TestHeadlessConsultExitWithoutTextIsSilent` (~330) and
  `TestHeadlessConsultNoTrailerIsSilentDespiteText` (~400):
  - The negative `os.Stat(c.FindingsPath); err == nil` check becomes
    `rt.Store.ReadFile(c.FindingsPath); err == nil`, i.e. no row and no file.
- **F4** `internal/relevo/ask_test.go` `TestAskRoundFinalMessageBecomesFindings` (~1152-1190):
  - The same port as F2: read through `rt.Store.ReadFile`, and add not-on-disk.

Fixture tests that seed `DiffPath` or `FindingsPath` files on disk and read them back keep
passing unchanged, because `Store.ReadFile` reads disk first. **Do not edit them.** They
include:

- seal_test.go
- show_test.go (both packages)
- review_test.go
- fork_test.go
- cmd/relevo/main_test.go

### 7.4 Mutation checks (run each, report pass/fail, restore)

| # | Break | Must fail |
|---|---|---|
| M1 | capture.go: put back `os.WriteFile(patchPath, diff.Patch, 0o644)` instead of PutRoundFile | `TestCaptureRoundDiff_NormalDiff` (not-on-disk assertion) |
| M2 | consult.go: put back `os.WriteFile(c.FindingsPath, …)` | `TestHeadlessConsultFinalMessageBecomesFindings` |
| M3 | headless.go markerClose: pass `verifyDiffCommand(next.RoundBaselineTree, next.RoundClosedTree)`, i.e. read the baseline after the close | N3 (`Diff: none`) |
| M4 | verify.go: `verifyDiffCommand` swaps its arguments | N2 |
| M5 | remote.go: put back `writeTempAndRename(rt.Store.DiffPath(name, n), rcDiff)` | `TestCatchUpWritesDiffEntryFromView` |

If a mutation does **not** make its named test fail, report it. Do not strengthen tests
beyond this plan.

## 8. Scope check for the reviewer

- `git diff --stat` should show only:
  - the files in §2;
  - the tests named in §7.2 and §7.3: capture_test, verify_test, headless_test,
    remote_test, consult_test, ask_test.
- No golden file changes. No migrations. No new exported names except `FindingsCommand`.
