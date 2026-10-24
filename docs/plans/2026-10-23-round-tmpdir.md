# Plan T3: round-tmpdir

Part of the disk lane (#1314 runner temp dirs, #1308 Go build cache).

Depends on T1 and T2.

**Behaviour.**
- Every round process gets its own temp directory `<TmpRoot>/rounds/<binding>-NNN`, with `NNN` formatted like `ScratchWorktreePath` (`%03d`).
- It is created when the process is started or relaunched, and the process's `TMPDIR` and `GOTMPDIR` point at it.
- It is removed, with failures logged and swallowed, when the round closes by any route, and whenever relevo kills the round's process.
- This covers builders and readers, local and served. A relaunch after a lost process, a resume or a mid-round switch reuses the same directory name.

**Seams.**
- New `internal/relevo/procprep.go`. This is the one seam where a process is prepared.
  - `procRequest` carries the kind (round or check), a key, the working tree and the binding when there is one.
  - `prepareProc(ctx, rt, req)` returns the temp dir to set on `ProcSpec.TmpDir`, any env additions, and a cleanup.
  - In this plan it only creates the tmp dir, through `rt.Store.EnsureTmpEntry`. A `rt.TmpRoot == ""` makes it a no-op.
  - Chain C extends it later, in C3. Leave named, commented insertion points for its two later calls. Do not add stub files.
- New `internal/relevo/roundtmp.go`:
  - `roundTmpRel(name, round)`.
  - `removeRoundTmp(rt, b, round)`, which logs and swallows like `removeReaderScratch` (`summary.go` ~334).
- `internal/relevo/headless.go`, `startProcess` (~330-420): call `prepareProc` before `rt.Runner.Start` and set `spec.TmpDir`.
  - A prepare error returns `b, err` plainly. It is neither a `spawnFailure` nor a call to `RecordSpawnFailureLocked`, so it never gates a candidate.
  - Net line growth in `headless.go` must be zero. Move code out to pay for the call site.
- Removal hooks, each a one-line call to `removeRoundTmp` placed beside the existing cleanup:
  - Close: `reconcile.go` ~781, next to `removeReaderScratch`. Verify every close route (marker close, `closeStopped`, served close, timeout close) goes through it. If one does not, hook that route too and list it in the report.
  - `done.go` ~174.
  - `bind.go` ~1026 (unbind).
  - `stopProcess` (`headless.go` ~1565), after a successful `Kill`. This covers stop, timeout, silence/stall kills and done, and the mid-round switch if it routes through `stopProcess`. Grep the other `Runner.Kill(` sites for round processes (`switch.go`) and cover any that bypass it.
  - The next start recreates the directory, so removing it on a kill is safe even when the round resumes.

**Steps.**
1. Add `procprep.go` and `roundtmp.go` with their unit tests. Deliverable: `go test ./internal/relevo -run 'RoundTmp|PrepareProc'` is green.
2. Wire the `startProcess` call and the removal hooks. Deliverable: the new integration-style tests with `fakeRunner` pass, and `go test ./internal/relevo/...` is green.
3. Run `make check-affected`.
4. Save the plan to `docs/plans/2026-10-23-round-tmpdir.md` in the same commit.

**Tests.**
- `TestPrepareProcCreatesRoundDir` and `TestPrepareProcNoRootIsNoop`.
- `TestStartProcessSetsSpecTmpDir`: the `fakeRunner` captures the spec, and the dir exists.
- One removal test per route:
  - `TestRoundCloseRemovesTmp`.
  - `TestDoneRemovesTmp`.
  - `TestUnbindRemovesTmp`.
  - `TestStopProcessRemovesTmp`.
  - `TestSwitchEmptiesTmp`.
  - Each asserts the dir is gone after the route runs.
- `TestRemoveRoundTmpFailureDoesNotFailClose`: the removal error is swallowed.
- `TestPrepareErrorDoesNotGateCandidate`: a prepare error leaves the availability ledger untouched.
- A reader round gets the same dir as a writer.

**Mutation targets.**
- Delete each removal hook in turn: its named test must fail.
- Drop `spec.TmpDir = ...`: `TestStartProcessSetsSpecTmpDir` must fail.
- Return a `spawnFailure` from the prepare error: the no-gate test must fail.

**Deleted.** None.

**Report must include.**
- The list of close and kill routes that were hooked, and any found that bypass `stopProcess`.
- The `headless.go` line count before and after.
- Confirmation that the spawned child sees `TMPDIR` equal to the round dir. A small `proc` real-process check is enough, and it spawns no harness.

## Standing constraints (every plan carries them)

- CLAUDE.md applies.
  - Comments say *why* and never restate code.
  - No issue or PR numbers, and no "used to", in code or tests.
  - Functions are at most 70 lines; non-test files at most 600.
  - `headless.go` is over the limit and on the allow-list: do not grow it. New code goes in a new file, and a call-site addition there is paid for by moving the same number of lines out.
  - Add no lint or allow-list exclusion.
- Tests in `cmd/relevo` must not spawn a harness or reach the network, because CI has neither. Test rules as pure functions in `internal/relevo` or the owning package.
- Run only the focused tests of the packages you touched, fixing every error before the next run. Then run `make check-affected` once at the end. The chain runs the full gate (`make check`) after the last plan.
- A step that moves code between packages regenerates `testdata/coverage-baseline.txt` with `sh scripts/check-coverage.sh --write` and says so. No step lowers a baseline.
- Every plan's last step is to save the plan to `docs/plans/2026-10-23-<slug>.md` in the same commit.
- Never amend or rebase a pushed commit. Fix forward with new commits.
- If a premise below contradicts the code, halt and report. Do not improvise.
