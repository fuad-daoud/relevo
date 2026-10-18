# Plan: make internal/syncpipe's coverage deterministic (issue #1233)

Save to `docs/plans/2026-10-18-syncpipe-coverage.md` as the last step. One builder round, one PR (new commits only, no amend).

## Behaviour and goal

`internal/syncpipe` measures 87.9-90.2% across CI runs (baseline `testdata/coverage-baseline.txt` line 55: 90.0). zen measures 90.0% every time, so the variable blocks only run under CI load or scheduling. Goal: every statement the suite can reach is reached on every run, so coverage is one stable number. Test changes only. Production code gets a seam only where step 2 shows no test-side sync point can drive a path.

## Leads (hypotheses to confirm by measurement, not conclusions)

The suite is built around a fake worker: `TestMain` in `client_test.go` (lines ~67-75) respawns the test binary, and `runFakeWorker` picks a mode. Timing-dependent code lives in `client.go` and `supervisor.go`. Candidate variable blocks:

1. `Client.callLocked` / `Client.fail` / `Client.miss` (`client.go` ~365-460). The write may fail (worker already exited, EPIPE) or the read may meet EOF, depending on whether a dying worker (`modeDie`, `modeWrongID`, `modeStaleID`) exits before or after the write. The `c.isGone()` early branch in `callLocked` and in `Close` is also race-decided.
2. `Client.readReply` (`client.go` ~462-495). The `out.err != nil` read-error arm vs the `timer.C` arm, with 300ms deadlines in `client_test.go` (`TestFakeWorkerHangHitsTheDeadline`, `TestPipeClientCountsOnlyAMissAndNotARefusal`) and `supervisor_test.go` (`TestMissedDeadlineKillsAndCounts`). Under load the 300ms bound can also expire during the hello handshake in `Start` (`client.go` ~226-232, the `c.Close()` on handshake failure).
3. The cancel path: `Client.cancel`, the `c.cancelled.Load()` arms in `callLocked` and `fail`, and `Supervisor.Cancel` (`supervisor.go`) against `TestCancelInterruptsACallInFlight` and `TestDisableCancelsAHungPullWithoutCountingADeath` (`supervisor_test.go` ~230-260, ~465-507). Whether the cancel lands before the request is written, during the read, or after the worker is already gone depends on scheduling. `waitForWorker` only proves a process exists, not that the call is in flight.
4. `Client.Close` shutdown/kill ordering, and `reap` / `waitForExit` polling.

## Steps

1. **Baseline and find the blocks.** Deliverable: a list in the report of every block covered only sometimes, with the tests that reach it.
   - Export `TMPDIR` and `GOTMPDIR` off `/tmp`.
   - Write a loop script outside the repo (not committed) that builds the test binary once and runs `go test -race -count=1 -coverprofile=<dir>/pN.out ./internal/syncpipe` >= 30 times for each configuration: a normal run, `GOMAXPROCS=1`, `GOMAXPROCS=2`, and the same with 4-8 copies running in parallel plus a CPU burner, so contention is real. Also run `-cpu 1,2` and `taskset -c 0`.
   - Convert each profile into a set of blocks with a zero count. Diff against a normal run: a block is "variable" if its count is zero in some runs and non-zero in others. For each, record file:line range and run `go test -run <Test> -coverprofile` per test (or `-coverprofile` with `-list` of tests) to find which test(s) reach it.
   - Done when the variable-block list is stable across two repeats of the 30-run loop. If no variable block appears locally (zen showed none), widen the contention (more parallel copies, `stress`-style burner, `-race` with `GOMAXPROCS=1`) before giving up. If still none, the report says so and falls back to the static leads above, instrumenting each lead with a temporary counter to see which arm is actually decided by timing. That fallback is a halt-and-report, not a guess.
