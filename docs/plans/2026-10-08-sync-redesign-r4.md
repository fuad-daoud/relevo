# Sync redesign R4: wiring

The round plans of this slice of the sync redesign, in the order they ran,
as the builders received them. Spec: `docs/specs/2026-10-07-sync-redesign-design.md`.

---

<!-- r4/round-0-polish.md -->

# Sync redesign polish round (before R4 wiring)

Branch `relevo/sync-log` with R0-R3 merged: `internal/synclog` (exchange),
`internal/syncworker` (worker + Turso backend), `internal/syncpipe` (daemon
pipe client + supervisor), breaker in `internal/sync`. Spec:
`docs/specs/2026-10-07-sync-redesign-design.md`. Every change keeps
`make check` and `go build -tags modernc ./...` green; new commits only.

Rules: CLAUDE.md style (no history in code or tests: no "used to", "no
longer", "old ...", "round N"; functions <= 70 lines; files <= 600 lines;
comments say why); no new lint/comment/filesize exclusion; do not regenerate
the whole coverage baseline -- add or raise only the lines of packages this
round changes, and never lower one. Name the test that fails for each
condition below and report the mutation you ran against it.

## 1. Reconcile pages by rowid

`ownedRowPageQuery` (`internal/db/sync_reconcile.go`) orders and compares on
`json_array(<pk>)`, which no index serves: every page scans and sorts the whole
table, BLOB bodies included (`round_file` holds ~7.7k compressed files). Page
by the table's rowid instead: `... AND rowid > ? ORDER BY rowid LIMIT ?`, with
the reconcile cursor carrying the last rowid for the upsert phase (the rowid is
local to this file, which is all a cursor needs). Keep the emitted entries'
`pk` text and the output order rules unchanged. Test: a page query plan or a
counting seam shows a page reads only its own rows; the existing reconcile and
converge tests stay green.

## 2. The worker's pull reads only what is past the marks

`TursoBackend.Pull` (`internal/syncworker/turso.go`) selects every log row of
every other origin and filters marks in Go, so each pull reads the whole log,
bodies included. Filter in SQL per origin (`origin = ? AND seq > ?` for each
origin with a mark, plus origins without one from 1), keep whole batches, and
bound one pull (a page size; the importer pulls again for the rest). Test:
`TestPullReadsOnlyWhatIsPastTheMarks` with a counting seam or by asserting the
statement; mutation: dropping the SQL filter fails it.

## 3. History wording

Rewrite to state current behaviour, no history:
- `internal/relevo/sync_tick.go` (queueSync/idleSync docs) "used to";
- `internal/relevo/syncverb.go` "old direct-open path";
- `internal/sync/sync.go` (`ErrSyncUnavailable` doc) "no longer" (if the
  identifier still exists);
- `cmd/relevo/db_sync.go` "enable no longer needs the daemon stopped";
- `internal/relevo/syncverb_test.go` "used to" and "this round's own pin";
- `internal/db/engine_turso.go` "round 2's one-time";
- `cmd/relevo/registry_test.go` "no longer carries a map";
- `cmd/relevo/main.go` "(round 3, step 3.1)".
Then grep every `.go` file changed since `7320331d` for
`used to|no longer|round [0-9]|old (engine|path|driver|design)|stopped using`
and fix what describes history.

## 4. Spec formatting

`docs/specs/2026-10-07-sync-redesign-design.md` §3a item 2: re-wrap the long
line that starts "append that wrote it" to the file's ~78-column width.

---

<!-- r4/round-1.md -->

# R3/R4 round 5 of 8: R4a: enable/join (bootstrap, import, chunked resumable reconcile-export)

Slices R3 (worker) and R4 (wiring) of the sync redesign, branch
`relevo/sync-log`. Read the spec first, especially §3a-§3c:
`docs/specs/2026-10-07-sync-redesign-design.md`. R2 (`internal/synclog`) and R3 are on this branch; read them before anything
else and use their names. Every
round ends with `make check` and `go build -tags modernc ./...` green; new
commits only.

Rules: CLAUDE.md style (no history in code or tests: no "used to", "no
longer", "old ...", "round N"; functions <= 70 lines; files <= 600 lines;
comments say why); no new lint/comment/filesize exclusion; no lowered
baseline; `cmd/relevo` tests are pure functions (no harness, network, worker).

## MasterMind amendments for the whole slice (these override the plan below)

- **Testable backend.** CI has no network and no Turso. Split the Turso
  backend into (a) the replica SQL (create log/head/meta, seq assignment,
  append, head update, pull reads, head paging) over a `*sql.DB`/connection it
  is handed, and (b) the driver calls (bootstrap, `Push`, `Pull`, stats)
  behind a small interface. CI tests run (a) against a plain local file with a
  fake (b) that can fail or panic-exit; only the env-gated test uses the real
  driver. In production every statement still goes through
  `TursoSyncDb.Connect()`.
- **Batches.** The worker writes each `export` request as ONE replica
  transaction and sets `Entry.batch` to the first seq it assigned in that
  request (spec §3a.2). It refuses any entry whose origin is not the origin
  given in `hello`.
- **Never write into a foreign database.** On first use: a remote with no
  tables gets `log`/`head`/`meta` (meta records the log format version); a
  remote whose `meta` matches is used; ANY other remote (other tables, no
  meta, a different format) is refused with a fixed message naming the URL
  and saying it is not a relevo log. Pin with
  `TestWorkerRefusesARemoteThatIsNotALog`.
- **Lifecycle.** The worker exits when its stdin closes. The daemon kills its
  worker on shutdown and on re-exec (upgrade), and a fresh daemon starts a
  fresh worker; no orphaned worker may keep `relevo-sync.db` open. Pin with
  `TestWorkerExitsWhenTheDaemonGoesAway`.
- **Latch gates the tick.** While the breaker is latched, the seal and idle
  triggers do nothing until `relevo db sync retry`.
- Round headers below say "R3a".."R4d"; they are rounds 1-8 of THIS chain.

### Round 5 amendment
Enable against a remote the worker refuses (not a relevo log) fails before any mark is written and says why.

## MasterMind notes for R4 (these override anything below)

- R3 is on this branch: `internal/syncworker` (worker, Turso backend, pipe
  protocol with refusal codes), `internal/syncpipe` (client, supervisor) and
  the breaker in `internal/sync`. The daemon builds its one pipe client in
  `syncpipe.NewSyncRunner`, called from `cmd/relevo/syncverb_hook.go`; R4
  fills its `Config` (worker executable, replica path beside `relevo.db`,
  remote URL from the `sync` section, token from the local secret) when an
  enable decides the remote. Read these before anything else.
- The importer reports three kinds of trouble besides errors: `Held` (newer
  schema), `Drops` (refused batches) and `Gaps` (a hole in an origin's
  sequence). Status, the statusline token and `:sync` must surface all three,
  not only held origins.
