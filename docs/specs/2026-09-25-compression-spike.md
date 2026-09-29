# Spike: compressing the relevo record before cloud sync (#475)

Status: spike report, 2026-09-25. Feeds #473 (cloud sync). Series: #466 (driver) →
#472 (row origin) → #475 (this spike) → #473 (sync). No production code was written.

## Method

- **Data:** a `.backup` snapshot of the dev laptop's live `relevo.db` (445 MB,
  2026-09-25 12:02), with the `secret` table emptied. It covers 478 rounds between
  2026-09-05 and 2026-09-25.
- **Benchmark:** a throwaway Go program, pure Go and `CGO_ENABLED=0`, using
  `modernc.org/sqlite` v1.59.0 and `github.com/klauspost/compress/zstd` v1.18.4.
  - It compresses **each row on its own**, as a relevo column codec would, and checks
    the round trip.
  - Speeds are single-core, measured on an Intel Core Ultra 7 155H.
- **zstd dictionary:** 110 KB, trained with `zstd --train` on 4,000 random
  `transcript` rows. The training rows are part of the measured set, so the dictionary
  figures are slightly optimistic.
- **Conversion trials:** run on copies of the snapshot. Each rewrites the columns in
  batches of 200 rows per transaction, then runs `wal_checkpoint(TRUNCATE)` and
  `VACUUM`.
- **Turso:** code read at tag `v0.8.0-pre.12` plus docs.turso.tech and turso.tech/pricing.
  The key claims were checked against the source: the sdk-kit's `tables_ignore`, and
  the sync client always requesting `Raw` page encoding.

## Where the bytes are

| table | MB (dbstat) | what it holds |
|---|---|---|
| `transcript` | 234 | planner transcripts: 44,708 rows, 132 MB; builder streams: 32,361 rows, 72 MB; `rendered`: 4.5 MB |
| `round_file` | 193 | `builder.jsonl` 157 MB (233), `diff.patch` 21.5 MB (407), `plan.md` 5.0, `report.md` 3.9, `builder.log` 3.7, `drift.patch` 2.9 |
| everything else | < 5 | |

## 1. How much each row type shrinks

Per-row results, stored MB (ratio):

| kind | raw MB | zstd fastest | zstd default | zstd best | gzip-6 | zstd default + dict |
|---|---|---|---|---|---|---|
| `round_file` `builder.jsonl` | 157.0 | 25.4 (6.2×) | 23.3 (6.7×) | 20.5 (7.7×) | 29.2 (5.4×) | 22.8 (6.9×) |
| `transcript` planner | 132.5 | 72.2 (1.8×) | 66.0 (2.0×) | 61.9 (2.1×) | 67.9 (2.0×) | **51.4 (2.6×)** |
| `transcript` round | 71.5 | 24.5 (2.9×) | 23.3 (3.1×) | 22.0 (3.3×) | 23.4 (3.1×) | 17.9 (4.0×) |
| `round_file` `diff.patch` | 21.5 | 5.7 (3.8×) | 5.3 (4.0×) | 4.8 (4.5×) | 5.3 (4.0×) | 5.1 (4.2×) |
| `plan.md` / `report.md` | 8.9 | 3.8 (2.3×) | 3.6 (2.4×) | 3.5 (2.5×) | 3.5 (2.5×) | 3.3 (2.7×) |
| **all rows** | **397.9** | 133.1 (3.0×) | **123.0 (3.2×)** | 114.0 (3.5×) | 130.8 (3.0×) | **102.1 (3.9×)** |

