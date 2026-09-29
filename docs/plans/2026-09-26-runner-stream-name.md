# Plan: a round's stream is `NNN-runner.jsonl` (old rounds keep `NNN-builder.jsonl`)

Round: 1 (2026-09-26). Repo: relevo. Working tree: the round's own worktree, branch off `main`.

Read this with the round's task text. Where this plan and the code disagree, **halt and report**;
do not improvise a different rename.

## 1. System overview

Every round's harness stream -- the harness's JSON, one event per line, plus the supervisor's
exit trailer -- is stored as `<state>/<binding>/NNN-builder.jsonl`
(`store.BuilderStreamPath`, `internal/store/paths.go:148`). relevo's vocabulary is now
actor/runner/candidate (`docs/specs/2026-09-26-a4-state-rename-design.md`), and the live process
is the **runner**, so new rounds write `NNN-runner.jsonl` in the same flat place.

The rename is store-level only. Three facts drive the design:

1. **Nothing already written is rewritten.** Rounds spooled on disk and `round_file` rows sealed
   before this change carry the old name, and must keep being read. So the store keeps *both*
   name helpers and adds one resolver, and every reader that resolves a file path goes through
   the resolver.
2. **A round's stream never changes its name once it exists.** Writers resolve the same way
   readers do, so a round in flight across the upgrade keeps appending to its
   `NNN-builder.jsonl`, and a new round starts on `NNN-runner.jsonl`. No round ever ends up
   with two stream files.
3. **The wire carries no file name.** A round file is fetched as
   `GET /v1/bindings/{name}/rounds/{n}/files/{kind}` with `kind = "stream"`
   (`internal/serve/routes.go:67`, `internal/remote/client/rounds.go:157-158`,
   `internal/relevo/remotefetch.go:479`). The server resolves the kind to its own local file and
   the client writes the bytes to its own local file: the name never crosses the wire and the
   kind stays `stream`. A new client against an older server and an old client against a new
   server both work, with no protocol change and no header.

`NNN-builder.log` is untouched: it is the pre-builder-log-r2 rendered log, new rounds never
create one, and every decision about it already funnels through `legacyLog`.

Out of scope, per the round's task: the summary.md status block and the close-payload wording
(`internal/relevo/send.go`, the payload text in `reconcile.go`/`stop.go`, `remote_catchup.go`,
`reporttail`). Two of those files hold a `BuilderStreamPath` call -- `reconcile.go:510` and
`remote_catchup.go:57` -- and both change by the pure accessor rename only (step 3). No payload
string, no note, no status line is reworded.

## 2. File structure

```
internal/store/
  paths.go                     MODIFY  RunnerStreamPath + the frozen BuilderStreamPath + StreamPath
  paths_stream_test.go         NEW     TestStreamPathResolvesTheRoundStream (disk and sealed rows)
  seal.go                      MODIFY  StreamDrained resolves the stream (line 232)
  seal_test.go                 MODIFY  TestStreamDrained gains pre-rename rows
  store_test.go                MODIFY  TestPathShapes names RunnerStreamPath and keeps the legacy one
internal/relevo/
  transcript.go                MODIFY  builderTail, currentBuilderTail, RoundTranscript (158, 175, 216)
  summary.go                   MODIFY  writeReaderSummary resolves the stream (41)
  usage.go                     MODIFY  roundSource resolves the stream (63)
  headless.go                  MODIFY  startProcess, drainStream, streamLastActivity, both ExitCode sites
  reconcile.go                 MODIFY  the rusage read (510) -- accessor rename only
  remotefetch.go               MODIFY  fetchCatchUpStream installs under the resolved name (489)
  remote_catchup.go            MODIFY  applyCatchUpFiles installs under the resolved name (57)
  transcript_test.go           MODIFY  pre-rename subtest; primary fixtures on the new name
  headless_test.go             MODIFY  streamWrite and the path assertions on the new name;
                                       new TestStartRoundKeepsWritingAPreRenameRoundStream
  usage_test.go                MODIFY  the stream path expectation (135)
  reader_close_test.go         MODIFY  the fixture path (56)
  reconcile_hooks_test.go      MODIFY  the fixture path (118)
  remote_test.go               MODIFY  the mirror fixture and assertion (4617, 4663)
  remotefetch_test.go          MODIFY  only if it writes a stream path (it writes logs today)
internal/serve/
  roundfiles.go                MODIFY  kind "stream" resolves (25)
  serve_test.go                MODIFY  round-file fixtures on the new name; new pre-rename test
  helpers_test.go              MODIFY  fakeStreamUsage's name check (303)
internal/ui/
  fetch.go                     MODIFY  LogPath comparison and the not-written-yet line (419, 453)
  fetch_test.go                MODIFY  primary fixture on the new name; new pre-rename sibling
internal/ingest/
  dedupe.go                    MODIFY  a sealed stream is read under either name
  dedupe_test.go               MODIFY  one round on the new name; a named pre-rename test
internal/usage/
  source.go                    MODIFY  the StreamPath field comment (31)
CLAUDE.md                      MODIFY  "Working with builders" names the new file (29)
README.md                      MODIFY  the three stream-name sentences (596, 696, 799)
docs/plans/2026-09-26-runner-stream-name.md  NEW  this plan (step 10)
```