- Coverage baseline: add or raise only the lines of packages this round
  changes; never regenerate the whole file and never lower a line.

## Plan for this round

Goal: enable as join per spec §3c.1/§4: preflight (origin gate, token, remote URL from `sync` section or `--url`, `--url`-vs-section conflict refuses), then start worker, bootstrap replica, import every other origin, reconcile-export this origin's history in chunks resumable from `head`, progress under a `sync.join` marker in `relevo-local.db` (interrupted enable resumes), mark-on only when the join finished.

Seams: `internal/sync/enable.go:22-46` (existing refusals `ErrAlreadyEnabled/ErrNoToken/ErrNoRemote/ErrRemoteConflict` — kept, wired to real path); `internal/sync/settings.go:16-30` (`Settings` — remote URL source); `internal/sync/token.go` (`SetToken`/`ReadToken`); `internal/db/preflight.go` (origin gate); `internal/relevo/syncverb.go:60-73` (enable verb stops refusing); `cmd/relevo/db_sync.go:193-234` (enable CLI — thin, pure-tested); `internal/synclog` exporter/importer/reconcile + `sync_import_mark` table (read first, call as found).

Steps:

1. Deliverable: enable preflight order (already-on → token → remote → conflict → origin gate) — verified by `TestEnablePreflightOrder` failing if any check is skipped or reordered.
2. Deliverable: join pipeline (start worker → bootstrap → import others → chunked reconcile-export → mark on) with `sync.join` progress — verified by `TestJoinResumesFromTheJoinMarker` (kill mid-export, re-enable resumes without re-sending converged chunks) failing if export restarts from zero or the mark goes on early.
3. Deliverable: chunked resumable export bounded per chunk (bulk bodies travel like any row, first export resumable from `head`) — verified by `TestJoinExportsInBoundedResumableChunks` failing if one chunk is unbounded or a re-run re-emits converged rows.
4. Deliverable: `TestJoinWithHistoryOnBothSides` (two origins, both with history, converge) using R2's in-memory transport or fake worker — verified by divergence on either side failing it.
5. Deliverable: enable CLI stays thin (flag parsing + verb send only; pure tests in `cmd/relevo`) — verified by existing `db_sync_test.go` pattern, no harness/network/worker in `cmd/relevo` tests.
6. Deliverable: `make check` + modernc build green; baseline `--write` if new files added.

Report must include: preflight order; chunk bound chosen and why; join-marker key; resume demonstration; new commits; both green builds.

## Context: the slice preamble

### Conflicts between the seed and the code (flagged, not re-opened; §3b/§3c stay settled)

- C1. The seed says R2's `internal/synclog` "will be on the branch before R3 starts". It is not: `ls internal/synclog` fails on this tree, and nothing imports it. Every R3 round therefore plans against the spec's §3a contract, and every round's first step orders the builder to read `internal/synclog` as it exists on the branch and adapt names to it.
- C2. The seed names `NewTursoSyncDb`/`Connect()`, `BootstrapIfEmpty`, `PushOperationsThreshold`, `PullBytesThreshold`. None of these strings occurs anywhere in the tree (`grep` over `*.go` is empty). `go.mod:16` carries `tursogo v0.8.1`; `internal/db/engine_turso.go:1-363` shows only local-open patterns (`openPool`, `newTursoConnector`). Round 2's first step orders the builder to read the tursogo module source for the real sync API and to halt-and-report if the API cannot provide bootstrap-via-`Connect()` plus push/pull thresholds as specced, rather than inventing a wrapper.
- C3. The current `internal/sync` seam is the old file-replication engine shape, not the log shape: `SyncClient` (`client.go:20-29`: `Push/Pull/Stats/Checkpoint`), `Fake` (`fake.go`), `OpenConfig/Opener` (`open.go:17-36`), `Runner{Client, Local}` (`runner.go:36-43`). R3 replaces this seam with a pipe client implementing `synclog.LogTransport`; the old types are deleted in round 4 (see deletion list). R2's in-memory fake is the surviving fake; any name collision with `sync.Fake` is resolved by deleting `sync.Fake`, never by renaming R2's.
- C4. Seal/idle triggers are currently pinned no-ops: `queueSync`/`idleSync` in `internal/relevo/sync_tick.go:21-27` do nothing, and `Daemon.Tick` (`internal/relevo/daemon.go:219-306`) calls `queueSync` after `sealed > 0` (`daemon.go:470-472`) plus `truncateOutbox` while off (`daemon.go:257`, `sync_tick.go:42-74`). R4 rounds 5–6 replace the no-ops with the drain/export/pull/import pipeline; the no-op-pinning tests (`sync_tick_test.go`, e.g. `TestSyncSealOpensNoHandle`, `TestSyncIdleTickIsAReachableNoOp`) are deleted/rewritten there, not earlier.
- C5. `Disabler.Disable` (`internal/sync/disable.go:79-102`) runs four steps with no replica-file deletion; R4 round 7 extends the contract (stop worker, delete token, delete `relevo-sync.db` + driver files, keep `relevo.db` rows).
- C6. `ErrSyncUnavailable` (`internal/sync/sync.go:22`) is still raised at `internal/relevo/syncverb.go:108` and `internal/ui/actions_sync.go:182`, with tests pinning it (`syncverb_test.go:104`, `actions_sync_test.go:238`). It is removed only in round 7, when the verbs work.

### Open points (builder decides, reports, does not re-open the spec)

- O1. New-package name for the worker (suggested `internal/syncworker`, one package per concept); daemon-side pipe client lives beside it, breaker beside the client.
- O2. `sync.join` progress marker and the breaker in-call marker both live in `relevo-local.db` per spec; suggested as `sync.join` / `sync.incall` keys in the existing `db.KV` marker store reached via `LocalHandle` (`internal/sync/sync.go:43-48`).
- O3. `push`/`pull` one-shot verbs keep their names and become drain/export/pull/import one-shots; `retry` and `reconcile --dry-run` surface in rounds 5/7 as specced.
- O4. Exact JSON-lines field names are the builder's, but must match between worker and client and be pinned by a golden round-trip test.

### Round one-liners

- Round 1 (R3a): pipe protocol + worker skeleton + daemon pipe client + `go list -deps` isolation test.
- Round 2 (R3b): Turso backend — replica ownership, remote log/head/meta creation, export/pull/head/stats.
- Round 3 (R3c): breaker — in-call marker, backoff, latch-after-three, restart-after-cancel.
- Round 4 (R3d): daemon cutover to the pipe transport; old engine seam deleted; baselines regenerated.
- Round 5 (R4a): enable/join — bootstrap, import, chunked resumable reconcile-export under `sync.join`.
- Round 6 (R4b): steady state — seal + idle triggers drive the pipeline; real-`Tick` reachability test.
- Round 7 (R4c): disable/leave + `retry` + `ErrSyncUnavailable` removal.
- Round 8 (R4d): status/statusline/`:sync` surface + final baselines + chain-green proof.

