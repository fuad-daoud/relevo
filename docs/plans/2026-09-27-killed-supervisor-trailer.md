# A killed supervisor cannot leave an exit trailer

Fixes GitHub issue #629. Commit message prefix: `fix(proc):`.

Line numbers below are from this tree's HEAD, `3d7c0aff`. Each is an anchor, not
a contract: if one has drifted, the named function and the quoted text around it
identify the place.

## Hard rules for this round (read first)

- **Nothing is deleted.** Every existing test keeps every assertion it has. Two
  tests gain one assertion each; several call sites gain one argument.
- No test may run `systemctl`, a harness binary, or signal a process it did not
  start. `internal/proc` tests spawn `sh`, `sleep` and `ps`-class binaries only,
  as they do today. CI has no harness binary and no network.
- Code style (CLAUDE.md): comments say *why* only; no issue numbers, no history,
  no `§` in code or test names; functions at most 70 lines; non-test files at
  most 600 lines; never add a lint or file-size exclusion.
- If a step is impossible as written, or the code contradicts this plan, **stop
  and report**. Do not improvise a different design, and do not bend a test to
  fit.
- Name any existing test whose expected value you had to change in the report,
  with the reason.
- Commit prefix `fix(proc):`; this plan is committed verbatim as
  `docs/plans/2026-09-27-killed-supervisor-trailer.md`.

## 1. System overview

Every local round runs under `/bin/sh -c <supervisorScript> relevo-supervisor
<scope> <bin> ...`. The supervisor runs the builder in the foreground and, when
the builder returns on its own, appends `relevo-exit:<rc>` to the round's
stream; `Runner.ExitCode` reads that last line and the round's classification
(`internal/relevo/headless.go`) depends on it. A supervisor killed by a signal
must leave **no** trailer: that absence is what makes `ExitCode` report
`ok=false`, the round read as code `unknown`, and the restart-lost and oom
branches classify correctly.

Today the only thing keeping that true is the supervisor script's
`trap 'exit 143' TERM` (internal/proc/proc.go:70), added for #439 on the
assumption that "a pending trap runs before the next command in every POSIX
shell". #629 shows that assumption is false: on bash 3.2 (macOS `/bin/sh`) the
trap is skipped or deferred and the script runs on to the trailer, so a killed
supervisor can leave `relevo-exit:143` behind. The flake was seen once, on a
macOS CI leg, in `TestStartedProcessIsInItsOwnGroupAndKillReturnsWithinGrace`.
It cannot be reproduced here: on this Linux box `/bin/sh` is dash, which dies
on TERM before the trailer, and the CI Linux legs run dash too.

**Chosen shape: A, the kill record.** The killer is the only party that knows a
kill happened, so it must be the one to say so. `Kill` records the kill it is
about to perform beside the stream it voids, and `ExitCode` honours that record
before it reads the trailer. This is the only shape that can be *proved* here:
the record makes `ok=false` follow from a write `Kill` performs, not from any
shell's signal behaviour, so it holds on dash, bash 5 and bash 3.2 alike, and
the deterministic test below fails on this Linux box when the record is
reverted. It is also the second attempt at this problem: the first, shell level,
is the trap that #629 disproves.

Shape B (a shell-level guarantee) is **not** chosen and is not added: no
construct can be validated here for bash 3.2, and removing the trap would risk
the opposite failure -- a shell that runs on and exits 0, printing a trailer
that reads as a clean exit. The trap stays exactly as it is; it is a best-effort
reduction of the race on shells that do run pending traps, not the guarantee.

**Out of scope, stated so it is not mistaken for covered:** a supervisor killed
by something that never calls `Kill` (a cgroup kill when systemd stops the
daemon, a machine reboot) still reads as `unknown` only because its shell dies
before the trailer. That path is unchanged and stays Linux/dash-correct; the
record covers every kill relevo performs itself, which is what this round's
contract is. `Rusage` is also unchanged: it keeps reading whatever rusage line a
racing shell wrote, which only affects the round's recorded CPU figure.

## 2. What this round changes, and what it does not

The record is a sibling of the stream, `<stream>.killed`, holding one line:
the pid and start time of the process `Kill` signalled. `ExitCode` for that same
handle reads `ok=false` whatever the stream's last line says. A record for a
*different* process -- a predecessor on a stream a later round reopened, or a
round relaunched after a failed send -- is ignored, so a live supervisor's own
trailer always wins.

Checked, so the report can state it:

- **Seal** (`store.RoundFiles`, `diskFiles`, `SealRound`, `SealAll`): the record
  is a flat NNN-* file for the round, so the seal pass reads it into `round_file`
  and removes it from disk exactly like the round's other files. It never blocks
  a seal, and it does not keep a DONE binding's directory alive any longer than
  the round's own files already do.
- **Archive / unbind** (`Tx.archive`, `Tx.remove`): both `os.RemoveAll` the
  binding directory after sealing, so nothing is left behind.
- **`relay show --artifacts`** (`artifactRels`): artifact names are filtered to
  the `NNN-<actor>/` prefix, so a flat `NNN-builder.jsonl.killed` is never shown
  as an artifact. `relevo fork` copies it as a round file, which is harmless.
- **Scratch sweep** (`SweepScratch`): worktrees only, untouched.
- **Stream drain** (`StreamDrained`, `trailerLinesOnly`): read the stream file
  only, untouched.

## 3. File structure

```
internal/proc/kill_record.go        NEW: the record path, writer, reader, parser (unix)
internal/proc/kill_record_test.go   NEW: parser table test + the four record tests
internal/proc/proc.go               ExitCode honours the record; Kill writes it
internal/proc/proc_other.go         Kill's new argument
internal/proc/proc_test.go          three Kill calls gain the stream argument
internal/spawn/spawn.go             Runner.Kill's signature and the contract text
internal/relevo/headless.go         stray kill passes ""; stopProcess takes the binding
internal/relevo/bind.go             stopProcess call site
internal/relevo/done.go             stopProcess call site
internal/relevo/stop.go             stopProcess call site
internal/relevo/gate.go             gate timeout kill passes the gate log
internal/relevo/switch.go           mid-round switch kill passes the round's stream
internal/relevo/send.go             failed-send kill passes the round's stream (or "")
internal/relevo/fake_test.go        fakeRunner.Kill records the stream; its own test
internal/relevo/stop_test.go        one assertion: the stop kill names the stream
internal/relevo/consult_test.go     one assertion: the consult kill names its stream
internal/consult/reconcile.go       consult timeout kill passes Endpoint.LogPath
internal/e2e/fakes_test.go          scriptRunner.Kill's signature
internal/e2e/headless_test.go       stopRecordedBuilders passes the round's stream
internal/mcp/verbs_test.go          stubRunner.Kill's signature
internal/remote/client/helpers_test.go  scriptRunner.Kill's signature
internal/serve/helpers_test.go      scriptRunner.Kill's signature
internal/serve/admin_test.go        aliveRunner.Kill's signature
docs/plans/2026-09-27-killed-supervisor-trailer.md   this plan, verbatim (last step)
```

The seam is 19 tracked files plus the two new ones; a walkthrough of exactly this
shape was built and reverted in a scratch copy before this plan was written, and
the results are in section 8.1.

## 4. The record

One file per stream, `<streamPath>.killed`, e.g.
`~/.local/state/relevo/<name>/001-builder.jsonl.killed`; for a gate,
`001-gate.log.killed`; for a consult, `001-<id>-consult.jsonl.killed`.

| field | type in the file | meaning | constraint |
|---|---|---|---|
| pid | decimal, first field | the handle's `PID` | always a real pid: a record is only written for a live process |
| startedAt | decimal, second field | the handle's `StartedAt` in Unix seconds | exactly `h.StartedAt.Unix()`, the same truncation `handleFor` and `handleOf` use |
| | | | one line, `"<pid> <startedAt>\n"`, mode 0o644 |

Written with `O_CREATE|O_TRUNC|O_WRONLY`. A record that is missing, unreadable,
unparsable, or whose pair does not match the handle `ExitCode` was given means
"not this process" and the trailer is read as today. The writer only ever writes
while the process is still alive, and a reader only reads after the handle is
dead, so a reader never sees a half-written record.

Lifetime: the record is written by `Kill` and never removed by relevo's own
code; it ages out with the round's files when the seal pass moves them into the
database, and with the binding directory on `archive`/`remove`. It is not
cleared by `Start`: identity matching already makes a stale record harmless, and
clearing at spawn would throw the record away on the very relaunch the round
needs it for.

## 5. Interfaces and contracts

### `spawn.Runner` (internal/spawn/spawn.go, lines 126-147)

```go
Kill(ctx context.Context, h ProcHandle, streamPath string) error
```

