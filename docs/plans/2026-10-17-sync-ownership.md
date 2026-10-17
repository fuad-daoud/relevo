# Sync step 6: ownership and hand-over — plan

Lands at `docs/plans/2026-10-17-sync-ownership.md` (round 4 saves it). Spec:
`docs/specs/2026-10-10-sync-everything-design.md` §1 D4, §2, §3, §12 item 6, §13.
Base: main af52df35 (v0.18.2), schema 30, `ProtocolVersion` 3.

Check commands. Focused, per round: `GOTOOLCHAIN=go1.27.2 go test ./internal/db/ ./internal/synclog/ ./internal/sync/ ./internal/syncworker/ ./internal/relevo/ ./internal/store/ -run '<names the round adds>'`, then the whole package set it touched. Full, once at the end of each round: `RELEVO_REQUIRE_COVERAGE=1 GOTOOLCHAIN=go1.27.2 make check`. No new exclusions in `.golangci.yml`, `check-comments.sh` or `check-filesize.sh`.

# MasterMind corrections (binding; they override the plan text where they differ)

C1. **The exporter's writer re-check is for takeovers only.** §B "Exporter" says `owned()` re-checks the current writer for the two root families. Applied to a hand-over that contradicts §0.5: the hand-over's own root rows, and every child row the old writer queued before the hand-over transaction and has not exported yet, are attributed to the old writer while the row's writer is already the target, so the re-check would drop the hand-over itself and lose the old writer's last writes. The re-check moves an outbox row to `sync_fenced_change` only when the root carries a takeover fence against this installation (`sync_binding_left.kind = takeover`, or the term this machine's import of a takeover recorded). A hand-over exports everything queued up to and including its own transaction. Test: a hand-over with unexported children written just before it; every importer receives the children and the flip, in that order, and nothing lands in `sync_fenced_change`.

C2. **An import never queues an export of this installation, in any shape.** Echo suppression today is the attribution filter alone (`owned()`, `synclog/export.go:234`). With update and delete triggers on `OLD.writer`, a returning old writer that applies a takeover runs `UPDATE ... SET writer = <taker>` and the trigger attributes that row to itself. Exported, it would be an entry in the old writer's log, above N, whose body hands the binding to the taker: an importer that has not yet applied the takeover reads it as a hand-over (rule 5) and records a term with `upto` above N, which widens the fence over the very changes the takeover meant to refuse. Replace the plan's narrow echo guard ("post-write writer is this installation and e.Origin is not") with: every outbox row an import statement queues that is attributed to this installation is discarded in the same transaction (`DiscardOutboxSince`), whatever the op. A takeover's flip on the returning machine is therefore not counted as a fenced change either. Tests: the returning machine applies the takeover and exports nothing for the binding; a hand-over landing on the target (first sight and existing row) queues nothing for the target. Mutation: restrict the discard to inserts.

C3. **Hand-over refusals, complete list** (pure `HandoverRefusal`, table-tested; each with its sentence):
- sync not enabled, or the machine locked (hand-overs are signed only, decision 7);
- a round open, or the binding a member of a running chain (spec);
- a dirty worktree, or commits the push cannot carry: uncommitted changes do not travel, and releasing the tree would strand them;
- a server binding (§C, accepted);
- `--to` naming this machine, an installation the remote does not know, or a label that matches more than one installation. Labels are not unique (the laptop and zen report the same hostname): `--to` takes an installation id or a label, and an ambiguous label is refused with the ids listed;
- the target's newest entry written below schema 31 (moved here from round 4's runbook line, so the refusal exists from the round that adds the verb);
- already handed over to another machine (the plan's rerun case with the same `--to` stays idempotent).
Also name every local writer of a binding's tree that can run after the hand-over transaction without going through `store.Load`/`send` — round sealing and transcript ingest, gc and age prune, relabel, repo rename, the board — and for each say whether it is refused on a handed-over binding or cannot reach one. A write that slips through is attributed to the target by the triggers and silently dropped by this machine's exporter, so it must be refused, not lost.

C4. **`enable --as` never appends or marks under a temporary identity.** §F's mechanics join under the machine's own fresh id and adopt afterwards. A join exports (the installation row, shared docs, marks, the machine's own head), so that leaves a ghost installation with its own log on the remote, and the plan's own refusal ("no rows with a foreign-to-X origin") contradicts the temporary identity's rows. Required property: from `enable --as` on, nothing reaches the remote under any id but X, and nothing reaches it under X until X's log has been imported in full and the own head set to X's remote head. Preconditions: the outbox and `shared_outbox` empty, and no shared rows but this machine's own `installation` row, which is replaced by X's. Choose the mechanism (adopt first and import X's log as own for this join only, or join with export and mark publishing off until the swap) and justify it from the code; a memfake test asserts the remote holds no entry, mark or meta row under any id other than X after the adoption.

C5. **The identity claim is detected, not only compared-and-set.** 5b found that an insert-if-absent on a worker's replica followed by a last-writer-wins push lets two machines both win (the rekey double win). `ClaimIdentity` as written ("insert only if absent and read back") has the same hole. Do what 5b did: the claim row holds the claimer's random token; after the push the adopter pulls and re-reads; and every steady attempt of a machine whose identity came from `enable --as` compares `claim:<X>` with its own token. A mismatch stops export and import, `db sync status` says another machine adopted this identity and to run `relevo db sync disable` on one of them, and `--json` shows it. Test: the memfake double win (B's claim written over A's after A read back its own) is detected on A's next attempt.

C6. **The remote log's new columns go through the format upgrade path.** Use the existing `upgrades` map in `syncworker/format.go` (the way `addMACStatement` arrived): `logFormatVersion` 4, columns `after_origin TEXT, after_seq INTEGER`. Not an ad-hoc ALTER on every open: two workers opening at once would both ALTER, and the upgrade path is the one that is already tested. Say what a v0.18.2 worker does on a format-4 remote (it should refuse with the "upgrade" status the mixed-version test showed for schema), and tolerate a concurrent upgrade by another machine (a duplicate-column error is not a failure). One protocol bump: `ProtocolVersion` 4 carries `After`, `ClaimIdentity` and the takeover entry kind, because nothing is released between rounds.

C7. **The old unique index.** `binding_record_origin_owner_name_uidx` (`(origin, owner, name) WHERE archived_at IS NULL`, migration 014) stays on the creator. Say what it does after a hand-over: on the old machine (can it create a new binding with the handed-over name while the target holds it live?), on a third machine importing both. Either keep it with that behaviour stated and tested, or drop it in 031 in favour of the writer index, and say which and why.

C8. **The resumed binding's owner scope.** `bind --resume` on the target sets `MasterMind`/`MasterMindID` to the caller's. State what that does to `binding_record.owner` (the column the unique indexes scope by) and to the other machines' copies, and test that the resumed binding is visible to the target's MasterMind in `relevo status` and not left under the creator's.

C9. **No-change rule for the old writer's log after the hand-over.** The plan accepts as noise a delete the old writer's reconcile emits for a row the new writer deleted (§D). Make the old writer's reconcile skip every row whose root it handed over or lost to a takeover (`sync_binding_left`), so its log never carries an entry for a binding it no longer writes. Test: after the new writer deletes the binding, the old writer's next reconcile appends nothing for it.

## 0. Where the seed and the code disagree (read first)

1. **`relevo done` does not push the branch.** `Done` (`internal/relevo/done.go:38-215`) stops the process, confirms entries, and calls `worktreeTeardown`; nothing pushes. Spec §3 step 2 and the seed ("pushes the branch ... as `done` does") are wrong on the push. Hand-over calls `git.Client.Push(ctx, dir, remote, branch, false)` (`internal/git/sync.go:144`) itself, then the same `worktreeTeardown`. The push reaches the network, so it is never run from a `cmd/relevo` test.
2. **"Triggers are immutable" means migration files are never edited, not that a trigger cannot change.** `027_shared_outbox_unique_signal.sql` already uses `DROP TRIGGER`. The new migration drops and recreates the affected triggers. `TestOwnerResolutionMatchesTriggers` keeps holding because it compares `OwnerRules` against the live `sqlite_schema` bodies, so the rules change in the same round (§A).
3. **Decision 2 is not enough by itself.** "Apply only when the entry's origin is the row's writer at that point in the log" cannot be read off the row's current `writer` column: the old writer's children, and any snapshot of its log, arrive after the root already says the new writer. The importer needs a small per-binding term table (§B). This implements decision 2; it does not change it.
4. **Decision 3 says the first entry carries `after`.** Compaction (`compactDrops`) and reconcile re-emission can remove or replace the first entry. The exporter therefore attaches `after` to *every* entry of the binding tree until the writer changes again. That is a superset of "the first entry". If the builder finds this breaks something, halt and report; do not drop the superset silently.
5. **Update triggers on the two roots must attribute to `OLD.writer`.** The hand-over is `UPDATE ... SET writer = <target>`. An outbox row attributed to `NEW.writer` would name the target, and `owned()` (`synclog/export.go:234`) would drop the very entry that carries the hand-over. Insert triggers use `NEW.writer`; update and delete use `OLD.writer`.

## 1. Behaviour

- `binding_record` and `binding` gain `writer`; `origin` stays the creator. Children resolve the writer through the root.
- Exporter emits a row only if its writer is this installation. Importer applies an entry only if its origin was a valid writer of the binding at that point in the log (term rule, §B).
- `relevo handover <name> --to <machine>`, `relevo bind --resume --name <n>` on the target, `relevo takeover <name> --force`, `relevo db sync enable --as <installation>`.
- Entries gain `After{Origin, Seq}` (MAC-covered). A new reserved entry table `ctl:takeover`. Remote log gets two nullable columns. `ProtocolVersion` 3 to 4.

## A. Separating writer from origin (the import path, triggers, OwnerRules)

**Today.** `Entry.Origin` is "whose log" and "the row's owner". `applyEntry` (`synclog/import.go:495-541`) calls `tx.ExchangeUpsert(e.Table, e.Origin, values)`; `ownedValues` (`db/sync_import.go:~190`) overwrites the column named by `ownerColumn(tbl)` (`rule.owner`, `"origin"` for every root) with that argument. `owns` and `ownsAfterWrite` compare `ResolveOwner` to `e.Origin`.

**New.**
- `OwnerRules` (`db/sync_exchange.go:458`): `binding_record` and `binding` become `{owner: "writer"}`; the hops of `binding_event`, `round_file`, `round`, `event`, `artifact`, `transcript` (round kind) read `writer` instead of `origin`. `repo`, `mastermind`, `chains`, `installation`, history tables, chain children and the transcript mastermind kind are unchanged. `ownerRule` gains `oldOnUpdate bool`, set on the two roots.
- `ownerColumn("binding_record"|"binding")` now returns `writer`. A second helper `creatorColumn(tbl)` returns `origin` for exactly those two tables and `""` for the rest.
- `ExchangeUpsert` keeps its `(tbl, owner, values)` signature. For the two roots `owner` means *the writer after this entry*; the caller passes `e.Origin` normally and `values["writer"]` only when `authorize` (below) approved a hand-over. The `origin` column is **taken from the body, never overwritten with `e.Origin`**, and validated inside the upsert: if the row exists, body origin must equal the stored origin (immutable, else `ErrInvalid`); if absent, body origin must be non-empty and, when it differs from the entry's origin, the entry must carry `After` (a handed-over row's first sight) — otherwise refused as a forged creator claim. `db` cannot see the entry, so the caller passes a `creatorClaimOK bool`; the check lives in `synclog` (§B `authorize`), `db` only enforces immutability.
- Echo guard: when the post-write writer is *this* installation and `e.Origin` is not (a hand-over landing on the target, including a first-sight insert whose `NEW.writer` is this machine), the importer deletes the outbox rows the statement just added (new `Tx.DiscardOutboxSince(tbl, pk, seq)`, seq read before the write). Without it the target would re-export the row it just received.
- `ownsAfterWrite` and `owns` are replaced by one `authorize(tx, e) (decision, error)` (new file `synclog/authorize.go`, §B). `import.go` is 577 lines: nothing is added there beyond the call; `authorize.go` and `terms.go` carry the logic.

