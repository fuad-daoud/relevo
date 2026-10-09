# Plan: repo rename follow + `relevo db rename-repo` repair verb

## Seed vs code (said, not guessed around)

1. `UpsertRepo` is in `internal/db/repo_write.go` (moved there by this branch), not `internal/db/write.go`. `Tx.UpsertRepo` at :44, `upsertRepoBy` at :84-101, `fillRepo` at :141-159, `findRepoBy` at :110-122.
2. "Consent rows" do not exist: repo consent is two columns on `repo` (`mastermind_consent`, `consent_at`, migration 010). `session_consent` (011) is keyed by session, not repo. The merge therefore *carries consent columns*, not rows.
3. The issue's "keep both common_dirs reachable" is impossible in the schema: one `common_dir` per row, and `repo_origin_origin_url_uidx` (migration 014) forbids two rows sharing the new URL. The merged row keeps **one** common_dir — the `--to` row's own (decision below).
4. The only foreign key into `repo` is `binding.repo_id` — pinned against the schema by `backfillTwinReferences` (`internal/db/origin_repoint.go:51-63`). Everything else repo- or ticket-shaped is *data*, not a reference: `binding.ticket` (012), `chains.ticket` (016:40), and `binding_record.record_json` (`store.Binding.Ticket` :379-381, `RepoRef{origin_url,common_dir}` :62-67 in `internal/store/binding.go`).
5. Stored tickets are never URLs: `store.ParseTicket` (`internal/store/types.go:119`) normalises `.../issues/N` input to `owner/repo#N` at entry. The rewrite only ever sees `#N` and `owner/repo#N`.

## Behaviour and cases

### A. Rename follow in `UpsertRepo` (decision 1)

In `upsertRepoBy` (`internal/db/repo_write.go:84-101`), the `case byDir != ""` branch today calls `fillRepo`, which fills `origin_url` only when the column is NULL. Add: when the byDir row's `origin_url` is non-null and differs from the record's, **update that row's `origin_url` to the record's** (and stamp `origin = t.origin`, as `fillRepo` does) instead of leaving it — the remote was renamed. `byURL == ""` is the guarantee the new URL is free in this origin's scope (`originScope = origin IN (?, '')`, `internal/db/record.go:58`), so the partial unique index cannot refuse. When the record's URL is held by another row, nothing changes: the `byURL` case already wins (`TestUpsertRepoPrefersTheRowHoldingTheURL` pins it). No existing test pins the dir-hit-with-different-non-null-URL shape, so nothing must be bent. Keep functions ≤ 70 lines: a small helper beside `fillRepo`.

### B. `relevo db rename-repo --from <url> --to <url> [--adopt-cwd <prefix>]... [--dry-run] [--json]`

Normalise both URLs with `git.NormalizeOriginURL` (`internal/git/repo.go:85`); derive owner/repo paths with `git.OwnerRepo` (:147). `--from == --to` after normalisation, or a missing/empty flag: `usage`. Resolve both rows with `findRepoBy("origin_url", …)` inside this handle's origin scope.

Structure — **plan, then one apply gate** (the shape that makes `--dry-run` writes impossible, unlike the old `unbind --dry-run` bug): a read phase builds the whole change set (row ids, per-statement counts, decoded `record_json` rewrites) from SELECTs only; a single apply function performs every write and is called only when not dry-run. Dry-run returns the same counts the apply would have produced. The whole repair is one `d.Tx`.

