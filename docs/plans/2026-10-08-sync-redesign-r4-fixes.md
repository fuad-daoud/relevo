# Sync redesign R4 fixes: drain, bounds, one worker

The round plans of this slice of the sync redesign, in the order they ran,
as the builders received them. Spec: `docs/specs/2026-10-07-sync-redesign-design.md`.

---

<!-- r5/round-0.md -->

# Sync fix round: drain a backlog, size the bounds

Branch: this chain's base is the sync chain head after its disable/retry and
status rounds (both landed); read their code first, adapt names, and use the
`retry` verb and `db sync status` as they now exist.

## MasterMind amendments (these override the plan below)

- **FIRST, and the most important step: no worker outside the daemon.**
  `internal/ui/actions_sync.go` builds its own `relevo.VerbRunner` with its
  own `syncpipe` supervisor and calls `runner.Run`/`runner.Probe` in the
  cockpit's process. The comment there claims the verb is serialized
  daemon-side; it is not sent to the daemon at all. Now that push, pull,
  test and disable do real work, a cockpit action starts a second worker on
  the same `relevo-sync.db` the daemon's worker holds open (two sync engines
  on one file, the failure that corrupted relevo.db before this redesign),
  imports into relevo.db from the cockpit process, and a cockpit disable
  deletes the replica under the daemon's worker. Fix: every cockpit sync
  action sends the verb to the daemon over the owner socket, exactly as
  `cmd/relevo/db_sync_call.go` `sendSyncVerbToken` does (`shared.SyncVerb`);
  the test-connection action becomes a daemon verb too (add a wire verb only
  if no existing one fits; a probe that reads stats moves nothing). Pin it
  with a `go list -deps` style test that `internal/ui` does not import
  `internal/syncpipe` or `internal/syncworker`, and a test that each cockpit
  action reaches the owner's sync-verb hook (fake owner), not a local runner.
  Fix the wrong comment.
- **Disable during a join.** A disable while an enable's join runs in the
  daemon stops that join (cancel its worker), and disable clears the
  `sync.join` marker, so status never shows a join on a machine that is off.
  Pin both.
- **Trouble that does not vanish.** The steady attempt overwrites the
  `sync.trouble` marker every success, so a dropped batch (never re-reported:
  the mark moved past it) shows for one tick and disappears. Keep dropped
  reports cumulative (capped, newest kept, e.g. 50) until `relevo db sync
  retry` clears them; held and gaps stay per-run (they recur while true). With
  the import loop, aggregate trouble across the loop's runs. Pin: a drop
  survives a later clean attempt and is cleared by retry.
- **Dead remote stats.** `KeyStats` (`sync.stats`) is read by status and the
  `:sync` view (last push/pull, sent/received, revision) but nothing writes it
  any more. Either the steady attempt records the worker's `stats` there, or
  the dead lines, the reader and the `Measured` placeholder logic go. Pick the
  smaller, say which; no screen line may show a value nothing writes.
- **Step 1 never halts.** Report the tursogo facts (partial progress kept on
  kill for bootstrap and for Pull? progress exposed? file:line), then go on.
  If bootstrap or Pull starts over after a kill, that is the reason the data
  bound must cover a whole transfer, not a reason to stop.
- **Values.** Data-call bound (hello, pull, export/append) = 10 minutes; the
  comment derives it (a ~300 MB first bootstrap or join export at ~1 MB/s,
  plus margin). Control calls (stats, head, shutdown) keep 30 s. The steady
  data-step bound sits just below the data-call bound (e.g. 9 min) so the
  deliberate cancel wins. Import-loop deadline: steady 4 minutes (under the
  5-minute idle window, so attempts never overlap); join 30 minutes, after
  which the join goes on to reconcile and steady ticks finish the import.
- **Enable (step 7): option "join keeps running in the daemon".** Today the
  daemon already finishes the join after the CLI's `--timeout` expires, but
  the CLI reports a timeout as a failure. Make that explicit: when the CLI's
  bound passes before the reply, enable exits 0 with a line saying the join
  continues in the daemon, `relevo db sync status` shows it, and re-running
  enable resumes it if the daemon restarted. `relevo db sync status` (text
  and `--json`) shows a join in progress with its start time, read from
  `sync.join`. This round owns that status line. Keep `cmd/relevo` tests pure
  (test the message/the decision as a function; the daemon-side behaviour in
  `internal/relevo`/`internal/sync`).
- **Steps 7-8 own their text.** No "left to the owning round": those rounds
  have landed. Disable and retry cancel an in-flight data step through the
  supervisor's Cancel (not counted as a death); pin it with a test that a
  disable during a hung pull returns within a few seconds and the death count
  is unchanged.