- Single responsibility, unchanged: stop one process group, escalating.
- `streamPath` is the stream a later `ExitCode` for the same handle will read --
  the round's `BuilderStreamPath`, a gate's `GateLogPath`, or a consult's
  `Endpoint.LogPath` (all three are the file the supervisor appends the trailer
  to). It may be `""`, which means "no reader will read this handle's exit";
  then nothing is recorded.
- Precondition: `h` names the process the caller believes is running; callers
  follow `Alive` first, as they do today.
- Postcondition (killed): when `Alive(h)` was true and the record write
  succeeded, the record exists **before** the first signal, and
  `ExitCode(ctx, h, streamPath)` reports `(0, false)` for as long as it exists,
  whatever the stream ends with.
- Postcondition (not alive): nothing was signalled and **nothing is recorded**;
  a process that exited on its own keeps the code its own trailer carries.
- Errors: `Alive`'s error, unchanged; a record-write error (the process is not
  signalled, the caller may retry); `SIGTERM`/`SIGKILL` errors other than
  `ESRCH`, unchanged.

The type comment (lines 130-137) gains one sentence: a kill is recorded against
the stream so a trailer written after the signal is never read as that process's
code, and `ExitCode`'s "ok false when there is none" list gains "or a kill was
recorded for this handle".

### `(*proc.Runner).Kill` (internal/proc/proc.go:365)

Same contract; the comment gains: the record precedes the signal, because a
reader only ever reads a dead handle, so the record is in place before the
process could die.

### `(*proc.Runner).ExitCode` (internal/proc/proc.go:342)

The second parameter stops being `_` and becomes `h spawn.ProcHandle`. The
comment's last sentence ("The handle is unused: the stream is the record.")
is replaced by the record rule.

### `proc` record helpers (internal/proc/kill_record.go, new, `//go:build unix`)

```go
const killRecordSuffix = ".killed"

// killRecordPath is the record beside the stream: a sibling, not the stream
// itself, which the dying supervisor may still be writing to.
func killRecordPath(streamPath string) string

// recordKill writes the handle Kill is about to signal. An empty streamPath
// records nothing, and a failed write is returned so the caller can refuse to
// signal.
func recordKill(h spawn.ProcHandle, streamPath string) error

// killRecorded reports whether streamPath carries a record for exactly this
// handle. A record for another process -- a predecessor on a stream a later
// process reopened -- says nothing about this one.
func killRecorded(h spawn.ProcHandle, streamPath string) bool

// parseKillRecord reads "<pid> <unix-seconds>"; ok is false for anything else.
func parseKillRecord(s string) (pid int, startedAt int64, ok bool)
```

### `relevo.stopProcess` (internal/relevo/headless.go:1144)

```
stopProcess(ctx context.Context, rt Runtime, b store.Binding, why string) (int, error)
```

The parameter becomes the binding, not `b.Builder`: the function now needs the
round's stream path as well as the endpoint, and computing
`rt.Store.BuilderStreamPath(b.Name, b.Round)` inside keeps the four call sites
from hand-typing it (and from swapping the two string arguments). The doc
comment's "A pane endpoint, or a headless one between rounds, is a no-op"
becomes "A binding with no headless process, or one between rounds, is a no-op".
All four callers keep passing their `b`: bind.go:810, done.go:74,
headless.go:1025, stop.go:207.

## 6. Pseudocode

### `Kill`

```
alive, err = Alive(ctx, h)
if err != nil: return err
if !alive: return nil                      # nothing died on relevo's account
if streamPath != "":
    write "<h.PID> <h.StartedAt.Unix()>\n" to killRecordPath(streamPath)
    on failure: return the error           # no signal yet: the round is untouched
signal SIGTERM to -h.PID, ESRCH is not an error
deadline = now + grace
while now < deadline:
    sleep 100ms; alive = Alive(ctx, h)
    if err != nil: return err
    if !alive: return nil                  # existing behaviour, unchanged
signal SIGKILL to -h.PID, ESRCH is not an error
return nil                                 # existing behaviour, unchanged
```

### `ExitCode`

```
if killRecorded(h, logPath): return 0, false
... existing last-line trailer read, unchanged (spawn.ExitTrailer then
    legacy.ExitTrailer, Atoi) ...
```

### `recordKill` / `killRecorded`

