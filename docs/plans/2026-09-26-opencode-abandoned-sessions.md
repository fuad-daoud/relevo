# Round 1 plan

# OpenCode sessions relevo abandons are deleted, so nothing resumes them

Fixes GitHub issue #570. Commit message prefix: `fix(opencode):`.

## Hard rules for this round (read first)

- **You are an OpenCode builder. Never run `opencode` yourself** — not
  `opencode run`, not `opencode session ...`, not `opencode api ...`. Never
  run `pkill`, `killall`, `kill` on anything you did not start in this
  round, and never `tmux kill-server`. Every test uses a fake `usage.Exec`;
  no test may execute a real harness binary (CI has none).
- If a step is impossible as written, or the code contradicts what this
  plan says is there, **stop and report**. Do not improvise a different design.
- Code style: CLAUDE.md in the repo root. No issue numbers, "round N" or
  history in code or test names. Comments say *why*, only where the code
  cannot. Functions at most 70 lines; the new file stays under 600 lines.
  Never add a lint or file-size exclusion.

## 1. System overview

OpenCode 2.x keeps every session in its shared database
(`~/.local/share/opencode/opencode.db`, table `session_v2`). While a session
runs, `time_suspended` is set. A session whose process dies mid-run keeps
that mark. When OpenCode's background service (`opencode serve --service`)
next starts, for example after a reboot, it resumes every such session in
place, in its original directory. relevo only tracks the `opencode` pid, so
a session relevo has written off keeps editing the worktree.

relevo makes this worse after a restart: it resumes a lost OpenCode builder
with `--session X --fork`, which makes a **new** session and leaves X marked
as running. Each reboot added one more writer. The incident had up to five.

The planner verified these facts by experiment on OpenCode 2.0.18:
- `opencode session delete --standalone <id>` removes the session in about
  0.7 s, with no background service involved.
- Deleting a *running* session stops it at its next step. The tool call
  already in flight finishes, then the run fails with
  `Session not found: <id>` and exits.
- Deleting an id that does not exist prints `Session not found: <id>` and
  exits 1.
- A private database (`OPENCODE_DB` or a separate `XDG_DATA_HOME`) does not
  work: provider routing lives in the database, so runs fail with "Model
  unavailable".
- `opencode api session.interrupt` on a session that is not running returns
  `{"interrupted":false}` and changes nothing.

**The fix.** Whenever relevo stops using an OpenCode builder session while
its round is still open, it records the session on the binding as
*abandoned*. The daemon then deletes each abandoned session **outside the
state lock**. The `relevo stop`, `relevo done` and `relevo unbind` commands
also delete them directly, so the fix works with no daemon running. relevo
already keeps the round's full transcript in its own stream, so no history
is lost.

A session is not deleted while a live process on that binding has not yet
announced its own session. Reason: a `--fork` resume reads the original
session when it starts, so deleting the original first would break the fork.

Only OpenCode sessions are recorded. claude and agy continue a session in
place, and codex has no resume; none of them restart a session on their own.

## 2. What is deleted, and what is not (closed lists)

**Recorded as abandoned (exactly these points, nothing else):**
1. A local lost-to-restart builder (the `lost && switchable` branch, local
   half): the session it had before the resume or relaunch. This happens
   whether the resume succeeds, falls back to a fresh launch, or halts.
2. A served lost-to-restart builder (the same branch, `b.Owner != ""`
   re-queue half): its session.
3. `switchBuilder`: the old builder's session, after the `closeOld` kill
   block. Also on its two early halts (switch limit reached, cannot
   resolve), but only when `b.Builder.PID == 0`, meaning no live process.
4. `Stop`, `stopKill` case: the killed builder's session.
5. `Done`: the stopped builder's session, only when a process was actually
   stopped (`stopErr == nil && pid != 0`).
6. The stray-process kill in `reconcileHeadless` (the `!roundOpen` branch
   with `b.Builder.PID != 0`), after a successful kill.
7. `Unbind`: the stopped builder's session (only when `pid != 0`) plus
   everything already in `b.AbandonedSessions`. These are deleted directly,
   because the record is about to be removed.

**Never recorded:**
- the session of a round that closed with a report, because `relevo ask`
  forks it later;
