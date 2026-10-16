# Scopes: what may leave this machine

The database is one file per machine today and is meant to become one shared
file. In a shared file every row has to say which installation wrote it, and
every table and key has to say whether it may travel at all. This document is
that classification, written next to the migrations that create the tables.
It is proposal item 4 of the shared-database work; which file each scope lands
in, and the decision for each per-user config section, belong to the sync round.

Migrations 001-021 are the whole series this classifies. A migration that
adds only columns or indexes adds no table, so every table in the series is
listed below. Migration 022 is the first outside it: it adds `sync_outbox` and
the triggers, which are machine-local like the sections below, and it changes
no table's scope. Migration 023 adds `sync_import_mark`, machine-local like the
outbox, and changes no table's scope either. Migrations 025 onward add the
documents' own bookkeeping, machine-local like the outbox for the same reason: a
row there is about what this machine has sent, seen or parked, and two machines
sharing one would each believe the other had done it. The documents themselves
are not tables and are listed under their own heading below. Migration 029 adds
`availability_event` and `latency_sample`, which are shared like the mirror
tables and listed in the table below; the `availability` and `latency` kv
documents they replaced were migrated into them and deleted, and the local marker
`sync.history_rows.v1` records that the pass ran. The rules a
migration must follow are in
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
| `availability_event` | One availability observation: a ledger entry mirrored into the 30-day window the ledger itself does not keep. `origin` is a root column of its own, and `at_ms` is the observation's time as Unix milliseconds in UTC rather than text, because today's events carry mixed offsets and a text comparison misorders them. Append-only: a row is never updated and is deleted only by age, so it carries an insert trigger alone and the age delete is never exported. |
| `latency_sample` | One probe of one candidate: time to first output, whole-run time, and the error when it failed. `host` is kept, because a latency figure is only readable next to the machine that produced it. Root and append-only, for the same reasons as `availability_event`. |

Both history tables are written and read now. The `availability` and `latency`
kv documents they replaced were migrated into these tables once, behind the
local marker `sync.history_rows.v1`, and then deleted, so the tables are the
only place either window lives. Every display read combines all origins, which
is what a panel wants and what the two documents could never have given it.
Behaviour-deciding reads stay on this machine's own rows and on the ledger
whatever the readers become: peers' outages and latencies must never gate a pick
on this machine, which is why a peer having them is a display concern and not a
scheduling one.

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
  - `ledger`: this machine's own view of its gates, after the entries that
    arrived from peers were applied into it. The row itself never syncs: the
    gates document below is what carries a gate, and it carries one key per
    ledger subject rather than the row. A `spawn_failed` entry is in this row
    and in no document -- it says this binary could not start that candidate
    here, which is true of this host alone -- and an entry this build cannot
    read stays here too, for the same reason the ledger keeps it raw.
  - `sync.history_rows.v1`: the marker recording that the `availability` and
    `latency` kv documents moved into `availability_event` and `latency_sample`
    and were deleted. Local, like every other run-once marker, because a
    machine's documents are its own and there is nothing for a peer to move.
  - `serve.*` (`serve.clients`, `serve.ui`, `serve.ledger`): serve-wide state
    read and written on the server host.
  - `ui`: the cockpit's remembered query and sort column (`serve.ui` on a
    server).
  - `agents-manifest`: the checksums of the agent definitions relevo wrote.
  - `release-check`: the last release check.
  - `planner.pruned_at`: when the MasterMind registry was last pruned.
  - `history.pruned_at`: when the aged availability and latency rows were
    last deleted. Each machine prunes its own file on its own clock.
  - `ingested.archive.*`: one entry per archived binding already swept into
    the mirror, keyed by record id.
  - `mirror-dedupe.v1` / `mirror-dedupe.v2`: the run-once markers of the
    mirror dedupe pass (`mirror-dedupe.v2` is the key current binaries write;
    `v1` is the earlier spelling).
  - `zstd-compress.v1`: the run-once marker of the history compression pass.
  - `origin-backfill.v1`: the run-once marker of the origin backfill pass.