```
recordKill(h, streamPath):
    if streamPath == "": return nil
    body = Itoa(h.PID) + " " + FormatInt(h.StartedAt.Unix(), 10) + "\n"
    return WriteFile(killRecordPath(streamPath), body, 0o644)

killRecorded(h, streamPath):
    if streamPath == "": return false
    data, err = ReadFile(killRecordPath(streamPath)); if err != nil: return false
    pid, started, ok = parseKillRecord(string(data))
    return ok && pid == h.PID && started == h.StartedAt.Unix()

parseKillRecord(s):
    fields = Fields(s); if len(fields) != 2: return 0, 0, false
    pid, e1 = Atoi(fields[0]); started, e2 = ParseInt(fields[1], 10, 64)
    return pid, started, e1 == nil && e2 == nil
```

## 7. Error handling

- **The record cannot be written** (read-only directory, no space): `Kill`
  returns the error before signalling. Every caller already treats a `Kill`
  error as "the round may still be running": `Stop` leaves the round open,
  `switchBuilder` halts, `gateStep` records its timeout and warns, `send`
  reports the pid it could not stop, the stray path warns. A refused kill is
  strictly better than an unrecorded one.
- **The signal fails after the record is written** (`EPERM`, or `ESRCH` when the
  process died between `Alive` and the signal): the record stays. If that
  process then exits on its own, its round reads `unknown` rather than the code
  it wrote. That is the conservative reading for a process relevo tried to kill,
  and the window is the microseconds between two syscalls. Do not add
  "remove the record on failure": that would reintroduce the race the record
  exists to close.
- **The record is unreadable or junk**: `ExitCode` reads the trailer as today.
  Never fail a read because the record could not be read.
- No new logging. The record is bookkeeping, not an event; a kill is already
  logged by its caller.

## 8. Working efficiently

Each model step costs a round trip, so:

1. Read in **one batch of parallel reads** (do not search for anything; this
   plan names every place):
   - `internal/proc/proc.go` 339-392 (ExitCode, Kill)
   - `internal/proc/proc_other.go` 25-44
   - `internal/spawn/spawn.go` 126-147
   - `internal/proc/proc_test.go` 121-180; `internal/proc/helpers_test.go` 31-42
   - `internal/relevo/headless.go` 605-620, 1015-1030, 1140-1160
   - `internal/relevo/stop.go` 195-212; `bind.go` 805-815; `done.go` 68-80;
     `gate.go` 60-75; `switch.go` 118-130; `send.go` 640-665
   - `internal/consult/reconcile.go` 100-112
   - `internal/relevo/fake_test.go` 720-742 and 830-850; `stop_test.go` 128-150;
     `consult_test.go` 328-352
   - `internal/e2e/fakes_test.go` 44-58; `internal/e2e/headless_test.go` 886-902
   - the `Kill` method in each of: `internal/mcp/verbs_test.go` 46,
     `internal/remote/client/helpers_test.go` 93,
     `internal/serve/helpers_test.go` 166, `internal/serve/admin_test.go` 34
2. Edit each file in **one edit call** where the whole change is local
   (`proc.go` needs one edit for `Kill`, one for `ExitCode`).
3. Focused loop, fixing every reported error before the next run:
   - `go vet ./...` -- the seam's worklist. It compiles the test files too, so
     every fake is caught; `go build ./...` alone misses all six of them.
   - `go test ./internal/proc/ -count=1`
   - `go test ./internal/relevo/ -run 'Stop|Consult|Headless|Reconcile|FakeRunner' -count=1`
   - `go test ./internal/consult/ ./internal/serve/ ./internal/mcp/ ./internal/remote/client/ -count=1`
4. Run `make e2e` once (it is not part of `make check`, and it exercises the
   real `proc.Runner`).
5. At the end, run `make check` once. It runs `go test -race -count=1 -cover
   ./...` plus `scripts/check-coverage.sh`: the new lines are covered by the new
   tests, so no baseline moves. If golangci-lint is missing on this machine the
   lint step skips itself; say so in the report.

### 8.1 What this shape was measured at (scratch copy, reverted)

The whole change was built in a throwaway copy of this tree and taken through
these commands, to make sure the plan is executable and the tests are the ones
that catch it:

