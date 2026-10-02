# Scopes: what may leave this machine

The database is one file per machine today and is meant to become one shared
file. In a shared file every row has to say which installation wrote it, and
every table and key has to say whether it may travel at all. This document is
that classification, written next to the migrations that create the tables.
It is proposal item 4 of the shared-database work; which file each scope lands
in, and the decision for each per-user config section, belong to the sync round.

Migrations 001-016 are the whole series this classifies. Migration 015 adds
columns to `binding_record` and no table, so every table in the series is
listed below. The rules a migration must follow are in
[`README.md`](README.md); the file that created a table is its history.

## Shared history

Rows another installation may read, and must be able to attribute to a
machine. Each row carries `origin`, the ULID of the installation that wrote
it, from migration 014.

| table | what it is |
|---|---|
| `binding_record` | A binding as `internal/store` reads and writes it: the system of record. `origin` is a live key column, and the live uniqueness key is `(origin, owner, name)`. |
| `chains` | A chain and its members. `origin` is a live key column, and `(origin, owner, name)` is its natural key. |
| `binding_event` | A binding record's append-only log. No column: it inherits origin through `record_id`. |
| `chain_event` | A chain's append-only trace. No column: it inherits origin through `chain_id`. |
| `chain_member` | A chain's members: the binding that fills each actor's slot, in order. No column: it inherits origin through `chain_id`. |
| `chain_check` | A check step's run for a chain: its command, result and log. No column: it inherits origin through `chain_id`; its `pid` names a process on the machine that wrote the row. |
| `round_file` | A closed round's sealed files. No column: it inherits origin through `record_id`. |
| `installation` | The display label of every installation seen, so another machine can name the writer of a row. A projection of the installation file that sits beside the database; the file is authoritative and never syncs. |
| mirror `repo` | A git repository relevo has seen. `origin` is part of the natural keys `(origin, origin_url)` and `(origin, common_dir)`. |
| mirror `mastermind` | A MasterMind session relevo has seen (created as `planner` by migration 001, renamed by 008). `origin` is part of the natural key `(origin, harness_kind, session_id)`. |
| mirror `binding` | A binding as the history and stats readers see it. `origin` is part of the natural key `(origin, name, created_at)`. |
| mirror `round` | One round of a mirror binding. No column: it inherits origin through `binding_id`. |
| mirror `event` | One event of a mirror binding. No column: it inherits origin through `binding_id`. |
| mirror `artifact` | One captured file of a mirror round. No column: it inherits origin through `round_id`. |
| mirror `transcript` | A round's or a session's stream records. No column: it inherits origin through `owner_id`, which is a round or a mastermind id. |

A parent id is a ULID, so inheritance stays unambiguous when two installations
hold rows with the same name. Child rows get no origin column of their own,
and no reader may assume one.

One row per installation in `repo` is acceptable until sync merges two
installations' history; rows are never merged across origins.

## Machine-local, never synced

Values that mean something only on the machine that wrote them: pids, absolute
paths, offsets, listen addresses, secrets, and this machine's own identity.
These are the destination for the second, local-only database file.

- `kv` keys and namespaces:
  - `daemon`: the running daemon's pid, exe and start time.
  - `serve.daemon`: the serve daemon's pointer (pid, root, exe).
  - `planner/*`: the MasterMind registry rows written before migration 008
    renamed the prefix.
  - `mastermind/*`: the same registry rows under their current prefix.
  - `claim/*`: a channel claim, keyed by the claiming process.
  - `hooks.log`: the hook run log.
  - `ledger`, `availability`, `latency`: this machine's gates, availability
    history and candidate latency samples.
  - `serve.*` (`serve.clients`, `serve.ui`, `serve.ledger`): serve-wide state
    read and written on the server host.
  - `ui`: the cockpit's remembered query and sort column (`serve.ui` on a
    server).
  - `agents-manifest`: the checksums of the agent definitions relevo wrote.
  - `release-check`: the last release check.
  - `planner.pruned_at`: when the MasterMind registry was last pruned.
  - `ingested.archive.*`: one entry per archived binding already swept into
    the mirror, keyed by record id.
  - `mirror-dedupe.v1` / `mirror-dedupe.v2`: the run-once markers of the
    mirror dedupe pass (`mirror-dedupe.v2` is the key current binaries write;
    `v1` is the earlier spelling).
  - `zstd-compress.v1`: the run-once marker of the history compression pass.
  - `origin-backfill.v1`: the run-once marker of the origin backfill pass.
- `secret`: the `client.key` remote-builder identity, the `typesafe`
  classifier token, and `serve.tls.*` (the server's certificate and key).
- `session_consent`: one session's own consent answer and last status token,
  keyed by `(harness_kind, session_id)`.
- `ingest_cursor`: an ingest source's byte offset and last hashes, keyed by an
  absolute path.
- `config_import`: the watch-list of config files already imported, keyed by
  an absolute source path.
- the installation file, `<state root>/installation.json`: this installation's
  id, label and creation stamp.

The installation id is the value every shared row's `origin` holds, so it must
never sync: two machines sharing one id could not tell each other's rows apart,
and a database copy, backup or template would clone the id onto another
machine. That is why it lives in a file beside the database and not in `kv`.

## Per-user config, synced deliberately or not at all

Configuration describes what this user's machine can run: its candidates,
actors, policy, roles, prices and servers name local binaries and harnesses.
Another machine's are not the same, so a section syncs only if the sync round
decides it should. Current leaning, not a decision: machine-local, in the
second file.

- `config_doc` rows: one row per section (`candidates`, `actors`, `policy`,
  `roles`, `prices`, `servers`).
- `config_revision`: the append-only revision log of those sections.
- `config_meta`: the singleton config version counter.

## Reading a path that belongs to another machine

Paths stay absolute and are never rewritten. A path recorded on machine A
stays machine A's path; an absolute path on B is meaningless, and rewriting it
would silently point at the wrong file. So every read pairs the value with the
`origin` of the row it came from, and when that origin is not the local
installation, a surface shows the value with that installation's label and
never opens it.

Path-valued columns:

- `binding.cwd`, `binding.worktree`, `binding.archive_path`
- `binding_record.cwd`
- `repo.common_dir`
- `planner.transcript_locator` (now `mastermind.transcript_locator`): the column is retained but no longer written or read.
- `event.path`
- `ingest_cursor.source`
- `config_import.source_path`

Path-valued fields inside `record_json`:

- `cwd`, `worktree`, `repo_ref.common_dir`, `builder.log_path`,
  `serve.bare_repo`

Path-valued `kv` values:

- `daemon.exe`, `serve.daemon.root`

This pass adds no code for the rule; it records it here and in the plan.