Cases:
1. **Merge** (`--to` row exists): re-point every `binding.repo_id = fromID` to `toID` (one UPDATE, counted); carry consent — if the `--to` row's `mastermind_consent` is NULL and the `--from` row answered, copy `mastermind_consent`/`consent_at`; then delete the `--from` row. `foreign_keys=ON` (`internal/db/engine_modernc.go:42`), so the delete must be the **last** statement, after every re-point.
2. **Rename only** (`--to` row absent): `UPDATE repo SET origin_url = --to` on the `--from` row.
3. **No-op** (`--from` row absent, `--to` row present): zero counts, exit 0 — this is what makes a second run idempotent. Neither row present: `conflict` ("nothing to rename"), exit 1 — a likely typo, not a repaired state.
4. **Ticket rewrite** (both cases): in this origin's scope (`origin IN (?, '')`), rewrite `owner/old#N` → `owner/new#N` in `binding.ticket` and `chains.ticket`. Match by `substr(ticket, 1, length(old)+1) = old || '#'` — **not** LIKE: `_` is a valid owner/repo char (`validOwnerRepo`) and a LIKE wildcard. `#N` shorthand rows are relative to the binding's repo and are left untouched. If `git.OwnerRepo` returns "" for either URL, the ticket pass rewrites nothing (count 0).
5. **`record_json` rewrite**: for every origin-scoped `binding_record` row, decode `record_json` (plain JSON — the 013 codec covers only `round_file.body` and `transcript` columns, not this one) as `map[string]any`; rewrite `ticket` when it carries the `owner/old#` prefix, and `repo_ref.origin_url` when it equals the `--from` URL; re-encode and UPDATE by id (the UPDATE shape already exists at `internal/db/record.go:268`). Leave `repo_ref.common_dir` alone: URL resolution wins, and the dir stays free.
6. **`--adopt-cwd P`** (repeatable): bindings with `repo_id IS NULL` whose `cwd` matches P get `repo_id = toID`. **Prefix rule: whole path components** — match iff `cwd == P` or `cwd` starts with `P + "/"`, P cleaned with `filepath.Clean` and stripped of trailing separators. So `/home/fuad/projects/relay` matches `/home/fuad/projects/relay` and `/home/fuad/projects/relay/x`, and does **not** match `/home/fuad/projects/relay-plugin-lock`; the operator passes each prefix (`…/relay`, each `…/relay-*` sibling wanted, `/home/fuad/.local/state/relay/.worktrees`). Implement as a SELECT of origin-scoped repo-less (id, cwd) rows, match in Go through the pure matcher, UPDATE by id — no LIKE on paths.
7. **Origin stamping**: every repo/binding/chains/binding_record row the repair updates is stamped `origin = t.origin` in the same UPDATE (the codebase's own rule — `fillRepo`: a row the scope sees and the origin does not is about to become this origin's). The `--from` row is stamped **before** its delete so the delete trigger's `OLD.origin` carries this machine's origin rather than a legacy `''`.
8. Rows of other origins are untouched: id equality plus the `originScope` WHERE on every read; a binding of another origin cannot hold this machine's repo ULID.
9. Output: counts document `{dry_run, from, to, repos_merged, repos_renamed, bindings_repointed, tickets_rewritten, bindings_adopted, records_rewritten}`; `tickets_rewritten` sums binding + chains + record_json ticket rewrites; `records_rewritten` counts record_json rows touched (ticket or repo_ref). Text mode prints the same counts.

### C. Reach the db like every other write verb (decision 3)

`config set` (`cmd/relevo/config_edit.go:90-142`) goes `newRuntime()` → `openDB` (`cmd/relevo/wire.go:256`) → owner route when installed (`openDBRoute`, `cmd/relevo/machinedb.go:180`) → writes run through the owner's pool, where the 022 triggers fire ("client-socket writes included", `internal/db/db.go:380-384`). The new verb does the same: `cmd/relevo/db_rename_repo.go` opens `machineDBPath()` through `openDB`, calls `d.RenameRepo(...)`, prints via `printDoc` under `--json`, and maps errors through `outcomeError`; state refusals use `fail(codeUsage|codeConflict, …)` (`cmd/relevo/clierror.go:120`). The daemon may be running; no flock dance (that is `db query`'s ad-hoc read path).

## Inventory: every repo/ticket holder and what the repair does

