# builder.log round 2a: readers render the stream; segments per round; the #452 missing-ref fix

Date: 2026-09-25. Base: origin/main (7db0ec7d or later).
Spec: docs/specs/2026-09-25-builder-log-design.md (§4.2, §4.3). Round 1 (#469) is merged.
Round 2 is split into two builder rounds on one branch and one PR:

- **2a (this plan):** every reader of the builder log learns to render the round's stream
  when there is no `NNN-builder.log`. **The log is still written exactly as today**, so every
  live round still has a log and behaviour does not change. The stream path is exercised
  by tests only.
- **2b (next plan):** stderr goes into the stream, the drain stops writing the log, and the
  markers go. That is when the stream path goes live.

**Stop rather than improvise.** If a step is impossible as written, if the code contradicts
a fact stated here, or if an existing test outside §7.3 fails, halt and report it. Do not
bend a test to make it pass.

## 1. System overview

After this round:

1. **Each round records its stream segments as a round file.**
   - Every spawn writes `NNN-builder-segments.json` (the `[]store.StreamSegment` of the
     round) straight into `round_file` with `Tx.PutRoundFile`.
   - A finished round can then be rendered with the right harness per process, even after
     the endpoint moved on to a later round.
   - Round 1 keeps segments only on the live endpoint, which is not enough once the log
     stops being written.
2. **One function answers "what did the builder write in round N"**:
   `RoundTranscript`.
   - It returns the round's `NNN-builder.log` when one exists: history, and rounds from
     before 2b.
   - Otherwise it returns the round's stream rendered per segment.
   - It is used by `relevo show --transcript` (live and archived), the UI terminal tab and
     serve's `files/log` route.
3. **One function gives the tail of the current round**: `builderTail`.
   - It reads the log when the endpoint writes one (`LogPath == BuilderLogPath`, today
     always).
   - Otherwise it reads the rendered stream's last n lines.
   - It replaces every `logTail(b.Builder.LogPath / e.LogPath, n)` call: exit payload,
     limit scans, denial scan, status tail.
4. **The #452 fix.** `bindingRefCandidates` no longer proposes `refs/heads/relevo/<name>`
   when that branch does not exist in the repo. Today that makes `unbind --done` print a
   `check failed: … malformed object name …` line for every remote binding.

## 2. File structure

```
internal/store/store.go             + BuilderSegmentsPath
internal/relevo/transcript.go       NEW: renderStream, streamTail, builderTail, RoundTranscript, roundSegments
internal/relevo/transcript_test.go  NEW: N1-N6
internal/relevo/headless.go         startProcess writes the segments row; logTail call sites -> builderTail
internal/relevo/limit.go            limitText -> builderTail
internal/relevo/escape.go           halt text names the show command, not a path
internal/relevo/show.go             ShowTranscript via RoundTranscript (live + archived)
internal/relevo/refclean.go         bindingRefCandidates checks the head ref exists (RefSHA)
internal/relevo/refclean_test.go    N9
internal/relevo/show_test.go        N8
internal/ui/fetch.go                terminal tab via RoundTranscript
internal/ui/fetch_test.go           N10
internal/serve/rounds.go            files/log via RoundTranscript
internal/serve/serve_test.go        N11
docs/plans/2026-09-25-builder-log-r2a.md   this plan, verbatim
```

If a new file name collides with an existing one (`transcript.go` in internal/relevo), use
`buildertranscript.go` / `buildertranscript_test.go` instead and say so.

## 3. Data structures

- **`Store.BuilderSegmentsPath(name string, round int) string`**
  - Returns `roundFile(name, round, "builder-segments", ".json")`, i.e.
    `<binding dir>/NNN-builder-segments.json`.
  - Doc comment: "the round's []StreamSegment as JSON, written by startProcess straight
    into round_file (never a file on disk). Read it with ReadFile."
  - Put it next to `BuilderStreamPath`.
- **The row's body** is `json.Marshal(b.Builder.StreamSegments)`, e.g.
  `[{"start":0,"kind":"agy"},{"start":48213,"kind":"claude"}]`.
- No struct changes. No `BindingFormat` change: the segments row is a round file, not
  binding JSON.

## 4. Interfaces and contracts (internal/relevo unless noted)

### 4.1 `renderStream(stream []byte, segs []store.StreamSegment, fallback string) []byte`

- Renders every **complete** line of `stream`, i.e. up to the last `'\n'`. A trailing
  partial line is ignored.
- Each line is rendered with `transcript.Render(segmentKind(segs, off, fallback), line)`,
  where `off` is the line's absolute offset in `stream`.
- The output is byte-for-byte what `drainFile` + `appendLines` would have appended to an
  empty log for the same stream: each non-empty render result is appended as
  `strings.Join(lines, "\n") + "\n"`, in order.
- It must equal the drain's output, because serve's `files/log?from=` offsets and the
  client mirror (#442) depend on the rendered prefix being stable and identical.
- Pure.

### 4.2 `streamTail(path string, read func(string) ([]byte, error), segs []store.StreamSegment, fallback string, n int) string`

- Returns the last `n` lines of `renderStream(<whole file>, segs, fallback)`, where
  "lines" are `'\n'`-separated after rendering, since a rendered string may itself contain
  newlines. There is no trailing newline. The result is "" when the file is missing or
  empty, or n ≤ 0. These are exactly `logTail`'s conventions (headless.go:478).
- **Efficiency:**
  - When `path` exists on disk, read backwards with `os.Open` + `ReadAt`:
    - start with the last 64 KiB and double the window until it yields ≥ n rendered
      lines or covers the whole file;
    - drop the first, partial line of any window that does not start at 0;
    - render the window's lines with their absolute offsets, so segment kinds stay
      correct.
  - When the file is not on disk (sealed), read it whole with `read` (`Store.ReadFile`)
    and render it.
  - The spike measured up to 271 KB of stream for 40 rendered lines, so a fixed 64 KiB
    window is wrong.

### 4.3 `builderTail(rt Runtime, b store.Binding, n int) string`

- Decides between the log and the stream:
  - If `b.Builder.LogPath != ""` and `b.Builder.LogPath == rt.Store.BuilderLogPath(b.Name, b.Round)`,
    return `logTail(b.Builder.LogPath, n)`. That is today's behaviour, and in 2a it is
    always the case.
  - Otherwise return
    `streamTail(rt.Store.BuilderStreamPath(b.Name, b.Round), rt.Store.ReadFile, b.Builder.StreamSegments, b.Builder.Kind, n)`.
- **Replace these call sites**:
  - headless.go ~698 and ~869 (`gateOnLimit(…, logTail(b.Builder.LogPath, limitScanLines), …)`);
  - headless.go ~737 (`matchDenial(logTail(…))`);
  - limit.go:322 (`limitText`);
  - headless.go ~1035 (status tail; keep its `if e.LogPath != ""` guard, and call
    `builderTail(rt, b, statusTailLines)`).
- **Do not change** `exitEntry`'s signature: headless.go ~506 computes the payload with
  `logTail(logPath, …)`. Change that one line to take the payload from the caller:
  - add a `payload string` parameter, and have the caller at ~743 pass
    `builderTail(rt, b, logTailLines)`;
  - update exitEntry's other callers, if any, the same way.

### 4.4 `roundSegments(live store.Endpoint, round int, read func(string) ([]byte, bool, error), segPath string) []store.StreamSegment`

- If `live.StreamRound == round` and `len(live.StreamSegments) > 0`, return
  `live.StreamSegments`.
- Else, if `read(segPath)` finds it, return the JSON-decoded segments.
- Else nil. A decode error is logged with `slog.Warn` and returns nil.

### 4.5 `RoundTranscript` (exported)

```
func RoundTranscript(st *store.Store, name string, round int, live store.Endpoint,
    read func(path string) ([]byte, bool, error)) (text []byte, source string, found bool, err error)
```

- `read` returns (data, found, err): not-found is `found=false, err=nil`, which is how
  `readFileOrMissing` and `ArchivedFile` report a miss.
- In order:
  1. Read `st.BuilderLogPath(name, round)`. If found, return it with
     `source = filepath.Base(that path)`, e.g. `003-builder.log`.
  2. Read `st.BuilderStreamPath(name, round)`. If found, compute
     `segs := roundSegments(live, round, read, st.BuilderSegmentsPath(name, round))` and
     return `renderStream(data, segs, live.Kind)`, with
     `source = filepath.Base(streamPath) + " (rendered)"`.
  3. Otherwise `found=false`.
- A read error returns it as is.
- The live and archived `read` adapters: live wraps `st.ReadFile` (a not-exist error means
  not found); archived wraps `st.ArchivedFile(recordID, filepath.Base(path))`.
  `showArchived` already has the second one (show.go ~266-276). Add a small adapter where
  needed.

### 4.6 startProcess writes the segments row (headless.go ~233-285)

- After the segment append/replace from round 1, add
  `tx.PutRoundFile(b.Name, b.Round, rt.Store.BuilderSegmentsPath(b.Name, b.Round), <json>)`.
- A failure is `slog.Warn("record stream segments", …)`, **never** a spawn failure.
  Tests with no saved record hit `ErrNotFound` there, which is fine.
- If `tx` can be nil on some caller path (check every caller of startProcess), guard it.

### 4.7 Readers switch to RoundTranscript

- **show.go ~196-197 (`case ShowTranscript`):**
  - `showSections` gains the endpoint needed for `live`. Add a `live store.Endpoint`
    parameter. `showLive` passes `b.Builder`; `showArchived` passes `ab.Binding.Builder`.
  - It also needs a bytes read adapter next to its existing string `read`: the simplest
    is a second parameter `readBytes func(string) ([]byte, bool, error)` passed by both
    callers.
  - `ShowTranscript` sets `res.Text` and `res.Missing` from
    `RoundTranscript(rt.Store, name, round, live, readBytes)`.
  - The DB-only fallback (`showDB`, ~358) is unchanged.
- **ui/fetch.go fetchTerminal (~330-400), headless branch:**
  - Past round (~340) and current round (~360-385): replace the `logTab(… logPath)` reads
    with `relevo.RoundTranscript(rt.Store, name, r, b.Builder, <live read adapter>)`.
    - `r` is `round` for a past round.
    - For the current round, `r` is `b.Builder.StreamRound` when it is non-zero, else
      `round`.
  - Build the tab from the returned text, with `logName = source`.
  - Refactor `logTab` into `transcriptTab(key string, body []byte, logName string) tabMsg`
    (same trimming and 5000-line cap), and keep `logTab` as a thin wrapper if other
    callers need it.
  - The empty-prose cases keep their exact wording:
    - "terminal is live; round N left no log";
    - "headless builder; no round has run yet, so there is no log";
    - "log not written yet: <path>". Use `rt.Store.BuilderStreamPath(name, r)` as the path
      when there is no log.
  - The **remote-builder branch (~390-400) is unchanged.**
- **serve/rounds.go handleRoundFile (~361-420), `case "log"`:**
  - Instead of `rt.Store.ReadFile(BuilderLogPath)`, call
    `relevo.RoundTranscript(rt.Store, name, n, b.Builder, <live read adapter>)` and use
    its text as `data`. Not found gives the same 404 "file not found".
  - The size and from headers and the slicing (~406-420) are unchanged, and operate on
    the rendered bytes.
  - The closed-round rule is unchanged: "log" is served for running rounds.
- **escape.go ~112-116:** the halt text's `see %s` with `b.Builder.LogPath` becomes
  `see ` + `showCommand(b.Name, b.Round, "transcript")`. Adjust the format arguments.

### 4.8 The #452 fix (refclean.go `bindingRefCandidates` ~47-49)

- Before `add("refs/heads/relevo/" + b.Name)`:
  1. Call `_, ok, err := rt.Git.RefSHA(ctx, dir, "refs/heads/relevo/"+b.Name)`.
  2. If `err != nil`, still add it, so cleanRefs reports `check failed`.
  3. If `!ok`, skip it silently.
- The `ListRefs` refs need no check: they exist by construction.
- Do not touch `SweepRefs`. It only lists existing refs.

## 5. Pseudocode

```
startProcess:  ... segments (round 1) ...; PutRoundFile(NNN-builder-segments.json, json(segs)) (warn on error)
builderTail(b,n):   LogPath == BuilderLogPath(name, b.Round) ? logTail(LogPath,n) : streamTail(stream(b.Round), segs, Kind, n)
RoundTranscript:    log found ? log : stream found ? renderStream(stream, roundSegments(...), live.Kind) : not found
serve files/log:    data = RoundTranscript(...).text  -> headers + from-slice as today
```

## 6. Error handling

- No new error types.
- RoundTranscript returns read errors. A segments decode error falls back to nil segments
  and logs a warning.
- A segments row write failure is only a warning.

## 7. Ordered implementation steps

### 7.0 Working efficiently

- Read these in one parallel batch, and do not re-search what this plan locates:
  - headless.go 225-300, 370-520, 690-750, 860-875 and 1020-1060
  - limit.go 315-330
  - escape.go 100-125
  - show.go 150-290
  - ui/fetch.go 260-420
  - serve/rounds.go 330-425
  - refclean.go
  - store/store.go 180-205
  - the tests named in §7.3
- **Line numbers in this plan are from origin/main 7db0ec7d.** Re-grep if they have moved.
- Make every change to a file in one edit call.
- Iterate with these focused commands:
  - `go test ./internal/relevo/ -run 'Transcript|Tail|RenderStream|Segments|Show|Limit|Denial|Status|Exit|Escape|RefCandidates|GC' -count=1`
  - `go test ./internal/ui/ -run 'Terminal|Fetch|Source' -count=1`
  - `go test ./internal/serve/ -run 'RoundFile|Files' -count=1`
- Full check once at the end:
  - `make check`. If a hook blocks it, run its steps directly;
  - `gofmt -l $(git ls-files '*.go')`, and **paste its empty output**.
- A cmd/relevo test must not spawn a harness or reach the network. This round adds no CLI
  test.

### 7.1 Steps

1. **Segments row.** `BuilderSegmentsPath` and the startProcess write (§3, §4.6), with N5.
2. **Rendering.** `renderStream` and `streamTail` (§4.1, §4.2), with N1, N2.
3. **Tail.** `builderTail` and its call sites (§4.3), with N3.
4. **Transcript.** `roundSegments` and `RoundTranscript` (§4.4, §4.5), with N4 and N6.
5. **Readers.** show, UI, serve and escape (§4.7), with N8, N10 and N11.
6. **The #452 fix** (§4.8), with N9.
7. **Full check**, then the §7.4 mutations one at a time, restoring after each.
8. **Save this plan** verbatim at `docs/plans/2026-09-25-builder-log-r2a.md`.
9. **Commit and push, no PR.**
   - One commit: `refactor(transcript): readers render the round's stream when it has no
     builder.log; stream segments recorded per round; unbind skips a missing relevo branch`.
   - Push with `git push -u origin <branch>`. Do **not** open a PR: round 2b opens it.

### 7.2 New tests

- **N1 `TestRenderStreamMatchesTheDrain`:**
  - Build a stream with an agy segment `{0,"agy"}`, then a claude segment `{N,"claude"}`,
    ending in a partial line. Take lines from the transcript package's testdata.
  - Run `drainStream` over it into an empty log, with the same segments on the endpoint.
  - `renderStream(stream, segs, "claude")` must equal the log's bytes exactly.
  - Then append more complete lines to the stream: the old output must be a prefix of the
    new one. This is prefix stability.
- **N2 `TestStreamTail`:**
  - A stream over 300 KiB whose rendered form has multi-line entries (claude text with
    embedded newlines).
  - For n = 1, 3, 40, `streamTail` equals the last n lines of `renderStream`, split on
    `'\n'`.
  - It is correct across a segment boundary inside the window.
  - It returns "" for a missing file.
  - The same result through the `read` fallback, when the path is not on disk and `read`
    serves the bytes.
- **N3 `TestBuilderTailLogOrStream`:**
  - With `LogPath == BuilderLogPath` and a log file, it returns the log's tail even when
    the stream says something else.
  - With `LogPath` set to the stream path (as in 2b), it returns the rendered stream's
    tail.
  - With both absent, it returns "".
- **N4 `TestRoundTranscript`:**
  - log present gives the log and source `NNN-builder.log`;
  - no log, stream plus segments row gives the rendered stream with per-segment kinds, and
    source `NNN-builder.jsonl (rendered)`;
  - no log, no row, live endpoint segments for that round gives the kinds used;
  - no row and no live segments uses the fallback kind;
  - nothing gives `found=false`;
  - an archived-style `read` adapter works.
- **N5 `TestStartProcessRecordsSegmentsRow`:**
  - With a saved binding, after startProcess, `Store.ReadFile(BuilderSegmentsPath)`
    decodes to the endpoint's segments, and nothing is on disk at that path.
  - A second spawn in the same round overwrites it with two segments.
  - Without a saved record, startProcess still succeeds (the warning only).
- **N6 `TestRoundTranscriptOfASealedSwitchedRound`:**
  - A round whose stream has two segments, and whose endpoint has moved to round+1
    (StreamRound = round+1, other segments).
  - The transcript of the old round uses the **row's** segments, not the live ones.
- **N8** (show_test.go):
  - `relevo show --transcript` of a live round with a stream and no log returns the
    rendered stream.
  - The archived variant returns the rendered sealed stream.
  - Existing show tests with a seeded log are unchanged.
- **N9 `TestBindingRefCandidatesSkipsAMissingBranch`:**
  - fakeGit `refSHA` map without `refs/heads/relevo/<n>`: no head candidate. The listed
    refs are still returned.
  - Map with the key: the head candidate is present.
  - `refSHAErr` set: the head candidate is present.
  - Cover the remote shape (Remote builder, `Branch` "feature/x") and the relevo-cut shape.
- **N10** (ui/fetch_test.go):
  - A headless binding whose round has a stream and no log: the terminal tab body is the
    rendered stream, and `logName` is `NNN-builder.jsonl (rendered)`.
  - Existing tests with seeded logs are unchanged.
- **N11** (serve/serve_test.go, next to `TestRoundFileLogFrom`):
  - A served binding with a stream and no log: `GET files/log` returns the rendered bytes,
    with `X-Relevo-Size` equal to their length.
  - `?from=k` returns the suffix.
  - Appending complete lines to the stream and fetching `?from=<old size>` returns exactly
    the new rendered lines. That is the #442 mirror contract.

### 7.3 Fenced tests

You may change only these existing tests:

- tests that call `exitEntry` directly, for the new `payload` parameter only. The expected
  payloads must stand.
- tests of `showSections`, if any call it directly, for its new parameters only.
- tests asserting the old `escape` halt text ending `see <log path>`: change them to the
  show command. Report each one.
- `TestBindingRefCandidates` (N3 from #452): only if its fakeGit needs a `refSHA` entry.
  The nil map defaults to ok, so it should not.

Every other existing test must pass unchanged. That includes all UI and show tests that
seed `NNN-builder.log` files, all limit, denial and exit tests that seed log text, all
remote mirror tests, and `TestRoundFileLogFrom`. Round 2a changes no behaviour for a round
that has a log. If one fails, halt.

### 7.4 Mutation checks (run each, report pass/fail, restore)

| # | Break | Must fail |
|---|---|---|
| M1 | renderStream: render every line with `fallback` | N1 |
| M2 | renderStream: include the trailing partial line | N1 |
| M3 | streamTail: read only a fixed 64 KiB window, with no doubling | N2 (n=40) |
| M4 | builderTail: always read the log | N3 |
| M5 | RoundTranscript: try the stream before the log | N4 |
| M6 | startProcess: skip the segments row | N5, N6 |
| M7 | roundSegments: prefer the live endpoint's segments even when StreamRound != round | N6 |
| M8 | bindingRefCandidates: ignore `ok` from RefSHA | N9 |
| M9 | serve `case "log"`: read the log file only | N11 |

If a mutation does not make its named test fail, report it. Do not strengthen tests
beyond this plan.

## 8. Scope check for the reviewer

- `git diff --stat` shows only the §2 files and the fenced test edits.
- No golden changes. No `BindingFormat` change.
- `NNN-builder.log` is still written; drain, stderr and markers unchanged.
- New exported names: `Store.BuilderSegmentsPath` and `relevo.RoundTranscript`.
