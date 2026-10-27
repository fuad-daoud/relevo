# Plan: idle ticks queue no synced write

Issue #1361, the idle-write test. **This is test-only: no production code changes.** If a test here finds rows, that is a real bug. Do not fix it. Stop and report it (see "If it fails").

Save this plan as `docs/plans/2026-10-27-idle-write-ceiling.md` and commit it with the tests.

## Why

Before the fix that records a served binding's contact at most once an hour (`lastSeenEvery`, `internal/serve/bindings.go:293-299`), zen exported about 3,700 log entries an hour for weeks while no round ran, and nothing caught it. A running round's ceiling is already pinned: `TestRunningRoundQueuesNoSyncedWritePerTick` (`internal/relevo/round_write_ceiling_test.go:67`) and `TestHeadlessRoundQueuesNoSyncedWritePerTick` (`internal/e2e/headless_test.go:1214`). Nothing pins the idle case, where daemon ticks keep polling bindings and no round runs. These tests are its sibling, on both sides of a remote binding.

## Test 1: the client daemon's idle remote bindings (`internal/relevo`)

New file `internal/relevo/idle_write_ceiling_test.go`, test `TestIdleRemoteBindingsQueueNoSyncedWritePerTick`.

- **World.** Build the runtime with the machine database wired, as `outboxRuntime` does (`internal/relevo/sync_outbox_test.go:23`). Save two remote bindings, made from `remoteBinding(server)` (`remote_test.go:2727`) with distinct names, both with **no round open** on the server. Set `rt.Remote` to a `fakeRemote` (`remote_test.go:55`) whose `getBindingResp` is a `BindingView` of a binding whose round has closed or is idle. Choose the `RoundState` that means "no round running" in `internal/remote/proto.go`, and name it in the report.
- **Sync must not hide rows.** The daemon truncates the outbox while sync is off (`TestSyncOutboxTruncatesWhileOff`, `sync_outbox_test.go:70`), so a naive test would pass for the wrong reason. Set the world up the way `TestOutboxTruncationStaysOffWhileOn` (`:93`) does, so the truncate does not run and no real sync attempt starts. Assert in the test that the truncate is off: one seeded write survives a tick. That is the control that proves the counter can see rows.
- **Drive.** Use `NewDaemon(rt, time.Second)` and its `Tick`, as the `sync_outbox_test.go` tests do.
  - Run 2 warm-up ticks. The first sightings may write.
  - Then `TruncateOutbox()`.
  - Then 20 ticks, advancing `rt.Now` by the daemon interval between ticks if the runtime clock is injected.
  - Then count rows with `DrainOutbox` (or the package's `outboxCount` helper).
  - Want **0**. On failure, print each row's table and pk, so the message names what is being rewritten.
- **Variant: volatile fields.** A second subtest polls a view whose fields change on every poll the way a live but idle server's would. Find them in `remote.BindingView`: a last-seen, heartbeat or progress clock. Change them each tick and require 0 rows again, because a client must keep such fields in machine-local overlay state, not in synced rows. If `BindingView` has no such field, say so in the report and skip the variant.
- If `Daemon.Tick` cannot run hermetically with a `fakeRemote` (it reaches the network, a harness, or real HOME), fall back to the `reconcile` loop used by `TestRemoteBrokenHaltsOnceAcrossPolls` (`remote_broken_halt_test.go:65`), and say why in the report. CI runners have neither network nor a harness.

## Test 2: the server's idle served bindings (`internal/serve`)

`TestGetRecordsLastSeenAtMostEveryWindow` (`internal/serve/serve_test.go:1400`) pins that a poll inside the window does not *save* the binding. It does not count outbox rows. Add `TestIdlePollsQueueNoSyncedWrite` beside it, in `serve_test.go` if that file stays at or under its current size class, otherwise in a new `serve_idle_test.go`. It uses the same setup with a served binding that has no round open:
- poll it once (first contact may write);
- truncate the outbox;
- poll it 20 times inside `lastSeenEvery`;
- require 0 outbox rows.

If the serve test's store has no machine database with an outbox, wire one the way that package's other database tests do. If it cannot be done without production changes, say so and skip Test 2.

## Mutation checks

Each is temporary. Revert it, and report the failing test's name.
1. In the client: make a remote poll save the binding every tick, for example by writing a synced field such as `UpdatedAt` in the remote sync path (`internal/relevo/remote_sync.go`). Test 1 must fail.
2. In serve: set `lastSeenEvery` to 0. Test 2 must fail.

## If it fails

If either test shows rows **without** a mutation, that is a production bug, so do not change production code. Leave the test in place, commit it with a `t.Skip` that names the rows found, and report the exact tables, pks and the field that changed per tick. The MasterMind will file it.

## Gate

- `go test ./internal/relevo/ -run 'TestIdle|TestSyncOutbox|TestOutbox' -count=1`
- `go test ./internal/serve/ -run 'TestIdle|TestGetRecordsLastSeen' -count=1`
- `gofmt -l` on touched files, `make lint`, `sh scripts/check-filesize.sh`, `sh scripts/check-comments.sh`.
- Do NOT run `make check` or `make check-affected`.

## Style

- Test files at most 600 lines, functions at most 70.
- A comment says *why*.
- No issue numbers or history in code or tests.
- A test's name says what it pins.

## Report

Include:
- the files changed (`git diff --stat`);
- the `RoundState` and volatile fields you used;
- each gate command with its result;
- both mutation results;
- whether either test found rows without a mutation.

Commit everything, and leave the tree clean.