| Holder | Repair |
|---|---|
| `repo` row (`--from`) | merged (delete, last) or renamed (`origin_url = --to`) |
| `repo.mastermind_consent`/`consent_at` | carried to `--to` row when its consent is unset |
| `binding.repo_id` (only FK into repo) | re-pointed fromID → toID |
| `binding.ticket` | `owner/old#N` → `owner/new#N`; `#N` untouched |
| `chains.ticket` | same rewrite, origin-scoped (`chains.repo` is a checkout *path*, untouched) |
| `binding_record.record_json` | `ticket` rewritten; `repo_ref.origin_url` `--from`→`--to`; `common_dir` untouched |
| `binding.repo_id IS NULL` + cwd under P | adopted: set to toID |
| `session_consent`, `mastermind`, `round`, `event`, `artifact`, `transcript`, `chain_member`/`chain_event`/`chain_check`, `binding_record.link_*`, `installation`, kv | no repo/ticket reference — untouched |

## How #1065's sync log sees the repair

Every statement runs inside one `db.Tx` on the owner's pool, and migration 022's AFTER triggers append `(tbl, pk, op, origin)` to `sync_outbox` for every shared-table write — no writer cooperation needed. The repair produces: `binding`/`chains`/`binding_record` updates, one `repo` update (rename) or one `repo` update (stamp) + `repo` **delete** (merge). `DrainOutbox` (`internal/db/sync_exchange.go`) reads entries in seq order with row state, so the peer applies binding updates **before** the repo delete — the order the FK needs; write order inside the Tx (re-points first, delete last) is therefore load-bearing and gets a test. `pendingParents` pulls the surviving repo row into an early batch if a child needs it. On the peer, `applyEntry` (`internal/synclog/import.go:383-419`) upserts rows under the resolved owner and `ExchangeDelete`s the merged row idempotently; the `owns`/`ownsAfterWrite` gates pass because the entries claim this machine's origin (guaranteed by the stamping rule) and the peer's copies of these rows already resolve to this installation. A batch cut between updates and the delete still replays in order across two batches. The reconcile pass covers anything predating the outbox. Peer replay is therefore correct with no new sync code.

## Decision 5: re-ingest after the repair

- **Archived records**: `mirrorArchived` (`internal/relevo/daemon.go`, ~:514-545) ingests each archived record once, gated by kv key `ingested.archive.<recordID>`; and `ArchivedSource.Bind()` (`internal/ingest/source.go:183-198`) reads the **store record** (`binding_record.record_json`), not the sealed archive file. So archived bindings are normally never re-ingested after the repair, and even a lost kv mark re-ingests the *rewritten* record_json — repaired values. The sealed archive files on disk keep the old strings; nothing re-reads them for binding facts.
- **Live/DONE bindings**: `ingestLiveBindings` re-ingests on store-revision change, and `UpsertBinding` (`internal/db/write.go:179`) overwrites `repo_id`/`ticket` from the record. The repair's record_json rewrite changes the revision digest (`internal/store/revision.go:29`), so the next daemon tick re-ingests each touched live binding exactly once and writes the **repaired** ticket and the repo resolved from the rewritten `repo_ref.origin_url` — a URL hit on the merged row. The repair is self-healing, not fragile.
- **Adopted repo-less bindings**: their records carry no RepoRef, so a later revision bump (a rebind, say) re-ingests `repo_id` NULL. Accepted edge, stated in the report: a rebind re-resolves the repo from live git facts and lands on the merged row anyway; archived ones are kv-gated.

## Decision 4 sanity: the real laptop data

Merged row keeps the `--to` row's `common_dir` (`~/projects/static/relevo/.git`). Why safe: resolution prefers `origin_url` (`upsertRepoBy`), and both clones' remotes resolve to the `--to` URL — the issue records that later binds from the main checkout matched the relevo row *by URL*. The main checkout's dir (`~/projects/relevo/.git`) is held by no row after the merge, so nothing collides, and rewritten record refs hit by URL. Rename-follow cannot undo the merge: no row holds the old URL any more. Residual edge (stated, not solved): a bind from a checkout whose remote still says the *old* URL and whose dir no row holds inserts a fresh old-URL row; the recourse is re-running the verb — the real data (main checkout resolving to the new URL since 2026-10-04) has no such checkout. Expected counts on the laptop: 689 bindings re-pointed (relay row → relevo row), 339 tickets rewritten across binding/chains/records, ~84 adopted across three prefixes, 1 repo merged. Sanity-check only; nothing hardcodes these.

