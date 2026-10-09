# Sync liveness plan (#1075 follow-up to #1065, base e7c53f06)

## Behaviour

Cloud sync becomes near-live without becoming load or cost:

1. **Rerun on dirty.** A trigger arriving while an attempt holds the slot sets a dirty flag under `syncMu`; when the attempt ends it runs exactly once more if dirty. A write landing mid-attempt is exported by the rerun, not left for the next window.
2. **Export on change, debounced.** On each tick, when the outbox is non-empty (one cheap `EXISTS` on the daemon's own handle) and the last attempt *started* ≥ 15 s ago, queue an attempt. Never trigger per write (ingest writes nearly every 2 s tick while a round runs). Seals keep their immediate trigger.
3. **Adaptive pull.** Idle window is 30 s while "hot" (an attempt applied > 0 rows within the last 5 min), otherwise 5 min as today. Named constants with why-comments.
4. **Pull on read.** `relevo status` (not `status --line`), `:sync` open, and the cockpit send the daemon a non-blocking fire-and-forget "freshen" hint over the owner wire; the daemon queues an attempt only when the last attempt started > 15 s ago. No daemon running = no hint, never start one. The statusline path never sends it.
5. **Keep alive.** A daemon whose sync is on never idle-exits (`daemonActivity` gains a sync-on field; `idleExit` table covers it). No exit-time flush.
6. **Honest status.** The daemon records the last attempt (start, end, exported, applied, error, attention); `db sync status` (text and `--json`) and `:sync` show it. A failing machine reads as failing.
7. **One slot + Breaker for everything.** Every new trigger goes through the same slot and the Breaker; a join holds the slot and new triggers queue behind it (dirty) or drop, never interleave.

Cases: trigger while idle / in-flight / during join; outbox empty vs non-empty; debounce boundary; hot vs cold window; freshen within vs past throttle; no daemon; statusline vs full status; sync-on idle sampler; failed attempt with error + attention; breaker latched/backing-off refusing a trigger; join in progress shown on status.

## Seams (read before writing)

- `internal/relevo/sync_tick.go:1-196` — `syncWindow` (19-24), `queueSync` (34-71), `idleSync` (79-96), `runSync` (104-130), `syncRunner` (136-141), `syncClock`/`syncNow` (143-149). Home of dirty flag, debounce, adaptive window, attempt recording.
- `internal/relevo/daemon.go:46-121,139-181,300-306,464-477` — `Daemon` sync fields `syncMu/syncInFlight/syncIdle/syncLast/syncNow/syncVerbs` (71-100), `WaitSyncSlot` (154-172), `signalIdleLocked` (177-181), tick phase `idleSync` (300-303), seal trigger `queueSync` (464-477). Allow-listed over 600 lines: do not grow it; put new logic in `sync_tick.go` or a new `internal/relevo/sync_liveness.go`.
- `internal/relevo/syncverb.go:34-138,189-231,280-327` — `VerbRunner`, `runGuarded`/`Serialize`, `preempt`, `EnsureTransport`. Join holds the slot via `Serialize = WaitSyncSlot` (`cmd/relevo/syncverb_hook.go:84-103`).
- `internal/sync/steady.go:43-91,222-280` — `SteadyResult` (Exported/Applied/Err/Attention/Trouble), `SyncOnce`, `record`. Attempt outcome shape lives here.
- `internal/sync/breaker.go:115-220` — `Begin/Due/Success/Observe`; per-call gate inside `internal/syncpipe/supervisor.go:139-148` (`drive` calls `breaker.Begin`). There is **no trigger-level Breaker check** in `internal/relevo` today (grep finds none): the plan adds one before queueing so a backed-off/latched machine does not take the slot.
- `internal/sync/sync.go:24-64,150-182` — markers `KeyEnabled/KeyBacklog/KeyLastTick/KeyAttention/KeyTimes/KeyTrouble`, `State`, `ReadState`, `StatusToken`.
- `internal/db/outbox.go:21-33`, `internal/db/sync_exchange.go:284-396` — `TruncateOutbox`, `DrainOutbox`; **no cheap `EXISTS` helper exists**: the plan adds one (e.g. `HasOutboxEntries`) on the daemon's own handle.
- Owner wire: `internal/db/wire/wire.go:115-136` (`KindSyncVerb=12`, `KindSyncResult=13`), `internal/db/wire/msg.go:143-261` (verbs `enable/disable/push/pull/retry/probe`, `SyncVerb/SyncResult`), `internal/db/wire/owner/verb.go:22-96` (verb takes the connection request slot and runs serialized). A fire-and-forget hint needs a **new non-verb kind** (not a new `SyncVerb` name), or it contends with verbs.
- Status: `cmd/relevo/db_sync.go:176-230,427-530` (`dbSyncStatusDoc`, `cmdDBSyncStatus`, `dbSyncStatusLine/Details`); `cmd/relevo/db_sync_call.go:198-244` (`openDBSyncStatus` reads the local file directly, never starts a daemon); `cmd/relevo/status.go:91-209,242-301` (`cmdStatus`, `runStatusline`); `cmd/relevo/db_sync_status_test.go` (goldens both shapes).
- Cockpit/:sync: `internal/ui/actions_sync.go:99-128,187-242` (`syncVerb`, `SyncSnapshot`), `internal/ui/view_sync.go:115-132,326-401` (`newSyncView`/`syncSnapshotCmd`, status block).
- Keep-alive: `cmd/relevo/daemon_idle.go:14-117` (`daemonActivity`, `idleExit`, `daemonActivityNow`, `watchDaemonIdle`), `cmd/relevo/daemon.go:486-494` (watcher install), `cmd/relevo/startdaemon_unix.go:58-65` (`daemonAutoExitAfter = 10m`), `cmd/relevo/daemon_idle_test.go` (table + watcher tests).
- Checks: `make check` (gofmt + vet + tidy + golangci + `scripts/check-comments.sh` + `scripts/check-filesize.sh` + coverage vs `testdata/coverage-baseline.txt` via `scripts/check-coverage.sh`); `scripts/check-filesize.allow` lists `internal/relevo/daemon.go` already — never add a new exclusion.

## Seed-vs-code contradictions (said, not guessed around)

1. Decision 6 says the daemon keeps the last attempt "in memory" and `db sync status` shows it. The code's status path reads the **local file directly** (`openDBSyncStatus`) with **no daemon round-trip**, works with no daemon, and must never start one. In-memory-only state cannot reach it and would vanish on restart. Resolution: the daemon writes each attempt's outcome to **local markers** (extend the `sync.*` marker set; memory is the write-side cache, markers are the read side); status/:sync keep reading markers. The plan does not add a status→daemon query.
2. Decision 4 says the hint goes "over the owner wire". The wire's only sync kinds are the serialized verb/result pair. A hint sent as a verb would take the request slot and serialize behind a join — not fire-and-forget. Resolution: new frame kind (or connection-less datagram on the same socket) that the owner handles without the request slot, drops when no daemon/hook exists, and never starts a daemon.
3. Decision 7 says triggers go "through the Breaker". Triggers today only take the slot; the Breaker gates **per pipe call** inside the supervisor. Resolution: add an explicit trigger-level `Due()` check before queueing (backing-off/latched refuses without taking the slot), keeping the per-call `Begin` as the enforcement.

## Ordered steps (one line each: deliverable + how to know it worked)

1. Slot + dirty + trigger-level Breaker gate in `internal/relevo` (`sync_tick.go`, `daemon.go` fields only): trigger while in-flight sets dirty under `syncMu`, attempt end runs exactly once more if dirty and re-arms via `signalIdleLocked`; join/verbs keep holding the slot through `WaitSyncSlot`; `queueSync`/freshen path checks `Breaker.Due` before taking the slot — worked when `go test ./internal/relevo/ -run 'Sync|Slot|Join' -count=1` passes with the new rerun-on-dirty test (mutation: delete the dirty set → test fails) and no function exceeds 70 lines.
2. Cheap outbox signal in `internal/db/outbox.go` (new `HasOutboxEntries`-style `EXISTS` on the caller's own handle, missing-table reads as empty): export-on-change debounce in `sync_tick.go` (named `exportDebounce = 15s` + why-comment, last-attempt-*start* via `syncNow`, seals untouched) — worked when the debounce test with injected `syncNow` passes (mutation: drop the age check → test fails) and per-write triggering appears nowhere.
3. Adaptive pull in `sync_tick.go` (named `hotWindow = 30s` + cost why-comment, `coldWindow = 5m` replacing direct `syncWindow` use, `hotSince = 5min`, daemon `lastAppliedAt` set from `SteadyResult.Applied > 0`): `idleSync` branches hot/cold — worked when the hot/cold window test passes (mutation: swap the two windows → test fails).
4. Fire-and-forget freshen kind on the owner wire (`internal/db/wire/wire.go`, `msg.go`, `owner/verb.go`, `client/`): new kind handled outside the request slot, dropped with no hook/daemon, daemon side throttles on last-attempt-start > 15 s through the same slot+Breaker — worked when `go test ./internal/db/wire/... -count=1` passes with the freshen-throttle test (mutation: remove the throttle → test fails) and a hung join never blocks a hint.
5. Pull-on-read senders as pure functions in `cmd/relevo` (helper like `shouldSendFreshen(line bool, err error)`; `cmdStatus` full path and `:sync` open/cockpit call it fire-and-forget, `runStatusline` never does; no daemon started for the hint; no network/harness in `cmd/relevo` tests) — worked when `go test ./cmd/relevo/ -run 'Status|Freshen|Statusline' -count=1` passes with the statusline-sends-no-hint pin (mutation: send from `runStatusline` → test fails).
6. Keep-alive in `cmd/relevo/daemon_idle.go` (`daemonActivity` gains sync-on field read from the local enabled marker, `idleExit`/`watchDaemonIdle` treat it as busy, no exit flush added): sampler failure reads busy, not idle — worked when the extended `idleExit` table test passes (mutation: drop the sync-on condition → test fails).
7. Honest last-attempt surfacing (`internal/sync` marker extension + `internal/relevo` attempt recording in `runSync`; `dbSyncStatusDoc` text + `--json`, `SyncSnapshot`, `:sync` body show start/end/exported/applied/error/attention with sanitize-at-render preserved): a seeded failed attempt renders failing on both status shapes — worked when `go test ./cmd/relevo/ -run 'SyncStatus|StatusLine' -count=1` and `go test ./internal/ui/ -run Sync -count=1` pass with the failing-machine golden (mutation: render from `LastTickOK` alone → test fails).
8. Full verification + coverage: run the focused suites above iteratively to green, then `make check` once; if packages moved, regenerate with `sh scripts/check-coverage.sh --write` and say so, never lower a baseline to get green — worked when `make check` passes on the CI-equivalent leg and `git diff --stat` stays inside the named seams.
9. Save this plan verbatim as `docs/plans/2026-10-09-sync-liveness.md` and commit it with the code as one new commit (no amend/rebase of any remote branch) — worked when the commit contains the plan file plus the code and `git log --oneline -1` shows it.

## What is deleted (closed list)

1. The drop-while-in-flight early return in `queueSync` (`sync_tick.go:49-53` behaviour): replaced by dirty-flag set + exactly-once rerun; no silent trigger loss remains.
2. The single fixed `syncWindow` idle gate as the only pull cadence (`sync_tick.go:19-24,87-96` behaviour): replaced by the hot/cold branch; the 5 min value survives as `coldWindow`.
3. The status "on with stale times reads healthy" behaviour (`db_sync.go` status rendering before step 7): replaced by last-attempt error/attention rendering; no other status field is removed.
4. Nothing else is deleted: no exit-time flush is added, no verb is removed, no marker key is renamed, no allow-list or baseline entry is removed.

## Report must include

- Which steps landed and the commit hash; `make check` result; coverage baseline kept vs regenerated (`--write` stated if used).
- Mutation proof per new behaviour: debounce, rerun-on-dirty with mid-attempt write, hot/cold window, freshen throttle, statusline-no-hint, keep-alive — each named with the mutation that fails it.
- Measured pull cost note (30 s hot vs 5 min cold round-trips/machine/day) and the constants chosen.
- Any halt: which step, which seam contradicted it, and what was left undone (a halt on a design error beats a green suite that bent a test).

## MasterMind review notes (apply these over the plan above)

- Contradiction 1 is resolved as the plan says, but its premise is slightly off:
  `openDBSyncStatus` (`cmd/relevo/db_sync_call.go:218`) DOES dial the owner, then
  reads the machine-local rows over the owner's local scope. The resolution stands:
  the daemon persists each attempt's outcome as local `sync.*` markers and status
  reads those rows; no status->daemon query is added.
- Contradictions 2 and 3: accepted as written (new non-slot frame kind for the
  freshen hint; trigger-level `Breaker.Due` check before taking the slot).
- Base: origin/main. One commit on top, no amend, no force-push.
- Before step 8, run `gofmt -l` over changed files yourself (a worktree's
  make check gofmt step can be vacuous).