## 3. Data structures and type definitions

No new struct, no wire type, no database column, no schema migration, no golden of a stored
shape. The change is three functions and one ingest helper.

**`store.Store` path helpers** (`internal/store/paths.go`)

| name | returns | notes |
|---|---|---|
| `RunnerStreamPath(name string, round int) string` | `<state>/<name>/NNN-runner.jsonl` | the new name; every new round writes it |
| `BuilderStreamPath(name string, round int) string` | `<state>/<name>/NNN-builder.jsonl` | **frozen**: the pre-rename name; body unchanged from today's `roundFile(name, round, "builder", ".jsonl")` |
| `StreamPath(name string, round int) string` | one of the two above | the round's stream wherever it is; total, never errors |

**`StreamPath` resolution rule** (this is the whole contract, and every writer obeys it):

1. if `RunnerStreamPath(name, round)` exists -- on disk **or** as a sealed `round_file` row --
   return it;
2. otherwise if `BuilderStreamPath(name, round)` exists the same way, return it;
3. otherwise return `RunnerStreamPath(name, round)` (the name a new round starts on).

Existence is `Store.StatFile`, so a sealed row counts and a missing binding/database is a miss,
not an error.

**Ingest's two names** (`internal/ingest/dedupe.go`): the existing `builderStreamPathBase(round)`
(`filepath.Base(memberStore.BuilderStreamPath("x", round))`) becomes the *legacy* base and a
`runnerStreamPathBase(round)` joins it; the new `sealedStream` helper returns
`(name string, body []byte, found bool, err error)` -- the round's sealed stream row under the
current name first, then the pre-rename name, with the name it was found under.

**No type changes**: `store.Endpoint` (`StreamRound`, `StreamOffset`, `StreamStart`,
`StreamSegments`, `StreamSessionID`), `usage.Source.StreamPath`, `spawn.ProcSpec.StreamPath` and
`remote.BindingView` keep their fields and names. `b.Builder.LogPath` keeps holding the round's
stream path for a round started after builder-log r2; that is why the comparison sites resolve
rather than hard-code (step 3, `ui/fetch.go:419`).

## 4. Interfaces and component contracts

### 4.1 `RunnerStreamPath` / `BuilderStreamPath` / `StreamPath` (`internal/store/paths.go`)

*Single responsibility: name a round's stream file.*

- `RunnerStreamPath(name, round) string` -- pure; `round` is not validated (as every other path
  helper here). Postcondition: the string always ends `NNN-runner.jsonl`.
- `BuilderStreamPath(name, round) string` -- pure; **left byte-for-byte as today** except the
  doc comment, which says it is the pre-rename name that old rounds and sealed rows carry.
  Nothing new may call it for a round that has not resolved to it.