## Seams

- `internal/db/repo_write.go` — rename follow in `upsertRepoBy` :84-101 (+ helper near `fillRepo` :141-159).
- `internal/db/repo_rename.go` (new) — `RenameRepoParams`/`RenameRepoCounts`, `func (t *Tx) RenameRepo`, `func (d *DB) RenameRepo`, read-phase/apply split, the pure whole-component cwd matcher, record_json surgical rewrite. Split into a second file before any file passes 600 lines; functions ≤ 70.
- `internal/db/repo_rename_test.go` (+ `repo_twin_test.go` for the follow tests) — the tests below; `dbtest` harness as in `write_test.go`.
- `cmd/relevo/db_rename_repo.go` (new) — flags, normalisation, `openDB(machineDBPath())`, output doc; dispatcher case in `cmdDB` (`db_query.go:89-101`) and a `dbUsage` line (:24-35).
- `cmd/relevo/registry.go` — `verbFlagSets["db rename-repo"]`; `cmd/relevo/registry_rows.go` — new verb entry (Errors: `conflict, internal, refused, usage`; Output: `json:{dry_run,from,to,repos_merged,repos_renamed,bindings_repointed,tickets_rewritten,bindings_adopted,records_rewritten}`) and fix the stale `"db"` row (:296-302: Summary/Args still say read-only query-only) to name query|sync|rename-repo.
- `cmd/relevo/testdata/contract/help-json.golden` — regenerate; asserted at `contract_test.go:921,936`.
- `internal/ingest` — one re-ingest-survival test.
- Reused as-is: `git.NormalizeOriginURL`, `git.OwnerRepo`, `findRepoBy`, `originScope`, `isUniqueViolation`, `printDoc`, `outcomeError`, `fail`.

## Ordered steps

