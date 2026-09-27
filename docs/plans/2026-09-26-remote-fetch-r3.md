# #535 -- the round-close ack runs outside the state lock

Plan for one builder round. Save this plan verbatim as
`docs/plans/2026-09-26-remote-fetch-r3.md` in the last step and commit it with the
change.

## 0. Facts, verified against origin/main `0c8da53`

The builder's branch is cut from origin/main. Confirm the base first:

```
git log -1 --format=%H
# expect 0c8da53745d44aa9e64050adea3b10862b961c92
```

If HEAD is a later commit, check that every quoted snippet below still matches at
its `file:line` (within a few lines). If a quote does not match, **halt and
report**; do not improvise.

### 0.1 What already landed -- do not redo it

1. **Read verbs skip the remote sync while a daemon runs.** `SyncRemoteUnlessDaemon`
   (`internal/relevo/remote_sync.go:466-476`) is called by `Wait`
   (`internal/relevo/wait.go:198`) and by `cmd/relevo/status.go:94`. When
   `Store.DaemonRunning()` is true, nothing is fetched and no lock is taken. This is
   issue step 1 (PR #538).
2. **The daemon fetches a remote binding before its lock.** `internal/relevo/daemon.go:293`
   `pre := d.prefetchRemote(ctx, name)`, then `daemon.go:296` takes the lock;
   `prefetchRemote` (`daemon.go:347-365`) runs `fetchRemote`
   (`internal/relevo/remotefetch.go:54-82`), which does `GetBinding`
   (`remotefetch.go:60`), the log range read (`remotefetch.go:79` ->
   `fetchLogMirror` `:100`), the drift read (`remotefetch.go:80` -> `fetchDrift`
   `:181`) and the whole catch-up download (`remotefetch.go:66-74` ->
   `fetchCatchUp` `:353`, `:365-499`) outside the lock. (`#547`, `#555`.)
3. **`SyncRemote` does the same.** `internal/relevo/remote_sync.go:427` fetches,
   `:428` is the lock; `applyRemote` runs inside it.

### 0.2 What still runs HTTP under the lock -- this round's target

`applyCatchUpSettle` (`internal/relevo/remote_catchup.go:91-103`) acks inside the
caller's transaction. Exact current code:

```go
// applyCatchUpSettle carries out the apply half's last steps: it records the
// absorbed result, acks the round, and queues the report entry. A failed ack
// leaves the round for the next tick exactly as before.
func applyCatchUpSettle(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, view remote.BindingView, cf *catchUpFetch) (store.Binding, error) {
	n := view.ClosedRound
	server, name := b.Builder.Server, b.Name

	b.Builder.LastKnown = view.ResultCommit
	b.RemoteAbsorbFailures = 0

	if _, err := rt.Remote.Ack(ctx, server, name, n); err != nil {
		slog.Warn("ack failed", "server", server, "name", name, "round", n, "err", err)
		return b, nil
	}
	return applyCatchUpReport(ctx, rt, tx, b, view, cf)
}
```

Its only caller is `applyCatchUp` (`internal/relevo/remotefetch.go:570-584`),
reached from `applyRemoteView` (`internal/relevo/remote_sync.go:327-334` for
`RoundClosed`, `:344-353` for the lost-ack `RoundIdle` recovery) while
`daemon.go:296` or `remote_sync.go:428` holds the lock. The client's `Ack` has a
30 s deadline (`internal/remote/client/bindings.go:95`), so a slow server holds the
machine-wide state lock for up to 30 s per closed round on the daemon tick and on
every no-daemon sync -- the last such hold on the observe paths the issue names.

**Verdict: not fixed. Plan only the ack.** Everything else the issue lists
(`GetBinding`, the log/drift mirror, the catch-up downloads) is already out of the
lock on the daemon and `SyncRemote` paths, as quoted in 0.1.

### 0.3 Residuals this round does not touch (name them in the report, do not fix)

- `internal/relevo/stop.go:72` holds the lock across `WhoAmI` (`:90`), `Stop` (`:97`)
  and the inline `observeRemote` (`:126`) for one `relevo stop` command.
- The legacy on-disk log mirror still fetches under the lock
  (`internal/relevo/remote_sync.go:313-314` -> `mirrorLog` `:56-112`), for a round
  whose `NNN-builder.log` file exists (started by an older relevo).
- `deliverAndSettle` can POST to a planner's opencode session under the lock
  (`internal/relevo/remote_sync.go:392` -> `internal/relevo/reconcile.go:620` ->
  `internal/delivery/deliver.go:68`; 5 s timeout,
  `internal/delivery/deliver_opencode.go:24`). That is delivery, not observation.

## 1. Rules for this round

- Run every check in the foreground and wait for it. Never background `make check`
  or a test: this process exits when the turn ends, and nothing will wake it. End
  the turn only after the report and the done marker exist.
- This is a **move of where one call runs**, not a behaviour change: same order
  (absorb -> ack -> queue the report), same retry behaviour (a failed ack queues no
  report, and the next pass re-collects the still-closed round and re-acks), same
  log line.
- Every existing test must pass **unchanged**. If one fails, stop and report which
  test and why; do not edit its assertions. (The 188 `reconcile(t, rt, b)` call
  sites and the daemon/`SyncRemote` tests are the guard.)
- Touch only the files in section 2. Anything else: halt and report.
- Comments say *why* only: no issue numbers, no plan or round names, no "used to".
  Functions <= 70 lines, files <= 600. Never add a lint/size/coverage exclusion and
  never lower `testdata/coverage-baseline.txt`.
- Tests: `internal/relevo` tests never construct an HTTP client and never reach the
  network. The blocking fake here is `fakeRemote`, a Go double; it pins the same
  property an httptest server would with no HTTP stack. Do not add an httptest
  server to this package. No `cmd/relevo` test is added.
- If a step is impossible as written or contradicts the code, halt and report.

## 2. File structure

```
internal/relevo/remote_catchup.go     catchUpAck type + matches; ackCatchUp; applyCatchUpReport
                                      takes the settle; catchUp wrapper and settleCatchUpInline
internal/relevo/remotefetch.go        remoteFetch.Settle field; applyCatchUp (already here) returns
                                      *catchUpAck; applyCatchUpSettle is deleted from remote_catchup.go
internal/relevo/remote_sync.go        applyRemote/applyRemoteErr/applyRemoteView take *remoteFetch;
                                      observeRemote settles inline; reconcileRemote skips delivery
                                      while a settle is pending; SyncRemote settles after its lock;
                                      settleCatchUp
internal/relevo/daemon.go             tickOne settles after its lock
internal/relevo/remote_test.go        fakeRemote.Ack calls beforeCall
internal/relevo/remotefetch_test.go   the new tests of section 7
docs/plans/2026-09-26-remote-fetch-r3.md   this plan (last step)
```

No other file changes. `cmd/relevo` and the client are untouched.

## 3. Data structures & type definitions

### 3.1 `remoteFetch` gains one field (internal/relevo/remotefetch.go:22-32)

Current struct, to be extended:

```go
// remoteFetch is what the fetch half read from the server for one binding,
// without the state lock, for the apply half to act on under it.
type remoteFetch struct {
	Name    string             // b.Name at snapshot
	Server  string             // b.Builder.Server at snapshot
	Round   int                // b.Round at snapshot
	View    remote.BindingView // GetBinding's result; zero when Err != nil
	Err     error              // GetBinding's error, classified by the apply half exactly as today
	Log     *logMirror         // nil: nothing fetched to write (not running, legacy log, error, no growth)
	Legacy  bool               // the round's log is the legacy file form: apply runs the legacy mirror inline
	Drift   []byte             // nil: nothing fetched
	CatchUp *catchUpFetch      // nil: no catch-up was fetched
}
```

New field, appended with its doc comment:

```go
	// Settle is what the apply half still owes once the lock is released: the
	// closed round's ack, which must not run under the lock, and the report the
	// ack gates. The apply half writes it; the caller that held the lock runs it
	// (settleCatchUp, or settleCatchUpInline when it still holds the lock).
	Settle *catchUpAck
```

The struct stays a value type; `release` (`remotefetch.go:35-40`) is unchanged and
stays nil-safe.

### 3.2 New type `catchUpAck` (internal/relevo/remote_catchup.go)

```go
// catchUpAck is a catch-up the apply half installed whose ack has not left yet.
// The ack runs outside the state lock; only a successful one unblocks the
// report entry.
type catchUpAck struct {
	Server       string             // b.Builder.Server at the apply
	Name         string             // b.Name at the apply
	Round        int                // view.ClosedRound: the round to ack and to file the report under
	BindingRound int                // b.Round at the apply: guards the report against a binding that moved on
	View         remote.BindingView // the closed-round facts the report entry is built from
	HaveReport   bool               // a report temp was renamed into place
	HaveDiff     bool               // the diff body was stored
}
```

`matches` is the phase-B guard (a small method, near the type):

```go
// matches reports whether b is still the binding the ack was for: same name,
// same round, still remote, and in a known live state. A false answer means the
// report is not queued; the pass that moved the binding owns it.
func (a *catchUpAck) matches(b store.Binding) bool
```

Body: `b.Name == a.Name && b.Round == a.BindingRound && b.Builder.Remote() &&
b.State != store.StateDone && b.State != store.StatePaused &&
store.KnownState(b.State)` -- the same shape as `remoteFetch.matches`
(`internal/relevo/remotefetch.go:209-217`).

Nothing is persisted: the settle lives for one pass in memory only. No binding
field, no JSON change, no migration.

## 4. Interface definitions & component contracts

### 4.1 `applyCatchUp` (internal/relevo/remotefetch.go:570-584, body kept, return type extended)

```go
func applyCatchUp(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding,
	view remote.BindingView, cf *catchUpFetch) (store.Binding, *catchUpAck, error)
```

Precondition: caller holds the state lock; `cf` was produced by `fetchCatchUp` for
`view`. Postcondition, in the same order as today:
`Abort` -> `(b, nil, nil)`; `ReportMissing && view.Stopped == ""` -> halt through
`haltBinding` -> `(next, nil, err)`; `applyCatchUpFiles` false -> `(b, nil, nil)`;
`applyCatchUpAbsorb` stop -> `(next, nil, err)`; otherwise set
`b.Builder.LastKnown = view.ResultCommit` and `b.RemoteAbsorbFailures = 0` and
return `(b, &catchUpAck{...}, nil)` filled from `b`, `view` and `cf`
(`HaveReport: cf.ReportTemp != ""`, `HaveDiff: cf.Diff != nil`). It performs **no**
ack and queues **no** report. `applyCatchUpSettle` is deleted; its three jobs move
to 4.2/4.3/4.4.

### 4.2 `ackCatchUp` (new, remote_catchup.go)

```go
// ackCatchUp acks a collected round outside the state lock. A failure is the
// same retry as before: warned here, no report queued, the still-closed round
// re-collected by the next pass.
func ackCatchUp(ctx context.Context, rt Runtime, a *catchUpAck) error
```

Body: `rt.Remote.Ack(ctx, a.Server, a.Name, a.Round)`; on error, the existing
`slog.Warn("ack failed", "server", ..., "name", ..., "round", ..., "err", err)`
with `a`'s fields, and return the error.

### 4.3 `applyCatchUpReport` (remote_catchup.go:108, parameter change only)

```go
func applyCatchUpReport(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding,
	a *catchUpAck) (store.Binding, error)
```

Body unchanged from today except: `n := a.View.ClosedRound`; the diff path is taken
when `a.HaveDiff`; `catchUpPayload(b, a.View, a.HaveReport)`; every other use of
`view` reads `a.View`. This is the phase-B body: it runs under a fresh lock.

### 4.4 The two settle entry points (remote_catchup.go, remote_sync.go)

```go
// settleCatchUpInline finishes a catch-up for a caller that still holds the
// lock: today's one-pass behaviour, for the inline paths.
func settleCatchUpInline(ctx context.Context, rt Runtime, tx *store.Tx,
	b store.Binding, a *catchUpAck) (store.Binding, error)
```

Body: if `ackCatchUp` fails, `(b, nil)`; else `applyCatchUpReport(ctx, rt, tx, b, a)`.

```go
// settleCatchUp finishes a catch-up after the lock was released: the ack, then
// the report under a fresh lock, guarded against a binding that moved on.
// reconcile is true for the daemon's tick, which also delivers the queued
// payload and emits the mutation events a single-pass reconcile used to emit;
// the read verbs collect without either.
func settleCatchUp(ctx context.Context, rt Runtime, a *catchUpAck, reconcile bool) error
```

Body (contract, not code):
1. `ackCatchUp` failure -> log already done, return `nil` (the pass owes nothing
   else; the next pass re-collects the still-closed round).
2. Else `rt.Store.WithLock`: load `a.Name`; `ErrNotFound` -> `nil` (unbind raced);
   other load error -> return it; `!a.matches(cur)` -> return `nil` (a pass that
   moved the binding owns it, and the next pass collects a report still missing).
3. `applyCatchUpReport(ctx, rt, tx, cur, a)`; on error return it.
4. If `reconcile`: `next = deliverAndSettle(ctx, rt, tx, next)` (same error rule: an
   error returns before the save, so this phase's writes are discarded and retried
   next pass, exactly as a delivery error behaves today), then
   `emitMutations(ctx, rt, cur, next)` -- this is what keeps
   `hooks.EventRoundStarted` firing when a remote round closes, because
   `queueReport` advances `Round` inside this second lock instead of inside
   `reconcileWith`'s one (test `TestReconcile_EmitsRoundStartedOnReport` pins the
   local path; do not break it).
