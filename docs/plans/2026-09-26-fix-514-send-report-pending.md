# Fix #514 -- `relevo send` refuses a round whose report is on disk but not yet delivered

## 0. Rules for this round

- Before anything else: `git fetch origin && git merge --ff-only origin/main`.
  If the fast-forward fails, stop and report. (`main` now contains the stale
  builder token change: `internal/relevo/stale.go`, and a `pf.staleToken`
  guard around the under-lock `roundOpenIn` check in `send.go`. Keep it intact.)
- If a step is impossible as written, or the code contradicts this plan, **stop
  and report** -- do not improvise.
- **Run every check in the foreground and wait for it.** Never run `make check`
  or a test as a background task, and never end your turn to wait for one:
  this process exits when your turn ends and nothing will wake it. Your turn
  ends only after the report is written and the done marker exists.
- Comments: *why* only, no issue numbers, no `§`, no history. No new
  `.golangci.yml` exclusion or allow-list entry. Functions stay <= 70 lines.
- `cmd/relevo` tests must not spawn a harness or reach the network. This round
  adds no `cmd/relevo` test.

## 1. System overview

A round closes when the daemon ingests the builder's `NNN-done` marker (or,
for an exited builder, its `NNN-report.md`): `closeOnMarker`
(`internal/relevo/reconcile.go` ~297-340) or the "exited with a report but no
marker" branch of `reconcileHeadless` (`internal/relevo/headless.go`
~732-765) call `queueReport`, which queues the report for the planner and
increments `b.Round`.

`Send` never increments the round: it stages the plan at `PlanPath(name,
b.Round)` and starts a builder. Between the builder writing its files and the
daemon ingesting them (seconds normally, minutes when the database is busy),
`b.Round` still names the finished round and the builder's process is dead,
so `Send` passes every check, overwrites `NNN-plan.md`, and starts a second
builder in a round that already has a done marker and a report. The daemon
then closes that round on the stale marker and delivers the old report.

The fix: `Send` refuses while the current round has `NNN-done` or
`NNN-report.md` on disk. The refusal writes nothing, and the planner retries
after `relevo wait` delivers the report (by then `b.Round` is N+1 and has no
such files). A round whose builder exited *without* writing either file is
still re-sendable, as today.

The remote path needs no client change: a remote send runs `relevo.Send` on
the server (`internal/serve/rounds.go` ~260), which will refuse the same way;
the server maps the new sentinel to `409` with `remote.CodeRoundOpen`.

## 2. File structure

```
internal/relevo/headless.go      new sentinel ErrReportPending next to ErrBuilderBusy (~line 29)
internal/relevo/send.go          new helper pendingRoundFile; two call sites (preflight + under lock)
internal/relevo/send_test.go     new tests next to TestSendBuilderRefusedWhileRoundOpen (~1045)
internal/serve/rounds.go         map ErrReportPending -> 409 CodeRoundOpen (~265-277)
internal/serve/serve_test.go     one test of that mapping, next to the existing CodeRoundOpen tests (~2061, ~2160)
docs/plans/2026-09-26-fix-514-send-report-pending.md   this plan (last step)
```

No other file changes.

## 3. Data and contracts

```
var ErrReportPending error   // internal/relevo/headless.go, beside ErrBuilderBusy
  message: "the round's report is on disk but not yet delivered; relevo wait delivers it, then send the next plan"

pendingRoundFile(rt Runtime, name string, round int) (path string, found bool)
  // internal/relevo/send.go
  Returns the first of DonePath(name, round), ReportPath(name, round) that
  exists on disk (os.Stat err == nil). Any stat error (including not-exist)
  counts as absent. Pure read; no lock needed.
```

Refusal error, wrapped so `errors.Is(err, ErrReportPending)` holds:
`fmt.Errorf("binding %q round %d: %s exists: %w", name, b.Round, filepath.Base(path), ErrReportPending)`

## 4. Pseudocode

