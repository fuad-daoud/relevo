# Plan: `relevo db relabel` — set the feature label on finished bindings

## Seed vs code (said, not guessed around)

1. **There is no id link between `binding` and `binding_record`.** `binding_record` is keyed by (origin scope, owner, name) with one *live* row per name (`binding_record_name_uidx`, migration 003:34-35; `RecordPut` `internal/db/record.go:255-279`); archived rows free the name. The `binding` row's id is a db-minted ULID (`UpsertBinding`, `internal/db/write.go:179-230`), natural-keyed by (origin, name, created_at), and `binding.created_at` is `formatTime(record.CreatedAt)` (RFC3339 UTC millis, `internal/db/db.go:17-19`; set at ingest, `internal/ingest/ingest_tx.go:120-127,155-176`). So "that binding's record" must be resolved as: origin-scoped `binding_record` rows (live **and** archived) whose `name` equals the binding's `name` **and** whose `record_json` `created_at`, decoded and passed through `formatTime`, equals the binding's `created_at`. The name alone is never the key — names are reused across time, which is why the verb takes ids.
2. **`internal/db` must not import `internal/store`** (the package's own rule, `internal/db/kv.go:18`). The `store.ValidFeature` check therefore lives in the CLI's pure parse phase, before the db is opened; the db layer guarantees "nothing is written" structurally through the read-phase/apply gate, not by re-validating labels.
3. **A record whose `created_at` key is missing or zero cannot be matched** (its binding's `created_at` came from the log-fallback at ingest). Such a binding is relabelled column-only. Stated edge, not solved: those records predate the `created_at` key and their bindings are archived and kv-gated (`mirrorArchived`, `internal/relevo/daemon.go:508-527`), so no re-ingest path can undo the column; the verb reports the situation through `records_rewritten` and the report names the edge.
4. **The issue's "or clears" and "optionally ticket" are out of scope** — decision 1's shape sets labels only. A binding nothing fits stays featureless: it is simply not named in the file. No `--clear`, no ticket rewrite.
5. **`chains.feature` is the chain's own label** (migration 016:39, `NOT NULL DEFAULT ''`), copied onto member bindings at bind time (`internal/relevo/chain_remote.go:65,212,289`, `chain_served.go:125,283,400`, `chain_fork_start.go:63`). It is not a per-binding copy the verb maintains; relabel leaves it untouched and the resulting divergence is the owner's own intent. `round` rows carry no feature (read path joins `binding.feature`).

## Where feature lives, and what the verb does to each

| Holder | Verb |
|---|---|
| `binding.feature` (001_initial.sql:33, index :54) | written: `UPDATE binding SET origin = ?, feature = ? WHERE id = ?`, stamped `origin = t.origin` |
| `feature` key of `binding_record.record_json` (`store.Binding.Feature`, `internal/store/binding.go:377`) | written byte-preserving via `mapObject`; a record with no `feature` key gets one appended; stamped `origin = t.origin`; only when bytes actually change |
| `chains.feature` (016:39) | untouched — the chain's own label, not a binding copy |
| `round` rows | no feature column — nothing to do |
| sealed archive files on disk (`binding.archive_path`) | untouched — nothing re-reads them for binding facts (`ArchivedSource.Bind` reads the store record, `internal/ingest/source.go:195-204`) |
| readers (`queryRounds` `internal/db/read.go:25`, `bindingColumns` :233, filters :70/:343, histq, stats, ui) | untouched — they read `binding.feature` and pick the change up as-is |

## Read path that shows the relabel (named, no changes needed)

`:stats` → `statsView.fetch` (`internal/ui/view_stats.go:195-206`) → `relevo.StatsInputs` (`internal/relevo/statsinputs.go:17-18`) → `db.Query` → `queryRounds` (`internal/db/read.go:25`, selects `binding.feature`) → `db.RoundRow.Feature` → `stats.Build`'s per-repo `Features`/`NoFeature` buckets (`internal/stats/groups.go:27-67`). The same column feeds `history --feature` (`read.go:70,343`). Writing the column is sufficient for every view.

## Behaviour and cases

### A. CLI shape (decision 1), pure parse phase

`relevo db relabel (--file <tsv> | --binding <id> --feature <label>) [--overwrite] [--dry-run] [--json]`, no positionals. Exactly one source: both or neither, `--binding` without `--feature` or vice versa: `usage`. File: one `<id>\t<label>` per line, split on the **first** tab; blank lines and `#`-lines skipped; a line whose two fields are exactly `id`/`feature` is skipped as a header (a ULID is never the literal `id`); trailing `\r` stripped; labels are taken verbatim, never trimmed (they may contain inner spaces; `ValidFeature` already refuses leading/trailing ones). Every refusal **collects every offending line** (1-based, counting skipped lines) into one error: malformed lines, labels failing `store.ValidFeature` (`internal/store/types.go:62-85`), and one id appearing twice with two different labels — all `usage`. One id twice with the same label dedupes silently. An unreadable/empty file (zero entries after skips) is `usage`. This phase runs before `openDB`, so cmd tests pin it with no database, no harness, no network.

### B. db verb: plan, then one apply gate (the rename-repo shape)

`RelabelParams{Entries []RelabelEntry{ID, Feature string; Line int}, Overwrite, DryRun bool}`; `RelabelCounts{dry_run, requested, labelled, unchanged, skipped_labelled, records_rewritten}`. `d.Relabel` wraps `Tx.Relabel` in one `d.Tx` (`internal/db/db.go:373`): a read phase builds the whole change set from SELECTs only; a single apply function performs every write and is called only when not dry-run; dry-run returns the same counts the apply would have produced.

Read phase, per entry (ids already deduped by the CLI):
1. `SELECT name, created_at, feature, origin FROM binding WHERE id = ?` — **no scope filter**: no row → refusal "unknown id"; a row whose `origin` is neither `t.origin` nor `''` → refusal "belongs to origin X" (`''` is legacy-own, the `originScope` rule, `internal/db/record.go:52-58`). Refusals accumulate across the whole batch and are returned joined — one error naming every offending id and line — before any classification; class sentinel in `internal/db`, mapped to `codeConflict` in the CLI (the file-level contradictions are the `usage` ones; the world-level ones are `conflict`, mirroring `ErrNothingToRename`'s mapping).
2. Classify: feature NULL → **labelled**; equal to the requested label → **unchanged** (no write even under `--overwrite`); different → **labelled** under `--overwrite`, else **skipped_labelled**.
3. For each labelled entry, resolve its record rows by the rule in Seed-vs-code 1 and compute the rewritten JSON with a `setRecordFeature` helper: `mapObject` rewrites an existing `feature` key; when the object has no such key, the member is appended onto `mapObject`'s own re-emission (splice before the closing `}`, comma only when the object is non-empty). Rows whose bytes do not change are not updated and not counted.
4. `requested` = distinct ids named; `labelled + unchanged + skipped_labelled == requested`; `records_rewritten` = record rows actually UPDATEd.

Apply phase: per labelled binding one `UPDATE binding SET origin = ?, feature = ? WHERE id = ?`; per changed record one `UPDATE binding_record SET origin = ?, record_json = ? WHERE id = ?`. Every touched row stamped `origin = t.origin` (the codebase's own rule — a row the scope sees and the origin does not is about to become this origin's). Unchanged/skipped rows are never touched, never stamped — running the same file twice changes nothing the second time.

### C. Output (decision 6)

`--json`: the counts document alone (`printDoc`). Text: the same counts; under `--dry-run` additionally one line per binding that **would change** (labelled only): `<id> <name> <old or -> -> <new>`, `-` for NULL.

### D. Owner routing (decision 5)

`isPeekArgs` (`cmd/relevo/machinedb.go:124-144`) exempts `db relabel` in the same arm as `db rename-repo` (:129-133): the verb writes, so it reaches the database through the owner like any other writer; the direct open its `db` siblings take is refused while the daemon holds the lock. Pinned by a `{"db relabel", …, routeOwner, verbDialBudget, ownerStartWait}` row in `TestRouteStartWaitExemptions` (`cmd/relevo/machinedb_wait_test.go:403-435`). The verb opens `machineDBPath()` through `openDB` (`cmd/relevo/wire.go:256`), calls `d.Relabel`, and maps errors through `outcomeError`; refusals use `fail(codeUsage|codeConflict, …)`.

## How sync sees the relabel

Every statement runs inside one `db.Tx` on the owner's pool; migration 022's AFTER triggers append `(tbl, pk, op, origin)` to `sync_outbox` for every `binding` and `binding_record` write — no writer cooperation needed. Unlike rename-repo there is **no ordering hazard**: no delete, no FK between the two tables, so write order inside the Tx is free; the plan still keeps binding updates before record updates for readability. Stamping guarantees every entry carries this machine's origin (never `''`/NULL), so the peer's `applyEntry` (`internal/synclog/import.go`) upserts under the resolved owner and the `owns`/`ownsAfterWrite` gates pass; `DrainOutbox` reads row state at drain time. `link_origin`/`link_id` columns are untouched. Peer replay is correct with no new sync code.

## Re-ingest after the relabel

Live/DONE bindings: the record_json rewrite changes the revision digest, so the next daemon tick re-ingests once (`ingestLiveBindings`, `internal/relevo/daemon.go:681-693`) and `UpsertBinding` writes `feature` from the **rewritten** record — self-healing, the same argument rename-repo made. Archived: kv-gated (`ingested.archive.<recordID>`); even a lost mark re-ingests the rewritten record. Stated hazard, not guarded (the decisions close the refusal list): relabelling a binding whose round is still running can be undone by the daemon's next save of its in-memory record copy; the backfill targets finished bindings.

## Seams

- `internal/db/record_json.go` (new) — `mapObject` (moved from `repo_rename.go:390-421`) and `encodeJSONString` (:379-385), verbatim move, no behaviour change; `rewriteRecordJSON` stays in `repo_rename.go`.
- `internal/db/relabel.go` (new) — `RelabelEntry`/`RelabelParams`/`RelabelCounts`, the refusal sentinel, `DB.Relabel`, `Tx.Relabel`, read-phase/apply split, the record resolver, `setRecordFeature`. ≤ 600 lines, functions ≤ 70.
- `internal/db/relabel_test.go` (new) — reuses `directOpen`/`newTestBinding`/`upsertBinding` (`helpers_test.go:33,119,330`), `execRaw`/`dumpDB` (`repo_rename_test.go:55,346`), `RecordPut` (`record.go:454`) to seed records whose JSON `created_at` matches the binding.
- `internal/ingest` — one re-ingest-survival test beside the existing fixtures.
- `cmd/relevo/db_relabel.go` (new) — flags (`dbRelabelOwnFlags` declares `--file/--binding/--feature/--overwrite`; `dbRelabelFlagSet` adds `--dry-run` and `--json` — the shared `dbFlagSet` must not re-declare `--dry-run`, which `dbRenameRepoOwnFlags` already gives it, nor `--json`, which `dbQueryFlagSet` gives it), pure parse, `openDB(machineDBPath())`, counts print.
- `cmd/relevo/db_query.go` — `dbUsage` (:24-40) gains a relabel line and a short paragraph; `dbFlagSet` (:72-75) calls `dbRelabelOwnFlags`; `cmdDB` (:97-108) gains the `relabel` case (the dispatcher walk in `registry_test.go:33` reads it from source and requires the registry entry).
- `cmd/relevo/machinedb.go` — exemption at :129-133 extended to `relabel`.
- `cmd/relevo/machinedb_wait_test.go` — new row in `TestRouteStartWaitExemptions`.
- `cmd/relevo/registry.go` — `verbFlagSets["db relabel"] = installer(dbRelabelFlagSet)` beside :66-68.
- `cmd/relevo/registry_rows.go` — new `db relabel` entry sorted between `db query` (:302-310) and `db rename-repo` (:311-319): Flags `--binding, --dry-run, --feature, --file, --json, --overwrite`; Output `json:{dry_run,requested,labelled,unchanged,skipped_labelled,records_rewritten}`; Exit `{0,1,2}`; Errors `conflict, internal, refused, usage`. The `db` row (:294-301) gains relabel in Summary/Args and `--binding, --feature, --file, --overwrite` in its sorted Flags.
- `cmd/relevo/testdata/contract/help-json.golden` — regenerated.
- `cmd/relevo/db_relabel_test.go` (new).
- Reused as-is: `originScope`, `mapBusy`, `Tx.exec`/`Tx.queryRow` (`db.go:538,542`), `printDoc` (`readjson.go:22`), `outcomeError`, `fail`/`failWrap` (`clierror.go:130,144`), `parseFlags` (`args.go:172`), `store.ValidFeature`.

## Ordered steps

1. Move `mapObject`/`encodeJSONString` into `internal/db/record_json.go`. Know it worked: `go test ./internal/db -run TestRenameRepo` green with every existing test unbent.
2. `internal/db/relabel.go` with the read-phase/apply split, classification, record resolver, `setRecordFeature`, counts, refusals. Know it worked: `go test ./internal/db -run TestRelabel` green for the db tests below.
3. Dry-run and stamping pins: `TestRelabelDryRunWritesNothing` (dump every table **and** `sync_outbox` before and after; identical) and `TestRelabelStampsOutboxEntries` (after an apply, the binding and binding_record entries carry the test origin, none `''`/NULL). Know it worked: both green; the dry-run test fails if any write moves above the gate.
4. Re-ingest pin in `internal/ingest`: `TestReingestAfterRelabelKeepsNewLabel`. Know it worked: `go test ./internal/ingest -run Reingest` green.
5. CLI: `cmd/relevo/db_relabel.go`, dispatcher case, `dbUsage` text, `isPeekArgs` exemption, `TestRouteStartWaitExemptions` row, `TestDBRelabelUsage`, `TestDBRelabelFileParse`. cmd/relevo tests must not spawn a harness, reach the network, or execute against a live db: every refusal fires before `openDB`, flags/parse only. Know it worked: `go test ./cmd/relevo -run "Relabel|RouteStartWait"` green.
6. Registry rows + golden. Know it worked: `go test ./cmd/relevo -run "Contract|HelpJSON" -update` then `go test ./cmd/relevo -run "Contract|HelpJSON|Registry"` green with the regenerated golden.
7. Name guard, non-vacuous: `git add` the round's files first (`git grep` only scans the index, so an unstaged new file is invisible to it), confirm each new path with `git ls-files --error-unmatch`, then run `sh scripts/check-name.sh`. Know it worked: exit 0 with the new files provably in the index.
8. Iterate during the round on `go test ./internal/db ./internal/ingest ./cmd/relevo`, fixing every reported error before the next run; run `make check` once at the end. Coverage: new code is new-tested; regenerate `testdata/coverage-baseline.txt` (`sh scripts/check-coverage.sh --write`) only if a package moves more than a point, and say so in the report — never lower a baseline.
9. Save this plan verbatim at `docs/plans/2026-10-09-relabel-bindings.md` and include it in the round's commit. All work lands as **new commits** — no amend, no rebase of anything already on a remote branch.

## Deleted behaviour

1. None. The `mapObject`/`encodeJSONString` move is intra-package relocation with no behaviour change; every existing pin stays; nothing else is removed.

## Tests named for what they pin

`TestRelabelFillsNullFeature` (column + record + origin stamp) · `TestRelabelSkipsLabelledBinding` (different label, no `--overwrite`: skipped, row bytes unchanged) · `TestRelabelOverwriteReplacesLabel` · `TestRelabelUnchangedWritesNothing` (same label, and the whole run twice: identical dump, unchanged counts) · `TestRelabelRefusesUnknownIDWritesNothing` (batch of two valid ids plus one unknown: refusal names it, **nothing** written to the valid ones either — the atomicity pin) · `TestRelabelRefusesOtherOriginID` · `TestRelabelRewritesRecordJSONBytePreserving` (large integer above 2^53 and an unknown key survive verbatim; only `feature` changes) · `TestRelabelAppendsMissingFeatureKey` · `TestRelabelRewritesOnlyTheNamedBindingsRecord` (name reuse: relabelling the archived binding's id rewrites its archived record and leaves the new binding's live record alone) · `TestRelabelDryRunWritesNothing` · `TestRelabelStampsOutboxEntries` · `TestReingestAfterRelabelKeepsNewLabel` · `TestDBRelabelUsage` (flag shapes, positionals, unknown flag) · `TestDBRelabelFileParse` (malformed lines, invalid labels, duplicate-id conflict — every offending line named; pure, no db: the label rule lives here because `internal/db` cannot import `internal/store`, and cmd tests cannot touch a live db) · `TestRouteStartWaitExemptions` (gains the `db relabel` row). No issue numbers in code or test names; comments say why; test names say what they pin.

## Mutations for the MasterMind

1. **NULL-only fill**: in the read-phase classification, route a differently-labelled binding to labelled regardless of `Overwrite` (drop the skip branch). `TestRelabelSkipsLabelledBinding` must fail; everything else stays green.
2. **Batch atomicity**: on an unknown id, skip that entry and continue instead of refusing the batch. `TestRelabelRefusesUnknownIDWritesNothing` must fail.
3. **Routing exemption**: remove `relabel` from the `isPeekArgs` exemption arm. `TestRouteStartWaitExemptions`'s `db relabel` case must fail.
4. Optional: re-encode the record through `map[string]any` instead of `mapObject`. `TestRelabelRewritesRecordJSONBytePreserving` must fail.

## Report must include

Changed paths vs this plan's declared scope (`git diff --stat`); the focused and full commands run with results; the named tests added; confirmation the dry-run test diffs every table including `sync_outbox`; the seed-vs-code findings above (no id link binding↔binding_record and the (name, created_at) match rule; `internal/db`/`internal/store` separation putting the label check in the CLI; `chains.feature` left alone; no clear/ticket per the closed decisions); the unmatched-record edge (zero/missing `created_at` → column-only, why that is stable) and the running-binding hazard, both stated not solved; that `sh scripts/check-name.sh` ran with the new files staged and scanned; whether the coverage baseline was regenerated and why; the mutation results if run.