Global rules for every round: builder first reads `internal/synclog` as it exists and adapts names to it; `cmd/relevo` tests stay pure functions (no harness, network, or worker — test the rule in `internal/relevo`/`internal/sync*` instead); CLAUDE.md style (no history in code or tests, functions ≤ 70 lines, files ≤ 600 lines, comments say why); no new lint/comment/filesize exclusion (`.golangci.yml`, `scripts/check-comments.sh`, `scripts/check-filesize.sh` stay clean); new packages get baseline lines via `sh scripts/check-coverage.sh --write`, and no round lowers a baseline; each round ends with `make check` and `go build -tags modernc ./...` green.

## Deletions (closed list — nothing else is deleted)

1. Round 4: `internal/sync/client.go` — `SyncClient` interface (`Push/Pull/Stats/Checkpoint`), replaced by the pipe client as `synclog.LogTransport`.
2. Round 4: `internal/sync/fake.go` — old-shape `Fake`; R2's `synclog` in-memory fake is the surviving fake.
3. Round 4: `internal/sync/open.go` — `OpenConfig`/`Opener`; the worker owns the only remote open.
4. Round 7: `ErrSyncUnavailable` (`internal/sync/sync.go:22`) and its comment block.
5. Round 7: `VerbRunner.unavailable()` (`internal/relevo/syncverb.go:99-109`) and the enable/push/pull refusal branch (`syncverb.go:62-63`), replaced by real verb paths.
6. Round 7: `SyncTest`'s refusal in `internal/ui/actions_sync.go:178-183`, rewired per §3c.4; refusal-pinning assertions in `internal/ui/actions_sync_test.go:238` and `internal/relevo/syncverb_test.go:104` rewritten.
7. Round 6: no-op-pinning tests in `internal/relevo/sync_tick_test.go` (`TestSyncSealOpensNoHandle`, `TestSyncIdleTickIsAReachableNoOp`, `TestSyncSealIsQuietWhenItSealedNothing`, `TestSyncTickWithVerbsOpensNoHandle`, and the no-call assertions in `TestSyncNeverBlocksSeal`), replaced by pipeline-driving tests.
8. Round 6: `queueSync`/`idleSync` no-op bodies (`internal/relevo/sync_tick.go:21-27`), replaced by the pipeline.

## Report requirements

Each round's report states: new commit hashes (no amend/rebase); `internal/synclog` as found that round and names adapted; the named failing test(s) with the mutation performed (what was broken, what failed); `make check` output and `go build -tags modernc ./...` output; coverage-baseline action (`--write` or untouched, never lowered); conflicts with the spec or code found (halt rather than improvise — a step that is impossible as written halts the round and is reported); and what was deliberately left for a later round.

---

<!-- r4/round-2.md -->

# R3/R4 round 6 of 8: R4b: steady state (seal + idle triggers drive the pipeline)

Slices R3 (worker) and R4 (wiring) of the sync redesign, branch
`relevo/sync-log`. Read the spec first, especially §3a-§3c:
`docs/specs/2026-10-07-sync-redesign-design.md`. R2 (`internal/synclog`) and R3 are on this branch; read them before anything
else and use their names. Every
round ends with `make check` and `go build -tags modernc ./...` green; new
commits only.

Rules: CLAUDE.md style (no history in code or tests: no "used to", "no
longer", "old ...", "round N"; functions <= 70 lines; files <= 600 lines;
comments say why); no new lint/comment/filesize exclusion; no lowered
baseline; `cmd/relevo` tests are pure functions (no harness, network, worker).

## MasterMind amendments for the whole slice (these override the plan below)

- **Testable backend.** CI has no network and no Turso. Split the Turso
  backend into (a) the replica SQL (create log/head/meta, seq assignment,
  append, head update, pull reads, head paging) over a `*sql.DB`/connection it
  is handed, and (b) the driver calls (bootstrap, `Push`, `Pull`, stats)
  behind a small interface. CI tests run (a) against a plain local file with a
  fake (b) that can fail or panic-exit; only the env-gated test uses the real
  driver. In production every statement still goes through
  `TursoSyncDb.Connect()`.
- **Batches.** The worker writes each `export` request as ONE replica
  transaction and sets `Entry.batch` to the first seq it assigned in that
  request (spec §3a.2). It refuses any entry whose origin is not the origin
  given in `hello`.
- **Never write into a foreign database.** On first use: a remote with no
  tables gets `log`/`head`/`meta` (meta records the log format version); a
  remote whose `meta` matches is used; ANY other remote (other tables, no
  meta, a different format) is refused with a fixed message naming the URL
  and saying it is not a relevo log. Pin with
  `TestWorkerRefusesARemoteThatIsNotALog`.
- **Lifecycle.** The worker exits when its stdin closes. The daemon kills its
  worker on shutdown and on re-exec (upgrade), and a fresh daemon starts a
  fresh worker; no orphaned worker may keep `relevo-sync.db` open. Pin with
  `TestWorkerExitsWhenTheDaemonGoesAway`.
- **Latch gates the tick.** While the breaker is latched, the seal and idle
  triggers do nothing until `relevo db sync retry`.
- Round headers below say "R3a".."R4d"; they are rounds 1-8 of THIS chain.

## MasterMind notes for R4 (these override anything below)

- R3 is on this branch: `internal/syncworker` (worker, Turso backend, pipe
  protocol with refusal codes), `internal/syncpipe` (client, supervisor) and
  the breaker in `internal/sync`. The daemon builds its one pipe client in
  `syncpipe.NewSyncRunner`, called from `cmd/relevo/syncverb_hook.go`; R4
  fills its `Config` (worker executable, replica path beside `relevo.db`,
  remote URL from the `sync` section, token from the local secret) when an
  enable decides the remote. Read these before anything else.
- The importer reports three kinds of trouble besides errors: `Held` (newer
  schema), `Drops` (refused batches) and `Gaps` (a hole in an origin's
  sequence). Status, the statusline token and `:sync` must surface all three,
  not only held origins.
- Coverage baseline: add or raise only the lines of packages this round
  changes; never regenerate the whole file and never lower a line.

## Plan for this round

Goal: while on — after a round seals and on the 5-minute idle window — drain outbox, export, pull, import; runtime wires the runner so the idle path runs in production; outbox truncation stays off while on; a test drives the real `Tick` to prove the idle path is reachable.

Seams: `internal/relevo/sync_tick.go:21-27` (no-ops replaced); `internal/relevo/daemon.go:219-306` (`Tick`), `:470-472` (seal trigger), `:303` (`idleSync` phase), `:71-100` (slot guard, verbs share it via `WaitSyncSlot`); `internal/relevo/sync_tick_test.go` (no-op-pinning tests deleted/rewritten — deletion §7); `internal/sync/sync.go:33-38` (`KeyLastTick`/`KeyBacklog` markers the tick writes).

