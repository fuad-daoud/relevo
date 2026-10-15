# Plan lane D: sync defects (`sync-status-defects`)

Base: `origin/main` at `41bf202d`. Builder is a plan-executor on server zen, starting from that base, as new commits only (never amend or rebase a branch already on a remote binding).

Issues: fuad-daoud/relevo #1165 (`db sync pull` silent while an origin is held) and #1154 (status byte counters 2x / replica files 0644 / `ErrRemoteConflict` names nothing). The builder reads both with `gh issue view <n> --comments` first and verifies every premise below against the code; a premise that is false halts the round and is reported, not worked around.

## 0. Premises already verified from the code (builder re-confirms on zen)

- #1165 mechanism is real: one-shot `pull` (`internal/relevo/syncverb_steady.go:37-50`) calls single-shot `importOnce` (`internal/relevo/syncverb_key.go:44-50`), discards the `ImportResult` except `Applied`, and writes no markers. The steady path (`internal/sync/steady.go:117-158` exchange, `:332-341` record, `:354-362` putTrouble) is the only writer of the `sync.trouble` marker that `db sync status` reads (`cmd/relevo/db_sync_r2.go:173-244` `dbSyncStatusRead`, via `internal/sync/sync.go:163-194` `ReadState`). So a pull can find a hold and neither say so nor leave it for status. Hold sentences come from `Importer` (`internal/synclog/import.go:88-198` result types, `:226-299` import) folded by `troubleFrom` (`internal/sync/steady.go:258-273`), which also folds `Stalled` into `Held`.
- `ErrRemoteConflict` premise is real and self-contradictory in-tree: the doc comment (`internal/sync/enable.go:43-47`) says it "names both remotes", the message (`:47`) names neither. Raised bare in `Preflight` (`internal/sync/join.go:151-153`). CLI mappings add no next step (`cmd/relevo/db_sync.go:557-558`, `cmd/relevo/db_sync_call.go:180-182`).
- `relevo config set sync` exists as the re-point route (`cmd/relevo/config_edit.go:95-154`); enable has no repoint flag, only `--url` (`cmd/relevo/db_sync.go:109-120`).
- Turso-storage premise is suspicious as stated: status measures the replica file's size (`internal/sync/logsize.go:110-132`, `Runner.ReplicaPath` in `internal/sync/runner.go:49-51`), which is a partial-sync copy, against cloud quota. The builder determines the right measure, not the issue text.
- R2 2x candidates, in order of suspicion: (a) the join counts the same worker totals twice through two baselines started at zero — `Enabler.last` (`internal/sync/join.go:333-355,436-442`) and `Runner.lastRead` (`internal/sync/bytes.go:117-145`) both fold cumulative `Stats` (`internal/synclog/transport.go:191-210`) into the same month row; (b) join-time export versus reconcile `RepairBodies` uploading the same bodies (`internal/sync/join.go:461-485`, `internal/synclog/reconcile.go:177-182,184-251`, `internal/synclog/blobmove.go:202-229`); (c) worker-side double-add (`internal/syncworker/worker.go:280-288` adds `s.r2` onto backend bytes; `internal/syncworker/bytes.go:26-46`, `internal/syncworker/turso.go:350-372`). The builder proves which one with a failing test before fixing.
- Staging-file modes look already 0600 on the two production writes (`internal/synclog/blobmove.go:220` stage, `internal/syncworker/blob.go:122` fetch); the staging dir is 0700 with repair (`internal/syncpipe/wiring.go:96-113`). The replica/driver files have no chmod in-tree (created inside the turso driver open, `internal/syncworker/driver_turso.go:57-80`) and no startup repair. Builder verifies by listing real files, including the exact `-wal` / `-changes` / `-info` / `-wal-revert` names against `deleteReplica`'s prefix match (`internal/sync/disable.go:202-233`).

## 1. Behaviour and cases

### A. Pull reports a hold and status shows it with no tick (#1165)
- A `db sync pull` whose import holds any origin prints each hold (origin and reason) plus `next: relevo db sync status`, in both text and `--json` output.
- The hold the pull found is written to the local trouble marker synchronously, so an immediate `db sync status` (text and `--json`) shows it without waiting for a steady tick. Dropped-report accumulation semantics are preserved (prior drops carry forward, capped — share the helper with `putTrouble`, do not duplicate it).
- Cases: newer-schema hold; locked-origin/signature hold (folds into held, per `troubleFrom`); boundary hold; gap report; clean pull (no hold lines, marker cleared per existing write-empty semantics); pull that applied entries and also held another origin (both reported).
- Out of scope: changing the single-`Import` shape of one-shot pull into a drain loop; changing steady-pipeline behaviour.