**Migration `031_binding_writer.sql`** (README rules: add-only, no cascade FK, never raises in a trigger):
1. Drop the 24 outbox triggers of `binding_record`, `binding`, `binding_event`, `round_file`, `round`, `event`, `artifact`, and the three `transcript` triggers *before* anything is updated (a backfill `UPDATE` would otherwise queue the whole table).
2. `ALTER TABLE binding_record ADD COLUMN writer TEXT NOT NULL DEFAULT ''`, same for `binding`; `UPDATE ... SET writer = origin`; indexes `binding_record_writer_idx`, `binding_writer_idx`; unique partial index `binding_record(writer, owner, name) WHERE archived_at IS NULL` (equals the existing `(origin, owner, name)` index on day one, so it cannot fail on existing data; it stops a handed-over name colliding with a local live one in the scoped reads).
3. Recreate the triggers: roots `ins` uses `NEW.writer`, `upd` and `del` use `OLD.writer`; children read `(SELECT writer FROM binding_record|binding ...)`.
4. Create four machine-local, trigger-free tables (same shape and reason as `030`): `sync_binding_term(root_id, writer, upto, regrant_origin, regrant_seq, PRIMARY KEY(root_id, writer))`, `sync_writer_after(root_id PRIMARY KEY, after_origin, after_seq)`, `sync_binding_left(root_id PRIMARY KEY, owner, name, to_writer, kind, at)`, `sync_fenced_change(root_id, tbl, pk, at, PRIMARY KEY(root_id, tbl, pk))`.