- `nudgeResume` (the original session is idle and is forked);
- consult processes (`consult.go`);
- gate processes;
- the spawn rollback in `send.go`, whose process never announced a session.

This round deletes no existing behaviour. No existing test is deleted. A
test whose binding comparison now also sees `AbandonedSessions`, or an empty
`StreamSessionID` after a stop, gets its expected value updated. Name every
such test in your report.

## 3. File structure

```
internal/harness/resume.go          + DeleteSession, ErrSessionDeleteUnsupported
internal/harness/resume_test.go     + DeleteSession tests (create the file if absent,
                                      or add to the existing harness test that covers Resume)
internal/store/binding.go           + Binding.AbandonedSessions field
internal/store/log.go               + AbandonedSession type (next to BuilderSession)
internal/relevo/abandon.go          NEW: recording, the reaper, reapAbandoned, reapAll
internal/relevo/abandon_test.go     NEW: tests for everything in abandon.go
internal/relevo/runtime.go          + Runtime.SessionReaper field
internal/relevo/headless.go         call sites 1, 2, 6
internal/relevo/switch.go           call site 3
internal/relevo/stop.go             call site 4 + reap after the lock
internal/relevo/status.go           call site 5 + reap after the lock
internal/relevo/bind.go             call site 7 (Unbind)
internal/relevo/daemon.go           reap phase in Tick
internal/serve/serve.go             Config.SessionReaper -> runtimeAt
cmd/relevo/wire.go                  wire relevo.NewSessionReaper(binExec{})
cmd/relevo/serve.go                 wire it in the Runtime at line ~230 and serve.Config at ~487
docs/plans/2026-09-26-opencode-abandoned-sessions.md   this plan, copied verbatim (last step)
```

## 4. Data structures

### `store.AbandonedSession` (internal/store/log.go, next to `BuilderSession`)
| field | type | json | meaning |
|---|---|---|---|
| `Kind` | string | `kind` | harness kind; always `"opencode"` today |
| `ID` | string | `id` | the harness session id, e.g. `ses_...` |
| `Attempts` | int | `attempts,omitempty` | failed delete attempts so far |

Constraints: Kind and ID are non-empty. At most one entry per (Kind, ID).

### `store.Binding.AbandonedSessions`
`AbandonedSessions []AbandonedSession \`json:"abandoned_sessions,omitempty"\``,
placed right after `RoundExcluded`. Its comment must say *why*: these are
harness sessions relevo stopped using, which the harness would otherwise
resume on its own, and the daemon deletes them. Do **not** bump
`BindingFormat`. The field is omitempty, so existing records and the
remote-wire goldens are unchanged while it is empty. If a golden test
changes anyway, stop and report.

`SameBinding` compares by JSON, so it needs no change.

### `relevo.Runtime.SessionReaper`
`SessionReaper SessionDeleter`. Nil means deletes are skipped and
abandoned entries stay on the binding; log once at debug level. Every
existing test gets nil, so existing behaviour is unchanged.

## 5. Interfaces and contracts

### harness (internal/harness/resume.go)
- `var ErrSessionDeleteUnsupported = errors.New(...)`. Message: the
  harness's sessions end with their process.
- `func (h Harness) DeleteSession(sessionID string) ([]string, error)`
  - Returns the argv *after* the binary. The caller prepends `h.Binary`, as
    for `Resume`.
  - opencode: exactly `["session", "delete", "--standalone", sessionID]`.
    `--standalone` matters: going through the background service could
    start that service, and on startup it resumes every session marked
    running.
  - Any other kind: `ErrSessionDeleteUnsupported` (wrapped, naming the kind).
  - Validates the id with the existing `checkResumeSessionID`, first.

### relevo (internal/relevo/abandon.go)
- `type SessionDeleter interface { DeleteSession(ctx context.Context, s store.AbandonedSession) error }`
  - Postcondition: nil means the session no longer exists, whether deleted
    now or already gone.
- `func NewSessionReaper(x usage.Exec) SessionDeleter` returns the real
  implementation.
  - It looks up the harness with `harness.Lookup(s.Kind)`, gets the argv
    from `DeleteSession`, and runs `x.Run(ctx, h.Binary, argv...)` under a
    30 s timeout.
  - An error whose text contains `Session not found` is mapped to nil.
  - An unknown kind, or `ErrSessionDeleteUnsupported`, returns an error.
