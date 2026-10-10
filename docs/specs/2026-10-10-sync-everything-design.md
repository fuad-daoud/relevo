# Sync everything: bodies in R2, shared config, hand-over

Issues: #1103 (a first join moves every body), #1089 (cost model). Builds on
`2026-10-07-sync-redesign-design.md` (the per-origin log, merged as #1065, with
liveness #1088), which stays in force except where this spec changes it.
Status: draft for owner review, 2026-10-10. Canvas: the "New design" page of
https://claude.ai/artifact/Qi4XQb6mKva9wpH2ZaoXTq.

## 0. Why

Sync is off on all three machines. Turning it on as merged would cost:

- A first join of all three machines moves about 2.6 GB through Turso, against a
  10 GB monthly sync quota with overages disabled. Each machine's replica
  (`relevo-sync.db`) is a full copy of the remote, its own entries included, so
  every machine downloads every byte any machine writes.
- About 90% of those bytes are round-file and transcript bodies. They are
  already zstd at rest (the laptop's round files: 1,185 MiB raw, 203 MiB
  stored) and travel as base64 inside JSON, a third larger again.
- The log is never compacted, so storage (9 GB cap) and every future join grow
  for as long as relevo runs.
- Nothing counts bytes. The driver's own stats are discarded.

The owner's goals also go past what the merged design does. Sync must let them:

1. see every machine's bindings and rounds on any machine;
2. read any round, report, diff and transcript on any machine;
3. recover a machine's history after losing the machine;
4. move a binding's work from one machine to another;

and **a new machine must need no reconfiguring**: config, custom agents and
actors, hook scripts, gates, consents and the secrets relevo stores all travel.
The merged design does 1 and 2, does 3 only partly (a rebuilt machine gets a
new installation id, so its old rows come back read-only), and cannot do 4:
each row has exactly one writer forever. It syncs no config at all.

## 1. Decisions

- **D1. Everything still flows through per-origin logs.** Each machine appends
  only its own entries, as in the merged design. Data several machines may
  change is merged when it is applied, never by two machines writing one
  remote row.
- **D2. Bodies go to R2, rows go to Turso.** A body whose stored size is over
  4 KiB is uploaded to an R2 bucket as `<origin>/<sha256>` before its entry is
  appended, and the entry carries a reference. Every machine still fetches
  every body eagerly; R2 charges nothing for downloads. Measured on the laptop:
  4 KiB moves 45% of round files and 97% of their bytes.
- **D3. Turso stays behind the embedded replica and the sync-worker.** What's
  merged and live-tested stays. With bodies in R2 the remote holds about
  50–70 MiB, so a full replica is cheap.
- **D4. A binding has one writer at a time, and the writer can change.**
  `origin` remains the creator and never changes. A new `writer` column names
  who writes the binding now. Hand-over and takeover change it.
- **D5. Data any machine may change merges newest-wins per key**, ordered by a
  hybrid logical clock. The losing edit is kept, never dropped.
- **D6. Config is a shared layer plus a local layer.** The shared layer syncs.
  A per-machine patch holds what only that machine knows. The `sync` section
  stays machine-only.
- **D7. Every entry is signed** with a key derived from a passphrase the owner
  enters once per machine (from step 3 on). Secrets on an allow-list are also
  encrypted with it. Someone holding only the Turso token can't inject config,
  a hook or a takeover.
- **D8. One enable sets up a new machine:** a join code plus the passphrase.
  Harness logins are the only thing left to the owner, and `relevo doctor`
  lists them.
- **D9. Costs are counted and bounded:** bytes per month per service, a guard
  before a join, and log compaction.

Out of scope: lazy body fetch; handing over a whole chain; copying harness
logins (Claude Code, Codex, OpenCode credentials); a self-hosted hub (the
`LogTransport` seam keeps that possible later).

## 2. What syncs, and by which rule

| Rule | What |
|---|---|
| One writer per binding (history) | `repo`, `mastermind`, `installation`, `binding_record`, `binding_event`, `round_file`, `chains`, `chain_event`, `chain_member`, `chain_check`, `binding`, `round`, `event`, `artifact`, `transcript` |
| One writer per machine (history, new tables) | `availability_event`, `latency_sample` |
| Newest wins per key (shared docs) | config shared layer (candidates, agents, actors, accounts, policy, roles, prices, servers, hooks, workflows); the files config points at (native agent files, hook scripts); gates; repo consent by remote URL; session consent; allow-listed secrets (encrypted) |
| Bytes through R2 | `round_file.body`, `transcript.record_json`, `transcript.rendered` over 4 KiB stored |
| Never | installation id; the `sync` config section; the local config layer; `turso.token`; `serve.tls.*`; `agy/*`; `sync_outbox`, `sync_import_mark` and the new local sync tables; `ingest_cursor`; `config_import`; kv `daemon`, `serve.*`, `claim`, `hooks.log`, `ui`, `agents-manifest`, `release-check`, run-once markers; `schema_version` |

`SCOPES.md` is rewritten to match this table in the step that changes each row.

## 3. Ownership and hand-over (step 6)

**Schema.** `binding_record` and `binding` gain `writer TEXT NOT NULL`, set to
`origin` by the migration. Child rows get no column; they resolve their
writer through their parent exactly as they resolve `origin` today
(`ResolveOwner`).

**The rule.** The exporter emits a row only if its resolved writer is this
installation. The importer applies an entry only if the entry's origin is the
row's writer at that point in the log. One writer per row still holds, so
nothing conflicts.

**Ordering across machines.** An entry may carry `after = (origin, seq)`. An
importer holds that entry's origin until its mark for `after.origin` is at
least `after.seq`. The first entry a new writer appends for a binding carries
`after` = the hand-over or takeover entry it depends on. No wall clocks are
involved.

**`relevo handover <name> --to <machine>`**, run on the current writer:
1. Refused while a round is open, or while the binding is a member of a
   running chain.
2. Pushes the branch and releases the worktree, as `relevo done` does.
3. Sets `writer` on the binding's root rows, in one transaction. This is an
   ordinary change by the current writer and travels in its log.
4. The binding shows as handed over locally; `send` and `bind --resume` on it
   are refused here.

On the target, `relevo bind --resume --name <name>` sees `writer` = this
machine, recreates the worktree from the branch and continues.

**`relevo takeover <name> --force`**, run on the new machine when the writer
is gone. It appends a signed `takeover` entry naming the binding and
`upto = (old writer, N)`, where N is this machine's mark for the old writer.
Every importer applying it records a fence: entries from the old writer for
that binding with seq > N are refused. If the old machine comes back, its
importer applies the takeover, its exporter stops emitting the binding, and
`db sync status` reports any local changes that were fenced off.

**`relevo db sync enable --as <installation>`** recovers a whole machine by
adopting the dead machine's identity. It's refused if that origin appended
anything or published a mark in the last 24 hours, and it asks for
confirmation. After adoption the machine joins as usual: its own rows come
back as its own, and reconcile repairs anything missing.

## 4. Shared docs (step 3)

**Shared doc.** A shared doc is anything that splits into keyed items, each of
which any machine may set or delete. One interface covers them all:

```go
type SharedDoc interface {
	Name() string                                   // "config", "gate", "consent", "secret", "file"
	Items(tx) (map[string]json.RawMessage, error)   // the current value of every key on this machine
	Apply(tx, key string, value json.RawMessage) error // nil value deletes the key
}
```

Instances: config sections (one key per item in candidates, agents, actors,
accounts and servers; one key per section for policy, roles, prices, hooks and
workflows), gates (one key per ledger subject), repo consent (key: remote
URL; repos without one don't share consent), session consent (key: harness
kind + session id), allow-listed secrets (§5), and files (§4.3).

**Change capture.** A new `shared_outbox` in `relevo-local.db` is filled by
triggers on `config_doc`, `secret`, `session_consent` and the kv rows of the
shared namespaces. The trigger only records which doc changed. The exporter
diffs that doc's items against `shared_export_state` (key → hash of the last
exported value) and emits one entry per changed key, stamped with the hybrid
clock. Diffing keeps entries at item level even though config is stored per
section; triggers keep the rule that every writer is caught.

