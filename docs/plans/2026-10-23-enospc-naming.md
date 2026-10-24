# Plan C4: enospc-naming

Part of the disk lane (#1314 runner temp dirs, #1308 Go build cache).

Depends on T3 and T4 (it edits `gate_proc.go` and the `headless.go` exit path), and on C2 (`EnvironmentError`).

**Behaviour.**
- When a check's output contains `no space left on device` or `no storage space` (case-insensitive), its record's note reads `environment: disk full: the output reports "no space left on device"`.
  - A chain check with that note halts the chain with that reason. It is not fed to the repair loop as red.
  - A round gate with that note halts the binding and does not spend the regate budget.
- When a round's builder exits without a report and the stream tail contains either phrase, the round halts with `environment: disk full: ...`.
  - It does not switch to another candidate, and it does not gate or exclude the candidate that hit the full disk.
- The scan reads only a bounded tail: 64 KiB for checks, `availability.LimitScanLines` lines for a stream.

**Seams.**
- New `internal/relevo/diskfull.go`:
  - `diskFullIn(tail string) bool`, pure, with the two phrases as named constants.
  - `diskFullNote(...)`.
  - A bounded tail reader.
- `internal/relevo/gate_proc.go`, `advanceGateProc`: on `fail`, `timeout` or `error`, scan the log tail and set `rec.Note`. `Result` is unchanged, so downstream result handling is untouched.
- Consumers:
  - `chain_check.go`'s settle step (~262-300): `row.Note` with the environment prefix halts the chain with reason `check <step>: <note>`.
  - The regate path: find the consumer of `GateRegate()` with `grep GateRegate` and the gate-fail repair code. An environment note halts instead.
- `internal/relevo/headless.go`, the "Exited without a report" block (~1050): call `diskFullIn` on the tail already read for denial matching (`currentBuilderTail`) and halt through `haltAndSettle` before the switch decision. Net growth must be zero, so put the logic in `diskfull.go` and keep the call site to a few lines.

**Steps.**
1. Add `diskfull.go` with its pure tests.
2. Wire the check side (`advanceGateProc`, the chain settle, the regate guard).
3. Wire the round exit.
4. Deliverable: `go test ./internal/relevo/...` is green. Disk-full output comes from fake log files, never a full disk.
5. Run `make check-affected`.
6. Save the plan to `docs/plans/2026-10-23-enospc-naming.md` in the same commit.

**Tests.**
- `TestDiskFullIn`: a table covering both phrases, case-insensitivity, a near miss, and a phrase outside the bounded tail.
- `TestAdvanceGateProcNamesDiskFullOnFail`, and the same for timeout.
- `TestChainHaltsOnDiskFullCheck`: no repair round is started.
- `TestGateDiskFullDoesNotSpendRegate`.
- `TestExitWithDiskFullTailHaltsWithoutSwitch`: no candidate exclusion is recorded.
- `TestExitWithoutDiskFullStillSwitches`: the existing behaviour is unchanged.

**Mutation targets.**
- Remove the guard before the switch: the no-switch test must fail.
- Scan only a passing check: the fail test must fail.
- Drop the regate guard: that test must fail.

**Deleted.** None.

**Report must include.**
- The `headless.go` line count before and after.
- The exact consumers found for the regate budget and the chain red path.

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

## Review fix

Two rules keep the disk-full text from being taken for proof of a full disk.

1. **Match only the error form.** Real ENOSPC output ends `<something>: no space left on device`, so a phrase counts only when it directly follows `": "`, case-insensitively. The same holds for `no storage space`. A file a builder printed, or a test message quoting the phrase, no longer matches.
2. **A builder's exit halts as disk full only when the disk really is short.** `haltOnDiskFullExit` halts when the stream tail matches rule 1 **and** `preflightDisk` refuses for the binding's worktree now; otherwise the exit is handled as before. The halt text is the preflight's own `environment: disk full: ...` reason, with the tail's phrase as detail. A full disk that has already cleared (a check's temp dir that was removed) falls through to the normal exit handling, as it did before.

Checks keep rule 1 only: a check's temp dir is removed when it ends, so a free-space probe afterwards proves nothing.