- `func abandonSessionID(b store.Binding, kind, id string) store.Binding`, pure.
  - Appends `{Kind: kind, ID: id}` only when all of these hold: `id != ""`;
    `kind` names a harness; that harness's `DeleteSession(id)` returns no
    error; and the (kind, id) pair is not already present.
  - Returns the binding otherwise unchanged.
- `func abandonSession(b store.Binding) store.Binding`, pure.
  - When `b.Builder.Headless()`, it calls
    `abandonSessionID(b, b.Builder.Kind, b.Builder.StreamSessionID)`, then
    sets `b.Builder.StreamSessionID = ""`.
  - Clearing the id is deliberate. The stopped round's report entry, built
    by `builderSessionOf` in `queueReport`, then names no session instead
    of one that is about to be deleted.
- `func reapable(b store.Binding) bool`, pure.
  - False when `b.Builder.PID != 0 && b.Builder.StreamSessionID == ""`,
    meaning a live process has not announced its session yet. A `--fork`
    resume reads the original when it starts.
  - Otherwise true when `len(b.AbandonedSessions) > 0`.
- `const reapMaxAttempts = 5`
- `func reapAbandoned(ctx context.Context, rt Runtime, name string)`
  - Loads the binding with `rt.Store.Load`, with no lock held.
  - Returns if the binding is not found, `!reapable(b)`, or
    `rt.SessionReaper == nil`.
  - Calls `DeleteSession` for each entry, **with no state lock held**.
  - Then, inside one `rt.Store.WithLock`, it reloads the binding. For each
    entry: deleted → removed; failed → `Attempts++`; if `Attempts` reaches
    `reapMaxAttempts` → removed, with `slog.Warn` naming the kind and id so
    a human can delete it.
  - Entries added between the unlocked load and the lock are kept
    untouched. Match by (Kind, ID).
  - Saves only when the binding changed.
  - Logs each delete (Info) and each failure (Warn). Returns nothing;
    errors are logged.
- `func reapAll(ctx context.Context, rt Runtime, bindings []store.Binding)`
  calls `reapAbandoned` for each binding with non-empty `AbandonedSessions`.

## 6. Pseudocode per call site

### Call site 1: local lost-to-restart (internal/relevo/headless.go, the `if lost && switchable` block, local half, ~lines 857-916)
```
oldKind := b.Builder.Kind            // capture next to `sess := b.Builder.StreamSessionID`
... existing resume / relaunch switch, unchanged ...
if err != nil:
    b = abandonSessionID(b, oldKind, sess)
    return haltBinding(ctx, rt, b, <same message>)
b = next
b = abandonSessionID(b, oldKind, sess)      // the new process has StreamSessionID "", so reapable waits for it
... rest unchanged (RoundStartedAt keep, log entry, StateActive) ...
```
The `staleBuilder` branch goes through `switchBuilder` (call site 3); do not
add anything there.

### Call site 2: served lost-to-restart (same block, `if b.Owner != ""` half, ~lines 836-853)
Before `b.Builder = clearProcess(b.Builder)`: `b = abandonSession(b)`.

### Call site 3: switchBuilder (internal/relevo/switch.go, `switchBuilder`, lines 97-181)
- Right after the `if closeOld { ... }` block and before
  `old := b.BuilderCandidate`: `b = abandonSession(b)`.
- In the two early `haltBinding` returns (the limit check, and the
  `resolveRole` error): before each, `if b.Builder.PID == 0 { b = abandonSession(b) }`.

### Call site 4: Stop (internal/relevo/stop.go, `Stop`, `case stopKill:` ~lines 141-148)
After `b.Builder = clearProcess(b.Builder)`: `b = abandonSession(b)`.
After `rt.Store.WithLock(...)` returns with no error and `out.Action == "killed"`:
`reapAbandoned(ctx, rt, name)`. Its errors are only logged and never
change `Stop`'s result.

### Call site 5: Done (internal/relevo/status.go, `Done`, ~lines 1055-1058)
```
pid, stopErr := stopProcess(...)
if stopErr == nil:
    b.Builder = clearProcess(b.Builder)
    if pid != 0: b = abandonSession(b)
```
After `WithLock` returns with no error: `reapAbandoned(ctx, rt, name)`.
Only logged; it never changes `Done`'s result.