- `secret`: the `client.key` remote-builder identity, the `typesafe`
  classifier token, the `r2.*` credentials, `turso.token`, `sync.key` and
  `serve.tls.*` (the server's certificate and key). Six names travel, in the
  `secrets` document below: `typesafe`, `client.key`, `r2.endpoint`,
  `r2.bucket`, `r2.key_id` and `r2.secret`. The rest -- `turso.token`,
  `sync.key`, `serve.tls.*` and any name not on that list -- stay
  machine-local: they are refused on import and never exported.
- `session_consent`: one session's own consent answer and last status token,
  keyed by `(harness_kind, session_id)`. The answer and its time travel, in the
  `session_consent` document below; the told baseline stays here, because it says
  what this machine has already shown this session on this host, which is true
  of no other machine and would tell a peer about a delivery it never made.
- `ingest_cursor`: an ingest source's byte offset and last hashes, keyed by an
  absolute path.
- `config_import`: the watch-list of config files already imported, keyed by
  an absolute source path.
- `sync_outbox`: the local change log the shared tables' triggers append to,
  keyed by `seq`. The rows are about shared history but they are this
  machine's own record of it, and the log itself is what another machine
  receives, so it does not sync: a row travels in the log, not as one.
- `sync_import_mark`: how far this machine has applied each other
  installation's entries, one row per origin. It is progress rather than
  history, so it carries no trigger and stays here; two machines sharing a
  mark would each believe the other had applied the entries between them.
- `sync_epoch_cut`: the highest epoch cut this machine has verified for each
  origin, one row per origin. Like `sync_signed_from`, it records what this
  machine has checked, so it stays here; another installation sharing the
  table would take this machine's verification on as its own, and a cut is
  exactly the fact a remote that lost its key must not be able to hand over.
- the installation file, `<state root>/installation.json`: this installation's
  id, label and creation stamp.

The installation id is the value every shared row's `origin` holds, so it must
never sync: two machines sharing one id could not tell each other's rows apart,
and a database copy, backup or template would clone the id onto another
machine. That is why it lives in a file beside the database and not in `kv`.

## Shared documents, synced per key

A document is not a table: it travels as ordinary log entries naming `doc:<name>`
and one key each, and its newest write per key wins. Every doc's write stamps
live in `config_write_stamp` under `<doc>/<key>`, so a document stamp can never
be read as a config section's -- no section below is named after a document.

| document | what it is | what never leaves |
|---|---|---|
| `config` | The shared config layer: one key per candidate, agent, actor, account, policy, role, price, server, hook set and workflow, plus one key per section that has no items. The `hooks` key carries the hook scripts themselves -- each one's contents and executable bit -- beside the argv rows that name them, root-relative, so a peer receives the hook and not only the name of one. A native `agents` key carries each of its kinds' harness file beside the entry that names it, so a peer receives the agent and not only a pointer to a definition it may never have had. | The `sync` section and the machine-local layer: both say what this machine can run, and neither is the owner's answer to hand out. For `hooks` that means an argv row naming a file outside `<config root>/hooks/`: it stays in `config_local`, because a path elsewhere on this filesystem means nothing on another one. A hook script or a native agent definition a person edited on disk is also kept rather than overwritten -- the item lands, the file does not, and `relevo doctor` names the one kept. |
| `gates` | One key per ledger subject, holding that subject's live rate-limit entries. A rate limit is the provider's quota, so every machine gating that provider gates the same candidates. | `spawn_failed` entries, which are about this host's binaries failing to start; entries whose kind or source this build cannot read; and an expiry that no write has yet pruned. |
| `consent` | One key per remote URL, holding the repository's answer to "may relevo register this repo's sessions as MasterMinds?". It reads and writes only the row this installation wrote, and the read scopes to it the same way: the `repo` rows themselves sync through the row log, so a machine holding several installations' rows for one URL holds several answers and only its own answers for it. | A repository with no remote URL, which no other machine can be asked about, and an answer that has been cleared: a clear is the key's absence, so it travels as a tombstone rather than as a value. A peer's copy of `mastermind_consent` in its own `repo` row means nothing here -- nothing reads it for the decision. |
| `session_consent` | One key per `harness_kind/session_id`, holding the answer that session gave on its own rather than inheriting from the repository. A session id is not a repository, so a peer can hand over an answer for a session this machine has never seen and the row is simply created. | The told baseline and its time, which are this machine's own record of what it has already shown the session here; and an answer that has been cleared, which is a tombstone rather than a value. |
| `secrets` | One key per allow-listed secret name (`typesafe`, `client.key`, `r2.endpoint`, `r2.bucket`, `r2.key_id`, `r2.secret`), each value an XChaCha20-Poly1305 envelope sealed under a subkey derived from `sync.key`, with the name as associated data. A cleared secret is a tombstone. The document is always inline in the log, never a `$ref`, because a joiner holds no R2 credentials until it has applied it. | `turso.token`, `sync.key`, `serve.tls.*` and every name off the list, in both directions. A `client.key` nobody stamped (one relevo generated or normalized) is not exported, so a machine's own identity is not replaced by a peer's generated one; only a person's `relevo config secret set` makes one travel. A locked machine neither exports nor applies the document. The value is never printed: a loss record names the key and the reason. |

## Per-user config, synced deliberately or not at all

Configuration describes what this user's machine can run: its candidates,
actors, policy, roles, prices and servers name local binaries and harnesses.
Another machine's are not the same, so a section travels only through the
`config` document's keys, and only the shared layer of it does.

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

## Hook scripts inside the config document

The `hooks` section is the one section whose keys name files rather than
values, so the rule above applies inside it as well: a path stored there is
either this machine's or it is nobody's.

The shared half holds the scripts under `<config root>/hooks/<event>.d/`,
named `hooks/<event>.d/<file>`, with their contents and their executable bit.
The name is root-relative so a peer whose config root differs still applies it
under its own directory, and the directory is computed from the event and file
names rather than read out of the entry, so an entry cannot steer a write.

What never leaves the machine:

- an argv row whose `argv[0]` is absolute and outside the config root. It is
  split out into `config_local` under the `hooks` key's `local` member before
  the write commits, by the idempotent step the `sync.config_hooks_local.v1`
  local kv marker records. The marker is its own row rather than
  `sync.config_local.v1`, because that one is already set on every machine that
  ran the machine-layer move, and a move guarded by it would never run there.
- a script a person edited on disk. The applier writes only a missing file or
  one whose bytes still match, and `relevo doctor` names what it kept.

Caps, refused with a test each: one script over 256 KiB, and one hooks value
over 2 MiB in total.

`apply` computes every path from the config root and refuses a name that is not
one plain component (no `/`, no `\`, not `.` or `..`, no NUL, at most 128
bytes). Writes go through a temp file in the target's own directory and a
rename; a target that exists as a symlink or as anything but a regular file is
refused rather than followed; and the executable bit is set only under
`<config root>/hooks/`.

The `sync.filehash.v1` local kv row is the capture's cache: one root-relative
path to the digest, size, modification time and executable bit last seen. The
capture stats first and reuses the document's bytes for a script the cache
still vouches for, so an unchanged hook costs one stat per file and no write.
The stamp is the capture time: it is recorded on the `hooks` item through the
same write path `relevo config set` uses, so an edited script exports with the
next attempt rather than with the next tick.

## Native agent files inside the config document

An `agents` item is one agent. A source agent is its own text, which the item
already travels, and its rendered definitions are derived from that text, so
nothing is embedded. A native agent names an agent each harness already knows
how to run, and that is the case the file map is for: `native` says which
definition, `files` says what it contains.

The `files` member maps a harness kind to that kind's definition of the
agent. The path is never stored. It is computed from the kind through the same
harness table `relevo config agents` installs through, so a peer whose home
differs applies the file under its own and an item cannot steer a write or name
a directory outside a kind's own home.

What never leaves the machine:

- a definition a person edited on disk. The applier writes only a missing file
  or one whose bytes the capture's cache still vouches for, and `relevo doctor`
  names what it kept.
- a rendered definition a person edited. The re-render that follows an arriving
  `agents` item runs `InstallCustomAgents` without `--force`, so the outcome is
  keep-differs -- the same outcome `refreshRoles` gets for the definitions it
  does not force either. A render that cannot run is one warning and never
  fails the import that named it.

Caps, refused with a test each: one definition over 256 KiB, and one agents
item over 2 MiB in total.

`apply` refuses a kind that is not a harness kind, an agent name that is not
one plain component (no `/`, no `\`, not `.` or `..`, no NUL, at most 128
bytes), and a file whose kind the item's own `native` map does not name. Writes
go through a temp file in the target's own directory and a rename; a target
that exists as a symlink or as anything but a regular file is refused rather
than followed; and a definition is never executable, whatever mode the item
asked for -- only a hook script is, and only under `<config root>/hooks/`.

The `sync.agenthash.v1` local kv row is the capture's cache for these files: one
absolute path to the digest, size and modification time last seen. It is a
separate row from the hooks' `sync.filehash.v1` because the two describe
different trees, and one row for both would make a section's capture rewrite
the other's entries. The capture stats first and reuses the item's bytes for a
definition the cache still vouches for, so an unchanged agent costs one stat
per file and no write. The stamp is the capture time, recorded through the same
write path `relevo config set` uses, so an edited definition exports with the
next attempt rather than with the next tick.

The re-render reads the config the apply wrote, so it cannot run inside the
importer's transaction. The apply queues the agent and the pipeline drains the
queue once the transaction has committed -- after the steady import, after a
join's drain and after an unlock's drain.