- **Loop predicate** is `Applied > 0 || len(Dropped) > 0` (the plan's
  conflict 3 is right). A run that only reports Held or Gaps stops the loop.
- Pin `db sync status` (text and --json) for held, dropped AND gaps: today removing `Gaps: state.Trouble.Gaps` from the doc fails no test.
- Rename `TestSyncSnapshotCarriesTheR4Fields` (slice name is history) to say what it pins.
- History wording the branch added, rewrite to say what is pinned: `cmd/relevo/db_sync_status_test.go` ("used to meet", "The test that used to live here", "status no longer needs"), `internal/relevo/syncverb_test.go` ("no longer a stub"). `git diff origin/main...HEAD | grep -E "^\+.*(used to|no longer|round [0-9])"` must be empty when done.
- Rules as the plan says; also: no history in code or tests ("used to",
  "no longer", "round N", issue numbers).

## Plan for this round


Tree: the chain base (sync chain head incl. disable/retry and status). Spec: `docs/specs/2026-10-07-sync-redesign-design.md` §3a–§3c. Context: 1.3 GB `relevo.db` (8k `round_file` rows, ~200 MB bodies, ~16k other shared rows); second machine joins same remote. Disable/retry/status have landed; touch them only where the amendments say; do not amend or rebase any remote branch — new commits only.

## Seed-vs-code conflicts (said, not guessed around)

1. "Enable at 20s (`DefaultTimeout`)" is false: `internal/sync/join.go:183-205` `Enabler.Enable()` takes no context/timeout, `internal/relevo/syncverb.go:129-160` `enable()` never calls `verbTimeout()` (only `disable()` does, `syncverb.go:204,237-242`); join's `Import`/`reconcile` are currently unbounded. Size it in this round.
2. "`head` if it reads the remote" is false: `internal/syncworker/turso.go:309-351` `Head()` is a local replica SQL read, no `driver.Pull`; it keeps the short bound. Do not promote it.
3. `ImportResult.Batches` alone does not mean "moved a mark": `internal/synclog/import.go:212-218` increments `Batches` only on applied entries; dropped batches move the mark via `past()` (`import.go:328-335`) without incrementing it. Loop predicate must be `Applied>0 || Dropped non-empty`, not `Batches>0`.
4. `disable`/`retry`/`status` overlap: `internal/sync/disable.go:32-59` `pusher` has only `Push(ctx)`, `VerbRunner.disable` passes no client and no cancel (`syncverb.go:204`), there is no retry verb (`syncverb.go:66-81`), and `db sync status` prints only `Enabled/Remote/Namespace/TokenPresent` (`cmd/relevo/db_sync.go:156-164,324-378`) never reading `KeyJoin` (`internal/sync/join.go:28`). This round adds only the cancel hook and the join-marker read helper; wording/verbs stay with the owning rounds.

## Behaviour and cases

§1 — Join (`Enabler.join`) and steady (`Runner.attempt`) drain: loop `Import()` while a run moved some mark (applied entries or dropped a batch); stop on a run that moved none (empty, held by newer schema, gapped) or at a loop deadline (deadline stop is not an error; next tick continues); each `Import` keeps its own step bound. One join and one steady attempt fully import >1 page for one origin; a held/gapped origin returns without spinning to the deadline.

§2 — Data-moving pipe calls (`hello`, `pull`, `export`/`append`) get a transfer-sized bound (named constant; comment says which measured size it comes from: first-export/ bootstrap bytes, 4 MB `PullBytesThreshold`, 256-entry appends with ~1 MB bodies); control calls (`stats`, `head`, `shutdown`) keep the short bound; steady data-step bound stays below its call bound so the deliberate cancel wins and a cancel is never a counted death; enable either fits the join or returns early with resume instructions; disable/retry cancel an in-flight data call; a guard test fails if a data kind ever gets the short bound again.

## Seams