Steps:

1. Deliverable: seal trigger runs drain→export→pull→import without blocking the seal (bounded per-step deadlines, off the seal lock) — verified by `TestSealTriggersASyncWithoutBlockingTheSeal` (blackholed transport, seal commits within the bound) failing if the tick waits on the network; this replaces `TestSyncNeverBlocksSeal`'s no-op form.
2. Deliverable: idle-window trigger runs the same pipeline on its interval in production wiring — verified by `TestIdleWindowReachesThePipelineThroughTheRealTick` driving the real `Tick` with a fake transport and asserting export+import happened; failing if the idle path is unwired.
3. Deliverable: outbox truncation skipped while on, running while off (existing `truncateOutbox` gate kept) — verified by `TestOutboxTruncationStaysOffWhileOn` failing if an enabled tick truncates.
4. Deliverable: cancelled worker restarted, never reused, on this path too — verified by `TestTickRestartsACancelledWorker` failing on reuse.
5. Deliverable: no-op-pinning tests deleted/rewritten (deletion §7) — verified by `grep` showing no test asserting "drives nothing".
6. Deliverable: `make check` + modernc build green.

Report must include: per-step deadlines chosen and why; where the idle interval is configured; Tick-phase ordering; deletion checklist; new commits; both green builds.

## Context: the slice preamble

### Conflicts between the seed and the code (flagged, not re-opened; §3b/§3c stay settled)

- C1. The seed says R2's `internal/synclog` "will be on the branch before R3 starts". It is not: `ls internal/synclog` fails on this tree, and nothing imports it. Every R3 round therefore plans against the spec's §3a contract, and every round's first step orders the builder to read `internal/synclog` as it exists on the branch and adapt names to it.
- C2. The seed names `NewTursoSyncDb`/`Connect()`, `BootstrapIfEmpty`, `PushOperationsThreshold`, `PullBytesThreshold`. None of these strings occurs anywhere in the tree (`grep` over `*.go` is empty). `go.mod:16` carries `tursogo v0.8.1`; `internal/db/engine_turso.go:1-363` shows only local-open patterns (`openPool`, `newTursoConnector`). Round 2's first step orders the builder to read the tursogo module source for the real sync API and to halt-and-report if the API cannot provide bootstrap-via-`Connect()` plus push/pull thresholds as specced, rather than inventing a wrapper.
- C3. The current `internal/sync` seam is the old file-replication engine shape, not the log shape: `SyncClient` (`client.go:20-29`: `Push/Pull/Stats/Checkpoint`), `Fake` (`fake.go`), `OpenConfig/Opener` (`open.go:17-36`), `Runner{Client, Local}` (`runner.go:36-43`). R3 replaces this seam with a pipe client implementing `synclog.LogTransport`; the old types are deleted in round 4 (see deletion list). R2's in-memory fake is the surviving fake; any name collision with `sync.Fake` is resolved by deleting `sync.Fake`, never by renaming R2's.
- C4. Seal/idle triggers are currently pinned no-ops: `queueSync`/`idleSync` in `internal/relevo/sync_tick.go:21-27` do nothing, and `Daemon.Tick` (`internal/relevo/daemon.go:219-306`) calls `queueSync` after `sealed > 0` (`daemon.go:470-472`) plus `truncateOutbox` while off (`daemon.go:257`, `sync_tick.go:42-74`). R4 rounds 5–6 replace the no-ops with the drain/export/pull/import pipeline; the no-op-pinning tests (`sync_tick_test.go`, e.g. `TestSyncSealOpensNoHandle`, `TestSyncIdleTickIsAReachableNoOp`) are deleted/rewritten there, not earlier.
- C5. `Disabler.Disable` (`internal/sync/disable.go:79-102`) runs four steps with no replica-file deletion; R4 round 7 extends the contract (stop worker, delete token, delete `relevo-sync.db` + driver files, keep `relevo.db` rows).
- C6. `ErrSyncUnavailable` (`internal/sync/sync.go:22`) is still raised at `internal/relevo/syncverb.go:108` and `internal/ui/actions_sync.go:182`, with tests pinning it (`syncverb_test.go:104`, `actions_sync_test.go:238`). It is removed only in round 7, when the verbs work.

### Open points (builder decides, reports, does not re-open the spec)

- O1. New-package name for the worker (suggested `internal/syncworker`, one package per concept); daemon-side pipe client lives beside it, breaker beside the client.
- O2. `sync.join` progress marker and the breaker in-call marker both live in `relevo-local.db` per spec; suggested as `sync.join` / `sync.incall` keys in the existing `db.KV` marker store reached via `LocalHandle` (`internal/sync/sync.go:43-48`).
- O3. `push`/`pull` one-shot verbs keep their names and become drain/export/pull/import one-shots; `retry` and `reconcile --dry-run` surface in rounds 5/7 as specced.
- O4. Exact JSON-lines field names are the builder's, but must match between worker and client and be pinned by a golden round-trip test.

### Round one-liners

- Round 1 (R3a): pipe protocol + worker skeleton + daemon pipe client + `go list -deps` isolation test.
- Round 2 (R3b): Turso backend — replica ownership, remote log/head/meta creation, export/pull/head/stats.
- Round 3 (R3c): breaker — in-call marker, backoff, latch-after-three, restart-after-cancel.
- Round 4 (R3d): daemon cutover to the pipe transport; old engine seam deleted; baselines regenerated.
- Round 5 (R4a): enable/join — bootstrap, import, chunked resumable reconcile-export under `sync.join`.
- Round 6 (R4b): steady state — seal + idle triggers drive the pipeline; real-`Tick` reachability test.
- Round 7 (R4c): disable/leave + `retry` + `ErrSyncUnavailable` removal.
- Round 8 (R4d): status/statusline/`:sync` surface + final baselines + chain-green proof.

Global rules for every round: builder first reads `internal/synclog` as it exists and adapts names to it; `cmd/relevo` tests stay pure functions (no harness, network, or worker — test the rule in `internal/relevo`/`internal/sync*` instead); CLAUDE.md style (no history in code or tests, functions ≤ 70 lines, files ≤ 600 lines, comments say why); no new lint/comment/filesize exclusion (`.golangci.yml`, `scripts/check-comments.sh`, `scripts/check-filesize.sh` stay clean); new packages get baseline lines via `sh scripts/check-coverage.sh --write`, and no round lowers a baseline; each round ends with `make check` and `go build -tags modernc ./...` green.

## Deletions (closed list — nothing else is deleted)

