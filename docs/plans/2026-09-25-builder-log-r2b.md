# builder.log round 2b: stop writing the log

Date: 2026-09-25. Binding blog2, branch relevo/blog2. It follows round 2a (commit 632a4ed6 on
this branch, reviewed and green).
Spec: docs/specs/2026-09-25-builder-log-design.md (§4.4, §4.5).

After this round a new round has no `NNN-builder.log`:

- the builder's stderr goes into its stream;
- the drain no longer writes a log;
- the `--- relevo … ---` marker lines are gone.

The readers from 2a render the stream. A round that already has a `NNN-builder.log`
(history, or a round in flight across the upgrade) keeps using it. **This round opens the
PR** for 2a + 2b.

**Stop rather than improvise.** If a step contradicts the code, or an existing test outside
§7.3 fails, halt and report it. Do not bend a test.

## 1. The rule: a round that has a builder.log keeps it

`legacyLog(rt Runtime, name string, round int) bool` (headless.go, new, pure apart from one
`os.Stat`):

- It is true exactly when a file exists **on disk** at `rt.Store.BuilderLogPath(name, round)`.
- A new round never has one, because nothing creates it any more.
- A round started by an older relevo has one, because proc opened it for stderr at spawn.
- Every decision in this round uses this one rule.

## 2. Changes

### 2.1 startProcess (headless.go, around the `logPath := rt.Store.BuilderLogPath(...)` line ~278)

- `logPath := rt.Store.BuilderStreamPath(b.Name, b.Round)`, unless
  `legacyLog(rt, b.Name, b.Round)`. In that case keep `rt.Store.BuilderLogPath(b.Name, b.Round)`.