5. `store.SameBinding(next, cur)` -> `nil`, else `tx.Save(next)`.

Postcondition: the report entry is queued exactly once per collected round. Two
processes that both fetched the same round both ack (the server's ack is
idempotent), but only the first to take the lock queues the report; the second sees
`b.Round` advanced and `matches` false.

### 4.5 The inline wrapper `catchUp` (remote_catchup.go:18-22)

Signature unchanged: `(ctx, rt, tx, b, view) (store.Binding, error)`. Body: fetch
(`fetchCatchUp` + `defer cf.release()`), then `applyCatchUp`, then, when a settle
came back, `settleCatchUpInline`. This keeps `observeRemote`'s existing callers
(one-off stop, `Reconcile` inline) exactly as they behave today.

### 4.6 Pointer plumbing in remote_sync.go

Signatures change from value to pointer, nothing else:

```go
func applyRemote(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding,
	f *remoteFetch) (store.Binding, bool, error)
func applyRemoteErr(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding,
	err error, now time.Time) (store.Binding, bool, error)
func applyRemoteView(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding,
	f *remoteFetch, now time.Time) (store.Binding, bool, error)
```

`observeRemote` (`:364-366`) keeps its signature and becomes: build `f` with
`fetchRemote`, call `applyRemote(..., &f)`, and if `f.Settle != nil` finish the
binding with `settleCatchUpInline` before returning `(next, deliver, err)`.

