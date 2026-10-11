# MasterMind amendments (these override the plan below)

1. **Measure first, then fix what the measurement names.** Step 1 is mandatory and decides the rest. Record Go heap (pprof `inuse_space`) vs RSS at start and after ~10 minutes idle (not 30) on the synthetic database, in an isolated XDG_STATE_HOME, with exact commands.
2. **Drop steps 3 and 4 (streaming `splitReadRows`, bounding the dedupe planner)** unless step 1 shows one of them dominates the start heap. Both are one-time passes that have already run on every real install, and after the split fix on main the split finds nothing to move, so streaming them saves nothing in practice. If you skip them, say so in the report with the step-1 numbers.
3. **Step 2 (engine cache + idle connections) is the core.** Find what the Turso engine actually honours (`PRAGMA cache_size` may be ignored or differently named in tursogo v0.8.1 -- read the library or test it; do not assume). Keep `MaxOpenConns` unchanged (the pool ceiling is load-bearing for the database owner's concurrency); reduce idle retention (`SetMaxIdleConns` and/or `SetConnMaxIdleTime`) and pin it in poolbound_test.go. Show the before/after RSS on the same fixture.
4. **Step 5 (growth):** only the outbox question is in scope -- check whether `sync_outbox` grows without bound while sync is off and, if it does, bound it within `outbox.go`'s own contract. Do not touch WAL checkpointing or the served-handle reclaim (those were just changed on main).
5. **Step 6 (OOM ordering) is in scope as written**, with the systemd man-page citation for whether `ManagedOOMPreference` is honoured on a user-manager unit. If it is not, prefer the mechanism that is (e.g. `OOMScoreAdjust` on the unit, `ManagedOOMPreference=prefer` or a positive OOM score on round scopes) and say why.
6. Scope list: remove `internal/ingest/dedupe.go`, `internal/db/split.go`, `internal/db/compress.go`, `internal/db/cdc_drop.go`, `internal/db/origin*.go`, `internal/db/reclaim_free.go`, `internal/db/vacuum.go` unless amendment 2's exception applies (then name it in the report).
7. Do not fetch (no origin remote on the server worktree). Never use `git stash`. Commit the plan with these amendments as docs/plans/2026-10-11-daemon-memory.md. Fixes #1124 only if the measured start heap and growth are both bounded; otherwise use `Refs #1124` and say what remains.

---

# Plan: daemon heap ~2.3 GB at start and growing (daemon-memory)

Base: `origin/main` at `f1931b74`. All line numbers below are at that base.
Conventions from `CLAUDE.md`: no history in code or tests (no issue/PR
numbers in comments, names, or strings), comments say why only, functions
<= 70 lines, non-test files <= 600 lines, no new lint exclusions, coverage
baseline in `testdata/coverage-baseline.txt` regenerated with
`sh scripts/check-coverage.sh --write` if code moves packages (never lowered
to get green).

## 1. What the code actually shows (evidence, not guesses)

### 1.1 The engine is native code with no cache cap

- `go.mod:12,16` selects `tursogo v0.8.1` over
  `tursodatabase/turso-go-platform-libs v0.8.1` (plus `modernc.org/sqlite`
  as the second driver linked into the same binary,
  `internal/db/engine_turso.go:21-24`). The library loaded is a native
  object (`libturso_sync_sdk_kit.so`, `engine_turso.go:378-387`, extracted
  and `dlopen`ed in `extractAndLoad`, `engine_turso.go:306-336`). A heap
  proportional to the database that does not appear in a Go `pprof` heap
  profile therefore points at this native page cache, not at Go objects.
- `openPool` (`engine_turso.go:64-101`) sets
  `pool.SetMaxOpenConns(128)` and `pool.SetMaxIdleConns(128)`
  (`engine_turso.go:92-93`; the ceiling is pinned by
  `internal/db/poolbound_test.go`). Every idle connection keeps its own
  native page cache, so the pool multiplies one connection's cache by up to
  128. Nothing in the repo sets `PRAGMA cache_size`: a grep for
  `cache_size|page_cache|mmap` over the tree finds no production hit, and
  `openPragmas` (`engine_turso.go:123-128`) issues only `journal_mode`,
  `foreign_keys`, and `temp_store = MEMORY` per connection (`temp_store =
  MEMORY` itself keeps temp tables and sorts in engine memory on every one
  of those connections). The `modernc` build (`engine_modernc.go:37-45`)
  likewise sets no `cache_size`, but it does cap the WAL at 64 MiB
  (`journalSizeLimit`, `engine_modernc.go:21`); the Turso path has no such
  cap. So on current main there are two independent unbounded native
  consumers: page cache x up to 128 idle connections, and WAL/temp-store
  growth with no size limit.
- `DB.Close` checkpoints the WAL (`internal/db/db.go:305-333`), and the
  one-time passes checkpoint explicitly (`walCheckpoint`,
  `internal/db/compress.go:307-312`, called from `finishCompress`,
  `compress.go:292-303`). A long-lived daemon that never closes its handle
  and whose passes now skip compaction on a served handle (see 1.3) keeps
  whatever the WAL and the native cache accumulate between restarts.

### 1.2 Startup reads a database-sized amount of memory

Order at start is `daemonStartup` (`cmd/relevo/daemon_startup.go:50-58`):
`reclaimBeforeServing`, then serve the owner, then the passes, then the
sync-verb hook. The passes (`runStartupPasses`,
`daemon_startup.go:109-189`, plus `ConvertLegacyChains` in `newRuntimeOn`,
`cmd/relevo/wire.go:195-197`) are:

1. `ingest.DedupeMirrorOnce` (`internal/ingest/dedupe.go:465-520`): plans
   by loading **all** bindings (`dedupe.go:97`), **all** live plus archived
   records (`dedupe.go:102-110`), then per round the full transcript with
   no limit (`planTranscript` calls `Transcript(..., 0, 0)`,
   `dedupe.go:194`) and full round-file bodies (`RoundFileGet`,
   `dedupe.go:172,332,368`). Takes a full `BackupTo` copy first
   (`dedupe.go:487-491`).
2. `db.CompressHistoryOnce` (`internal/db/compress.go:76-116`): walks
   `round_file`/`transcript` in bounded batches of 200 rows
   (`compressBatchRows`, `compress.go:23`; `compressBatch`,
   `compress.go:180-192`), so row iteration is already bounded; but it
   takes a full `BackupTo` copy first (`compress.go:97-101`) and its
   per-row path loads whole bodies (`readCompressRow`, `compress.go:265`).
3. `dropChangeCaptureTable` (`cmd/relevo/daemon_cdc.go:16-26` ->
   `DropTursoCdcOnce`, `internal/db/cdc_drop.go:45-75`): full `BackupTo`
   per file holding the table (`backupAndDrop`, `cdc_drop.go:128-145`).
4. `daemonEnablePath` (`cmd/relevo/daemon_passes.go:43-108`) ->
   `SplitOnce` (`internal/db/split.go:268-329`) + `BackfillOriginOnce`
   (`internal/db/origin.go` and siblings): `SplitOnce` takes a full
   `BackupTo` (`split.go:294-298`) and `splitReadRows`
   (`split.go:386-410`) loads **every row of each moved table into one
   `[][]any`** before copying and deleting. That is the single largest
   confirmed Go-heap spike proportional to table size.
5. `ReclaimFreePages` before serving (`daemon_startup.go:65-78` ->
   `internal/db/reclaim_free.go:31-58`) runs `VACUUM` (via `vacuum.go`),
   which rewrites the file through a full sibling copy (`vacuumInto`,
   `internal/db/vacuum.go:33-79`).

`BackupTo` itself is `VACUUM INTO` (`internal/db/vacuum.go:22-27`), i.e. a
whole-database copy; a first-ever start after the two recent merges can
still take up to four whole-database copies (dedupe, compress, split, cdc)
plus a vacuum rewrite, each transiently doubling RSS. `collectRows`
(`internal/db/scan.go:15-32`) materialises every query result into a slice;
`listTranscript` (`internal/db/lookup.go:168-186`), `RoundFileGet`
(`internal/db/roundfile.go:46-71`), and `splitReadRows` all return whole
bodies/rows to the caller. The seed's "start heap is about the database's
size" is consistent with `splitReadRows` plus native cache fill plus
backup/vacuum copies, but which half (Go heap vs native) dominates must be
settled by the measurement in step 1, not assumed here.

Note on the seed's premise: since merges #1141/#1142 the shared file should
fall toward ~250 MB and the reclaim now runs before serving, so a start
heap still near 2 GB on a fresh large file falsifies the "it follows the
file down" hope, while a start heap near 250 MB confirms the file-size
coupling and leaves only the growth half. Step 1 records which outcome
holds; if the heap already tracks the smaller file, the bounding work
concentrates on the growth half (WAL/idle-cache/outbox) and the
whole-table slurps that remain (`splitReadRows`, dedupe planning).

### 1.3 What grows afterwards (candidates with file:line homes)

- `sync_outbox` triggers record every shared-table write
  (`internal/db/migrations/022_sync_outbox.sql`, `internal/db/db.go:380-384`
  notes triggers fire on every pooled connection). `TruncateOutbox` /
  `HasOutboxEntries` exist (`internal/db/outbox.go:21-48`) but no tick
  path in the evidence drains or bounds the table on a machine whose sync
  is off; confirm or refute by grep in step 2. An undrained outbox grows
  one row per write forever.
- The served handle never compacts: `Reclaim` skips on a served handle
  (`internal/db/vacuum.go:135-144`), and the passes' `finishCompress`
  path records the skip (`compress.go:292-303`). Free pages therefore
  accumulate on the file's free list until the next restart's
  `ReclaimFreePages`; the file (and any reader that maps it) only grows
  between restarts.
- Per-tick in-daemon retention: `ingestSeen map[string]string` on the
  daemon (`internal/relevo/daemon.go:69`, populated near line 705),
  `bindingMasterMindCounts` maps (same file, ~839+), the release-check kv
  cache (same file, ~312-374), `Watched`/`AuthGrace` sets
  (`cmd/relevo/daemon.go:229-234`). None is database-sized, but each must
  be shown bounded or fixed in step 2.
- 128 idle connections (`engine_turso.go:92-93`) each hold a native cache
  and `temp_store = MEMORY` workspace for the process's life; with no
  `SetConnMaxLifetime`/`SetConnMaxIdleTime` anywhere in the tree (grep
  finds only the two `SetMax*` calls), idle native memory never drains.

### 1.4 The unit / scope half

- `dist/relevo.service:18-28` deliberately sets no `MemoryMax` and
  `OOMPolicy=continue`, with the reason (builders share the unit's cgroup
  only when scopes are unavailable). `scripts/relevo-service-template_test.sh:24-32`
  pins exactly that: fails on any `^MemoryMax=` and requires exactly one
  `OOMPolicy=continue`. Any unit change must update that script, not work
  around it.
- Round scopes are built in `ScopeArgv` (`internal/proc/scope.go:28-49`)
  from `ScopeSpec` (`internal/spawn/spawn.go:38-57`), which today carries
  `MemoryMax`/`CPUQuota`/`TasksMax`/`AllowedCPUs` but no OOM-preference
  field. The daemon's own scope template comes from policy via
  `scopeFromPolicy` (`cmd/relevo/serve_isolation.go:103-126`).

## 2. Behaviour and cases the work must cover

1. **Attribution.** With a ~1-2 GB synthetic database, heap (Go, from
   pprof) vs RSS (process, from `/proc`/ps) at daemon start, before and
   after the fix. Pre-fix expectation to confirm: RSS >> Go heap (native
   cache/copies dominate) and/or a Go spike during the split/dedupe
   planning window.
2. **Bounded engine cache.** After the fix, starting against the same
   synthetic database keeps (RSS - Go heap) under a stated bound, and
   `PRAGMA cache_size` (or the Turso equivalent setting the builder finds)
   reads back the configured value on every pooled connection, not just
   the first.
3. **Streamed passes.** `SplitOnce` and the dedupe planner move/plan the
   same rows with O(batch) peak memory: same rows moved, same conflict
   counts, same markers, on small fixtures; plus a large-row test that
   fails on the old slurp and passes streamed.
4. **No unbounded growth.** Outbox size and idle-connection native
   retention stay bounded over repeated writes/ticks (exact mechanism per
   evidence: drain, cap, or lifetime -- builder picks what the code shows
   is missing).
5. **OOM ordering.** Under memory pressure the OOM killer takes a round
   scope before the daemon owner: unit settings chosen with a cited
   systemd reference, and the template test still pins the unit's
   invariants. Round scopes prefer to be killed; the daemon prefers to
   survive.
6. **Premise check.** If on current main the start heap already tracks the
   smaller file (seed's hoped-for outcome), the plan's bounding steps
   still apply to the growth half, and the report says so with numbers
   instead of planning file-size work that no longer exists.

## 3. Seams (builder's map)

- Engine/pool: `internal/db/engine_turso.go:60-157` (`openPool`,
  `maxOpenConns`, `openPragmas`, `pragmaConnector`); `modernc` mirror
  `internal/db/engine_modernc.go:34-57`; pool-bound test
  `internal/db/poolbound_test.go`; `DB.Close` checkpoint
  `internal/db/db.go:305-333`.
- Startup order: `cmd/relevo/daemon.go:146-292` (lock, socket, pprof,
  `openDBDirect`, `newRuntimeOn`, `daemonStartup`); `cmd/relevo/daemon_startup.go:50-189`;
  `cmd/relevo/wire.go:181-277` (`newRuntimeOn`, `openDBDirect`,
  `OpenSplit`); `cmd/relevo/daemon_passes.go:43-108`;
  `cmd/relevo/daemon_cdc.go:12-26`.
- Passes: `internal/ingest/dedupe.go:92-125,465-520`;
  `internal/db/compress.go:76-192,292-312`;
  `internal/db/split.go:268-410,466-496`;
  `internal/db/cdc_drop.go:45-145`;
  `internal/db/reclaim_free.go:31-76`;
  `internal/db/vacuum.go:22-144` (`BackupTo`, `vacuumInto`, `Vacuum`,
  `Reclaim`); `internal/db/origin*.go` (backfill batching).
- Read paths that slurp: `internal/db/scan.go:15-32` (`collectRows`);
  `internal/db/lookup.go:114-186` (`Rounds`, `Transcript`);
  `internal/db/roundfile.go:44-84`; `internal/db/read.go`
  (`queryRounds`, `queryBindings` -- bounded by `LIMIT`, leave alone
  unless measurement names them).
- Growth: `internal/db/outbox.go:21-48`;
  `internal/db/migrations/022_sync_outbox.sql`;
  `internal/relevo/daemon.go:46-90,300-380,560-720,839+` (tick, caches,
  `ingestSeen`, counts); `internal/db/handles.go` (per-path handle
  sharing -- do not break the open lock).
- Unit/scopes: `dist/relevo.service:10-28`;
  `scripts/relevo-service-template_test.sh:20-62`;
  `internal/proc/scope.go:21-49` (`ScopeArgv`);
  `internal/spawn/spawn.go:36-57` (`ScopeSpec`);
  `cmd/relevo/serve_isolation.go:103-126` (`scopeFromPolicy`);
  `cmd/relevo/serve.go:251-260` (`scopeStatusText`).
- Profiling: `cmd/relevo/pprof.go:33-57` (`servePprof`, socket mode
  `0o600`); `cmd/relevo/daemon.go:42,172-183` (`--pprof` flag wiring).
- Test template infra: `internal/db/dbtest/dbtest.go:27-80` (migrated
  template seeding for synthetic databases).

## 4. Ordered steps (one line each: deliverable + how to know it worked)

1. **Attribute the heap** -- deliverable: a measurement note (Go heap
   from `relevo daemon --pprof <socket>` vs RSS) on a ~1-2 GB synthetic
   database in an isolated `XDG_STATE_HOME` before any fix; worked when
   the note names the Go-heap share and the native share with commands
   and numbers. (Never touches the server's installed relevo or state
   dir; builds the binary, generates the fixture with `dbtest` or an
   existing generator, starts the daemon against a temp root.)
2. **Cap the engine cache** -- deliverable: a per-connection cache bound
   (`PRAGMA cache_size` or the Turso equivalent) applied in
   `openPragmas`/`pragmaConnector` plus reduced idle retention (lower
   `MaxIdleConns` and/or `SetConnMaxIdleTime`/`SetConnMaxLifetime`); worked
   when a test reads the pragma back on more than one pooled connection
   and `poolbound_test.go`-style stats show the idle ceiling.
3. **Stream the split move** -- deliverable: `splitReadRows` replaced by
   a bounded-batch cursor with identical rows/conflicts/markers on
   existing split fixtures; worked when a new large-row test fails on the
   old whole-table read and passes streamed.
4. **Bound the dedupe planner** -- deliverable: dedupe planning in
   bounded batches (or a proven small bound) with identical
   plan/stats/markers; worked when the planner test suite passes plus a
   new peak-allocation test over a synthetic transcript bulk.
5. **Stop the slow growth** -- deliverable: outbox/WAL/idle-cache growth
   bounded (drain, cap, or lifetime per what step 1-2 evidence names);
   worked when a repeated-write test shows table/WAL/RSS stable across
   ticks instead of monotonic.
6. **Order the OOM kill** -- deliverable: `dist/relevo.service` unit
   settings decided with a cited systemd reference (whether
   `ManagedOOMPreference=avoid` is honoured on a user-manager unit, else
   `MemoryHigh`/`MemoryLow`/`OOMScoreAdjust`) plus `ManagedOOMPreference=prefer`
   (or the evidenced equivalent) on round scopes via `ScopeSpec`/`ScopeArgv`;
   worked when the updated template-test script passes and a pure-function
   test pins the scope argv mapping.
7. **Server before/after** -- deliverable: the step-1 measurement repeated
   after the fix on the same fixture and binary path; worked when
   start RSS and 30-minute RSS are recorded side by side with the bound
   from step 2.
8. **Full check and plan doc** -- deliverable: `make check` green plus
   the plan saved as `docs/plans/2026-10-11-daemon-memory.md` and
   committed with the code in new commit(s) naming the issue (`Fixes
   #1124`); worked when `git log` shows only new commits on top of
   `f1931b74` and `git diff --stat` stays inside the declared scope.

## 5. What is deleted or narrowed (closed list)

1. The uncapped per-connection native page cache (replaced by the step-2
   bound; default size chosen by measurement, not kept unbounded).
2. Up to 128 permanently idle native connections (idle ceiling and/or
   idle lifetime replace keep-forever; `MaxOpenConns` stays or is
   justified by measurement).
3. Whole-table in-memory materialisation in `splitReadRows` (replaced by
   bounded batches; the old slurp goes, behaviour of rows/conflicts/markers
   unchanged).
4. Unbounded dedupe-planner retention over a whole database (replaced by
   batched planning with identical plans).
5. Any unbounded outbox/WAL/idle growth the measurement names (replaced by
   the drain/cap/lifetime the builder evidences; no silent data loss: a
   reconcile-derivable truncate only, per `outbox.go`'s own contract).
6. The unit's lack of OOM ordering (replaced by the evidenced unit/scope
   settings; the `OOMPolicy=continue` + no-`MemoryMax` invariants stay
   unless the cited systemd reference justifies a change, and the template
   test is updated to pin whatever is decided).

Nothing else is removed. In particular: no pass is deleted (all remain
one-time and idempotent), no backup is skipped, `VACUUM INTO` semantics
stay, and the serve-before-passes order stays.

## 6. Scope: files the builder may touch

Allowed: `internal/db/engine_turso.go`, `internal/db/engine_modernc.go`,
`internal/db/split.go`, `internal/db/outbox.go`, `internal/db/vacuum.go`,
`internal/db/reclaim_free.go`, `internal/db/compress.go`,
`internal/db/cdc_drop.go`, `internal/db/origin*.go`,
`internal/db/scan.go`, `internal/db/lookup.go`,
`internal/db/roundfile.go`, plus their `_test.go` files;
`internal/ingest/dedupe.go` and its tests; `internal/proc/scope.go` and
`internal/spawn/spawn.go` (OOM-preference field + argv mapping) with
tests; `cmd/relevo/serve_isolation.go` + `cmd/relevo/serve.go`
(scope-template wiring only); `dist/relevo.service`;
`scripts/relevo-service-template_test.sh`; `docs/plans/2026-10-11-daemon-memory.md`;
`testdata/coverage-baseline.txt` (regenerated only via
`sh scripts/check-coverage.sh --write`, said in the report).

Off limits (three parallel lanes; do not touch unless the step names why):
`cmd/relevo/args.go`; `cmd/relevo/mastermind_hook.go`; `internal/relevo`
mastermind attach/status text; `internal/harness` agent definitions;
builder spawn env; `internal/mastermind/guide.md`. If a bound genuinely
requires one of these (not expected), the report names the file and the
reason and stops that step for review.

Test-location rule (CI has no harness binary and no network): any CLI-level
assertion for step 6 goes as a pure-function test under `internal/relevo`
or beside `scopeFromPolicy`/`ScopeArgv`, never as a `cmd/relevo` test that
spawns a harness or reaches the network; say so in the step's report line.

## 7. Per-fix tests and the mutations each must kill

Each fix ships with a test that fails without it; name the breaking
mutation in the commit/report:

- Engine-cache test: set the pragma/limit back to unset (or restore
  `MaxIdleConns = MaxOpenConns = 128` with no lifetime) and the test must
  fail (reads back unconfigured/default).
- Split-streaming test: restore `SELECT <all> FROM <table>` into one
  slice and the peak-allocation/row-count test must fail (or OOM the
  synthetic bulk).
- Dedupe-bounding test: restore full-transcript/full-table planning and
  the allocation test must fail.
- Growth test: restore no-drain/no-lifetime and the repeated-write
  stability test must fail (monotonic outbox/WAL/idle count).
- Scope-OOM test: drop the `prefer`/argv mapping and the pure-function
  argv test must fail; set `MemoryMax=` back into the template and the
  template script must fail (it already does by construction).
- Unit-settings test: whatever invariant is decided (e.g.
  `OOMPolicy=continue` exactly once, `StartLimitIntervalSec=0` in
  `[Unit]`) stays pinned by the template script; any regression fails it.

`make check` (gofmt over tracked `.go`, `go vet`, `go mod tidy` check,
golangci-lint per `.golangci.yml`, comment/filesize/coverage scripts) is
the final gate; iterate on the focused package tests first, then run the
full `make check` once at the end. On a server worktree with no `origin`
remote the builder must not `fetch`; must never use `git stash` (shared
stack) -- use a WIP commit or copies outside the repo for mutation checks.
All work lands as new commits; never amend or rebase a commit already on a
remote binding's branch.

## 8. What the report must include

- The step-1 numbers (fixture size, Go heap vs RSS at start, 30-min RSS)
  and the step-7 after numbers on the same fixture, with exact commands.
- Which half dominated (Go slurp vs native cache/copies) and the file:line
  of the responsible code.
- The chosen cache bound and idle-retention values with the systemd/driver
  reference cited (man page or docs URL + version) for the unit decision,
  including whether `ManagedOOMPreference` is honoured on user-manager
  units.
- The mutation each new test kills (exact condition broken).
- `git diff --stat` against the scope list, `make check` output, and any
  coverage-baseline regeneration (or "not regenerated").
- If the premise proved wrong on current main (heap already tracks the
  smaller file), the evidence and which steps were therefore narrowed,
  instead of file-size work with no effect.

## Round 2

Mechanism change for the OOM-ordering step: the `oom_score_adj` wrapper
(`ScopeSpec.OOMScoreAdjust`, the `sh -c` insertion in `ScopeArgv`,
`roundOOMScoreAdjust`) is removed, and the unit sets
`ManagedOOMPreference=avoid` instead. The engine cache and idle-pool bound
from round 1 stay.

Why: on the owner's laptop the killer was systemd-oomd ("killed 25
process(es) in this unit"), not the kernel OOM killer. oomd picks a cgroup by
pressure and usage and ignores `oom_score_adj`. `oomctl` monitors
`.../user@1000.service/app.slice`, `relevo.service` sits under it with
`ManagedOOMPreference=none`, and both cgroups are owned by the same user,
which is the condition under which oomd honours `ManagedOOMPreference=` on a
user-owned cgroup. A shell in every round's argv is not worth a kernel-OOM
ordering nobody has hit.

`dist/relevo.service` is installed verbatim (`make install`:
`install -Dm644`), so the line reaches the user unit unchanged; no Go
template renders it.
