**Note on the base.** This branch already holds C1, C2, T1-T4. The "insertion point" comments that T3 left in `procprep.go` were removed in review; put the preflight before the tmp-dir creation and the `goFlagsEntry` env entry after it, inside `prepareProc`, as the steps below say.

# Plan C3: prep-trimpath-and-preflight

Part of the disk lane (#1314 runner temp dirs, #1308 Go build cache).

Depends on C1, C2, and T3 plus T4 (the `procprep.go` seam and the `gate_proc.go` call).

**Behaviour.**
- Before any round or check process starts, the preflight runs against two paths: the worktree and `TmpRoot`.
  - Under the policy minimum, nothing starts.
  - A round halts with `environment: disk full: <path> has N GB free (< M GB)` (verbatim, no "builder spawn failed:" prefix).
  - A chain halts with that reason, not a red gate.
  - A served check or round gate records an errored run whose note is that text.
- For a round, and for a check whose working tree has a `go.mod` or `go.work` at its root, `GOFLAGS` gets `-trimpath` appended. This is skipped when the repository opted out (C1) and when `-trimpath` is already present.
- A preflight failure never gates a candidate and never counts as a spawn failure.

**Seams.**
- `internal/relevo/procprep.go`: add the two calls at the insertion points T3 left.
  - Preflight first, then the tmp dir, then the env entry from `goFlagsEntry`, merged into the `prepareProc` result.
  - The opt-out comes from `RepoSettingsFor(ctx, rt, tree)` (`repo.go` ~127). A runtime with no `Config`, which is every served runtime, reads as the default: on.
- Halt text. A helper `spawnHaltText(err)` returns `err.Error()` verbatim when `errors.As` finds an `*EnvironmentError`, and `"builder spawn failed: " + err.Error()` otherwise.
  - It is used at `send.go` ~642, `chain_send.go` ~113, and `queue.go` ~110 (the `haltAndSettle` text).
  - Other paths that wrap the error ("builder lost to a daemon restart and could not be relaunched: ...", switch) keep their prefixes; the reason text still reaches the halt.
- `internal/relevo/chain_engine.go` ~321-326 (and the placed-check branch ~305-310): the chain halt reason is `environment: disk full: ...` when the start error is an `*EnvironmentError`, instead of "check X could not start: ...".
- `startGateProc` records the note on the `GateRecord`. The text is already the error text.

**Steps.**
1. Add `spawnHaltText` and the three call-site changes, with their pure tests.
2. Extend `prepareProc` with the preflight and GOFLAGS, and test the merged result.
3. Change the chain halt reason in `chain_engine.go`.
4. Deliverable: `go test ./internal/relevo/...` is green. All tests use `fakeRunner` and a fake free-space function, with no real disk filling.
5. Run `make check-affected`.
6. Save the plan to `docs/plans/2026-10-23-prep-trimpath-and-preflight.md` in the same commit.

**Tests.**
- `TestPrepareProcRefusesLowDisk`: the error is an `EnvironmentError`, and the spec was not started.
- `TestPrepareProcChecksWorktreeAndTmpRoot`: each path is checked.
- `TestPrepareProcAddsTrimpathForGoTree`, and:
  - none for a non-Go tree;
  - none when the repository opted out;
  - appends to an existing GOFLAGS;
  - leaves a value that already has `-trimpath` alone.
- `TestSendHaltsWithEnvironmentReason`: the binding Halt equals the exact text.
- `TestChainHaltsWithEnvironmentReasonNotRedCheck`.
- `TestLowDiskDoesNotRecordSpawnFailure`: the candidate stays ungated.
- `TestServedCheckRecordsEnvironmentNote`.
- `TestSpawnHaltTextPlainErrorKeepsPrefix`.

**Mutation targets.**
- Skip the preflight for checks: the chain and served tests must fail.
- Wrap the environment error as a `spawnFailure`: the no-gate test must fail.
- Drop the Go-tree guard or the opt-out lookup: the matching tests must fail.

**Deleted.** None.

**Report must include.**
- The three halt-text sites changed.
- A restatement of the limit that served rounds cannot opt out of trimming.

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