`applyRemoteView`'s two catch-up branches (`:327-334`, `:344-353`) become:

```
if f.CatchUp != nil && f.CatchUp.Round == view.ClosedRound {
    next, a, err := applyCatchUp(ctx, rt, tx, b, view, f.CatchUp)
    f.Settle = a
    return next, true, err
}
next, err := catchUp(ctx, rt, tx, b, view)     // unchanged inline fallback
return next, true, err
```

No other line of `applyRemoteView` changes.

### 4.7 `reconcileRemote` (remote_sync.go:372-393)

Signature unchanged. Only the tail changes: after the `err != nil || !deliver`
return, if `pre != nil && pre.Settle != nil`, return `(next, nil)` **without**
`deliverAndSettle` -- the report is not queued yet, so there is nothing to deliver;
the caller settles and, for the daemon, delivers after. The `pre == nil` inline
path is untouched (its settle was finished inline).

### 4.8 `SyncRemote` (remote_sync.go:408-456)

One addition in the loop, after `f.release()`:

```
if err == nil && f.Settle != nil {
    err = settleCatchUp(ctx, rt, f.Settle, false)
}
```

`synced` counting, the discard branch and the error join (`%s: %w` with the binding
name) are unchanged. A failed ack contributes no error (it is warned and retried).

### 4.9 `tickOne` (daemon.go:279-339)