- `StreamPath(name, round) string` -- reads the store (two `StatFile` calls, so disk and sealed
  rows both count). Returns the resolved path; a miss on both returns `RunnerStreamPath`.
  Preconditions: none; `s` may be a store with no database (`StatFile` answers miss). Errors:
  none -- a read error and a not-exist are both a miss, exactly as `StatFile`'s `ok=false` is.
  Postcondition: `StreamPath` is the path the round's *file readers* open, and
  `os.Stat(StreamPath(...))` succeeding implies no other name has to be tried.

  Note the precedence: when both names exist (a test or an out-of-band file, never two relevo
  writers), the new name wins.

### 4.2 Reader contract (the rule every caller keeps)

Any component that opens, stats, tails, renders or transmits a round's stream resolves the path
once through `StreamPath` and passes that path down. No component builds either name by hand
outside `paths.go` (tests and `ingest`'s base-name helpers excepted). Concretely, after this
change `grep -rn 'BuilderStreamPath' --include='*.go' .` outside `_test.go` files matches only
`internal/store/paths.go` (the definition) and `internal/ingest/dedupe.go` (the legacy base name).

`RoundTranscript` (`internal/relevo/transcript.go:208`) is the one reader that owns its own
lookup: its `read` parameter may be the database (`rt.Store.ReadFile`), an archive
(`ArchivedFile`, keyed by basename) or a test map. It therefore tries the two names **in the
same order** `StreamPath` resolves -- `RunnerStreamPath` first, then `BuilderStreamPath` -- and
answers the first the reader finds; its `source` label stays `filepath.Base(streamPath) + " (rendered)"`,
so a new round reads `NNN-runner.jsonl (rendered)` and an old one keeps
`NNN-builder.jsonl (rendered)`.

### 4.3 The wire (server and client)

- The kind string is `stream` and stays `stream` (`internal/serve/roundfiles.go:24`,
  `internal/relevo/remotefetch.go:479`, `internal/relevo/remotefetch.go:190` for drift etc.).
  Nothing else on the wire names the stream file: no header, no `BindingView` field, no URL
  segment.
- `roundFilePath` (`internal/serve/roundfiles.go:16`) answers kind `stream` with
  `rt.Store.StreamPath(name, n)`, so the server serves whichever file it has -- `ReadFile` then
  finds a disk file or a sealed row under that name.
- Client-side catch-up installs the fetched bytes under `rt.Store.StreamPath(name, n)`
  (`fetchCatchUpStream` line 489 -> `applyCatchUpFiles` line 57), so a round already mirrored
  under the old name is overwritten in place and a new mirror round takes the new name.
- Cross-version, both directions, no coordination: an older server resolves its own old name and
  serves by kind; a newer server resolves either name and serves by kind; an older client writes
  whatever it fetches to its own old name; a newer client writes to its own resolved name. The
  only thing that must not change is the kind string -- state this in the commit message body.

### 4.4 Ingest contract (`internal/ingest/dedupe.go`)

`sealedStream(d *db.DB, record db.Record, round int) (name string, body []byte, found bool, err error)`.

- Preconditions: `record` is a mirror/archive record; `round >= 1`.
- Postconditions: `found` implies `body` is that round's sealed stream row and `name` is the
  `round_file` name it was found under (`NNN-runner.jsonl` or `NNN-builder.jsonl`), for error
  text and for `roundFileLines`. `d.RoundFileGet`'s own error is returned wrapped as today; a
  miss on both names is `found=false, err=nil`.
- Used by `deriveTranscripts` (today lines 334-350) and `streamLinesCover` (today lines 237-248).
  Both lose nothing: the names they used to compute inline become the helper's two tries.

## 5. High-level pseudocode

```
-- Resolution (store.StreamPath)
for p in [RunnerStreamPath(name, round), BuilderStreamPath(name, round)]:
    if StatFile(p).ok: return p
return RunnerStreamPath(name, round)

-- A round starts (relevo.startProcess, headless.go:240)
stream := StreamPath(b.Name, b.Round)          # old name when the round already has one
if StreamRound != Round: reset the cursor, segments          # unchanged
StreamStart = size(stream) or 0                              # was: size(BuilderStreamPath)
logPath = stream                                             # stderr joins the stream
                                     unless legacyLog(round): logPath = BuilderLogPath   # unchanged
record segments row, clear StreamSessionID                   # unchanged
ProcSpec{LogPath: logPath, StreamPath: stream}               # writers follow the resolver
b.Builder.LogPath = logPath

-- A reader tails the round (transcript.go)
builderTail:        streamTail(StreamPath(name, b.Round), ReadFile, ...)
currentBuilderTail: streamTail(StreamPath(name, b.Round), ReadFile, ..., from)
RoundTranscript:
    logPath := BuilderLogPath(name, round)          # untouched legacy preference
    if read(logPath) found: return it, base(logPath)
    for p in [RunnerStreamPath(name, round), BuilderStreamPath(name, round)]:
        data, ok, err := read(p)
        if err: return err                          # a real read error is not a miss
        if ok:  return renderStream(data, segments), base(p) + " (rendered)"
    return not found

-- The cockpit's terminal tab (ui/fetch.go)
if b.Builder.LogPath != "" and b.Builder.LogPath != StreamPath(name, b.Builder.StreamRound):
    read that file verbatim                   # rule 1, unchanged wording
else:
    RoundTranscript(...)                      # rule 2 renders the resolved stream
    on a miss, the empty line names StreamPath(name, r)

-- Ingest: prove a mirror round's rows against the sealed stream
sealedStream: for base in [runnerBase(round), builderBase(round)]:
                  body, ok := RoundFileGet(record, base)
                  if ok: return base, body, true
              return "", nil, false

-- Remote mirror install (client side)
StreamTemp -> os.Rename to StreamPath(name, n)      # never two files for one round
-- Server side
kind "stream" -> readRoundBytes(StreamPath(name, n)) via Store.ReadFile     # disk, then row
```

Branch points that matter, and why: (a) the resolver's first hit decides the name, which is how
a pre-rename round keeps its file; (b) `RoundTranscript` tries the log before either stream name,
unchanged; (c) ingest tries the new base before the old, so a round sealed after this change and
one sealed before it both answer.

## 6. Error handling strategy

No new error type, no new error string, no change to any error path.

| case | behaviour |
|---|---|
| neither name exists | readers keep today's miss (`os.ErrNotExist` from `ReadFile`, `found=false` for `RoundTranscript`, `"log not written yet: <path>"` for the cockpit, which now names the new path) |
| only the old name exists (disk or sealed row) | resolved and read; no warning, no log line |
| an error that is not not-exist (a real read error) | returned unchanged; `StreamPath` is total and never swallows it because it never reads bytes |
| both names exist | the new name wins; this cannot happen from two relevo writers, because every writer resolves first |
| no database at all | `StatFile` answers miss; the resolver returns the new name; nothing fails |
| server has an old round, client is new | kind `stream`; the client writes to its own resolved name |
| server is old, client is new | kind `stream`; unchanged on the wire |

Observability: nothing new is logged. `StreamPath` must not `slog.Warn` a fallback -- a normal,
expected read of a pre-rename round would become noise on every tick.

## 7. Working efficiently

Read this before step 1. Every change below names its file and its symbol or line range; do not
re-find anything with a broad search.

- **Batch the reads.** Steps 1-2 read three files; step 4 reads six test files; do every read a
  step needs in one batch, from the locations this plan names.
- **One scripted pass for the mechanical half.** Step 3 is a single `perl -pi -e` over an
  explicit file list. Do not hand-edit those eighteen call sites one at a time, and do not let the
  script reach a test file.
- **One edit per file.** Use `edit` with enough context to be unique (the surrounding function),
  or the `write` tool for a wholly rewritten block.
- **Iterate on a focused command, then run the full check once.** For each step:
  `go test -count=1 -run '<TestName>' ./internal/<pkg>`; for a whole package,
  `go test -count=1 ./internal/<pkg>`; after step 9 run the sweep
  `go test -count=1 ./internal/store ./internal/ingest ./internal/relevo ./internal/serve ./internal/ui ./internal/usage`
  and fix every failure before moving on; then `make check` exactly once at the end. `make check`
  is gofmt + `go vet` + golangci-lint + the comment/filesize/name/plugin-version guards +
  `go mod tidy` + `go test -race -count=1 -cover ./...` + the coverage baseline. It also runs
  `internal/e2e`'s `TestHeadlessE2E` (it has no build tag), which is where the one end-to-end
  round is exercised; do not run `make e2e` separately and never add a test that spawns a
  harness or touches the network.
- **If a failure is not about the stream name**, stop and check whether the step is wrong: do not
  add an exclusion, do not lower `testdata/coverage-baseline.txt` (only
  `sh scripts/check-coverage.sh --write` with a sentence in the report if a real drop needs it),
  and do not weaken an assertion to get green.
- **Comments in the files you touch**: `internal/store`, `internal/ingest`, `internal/serve` and
  `internal/usage` are not on `scripts/check-comments.allow`, so a new comment there may not cite
  `#NNN` or `§`, and may not say "used to" or "pre-#NNN".
- `make check` may not be run before step 10; a package test loop is cheap, a failing lint leg
  after four more edits is not.

## 8. Ordered implementation steps

Nothing in this plan deletes behaviour beyond the closed list below. Everything not on the list
survives.

**Deletion fence (closed list).** After this round:
1. a new round no longer writes `NNN-builder.jsonl`; it writes `NNN-runner.jsonl`;
2. `BuilderStreamPath` stops being *the* accessor every caller uses -- it survives as the frozen
   pre-rename name, and exactly two non-test references may use it for that purpose
   (`paths.go`'s own definition and `ingest`'s legacy base name);
3. no third thing changes: no stored file or `round_file` row is renamed or removed, `NNN-builder.log`
   keeps its role and its name, and the wire kind stays `stream`.
No test is deleted by this round. A test whose fixture moves to the new name is *ported* (its
expected name changes with it); say so in the report per file.

---

### Step 1 -- Store: the new name, the frozen old name, the resolver, and its unit test

Depends on: nothing. Deliverable: `internal/store/paths.go` exposes all three helpers.

Edits:
- `internal/store/paths.go:145-150`: replace `BuilderStreamPath`'s body with three functions --
  `RunnerStreamPath` (`return s.roundFile(name, round, "runner", ".jsonl")`),
  `BuilderStreamPath` (unchanged body, `"builder", ".jsonl"`, doc comment says frozen pre-rename
  name), and `StreamPath` implementing section 3's rule with `StatFile`.
- `internal/store/store_test.go:207-208`: the table row becomes
  `{"RunnerStreamPath", s.RunnerStreamPath("webshop", 3), "/state/webshop/003-runner.jsonl"}` and
  a second row keeps `{"BuilderStreamPath", s.BuilderStreamPath("webshop", 3), "/state/webshop/003-builder.jsonl"}`.
  Line 225's round-parse list gains a `runner stream` entry beside the `builder stream` one.
- New `internal/store/paths_stream_test.go`, `TestStreamPathResolvesTheRoundStream`, using
  `seedBinding(t)` (helpers_test.go:84) and `s.WithLock(func(tx *Tx) error { return tx.PutRoundFile("webshop", 1, <path>, body) })`
  for the sealed cases. Cases, in order: neither name exists -> `RunnerStreamPath`; the new name
  on disk -> the new path; only the old name on disk -> the old path; only the new name as a
  sealed row -> the new path; only the old name as a sealed row -> the old path; both on disk ->
  the new path. Assert with `t.Run` per case and a table.

Verify: `go test -count=1 -run 'TestPathShapes|TestStreamPathResolvesTheRoundStream' ./internal/store`

### Step 2 -- Store: the drain test follows the resolver

Depends on: step 1. Deliverable: `StreamDrained` reads a pre-rename round's stream.

Edits:
- `internal/store/seal.go:232`: `path := s.BuilderStreamPath(b.Name, round)` -> `path := s.StreamPath(b.Name, round)`.
- `internal/store/seal_test.go:100-151` (`TestStreamDrained`): add a `name string` field to the
  table (empty = the round's new name) and change line 131 to
  `path := s.RunnerStreamPath(b.Name, drained)` unless `tc.name == "legacy"`, in which case
  `s.BuilderStreamPath(...)`. Add two rows: `{"a pre-rename stream, cursor inside the payload", ...}` with
  the `relevoOnly` body, cursor 1, want `false`, and `{"a pre-rename stream with the trailer", ...}`
  with the cursor at the stream's size, want `true`. Keep every existing row on the new name.

Verify: `go test -count=1 -run TestStreamDrained ./internal/store`

### Step 3 -- The scripted accessor rename

Depends on: step 1. Deliverable: every non-test reader and writer resolves the stream.

Run from the repo root, exactly:

```sh
files="internal/serve/roundfiles.go internal/relevo/transcript.go internal/relevo/summary.go \
internal/relevo/usage.go internal/relevo/headless.go internal/relevo/reconcile.go \
internal/relevo/remotefetch.go internal/relevo/remote_catchup.go internal/ui/fetch.go"
perl -pi -e 's/\.BuilderStreamPath\(/.StreamPath(/g' $files
grep -n 'BuilderStreamPath' $files   # must print nothing
go build ./...
```

Eighteen call sites move: `roundfiles.go:25`; `transcript.go:158,175,216`;
`summary.go:41`; `usage.go:63`; `headless.go:247,282,303,423,567,715,1182`;
`reconcile.go:510`; `remotefetch.go:489`; `remote_catchup.go:57`; `ui/fetch.go:419,453`.

Then, by hand:
- `internal/usage/source.go:31`: the comment becomes
  `StreamPath string // headless: the round's stream, NNN-runner.jsonl, or NNN-builder.jsonl for a round from before the rename`.
- `internal/relevo/remotefetch.go:479` and `internal/relevo/remote_catchup.go:57`: leave the kind
  string `"stream"` alone; only line 57's path changed.

Do **not** apply the script to any `_test.go` file, to `internal/store/seal.go` (step 2), or to
`internal/ingest/dedupe.go` (step 7).

Verify: the `grep` prints nothing, `go build ./...` is clean, and
`go vet ./internal/serve ./internal/relevo ./internal/ui ./internal/usage` is clean. Package tests
are expected to fail until steps 4-9; that is the point of the scripted pass.

### Step 4 -- `internal/relevo` tests, and the two new relevo tests

Depends on: step 3. Deliverable: `go test ./internal/relevo` green, with the pre-rename writer pinned.

Edits (fixtures and expectations only):
- `headless_test.go:699` (`streamWrite`): `BuilderStreamPath` -> `RunnerStreamPath`.
- `headless_test.go:1368,3177,3560,3698`: a round's stream fixture -> `RunnerStreamPath`.
- `headless_test.go:200,257,273-274,3003,3816,3842`: the expected stream path becomes
  `rt.Store.RunnerStreamPath(...)`. (3803-3823's second assertion reads `BuilderLogPath`: leave it.)
- `usage_test.go:135`: `rt.Store.RunnerStreamPath(b.Name, b.Round)`.
- `reader_close_test.go:56`: `rt.Store.RunnerStreamPath(name, round)`.
- `reconcile_hooks_test.go:118`: `rt.Store.RunnerStreamPath(b.Name, b.Round)`.
- `remote_test.go:4617,4663`: the mirrored stream fixture and assertion -> `RunnerStreamPath`.
- `transcript_test.go:265,279,305,320,470`: fixtures -> `RunnerStreamPath`, and the `source`
  expectations at 298-299 and 364-365 become `001-runner.jsonl (rendered)`. Lines 347-348 and
  217-235 stay as they are (the archived-read adapter and the legacy-log fixtures are the
  pre-rename cases).
- Any remaining failure the loop reports, resolved the same way: a fixture that represents a
  *new* round uses `RunnerStreamPath`; a fixture that means *an old round* stays on
  `BuilderStreamPath` and is left alone.

New tests (pure, no harness, no network):
- `transcript_test.go`: `t.Run("the pre-rename stream name", ...)` inside `TestRoundTranscript` --
  write `stream` with `s.BuilderStreamPath("webshop", 1)` only, call `RoundTranscript`, assert the
  rendered text and `source == "001-builder.jsonl (rendered)"`.
- `headless_test.go`: `TestStartRoundKeepsWritingAPreRenameRoundStream` -- `seedHeadless`, write
  bytes to `rt.Store.BuilderStreamPath("webshop", b.Round)` only, then `startRound`: assert
  `fr.specs[0].StreamPath == rt.Store.BuilderStreamPath("webshop", b.Round)`,
  `fr.specs[0].LogPath` equal to it, `got.Builder.LogPath` equal to it, and
  `got.Builder.StreamStart == len(those bytes)`. This is the round-in-flight-across-the-upgrade
  pin.

Verify: `go test -count=1 ./internal/relevo`

### Step 5 -- `internal/serve` tests, and the served pre-rename round

Depends on: step 3. Deliverable: kind `stream` serves either name.

Edits:
- `serve_test.go:1588` and `2442`: `rt.Store.RunnerStreamPath("api", 1)`.
- `helpers_test.go:303`: the suffix check becomes `"001-runner.jsonl"` (the scriptRunner writes
  the round's stream at `spec.LogPath`, which is now the new name).

New test in `serve_test.go`: `TestRoundFileStreamServesAPreRenameRound` -- `setupTestEnv`,
`sendRound`, then remove the stream the fake round start left
(`os.Remove(rt.Store.RunnerStreamPath("api", 1))`, tolerating not-exist) and write the round's
stream with `rt.Store.BuilderStreamPath("api", 1)` only; `finishRound`, then GET
`.../rounds/1/files/stream` and assert `200` and the exact body; then GET
`.../rounds/1/files/log` and assert the *rendered* body (this is the `RoundTranscript` half), and
assert `resp.Header.Get(remote.HeaderFileSize)` equals the rendered stream's length.

Verify: `go test -count=1 ./internal/serve`

### Step 6 -- `internal/ui` tests and the cockpit's pre-rename read

Depends on: step 3. Deliverable: the tab renders a pre-rename stream and labels a new one.

Edits:
- `fetch_test.go:604`: `st.RunnerStreamPath("webshop", 2)`; the `logName` wants at 619-620 and 632
  become `002-runner.jsonl (rendered)`.

New test in `fetch_test.go`: `TestFetchTerminalRendersThePreRenameStream` -- the same shape as
`TestFetchTerminalHeadlessRendersTheStream`'s first block, writing the stream with
`st.BuilderStreamPath("webshop", 2)` only, and asserting the body and
`logName == "002-builder.jsonl (rendered)"`.
`hist_test.go:249` stays exactly as it is: it is an old-round fixture and must keep passing.

Verify: `go test -count=1 ./internal/ui`

### Step 7 -- Ingest reads a sealed stream under either name

Depends on: step 1 (and is independent of steps 3-6). Deliverable: `internal/ingest` green.

Edits:
- `internal/ingest/dedupe.go:320-326`: `builderStreamPathBase(round)` stays (it becomes the legacy
  base; sharpen its comment), and add `runnerStreamPathBase(round)` returning
  `filepath.Base(memberStore.RunnerStreamPath("x", round))`.
- Add `sealedStream(d *db.DB, record db.Record, round int) (name string, body []byte, found bool, err error)`
  next to them, implementing section 4.4.
- `deriveTranscripts` (lines 331-350): replace the `RoundFileGet(record.ID, streamBase)` block with
  `sealedStream(...)`, keeping every existing error wrap and the `roundFileLines(name, body)` call.
- `streamLinesCover` (lines 236-248): same replacement for its stream block; the log block is untouched.

Tests (`dedupe_test.go`):
- change one round in the "same/altered/stranger" block to the new name -- line 128's
  `putRoundFile(t, d, recordID, "004-builder.jsonl", 4, raw)` becomes `"004-runner.jsonl"` -- so
  both names are exercised in one run, and change line 341's
  `putRoundFile(t, d, recordID, "003-builder.jsonl", 3, line+"\n")` to `"003-runner.jsonl"`.
- leave every other `NNN-builder.jsonl` fixture in the file alone: they are the old-round cases.
- add `TestStreamLinesCoverReadsThePreRenameStream`: mirrors an existing `streamLinesCover` case
  with the sealed row under `NNN-builder.jsonl` only, and asserts the `covered`/`renamed` result.

Verify: `go test -count=1 ./internal/ingest`

### Step 8 -- The mirror and usage sweep

Depends on: steps 3-7. Deliverable: no package outside `internal/store` names the stream accessor.

Verify: `go test -count=1 ./internal/relevo ./internal/serve ./internal/ui ./internal/ingest ./internal/usage ./internal/store`
and
`grep -rn 'BuilderStreamPath' --include='*.go' . | grep -v '_test.go'`
which must print only `internal/store/paths.go` and `internal/ingest/dedupe.go`.
Fix whatever the loop reports the same way as steps 4-7.

### Step 9 -- Docs

Depends on: nothing (do it after the code so the wording can be checked against behaviour).

Edits:
- `CLAUDE.md:28-31`: the bullet names `~/.local/state/relevo/<name>/NNN-runner.jsonl`, and adds
  that a round from before the rename is `NNN-builder.jsonl` (readers fall back), beside the
  existing `NNN-builder.log` sentence.
- `README.md:596`: `NNN-builder.jsonl` -> `NNN-runner.jsonl`.
- `README.md:696`: the stall signal's `(NNN-builder.jsonl)` -> `(NNN-runner.jsonl)`.
- `README.md:799`: the catch-up sentence's `(NNN-builder.jsonl)` -> `(NNN-runner.jsonl)`.
- Change nothing else in the README: `NNN-builder.log` mentions (line 596's "open-round files" and
  every log sentence) stay.

Verify: `grep -rn 'NNN-builder.jsonl' CLAUDE.md README.md` prints only lines that say it is the
pre-rename name of a round; `grep -c 'NNN-runner.jsonl' README.md` is at least 3.

### Step 10 -- Save this plan, run the full check, mutation-check, commit

Depends on: steps 1-9.

1. Write this plan to `docs/plans/2026-09-26-runner-stream-name.md` (same content, same section
   order).
2. Run `make check` (once). Fix what it reports inside this plan's scope; if a fix would leave the
   scope, halt and report instead.
3. **Mutation check**, three mutations, each reverted before the next:
   - M1: make `StreamPath` `return s.RunnerStreamPath(name, round)` unconditionally. Expected
     failures: `TestStreamPathResolvesTheRoundStream` (the old-only disk and old-only sealed
     cases), `TestStreamDrained` (the two pre-rename rows),
     `TestStartRoundKeepsWritingAPreRenameRoundStream`,
     `TestRoundFileStreamServesAPreRenameRound` (the `stream` half),
     `TestFetchTerminalRendersThePreRenameStream`.
     Command: `go test -count=1 -run 'TestStreamPathResolvesTheRoundStream|TestStreamDrained|TestStartRoundKeepsWritingAPreRenameRoundStream|TestRoundFileStreamServesAPreRenameRound|TestFetchTerminalRendersThePreRenameStream' ./internal/store ./internal/relevo ./internal/serve ./internal/ui`
   - M2: drop `BuilderStreamPath` from `RoundTranscript`'s two-name loop. Expected failures: the
     `the pre-rename stream name` subtest, `TestRoundFileStreamServesAPreRenameRound` (the `log`
     half), `TestFetchTerminalRendersThePreRenameStream`.
   - M3: drop the legacy base from `sealedStream`. Expected failure:
     `TestStreamLinesCoverReadsThePreRenameStream`.
   Revert each precisely; `git diff` must be clean of the mutation before the next.
4. `git status` must show only this plan's files plus `docs/plans/2026-09-26-runner-stream-name.md`.
   Commit them together: `refactor(store): write a round's stream as NNN-runner.jsonl`. The commit
   body states that the wire kind stays `stream`, so old and new client/server pairs interoperate,
   and that stored rounds keep the old name.
5. Report: the changed paths, the two `grep` outputs from step 8, the `make check` result, the
   three mutation results, and any test ported from the old name to the new.

Halt and report, with the step number, rather than improvising, if: a step's edit site does not
exist as written, a code path contradicts this plan, `make check` fails for a reason outside this
plan's scope, or a test needs an exclusion or a baseline change to pass.