| command | result |
|---|---|
| `go vet ./...` | clean with the seam in place |
| `golangci-lint run ./internal/proc/... ./internal/spawn/... ./internal/consult/... ./internal/serve/... ./internal/mcp/... ./internal/remote/client/...` | `0 issues` |
| `go test -race -count=1 ./internal/proc/` | ok (9.9s) |
| `go test -count=1 ./internal/relevo/ ./internal/consult/ ./internal/serve/ ./internal/mcp/ ./internal/remote/client/` | ok, all five |
| `go test ./internal/e2e/ -run TestHeadlessE2E -count=1` | ok |
| `sh scripts/check-comments.sh`, `sh scripts/check-filesize.sh` | ok |
| mutation 1: drop the `killRecorded` early return in `ExitCode` | `TestKilledProcessReadsUnknownEvenWhenTheStreamEndsInATrailer` fails: `ExitCode = 143, true` |
| mutation 2: move `recordKill` above the `!alive` return in `Kill` | `TestKillOnADeadProcessRecordsNothing` fails: a record exists and `ExitCode = 0, false` where `7, true` was pinned |
| mutation 3: make `killRecorded` ignore the handle identity | `TestASecondProcessOnTheSameStreamKeepsItsOwnTrailer` fails: `ExitCode(second) = 0, false` |

Note what mutation 1 does *not* break: `TestKilledSupervisorNeverWritesTheTrailer`
still passes on this box, because dash never writes the trailer. That is the
whole reason the record exists, and why the trailer-plus-record test above is
the one that has to exist.

## 9. Ordered implementation steps

### Step 1 -- the seam, every implementation and every call site (behaviour unchanged)

1. `internal/spawn/spawn.go`: change the interface method to
   `Kill(ctx context.Context, h ProcHandle, streamPath string) error` and add
   the contract sentence to the type comment (section 5).
2. The eight `Kill` methods gain the argument. `proc.Runner` and `fakeRunner`
   will use it; the rest ignore it as `_ string`:
   - `internal/proc/proc.go:365` -- `h spawn.ProcHandle, streamPath string`
   - `internal/proc/proc_other.go:37` -- `context.Context, spawn.ProcHandle, string`
   - `internal/e2e/fakes_test.go:50`, `internal/remote/client/helpers_test.go:93`,
     `internal/serve/helpers_test.go:166` -- `h spawn.ProcHandle, _ string`
   - `internal/mcp/verbs_test.go:46`, `internal/serve/admin_test.go:34` -- the
     same, on their one-line stubs
   - `internal/relevo/fake_test.go:729` -- `h spawn.ProcHandle, streamPath string`,
     and append `streamPath` to a new `killStreams []string` field next to
     `kills` (the struct is at lines ~600-666)
3. Pass the stream path at every call site, exactly these expressions:
   - `internal/relevo/gate.go:69`: `log` (the gate's own stream,
     `GateLogPath`).
   - `internal/relevo/switch.go:124`: `rt.Store.BuilderStreamPath(b.Name, b.Round)`.
   - `internal/relevo/send.go:656`: a local `stream := ""`, and
     `if round > 0 { stream = rt.Store.BuilderStreamPath(name, round) }` before
     the call; the send-after-spawn hook can fail before `round` is set.
   - `internal/relevo/headless.go:614` (the no-round stray): pass `""` with a
     one-line comment -- no round is open, nothing reads this handle's exit, and
     `b.Round` may be 0.
   - `internal/relevo/headless.go:1144` `stopProcess`: take
     `b store.Binding` instead of `e store.Endpoint`, keep the early return when
     `b.Builder` has no live headless process, and call
     `Kill(ctx, handleOf(b.Builder), rt.Store.BuilderStreamPath(b.Name, b.Round))`.
     Update the four callers to pass `b`: `bind.go:810`, `done.go:74`,
     `headless.go:1025`, `stop.go:207`.
   - `internal/consult/reconcile.go:106`: `c.Endpoint.LogPath`.
   - `internal/proc/proc_test.go` lines 143, 157, 170: the `stream` from `start`.
   - `internal/e2e/headless_test.go:898`: `rt.Store.BuilderStreamPath(name, b.Round)`.
   - `internal/relevo/fake_test.go:841`: a literal path such as
     `"/state/webshop/002-builder.jsonl"`, and assert it landed in
     `f.killStreams` next to the existing `f.kills` assertion.
4. Wiring assertions (one line each), which must pass before the record exists:
   - `internal/relevo/stop_test.go`, in `TestStopHeadlessKillsAndClosesWithoutSwitch`'s
     "no report" subtest after the `fr.kills` check: `fr.killStreams` is exactly
     `[rt.Store.BuilderStreamPath("webshop", b.Round)]`.
   - `internal/relevo/consult_test.go`, in `TestHeadlessConsultTimesOut` after
     the `len(fr.kills)` check: `fr.killStreams[0]` is `c.Endpoint.LogPath`.