The lock call changes from `return d.rt.Store.WithLock(...)` to assign the named
return `err` (`err = d.rt.Store.WithLock(...)`), then:

```
if err != nil { return err }
if pre != nil && pre.Settle != nil {
    return settleCatchUp(ctx, d.rt, pre.Settle, true)
}
return nil
```

The deferred `recover` and the named `err` keep containing a panic in the settle
too. `pre.release()` stays deferred where it is: the settle holds no temp paths, so
releasing after it is correct (files were renamed in the first phase; release only
removes what a failed apply left).

## 5. High-level pseudocode

```
daemon tickOne(name):
  pre = prefetchRemote(name)                  # no lock: GetBinding, log, drift, catch-up downloads
  defer pre.release()
  err = WithLock:                             # PHASE A
      loaded = load(name); guards; sealRounds
      next = reconcileWith(loaded, pre)       # apply half; on a closed round:
                                              #   files installed, bundle absorbed,
                                              #   LastKnown = view.ResultCommit
                                              #   pre.Settle = catchUpAck (no ack, no report)
      if changed: save(next)
  if err != nil: return err
  if pre.Settle != nil:
      return settleCatchUp(pre.Settle, reconcile=true)
          ackCatchUp(pre.Settle)              # NO LOCK, 30 s deadline
              on failure: warn, return nil    # nothing else is owed
          WithLock:                           # PHASE B
              if !settle.matches(load()): return nil
              next = applyCatchUpReport(...)  # diff entry, payload, queueReport, stop entry, idle
              next = deliverAndSettle(next)   # the daemon's delivery, as before
              emitMutations(phase-B binding, next)
              save(next)

sync path SyncRemote:
  for each remote live binding:
      f = fetchRemote(b)                      # no lock
      err = WithLock:                         # PHASE A, same as the tick
          fresh = load; if !f.matches(fresh): discard
          next, _, err = applyRemote(fresh, &f); save
      f.release()
      if err == nil && f.Settle != nil:
          err = settleCatchUp(f.Settle, reconcile=false)   # ack, then report; no delivery
      collect err

inline paths (unchanged behaviour, ack still under the caller's lock):
  catchUp(b, view):   cf = fetchCatchUp(b, view); next, a, err = applyCatchUp(cf)
                      if a != nil: settleCatchUpInline(a)   # ack + report in the caller's tx
  observeRemote(b):   f = fetchRemote(b); next, deliver, err = applyRemote(&f)
                      if f.Settle != nil: settleCatchUpInline(f.Settle)
```