1. Round 4: `internal/sync/client.go` — `SyncClient` interface (`Push/Pull/Stats/Checkpoint`), replaced by the pipe client as `synclog.LogTransport`.
2. Round 4: `internal/sync/fake.go` — old-shape `Fake`; R2's `synclog` in-memory fake is the surviving fake.
3. Round 4: `internal/sync/open.go` — `OpenConfig`/`Opener`; the worker owns the only remote open.
4. Round 7: `ErrSyncUnavailable` (`internal/sync/sync.go:22`) and its comment block.
5. Round 7: `VerbRunner.unavailable()` (`internal/relevo/syncverb.go:99-109`) and the enable/push/pull refusal branch (`syncverb.go:62-63`), replaced by real verb paths.
6. Round 7: `SyncTest`'s refusal in `internal/ui/actions_sync.go:178-183`, rewired per §3c.4; refusal-pinning assertions in `internal/ui/actions_sync_test.go:238` and `internal/relevo/syncverb_test.go:104` rewritten.
7. Round 6: no-op-pinning tests in `internal/relevo/sync_tick_test.go` (`TestSyncSealOpensNoHandle`, `TestSyncIdleTickIsAReachableNoOp`, `TestSyncSealIsQuietWhenItSealedNothing`, `TestSyncTickWithVerbsOpensNoHandle`, and the no-call assertions in `TestSyncNeverBlocksSeal`), replaced by pipeline-driving tests.
8. Round 6: `queueSync`/`idleSync` no-op bodies (`internal/relevo/sync_tick.go:21-27`), replaced by the pipeline.

## Report requirements

Each round's report states: new commit hashes (no amend/rebase); `internal/synclog` as found that round and names adapted; the named failing test(s) with the mutation performed (what was broken, what failed); `make check` output and `go build -tags modernc ./...` output; coverage-baseline action (`--write` or untouched, never lowered); conflicts with the spec or code found (halt rather than improvise — a step that is impossible as written halts the round and is reported); and what was deliberately left for a later round.

---

<!-- r4/round-3.md -->

# R3/R4 round 7 of 8: R4c: disable/leave + retry + `ErrSyncUnavailable` removal

Slices R3 (worker) and R4 (wiring) of the sync redesign, branch
`relevo/sync-log`. Read the spec first, especially §3a-§3c:
`docs/specs/2026-10-07-sync-redesign-design.md`. R2 (`internal/synclog`) and R3 are on this branch; read them before anything
else and use their names. Every
round ends with `make check` and `go build -tags modernc ./...` green; new
commits only.

Rules: CLAUDE.md style (no history in code or tests: no "used to", "no
longer", "old ...", "round N"; functions <= 70 lines; files <= 600 lines;
comments say why); no new lint/comment/filesize exclusion; no lowered
baseline; `cmd/relevo` tests are pure functions (no harness, network, worker).

## MasterMind amendments for the whole slice (these override the plan below)

- **Testable backend.** CI has no network and no Turso. Split the Turso
  backend into (a) the replica SQL (create log/head/meta, seq assignment,
  append, head update, pull reads, head paging) over a `*sql.DB`/connection it
  is handed, and (b) the driver calls (bootstrap, `Push`, `Pull`, stats)
  behind a small interface. CI tests run (a) against a plain local file with a
  fake (b) that can fail or panic-exit; only the env-gated test uses the real
  driver. In production every statement still goes through
  `TursoSyncDb.Connect()`.
- **Batches.** The worker writes each `export` request as ONE replica
  transaction and sets `Entry.batch` to the first seq it assigned in that
  request (spec §3a.2). It refuses any entry whose origin is not the origin
  given in `hello`.
- **Never write into a foreign database.** On first use: a remote with no
  tables gets `log`/`head`/`meta` (meta records the log format version); a
  remote whose `meta` matches is used; ANY other remote (other tables, no
  meta, a different format) is refused with a fixed message naming the URL
  and saying it is not a relevo log. Pin with
  `TestWorkerRefusesARemoteThatIsNotALog`.
- **Lifecycle.** The worker exits when its stdin closes. The daemon kills its
  worker on shutdown and on re-exec (upgrade), and a fresh daemon starts a
  fresh worker; no orphaned worker may keep `relevo-sync.db` open. Pin with
  `TestWorkerExitsWhenTheDaemonGoesAway`.
- **Latch gates the tick.** While the breaker is latched, the seal and idle
  triggers do nothing until `relevo db sync retry`.
- Round headers below say "R3a".."R4d"; they are rounds 1-8 of THIS chain.

## MasterMind notes for R4 (these override anything below)

- R3 is on this branch: `internal/syncworker` (worker, Turso backend, pipe
  protocol with refusal codes), `internal/syncpipe` (client, supervisor) and
  the breaker in `internal/sync`. The daemon builds its one pipe client in
  `syncpipe.NewSyncRunner`, called from `cmd/relevo/syncverb_hook.go`; R4
  fills its `Config` (worker executable, replica path beside `relevo.db`,
  remote URL from the `sync` section, token from the local secret) when an
  enable decides the remote. Read these before anything else.
- The importer reports three kinds of trouble besides errors: `Held` (newer
  schema), `Drops` (refused batches) and `Gaps` (a hole in an origin's
  sequence). Status, the statusline token and `:sync` must surface all three,
  not only held origins.
- Coverage baseline: add or raise only the lines of packages this round
  changes; never regenerate the whole file and never lower a line.

## Plan for this round

Goal: disable stops the worker, marks off, deletes the token, deletes `relevo-sync.db` and the driver's files beside it, keeps every `relevo.db` row (imported rows stay as read-only history); `relevo db sync retry` clears a latch; `ErrSyncUnavailable` is deleted with all its raise sites and mappings.

Seams: `internal/sync/disable.go:23-102` (`Disabler` — gains worker-stop + file-deletion steps in the fixed order); `internal/sync/token.go:46-53` (`DeleteToken`); `internal/sync/sync.go:16-22` (`ErrSyncUnavailable` — deleted); `internal/relevo/syncverb.go:99-109` (`unavailable()` — deleted); `internal/ui/actions_sync.go:169-183` (`SyncTest` refusal — rewired to a real probe or removed per §3c.4, builder follows spec); `cmd/relevo/db_sync.go:266-311` (disable CLI) + new `retry` verb + wire codes (`internal/db/wire/msg.go:150-196` — add code only if the closed set needs one, else reuse); `internal/relevo/daemon.go:143-172` (slot guard — disable/retry serialize through it).

Steps:

1. Deliverable: disable order (final export attempt best-effort → mark off → delete token → stop worker → delete replica + driver files → drop client) — verified by `TestDisableDeletesReplicaAndTokenButKeepsImportedRows` failing if a file survives or a row is lost.
2. Deliverable: `retry` clears the breaker latch and restarts the worker — verified by `TestRetryClearsALatchedBreaker` (latch, retry, next tick syncs) failing if the latch survives or the old worker is reused.
3. Deliverable: `ErrSyncUnavailable` gone from non-test code and its tests rewritten to the new behaviour — verified by `grep -rn ErrSyncUnavailable --include="*.go" internal cmd` returning only nothing, plus `make check` green (deletion §4–6).
4. Deliverable: disable/retry CLI thin with pure `cmd/relevo` tests — verified by existing `db_sync_test.go`/`db_sync_verb_test.go` patterns extended without harness/network/worker.
5. Deliverable: `make check` + modernc build green.