**Hybrid clock.** 64 bits: wall milliseconds << 16 | counter, ties broken by
origin. The local clock never runs backwards, and applying a newer stamp moves
it past that stamp, so a machine with a slow clock can't keep losing.

**Apply.** For each doc entry, the importer compares its stamp with
`shared_applied` (key → stamp and origin of the value in force).
- Newer: the importer applies it through the doc's `Apply`, inside the batch
  transaction.
- Older: the importer keeps it as a superseded revision.

Config changes, applied or superseded, land in `config_revision` with source
`sync:<machine label>` or `sync-superseded:<machine label>`, so `relevo config
log` shows them and `relevo config rollback` restores one.

### 4.1 Config layers

`config_doc` holds the shared layer. A new local-only table
`config_local(section, patch, updated_at)` holds a JSON merge patch per
section. `config.Store.Load` returns shared + local. Edits to known
machine-specific fields land in the local layer by default; `--local` sends
any edit there. The machine-specific fields are:

- `accounts.*.config_dir`, `accounts.*.home`
- `policy.serve.*`
- the argv of a hook that points outside the relevo config root

The `sync` section is never a shared doc.

### 4.2 Gates, availability, latency

**Gates.** The kv `ledger` document splits into one key per subject. Clearing
a gate deletes its key. `until` stays an absolute UTC time. A gate write
queues an immediate sync attempt instead of waiting for the debounce.