Order and retry, stated once more: absorb (phase A, committed) -> ack (no lock) ->
report (phase B). Ack fails: no report, the binding keeps `LastKnown`, the server
stays Closed, the next pass builds a fresh catch-up (`fetchRemote`
`remotefetch.go:66-69` is unconditional for `RoundClosed`) and acks again. Process
dies between A and B: same recovery, one redundant download. Binding moves on
between A and B (`matches` false): no report queued and no `Round` advance; if the
binding is live, the `RoundIdle` + missing-report branch (`remote_sync.go:344-353`)
re-collects it, exactly the existing lost-ack recovery.

## 6. Error handling

- Categories unchanged: `Abort`/halt/absorb failures return before the settle and
  behave exactly as today; the ack's transport errors are `client.ErrUnreachable`,
  `client.HTTPError`, `ErrCertChanged`, all treated as one warn-and-retry (as
  today's `ack failed` branch).
- Recoverable: every ack failure, every phase-B load/queue failure (retried next
  pass; the round stays closed on the server until acked).
- Non-recoverable in this path: nothing new. `queueReport` errors propagate to the
  caller as they do today (`Tick` logs "reconcile failed"; `SyncRemote` joins the
  binding's error).
- The phase-B guard turning false is not an error: it logs nothing at Info; a
  `slog.Debug` is acceptable if the surrounding style wants one.
- No log line changes: `ack failed` keeps its message and fields; no new slog at
  Warn in the happy path.
- Observability: `Ack` calls remain visible in `fakeRemote.calls` in tests; the
  daemon's existing `slog.Error("reconcile failed", ...)` covers a settle error.

## 7. Tests and what each pins

Add to `internal/relevo/remotefetch_test.go` (helpers `lockFreeWithin` `:19`,
`assertNoFetchTemps` `:322` live there) and `internal/relevo/remote_test.go`.

First, one test-only change: `fakeRemote.Ack` (`remote_test.go:175-179`) gains the
`beforeCall` hook the other methods have:

```go
func (f *fakeRemote) Ack(ctx context.Context, server, name string, round int) (remote.BindingView, error) {
	call := fmt.Sprintf("Ack:%s:%s:%d", server, name, round)
	if f.beforeCall != nil {
		f.beforeCall(call)
	}
	f.calls = append(f.calls, call)
	return f.ackResp, f.ackErr
}
```

Existing tests do not set `beforeCall` on an ack and do not match the `Ack:` prefix
in their assertions (checked), so this changes nothing for them.

1. **`TestTickAcksWithoutTheStateLock`** (remotefetch_test.go). Pins: a closed
   round's ack runs with the state lock free, **after** the apply committed, and
   the report is queued afterwards.
   Setup: `store.New(t.TempDir())`, `Save(remoteBinding("zen"))`, and
   `fr := roundClosedRemote()` with `fr.roundBundleResp = nil` (no bundle, so no
   transport is needed); its `roundFileFunc` already answers the report and 404s
   the rest.
   `fr.beforeCall` on the `Ack:` call: (a) `st.Load("api")` must show
   `Builder.LastKnown == "c0ffee"` -- the ack is after the commit; (b)
   `lockFreeWithin(t, st, 2*time.Second)` must be true. Run
   `NewDaemon(rt, time.Second).Tick(ctx)`; then assert the report entry exists,
   `Round == 2`, `countCalls(fr, "Ack:") == 1`, `assertNoFetchTemps`.
2. **`TestSyncRemoteAcksWithoutTheStateLock`** (remotefetch_test.go). Same fake,
   same assertions, driven by `SyncRemote(ctx, rt)`. Pins the no-daemon read path,
   which is a different call site.
3. **`TestCatchUpAckFailureLeavesTheReportUnqueued`** (remote_test.go). Pins: the
   report waits for the ack, and the next pass retries.
   Setup: closed-round binding, no bundle, report file available, `fr.ackErr =
   errors.New("boom")`, daemon Tick. Assert: no `KindReport` entry, `Round == 1`,
   `Builder.LastKnown == "c0ffee"` (phase A committed), `Ack` calls == 1. Then clear
   `ackErr`, tick again. Assert: exactly one `KindReport` entry, `Round == 2`,
   `Ack` calls == 2.
4. **`TestCatchUpSettleSkipsAChangedBinding`** (remote_test.go). Pins the phase-B
   guard: a binding that moved on between the ack and the report keeps its new
   state and gets no report entry.
   Setup: closed-round binding; `fr.beforeCall` on `Ack:` sets `State = done` on the
   stored binding with `st.Load` + `st.Save` (no lock -- a `WithLock` here would
   self-deadlock in a mutated build and hang instead of failing). Run `SyncRemote`.
   Assert: state still `done`, `Round == 1`, no `KindReport` entry. Note in the
   test comment (why only) that the downloaded report file is left in place and the
   existing Idle recovery collects a live binding on its next pass.

Every new test gets a comment in the neighbouring style saying what it pins and,
like the existing ones, `t.Parallel()` where the package's tests use it. Fixtures
keep invented names (`zen`, `api`, `c0ffee`).

**Mutation checks (do them, restore, name the test in the report):**

- A. Call `ackCatchUp` from inside `settleCatchUp`'s `WithLock` closure (i.e. put
  the ack back under the lock) -> `TestTickAcksWithoutTheStateLock` must fail on the
  lock-free assertion.
- B. In `settleCatchUp`, ignore the ack error and run phase B anyway ->
  `TestCatchUpAckFailureLeavesTheReportUnqueued` must fail on the report count.
- C. Drop the `a.matches(cur)` guard in `settleCatchUp` ->
  `TestCatchUpSettleSkipsAChangedBinding` must fail.

## 8. Working efficiently

- Read once, in this order: `internal/relevo/remote_catchup.go` (all 181 lines),
  `internal/relevo/remotefetch.go` 20-40 and 560-584, `internal/relevo/remote_sync.go`
  160-200 and 250-476, `internal/relevo/daemon.go` 279-345,
  `internal/relevo/remotefetch_test.go` 300-500. Everything you need is named
  above; do not re-find it.
- Make each file's whole change in one edit call. The mechanical part (the
  `cf *catchUpFetch` -> `a *catchUpAck` substitution in `applyCatchUpReport`) is a
  single edit of that function's header plus its four field uses; no script needed.