2. **Make each variable block deterministic.** Deliverable: test edits in `client_test.go` and `supervisor_test.go` (and a new `*_test.go` file if a file would pass 600 lines; `supervisor_test.go` is at 507, `client_test.go` at 854 and already over the size limit, so put new tests in a new file rather than growing it). Per block, pick the cheapest of:
   - an explicit sync point in the fake worker: a mode that signals "request received" (write a line to stdout/stderr or a marker the test reads) before hanging, so `Cancel` always lands during the read, never before the write or after the exit;
   - a worker mode that exits only after the client's write is acknowledged (read the request, then exit), so the failure always arrives at the read arm; and a separate mode that exits before the request (wait on `c.finished` / `waitForExit` first, then call) so the write/`isGone` arm is always taken;
   - a generous deadline for the handshake and a short deadline only for the call under test (set `cfg.Timeout` after `Start`, or let the fake worker answer hello at once before hanging), so load cannot expire the bound during hello;
   - call internal methods directly (`c.cancel()`, `c.kill()`, `c.callLocked`) on a client whose state the test controls, instead of racing a process.
   Do not weaken assertions: each rewritten test keeps what it pins (deaths counted once, a cancel is not a death, no worker reuse, the deadline names "no reply within"). Do not replace waits with larger sleeps.
   If a block is reachable only through true timing (an OS-level race such as a process exit versus a pipe write, with no hook that a test can use) and a seam would cost more than the roughly 0.1-0.3 points it carries, leave it covered-or-not by luck is NOT acceptable either: first try the direct-call approach; if impossible, add the smallest production seam (an unexported hook field on `Config` or `Client`, nil in production) or, failing that, say in the report which statements these are and why. Any production edit must be listed separately with its reason.
3. **Mutation-test every new or rewritten test.** Deliverable: a table in the report, one row per test: the condition broken (for example drop the `cancelled` check in `fail`, skip the kill in `cancel`, make `miss` not call `OnMiss`, flip the `isGone` branch) and the named test that failed. A test that passes with and without the logic is rewritten.
4. **Prove stability.** Deliverable: coverage numbers of >= 30 runs per configuration (normal, `GOMAXPROCS=1`, `GOMAXPROCS=2`, parallel copies plus burner), all with `-race -count=1 -cover`, reported as a count of distinct values. Pass condition: exactly one distinct value across all runs, and no variable block remains in the profile diff from step 1. Also run the loop with `-shuffle=on` for 10 runs.
5. **Baseline.** Compare the stable value to 90.0. Leave `testdata/coverage-baseline.txt` untouched if it is 90.0 (within the displayed rounding). If it differs, regenerate with `sh scripts/check-coverage.sh --write` only for that package's line, and say so in the report. Never lower it to get green.
6. **Gate.** Run `make check-affected` (stricter than `go test ./...`: gofmt, vet, mod tidy, affected packages). Fix every reported error before rerunning. Do not run the full `-race` suite. CI has no network and no harness binary: the new tests must use only the in-process fake worker, never a real harness or network. Check `scripts/check-comments.sh` and `scripts/check-filesize.sh` pass: comments say why, no issue or PR numbers, no history.
7. **Save the plan** to `docs/plans/2026-10-18-syncpipe-coverage.md` and commit it with the implementation (plans ship with the code).

## Scope

In: `internal/syncpipe/*_test.go` (new or edited), `docs/plans/2026-10-18-syncpipe-coverage.md`. Conditional: a minimal nil-by-default hook in `internal/syncpipe/client.go` or `supervisor.go`, `testdata/coverage-baseline.txt` only if the stable value differs from 90.0. Out: any other package, `.golangci.yml` exclusions (add none), the coverage script.

## Report must include

- The variable-block list (file:line, reaching tests) with the diff method and contention settings used.
- For each block: how it is now driven, or why it is left (timing-only, seam not worth it).
- The mutation table.
- The coverage of every stability run (>= 30 per configuration) and the count of distinct values, with the baseline decision.
- `git diff --stat`, compared against the scope above, with any production edit called out and justified.
- `make check-affected` result.

## MasterMind evidence (measured on a GitHub ubuntu runner, 2026-10-18)

A throwaway CI probe built `go test -race -c -cover -coverpkg=./internal/syncpipe`
and ran it 30 times idle and 30 times beside 3 looping `internal/db` race test
binaries. Idle: 30/30 at 89.5%. Loaded: 22 at 89.5%, 8 at 89.7%. Diffing all 60
profiles, exactly one block is covered only sometimes:

- `internal/syncpipe/client.go:391.3,392.1` -- the `c.requests.Write` error arm
  in `callLocked` (`return ..., c.fail(fmt.Errorf("write %s: %w", ...))`),
  covered in 8/60 runs. It is taken when a dying fake worker has already exited
  before the request is written (lead 1 in this plan).

CI's own swing (87.9-90.2 across runs, where syncpipe shares a shard with other
packages) is wider, so heavier load likely flips more blocks. Step 1 still
applies: reproduce with real contention on zen, and treat this block as
confirmed. The fix for it must drive the write-error arm on every run (a worker
mode that exits before the call, waited on via the client's exit signal), and a
separate test must keep driving the read-error arm.

The gate on this binding is `make check-affected BASE=<this round's base>`.