### B. Byte counters and storage figures tell the truth (#1154 part 1)
- First a finding, then the fix: builder determines from the code whether bodies are uploaded twice (real double put — a cost bug) or counted twice (a counter bug: put counter versus stored bytes), and whether the Turso storage figure reads the wrong measure (replica-file size versus what the cloud bills). The plan does not prejudge; the evidence bar is a test that reproduces the exact 2x on today's code.
- After the finding: if real double upload, fix the upload path (export vs reconcile repair overlap); if double count, fix the counter (baseline/aggregation) so `r2 put` matches stored bytes; fix the Turso storage measure or its labelling so it no longer reports a partial-replica size as cloud usage.
- Cases: join over a bucket with pre-existing bodies; steady export of a new large body; skipped put (store already holds digest) counts nothing; Turso push/pull deltas across a worker restart (reset counters read as `after` alone — existing `bytesDelta`/`counterDelta` rules stay).

### C. Secret-bearing sync files are owner-only (#1154 part 2)
- `relevo-sync.db` and every driver file named after it (`-wal`, `-changes`, `-info`, `-wal-revert` — exact set confirmed by listing) plus `sync-blobs/` staging files are created `0600`; the staging dir stays `0700`.
- Existing files with wider modes are repaired to `0600` on daemon start (before any worker opens the replica). Repair covers the replica set and staging files; dir repair already exists.
- Cases: fresh start creates all modes correctly under a permissive umask; restart tightens pre-existing `0644` files; files that do not exist are not an error; disable's deletion still removes the same set it repaired.

### D. Remote-conflict refusal names both remotes and the way out (#1154 part 3)
- `ErrRemoteConflict`'s message names the `--url` value and the stored `remote_url`.
- Its CLI surfacing carries `next` as the command that re-points the machine: `relevo config set sync ...` in the exact shape the builder verifies against `config set` semantics (section vs key path, `--local` as appropriate), or the existing enable flag if one exists (none does today — do not invent a flag; use the config command).
- Cases: flag-vs-stored mismatch names both; no stored remote with a flag is not a conflict (existing `ErrNoRemote` path unchanged); already-on / no-token ordering in `Preflight` unchanged.

## 2. Seams (hand over locations, not searches)

- Pull verb: `internal/relevo/syncverb_steady.go:18-50`; `internal/relevo/syncverb_key.go:34-50`; classification `internal/relevo/verbclass.go:21-88`.
- Trouble markers: `internal/sync/sync.go:96-111` type, `:222-264` write/clear helpers, `:149-194` read; `internal/sync/steady.go:43-57` result, `:258-273` fold, `:332-371` record; `internal/sync/drain.go:24-48` merge; `internal/sync/attempt.go:31-58` attempt record (untouched, noted only).
- Import report: `internal/synclog/import.go:88-198` types, `:226-299` import; export `internal/synclog/export.go:77-133,138-200`; reconcile `internal/synclog/reconcile.go:177-251`; staging/upload `internal/synclog/blobmove.go:202-256`; `Stats` `internal/synclog/transport.go:191-210`; fakes `internal/synclog/memfake.go:406-459` (tests only).
- Counting: `internal/sync/bytes.go:19-97` counters, `:113-159` folding; join counting `internal/sync/join.go:125-179,242-278,333-382,436-455`; worker `internal/syncworker/blob.go:59-89,196-221`, `worker.go:274-288`, `bytes.go:18-57`, `turso.go:350-372`, `driver_turso.go:52-124`; pipe `internal/syncpipe/supervisor.go:115-126,208-237`, `convert.go:~240-250` (`statsFromWire`), `wiring.go:27-113`.
- Storage measure: `internal/sync/logsize.go:91-148`, `internal/sync/joinguard.go:55-72`, quotas in `internal/sync/settings.go` (Turso/R2 quota accessors).
- Status CLI: `cmd/relevo/db_sync_r2.go:143-276` (status verb, reader, quota line); `cmd/relevo/db_sync.go:480-533` status line/details, `:544-575` classify, `:24-53` usage; one-shot `cmd/relevo/db_sync_call.go:22-73,159-209,228-274`. Size warning: `db_sync.go` is at 591/600 lines — new pull-hold rendering goes in `db_sync_call.go` or a new file, not `db_sync.go`.
- Wire surface: `internal/db/wire` verb/result/code definitions and `client.VerbError` (exact files located by grep; extend the pull result with the held report or reuse the refusal channel — builder picks the smaller change that keeps text and `--json` in agreement).
- Daemon wiring / repair hook candidates: `cmd/relevo/syncverb_hook.go:55-87` (`newVerbRunner`), `internal/syncpipe/wiring.go:27-70` (`OpenSupervisor`).
- Config re-point: `cmd/relevo/config_edit.go:83-154`.
- Existing pure-function status tests to extend, not replace: `cmd/relevo/db_sync_status_test.go` (direct-local pattern `directSyncStatus`/`readSyncStatusDoc`), `cmd/relevo/db_sync_test.go:44-73` line mapping.

