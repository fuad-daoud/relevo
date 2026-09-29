# Persistence: a database as relay's system of record (#172, #183)

Status: design, 2026-09-20. Supersedes the "Shape" section of #172 (tarball
index + read verbs); keeps its uses, its gap list and its test rule.

## 1. Purpose

relay's state directory is the only record of how a feature was built --
the plan, the builder that ran it, the diff, the report, the transcript,
what it cost -- and today that record is durable but unreadable past the
current binding, holds no relations, and is gone from every view once `gc`
tars it. This spec gives relay a database that holds **everything relay has
ever done** -- every repo, planner, binding, round, builder, token, event,
artifact and transcript -- so that `relay ui`, the CLI and later statistics
can render a round from a year ago exactly as they render one from today.

The store is SQLite (`modernc.org/sqlite`) behind `internal/db`, and the
schema stays movable, but the move to Turso is **not** a driver swap:
checked 2026-09-25 (#466), Turso's embedded driver cannot host one file
shared by several processes, and its opt-in multi-process mode is still
experimental. Decision 2 and `internal/db/migrations/README.md` hold the
dialect rules; #466 holds the findings and the conditions that start the
swap.

## 2. Direction, in three phases

This spec is phase 1. Phases 2 and 3 are the stated destination and the
reason several phase-1 choices look the way they do; they get their own
specs.

| phase | writes | reads | files |
|---|---|---|---|
| **1 (this spec)** | files, as today; an ingester fills the db from them every tick | live bindings from files, as today; **past bindings from the db only** | unchanged; `gc` still tars; a one-time backfill ingests every tarball and live dir |
| 2 | files, ingester | **every** reader (`status`, `log`, `tab`, `diff`, `pull`, `ui`) from the db | unchanged |
| 3 | **rows, directly**; ingester retires | db | the directory shrinks to the harness transport: the plan the builder reads, the report it writes, the stream it emits; `gc` deletes |

The db is the source of truth from phase 2 on. Phase 1 already treats it so
for anything not live.

## 3. Decisions

1. **Driver: `modernc.org/sqlite`** behind `internal/db`; pure Go, keeps
   `CGO_ENABLED=0` and the cross-compile matrix. Turso's own Go driver
   (`turso.tech/database/tursogo`) still replaces it one day by changing
   `Open` and the import, but the move is bigger than a driver swap
   (checked 2026-09-25, #466): several processes cannot open one file
   without the experimental `multiprocess_wal` mode, the binary stops
   being static (a native library of 16-21 MB per target), and the dialect
   rules needed correcting. #466 lists the conditions that start the swap;
   until then the driver stays. Current `modernc.org/sqlite` needs Go 1.25,
   so `go.mod` moves to `go 1.25` and CI's matrix to `['1.25', 'stable']`.
2. **A movable dialect.** The list that matters lives in one place,
   `internal/db/migrations/README.md`, checked against Turso v0.8.0-pre.12
   and re-checked at v0.8.1 (2026-09-29). It binds the schema to: no
   dependence on in-place `VACUUM` (`VACUUM INTO` is fine), no pragmas
   outside Turso's compatibility list, and driver errors mapped only
   through `mapBusy` and `mapPlannerKey`. `RETURNING`, `AUTOINCREMENT`,
   triggers and plain views are supported by Turso and are no longer
   banned on its account; whether to use them is a separate question. The
   schema's own conventions stay separate: text ULIDs as ids; RFC3339 UTC
   text with millisecond precision for timestamps; `INTEGER 0/1` booleans;
   JSON as `TEXT`; and no FTS, virtual tables or generated columns in the
   phase-1 schema.
3. **Files stay the write side in phase 1.** One ingester reads a binding
   directory (live, or a tarball) and upserts rows. The backfill and the
   daemon's per-tick ingest are the same function. New facts the db needs
   (repo, feature, fork parent, planner transcript locator) are added to
   `bind.json` as fields so they flow through the same parser and so phase 3
   does not have to invent them.
4. **Planner = observed session + optional human label.** One `planner` row
   per (harness kind, session id) relay sees at bind; `--feature <label>` on
   `bind`/`add`/`fork` as the one human-given relation, inherited by fork;
   `forked_from (binding, round)` a real field.
5. **Planner transcripts are copied**, one row per record, keyed on the
   planner session, drained by the daemon from the harness's own file with
   the cursor #228 already uses for builders. Size is a later optimisation.
6. **Repo identity is both**: normalised `origin` URL as the grouping key,
   git common dir as the fallback and the local match; `--here` matches
   either.
7. **ui gets a scope toggle, not a new screen.** `live` (today) / `all`
   (every binding ever). A past binding's detail pane is the live one plus
   round stepping (#183). The dashboard/grid with filters is the next spec,
   over the same `Query(Filter)` contract this one ships and tests.
8. **Round outcome is recorded, not derived**, once, by the ingester
   (phase 3 writers set it at close): `reported | halted | exited | switched
   | done_no_report | open`.
9. **`history.json` is renamed `availability.json`** (#172 q6) before the
   word is reused. `Runtime.HistoryPath` becomes `AvailabilityPath`; the old
   file is read once and moved.
10. **New verbs**: `relay db` (`migrate`, `backfill`, `path`, `stats`) and
    `relay history`, `relay show`, against the #114 freeze; #172 and #183
    already asked for the last two.

## 4. Schema

`internal/db/migrations/NNN_<name>.sql`, embedded, applied in order inside
one transaction each; `schema_version(version INTEGER PRIMARY KEY,
applied_at TEXT)`. `001_initial.sql` creates:

```
repo
  id TEXT PK, origin_url TEXT NULL, common_dir TEXT NULL, first_seen TEXT
  UNIQUE(origin_url) WHERE NOT NULL; UNIQUE(common_dir) WHERE NOT NULL

planner
  id TEXT PK, harness_kind TEXT, session_id TEXT, transcript_locator TEXT NULL,
  first_seen TEXT, last_seen TEXT
  UNIQUE(harness_kind, session_id)

binding
  id TEXT PK, name TEXT, repo_id TEXT NULL FK, planner_id TEXT NULL FK,
  feature TEXT NULL, forked_from_binding_id TEXT NULL FK, forked_from_round INTEGER NULL,
  cwd TEXT, worktree TEXT NULL, branch TEXT NULL, base_commit TEXT NULL,
  tier TEXT NULL, gate TEXT NULL, builder_mode TEXT, server TEXT NULL,
  created_at TEXT, final_state TEXT NULL, archived_at TEXT NULL, archive_path TEXT NULL,
  ingest_source TEXT      -- 'live' | 'archive'
  UNIQUE(name, created_at)

round
  id TEXT PK, binding_id TEXT FK, number INTEGER,
  started_at TEXT, closed_at TEXT NULL, outcome TEXT,
  builder_candidate TEXT NULL, builder_harness TEXT NULL, builder_provider TEXT NULL,
  builder_model TEXT NULL, builder_mode TEXT NULL, tier TEXT NULL,
  commits INTEGER NULL, tree TEXT NULL,
  gate_result TEXT NULL, gate_exit INTEGER NULL, gate_duration_ms INTEGER NULL,
  in_tokens INTEGER NULL, cache_tokens INTEGER NULL, write_tokens INTEGER NULL,
  out_tokens INTEGER NULL, cost_usd REAL NULL, cost_basis TEXT NULL,
  report_outcome TEXT NULL,   -- the report's own block: done|halted|blocked|deferred|unstructured
  switches INTEGER            -- number of mid-round switches
  UNIQUE(binding_id, number)

event
  id TEXT PK, binding_id TEXT FK, round_id TEXT NULL FK, seq INTEGER,
  ts TEXT, kind TEXT, direction TEXT, note TEXT NULL, path TEXT NULL,
  delivered_at TEXT NULL, confirmed INTEGER, late INTEGER,
  flagged INTEGER NULL, flagged_by TEXT NULL, entry_json TEXT
  UNIQUE(binding_id, seq)
  -- entry_json is the LogEntry verbatim; every typed column above is a
  -- projection of it, so phase 2's `relay log` loses nothing.

artifact
  id TEXT PK, round_id TEXT FK, kind TEXT, consult_id TEXT NULL,
  text TEXT, bytes INTEGER, sha256 TEXT, captured_at TEXT
  UNIQUE(round_id, kind, consult_id)
  -- kind: plan|report|diff|drift|gate_log|question|answer|ask|findings

transcript
  id TEXT PK, owner_kind TEXT, owner_id TEXT, seq INTEGER,
  ts TEXT NULL, record_json TEXT, rendered TEXT
  UNIQUE(owner_kind, owner_id, seq)
  -- owner_kind: round (builder stream) | planner (planner session)

ingest_cursor
  source TEXT PK,             -- absolute path, or "<tarball>::<member>"
  byte_offset INTEGER, head_sha TEXT, whole_sha TEXT NULL, updated_at TEXT
```

Indexes: `round(binding_id, number)`, `round(started_at)`,
`round(builder_harness, builder_provider, builder_model)`,
`event(binding_id, round_id, seq)`, `transcript(owner_kind, owner_id, seq)`,
`binding(repo_id)`, `binding(planner_id)`, `binding(feature)`.

The db file is `<state root>/relay.db` (`$XDG_STATE_HOME/relay`, default
`~/.local/state/relay`); the WAL and shm files sit beside it. `relay serve`
has its own state root and therefore its own db.

## 5. Components

### 5.1 `internal/db` -- the only package that imports a driver

```
type DB struct{ sql *sql.DB }

Open(path string) (*DB, error)          // modernc, WAL, busy_timeout 5s, foreign_keys on, migrate
(*DB).Close() error
(*DB).Version() (int, error)

// writers -- every one an upsert on the table's natural key; idempotent
(*DB).UpsertRepo(Repo) (id string, err error)
(*DB).UpsertPlanner(Planner) (id string, err error)
(*DB).UpsertBinding(Binding) (id string, err error)
(*DB).UpsertRound(Round) (id string, err error)
(*DB).AppendEvents(bindingID string, []Event) error        // skips seq already present
(*DB).UpsertArtifact(Artifact) error
(*DB).AppendTranscript(ownerKind, ownerID string, []TranscriptRecord) error
(*DB).Cursor(source string) (Cursor, bool, error)
(*DB).SaveCursor(Cursor) error
(*DB).Tx(func(*Tx) error) error         // the same writers on *Tx; Ingest uses one Tx per source

// readers -- the query contract
(*DB).Query(Filter) ([]RoundRow, error)
(*DB).Bindings(Filter) ([]BindingRow, error)
(*DB).Binding(name string) (BindingRow, bool, error)       // newest by created_at when names repeat
(*DB).Rounds(bindingID string) ([]Round, error)
(*DB).Artifact(roundID, kind string) (Artifact, bool, error)
(*DB).Transcript(ownerKind, ownerID string, fromSeq int, limit int) ([]TranscriptRecord, error)
(*DB).Events(bindingID string, round int) ([]Event, error)  // round 0 = all
(*DB).Stats() (Stats, error)                                // row counts per table, db size, version

type Filter struct {
    Repo, Here string            // origin url or common dir; Here is a cwd to resolve
    Feature, Binding, Planner    string
    Harness, Provider, Model     string
    Candidate                    string
    Outcome, ReportOutcome       string
    State                        string      // final_state
    GateResult, CostBasis        string
    Round                        int
    Since, Until                 time.Time
    Archived                     *bool
    Limit                        int
    Newest                       bool
}
```

`RoundRow` is the denormalised line `relay history` prints: binding name,
repo, feature, round number, started/closed, builder columns, outcome,
commits, tree, gate result, cost, archived flag. `Filter` is the shared
contract for `history --json`, the ui `all` scope and the later dashboard;
every field maps to one indexed column or a join, and the zero value means
"no constraint".

Multi-process: CLI verbs and the daemon both `Open` the file; WAL plus
`busy_timeout` covers concurrent use today. This is exactly what Turso's
embedded driver does not allow (decision 1, #466): the swap needs either
the daemon as the only opener, with the CLI reaching it over its socket,
or stable multi-process support upstream. Turso sync is phase 2's concern,
noted, not built.

### 5.2 `internal/ingest` -- files to rows, one function

```
type Source interface {
    Name() string                                   // binding name
    Bind() (store.Binding, error)                   // bind.json
    Open(member string) (io.ReadCloser, int64, error)  // a file in the dir; size
    List() ([]string, error)                        // member names
    Origin() (kind string, path string)             // "live", dir  |  "archive", tarball
}
DirSource(dir string) Source
TarSource(tarball string) Source                    // reads members without extracting

type Writer interface { ... the db writers above, on *db.Tx ... }

Ingest(ctx, src Source, w Writer, deps Deps) (Stats, error)
type Deps struct {
    Git       GitFacts       // Repo(ctx, cwd) (originURL, commonDir string, ok bool)
    Sessions  relay.SessionLocator
    Home      string
    Now       func() time.Time
}
type Stats struct{ Bindings, Rounds, Events, Artifacts, TranscriptRecords, Skipped int }
```

Order inside one `Ingest`, one transaction:

```
bind := src.Bind()
repo := upsert repo from bind.RepoRef (fallbacks: git facts of bind.CWD, then of bind.Repo -- the source checkout -- else null)
planner := upsert planner from bind.Planner (kind, session id, locator)
binding := upsert binding (natural key name+created_at; created_at from bind, else the log's first ts)
events := read log.jsonl from the cursor; append new entries with seq = line number
for each round number seen in events or in NNN-* members (never bind.json's round: it is the next, unstarted one):
    outcome := deriveOutcome(events for round, members)     -- §5.3
    builder columns := from the last pick/switch note for that round, else bind.BuilderCandidate
    usage / gate / commits / tree := from the round's report and diff entries
    upsert round
    for kind in plan, report, diff, drift, gate_log, question, answer, asks, findings:
        if member exists and whole_sha changed: upsert artifact
    if NNN-builder.jsonl exists: append transcript rows from the cursor (owner round; record_json + rendered)
    else if NNN-builder.log exists: append one row per rendered line (record_json empty) -- a pane round since #228, or an old archive
if planner locator resolves: append planner transcript rows from the cursor (owner planner)
if src is archive: set archived_at, archive_path, ingest_source
save cursors
```

Cursors: append-only members (`log.jsonl`, `NNN-builder.jsonl`, the planner
file) keep `byte_offset` + `head_sha` (sha256 of the first 4 KB); a
mismatch means the file was rewritten and the cursor restarts at 0 -- the
`UNIQUE` keys make the re-append a no-op for rows already present. Whole
members (`bind.json`, `NNN-plan.md`, ...) keep `whole_sha` and are skipped
when unchanged. A live tick where nothing changed costs one `stat` per
member and no writes.

Transcript rendering reuses `internal/transcript`'s per-record renderer so
`rendered` is byte-identical to the line in `NNN-builder.log`.

### 5.3 Round outcome

Derived from the round's events and members, in this order, first match
wins:

| condition | outcome |
|---|---|
| a `report` entry exists | `reported` |
| `NNN-done` exists and no report | `done_no_report` |
| an `exit` entry exists and the round is the binding's last, state is not active | `exited` |
| the binding's `Halt` names this round, or state is `needs_you` on this round | `halted` |
| a `switch` entry exists and nothing above | `switched` |
| the round is the binding's current round and state is active/held | `open` |
| otherwise | `open` |

`switches` counts `switch` entries for the round regardless of outcome.

### 5.4 Fields added to `bind.json`

All `omitempty`; a `bind.json` written before this change is byte-identical
after it.

```
Binding.RepoRef     *RepoRef  { OriginURL, CommonDir string }   // json "repo_ref"; Binding.Repo (string) already exists -- the add/fork source checkout (#192) -- and stays; set at bind/add/fork
Binding.Feature     string                                       // --feature; fork inherits
(fork parent: Binding.ForkedFrom string + ForkedAtRound int already exist, json "forked_from"/"forked_at_round"; nothing is added)
Endpoint.TranscriptLocator string   // on Planner: the harness file path when Sessions resolves it at bind
Binding.CreatedAt   time.Time                                    // already present on remote bindings; set for all
```

`--feature <label>` is accepted by `bind`, `add` and `fork`; `fork` copies
the parent's when none is given. A label is 1..64 bytes of
`[A-Za-z0-9._ -]`.

### 5.5 The daemon

At the end of `Daemon.Tick`, after `notifyFinished`: for each live binding,
`ingest.Ingest(ctx, DirSource(store.Dir(name)), db, deps)`. Errors are
logged at `Warn` with the binding name and never fail the tick. The db is
opened once in `NewDaemon` (`Runtime.DB *db.DB`; nil means "no db", every
call site skips -- tests that do not set it behave exactly as before).

Planner transcript drain: the locator on the planner endpoint is resolved
at bind for `claude` through `relay.HomeSessionLocator` (the #228 glob) and
re-resolved by the ingester when empty; other harness kinds record nothing
until their format is added.

### 5.6 `relay db`

```
relay db path                       print the db path
relay db migrate                    open (which migrates) and print the version
relay db backfill [--dry-run] [--archive-only|--live-only]
                                    Ingest every tarball under .archive/ (oldest first)
                                    then every live dir; print Stats per source and a total;
                                    a source that fails is reported and skipped, exit 1 at the end
relay db stats                      Stats: rows per table, size, version, newest round
```

`backfill` is idempotent and safe to re-run; the cursors make a second run
a no-op. It is a one-time verb for existing machines and the recovery path
if the db is ever deleted.

### 5.7 `relay history`, `relay show`

```
relay history [--here|--repo <url|dir>] [--feature L] [--binding N] [--planner S]
              [--harness K] [--provider P] [--model M] [--candidate T]
              [--outcome O] [--since D] [--until D] [--archived|--live]
              [--limit N] [--json]
```
One line per round, newest first:
`2026-09-15 14:02  api-auth  r3  agy/antigravity/opus  reported  +2 commits  clean  $0.42  (archived)`.
`--json` prints `[]RoundRow`. Every flag is one `Filter` field.

```
relay show <name> [--round N] [--plan|--report|--diff|--drift|--log|--transcript] [--json]
```
Default the newest completed round, default section `--plan`. Live binding:
read the file (today's path). Otherwise: read the artifact / transcript /
event rows. `--log` prints the round's events like `relay log`;
`--transcript` prints `rendered` lines. A missing section prints one line
saying so and exits 0.

### 5.8 `relay ui`

- Key `a` toggles the rail scope `live` / `all` (persisted in `ui.json`
  prefs). `all` lists `db.Bindings(Filter{Here: cwd})` when the ui was
  started inside a repo, else every binding, newest activity first; rows
  not in the live list are rendered dim with `archived <date>` in place of
  the state, and are never re-ordered by attention.
- Selecting a non-live row: the detail tabs are `plan / report / terminal /
  diff / log`; every tab fetches from the db by `(binding id, detail.round)`.
  `terminal` shows `transcript` rows for the round (styled by the same
  renderer as today's round log), scrollable, not tail-following.
- `[` / `]` step `detail.round` through the binding's rounds for live and
  past bindings alike (#183); the header reads `round 2 of 4` and, for a
  past binding, `· archived 2026-08-30`. On the open round of a live
  binding, `report`/`diff` show the empty prose #183 specifies.
- A `plan` tab is added first in the tab order for live bindings too (#183).
- No filters beyond `--here` in this spec; the grid is the next one.

## 6. Error handling

| category | example | recoverable | contract |
|---|---|---|---|
| `db.ErrOpen` | file unwritable, migration fails | no | `relay db *`, `history`, `show`: exit 1 with the path and the driver error. Daemon: `Warn` once per start, then every call site sees `DB == nil`. ui: the `all` scope shows "no database: <err>" in the rail and stays on `live`. |
| `db.ErrBusy` | `SQLITE_BUSY` after 5 s | yes | retried once by the caller; the daemon skips the binding this tick. |
| `ingest.ErrSource` | tarball unreadable, `bind.json` missing or invalid | per source | backfill reports and continues; the daemon logs and skips the binding this tick. |
| `ingest.ErrCursor` | head sha mismatch | yes | reset to 0, log at `Info`; rows dedupe on `UNIQUE`. |
| `ingest.ErrTranscriptRecord` | one unparsable stream line | yes | stored with `record_json` raw and `rendered` empty; counted in `Stats.Skipped`. |
| validation | bad `--feature`, unknown `--outcome` | n/a | CLI exit 2 with the accepted form. |

No error in this spec changes a binding's state or a round's files.
Observability: the daemon logs one `Info` line per tick that wrote rows
(`ingest binding=x rounds=1 events=3 transcript=41`), nothing when idle.

## 7. Testing

Pure functions in `internal/db` and `internal/ingest` over `t.TempDir()`;
no herdr, no `cmd/relay` test reaches a subcommand that needs it.

- `db`: migrate from empty; migrate is idempotent; every upsert is
  idempotent (call twice, count once); `Query` over a seeded db with three
  bindings across two repos exercises every `Filter` field with one
  assertion each; `Bindings` newest-first; `Transcript` paging.
- `ingest`: a golden fixture directory (a real binding dir with three
  rounds -- reported, switched, halted -- plus one pane round with no
  stream) ingests to the expected rows; the same directory packed as a
  tarball ingests to identical rows plus `archived_at`; re-ingest is a
  no-op; a truncated `log.jsonl` resets the cursor and dedupes; a
  `bind.json` without the new fields ingests with null repo/feature;
  `deriveOutcome` has one table test per row of §5.3.
- Mutation tests the plan names: remove the `head_sha` check and
  `TestIngestCursorReset` fails; drop the report-first rule and
  `TestOutcomeReportedBeatsDone` fails.
- `cmd/relay`: `history` line formatting and `show` section selection as
  pure functions over `[]RoundRow` / a seeded db.
- `ui`: golden tests of the `all` rail, the dim archived row, the `plan`
  tab, and stepping `detail.round` back two rounds with every tab
  refetching.
- Backfill against this machine's real `.archive/` (49 tarballs) is a
  manual verification step in the plan, not a test.

## 7a. What the first rounds showed

Verified on this machine after round 5: 62 sources (49 archives, 13 live)
backfill in under 3 s to 102 rounds, 459 events, 295 artifacts, 51k
transcript rows, 118 MB; a second backfill is a no-op. Two facts for
phase 2:

- **Remote builders' usage is not measured.** A remote round's report
  entry carries `Usage` with basis `unknown` (`shared cwd`) because the
  client reads the harness record locally and it lives on the server. The
  server must measure the round and ship the figure with the report
  (#216); until then `history` prints `unknown` for remote rounds.
- `relay serve` does not open a db; the server's state root is ingested by
  nothing. Its rounds exist in the client's db only as the client saw them.

## 8. Out of scope

Retention or pruning of any kind. Changing what `done`/`unbind`/`gc` do
(phase 3). Any reader other than `history`, `show` and the ui `all` scope
moving to the db (phase 2). The dashboard/grid and filters in the ui
(next spec). Turso sync (phase 2, #473). Searching transcript content. Cross-
machine history. Statistics verbs beyond `db stats`. #184's opencode
transcript.

## 9. Rounds

Five builder rounds, each a standalone plan under `docs/plans/`:

1. `2026-09-20-persistence-r1-db.md` -- go directive, `internal/db`, schema,
   migrations, writers, readers, `relay db path|migrate|stats`,
   `availability.json` rename.
2. `2026-09-20-persistence-r2-fields.md` -- `bind.json` fields, `--feature`,
   `ForkedFrom`, `Repo` capture, planner locator at bind.
3. `2026-09-20-persistence-r3-ingest.md` -- `internal/ingest`, sources,
   cursors, outcome derivation, `relay db backfill`, daemon tick hook.
4. `2026-09-20-persistence-r4-verbs.md` -- `relay history`, `relay show`.
5. `2026-09-20-persistence-r5-ui.md` -- ui `all` scope, `plan` tab, round
   stepping, db-backed detail tabs.

Related: #172, #183, #184, #186, #56, #61, #114, #124, #130, #142, #168,
#228.