```
sendPreflight (send.go, right after the "previous process still alive"
check that returns ErrBuilderBusy, ~line 249-257):
  if path, found := pendingRoundFile(rt, name, b.Round); found:
      return preflight{}, refusal(path)

Send, under the lock (send.go, right after the same ErrBuilderBusy check
inside rt.Store.WithLock, ~line 353-363), against the freshly loaded b:
  if path, found := pendingRoundFile(rt, name, b.Round); found:
      return refusal(path)
  // The re-check matters: the daemon may close the round between preflight
  // and the lock -- then b.Round is N+1 here and the send proceeds normally.

serve rounds.go, in the sendErr mapping, beside the ErrBuilderBusy case:
  if errors.Is(sendErr, relevo.ErrReportPending):
      writeErr(w, 409, remote.CodeRoundOpen, sendErr.Error()); return
```

Order within each site: the new check comes after the alive check (a live
builder is still reported as ErrBuilderBusy) and before anything is staged,
spawned or logged.

## 5. Tests (`internal/relevo/send_test.go`, next to `TestSendBuilderRefusedWhileRoundOpen`)

Use `switchSetup` and the fake runner as that test does. For each case, prime
a Send of round 1, then make the fake runner report the round-1 process as
exited (find how existing tests do this; if the fake runner cannot express a
dead process, stop and report).

1. `TestSendRefusedWhileDoneMarkerNotIngested`: write `DonePath("webshop", 1)`
   and `ReportPath("webshop", 1)`; Send a second plan with no options.
   Want: `errors.Is(err, ErrReportPending)`; error text names `001-done`;
   log length unchanged; `001-plan.md` unchanged; no new process spec.
2. `TestSendRefusedWhileReportNotIngested`: only `ReportPath` exists.
   Same assertions; error names `001-report.md`.
3. `TestSendAfterExitWithoutReportStillResends`: neither file exists.
   Want: Send succeeds, round stays 1, a second process spec was started.
   (This pins the resend of a round whose builder died without a report.)
4. `TestSendProceedsOnceRoundClosed`: write both files, close the round the
   way `reconcile_test.go`'s `closeOnMarkerUnderLock` helper does (read it;
   reuse it if it is in the same package), then Send. Want: success, the plan
   is staged at `002-plan.md`, `001-plan.md` unchanged.

`internal/serve/serve_test.go`: one test that drives the round-start
endpoint for a binding whose current round has `NNN-done` on disk and a dead
builder, and asserts `409` + `remote.CodeRoundOpen`. Model it on the test
around line 2061. If staging that state through the served path needs more
than the existing helpers, stop and report instead of building new fixtures.

Mutation check: delete the under-lock call only, run test 1 -- it must still
fail on the preflight (so also delete the preflight call and confirm test 1
fails); restore both. Then delete only the `rounds.go` mapping and confirm the
serve test fails; restore.

## 6. Error handling

- `ErrReportPending` is recoverable by the caller: wait for delivery, resend.
- A stat error other than not-exist counts as absent -- the check must never
  block a send because of an unreadable directory; the later staging write
  will surface a real I/O problem.
- If you find a code path that legitimately leaves `NNN-done` or
  `NNN-report.md` for the *current* round on disk after the round closed (so
  a binding would be refused forever), stop and report it with file:line.

## 7. Working efficiently

- Read in one batch: `send.go` 110-420, `headless.go` 20-40, `reconcile.go`
  290-345, `serve/rounds.go` 240-300, `send_test.go` 1000-1090,
  `serve_test.go` 2020-2170.
- One edit call per file.
- Focused loop: `go build ./... && go test ./internal/relevo -run 'Send' -count=1 && go test ./internal/serve -run 'RoundOpen|Start' -count=1`.
- Full check once, at the end, in the foreground: `make check`.

## 8. Ordered steps

1. Fast-forward to origin/main (§0).
2. Sentinel + helper + both call sites in `send.go` (§3, §4). Build passes.
3. `rounds.go` mapping. Build passes.
4. Tests (§5). Focused tests pass.
5. Mutation checks (§5). Report each failing test name.
6. `make check` in the foreground. `git diff --stat` shows only §2 files.
   Commit: `fix(send): refuse a round whose report is on disk but not yet delivered (#514)`.
7. **Save the plan.** Copy this plan to
   `docs/plans/2026-09-26-fix-514-send-report-pending.md` and commit it.

Report: the diff stat, the mutation checks' failing test names, and the
`make check` result.