## 3. Ordered steps (one line each: deliverable + how it is known good)

1. Branch from `origin/main` `41bf202d`, run `gh issue view 1165 --comments` and `gh issue view 1154 --comments`, and record each premise's true/false against the code — done when the verification note exists and any false premise has halted the round.
2. Pull records what it found: route one-shot pull's `ImportResult` trouble through the shared trouble-write helper (drops carried forward and capped, held/gaps per-run) — done when a new test with a held origin fails on today's code (mutation: skip the trouble write) and passes with the fix.
3. Pull says what it found: extend the pull result over the wire plus the text/`--json` one-shot printer with origin, reason, and `next: relevo db sync status` — done when pure-function tests (no harness, no network) pin the new lines and the mutation (printer without the hold clause) fails them.
4. Status parity without a tick: pin pull-then-status on a direct local handle showing the same hold list the pull printed — done when the new test passes with step 2 and fails with step 2's write removed.
5. Counter/storage finding: reproduce the exact 2x (`r2 put` vs bucket stored bytes) and the replica-size-vs-cloud-size gap in failing tests first — done when the tests fail on today's code and name the cause (double upload vs double count; wrong storage measure).
6. Fix the found cause only (upload path xor counter aggregation; storage measure or labelling) — done when the step-5 tests pass and the inverse mutations (re-add the double fold / swap the measure back) fail them, with `bytesDelta`/`counterDelta` reset rules still pinned by existing tests.
7. Owner-only files: create replica, all driver files, and staging files `0600` and repair existing wider modes on daemon start — done when mode tests (fresh create under permissive umask; pre-chmodded `0644` repaired; absent files fine; disable still deletes the set) fail today and pass with the fix.
8. Conflict refusal: message names both URLs and CLI failure carries the verified re-point command as `next` — done when tests asserting both URLs and the exact `next` command fail on today's strings and pass with the fix (mutation: strip either URL or the hint).
9. Narrowness and style pass: diff touches only the seams above, functions stay <= 70 lines, no non-test file exceeds 600 lines, comments say why only, no issue numbers or history in code or test names — done when `scripts/check-comments.sh`, `scripts/check-filesize.sh`, and `git diff --stat` against this plan's scope are clean.
10. Iterate with `make check-quick PKGS=./internal/<pkg>/...` over each touched package plus its importers while working — done when each step's focused run is green before moving on.
11. Finish in order: save this plan to `docs/plans/2026-10-15-sync-status-defects.md`, run `gofmt -l` over changed files, run the full gate once as `PATH=$(echo "$PATH" | tr ':' '\n' | grep -v '\.shims' | paste -sd:) make check`, commit everything on the branch, then report — done when `git status` is clean and the report names the commit.

## 4. What is deleted (closed list)

1. Nothing. No behaviour, flag, verb, marker, or quota is removed by this plan; the bare "nothing to apply" pull line stays for pulls with no hold.

## 5. What the report must include

- Base commit and branch, and the per-premise verification outcome for both issues (including the storage-measure and double-upload-vs-count findings with the evidence).
- Each fix with its failing-on-today test name and the exact mutation run to prove it (what was broken, which test failed).
- `make check-quick` invocations per touched package and the full-gate command output (or halt reason); `gofmt -l` result; commit hash; `git status` clean confirmation.
- Coverage-baseline note: regenerated with `sh scripts/check-coverage.sh --write` only if code moved between packages, else untouched.
- Anything deliberately left (e.g., drain-loop shape of one-shot pull, new enable repoint flag) under `not_done`, and any halt with the exact step number.