**Availability and latency.** Availability events and latency samples move
from whole-document kv values into two shared history tables in `relevo.db`,
`availability_event` and `latency_sample`, keyed by ULID with `origin`. Each
machine writes only its own rows, and readers combine all origins. The
30-day retention runs on every machine by age, and the exporter does not emit
those age-based deletes.

### 4.3 Files config points at

- **Native agent files.** A native `agents` entry carries the contents of its
  harness file per harness kind.
- **Hook scripts.** A `hooks` entry carries the contents and executable bit of
  every script under `<config root>/hooks/`. Its argv is stored relative to
  the relevo config root and resolved through `userConfigRoot()`.

On apply, the importer writes the file if it's missing or still matches the
hash it last wrote. A file edited by hand is kept, and `relevo doctor`
reports it. Applying an `agents` change re-renders that agent's harness files
at once, through the same path `refreshRoles` uses at daemon start.

## 5. Signing and secrets (steps 3 and 5)

**Key.** The passphrase is turned into a 32-byte key with Argon2id, using
`kdf_salt` from the remote `meta` table; `key_check` in `meta` rejects a
wrong passphrase. The key is stored locally as the machine-only secret
`sync.key`. It's entered at `db sync enable`, or later with
`db sync unlock`. Until it's present, the machine syncs history unsigned
under the merged rules, imports no shared docs, and status says "locked".

**Signing (step 3).**
- Every entry carries `mac = HMAC-SHA256(key, canonical entry)`.
- `meta` records, per origin, the first seq that was signed.
- An importer refuses an unsigned or badly signed entry at or above that
  seq, holds the origin, and latches with the reason.
- Shared docs and takeovers are only ever accepted signed.

**Secrets (step 5).**
- **Allow-list:** `typesafe`, `client.key` (one identity for all the owner's
  machines; revoking it on a server revokes them all) and `r2.*`. Any other
  name never syncs.
- **Encryption:** a value travels as XChaCha20-Poly1305 ciphertext, with the
  secret's name bound in as associated data.
- **Rotation:** `db sync rekey` re-encrypts every value under a new
  passphrase and rewrites `kdf_salt` and `key_check`; compaction then drops
  the old ciphertexts.
- **Lost passphrase:** if no machine holds the key any more, the synced
  secrets are unreadable. Every machine's local copies are unaffected.

## 6. Bodies in R2 (step 1)

**Which values.** `round_file.body`, `transcript.record_json` and
`transcript.rendered`, when the stored value (after the existing zstd codec)
is over 4 KiB.