- **Big stream files compress well; small rows don't.** Compressing a table as one
  stream (the numbers first posted in #475) gives 4–7×; per row it drops to 2–3× for
  transcripts, whose rows average about 3 KB.
- **A dictionary is the only thing that helps the small rows.** It takes planner
  transcripts from 2.0× to 2.6×, and the whole database from 123 MB to 102 MB.
- **zstd best is not worth it:** it saves 7% over default at a sixth of the speed.

## 2. CPU cost (pure Go, one core)

| codec | encode MB/s | decode MB/s |
|---|---|---|
| zstd fastest | 240 | 536 |
| zstd default | 186 | 538 |
| zstd better | 129 | 516 |
| zstd best | 31 | 526 |
| gzip-6 (stdlib) | 33 | 157 |
| zstd default + dict | 63 | 422 |

- **A round costs milliseconds.** At the current pace a round adds about 1.1 MB. With
  zstd default that is about 6 ms to compress when the round is sealed, and about 2 ms
  to read it back in the cockpit. Against a 2 s daemon tick that is noise.
- **The dictionary makes encoding about 3× slower** (63 MB/s), which is still under a
  millisecond for a 3 KB transcript row.
- **gzip loses on every axis** and has no reason to be chosen.

## 3. What reads inside these columns

Nothing in SQL looks inside `round_file.body`, `transcript.record_json` or
`rendered`, `artifact.text`, `event.entry_json`, `binding_event.entry_json` or
`binding_record.record_json`. There is no `LIKE`, `json_*`, `instr`, `substr` or
`length()` on any of them. Every `WHERE`, `ORDER BY` and aggregate uses ids, seq,
names, kind, owner or `archived_at`.

- **Each column has one read point and a few write points, all in `internal/db`:**
  - `round_file.body`: `RoundFileGet` (`roundfile.go:55`) and `RoundFilePut` (`:39`).
  - `transcript`: `listTranscript` (`read.go:671`) and `AppendTranscript` (`write.go:469`).
  - `artifact.text`: `getArtifact` (`read.go:644`) and `UpsertArtifact`.
  - `binding_record.record_json`: `scanRecord` (`record.go:87`) and `RecordPut`.

  A codec added at those points is invisible to every caller.
- **Places that must keep using the uncompressed values:**
  - `ingest/dedupe.go:194` compares `sha256` and `len(body)` against `artifact.bytes`.
  - `ingest/dedupe.go:219-226` compares transcript rows.
  - `ingest/ingest.go:577` compares artifact text.

  All of them compare after decoding, so hashes and sizes must stay computed on the
  plaintext. `round_file.bytes` and `artifact.bytes` stay plaintext sizes.
- **`RewritePathPrefix`** (`rewrite.go`) only matches values that start with an old
  root. It never matched JSON (which starts with `{`), and it will simply skip
  compressed blobs. Nothing is lost.
- **No search looks inside them:** `histq`, the cockpit `/` filter and
  `relevo history` all match names, repos, actors and state.
- **No external `sqlite3` reads relevo.db.** Every `sqlite3` call targets opencode's
  database.
- **Column types:** `round_file.body` is already declared `BLOB`. The rest are
  declared `TEXT`, which SQLite (and Turso) still let hold a BLOB value, so no table
  rebuild is needed.

## 4. Where to compress

| option | verdict | why |
|---|---|---|
| (a) in relevo, per row | **do this** | the only option that shrinks local disk, pushes, pulls and Cloud storage today |
| (b) inside Turso | not available | no page or column compression, no `compress()` SQL function, no loadable SQLite extensions (COMPAT.md:363, tursodatabase/turso#8 open). The experimental page codec (PR #8095) must preserve the page size: "not an interface for variable-size compression" |
| (c) on the sync wire | not available | see below |
| (d) don't sync bulky rows | not possible per table from Go | see below |

**Why not on the sync wire (c):**
- **Push** sends logical changes: rows from the CDC table (`turso_cdc`, mode `full`)
  replayed as SQL over Hrana JSON to `/v2/pipeline`. An INSERT sends the whole row, so
  a BLOB travels as base64 (+33%). An UPDATE sends only the changed columns.
- **Pull** receives raw 4 KB pages. The protocol defines a zstd page encoding, but the
  client always asks for `Raw` (`database_sync_operations.rs:1897, 2065, 2190, 3380`),
  and its decoder returns "zstd encoding is not supported" (`:3586`).
- Neither push nor pull is compressed over HTTP. Pull requests' `accept-encoding`
  header overrides Go's automatic gzip.

**Why not by leaving bulky rows out of sync (d):**
- The engine has `tables_ignore`, which is push-only, but the Go sdk-kit hard-codes it
  empty (`sync/sdk-kit/src/rsapi.rs:254`).
- Partial sync (experimental) works on pages, not tables: pages are fetched lazily on
  first read. It has open bugs on macOS/APFS (tursodatabase/turso#7841, #7843) and on
  bind mounts (#8301).
- Keeping bulk local-only therefore means a **second database file**. That is a design
  question for #473, not a compression one.

**CDC side effect:** `full` mode keeps before and after images of each changed row in
`turso_cdc` until sync, so uncompressed bodies would be held twice locally. Compressing
in relevo shrinks that too.

## 5. The duplication was real, and #476 removes it

- **#422's one-time cleanup already ran** on this database (kv `mirror-dedupe.v1`,
  2026-09-24). It deleted 15,127 builder-stream transcript rows of 63 rounds and kept
  149 rounds, whose rows it could not re-derive exactly.
- **The rows it kept were still mostly duplicates.** Of 200 random remaining
  `owner_kind = 'round'` rows, 187 (93.5%) appeared byte for byte inside some
  `round_file` `builder.jsonl`.
- **These rows no longer grow.** #423 stopped writing them (2026-09-24). The only
  production `AppendTranscript` caller left writes planner transcripts
  (`ingest/ingest.go:459`), so this was a one-time 86 MB, not a rate.
- **Resolved by #476 (PR #481, `mirror-dedupe.v2`).** A second one-time pass proves
  rows per row against the sealed files:
  - a row with record JSON must be a line of `builder.jsonl`, directly or after the
    relay→relevo path rewrite that `relevo migrate` applied to the files but never
    to these rows;
  - a row with no record JSON must be blank, or its rendered text must be a line of
    `builder.log`.
- **Rehearsed on a copy of the dev laptop's database:** 127 rounds and 31,749 rows
  planned (811 rows matched only after the rewrite), 21 rounds kept, 13 bindings
  unmapped. It runs once at the next daemon start, after a backup.
- **`transcript.rendered`** is derived from `record_json`, but it is only 4.5 MB.
  Keep it, uncompressed, as the cheap display column.

## 6. Growth per week

| week (Mon start) | rounds | `round_file` MB | builder transcript MB | planner transcript MB |
|---|---|---|---|---|
| 2026-W35 | 27 | 0 | 0 | 0 |
| 2026-W36 | 191 | 8 | 0 | 0 |
| 2026-W37 | 31 | 34 | 29 | 20 |
| 2026-W38 (Mon–Thu) | 229 | 150 | 43 (legacy) | 111 |

- **The current pace is about 1.1 MB per round:** 0.65 MB of round files plus 0.5 MB
  of planner transcript.
- **Raw growth is about 300 MB a week,** or 1.2 GB a month, at W38's pace.
- **After compression it is about 100 MB a week:** zstd default, or about 90 MB with a
  dictionary.
- **Planner transcripts become most of what is left,** because they are both the
  biggest growth and the worst compressors. Spec
  `2026-09-20-persistence-design.md` §3.5 said "Planner transcripts are copied … Size
  is a later optimisation". This is that moment: the options are a dictionary, or
  storing a locator plus a cursor instead of a copy.

**Sync budget:**
- The Turso Cloud Free plan has 5 GB of storage and 3 GB of sync transfer a month.
- **Uncompressed:** a 445 MB bootstrap plus about 1.6 GB a month of pushes (base64
  adds a third) spends half the transfer allowance.
- **Compressed and deduplicated:** a 124 MB bootstrap plus about 0.55 GB a month.
- How Turso counts "syncs" bytes, and whether a bootstrap counts, is not documented.

## 7. Converting existing rows

Trials on copies of the 445 MB snapshot:

| conversion | time | file after `VACUUM` |
|---|---|---|
| `VACUUM` only | 2.4 s | 445.0 MB |
| drop builder-stream transcript rows (dedupe) | 2.0 s | 359.6 MB |
| zstd default on `round_file.body` and `transcript.record_json` | 12.2 s (1.9 s + 9.1 s + 1.2 s vacuum) | **157.9 MB** |
| dedupe + zstd | 14.1 s | **123.6 MB** |

- **It is fast enough to run once, in the background.** A whole-database conversion
  is about 15 s of work in 200-row transactions. The daemon can do it a few batches
  per tick without holding the write lock for long.
- **Schema shape:**
  - add `codec INTEGER NOT NULL DEFAULT 0` to each compressed table, with 0 = raw,
    1 = zstd, 2 = zstd with dictionary *n*. `ALTER TABLE ADD COLUMN` only, which is
    Turso-safe;
  - store the compressed value as a BLOB in the existing column;
  - dictionaries, if used, live in their own table keyed by id and are never changed
    once a row uses them.
- **Older relevo is the hard part.** Today a newer schema is opened for reading only
  (#372 §4.5), so an older relevo would read a compressed body as text and show
  garbage. The migration that introduces `codec` must also stop an older binary from
  reading those columns. Options: bump a "minimum reader version" that older binaries
  already check (they don't today), or accept that this migration drops support for
  downgrading and say so in the release notes.
- **`VACUUM` needs a note.** Turso needs the experimental `vacuum` flag for in-place
  `VACUUM` (#466). Run the space-reclaiming `VACUUM` under modernc, before the driver
  swap.

## Side finding

While measuring, the live database's WAL file was **397 MB, next to a 467 MB database**,
and the same size again after the spike.

- **Possibly harmless:** SQLite does not shrink the WAL file after a checkpoint unless
  `journal_size_limit` is set or a `TRUNCATE` checkpoint runs, so this may only be a
  high-water mark.
- **Possibly a real problem:** with 10 processes holding connections, a long-lived
  reader may keep checkpoints from finishing.

Either way it is 0.4 GB of disk that compression does not touch. Noted here only; no
issue was filed.

## Recommendation

1. **Compress in relevo, per row, with zstd default** behind a `codec` column, at the
   single read and write points in `internal/db` (§3). Measured result: 445 MB → 158 MB,
   about 6 ms per round.
2. **Finish the #422 dedupe first.** Done in #476 (PR #481): about 445 MB → 360 MB
   once it runs; 124 MB together with (1).
3. **Decide separately about a dictionary for planner transcripts,** measured at a
   further −17% overall. The cost is a dictionary table and a rule that a dictionary is
   never changed once used. Doing it together with the §3.5 locator-vs-copy question is
   the better order.
4. **Do all of this before the Turso swap and before sync,** because Turso compresses
   nothing on disk or on the wire, and pushes send whole rows as JSON.
5. **In #473,** plan on a second, local-only database file for anything that must not
   be pushed. Go cannot use `tables_ignore`.