- Focused loop while working, foreground, one run per fix:
  `go build ./... && go test ./internal/relevo -run 'Remote|CatchUp|Ack|Tick|Sync|Fetch' -count=1`
- Then once: `go test ./internal/relevo -count=1 -race`
- Full check once, at the end, foreground: `make check`
  (it runs `gofmt` over tracked files, `go vet`, the tidy check, golangci-lint,
  `scripts/check-comments.sh`, `scripts/check-filesize.sh` and the coverage gate).
  Also run `gofmt -l .` and `sh scripts/check-comments.sh` explicitly and see both
  empty/quiet. Do not run `make e2e`.
- If coverage moves, add tests for the new branches; never edit the baseline.

## 9. Ordered implementation steps

1. **Step 1 -- the settle type and the inline path.** Add `catchUpAck` and its
   `matches`; add `ackCatchUp` and `settleCatchUpInline`; change `applyCatchUp`
   (remotefetch.go:570-584) to return `*catchUpAck` and delete `applyCatchUpSettle`
   (remote_catchup.go:88-103); change `applyCatchUpReport`'s parameter to `*catchUpAck`;
   rewrite the `catchUp` wrapper to fetch -> apply -> `settleCatchUpInline`; and adapt
   `applyRemoteView`'s two catch-up sites (`remote_sync.go:327-334`, `:344-353`) to
   call `applyCatchUp` and then `settleCatchUpInline` when a settle came back, so
   every path still acks under the caller's lock.
   Deliverable: the package builds and behaves exactly as before; every existing
   test passes.
   Verify: focused loop command above; `go test ./internal/relevo -run 'CatchUp|ObserveRemote' -count=1 -v` shows the catch-up tests green.