**`TestOwnerResolutionMatchesTriggers`** (`db/sync_exchange_test.go:785`): it picks `NEW` for insert/update and `OLD` for delete. Change it to pick `OLD` for `update` when `rule.oldOnUpdate`; the rest is unchanged and still compares every shared table and op to the live trigger text. Add a test that the 031 trigger set is complete (every op of every table present) and one that a hand-over `UPDATE` queues an outbox row attributed to the old writer.

**Closed list of `origin` writes/reads on the two roots to change** (builder greps `origin` in `internal/db/{record,read,write,relabel,repo_rename,origin,origin_twin,origin_repoint,origin_gate,history*,stats*}.go` and `internal/store`; anything not listed that the grep finds is reported, not guessed):
- `record.go:58` `originScope` and its users (lines 108-345): `origin IN (?, '')` becomes `writer IN (?, '')` for `binding_record`. The live-key lookup in `SaveRecord` (281) too.
- `record.go:287` update path no longer stamps `origin`; it stamps `writer` only when the stored writer is `''` or this installation. Insert (310): `origin = writer = t.origin`.
- `write.go` mirror `binding` insert/update, `relabel.go:208,213`, `repo_rename.go:296,329`: stamp `writer` with `origin` where they stamp ownership of a row this machine owns; never change `origin` of an existing row.
- `origin.go:203` backfill: `UPDATE ... SET origin = ?, writer = ? WHERE origin = ''`; `origin_gate.go` also refuses sync while a `writer = ''` root remains; `origin_twin.go`/`origin_repoint.go` rules stay on `origin`.
- Read paths that list "my" bindings for stats/status (`read.go`): writer-scoped; creator-scoped only where the label is "who created".