**Export.**
1. The exporter writes the stored bytes to the staging folder
   `<state>/sync-blobs/`.
2. The worker uploads them to `<origin>/<sha256>`, skipping the upload when
   the object already exists.
3. Only then is the entry appended, with
   `{"$ref": {"sha256": …, "bytes": …, "codec": …}}` in place of the value.
   An entry can never point at a missing object.

**Import.**
1. The worker fetches the batch's missing objects into staging, a few at a
   time.
2. The importer checks each sha256, then applies the batch in one
   transaction as today.

A failed fetch holds the origin until the next attempt. A missing object
(404) latches sync with the binding, round and file named. Bodies never
cross the JSON pipe.

**Interface and credentials.** R2 sits behind a `BlobStore` interface (`Put`,
`Get`, `Has`, `List`, `Delete`) with an in-memory fake; CI has no network. The
S3 client is a new dependency, chosen in step 1's plan. The credentials are
`r2.endpoint`, `r2.bucket`, `r2.key_id` and `r2.secret`: machine-local secrets
until step 5 lets them travel.

**Compression.** Sealing moves from zstd level 3 to level 9: about 12%
smaller, for about 30 ms per MB.

**Cleanup.** Weekly, each machine deletes objects under its own prefix that
no row on this machine references and that are older than 30 days.

## 7. Cost controls and joining (step 2)

**Counting.**
- The worker reports bytes per call, both exact: R2 from object sizes, Turso
  from the deltas of `NetworkSentBytes` and `NetworkReceivedBytes` in
  tursogo's `TursoSyncDbStats` (v0.8.1, `driver_sync.go:97`), which the
  worker discards today.
- The daemon adds them to kv `sync.bytes.<YYYY-MM>` (`turso_push`,
  `turso_pull`, `r2_put`, `r2_get`).
- `db sync status` shows them against quotas from new `sync` settings, with
  defaults of 10 GB of Turso sync traffic, 9 GB of Turso storage and 10 GB of
  R2 storage.

**Join guard.** Before bootstrapping, `enable` estimates the join's bytes
from the remote's log size and the bucket total. If that exceeds what's left
of the month, it says so and asks. A Turso refusal at quota latches as "quota
reached" and isn't retried.

**Compaction.**
- **Marks.** A remote `marks(reader, origin, seq, at)` table holds each
  machine's import marks, which it publishes after every import (it writes
  only its own rows).
- **Floor.** For origin O, the compaction floor is the minimum mark for O
  over readers whose `at` is within 30 days.
- **What's dropped.** O drops its own log entries below the floor that a
  later entry for the same key supersedes, plus deletes below the floor that
  are the latest for their key. The floor is recorded in `meta`.
- **When it runs.** Weekly, or when O's log grew 10% since the last run.
- **A reader that falls behind.** A reader whose mark is below the floor
  (silent for over 30 days) detects the gap and re-imports O from zero.
  Upserts are idempotent.

**Join, new.**
1. Bootstrap the small replica.
2. Import every origin, fetching bodies from R2.
3. Reconcile own rows: upload any body R2 lacks, then append entries.
4. Publish marks.

**Also in step 2.**
- A one-time pass drops the laptop's dead `turso_cdc` (1,688 MiB), after a
  backup.
- The spec'd `db sync reconcile --dry-run` verb gets built.
- `idle_seconds` and `backlog_threshold` are wired up, or removed.

## 8. A new machine (step 5)

On a machine that already syncs, `relevo db sync invite` prints a one-line
join code holding the Turso URL and token. On the new machine:
`relevo db sync enable --join <code>`, then the passphrase. The R2 key,
`client.key` and every other allow-listed secret arrive encrypted. Agents are
rendered and hook scripts written. `relevo doctor` lists the harness accounts
that still need a login.

## 9. Remote and protocol changes

**Log format 2.** `log` gains `kind` (`row` | `doc` | `takeover`), `hlc`,
`after_origin`, `after_seq` and `mac`. New table: `marks`. New `meta` keys:
`kdf_salt`, `key_check`, `signed_from.<origin>`, `floor.<origin>`. DDL runs
on a sync connection, so the engine teaches the remote (as in the earlier
spec, D7). A binary that reads format 2 refuses format 3 with "relevo on this
machine is older than …".

