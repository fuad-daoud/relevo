# Sync redesign R3: the sync worker

The round plans of this slice of the sync redesign, in the order they ran,
as the builders received them. Spec: `docs/specs/2026-10-07-sync-redesign-design.md`.

---

<!-- r34/round-1.md -->

# R3/R4 round 1 of 8: R3a: pipe protocol, worker skeleton, daemon pipe client

Slices R3 (worker) and R4 (wiring) of the sync redesign, branch
`relevo/sync-log`. Read the spec first, especially §3a-§3c:
`docs/specs/2026-10-07-sync-redesign-design.md`. R2 (`internal/synclog`) IS
on this branch now; read it before anything else and use its names. Every
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

## Plan for this round

Goal: `relevo sync-worker` exists as a hidden subcommand speaking JSON lines over stdin/stdout (one request in flight, each with an id; stderr to the daemon log; token in the first request, never argv/env), with a daemon-side pipe client implementing `synclog.LogTransport`; the worker package never imports `internal/db`.

Seams: `cmd/relevo/main.go:75-76` (subcommand registration); new `internal/syncworker/protocol.go` (request/response types + codec); new `internal/syncworker/worker.go` (stdin/stdout loop, dispatch to a `Backend` interface); new `internal/syncworker/client.go` (spawn `os.Executable` child, `hello/export/pull/head/stats/shutdown`, per-call deadlines, kill-on-miss); `internal/syncworker/worker_test.go`, `client_test.go` (fake `Backend`, test-binary re-exec helpers in the style of `internal/db/reexec_test.go:22-30`); `internal/synclog` (read first, adapt `LogTransport` method names).

Steps:

1. Deliverable: `LogTransport` method set as it exists on the branch, recorded in the round report — verified by the report quoting the interface verbatim so later rounds adapt to it.
2. Deliverable: protocol types + JSON-lines codec with golden round-trip test (`TestProtocolRoundTripPinsEveryRequest`) — verified by changing one field name and seeing that test fail.
3. Deliverable: worker loop behind a `Backend` interface (fake backend in tests; Turso backend lands in round 2) — verified by `TestWorkerServesExportPullHeadStatsShutdown` failing if any verb is unhandled.
4. Deliverable: hidden `sync-worker` subcommand wired in `cmd/relevo` (no CLI test spawns it) — verified by `relevo sync-worker --help` staying hidden from the help golden (`cmd/relevo/testdata/contract/help-json.golden`).
5. Deliverable: daemon pipe client (spawn, hello-with-token, request/response matching, deadline, kill-and-count hook for round 3) driving the fake backend through a real pipe — verified by `TestPipeClientDrivesAWorkerOverAPipe` failing if a reply is matched to the wrong id.
6. Deliverable: fake-worker re-exec modes die/hang/refuse exercised via test-binary re-exec — verified by `TestFakeWorkerDeathSurfacesAsCallError`, `TestFakeWorkerHangHitsTheDeadline`, `TestFakeWorkerRefusalSurfacesTheCause`, each failing if the client returns success.
7. Deliverable: `TestWorkerPackageDoesNotImportInternalDB` via `go list -deps` — verified by adding the import and seeing it fail.
8. Deliverable: coverage baseline lines for `internal/syncworker` via `sh scripts/check-coverage.sh --write` — verified by `make check` green with no lowered baseline elsewhere.

Report must include: the `LogTransport` interface as found; protocol field table; new-commit hashes; `make check` + `go build -tags modernc ./...` outputs; baseline diff.

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

<!-- r34/round-2.md -->

# R3/R4 round 2 of 8: R3b: Turso backend (replica ownership, remote schema, export/pull/head/stats)

Slices R3 (worker) and R4 (wiring) of the sync redesign, branch
`relevo/sync-log`. Read the spec first, especially §3a-§3c:
`docs/specs/2026-10-07-sync-redesign-design.md`. R2 (`internal/synclog`) IS
on this branch now; read it before anything else and use its names. Every
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

### Round 2 amendment
Apply the testable-backend split and the foreign-database refusal here.

## Plan for this round