- The ProcSpec gets `LogPath: logPath` (so stderr joins the stream, as consults do since
  #420) and `StreamPath` unchanged.
- `b.Builder.LogPath = logPath`, as today.
- Update the comments that say stderr goes to the log.
- `Endpoint.LogPath`'s doc (store/types.go ~128) becomes: "the file the current process's
  stderr is appended to: the round's stream (Store.BuilderStreamPath), or, for a round that
  already had a NNN-builder.log when the process started, that log; "" between rounds."

### 2.2 drainStream / drainFile (headless.go ~375-460)

- drainStream passes `rt.Store.BuilderLogPath(b.Name, round)` when `legacyLog(rt, b.Name, round)`,
  otherwise "".
- drainFile skips the append when `logPath == ""` and still advances the offset. Everything
  else is unchanged: the segment kinds, the session id guard, partial lines and the cursor.
- Update drainStream's doc: it renders into the log only for a round that has one;
  otherwise it only advances the cursor and captures the session id.

### 2.3 Markers are removed

- Delete `appendLogMarker` (headless.go ~999) and every call:
  - headless.go ~872 (lost to a daemon restart; the `KindSwitch` entry at ~851-866 stays);
  - headless.go ~1030 (`stopProcess`);
  - limit.go ~370 (`gateOnLimit`);
  - switch.go ~165;
  - send.go ~405 and ~561.
- No other change at those sites.
- In `stopProcess`, remove the marker line only. Its doc comment should no longer mention a
  marker.

### 2.4 `relevo done` records the stop in the ledger

- `relevo stop` already appends `store.LogEntry{…, Kind: store.KindStop, Note: "stopped/" + how, Confirmed: true}`
  (stop.go ~218-224). Copy its exact field set.
- In `Done` (status.go ~999), when `stopProcess` returned a non-zero pid and a nil error,
  append the same entry with `how = "done"` through the `tx` Done already holds, for
  `b.Round`.
- `unbind` gets no entry: its record is archived or deleted in the same call, and
  `UnbindResult.ProcessStopped` already reports it.

### 2.5 Docs

- store.go `BuilderLogPath` doc: "the builder log of a round from before builder-log round 2
  (stderr plus the rendered stream). New rounds have none: their stderr goes into the
  stream, and readers render it (relevo.RoundTranscript). Kept for history and for a round
  in flight across the upgrade."
- README.md ~620-626: the passage saying stderr goes to `NNN-builder.log` becomes:
  - stdout and stderr both go to `NNN-builder.jsonl`;
  - `relevo show <name> --round N --transcript` renders it;
  - rounds from older relevo versions keep their `NNN-builder.log`.

  Check README ~1028 ("builder log and stream") and adjust it if it claims every round has
  a log.
- CLAUDE.md:28 becomes: "A headless round's output is its stream
  `~/.local/state/relevo/<name>/NNN-builder.jsonl` (stderr included; sealed into the
  database after the round). Read it rendered with `relevo show <name> --round N --transcript`.
  Rounds from before #<this PR> also have `NNN-builder.log`."
  - Write the PR number in after `gh pr create`, or say "before builder-log round 2" if
    you cannot.

### 2.6 Strengthen 2a's N2 (the uncaught M3)

- In `TestStreamTail` (transcript_test.go ~106), add one case whose last 64 KiB renders to
  fewer than 40 lines. For example, use lines of about 8 KiB of assistant text, so one
  rendered line per stream line.
- `streamTail(…, 40)` must still equal the last 40 lines of `renderStream`.
- With the fixed-window mutation (2a M3) this case must fail.

## 3. Pseudocode

```
legacyLog(name, round) = exists on disk(BuilderLogPath(name, round))
startProcess: logPath = legacyLog ? BuilderLogPath : BuilderStreamPath;  spec{LogPath: logPath, StreamPath: stream}; b.Builder.LogPath = logPath
drainStream:  drainFile(legacyLog ? BuilderLogPath : "", stream, ...)   // "" = advance only
Done:         if stopped pid: AppendLog(KindStop "stopped/done")
markers:      gone
```

`builderTail` from 2a sees `LogPath` equal to the stream path, so it reads the stream's
rendered tail. The UI rule 1 from 2a sees `LogPath` equal to the stream path, so it falls
to `RoundTranscript`. Nothing else changes.

## 4. Steps

1. §2.1 and §2.2, with N1-N4 and N7.
2. §2.3, with the fenced test edits in §6 (markers).
3. §2.4, with N6.
4. §2.6 (N8).
5. §2.5, the docs.
6. Full check:
   - `make check`; if a hook blocks it, run its steps and paste `gofmt -l`'s empty output;
   - then `make e2e`, which runs one headless round end to end with the fake harness;
   - then the §7 mutations, one at a time, restoring after each.
7. Save this plan verbatim at `docs/plans/2026-09-25-builder-log-r2b.md`.
8. Commit and open the PR.
   - One commit on top of 632a4ed6 (do not squash the branch): `feat(headless): new rounds
     write no builder.log -- stderr goes into the stream and readers render it; markers
     become ledger entries`.
   - Run `git push`, then `gh pr create --base main` with the title `builder.log round 2:
     readers render the stream; new rounds write no builder.log`.
   - The body summarises 2a and 2b, and lists every mutation result of 2a (M1-M10, with M3
     now caught by N8) and 2b.
   - Then fill in CLAUDE.md's PR number and amend **only if** you can do it before
     pushing. Otherwise leave the wording without a number.

## 5. Working efficiently

- Read these in one parallel batch:
  - headless.go 270-300, 370-460, 850-880 and 990-1035
  - status.go 999-1090
  - stop.go 210-230
  - limit.go 360-375
  - switch.go 155-170
  - send.go 395-410 and 550-570
  - transcript_test.go 100-200
  - the tests named in §6
  - README.md 615-630 and 1020-1032
  - CLAUDE.md 20-32
- Focused runs:
  - `go test ./internal/relevo/ -run 'Drain|StartProcess|StartRound|Switch|Done|Stop|Limit|Exit|Budget|Permission|Status|Tail|Transcript|LostToDaemon' -count=1`
  - `go test ./internal/ui/ ./internal/serve/ -count=1`

## 6. Tests

### New

- **N1 `TestStartProcessSendsStderrToTheStream`:** a new round. `spec.LogPath ==
  spec.StreamPath == BuilderStreamPath(name, round)`, `b.Builder.LogPath` equals it, and no
  `BuilderLogPath` file exists afterwards.
- **N2 `TestStartProcessKeepsALegacyRoundsLog`:** with a file at `BuilderLogPath(name,
  round)`, both `spec.LogPath` and `b.Builder.LogPath` are that path.
- **N3 `TestDrainWritesNoLogForANewRound`:** stream lines, no log file. After drainStream,
  no log file exists, `StreamOffset` equals the stream's size, and `StreamSessionID` is
  captured from a session-announcing line.
- **N4 `TestDrainKeepsAppendingALegacyLog`:** with an (empty) log file present, drainStream
  appends the rendered lines to it, exactly as before.
- **N6 `TestDoneRecordsTheStop`:** `relevo done` on a headless binding with a live process.
  The ledger gets a `KindStop` entry with Note `stopped/done`. On an idle binding (pid 0)
  it gets none.
- **N7 `TestStderrLimitTextStillDetected`:**
  - A new-layout round (N1 shape). Its stream has JSON lines, then a raw stderr line
    `error: Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 1h0m0s.`,
    then `relevo-exit:1`.
  - `builderTail(rt, b, limitScanLines)` contains the stderr line, and
    `matchLimit(<that tail>, agy patterns, now, 0)` matches.
  - This is the spike's point: stderr-only agy limits must survive the move.
- **N8:** the strengthened `TestStreamTail` case (§2.6).

### Fenced: the only existing tests you may change

Name each change in the report, citing the item.

- **(a) Markers deleted (§2.3):**
  - delete `TestSwitchBuilderHeadlessMarksTheLog`, `TestSwitchBuilderHeadlessMarkerSurvivesStartFailure`,
    `TestStopProcessMarksUnbind`, `TestStopProcessKillFailureWritesNoMarker`,
    `TestStopProcessIdleWritesNoMarker` and `TestAppendLogMarker`;
  - `TestReconcileHeadlessLostToDaemonRestartResumesSession`: remove only the assertion
    that the log contains the restart marker. If the test does not already assert the
    `KindSwitch` entry note containing "lost to a daemon restart", add that assertion in
    its place.
  - `TestGateOnLimit`: remove only the assertion that the log's last line carries the
    `rate-limited:` marker (~367-374).
- **(b) Done:** port `TestDoneHeadlessMarksTheLog` to assert the `KindStop` entry
  (`stopped/done`) instead of a marker. Rename it `TestDoneHeadlessRecordsTheStop`, or
  merge it into N6 and say so.
- **(c) Layout (§2.1):** `TestStartRoundRecordsTheHandleAndTheLogPath` expects the stream
  path as `LogPath`, and `spec.LogPath == spec.StreamPath`.
- **(d) Seeded builder output.** These tests may have their seeded text **moved from
  BuilderLogPath into the round's stream file** (BuilderStreamPath). Keep each seeded
  line's order, and keep its position relative to any `relevo-exit:` trailer, which must
  stay last. Their assertions must not change, except (d′) below.
  - Only if a test fails because the text sits in the old log:
    - `TestReconcileHeadlessExitEntryCarriesTheRenderedResult`
    - `TestReconcileHeadlessExitWithoutReportLogsAndSwitches`
    - `TestReconcileHeadlessExitOnLimitGatesAndSwitchesUncounted`
    - `TestReconcileHeadlessExitWithReportOnLimitGatesAndClosesUnmarked`
    - `TestReconcileHeadlessExitPermissionBlockedHalts`
    - `TestReconcileHeadlessBudgetOnLimitKillsAndSwitches`
    - `TestReconcileHeadlessBudgetWithoutLimitStillHalts`
    - `TestStatusHeadlessWorkingShowsPidAndLogTail`
  - (d′) an exit entry's `Path` equal to the log path becomes the stream path.
- **(e) Drain tests that assert log content:** they may create an empty
  `BuilderLogPath` file before draining (a legacy round). Their assertions stay. Only if
  they fail without it:
  - `TestDrainStreamRendersNewLinesInOrderAndAdvances`
  - `TestDrainStreamWaitsForAPartialLine`
  - `TestDrainStreamCursorSurvivesAReload`
  - `TestDrainStreamCursorPastEndRendersFromTheStart`
  - `TestDrainStreamKeepsGoingAfterAMarkerClose`
  - `TestDrainRendersEachSegmentWithItsKind`
  - `TestDrainSessionIDComesOnlyFromTheCurrentProcess`
  - `TestReconcileHeadlessExitEntryCarriesTheRenderedResult`
- **(f)** `TestStreamTail`: only the added case (§2.6).

Any other existing test failing means halt. That includes the UI, serve, remote, e2e,
show and proc tests.

## 7. Mutation checks (report each, restore)

| # | Break | Must fail |
|---|---|---|
| M1 | startProcess: always `BuilderLogPath` for stderr | N1 |
| M2 | startProcess: ignore legacyLog (always the stream) | N2 |
| M3 | drainStream: always pass `BuilderLogPath` | N3 |
| M4 | drainStream: always pass "" | N4 |
| M5 | Done: skip the `KindStop` entry | N6 |
| M6 | streamTail: one fixed 64 KiB window (2a's M3) | N8 |
| M7 | builderTail: render the stream without the raw stderr line, e.g. drop non-`{` lines in renderStreamFrom | N7 |

If a mutation does not make its named test fail, report it. Do not strengthen tests
beyond this plan.

## 8. Scope check

- `git diff --stat 632a4ed6..HEAD` covers:
  - headless.go, status.go, limit.go, switch.go, send.go, store.go, types.go;
  - transcript_test.go, headless_test.go, limit_test.go and other tests named in §6;
  - README.md, CLAUDE.md;
  - this plan.
- No golden changes. No `BindingFormat` change.
