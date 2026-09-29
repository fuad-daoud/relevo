# builder.log spike: render the stream instead of keeping a second file

Date: 2026-09-25. HEAD b37a60a6. This is a spike: static reading of every writer and reader, plus
measurements on the live `relevo.db` (opened read-only) and the live spools.
It follows the file-writes spike (2026-09-24-file-writes-spike.md, §6 B2), which was dropped
"for now" because the rendered log is small. This spike asks a different question: does the
log hold anything the stream cannot give us?

## 1. Findings

1. **The log is almost entirely a copy of the stream.**
   - 215 of 244 sealed rounds are exact: every line of the rendered `NNN-builder.jsonl`
     appears in `NNN-builder.log`, in order.
   - Beyond the render, those logs hold:
     - 20 lines of true stderr (2,461 bytes, 0.07% of log bytes);
     - 8 marker lines;
     - duplicate and wrong-kind lines from the switch bug (item 3).
   - The other 29 rounds come from before streams existed (#168) or from the renderer
     before #181.
   - Sizes: 244 logs total 3.7 MB (p50 12 KB, max 74 KB). 219 streams total 158 MB (p50
     554 KB, max 5.2 MB).
2. **The only stderr that matters is agy's usage-limit text.**
   - The stderr lines are:
     - `error: Individual quota reached…` ×4;
     - `AGY_ERROR: {"short_error":"RESOURCE_EXHAUSTED (code 429)…` ×4;
     - agy's "root agent idle; waiting…" ×11;
     - one systemd scope warning.
   - Every real usage-limit detection in the data (7 rounds) comes from stderr. Rendering
     the stream catches none of them today, because `renderAgy` prints `result: ERROR` and
     drops the `result.error` field that carries the same text.
   - Replaying the 7 agy ERROR results, 5 of which are limits, a scan of the last 40
     rendered lines catches:
     - 0/5 with the current render;
     - **5/5** once `result.error` is rendered as an `error: …` line.
   - The 2 non-limit errors stay unmatched either way.
3. **Bug: a mid-round switch renders the whole stream again, with the new kind.**
   - `switchBuilder` installs a fresh Endpoint (`resolveBuilder`, bind.go ~653).
     `StreamRound` and `StreamOffset` become 0, and `startProcess` resets the cursor
     (headless.go ~234-238).
   - The drain then re-renders from byte 0, using the *new* harness kind for the old
     harness's lines.
   - In all 4 sealed rounds that switched away from agy, this added 1,151 `[type]` junk
     lines. oc-tui-a/3 also got 193 duplicated lines. The live spool oc-fix/001 shows it too.
   - The comment at types.go ~118-123 and `TestStartRoundOnTheSameRoundKeepsTheCursor`
     cover `startProcess`, not this path.
   - Two more streams mix harnesses with no switch recorded.
   - The stream has no record of which process, and so which kind, wrote which bytes.
     `Endpoint.StreamStart` keeps only the latest spawn.
4. **Bug: the log can lose its last lines.**
   - In 7 rounds, 3 of them remote, the log is missing the stream's last 1-3 lines: the
     final message, `relevo-rusage:` and `relevo-exit:`.
   - The log was sealed or mirrored before the last drain. The stream is the more complete
     record.
5. **Bug: `relevo status` reads the exit code from the log.**
   - `headlessStatus` calls `ExitCode(…, e.LogPath)` (headless.go ~1006). That works only
     because the drain copies `relevo-exit:N` into the log.
   - An undrained trailer, or a later `stopped:` marker, reads as code unknown.
   - The served `LiveView.ExitCode` inherits the same problem. The reconcile path reads
     the stream (headless.go ~616).
6. **Markers are mostly duplicates of ledger events.**
   - The live data has 9 markers in sealed logs: 4 rate-limited, 5 switched. The DB `event`
     table holds 24 `switch` events with the same reason text.
   - By site:
     - switch writes `KindSwitch`;
     - send `--builder` writes `KindPick`;
     - lost-to-restart writes `KindSwitch`;
     - `relevo stop` writes `KindStop`;
     - rate-limited writes a provider-ledger entry, and its switch writes `KindSwitch`.
   - Not in the binding ledger:
     - `stopped: done`;
     - `stopped: unbind`;
     - the rate-limited marker on the report-on-disk path, where the note is in the
       payload instead.
   - Nothing parses markers. The limit and denial scans do re-read them, and a
     `rate-limited: <line>` marker can match its own pattern again.
   - The 2026-09-24 remote-live-parity spec (§ line ~69) counts on markers reaching remote
     clients through the log.
7. **Markers cannot move into the stream.**
   - A marker written after the supervisor's `relevo-exit:` trailer breaks three readers:
     - `ExitCode`: the last line must be the trailer;
     - usage `streamClosed`: it waits 5 s;
     - `StreamDrained` / `trailerLinesOnly`: it blocks the seal for up to 1 h.
   - `stopProcess` and the exited-path `gateOnLimit` both write after the trailer.
8. **stderr can join the stream, as it already does for consults and gates.**
   - Consults (#420) and gates already point stderr at their stream file.
   - Every stream reader either skips non-`{` lines (usage, SessionID, FinalText) or
     reads only the last line (ExitCode). stderr ends before the supervisor's trailer, so
     it never lands after it.
   - Costs:
     - stderr writes bump the stream's mtime, so they count as activity for
       stall/progress (#252);
     - a partial stderr write in the middle of a JSON line would corrupt that one event.
       The consult and gate precedent has not shown this.
9. **Render cost is small.**
   - `transcript.Render(kind, line)` is pure and per-line. It has no cache and needs none.
   - Full render of the largest stream (5.2 MB): about 50 ms. Reading it: about 1 ms.
     Rendering all 158 MB: about 2.2 s.
   - Reading the last 40 rendered lines from the end needs 98 KB of stream at p50, 161 KB
     at p90 and 271 KB at most. So a fixed 64 KiB tail is **not** enough; a tail reader
     must keep reading backwards until it has n rendered lines.
10. **The remote tail offset is a byte offset into the server's rendered log** (#442,
    serve/rounds.go ~381-420, remote.go `mirrorLog` ~634-686).
    - It stays valid only if the rendered prefix never changes.
    - Rendering the stream with per-segment kinds, complete lines only, from an append-only
      stream, is prefix-stable.
    - The re-render bug (item 3) breaks prefix stability today.

## 2. Readers and what they need

| Reader | Needs | How often | Reads through |
|---|---|---|---|
| exit entry payload (headless.go ~455) | last 20 lines | once per exit without report | `logTail` (`os.ReadFile`) |
| limit scan (headless.go ~647, ~818; limit.go `limitText`) | last 40 lines | once per exit / timeout | `logTail` |
| denial scan (headless.go ~686) | last 40 lines | once per exit without report | `logTail` |
| `headlessStatus` tail + exit code (~977-1013) | last 3 lines + last line | every `status`, statusline, UI tick (2 s), MCP status, served live view (2 s TTL) | `logTail`, `ExitCode(log)` |
| UI terminal tab (ui/fetch.go ~273-420) | whole, last 5000 lines | every visible UI tick | `Store.ReadFile` |
| serve `files/log?from=` (serve/rounds.go ~361-420) | bytes from offset | every client tick per running remote round | `Store.ReadFile` |
| client mirror + catchUp (remote.go ~634-686, ~1210-1245) | writes the local copy | every tick / once at close | files |
| `show --transcript` (show.go ~196, ~269) | whole | per CLI run / hist tab | `Store.ReadFile`, `ArchivedFile` |
| escape halt text (escape.go ~112) | path only | on escape | none |
| ingest dedupe (dedupe.go ~236-289) | sealed rows | once, legacy | round_file |

## 3. Design options

**A. Render the stream on demand, and stop writing the log.** *Recommended.*
- stderr joins the stream (ProcSpec `LogPath == StreamPath`, as consults do).
- Every reader renders what it needs from `NNN-builder.jsonl`, with the right kind per
  segment.
- Markers are dropped. Their events live in the ledger, and the three missing ones get
  ledger entries.
- It removes one file per round and one write path (the drain render). It fixes items 3,
  4 and 5 by construction.

**B. Keep the rendered log, but as a `round_file` row.**
- It needs a transaction for every marker writer (three have none), and SQLite rewrites
  the whole blob every tick.
- It keeps the duplicated data and none of the bugs get fixed.
- Rejected.

**C. Keep the file and fix the bugs.**
- It is cheap, but it keeps a second copy of the stream and the drain's write path.
- The fixes in round 1 below are the part of C worth doing anyway.

## 4. Design A in detail

### 4.1 Stream segments: which process, and so which kind, wrote which bytes

- New Endpoint field: `StreamSegments []StreamSegment` (`json:"stream_segments,omitempty"`).
  Each segment is `{Start int64, Kind string}`.
- `startProcess` appends `{Start: size of the stream at spawn, Kind: b.Builder.Kind}`.
  `StreamStart` already records that size.
- A new round starts a new list.
- A switch or resume **keeps** the stream cursor and the segment list. This is the fix for
  item 3: `switchBuilder` carries `StreamRound`, `StreamOffset` and `StreamSegments` over
  into the fresh Endpoint.
- The field is omitempty, so an older binary still loads the binding. It follows the
  `BindingFormat` precedent of never-stamped formats 3/4 (store/format.go).
- A stream with no segments (older rounds, sealed history) renders with the round's
  recorded harness. That is today's behaviour, and it is wrong only for the 6 known
  mixed rounds.

### 4.2 Rendering helpers (internal/relevo, pure where possible)

- `renderStream(stream []byte, segs []StreamSegment, fallbackKind string) []string`:
  complete lines only, each rendered with the kind of the segment that contains its
  offset.
- `renderedTail(path string, segs, kind, n int) (string, error)`:
  - reads backwards from EOF in growing chunks (64 KiB, doubling, cap 4 MiB) until it has
    n rendered lines, or reaches the start;
  - drops a partial first line;
  - uses `Store.ReadFile` semantics for sealed rounds.
- `renderAgy` renders `result.error`, when present, as its own line `error: <first line>`
  after `result: ERROR` (item 2).

### 4.3 Readers switch to the stream

- `logTail(b.Builder.LogPath, n)` call sites become `renderedTail` of the stream. That
  covers the exit payload, limit and denial scans, and the status tail.
- `headlessStatus` reads the exit code from the stream. This is item 5, and it belongs in
  round 1.
- UI terminal tab and `show --transcript`:
  - if the round has a `NNN-builder.log` (on disk or sealed), show it, because it is
    history;
  - otherwise show `renderStream`.
  - The UI's source label becomes `NNN-builder.jsonl (rendered)`.
- serve `files/log?from=`:
  - if the round has a builder.log, serve that (old rounds);
  - otherwise serve `renderStream` of the stream, with the offset indexing into the
    rendered bytes.
  - Prefix stability holds (item 10), so the #442 client mirror keeps working unchanged.
- The client mirror keeps writing the **client's** local `NNN-builder.log` for remote
  rounds. That is a separate follow-up; it is out of scope here.

### 4.4 Writers stop

- `startProcess`: `LogPath: streamPath` (stderr into the stream). `Endpoint.LogPath`
  keeps pointing at the old name for display only, or is set to the stream path; the
  plan decides.
- `drainStream` stops appending. It still advances the cursor and captures
  `StreamSessionID`, so `StreamDrained` and resume keep their meaning.
- Markers:
  - remove `appendLogMarker` and its five call sites;
  - add a ledger entry for `stopped: done` and `stopped: unbind`: `KindStop`, the same
    shape `relevo stop` uses;
  - the rate-limited marker on the report-on-disk path is already in the unmarked payload.

### 4.5 Rounds in flight across the upgrade

- A builder started by the old binary writes stderr to `builder.log` through an inherited
  fd.
- The new binary recognises such a round by `b.Builder.LogPath == BuilderLogPath(name,
  round)` with no `StreamSegments`. For that round it keeps today's behaviour:
  - drain into the log;
  - readers use the log.
- Its next round uses the new layout. No migration is needed.

## 5. Suggested rounds

**Round 1: fixes that stand on their own. No layout change; builder.log is still written.**
- `renderAgy` renders `result.error` (item 2).
- `switchBuilder` keeps the stream cursor. Add `StreamSegments` and render the drain per
  segment (item 3).
- `headlessStatus` exit code from the stream (item 5).
- Tests, including a replay of the 7 real agy ERROR results as fixtures.
- The `renderStream` / `renderedTail` helpers move to round 2, where they get their first
  caller.

**Round 2: the switch to design A.**
- stderr into the stream.
- Readers render the stream, with the builder.log fallback for history and in-flight
  rounds.
- The drain stops writing.
- Markers go, and `done`/`unbind` get ledger entries.
- Serve renders for `files/log`.

**Later, separate:** the client's local mirror file for remote rounds, and dropping the
`StreamDrained` seal blocker if the drain no longer writes anything.

## 6. Evidence

- Measurement program and results, kept outside the repo: session scratchpad
  `spike-builderlog/results/`:
  - 1-sizes;
  - 2-aggregate and per-round;
  - 3-pattern-summary and 3b-limit-replay;
  - 4-render-cost and 4b-tail-bytes;
  - `spikelog-main.go.txt`.
- The DB was opened with `sqlite3 -readonly`. No row was written.