Goal: the Turso `Backend` implementing the pipe verbs: replica `relevo-sync.db` beside `relevo.db` opened only via `NewTursoSyncDb`/`Connect()` with `BootstrapIfEmpty`; first use creates remote `log`/`head`/`meta` (spec §2) over a sync connection and pushes; `export` assigns `seq = max+1` per origin in one replica transaction, appends `log`, updates `head`, pushes with `PushOperationsThreshold`, and replies only after the push; `pull` pulls with `PullBytesThreshold` then returns other origins' entries past marks; `head(origin, after, limit)` pages; `stats`; `shutdown`; never checkpoint/vacuum/copy/edit the replica or driver files; holds no `relevo.db` handle.

Seams: `internal/syncworker/turso.go` (new; Turso backend); `internal/db/engine_turso.go:60-111` (local-open precedent only — not reused for sync); `go.mod:16` (tursogo v0.8.1 source, read for the real API); env-gated test behind `RELEVO_SCRATCH_SYNC_URL`/`RELEVO_SCRATCH_SYNC_TOKEN`; `docs/sync-driver-panic.md` (abort behaviour record).

Steps:

1. Deliverable: Turso sync API findings (constructors, options, push/pull entry points) quoted from the module source — verified by the report naming file/line in the module cache; halt-and-report here if the API cannot do bootstrap-via-`Connect()` plus thresholds (C2).
2. Deliverable: replica open path used by every statement (`Connect()` per statement, `BootstrapIfEmpty` true) — verified by `TestReplicaOpensOnlyThroughTheSyncConstructor` failing if a test double opens the file directly.
3. Deliverable: remote `log`/`head`/`meta` creation on first use against an empty remote, idempotent on re-run — verified by `TestFirstUseCreatesTheRemoteLogTables` failing if a second run errors or a table is missing.
4. Deliverable: `export` seq assignment + `head` update + reply-after-push — verified by `TestExportAssignsMaxPlusOneAndRepliesOnlyAfterPush` (killed-before-push means the daemon must not delete outbox rows) failing if `seq` is taken from the request or the reply precedes the push.
5. Deliverable: `pull` past-marks filtering + `head` paging for reconcile — verified by `TestPullReturnsOnlyEntriesPastTheMarks` and `TestHeadPagesInSeqOrder` failing on off-by-one or cross-origin leak.
6. Deliverable: scratch-remote test (`TestScratchRemoteExportPullRoundTrip`, skipped unless both env vars are set) — verified by running it once on zen with the vars set and recording the result; CI skips by design.
7. Deliverable: `go list -deps` test still green with the Turso backend included — verified by `make check` + `go build -tags modernc ./...` green.

Report must include: Turso API surface found; replica path rule; scratch-remote result or why skipped; new commits; both green builds; baseline handling (new file in existing package — `--write` only if the package line is absent).

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

<!-- r34/round-3.md -->

# R3/R4 round 3 of 8: R3c: breaker (marker, backoff, latch, restart-after-cancel)

Slices R3 (worker) and R4 (wiring) of the sync redesign, branch
`relevo/sync-log`. Read the spec first, especially §3a-§3c:
`docs/specs/2026-10-07-sync-redesign-design.md`. R2 (`internal/synclog`) IS
on this branch now; read it before anything else and use its names. Every
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

## Plan for this round

Goal: daemon-side supervision per spec §3b.5: in-call marker in `relevo-local.db` written before each request and cleared on reply; worker death or deadline miss kills and counts (plus a marker found uncleared at daemon start counts); backoff doubling from one minute; three consecutive deaths latch `sync:err` with the cause until cleared; permanent remote refusal latches the same way; a cancelled worker is restarted, never reused.

Seams: new `internal/syncworker/breaker.go` (counts, backoff, latch state over `db.KV` via `relevosync.Local`); `internal/sync/sync.go:80-107` (`TokenOff/OK/Behind/Err` mapping — `Err` is the latched token, cause read via `ReadAttention`-style marker); `internal/sync/remoterefusal.go:22-34` (`ErrRemoteRefused`/`ErrRemoteSchema` feed the permanent-latch classification); `internal/relevo/daemon.go:71-100` (`syncMu`/`syncInFlight` guard — breaker composes with it, does not replace it).