### Call site 6: stray kill (internal/relevo/headless.go, `!roundOpen` branch, ~lines 614-627)
In the `else` of the successful kill (next to the "killed stray headless
process" log line): `b = abandonSession(b)`.

### Call site 7: Unbind (internal/relevo/bind.go, `Unbind`, ~lines 785-790)
Right after the `stopProcess` block and before `worktreeTeardown`:
```
if pid stopped != 0: b = abandonSession(b)
for each s in b.AbandonedSessions:
    if rt.SessionReaper == nil: break
    if err := rt.SessionReaper.DeleteSession(ctx, s); err != nil:
        slog.Warn("abandoned harness session not deleted", kind, id, err)
```
Unbind holds no state lock here. Nothing is saved, because the record is
deleted or archived right after. `UnbindResult` is unchanged.

### Daemon (internal/relevo/daemon.go, `Tick`, ~lines 171-186)
After the `fresh` list is read, before `ingest`:
`d.safely("reap sessions", func() { reapAll(ctx, d.rt, fresh) })`.

### Wiring
- `cmd/relevo/wire.go` at the `rt := relevo.Runtime{` literal (~line 348):
  `SessionReaper: relevo.NewSessionReaper(binExec{})`.
- `cmd/relevo/serve.go`: the `relevo.Runtime{` literal at ~line 230 gets the
  same field. The `serve.Config{` literal at ~line 487 gets
  `SessionReaper: relevo.NewSessionReaper(binExec{})`.
- `internal/serve`: add `SessionReaper relevo.SessionDeleter` to `Config`,
  and pass it through in `runtimeAt` (serve.go ~line 214). Leave
  `ledgerRuntime` and `serveAdminConfig` alone.

## 7. Error handling

- A delete failure is recoverable. The entry stays with `Attempts++` and is
  dropped after `reapMaxAttempts`, with a Warn naming the session. It never
  fails a tick, a stop, a done or an unbind.
- `Session not found` counts as success; it keeps the delete idempotent.
- `rt.SessionReaper == nil`: skip, and keep the entries.
- No `opencode` exec ever runs while the state lock is held.
- Accepted window: if the background service resumes a session before
  relevo deletes it (a reboot where the service starts first), the resumed
  session runs until the delete, and its in-flight tool call finishes.
  State this in one comment on `reapAbandoned`.

## 8. Working efficiently

Each model step costs a round trip, so:
- Read the files named here in one batch of parallel reads first:
  `internal/harness/resume.go`, `internal/store/binding.go` (lines 75-160),
  `internal/store/log.go` (lines 115-130), `internal/relevo/headless.go`
  (lines 600-630 and 780-920), `internal/relevo/switch.go`,
  `internal/relevo/stop.go` (lines 60-190), `internal/relevo/status.go`
  (lines 1000-1080), `internal/relevo/bind.go` (lines 754-800),
  `internal/relevo/daemon.go` (lines 141-190), `internal/serve/serve.go`
  (the `Config` struct and lines 205-230), `cmd/relevo/wire.go`
  (lines 340-380), `cmd/relevo/serve.go` (lines 225-240 and 480-495).
  Do not search for what this plan already located.
- Make all changes to a file in one edit where you can.
- Focused loop, fixing every error before rerunning:
  `go build ./... && go test ./internal/harness/ ./internal/store/ -count=1 && go test ./internal/relevo/ -run 'Abandon|Reap|Lost|Restart|Resume|Switch|Stop|Done|Unbind|Stray' -count=1`
- Then run `go test ./internal/relevo/ ./internal/serve/ ./cmd/relevo/ -count=1` once.
- Full check once, at the end: `make check`. Also run `gofmt -l .`, which
  must print nothing.

## 9. Ordered implementation steps

1. **harness.DeleteSession** (resume.go).
   Tests pin:
   - the opencode argv, exactly;
   - `ErrSessionDeleteUnsupported` via `errors.Is` for claude, agy and codex;
   - an empty id and an id starting with `-` are rejected.

   Done when `go test ./internal/harness/` passes.
2. **store types**: `AbandonedSession` in log.go, and the
   `Binding.AbandonedSessions` field.

   Done when `go test ./internal/store/` passes unchanged.
3. **abandon.go**: `SessionDeleter`, `NewSessionReaper`,
   `abandonSessionID`, `abandonSession`, `reapable`, `reapMaxAttempts`,
   `reapAbandoned`, `reapAll`; plus `Runtime.SessionReaper` in runtime.go.
   Tests in abandon_test.go, using a fake `usage.Exec` and a fake
   `SessionDeleter`, pin:
   - the reaper passes `opencode session delete --standalone <id>` to Exec;
   - `Session not found: x` maps to nil, and another error is returned;
   - `abandonSession` records opencode and ignores claude;
   - `abandonSession` dedups, clears `StreamSessionID`, and ignores an
     empty id and a non-headless builder;
   - `reapable` is false for PID set with an empty session, and true after
     the session is announced;
   - `reapAbandoned` removes deleted entries, increments `Attempts` on
     failure, and drops an entry at the 5th failure;
   - `reapAbandoned` keeps an entry added concurrently;
   - no state lock is held during the delete: the fake deleter calls
     `rt.Store.WithLock` itself and must not deadlock (bound the test with
     a timeout);
   - `reapAbandoned` skips when `SessionReaper` is nil.
4. **Call sites 1, 2, 6** (headless.go).
   Tests (in abandon_test.go, or next to the existing restart-resume tests)
   pin:
   - after a lost-to-restart resume of an opencode round,
     `AbandonedSessions` holds exactly the old session and the new process
     is not yet reapable;
   - the served re-queue records the session;
   - a stray kill records it.

   Update the expected values of existing restart tests only where they now
   see the new field.
5. **Call site 3** (switch.go). Tests pin:
   - a gate-driven `closeOld` switch records the old opencode session;
   - a limit halt with PID 0 records it.
6. **Call sites 4 and 5** (stop.go, status.go). Tests pin:
   - stop of a running opencode round records the session;
   - the stopped round's report entry names no session;
   - `SessionReaper` is called after the lock is released;
   - done with a live process records and reaps;
   - done between rounds (pid 0) records nothing.
7. **Call site 7** (bind.go). Tests pin:
   - unbind of a running opencode round calls the deleter for the current
     session and for earlier abandoned ones;
   - a deleter error does not fail the unbind.
8. **Daemon reap phase** (daemon.go). A test pins that a `Tick` over a
   binding with an abandoned session and no live process calls the deleter
   and saves the emptied list.
9. **Wiring** (serve.go Config and runtimeAt, cmd/relevo/wire.go,
   cmd/relevo/serve.go).

   Done when `go build ./...` and `go test ./internal/serve/ ./cmd/relevo/`
   pass. No cmd/relevo test may execute a real `opencode`.
10. **Mutation check.** Delete the `abandonSessionID(b, oldKind, sess)` line
    after `b = next` in call site 1 and confirm a named test fails. Then
    make `reapable` always return true and confirm a named test fails.
    Restore both. Name both tests in the report.
11. **Full check.** Run `make check` and `gofmt -l .`. If the coverage guard
    reports a drop, add tests. Never lower the baseline.
12. **Ship the plan.** Copy this file verbatim to
    `docs/plans/2026-09-26-opencode-abandoned-sessions.md`. Commit
    everything on the current branch in one commit, subject
    `fix(opencode): delete the OpenCode sessions relevo abandons so nothing resumes them`,
    with `Fixes #570` in the body.

## 10. Report

Include:
- the files changed;
- every existing test whose expected value you updated, with a one-line
  reason each;
- the two mutation checks and the test that failed for each;
- the `make check` result.

---

## Round 2 correction

`store.BindingFormat` was bumped to 6. Adding `Binding.AbandonedSessions`
changes the binding's JSON shape, and this repo's rule
(`internal/store/format.go`) is to bump `BindingFormat` whenever that shape
changes, so an older relevo refuses to save a binding whose rewrite would
erase fields it does not know. Round 1's instruction not to bump
`BindingFormat` was the planner's mistake; this section replaces it.
`recordFormat` is unchanged -- a record is still written at format 1 or 2 --
and the new field is `omitempty`, so the bump costs an older relevo nothing
until a session is abandoned.