**Worker protocol version 2.** New verbs: `put_blob` and `get_blob` (staging
paths, not bytes), `publish_marks`, `compact`. `stats` gains byte counters.
The daemon refuses a worker of the wrong version.

## 10. Failure modes

| Failure | Behaviour |
|---|---|
| R2 unreachable | Exports that need an upload wait; imports that need a body hold that origin; everything else proceeds |
| Object missing (404) | Latch, naming the binding, round and file |
| Wrong or missing passphrase | Shared docs, secrets and takeovers don't import; history still syncs; status says "locked" |
| Bad signature | Refuse, hold that origin, latch with the entry named |
| Takeover while the old writer still runs | Its later entries for the binding are fenced everywhere; it reports what was lost when it next imports |
| `enable --as` on a live identity | Refused (activity in the last 24 h) |
| Clock skew | Shared docs stay consistent (the hybrid clock); history ordering never uses clocks |
| Quota reached | Latch "quota reached", no retry loop |
| Reader below the compaction floor | Re-import that origin from zero |

## 11. Testing

- **No network in CI.** `MemTransport` gains kinds, stamps, `after`, marks and
  compaction; `BlobStore` has an in-memory fake. Nothing in `cmd/relevo`
  spawns a worker or reaches the network; rules are tested as pure functions.
- **Convergence tests** (extending `converge_test.go`): three machines with
  concurrent config edits converge to one state, with the loser in
  `config_revision`; hand-over, then the new writer's entries, in every
  pull order; a takeover fences the old writer; compaction followed by a
  rejoin; restore from a backup.
- **Mutation targets**, each pinned by a named test:
  - the writer check in the importer;
  - the `after` hold;
  - the newest-wins comparison;
  - signature verification;
  - upload-before-append ordering;
  - the sha256 check on fetch;
  - the compaction floor.
- **Live, per step,** in an env sandbox built from the branch before merge:
  a real Turso database and a real R2 test bucket. Two sandboxes in both
  directions, plus each step's own scenario (a hand-over, a config edit, a
  new machine from zero).

## 12. Build order

Each step is one PR with sandbox evidence before merge.

1. **Bodies in R2.** `BlobStore`, `$ref`, upload-then-append, fetch-and-check,
   zstd 9 at seal, byte counting. *Done when* a two-sandbox join moves bodies
   through R2, and the Turso bytes match the estimate.
2. **Compaction and joining.** Marks, compaction, the join guard, the
   `turso_cdc` drop, `reconcile --dry-run`. *Then sync goes on for history on
   all three machines, through a tagged release.*
3. **Signed shared docs.** Passphrase key, signing, hybrid clock,
   `shared_outbox`, config layers, gates, consents, the files config points
   at, agent re-render.
4. **Availability and latency** as history tables.
5. **Secrets and the new-machine flow.** Allow-list encryption, unlock,
   rekey, `invite` and `--join`.
6. **Ownership.** The `writer` column, `handover`, `takeover`,
   `enable --as`.

## 13. Settled and open questions

Settled 2026-10-10:
- R2 tokens scope to a bucket, not a prefix. The R2 item permissions take
  `com.cloudflare.edge.r2.bucket` resources only, and a bucket-scoped key got
  403 on another bucket. All machines therefore share one bucket key, which
  is why `r2.*` is on the secrets allow-list.
- Turso transfer bytes are exact (§7).
- The 4 KiB cut-off was measured on the laptop's round files (45% of rows,
  97% of bytes) and zen's transcripts (479 of 9,981 rows over 4 KiB, 21.3 of
  25.4 MiB).

Test resources for the sandboxes: Turso database `relevo-sync-test` (group
`relevo`) and R2 bucket `relevo-sync-test` (WEUR) with a key limited to it.
Credentials live outside the repo in a 0600 folder on the laptop.

Open:
- Hand-over of a whole chain: designed after step 6.