`SharedOwnedRowPage` (`db/sync_reconcile.go`) already renders `ownerExpression`, so reconcile's "owned rows" become writer-owned with no code change; a test pins it.

## B. "The writer at that point in the log"

Per-origin logs are applied strictly in sequence; the only cross-log ordering is `After`. Entry gains `After *Ref{Origin string; Seq int}` (`synclog/entry.go`), covered by the MAC.

**Terms (importer state, `sync_binding_term`).** A row `(root, W, upto)` means "W stopped being the writer of this root; its entries are valid only through `upto` in W's log". `upto` for a hand-over = `tail(batch)` of the batch carrying the root upsert whose body writer differs from its origin; the exporter orders such root rows **last** in their batch and the handover verb exports them as their own batch, so every child entry the old writer wrote is `<= upto`. For a takeover `upto = N`. A grant to a writer (hand-over to it, takeover by it) deletes its term row, or, when it had one, records `regrant = the grant entry`.

**`authorize(tx, e)`** (pure core `EntryValid(term, e)` in `terms.go`, testable on its own). For an entry on root R (root of a child resolved through its parent chain with a new `db.ResolveRoot(tbl, pk)`):
1. Root row present and `writer == e.Origin`: valid.
2. Term `(R, e.Origin)` present: valid iff `e.Seq <= upto`, or `regrant` is set and (`e.After == regrant`, or `regrant.origin == e.Origin` and `e.Seq > regrant.seq`). This is the **honest late write** (a child from the old writer's own snapshot or from a log applied after the root flipped). Anything above `upto` is **refused** (post-hand-over write: a bug or a forgery) and is reported as a drop with reason "writer moved".
3. Root row absent (first sight, joiner or snapshot): only the root upsert itself is decided here; it is valid iff body writer is its origin, or (body writer differs, body origin may differ) it is a hand-over and the entry carries `After` and passes the signed check. Children whose parent is absent keep today's FK deferral.
4. Any other writer: refused as today (`errNotOwner`).
5. A root upsert from the current writer with body `writer != e.Origin` is the hand-over: it flips the row, records the old writer's term (`upto` per above), records `sync_writer_after(R) = (e.Origin, tail)` when the new writer is **this** installation, and writes `sync_binding_left` when the *old* writer is this installation (cannot happen on import; the verb writes it).
6. **Signed only.** Rules 3 and 5 and every `After`-bearing or `ctl:` entry require `len(i.key) > 0` and `e.Seq >= signedFrom[origin]` and a verified MAC (decision 7). A keyless/locked importer **holds the origin** with a `Hold.Reason` instead of dropping (a drop would move the mark and lose the hand-over); an entry in the unsigned region of a signed origin is refused. A takeover or hand-over under a retired epoch fails `i.verify` exactly like any entry (`allowedKeys`).

**`After` hold.** `Import` (`import.go:269`) holds an origin whose next batch carries an `After` with `marks[after.Origin] < after.Seq`. Hold means: skip the origin, keep its mark, no drop. To make the same-run case work, `Import` becomes a fixpoint loop: process the origins not waiting, then retry the waiting ones while a pass advances any mark; the same applies in `snapshots()` (`snapshot.go:46`) which checks the chunk's `After`s before `snapshotOrigin` applies it. A hold that persists is a `Hold` with a Reason naming both origins; `Moved()` is unaffected so drain loops still stop.

**Orders and what holds vs refuses.**
- Hand-over in log A, new writer's entries in log B, B arrives first: B held until `mark[A] >= h`; then A applied through `h` (root flips, term closed), then B.
- A's log compacted past `h`: the snapshot's last root entry is the hand-over (body writer = target), placed on a joiner as the target's row; A's older children are valid by rule 2/3 only if a term exists, so the snapshot path records the term from the root entry it applies (same code as rule 5) before the children chunks. Root entries are applied in chunk order before children (`splitRegion` already orders parents first).
- Old writer entry above `upto` after the term: refused. Before the term is known (another origin's view): cannot occur — the old writer's own log is applied in order and `h` is in it.
- Gap entries between a writer's closure and its re-grant are not distinguishable on a late importer; documented limitation, they cannot be written by an honest exporter (its `owned()` filter drops them, and the verbs refuse local writes while handed over).
- A hand-over landing on a machine that already has a *live local binding of the same (owner, name)* fails the new unique index; that is a `Hold` (not a drop) with the reason "name taken: unbind or rename the local binding, then the hand-over applies", so the mark does not pass it.

**Exporter.** `owned()` additionally re-checks the row's current writer with `ResolveOwner` for the two root families (an entry already in the outbox when a takeover flips the row is moved to `sync_fenced_change` and dropped). Entries and reconcile upserts (`export.go:168`, `reconcile.go:347` `inspectUpsert`) call one `db.WriterAfter(tbl, pk)` (cached per batch) and set `Entry.After` from `sync_writer_after` of the row's root. MAC: `EntryMAC` (`mac.go`) keeps domain `relevo-sync-entry-v1` byte-for-byte for entries with no `After` and no `ctl:` table, and uses `relevo-sync-entry-v2` with two extra fields otherwise, so existing logs still verify and a v1-only binary cannot verify a v2 entry.

**Wire/store.** Remote `log` gets nullable `after_origin TEXT, after_seq INTEGER`, added idempotently on open (`syncworker/format.go`, check `pragma_table_info`; `logFormatVersion` stays 3, because every pull/push statement names its columns, so the extra columns are invisible to an old worker). `pullQuery` (`pull.go:94,104`), the append, and `syncworker/protocol.go` Entry wire form carry them; `ProtocolVersion` becomes 4 with the doc comment extended. `memfake.go` carries `After`. 

## C. Target side of a hand-over

- **Checkout.** `bind --resume` runs in the caller's cwd (`resume`, `bind.go:309`; today it restores a gone worktree at the *recorded* path with `CheckoutWorktree(ctx, opts.CWD, b.Worktree, b.Branch)`). On a handed-over binding that path is the old machine's, so a new `resumeHandedOver` (in `internal/relevo`, before the restore block) runs when the stored writer is this installation and the creator is not: (1) the caller's cwd must be a checkout of the binding's repository — pure `SameRepo(b.RepoRef.OriginURL, localRemoteURL)` using the existing remote-URL normalizer the repo rows use (builder locates it in `internal/db/repo_*.go` / `internal/git`); (2) `git.Client.Fetch(ctx, cwd, "origin", branch)` and a missing branch is a refusal naming "ask the old machine to run `relevo handover` again"; (3) the worktree path is recomputed with the fresh-bind convention for this machine and written into the record (`CWD`, `Worktree`, `Subdir` kept); (4) the mirror `binding.repo_id` is repointed to this machine's own `repo` row for that URL (created through the existing repo-row path when absent), and `MasterMind`/`MasterMindID` are the resuming caller's, which `resume` already sets, so the FK points at a mastermind row this machine owns.
- **No checkout / different repo:** refused with the URL; nothing changes; `--resume` is retried from the right directory. No auto-clone in this step.
- **Server bindings (`Link` set, or `Builder.Remote()`): refused.** The runner, queue and round state live on the server under this machine's client key; `Link.Installation`/`Link.ID` name the server's copy (`store/types.go:373`). Handing the client copy over would leave the server copy tied to a client the target cannot authenticate as. The owner finishes or unbinds there first. Pure `HandoverRefusal`.
- **Rows that stay with the creator:** `repo`, `mastermind`, `chains` rows (and chain children). The target reads them as foreign rows; the `After` hold guarantees they were applied before the target's first entry (they precede the hand-over in the creator's log).

## D. Reconcile, compaction, snapshots, join

- **Whose log.** From the hand-over, the new writer's log carries the whole binding tree: its reconcile emits every writer-owned row missing from its own head (the full tree, with `After`). The old writer's reconcile stops emitting (`SharedOwnedRowPage` filters by writer) and never emits a delete for a row it still holds (`deleteFor` finds it locally). If the new writer later deletes the binding, the old writer's head still names it; `deleteFor` then emits a delete that importers refuse by rule 2 (above `upto`). That is reported as a quiet "writer moved" drop; accepted noise, listed in the report.
- **Compaction** is per origin and unchanged (`compactDrops`, `FloorFor`); `ctl:takeover` uses pk `[root_id, old_writer]` so a later takeover supersedes only an earlier one for the same pair and the fence still only rises.
- **Joiner, two changes (A to B to C).** Entries carry `After`: C's entries `(B, h2)`, B's `(A, h1)`. The joiner (`sync/join.go:387`, `drainImport`) runs the fixpoint import: A through `h1` (term A closed), then B through `h2` (term B closed, writer C), then C. Snapshots of any of the three hold on the same rule. A's snapshot cannot resurrect rows C owns: A's root entry in the snapshot already says writer = B, and the `After` hold means C's deletes are applied after A's snapshot, never before.
- **`PrepassDocs`** (`docprepass.go`) only handles docs; unchanged.

## E. Takeover

`relevo takeover <name> --force` (new `cmd/relevo/takeover.go`, logic in `internal/relevo/takeover.go` as pure `TakeoverPlan`):
1. Requires sync enabled and unlocked; drains an import first so N is as high as it can be.
2. In one transaction: reads the root row (must exist locally, writer = old writer, not this machine) and `N = ImportMark(oldWriter)`; sets `writer = this` on both roots (outbox attributes it to the old writer, so it is not exported as a plain row); deletes any `(root, this)` term; records `(root, oldWriter, upto = N)`; writes `sync_binding_left`-free (the taker is not leaving).
3. Appends a `ctl:takeover` entry: body `{record_id, binding_id, old_writer, upto, taker}`, `After = (oldWriter, N)` (the same fact, so the generic hold applies and nobody flips the writer before applying the old writer's entries through N), signed.
- **N when the taker never imported the old writer:** a mark of zero or absent is allowed only when the taker holds the binding's root row; it prints "entries N+1..H of <old> will be fenced" (H from `ReadMarks().Heads`) and needs `--force` (already mandatory). A taker with no root row refuses ("run `relevo db sync pull`").
- **Fence location:** `sync_binding_term`. An importer applying the takeover (after the `After` hold): sets writer on its rows if present, upserts the term with `upto = N`, deletes the taker's term, records `sync_binding_left(kind=takeover)` when this installation was the old writer.
- **Old machine returns:** it applies the takeover, its exporter stops (writer flipped, `owned()` re-check moves still-queued entries into `sync_fenced_change`). `db sync status --json` gains `fenced: [{name, old_writer, count}]` and the line reports "N local changes to <name> were fenced by a takeover"; nothing is deleted locally. Entries it already appended above N are refused by every importer that has the term; an importer that applied them before it saw the takeover keeps them (documented residual; the new writer's reconcile repairs upserts, not stray children).
- **Second takeover (back, or a third machine):** the taker's "old writer" is whoever the row says is writer at that machine; it adds `(root, thatWriter, N')`. Earlier terms stay. Takeover back by the original machine deletes its own term and records `regrant = (itself, takeover seq)`, so its new entries (`Seq > regrant.seq`) are valid while its partition-era entries (`N < Seq < regrant.seq`) stay fenced.
- **Hand-over to a machine that never resumes:** undone by `takeover` from the original machine (old writer = the target, N = its mark, usually 0); same command, no extra case.

## F. `db sync enable --as <installation>`

- **Reading "last 24 hours" (pure `AdoptionRefusal(now, lastEntryAt, lastMarkAt)` in `internal/sync`):** last entry time = `Entry.At` of the origin's head entry (`ReadMarks().Heads[X]` then `PullRange(X, head-1, head)`); last mark time = max `Mark.At` over `MarkState.Marks` with `Reader == X`. Either within 24h (or stamped in the future up to `markSkew`) refuses. No entries and no marks is allowed.
- **Claim (stops two simultaneous adopters).** New transport verb `ClaimIdentity(ctx, origin, token, at) (winner string, claimedAt time.Time, err)` on `LogTransport`, `memfake` and the worker (`VerbClaim`, in protocol 4 — nothing is released between rounds, so no second bump): inserts `meta` key `claim:<X>` only if absent or older than 24h and reads it back. The loser is refused. After the join the adopter publishes marks as X at once, which trips the 24h rule for any later adopter, and the existing own-head guard (`ownhead.go`, `unlock.go:78`, `keyed.guard`) is the third line for a stale duplicate appender; the builder pins it with a memfake test.
- **Mechanics (fresh machine only).** Refuse unless the local shared tables hold no rows with a foreign-to-X origin and the outbox is empty. Join as usual under the machine's temporary identity (X's log imports as a foreign origin, so its rows land as X's), then: write the new `installation.json` via a new `installation.Adopt(root, id)` (atomic temp+rename like `mint`, label kept), delete the temporary identity's `installation` row, set `sync.own_head.v1` to X's remote head, keep `sync_import_mark`, `sync_signed_from`, `sync_epoch_cut` and the keyring (they are verified facts about other origins and this machine's own key), keep the `ClearDocExportState` the join already does, and require a daemon restart (the origin is read at open; the verb tells the owner). Confirmation prompt follows `db_sync_yes_test.go`'s `--yes` convention.

## G. Interruption, and what the owner runs

1. **Writer set, export never ran** (daemon down, offline): the outbox holds the hand-over rows attributed to the old writer; `relevo status` marks the binding "handed over to <m> (not yet exported)" (outbox query by root). Owner: nothing; `relevo db sync` (or the daemon tick) exports it. If the old machine never comes back, run `relevo takeover <name> --force` on the target, using the state it already imported.
2. **Branch push failed:** the verb pushes *before* releasing or setting anything. A failed push aborts with no state changed; rerun. If the push succeeded and the writer transaction failed, rerun is idempotent. A hand-over rerun against an already handed-over binding with the same `--to` only re-pushes the branch.
3. **Target resumes before importing the hand-over:** `bind --resume` finds writer = old machine (or no row): pure `ResumeRefusal` says "not handed to this machine yet: run `relevo db sync`", nothing is written.
4. **Hand-over held on the target** (locked, unsigned, name taken, waiting on `After`): `db sync status` reports the hold with its reason; resolving it and syncing applies it.

## 2. Steps by round (each green on its own)

### Round 1 — writer column, triggers, rule split (no behaviour change: writer == origin everywhere)
1. Move `OwnerRules`, `ownerExpression`, `ResolveOwner` and helpers (`sync_exchange.go:412-595`, file is 595 lines) into `internal/db/sync_owner.go`; add `oldOnUpdate`, `creatorColumn`, `ResolveRoot`. Done when `make check` filesize passes and nothing else moved.
2. `031_binding_writer.sql` as in §A. Done when a fresh and an upgraded DB both migrate and the writer equals origin for every row.
3. Rewrite the `origin` reads/writes in the closed list (§A). Done when every existing db/store test passes unchanged except the ones that name `originScope`.
4. `ExchangeUpsert` creator/writer split, immutability check, `DiscardOutboxSince`. Done when a unit test shows a body with a different origin cannot move an existing row's origin, and a hand-over-shaped upsert keeps origin and sets writer.
5. Update `TestOwnerResolutionMatchesTriggers`, add trigger-completeness and old-writer-attribution tests; `SCOPES.md` (rows for `binding_record`/`binding` gain the writer description, "Machine-local" section lists the four tables, migration 031 noted). Done when focused `-run 'Owner|Writer|Trigger'` is green.
- **Mutations (each must fail a named test):** `OLD.writer` to `NEW.writer` in the update trigger; hop column `writer` back to `origin`; drop the origin immutability check; drop the outbox discard.
- Report: counts of rows migrated, the list of origin sites changed vs the closed list, coverage baseline untouched.

### Round 2 — `After`, MAC v2, terms, importer rules, wire
1. `Entry.After`, `NewUpsert`/`NewDelete` unchanged signatures plus `WithAfter`; `mac.go` v2 domain. Done when old fixtures still verify and a v1-only check fails a v2 entry.
2. `synclog/authorize.go`, `terms.go` (`EntryValid`, term upserts, signed gate), hook into `applyEntry`; `Import` fixpoint loop and `snapshots()` After check; unique-collision `Hold`. Done when memfake tests cover hand-over in-order, out-of-order across logs, late child, post-term refusal, snapshot-first joiner, two changes, keyless hold.
3. Exporter: `db.WriterAfter`, batch ordering of hand-over roots last, `owned()` re-check, reconcile attach; `memfake` + `syncworker` (`format.go` columns, `pull.go`, `protocol.go`, `ProtocolVersion` 4 and its comment). Done when worker pipe tests and memfake round-trip carry `After`.
4. A pre-step importer test: an entry with `SchemaVersion` above `known` is held, never applied (it already is via `held()`; the test pins that a hand-over entry takes that path, including in the snapshot path).
- **Mutations:** skip the `After` hold; accept `e.Seq > upto`; accept a hand-over without a verified MAC; apply a hand-over when `i.key` is empty; let the root row's body origin through unchanged for a forged claim; attach `After` only to the first entry.
- Report: protocol/format notes, files over 500 lines.

### Round 3 — hand-over verb, resume, takeover, status
1. `internal/relevo/handover.go`: pure `HandoverRefusal` (open round, running chain member via the `refuseRunningChainMember` predicate, `Link`/`Builder.Remote()`, already handed over), `ResumeRefusal`, `SameRepo`; `cmd/relevo/handover.go` verb (push, `worktreeTeardown`, one transaction: both roots' writer plus `sync_binding_left`). Done when the rule functions are table-tested; **no cmd test spawns a worker, a harness, git push or the network — flag parsing only, rules in `internal/relevo`.**
2. `store`: `Load`/`send` return a handed-over error instead of not-found for rows in `sync_binding_left`; `relevo status` shows "handed over to X"; `bind --resume` refused on the old machine.
3. `resumeHandedOver` (§C) and `bind --resume` wiring.
4. `ctl:takeover` entry kind (`IsCtlTable`, body codec, importer apply, fence, `sync_binding_left`), `takeover` verb, `TakeoverPlan`, status `fenced`. Done when two memfake machines converge after a takeover and the returning machine reports its fenced changes.
- **Mutations:** allow handover with an open round; allow `Link` bindings; takeover without the `upto` fence; fence that lowers; regrant ignored.
- Report: each refusal message, the residual of §E.

### Round 4 — `enable --as`, claim, docs
1. `ClaimIdentity` on transport, memfake, worker; `AdoptionRefusal`; `installation.Adopt`; `enable --as` flow (§F). Done when two adopters race on memfake and exactly one wins.
2. `docs/plans/2026-10-17-sync-ownership.md` (this plan, with the deviations actually taken), `docs/runbook.md` rollout line ("every machine on this build before anyone hands over"; `handover` additionally checks each origin's last entry `SchemaVersion >= 31` and refuses naming the lagging origin), spec §3 reworded to "built" and §12 item 6 marked built; §13 hand-over of a whole chain stays out.
3. Regenerate the coverage baseline only if code moved between packages, and say so (`sh scripts/check-coverage.sh --write`).
- **Mutations:** drop the 24h check; accept a claim when one exists; skip the outbox-empty refusal.

## 3. Deleted or replaced behaviour (closed list)
1. `originScope` (`origin IN (?, '')`) as the scope of every `binding_record` read and write; replaced by the writer scope.
2. `record.go:287` stamping `origin` on every update.
3. `ownedValues` overwriting the `origin` column of `binding_record`/`binding` with the entry's origin.
4. `owns` and `ownsAfterWrite` as separate checks (replaced by `authorize`).
5. The 24 old outbox triggers of the two roots and their children (replaced by 031's).
6. "An importer applies any entry whose origin is the current row owner" for the two root families.

## 4. The report must include
Per round: files changed vs this plan's seams, `git diff --stat`, the mutation list with the test name that failed for each, the exact `make check` result under `RELEVO_REQUIRE_COVERAGE=1`, any file over 500 lines, the list of `origin` sites found by grep vs the closed list, the deviations from §0 actually taken (especially item 4), the residuals (§D noise, §E divergence of already-applied fenced entries), and any halt (a step impossible as written, or a contradiction with the code) stated rather than worked around. Never amend or rebase a pushed commit; every round is new commits.

# Deviations

Deviations actually taken, by round. Earlier rounds' entries below are those the
round reports recorded for the hand-over and takeover work (Round 3).

## Round 3

- A rerun of `handover` against a binding that is already handed over returns
  "already" and pushes nothing: once the transaction commits the record cannot
  be loaded here, so there is no branch or directory to push. A rerun after a
  push that succeeded but a transaction that failed pushes again.
- The takeover's fence warning prints N but not H, the old writer's head: H
  comes from `ReadMarks().Heads`, which needs a transport the CLI does not have.
- Round 2 code was touched: an `After` naming this installation's own log no
  longer makes its own import wait on its own origin's mark (`batchWaitingAfter`
  and `checkRegionAfterHold`), and `applyHandover` clears `sync_binding_left`
  when a hand-over lands back on this machine.
- The importer records the highest schema each origin wrote (kv
  `sync.origin_schema.v1`); the "target below schema 31" refusal (C3) reads it.
- Pending control entries and the left-row manifest live in kv keys rather than
  new tables, so migration 031 stays as written.

## Round 4

- **C4 mechanism: join under the temporary identity, but stop before any
  write.** Of the two mechanisms C4 allows, the first (adopt, then import X's
  log as own) needs a transport pull of the machine's own origin and a second
  import path for snapshots and floors; the second (join with export and mark
  publishing off) still runs the reconcile. The code instead runs the join only
  as far as its import (`Enabler.joinImport`): X's log lands as a foreign
  origin's through the existing importer, snapshots and floors included, and
  the temporary identity appends, publishes and writes nothing. The swap
  (`db.AdoptIdentity`, then `installation.Adopt`, then the own-head fact) runs
  only after the import reached the head and the claim was read again. The
  joinee's local-file capture is skipped during the join, so no document item is
  stamped with the temporary id.
- **The enable does not turn sync on.** §F said "require a daemon restart (the
  origin is read at open; the verb tells the owner)". The adoption leaves sync
  off with the join marker set; after the restart a plain `relevo db sync
  enable` resumes the join as X (import is a no-op, the reconcile is converged
  and appends nothing) and turns the machine on.
- **Precondition on the outbox and `shared_outbox`.** "Empty" is read as: no
  `sync_outbox` row except the machine's own installation-row upkeep (every
  open touches it), and no document signal except `config` (every open
  signals it) and `secrets` (this enable's own store step signals it). The
  signals live in the machine-local file, so that is where they are counted.
  Any other change, or any shared row but the machine's own installation row,
  refuses before the claim.
- **Claim read-only mode.** `ClaimIdentity` with an empty token only reads the
  claim; no second verb was added. The same function serves the steady attempt's
  comparison.
- **Claim reuse on a rerun.** The claim token is stored in kv `sync.adopted.v1`;
  a rerun of `enable --as` for the same id reuses it, so an interrupted
  adoption is not locked out by its own claim.
- **`--yes` is the confirmation**, following `db_sync_yes_test.go`; `--as` with
  an invite code is refused.
- **Runbook line follows C3, not the Round 4 step text:** the hand-over checks
  the target's newest entry for schema 31, not every origin's.
- **No new wire code:** the adoption refusals classify as `invalid`.

## Residuals

- A local write made by the daemon between the swap and its restart is
  attributed to the temporary identity and stays a foreign row of the adopted
  machine.
- The claim is exclusive per attempt and detected afterwards; two machines that
  both read their own token back are told apart only on their next steady
  attempt.
- The coverage baseline was not regenerated: no code moved between packages.
