# Plan T4: check-tmpdir

Part of the disk lane (#1314 runner temp dirs, #1308 Go build cache).

Depends on T3.

**Behaviour.**
- Every gate or check process (a binding's round gate, a served check, a chain check) gets `<TmpRoot>/checks/<unit>`.
- `<unit>` is the sanitized scope unit name already unique per check (`scopeUnitNameFor`, `headless.go` ~105). `TMPDIR` and `GOTMPDIR` point at it.
- It is removed when the check process ends, whatever the result: pass, fail, timeout, error or a failed start.
- A restart-loss re-run reuses the directory.

**Seams.**
- `internal/relevo/gate_proc.go`, the single seam for all three kinds:
  - `startGateProc` (~30) calls `prepareProc` with kind check and key `spec.UnitName`, and sets `ProcSpec.TmpDir`. If the start fails after the dir was created, it removes the dir.
  - `advanceGateProc` (~58) removes the dir at every `done == true` outcome: the timeout kill, pass, fail and the error branch. It does not remove it when `next != nil`, the re-run.
- `internal/relevo/procprep.go`: the key-to-path rule for checks. Entries whose `UnitName` is empty (tests) skip the dir.
- `gate.go`, `chain_check.go` and `served_check.go` are not edited. Their specs already carry `UnitName`.

**Steps.**
1. Teach `prepareProc` the check kind and add the `checks/` path helper with its unit tests.
2. Wire `startGateProc` and `advanceGateProc`. Deliverable: `go test ./internal/relevo/...` is green.
3. Run `make check-affected`.
4. Save the plan to `docs/plans/2026-10-23-check-tmpdir.md` in the same commit.

**Tests.**
- `TestStartGateProcSetsCheckTmpDir`.
- `TestAdvanceGateProcRemovesTmpOnPass`, and the same for fail, timeout and error.
- `TestAdvanceGateProcKeepsTmpAcrossRestartRerun`.
- `TestStartGateProcFailedStartRemovesTmp`.
- One test each for a round gate, a served check and a chain check, asserting that `TMPDIR` reaches the spec.

**Mutation targets.**
- Remove the removal in the timeout branch: the timeout test must fail.
- Remove it in the `!ok` re-run branch: the keep-across-rerun test must fail.
- Drop the removal after a failed start: that test must fail.

**Deleted.** None.

**Report must include.**
- That the unit name is filesystem-safe for every caller, or the sanitizer added.

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