Report must include: disable step order; exact files deleted on disable; retry semantics; `ErrSyncUnavailable` grep proof; new commits; both green builds.

## Context: the slice preamble

### Conflicts between the seed and the code (flagged, not re-opened; §3b/§3c stay settled)

- C1. The seed says R2's `internal/synclog` "will be on the branch before R3 starts". It is not: `ls internal/synclog` fails on this tree, and nothing imports it. Every R3 round therefore plans against the spec's §3a contract, and every round's first step orders the builder to read `internal/synclog` as it exists on the branch and adapt names to it.
- C2. The seed names `NewTursoSyncDb`/`Connect()`, `BootstrapIfEmpty`, `PushOperationsThreshold`, `PullBytesThreshold`. None of these strings occurs anywhere in the tree (`grep` over `*.go` is empty). `go.mod:16` carries `tursogo v0.8.1`; `internal/db/engine_turso.go:1-363` shows only local-open patterns (`openPool`, `newTursoConnector`). Round 2's first step orders the builder to read the tursogo module source for the real sync API and to halt-and-report if the API cannot provide bootstrap-via-`Connect()` plus push/pull thresholds as specced, rather than inventing a wrapper.
- C3. The current `internal/sync` seam is the old file-replication engine shape, not the log shape: `SyncClient` (`client.go:20-29`: `Push/Pull/Stats/Checkpoint`), `Fake` (`fake.go`), `OpenConfig/Opener` (`open.go:17-36`), `Runner{Client, Local}` (`runner.go:36-43`). R3 replaces this seam with a pipe client implementing `synclog.LogTransport`; the old types are deleted in round 4 (see deletion list). R2's in-memory fake is the surviving fake; any name collision with `sync.Fake` is resolved by deleting `sync.Fake`, never by renaming R2's.
- C4. Seal/idle triggers are currently pinned no-ops: `queueSync`/`idleSync` in `internal/relevo/sync_tick.go:21-27` do nothing, and `Daemon.Tick` (`internal/relevo/daemon.go:219-306`) calls `queueSync` after `sealed > 0` (`daemon.go:470-472`) plus `truncateOutbox` while off (`daemon.go:257`, `sync_tick.go:42-74`). R4 rounds 5–6 replace the no-ops with the drain/export/pull/import pipeline; the no-op-pinning tests (`sync_tick_test.go`, e.g. `TestSyncSealOpensNoHandle`, `TestSyncIdleTickIsAReachableNoOp`) are deleted/rewritten there, not earlier.
- C5. `Disabler.Disable` (`internal/sync/disable.go:79-102`) runs four steps with no replica-file deletion; R4 round 7 extends the contract (stop worker, delete token, delete `relevo-sync.db` + driver files, keep `relevo.db` rows).
- C6. `ErrSyncUnavailable` (`internal/sync/sync.go:22`) is still raised at `internal/relevo/syncverb.go:108` and `internal/ui/actions_sync.go:182`, with tests pinning it (`syncverb_test.go:104`, `actions_sync_test.go:238`). It is removed only in round 7, when the verbs work.

### Open points (builder decides, reports, does not re-open the spec)

- O1. New-package name for the worker (suggested `internal/syncworker`, one package per concept); daemon-side pipe client lives beside it, breaker beside the client.
- O2. `sync.join` progress marker and the breaker in-call marker both live in `relevo-local.db` per spec; suggested as `sync.join` / `sync.incall` keys in the existing `db.KV` marker store reached via `LocalHandle` (`internal/sync/sync.go:43-48`).
- O3. `push`/`pull` one-shot verbs keep their names and become drain/export/pull/import one-shots; `retry` and `reconcile --dry-run` surface in rounds 5/7 as specced.
- O4. Exact JSON-lines field names are the builder's, but must match between worker and client and be pinned by a golden round-trip test.

### Round one-liners

- Round 1 (R3a): pipe protocol + worker skeleton + daemon pipe client + `go list -deps` isolation test.
- Round 2 (R3b): Turso backend — replica ownership, remote log/head/meta creation, export/pull/head/stats.
- Round 3 (R3c): breaker — in-call marker, backoff, latch-after-three, restart-after-cancel.
- Round 4 (R3d): daemon cutover to the pipe transport; old engine seam deleted; baselines regenerated.
- Round 5 (R4a): enable/join — bootstrap, import, chunked resumable reconcile-export under `sync.join`.
- Round 6 (R4b): steady state — seal + idle triggers drive the pipeline; real-`Tick` reachability test.
- Round 7 (R4c): disable/leave + `retry` + `ErrSyncUnavailable` removal.
- Round 8 (R4d): status/statusline/`:sync` surface + final baselines + chain-green proof.

Global rules for every round: builder first reads `internal/synclog` as it exists and adapts names to it; `cmd/relevo` tests stay pure functions (no harness, network, or worker — test the rule in `internal/relevo`/`internal/sync*` instead); CLAUDE.md style (no history in code or tests, functions ≤ 70 lines, files ≤ 600 lines, comments say why); no new lint/comment/filesize exclusion (`.golangci.yml`, `scripts/check-comments.sh`, `scripts/check-filesize.sh` stay clean); new packages get baseline lines via `sh scripts/check-coverage.sh --write`, and no round lowers a baseline; each round ends with `make check` and `go build -tags modernc ./...` green.

## Deletions (closed list — nothing else is deleted)

1. Round 4: `internal/sync/client.go` — `SyncClient` interface (`Push/Pull/Stats/Checkpoint`), replaced by the pipe client as `synclog.LogTransport`.
2. Round 4: `internal/sync/fake.go` — old-shape `Fake`; R2's `synclog` in-memory fake is the surviving fake.
3. Round 4: `internal/sync/open.go` — `OpenConfig`/`Opener`; the worker owns the only remote open.
4. Round 7: `ErrSyncUnavailable` (`internal/sync/sync.go:22`) and its comment block.
5. Round 7: `VerbRunner.unavailable()` (`internal/relevo/syncverb.go:99-109`) and the enable/push/pull refusal branch (`syncverb.go:62-63`), replaced by real verb paths.
6. Round 7: `SyncTest`'s refusal in `internal/ui/actions_sync.go:178-183`, rewired per §3c.4; refusal-pinning assertions in `internal/ui/actions_sync_test.go:238` and `internal/relevo/syncverb_test.go:104` rewritten.
7. Round 6: no-op-pinning tests in `internal/relevo/sync_tick_test.go` (`TestSyncSealOpensNoHandle`, `TestSyncIdleTickIsAReachableNoOp`, `TestSyncSealIsQuietWhenItSealedNothing`, `TestSyncTickWithVerbsOpensNoHandle`, and the no-call assertions in `TestSyncNeverBlocksSeal`), replaced by pipeline-driving tests.
8. Round 6: `queueSync`/`idleSync` no-op bodies (`internal/relevo/sync_tick.go:21-27`), replaced by the pipeline.