2. **Step 2 -- the move.** Add `remoteFetch.Settle` (section 3.1);
   `applyRemote`/`applyRemoteErr`/`applyRemoteView` take `*remoteFetch`; the two
   catch-up sites set `f.Settle` and return without settling; `observeRemote`
   settles inline (it holds the caller's lock); `reconcileRemote` skips
   `deliverAndSettle` while a settle is pending; `settleCatchUp` is added;
   `SyncRemote` settles after `f.release()`; `tickOne` settles after its lock.
   Deliverable: the ack runs after the lock on the daemon and `SyncRemote` paths;
   the report is queued in the second locked phase.
   Verify: focused loop; `TestTickCatchesUpWithoutTheStateLock`,
   `TestObserveRemoteIdleCatchUpRecoversLostReport`, `TestCatchUpOrderAndIdempotence`
   and the `reconcile(t, ...)` suite all pass unchanged.
3. **Step 3 -- the ack test hook and the four tests of section 7.**
   Verify: focused loop with `-run 'Ack|CatchUp|Tick|Sync' -count=1`; then the
   race run once.
4. **Step 4 -- mutation checks A, B, C.** Restore each after confirming the named
   test fails. Record the test names.
5. **Step 5 -- full check.** `make check` in the foreground, plus `gofmt -l .` and
   `sh scripts/check-comments.sh`. `git diff --stat` must show only the files in
   section 2.
6. **Step 6 -- the plan doc and the commit.** Copy this plan verbatim to
   `docs/plans/2026-09-26-remote-fetch-r3.md`, `git add` the sources, the tests and
   the doc, and commit:
   `fix(remote): the round-close ack runs outside the state lock (#535)`.
   Do not push.

## 10. Report (final message)

Report: the commit sha; `git diff --stat`; the mutation results (which named test
failed for A, B and C); confirmation that no existing test was edited; the
`make check`, `gofmt -l .` and `sh scripts/check-comments.sh` results; the
residuals of section 0.3 as deliberately not done; and any halt.

End of plan.