Steps:

1. Deliverable: in-call marker write-before/clear-after on every pipe call — verified by `TestUnclearedMarkerCountsAtDaemonStart` (kill -9 the worker mid-call, marker present, next start counts one death) failing if the marker is cleared on failure paths.
2. Deliverable: death counting (exit mid-call, deadline miss, uncleared-at-start) with `TestWorkerDeathIsCounted` / `TestMissedDeadlineKillsAndCounts` failing if the daemon keeps serving a dead worker or drops the count.
3. Deliverable: exponential backoff from one minute — verified by `TestBackoffDoublesFromOneMinute` (pure clock-injected test, no sleeping) failing if the second delay is not double the first.
4. Deliverable: latch after three consecutive deaths with cause, success resets the count — verified by `TestThreeDeathsLatchSyncErr` failing if the third death does not latch or a later success does not reset.
5. Deliverable: permanent refusal latches instead of showing `sync:behind` — verified by `TestPermanentRefusalLatches` failing if a classified-permanent error only counts as a death.
6. Deliverable: cancel restarts, never reuses — verified by `TestCancelledWorkerIsRestartedNeverReused` failing if the same process handles the next call.
7. Deliverable: latch-clear seam (`ClearLatch`) for round 7's `retry` verb, with `TestLatchPersistsUntilCleared` failing if anything but the clear path unlatches.

Report must include: marker key names; backoff table; latch cause text (fixed text, no remote body, no token); new commits; both green builds.

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

<!-- r34/round-4.md -->

# R3/R4 round 4 of 8: R3d: daemon cutover to the pipe transport (R3 close)

Slices R3 (worker) and R4 (wiring) of the sync redesign, branch
`relevo/sync-log`. Read the spec first, especially §3a-§3c:
`docs/specs/2026-10-07-sync-redesign-design.md`. R2 (`internal/synclog`) IS
on this branch now; read it before anything else and use its names. Every
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

## Plan for this round

Goal: R3 ends with the daemon driving sync only through the pipe client as its `synclog.LogTransport`: the old file-replication seam is deleted (see deletion list §1–3), the runner holds the pipe client, CI tests drive the fake worker from rounds 1–3, and the tree is green with regenerated baselines. No R4 behaviour yet: enable still refuses, triggers still no-op.

Seams: `internal/sync/runner.go:36-43` (`Runner` — `Client` field becomes the pipe `LogTransport` client); `internal/sync/client.go`, `fake.go`, `open.go` (deleted); `internal/relevo/syncverb.go:30-52,60-73` (`VerbRunner` — unavailable path stays until round 7); `internal/relevo/sync_tick_test.go` (untouched — still pins no-ops until round 6); `testdata/coverage-baseline.txt` + `scripts/check-coverage.sh --write`.

Steps:

1. Deliverable: `Runner.Client` replaced by the pipe-client `LogTransport` (adapted to `internal/synclog` as found) with construction in exactly one place — verified by `TestDaemonBuildsOnePipeClient` failing if two clients are constructed for one daemon.
2. Deliverable: deletions §1–3 below executed, with every import fixed — verified by `go build ./...` and `go build -tags modernc ./...` green plus `grep -rn "SyncClient\|OpenConfig" --include="*.go" internal cmd` empty except historical comments (which are also removed per no-history rule).
3. Deliverable: fake-worker coverage for die/hang/refuse now runs through the daemon's client (not just the raw pipe) — verified by round-1 tests re-targeted, failing on the same conditions.
4. Deliverable: baselines regenerated with `--write` for moved code, no line lowered — verified by baseline diff in the report showing only adds for `internal/syncworker` (and removals for deleted files).
5. Deliverable: `make check` green (gofmt, vet, lint, comments, filesize, tidy, coverage) — verified by pasting the gate output.

Report must include: deletion checklist with `git diff --stat`; `internal/synclog` names as adapted to; baseline diff; both green builds; explicit "R3 exit criteria met" table (subcommand, protocol, replica-via-`Connect()`, remote tables, pipe transport client, breaker, `-deps` test, fake-worker die/hang/refuse tests, env-gated real-Turso test).

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