## Report requirements

Each round's report states: new commit hashes (no amend/rebase); `internal/synclog` as found that round and names adapted; the named failing test(s) with the mutation performed (what was broken, what failed); `make check` output and `go build -tags modernc ./...` output; coverage-baseline action (`--write` or untouched, never lowered); conflicts with the spec or code found (halt rather than improvise — a step that is impossible as written halts the round and is reported); and what was deliberately left for a later round.

---

<!-- r4/round-4.md -->

# R3/R4 round 8 of 8: R4d: status surface + final baselines + chain-green proof

Slices R3 (worker) and R4 (wiring) of the sync redesign, branch
`relevo/sync-log`. Read the spec first, especially §3a-§3c:
`docs/specs/2026-10-07-sync-redesign-design.md`. R2 (`internal/synclog`) and R3 are on this branch; read them before anything
else and use their names. Every
round ends with `make check` and `go build -tags modernc ./...` green; new
commits only.

Rules: CLAUDE.md style (no history in code or tests: no "used to", "no
longer", "old ...", "round N"; functions <= 70 lines; files <= 600 lines;
comments say why); no new lint/comment/filesize exclusion; no lowered
baseline; `cmd/relevo` tests are pure functions (no harness, network, worker).

## MasterMind amendments for the whole slice (these override the plan below)

- **Testable backend.** CI has no network and no Turso. Split the Turso
  backend into (a) the replica SQL (create log/head/meta, seq assignment,
  append, head update, pull reads, head paging) over a `*sql.DB`/connection it
  is handed, and (b) the driver calls (bootstrap, `Push`, `Pull`, stats)
  behind a small interface. CI tests run (a) against a plain local file with a
  fake (b) that can fail or panic-exit; only the env-gated test uses the real
  driver. In production every statement still goes through
  `TursoSyncDb.Connect()`.
- **Batches.** The worker writes each `export` request as ONE replica
  transaction and sets `Entry.batch` to the first seq it assigned in that
  request (spec §3a.2). It refuses any entry whose origin is not the origin
  given in `hello`.
- **Never write into a foreign database.** On first use: a remote with no
  tables gets `log`/`head`/`meta` (meta records the log format version); a
  remote whose `meta` matches is used; ANY other remote (other tables, no
  meta, a different format) is refused with a fixed message naming the URL
  and saying it is not a relevo log. Pin with
  `TestWorkerRefusesARemoteThatIsNotALog`.
- **Lifecycle.** The worker exits when its stdin closes. The daemon kills its
  worker on shutdown and on re-exec (upgrade), and a fresh daemon starts a
  fresh worker; no orphaned worker may keep `relevo-sync.db` open. Pin with
  `TestWorkerExitsWhenTheDaemonGoesAway`.
- **Latch gates the tick.** While the breaker is latched, the seal and idle
  triggers do nothing until `relevo db sync retry`.
- Round headers below say "R3a".."R4d"; they are rounds 1-8 of THIS chain.

## MasterMind notes for R4 (these override anything below)

- R3 is on this branch: `internal/syncworker` (worker, Turso backend, pipe
  protocol with refusal codes), `internal/syncpipe` (client, supervisor) and
  the breaker in `internal/sync`. The daemon builds its one pipe client in
  `syncpipe.NewSyncRunner`, called from `cmd/relevo/syncverb_hook.go`; R4
  fills its `Config` (worker executable, replica path beside `relevo.db`,
  remote URL from the `sync` section, token from the local secret) when an
  enable decides the remote. Read these before anything else.
- The importer reports three kinds of trouble besides errors: `Held` (newer
  schema), `Drops` (refused batches) and `Gaps` (a hole in an origin's
  sequence). Status, the statusline token and `:sync` must surface all three,
  not only held origins.
- Coverage baseline: add or raise only the lines of packages this round
  changes; never regenerate the whole file and never lower a line.

## Plan for this round

Goal: R4 ends with `relevo db sync status`, the statusline token, and `:sync` showing on/off, latched breaker + cause, outbox backlog, last export/import times, and newer-schema-held origins per §3c.4; final coverage baselines; proof the whole chain is green.

Seams: `cmd/relevo/db_sync.go:153-180,321-375` (`dbSyncStatusDoc`, `cmdDBSyncStatus`, `dbSyncStatusLine` — extended as pure functions); `internal/sync/sync.go:50-118` (`State`, `Token`, `ReadState`, `StatusToken` — extended with latch/backlog/times/held-origins inputs); `internal/sync/statusline_test.go` (mapping tests extended); `internal/ui/actions_sync.go:18-63,205-262` (`SyncSnapshot`, `SyncSnapshot()` — new fields); `cmd/relevo/status.go:106-240` (statusline path); `testdata/coverage-baseline.txt` final `--write`.

Steps:

1. Deliverable: extended status document as pure functions (doc + one-line render) — verified by `TestStatusLineShowsLatchBacklogTimesAndHeldOrigins` failing if any field is missing or the token order is wrong.
2. Deliverable: statusline token mapping extended (latched breaker → `sync:err` with cause available to the view) — verified by extended `statusline_test.go` cases failing if `err` does not outrank `behind`.
3. Deliverable: `:sync` snapshot shows the same fields — verified by `TestSyncSnapshotCarriesTheR4Fields` failing on a missing field.
4. Deliverable: newer-schema-held origins reported, not skipped (R2 surfaces the hold; this round displays it) — verified by `TestStatusNamesTheHeldOrigin` failing if the hold is silent.
5. Deliverable: final `sh scripts/check-coverage.sh --write` with no lowered line, `make check` + `go build -tags modernc ./...` green, plus a full-chain log reference — verified by pasting the gate output.
6. Deliverable: R4 exit checklist in the report (join marker, triggers via real `Tick`, disable file deletion, status fields, `retry`, `ErrSyncUnavailable` gone).

Report must include: status field table; token-order proof; final baseline diff; chain commits (new only, no amend/rebase); both green builds; explicit R3+R4 exit-criteria sign-off.

## Context: the slice preamble

### Conflicts between the seed and the code (flagged, not re-opened; §3b/§3c stay settled)