Done when `go vet ./...` is clean -- it compiles the test files as well, so it
catches all six fakes -- and
`go test ./internal/relevo/ -run 'Stop|Consult|FakeRunner' -count=1` passes.
Nothing behaves differently yet: the real `Kill` ignores `streamPath`.

### Step 2 -- the record

1. New `internal/proc/kill_record.go` (`//go:build unix`) with
   `killRecordSuffix`, `killRecordPath`, `recordKill`, `killRecorded` and
   `parseKillRecord`, exactly as section 5 declares them. Keep each comment a
   *why*.
2. `internal/proc/proc.go`: `ExitCode` takes `h`, checks `killRecorded(h,
   logPath)` first and returns `(0, false)`; replace the comment's last sentence
   (section 5). `Kill` takes `streamPath` and calls `recordKill(h, streamPath)`
   after the `!alive` return and before the SIGTERM, returning its error.
3. New `internal/proc/kill_record_test.go` (`//go:build unix`) with:
   - `TestParseKillRecord`: the table -- `"4242 1700000000\n"` and the same
     line without its newline (both parse), a huge pid, `""`, one field, three
     fields, a non-numeric pid, a non-numeric start, and a negative start
     (parses; it can never match a real handle).
   - `TestKilledProcessReadsUnknownEvenWhenTheStreamEndsInATrailer` (the
     deterministic test that fails when the fix is reverted): `r := New()`,
     `r.KillGrace = 2 * time.Second`, `start(t, r, "sleep", "60")`, then write
     `"partial\n" + spawn.ExitTrailer + "143\n"` into the stream by hand (what a
     racing shell leaves), `Kill(ctx, h, stream)`, assert `os.Stat(killRecordPath(stream))`
     succeeds, and assert `ExitCode(ctx, h, stream)` is `ok == false`.
   - `TestKillOnADeadProcessRecordsNothing`: `start(t, r, "sh", "-c", "exit 7")`,
     `waitGone`, `Kill(ctx, h, stream)` (returns nil), assert no
     `killRecordPath(stream)` exists and `ExitCode` still reads `7, true`.
   - `TestASecondProcessOnTheSameStreamKeepsItsOwnTrailer`: two `Start`s on one
     `StreamPath` -- the first `sleep 60`, killed; the second
     `sh -c "exit 5"`, awaited -- assert `ExitCode` for the *second* handle reads
     `5, true` although the first handle's record is still beside the stream.
   - `TestABuilderKilledByASignalLeavesItsCode`: `start(t, r, "sh", "-c", "kill
     -KILL $$")`, `waitGone`, assert `ExitCode` reads `137, true`: a builder
     killed by a signal while its supervisor lives keeps its trailer, which is
     the premise the oom re-queue rests on. (The self-exit half of this contract
     is already pinned by `TestStartCapturesBothStreamsAndTheExitTrailer`.)
4. Update the two live kill tests in `internal/proc/proc_test.go`: pass `stream`
   at lines 143, 157 and 170, and reword
   `TestKilledSupervisorNeverWritesTheTrailer`'s comment: the kill record, not
   the trap, is what makes `ok=false` hold on a platform where a shell defers
   its trap. Keep the 20 iterations and the assertion exactly as they are.

Done when `go test ./internal/proc/ -count=1` passes, including the four new
tests and both live kill tests.

### Step 3 -- mutation checks

Run each, name the failing test in the report, then restore:

1. Delete the `killRecorded` early return in `ExitCode`:
   `TestKilledProcessReadsUnknownEvenWhenTheStreamEndsInATrailer` must fail.
2. Move `recordKill` above the `!alive` return in `Kill`:
   `TestKillOnADeadProcessRecordsNothing` must fail (both assertions).
3. Make `killRecorded` ignore the handle identity (return the parse's `ok`
   without comparing; keep its two values referenced so the package still
   compiles): `TestASecondProcessOnTheSameStreamKeepsItsOwnTrailer` must fail.

### Step 4 -- full check and commit

1. `make e2e` once, then `make check` once (section 8).
2. Copy this plan verbatim to
   `docs/plans/2026-09-27-killed-supervisor-trailer.md`.
3. One commit: subject
   `fix(proc): a killed supervisor leaves no exit trailer, whatever its shell wrote`,
   with `Fixes #629` in the body.

## 10. Report

Include: the files changed; the exact text of the record path and line for a
real round; the four new test names and what each pins; the three mutation
checks with the test that failed; the `make check` and `make e2e` results; the
commit sha; and anything in sections 1-2 that the code contradicted.