- `internal/synclog/import.go:28-32,50-70,167-221,255-280` `Importer`, `ImportResult{Held,Dropped,Gaps}`, `Import()`, `applyBatch()`.
- `internal/synclog/transport.go:12-56` `LogTransport`; `internal/synclog/export.go:15,64-77`; `internal/synclog/reconcile.go:19`; `internal/synclog/memfake.go:56-122`.
- `internal/sync/join.go:183-205,227-240,249-298` `Enable`, `transport`, `join`, `reconcile`; `internal/sync/steady.go:33,85-102,113-154` `StepTimeout`, `attempt`, `within`, `cancelWorker`, `stepTimeout`; `internal/sync/runner.go:24-48` `DefaultTimeout`, `Runner`.
- `internal/syncpipe/client.go:49,52-78,222-266,269-363,379-393` `defaultTimeout`, `Config{Timeout,OnMiss}`, `Append/Pull/Head/Stats`, `call/readReply`, `cancel/miss`; `internal/syncpipe/supervisor.go:57-121,134-191` transport verbs, `Cancel`, `drive/settle`; `internal/syncpipe/wiring.go:25-50`.
- `internal/syncworker/protocol.go:39-88` verbs + `Request`; `internal/syncworker/turso.go:86-105,170-191,273-286,309-370` `Open/Append/Pull/Head/Stats`; `internal/syncworker/pull.go:16,23-51` `pullPageSize/readEntries`; `internal/syncworker/driver_turso.go:16-19,57-91` thresholds, `Open/Push/Pull`.
- `internal/relevo/syncverb.go:129-174,196-242`; `cmd/relevo/db_sync.go:48,156-178,324-378`; `internal/sync/breaker.go:24-26`; `internal/sync/sync.go:106-178`.
- tursogo source (module cache): `$(go env GOMODCACHE)/turso.tech/database/tursogo@v0.8.1/driver_sync.go:142-179,273-330`, `bindings_sync.go:58-84,420-435`, `driver_sync_test.go:854-` (PullBytesThreshold).

## Ordered steps (one line each: deliverable + how it passed)

1. Investigate tursogo `Open/Pull/Push` partial-progress-on-kill and progress exposure from the cached source above and write the report note; HALT the round here if bootstrap or `Pull` loses partial progress or exposes none, because resume/chunking then has no foundation.
2. Add the moved-mark predicate (`Applied>0 || len(Dropped)>0`) beside `ImportResult` in `internal/synclog/import.go` with unit coverage over the fake; passes when the focused synclog tests are green and flipping the predicate to `Batches>0` fails the dropped-batch case.
3. Loop `Enabler.join`'s import to drain on the predicate with a loop deadline (deadline stop returns success so far); passes when one join imports a >1024-entry origin fully and a held/gapped fixture returns far under the deadline (mutation: single-`Import` fails the >1-page join case).
4. Loop `Runner.attempt`'s import the same way, each `Import` under the existing per-step bound and the whole loop under the deadline; passes when one steady attempt imports a >1-page origin and held/gapped fixtures do not spin (mutation: single-`Import` fails the >1-page steady case).
5. Replace the single `defaultTimeout` in `internal/syncpipe/client.go` with a kind→bound map (data kinds get the transfer-sized constant documented from the measured sizes, control kinds keep the short bound); passes when all syncpipe tests are green and changing a data kind back to the short bound fails the guard test in step 8.
6. Set the steady data-step bound below its call bound so `within`/`Cancel` fires first and `settle` still counts it as deliberate-stop not death; passes when the existing cancel/timeout steady and supervisor tests are green and raising the step bound above the call bound fails the cancel-first case.
7. Pick the smaller enable fix (fit the join in a raised default vs return-early with daemon-side resume via `sync.join`), implement it with the chosen output/status seam left to the owning round (this round: resume path + helper reading `KeyJoin`; not status text); passes when a join of the context size completes or returns early with instructions that the resume test follows to completion.
8. Expose cancel of an in-flight data call to disable/retry (hook only; step text stays with the owning round) so a blackholed transfer ends promptly without a counted death; passes when a hung-transfer disable/retry returns promptly and the breaker count is unchanged (mutation: waiting the full bound fails the promptness case).
9. Add the kind→bound guard test asserting every data verb maps to the long bound and every control verb to the short one; passes when the suite is green and reverting one data kind to the short bound fails it.
10. Raise only the touched packages' lines in `testdata/coverage-baseline.txt` (never regenerate whole file, never lower), keep functions ≤70 lines / files ≤600 / why-comments only with no new lint/comment/filesize exclusion and pure `cmd/relevo` tests, then run `make check` plus `go build -tags modernc ./...` green; land as new commits only.

## Deleted behaviour (closed list)

1. Single-`Import`-per-run drain (one 1024-entry page per origin per join/attempt) — replaced by predicate-gated drain loops.
2. Uniform 30 s bound for all pipe calls — replaced by kind→bound (data long, control short).

## Report must include

Which enable option was picked and why it was smaller; tursogo finding (partial progress kept? progress exposed? file:line); the two bound values and the measured sizes each derives from; loop-deadline value and why a deadline stop is not an error; breaker effect (cancel not counted, deaths/latch unchanged); coverage delta per touched package; `make check` and `go build -tags modernc ./...` results; commit list (no amend/rebase).