- C1. The seed says R2's `internal/synclog` "will be on the branch before R3 starts". It is not: `ls internal/synclog` fails on this tree, and nothing imports it. Every R3 round therefore plans against the spec's §3a contract, and every round's first step orders the builder to read `internal/synclog` as it exists on the branch and adapt names to it.
- C2. The seed names `NewTursoSyncDb`/`Connect()`, `BootstrapIfEmpty`, `PushOperationsThreshold`, `PullBytesThreshold`. None of these strings occurs anywhere in the tree (`grep` over `*.go` is empty). `go.mod:16` carries `tursogo v0.8.1`; `internal/db/engine_turso.go:1-363` shows only local-open patterns (`openPool`, `newTursoConnector`). Round 2's first step orders the builder to read the tursogo module source for the real sync API and to halt-and-report if the API cannot provide bootstrap-via-`Connect()` plus push/pull thresholds as specced, rather than inventing a wrapper.
- C3. The current `internal/sync` seam is the old file-replication engine shape, not the log shape: `SyncClient` (`client.go:20-29`: `Push/Pull/Stats/Checkpoint`), `Fake` (`fake.go`), `OpenConfig/Opener` (`open.go:17-36`), `Runner{Client, Local}` (`runner.go:36-43`). R3 replaces this seam with a pipe client implementing `synclog.LogTransport`; the old types are deleted in round 4 (see deletion list). R2's in-memory fake is the surviving fake; any name collision with `sync.Fake` is resolved by deleting `sync.Fake`, never by renaming R2's.
- C4. Seal/idle triggers are currently pinned no-ops: `queueSync`/`idleSync` in `internal/relevo/sync_tick.go:21-27` do nothing, and `Daemon.Tick` (`internal/relevo/daemon.go:219-306`) calls `queueSync` after `sealed > 0` (`daemon.go:470-472`) plus `truncateOutbox` while off (`daemon.go:257`, `sync_tick.go:42-74`). R4 rounds 5–6 replace the no-ops with the drain/export/pull/import pipeline; the no-op-pinning tests (`sync_tick_test.go`, e.g. `TestSyncSealOpensNoHandle`, `TestSyncIdleTickIsAReachableNoOp`) are deleted/rewritten there, not earlier.
- C5. `Disabler.Disable` (`internal/sync/disable.go:79-102`) runs four steps with no replica-file deletion; R4 round 7 extends the contract (stop worker, delete token, delete `relevo-sync.db` + driver files, keep `relevo.db` rows).
- C6. `ErrSyncUnavailable` (`internal/sync/sync.go:22`) is still raised at `internal/relevo/syncverb.go:108` and `internal/ui/actions_sync.go:182`, with tests pinning it (`syncverb_test.go:104`, `actions_sync_test.go:238`). It is removed only in round 7, when the verbs work.

### Open points (builder decides, reports, does not re-open the spec)

- O1. New-package name for the worker (suggested `internal/syncworker`, one package per concept); daemon-side pipe client lives beside it, breaker beside the client.
- O2. `sync.join` progress marker and the breaker in-call marker both live in `relevo-local.db` per spec; suggested as `sync.join` / `sync.incall` keys in the existing `db.KV` marker store reached via `LocalHandle` (`internal/sync/sync.go:43-48`).
- O3. `push`/`pull` one-shot verbs keep their names and become drain/export/pull/import one-shots; `retry` and `reconcile --dry-run` surface in rounds 5/7 as specced.
- O4. Exact JSON-lines field names are the builder's, but must match between worker and client and be pinned by a golden round-trip test.

### Round one-liners

- Round 1 (R3a): pipe protocol + worker skeleton + daemon pipe client + `go list -deps` isolation test.
- Round 2 (R3b): Turso backend — replica ownership, remote log/head/meta creation, export/pull/head/stats.
- Round 3 (R3c): breaker — in-call marker, backoff, latch-after-three, restart-after-cancel.
- Round 4 (R3d): daemon cutover to the pipe transport; old engine seam deleted; baselines regenerated.
- Round 5 (R4a): enable/join — bootstrap, import, chunked resumable reconcile-export under `sync.join`.
- Round 6 (R4b): steady state — seal + idle triggers drive the pipeline; real-`Tick` reachability test.
- Round 7 (R4c): disable/leave + `retry` + `ErrSyncUnavailable` removal.
- Round 8 (R4d): status/statusline/`:sync` surface + final baselines + chain-green proof.

Global rules for every round: builder first reads `internal/synclog` as it exists and adapts names to it; `cmd/relevo` tests stay pure functions (no harness, network, or worker — test the rule in `internal/relevo`/`internal/sync*` instead); CLAUDE.md style (no history in code or tests, functions ≤ 70 lines, files ≤ 600 lines, comments say why); no new lint/comment/filesize exclusion (`.golangci.yml`, `scripts/check-comments.sh`, `scripts/check-filesize.sh` stay clean); new packages get baseline lines via `sh scripts/check-coverage.sh --write`, and no round lowers a baseline; each round ends with `make check` and `go build -tags modernc ./...` green.

## Deletions (closed list — nothing else is deleted)

1. Round 4: `internal/sync/client.go` — `SyncClient` interface (`Push/Pull/Stats/Checkpoint`), replaced by the pipe client as `synclog.LogTransport`.
2. Round 4: `internal/sync/fake.go` — old-shape `Fake`; R2's `synclog` in-memory fake is the surviving fake.
3. Round 4: `internal/sync/open.go` — `OpenConfig`/`Opener`; the worker owns the only remote open.
4. Round 7: `ErrSyncUnavailable` (`internal/sync/sync.go:22`) and its comment block.
5. Round 7: `VerbRunner.unavailable()` (`internal/relevo/syncverb.go:99-109`) and the enable/push/pull refusal branch (`syncverb.go:62-63`), replaced by real verb paths.
6. Round 7: `SyncTest`'s refusal in `internal/ui/actions_sync.go:178-183`, rewired per §3c.4; refusal-pinning assertions in `internal/ui/actions_sync_test.go:238` and `internal/relevo/syncverb_test.go:104` rewritten.
7. Round 6: no-op-pinning tests in `internal/relevo/sync_tick_test.go` (`TestSyncSealOpensNoHandle`, `TestSyncIdleTickIsAReachableNoOp`, `TestSyncSealIsQuietWhenItSealedNothing`, `TestSyncTickWithVerbsOpensNoHandle`, and the no-call assertions in `TestSyncNeverBlocksSeal`), replaced by pipeline-driving tests.
8. Round 6: `queueSync`/`idleSync` no-op bodies (`internal/relevo/sync_tick.go:21-27`), replaced by the pipeline.

## Report requirements

Each round's report states: new commit hashes (no amend/rebase); `internal/synclog` as found that round and names adapted; the named failing test(s) with the mutation performed (what was broken, what failed); `make check` output and `go build -tags modernc ./...` output; coverage-baseline action (`--write` or untouched, never lowered); conflicts with the spec or code found (halt rather than improvise — a step that is impossible as written halts the round and is reported); and what was deliberately left for a later round.