1. Rename follow in `internal/db/repo_write.go` + tests `TestUpsertRepoFollowsRemoteRename` (dir hit, different non-null URL → row renamed, one row) and `TestUpsertRepoURLHitWinsOverRenamedDirRow` (record URL held by another row → no merge, no rename of the dir row). Know it worked: `go test ./internal/db -run TestUpsertRepo` green, including the eight existing twin tests unbent.
2. `internal/db/repo_rename.go` with the read-phase/apply split, all writes and counts. Know it worked: `go test ./internal/db -run TestRenameRepo` green for the merge/rename/consent/ticket/record/adopt/idempotence/other-origin tests below.
3. Sync-order + dry-run pins: `TestRenameRepoLogsRepairInSeqOrder` (outbox holds binding/chains/record updates before the repo delete; every entry stamped this origin, none `''`/NULL) and `TestRenameRepoDryRunWritesNothing` (dump every table's rows **and** `sync_outbox` before and after a dry-run; identical). Know it worked: both green; the dry-run test fails if any write statement is moved above the gate.
4. Re-ingest pin in `internal/ingest`: `TestReingestAfterRepairKeepsRepairedValues` — repair a seeded record, re-run `Ingest` over it, binding keeps the repaired ticket and repo_id. Know it worked: `go test ./internal/ingest -run Reingest` green.
5. CLI: `cmd/relevo/db_rename_repo.go`, dispatcher, usage text; `TestDBRenameRepoUsage` (missing/equal URLs, unknown flag → usage). cmd/relevo tests must not spawn a harness or reach the network (CLAUDE.md): flags and refusals only, no subcommand execution against a live db; TestMain isolation already covers env. Know it worked: `go test ./cmd/relevo -run RenameRepo` green.
6. Registry rows + golden. Know it worked: `go test ./cmd/relevo -run Contract` green with the regenerated `help-json.golden`, and `relevo help --json` lists the verb with its error codes.
7. Full check: `make check`. Iterate during the round on `go test ./internal/db ./internal/ingest ./cmd/relevo`, fixing every reported error before the next run; run `make check` once at the end. Coverage: new code is new-tested; regenerate `testdata/coverage-baseline.txt` (`sh scripts/check-coverage.sh --write`) only if a package moves more than a point, and say so in the report — never lower a baseline.
8. Save this plan verbatim at `docs/plans/2026-10-09-repo-rename-repair.md` and include it in the round's commit. All work lands as **new commits** on the branch after #1065 merges — no amend, no rebase of anything already on the remote.

## Deleted behaviour

1. None. This round deletes no behaviour: the only behavioural change is `UpsertRepo`'s dir-hit branch, which gains the rename follow; every existing pin stays.

## Tests named for what they pin

`TestUpsertRepoFollowsRemoteRename` · `TestUpsertRepoURLHitWinsOverRenamedDirRow` (two rows → no auto merge) · `TestRenameRepoMergesAndRepointsBindings` · `TestRenameRepoRenamesWhenTargetRowAbsent` · `TestRenameRepoRewritesTickets` (binding + chains) · `TestRenameRepoLeavesShorthandTickets` (`#N` untouched; URLs are never stored — `ParseTicket` pins that upstream) · `TestRenameRepoRewritesRecordJSON` (ticket + `repo_ref.origin_url`, `common_dir` untouched) · `TestRenameRepoCarriesConsentWhenTargetUnset` · `TestAdoptCwdPrefixMatchesWholeComponents` (pure matcher: relay vs relay-plugin-lock) · `TestRenameRepoAdoptsRepolessBindingsUnderPrefix` · `TestRenameRepoDryRunWritesNothing` · `TestRenameRepoIsIdempotent` (second run: zero counts, no diff) · `TestRenameRepoLeavesOtherOriginRows` · `TestRenameRepoLogsRepairInSeqOrder` · `TestReingestAfterRepairKeepsRepairedValues` · `TestDBRenameRepoUsage`. No issue numbers in code or test names; comments say why; test names say what they pin.

## Mutation for the MasterMind

Break the rename follow: in `upsertRepoBy`'s `case byDir != ""` branch, route back through plain `fillRepo` (drop the non-null-URL overwrite). `TestUpsertRepoFollowsRemoteRename` must fail; everything else stays green. Second, independent mutation: change the adopt matcher to bare `strings.HasPrefix(cwd, prefix)` — `TestAdoptCwdPrefixMatchesWholeComponents` must fail.

## Report must include

Changed paths vs this plan's declared scope (`git diff --stat`); the focused and full commands run with results; the named tests added; confirmation the dry-run test diffs the whole db including `sync_outbox`; the seed discrepancies listed above (repo_write.go location, consent columns, one common_dir) and the decisions taken (keep `--to`'s common_dir; whole-component prefix rule; no-op-when-target-exists idempotence rule; consent carry rule); the re-ingest finding (kv gate + revision-driven self-heal, adopted-bindings edge); whether the coverage baseline was regenerated and why; the mutation result if run.

## MasterMind amendment (binding on the builder)

- **record_json fidelity.** Do not round-trip `record_json` through `map[string]any` with a default decoder: it turns every number into float64 and silently corrupts integers above 2^53 and the formatting of the rest. Decode with `json.Decoder.UseNumber()` (keeping unknown keys) or into the typed `store.Binding` the store itself encodes, whichever preserves every field the record carries; add `TestRenameRepoRecordJSONRoundTripKeepsOtherFields` (a record with a large integer and an unknown key comes back identical except the rewritten `ticket` / `repo_ref.origin_url`).
- **Base.** Bind with `--base origin/main` only after PR #1065 has merged; if main has moved under this plan's line numbers, re-locate the seams by name, not by line.
