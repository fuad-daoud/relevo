# Headless transcript: the builder log grows as the round runs

**Issue:** #168.
**Depends on:** nothing open. #153 (log markers) and #140 (limit gate) landed;
both read or write `NNN-builder.log` and keep doing so unchanged.
**Amends:** headless spec (`2026-09-12-headless-builders-design.md`) scope
boundary ("No live log streaming" struck), §3.3 (one more round file), §3.4
(`ProcSpec.StreamPath`), §3.5 (print table). README "Headless builders": the
two files and what each holds.
**Unblocks:** #135 (its headless "output grew" signal is the size of a file
that today grows once), #142 (usage is in the stream's final event).
**Status:** implemented by this plan; opencode table pinned 2026-09-17 (#173); plan at `docs/plans/2026-09-17-headless-transcript.md`.

## 1. System overview

A headless builder runs `<harness> -p <prompt> --output-format text`. In
print mode with `text` output every harness buffers until the turn ends, so
`NNN-builder.log` is written once, at exit. While the builder works --
sixteen minutes on 2026-09-16, three earlier attempts' failures still on
screen -- `relay ui`'s terminal tab, `relay status`'s tail and the file
itself show the *previous* exit. A pane builder shows its tool calls; a
headless one is a black box until it stops.

Every harness can stream the same turn as newline-delimited JSON, one event
per line, as it happens (verified from `--help` and one live run each on
2026-09-16; opencode's run failed on auth, so only its flag is verified):

| kind | streaming form | event envelope |
|---|---|---|
| agy | `--output-format stream-json` | `{"event": "init"\|"step_update"\|"result", ...}` |
| claude | `--output-format stream-json --verbose` | `{"type": "system"\|"assistant"\|"user"\|"result"\|"rate_limit_event", ...}` |
| opencode | `run --format json` | `{"type": ..., "timestamp": ..., "sessionID": ...}` |

claude refuses `stream-json` in print mode without `--verbose` ("Error: When
using --print, --output-format=stream-json requires --verbose"); the flag
adds nothing to stdout beyond the events.

This design switches the print form to the streaming one and splits the
round's output into two files:

- **`NNN-builder.jsonl`** -- the raw stream, exactly as the harness wrote
  it, plus the supervisor's `relay-exit:N` trailer as its last line. Durable,
  never rewritten by relay. The record #142 will read usage from.
- **`NNN-builder.log`** -- what a human opens: stderr as the harness wrote
  it, one rendered line per event the human would want to see, the
  `--- relay` markers, and the trailer. Same name, same readers, same
  writers as today.

The daemon renders. Each tick, for each headless binding, it reads the
`.jsonl` past a persisted byte cursor, turns every complete line into zero
or more human lines, appends them to the `.log`, and advances the cursor.
Latency is one tick (2 s). Nothing else changes: `logTail` for the exit
entry and `status`, the `ui` terminal tab, `limitText`'s scan and
`appendLogMarker` all read or write `.log` as they do now.

### Why the daemon and not a pipe

`startRound` is called from `relay send` (`send.go`), a CLI that exits, and
from the daemon's switch path. The process is detached on purpose
(`Setsid`, no `CommandContext`) so the CLI can go. A goroutine on the stdout
pipe inside `Runner.Start` would die with the CLI. A renderer process inside
the supervisor pipeline would be a third process per builder that has to
find relay's own binary, a renderer crash would `SIGPIPE` the builder
mid-round, POSIX `sh` has no `pipefail` so the exit code would need a status
file, and it would add a subcommand during the #114 freeze. The daemon
already ticks; a cursor and a file are the whole mechanism.

### Principles kept

- **Shown to humans, never parsed for meaning** (headless spec §1). Rendering
  an event's kind and a tool's name is presentation, the same class of read
  as #140's rate-limit regex. No relay decision is taken on a rendered line
  or on an event.
- **The trailer is the exit code's only source.** It moves file, not shape:
  `ExitCode` reads the last line of the `.jsonl` by the rule it uses today.
- **Relay writes to a builder's files in whole lines, `O_APPEND`, one call
  per batch**, so the supervisor's stderr writes and relay's rendered
  writes interleave at line boundaries, never inside one.

### Scope boundary

- No `--include-partial-messages` on claude: token deltas would grow the
  `.jsonl` by orders of magnitude and add no line the renderer prints.
- No usage or cost line in the `.log`. #142 reads the `.jsonl`.
- No cursor-addressed tail of the `.log` from a shell (`relay log --builder`
  or similar): a verb-surface change, and `tail -f` works on a file that
  grows.
- No change to `make e2e`. That suite pins the pane fallback path against a
  real herdr; this design does not touch `reconcile.go`'s nudge, fingerprint
  or scrape path. A real process is exercised by `proc_test` (§7).
- `relay status` and `relay ui` do not drain. Only the daemon writes
  rendered lines, so there is exactly one writer of the cursor.

## 2. File structure

```
internal/transcript/transcript.go      Render (new package; kind + raw line -> human lines)
internal/transcript/claude.go          claude table
internal/transcript/agy.go             agy table
internal/transcript/opencode.go        opencode table
internal/transcript/transcript_test.go fixtures in testdata/<kind>.jsonl + testdata/<kind>.log
internal/harness/harness.go            Launch: print form per kind (§3.5 of the headless spec)
internal/relay/runner.go               ProcSpec.StreamPath; Runner.ExitCode reads it
internal/relay/headless.go             drainStream (new); reconcileHeadless drains first; ExitCode call site
internal/store/types.go                Endpoint.StreamRound, StreamOffset
internal/store/store.go                BuilderStreamPath
internal/proc/proc.go                  Start: stdout -> StreamPath; trailer -> StreamPath; ExitCode unchanged in shape
docs/specs/2026-09-12-headless-builders-design.md   amendments listed above
README.md                              "Headless builders"
```

## 3. Data structures and type definitions

### 3.1 Round files (amends headless spec §3.3)

```
<binding dir>/NNN-builder.jsonl   stdout of every process of the round, appended; last line "relay-exit:N"
<binding dir>/NNN-builder.log     stderr, rendered stdout, relay markers, trailer (rendered through)
```

`Store.BuilderStreamPath(name, round)` returns the first; `BuilderLogPath`
the second, as today. Both are round files (`NNN-` prefix): `fork` copies
them through the forked round, `gc` archives them with the directory. A
mid-round switch's replacement process appends to the same pair.

### 3.2 `relay.ProcSpec` (amends headless spec §3.4)

```
ProcSpec
  Dir        string
  Argv       []string
  Env        []string
  LogPath    string   stderr, appended, created if absent
  StreamPath string   stdout and the exit trailer, appended, created if absent
```

`Runner.ExitCode(ctx, h, path)` keeps its signature; the caller passes the
stream path. The doc comment on `Runner` says "the runner's supervisor left
as the stream's last line".

### 3.3 Supervisor script

```
{ echo 500 >/proc/self/oom_score_adj; } 2>/dev/null || true
"$@" </dev/null
printf '\nrelay-exit:%s\n' "$?"
```

Run with stdout on the stream file and stderr on the log file. The trailer
is written with a leading newline: a builder that dies mid-line leaves a
partial event, and `lastLine` (which strips trailing newlines only) must
still find the trailer on a line of its own. An empty line before it is
what the renderer's pass-through rule turns into nothing (§4.1).

### 3.4 `store.Endpoint.StreamRound`, `StreamOffset`

```
// StreamRound is the round whose builder stream (Store.BuilderStreamPath)
// the daemon is rendering into that round's log, and StreamOffset how
// many bytes of it are rendered. They belong to the round's file, not to
// the process or to Binding.Round: a mid-round switch keeps them,
// clearProcess keeps them, finishRound's Round++ keeps them, and only
// startRound on a later round moves them (round = the new round, offset
// 0). So a round that closed on its marker while the builder was still
// flushing keeps draining until the next round starts. 0 means no stream
// has been started.
StreamRound  int   `json:"stream_round,omitempty"`
StreamOffset int64 `json:"stream_offset,omitempty"`
```

A state file written before these fields reads as round 0: nothing is
drained until the next `startRound`, and a round in flight across the
upgrade is rendered from wherever `.jsonl` starts once that happens --
harmless, and only once.

### 3.5 Print form per kind (amends headless spec §3.5)

| kind | Print (before extra) |
|---|---|
| agy | `-p <prompt> --model M --agent <def> --output-format stream-json --print-timeout <budget>` |
| claude | `-p <prompt> --model M --agent <def> --output-format stream-json --verbose` |
| opencode | `run <prompt> -m P/M --agent <def> --format json` |

`extra_args` from `candidates.json` still append after, so
`--dangerously-skip-permissions` is unaffected.

## 4. Interface definitions and component contracts

### 4.1 `transcript.Renderer` (new package)

Single responsibility: one raw line of a harness's stream in, the human
lines for it out. Knows harness kinds; knows nothing about rounds, files or
bindings. One renderer per pass: it carries nothing between passes, and one
piece of state within one -- claude's tool results carry the span from the
call that asked for them, so the renderer remembers the calls it has seen and
not yet answered. The one real cost of that state: a claude duration whose
call and result fall either side of a restart, a tick, or a tail window is
not shown.

```
func NewRenderer() *Renderer
func (*Renderer) Render(kind string, line []byte) []string
```

Preconditions: `line` is one line without its trailing newline; it may be
empty, may not be JSON, may be JSON of a shape the table does not know.
Postconditions: returns the lines to append, each without a trailing
newline; a multi-line assistant text is one element containing newlines.
Never returns an error and never panics on input.

Rules, in order:

1. An empty line renders as nothing (the blank the trailer's `printf`
   leaves).
2. A line that is not a JSON object renders as itself, verbatim. This is
   how `relay-exit:N` reaches the `.log`, and how a harness that prints a
   plain-text error to stdout is still seen.
3. A JSON object of a kind and event the table knows renders per the
   table.
4. A JSON object the table marks as noise renders as nothing.
5. Any other JSON object renders as `[<type>]` -- the value of `type`
   (claude, opencode) or `event` (agy), or `[?]` when neither is a string.
   A harness upgrade degrades to noise, not silence.

Vocabulary (every kind renders into the same shapes). Amended
2026-09-18 (#180 round 3): a tool call carries a `● ` marker and a result
a `⎿` marker, so a reader that styles the log -- `relay ui`'s terminal
tab -- can tell a call from a line of assistant prose without guessing;
`->` had no such property. Assistant text is still verbatim, so it never
starts with either marker unless the model typed one.

| what | line |
|---|---|
| tool call | `● <name> <main argument>` -- the name as the harness spells it (`Bash`, `view_file`); the argument on one line, truncated to 200 bytes with `...` |
| tool call, no argument | `● <name>` |
| tool result, ok | `  ⎿ ok: <first line of the tool's output>`, or `  ⎿ ok` when there is none (claude: `tool_result` content; agy: `tool_info.output`) |
| tool result, error | `  ⎿ error: <first line of the message>` |
| assistant text | the text, verbatim |
| denied action | `denied: <what the harness names>` |
| final result | the result text, verbatim; preceded by `result: <status>` when the harness says it was not a success |

Amended 2026-09-29: every element a table renders from an event that carries
a time or a duration is prefixed with one plain-text stamp -- never ANSI --
`<clock> <duration> <entry>`, exactly one space between the parts and before
the element's own bytes:

- `12:41:03 +4.2s ● shell go test ./...` -- both
- `12:41:03 ● shell go test ./...` -- clock only
- `+0.9s   ⎿ ok: ok  github.com/… 0.4s` -- duration only (the result's own
  two-space indent is untouched)

`clock` is the event's own time in the rendering machine's local zone,
`15:04:05` (8 cells); `duration` is `+`, the span in seconds to one decimal,
and `s`. One stamp per rendered element; a multi-line element (assistant
prose, a thinking block) carries it on the first physical line only. Neither
part means the entry is byte-for-byte what the table produced before: no
`--:--:--`, no zero, no placeholder. Non-JSON lines, supervisor trailers and
unknown kinds get no stamp; an unknown event of a known kind carries its
event's clock. A duration is omitted when either end is absent, unparseable,
or the end precedes the start; an equal pair is a measurement and reads
`+0.0s`.

| kind | clock from | duration from | line carrying the duration |
|---|---|---|---|
| claude | event `timestamp` (RFC3339Nano string) | the matching `tool_result` event's `timestamp` minus the `tool_use` block's event `timestamp` | the `  ⎿ ok`/`  ⎿ error` line; the `●` call line carries the clock only |
| opencode | event `timestamp` (epoch ms) | `part.state.time.end - start` (ms), same event | the `●` call line; its result line carries the clock only |
| agy | none -- the stream has no wall clock | `step_update.duration_seconds`, same event | the `  ⎿ ok`/`  ⎿ error` line; the `ACTIVE` call line gets nothing |
| codex | none | none | -- |

A claude call is paired to its result by `tool_use_id`, and the pairing lives
in the pass: a pair split across passes loses that one duration and nothing
else, because the renderer's state is earlier-lines-only. `SplitStamp(line)
(stamp, body string)` splits a rendered line for readers that match on the
markers: `IsThinking` tests the body for `∴`, and `colourTranscript` matches
its markers on the body and renders the stamp faint.

The *main argument* of a tool call is the first of these that is a
non-empty string, checked in order: `command`, `file_path`, `path`,
`AbsolutePath`, `pattern`, `description`, `prompt`, `query`, `url`; then the
only parameter if there is exactly one string parameter; then the first
string-valued parameter in sorted key order (JSON objects decode without
order); else none. Table-driven
per kind only where the event shapes differ; the argument pick is shared.
`oneLine` also strips a trailing `\r`: agy's command output is CRLF.

**claude** (`type`):

| event | render |
|---|---|
| `assistant`, content block `tool_use` | tool call: `name`, `input` |
| `assistant`, content block `text` | assistant text |
| `assistant`, content block `thinking` | noise |
| `user`, content block `tool_result` | tool result; error iff `is_error`; the message (error) or output (ok) is `content` when a string, else the first `text` element |
| `result` | `denied: <tool_name>` per `permission_denials` entry; `result: <subtype>` when `is_error`; then `result` text verbatim when non-empty |
| `system` (every subtype), `rate_limit_event` | noise |

One `assistant` event carries one message with a `content` array; every
block is rendered in order. Fixture: `testdata/claude.jsonl` from the
2026-09-16 probe.

**agy** (`event`):

| event | render |
|---|---|
| `step_update`, `step_type: tool`, `state: ACTIVE` | tool call: `tool_name`, `tool_info.parameters` |
| `step_update`, `step_type: tool`, `state: DONE` | `  ⎿ ok: <tool_info.output first line>` |
| `step_update`, `step_type: tool`, `state: ERROR` | `  ⎿ error: <tool_info.error.message>` |
| `step_update`, any other `step_type` | noise |
| `result` | `denied: <action> (<display_name>)` per `denied_actions` entry; `result: <status>` when `status != "SUCCESS"`; then `response` verbatim when non-empty |
| `init` | noise |

agy does not stream assistant text between tool steps; the only text is
`result.response` at the end. Fixture: `testdata/agy.jsonl` from the
2026-09-16 probe, which is the exact failure #168 was filed on (a tool
denied under headless permissions, `response` empty).

**opencode** (`type`):

| event | render |
|---|---|
| `tool_use`, `part.state.status: completed` | tool call: `part.tool`, `part.state.input`; then `  ⎿ ok: <part.state.output first line>` -- two lines from one event |
| `tool_use`, `part.state.status: error` | tool call as above; then `  ⎿ error: <part.state.error first line>` |
| `tool_use`, any other status | rule 5 |
| `text` | `part.text` verbatim |
| `error` | `  ⎿ error: <error.message>` (session-level: `aborted`, `provider.auth`, `provider.no-route` seen) |
| `step_start`, `step_finish` | noise |

opencode emits one `tool_use` per call, after the tool has finished, with
the call and its result in the same event; the table renders both lines
from it. A permission `run` auto-rejects is not a denial list but a
`tool_use` in status `error` with the message `The user declined this tool
call`, so it renders as a tool error, not a `denied:` line. Fixture:
`testdata/opencode.jsonl` from the 2026-09-17 probe against
`opencode/nemotron-3.5-lightning-free` (#173).

### 4.2 `drainStream` (new, `headless.go`)

Single responsibility: bring the `.log` up to date with the `.jsonl` for one
binding, once, without ever failing the tick.

```
func drainStream(rt Runtime, b store.Binding) store.Binding
```

Preconditions: `b.Builder.Headless()`. Nothing else: `StreamRound` may be 0
(no round ever started), the stream file may not exist yet (Start has not
run), the offset may be past the file's size (a rewritten state file), the
process may be alive or gone, `Binding.Round` may already be past
`StreamRound`.
Postconditions: every complete line of
`BuilderStreamPath(b.Name, b.Builder.StreamRound)` at or after
`b.Builder.StreamOffset` has been rendered with
`transcript.Render(b.Builder.Kind, line)` and the result appended to
`BuilderLogPath(b.Name, b.Builder.StreamRound)` in one write, and
`StreamOffset` on the returned binding is the offset just past the last
newline consumed. A trailing partial line is left for the next tick. The
returned binding is otherwise `b`.

- `StreamRound == 0`, stream missing, or size equals offset: return `b`
  unchanged, no log write, no warning (the steady state between rounds).
- Offset greater than size: treat as 0 (render from the start), warn
  with the binding and both numbers.
- Read or write error: `slog.Warn("builder transcript", "binding", ...,
  "err", err)`, return `b` unchanged; the next tick retries from the same
  offset. The cursor advances only after the append succeeded, so a failed
  append renders the same lines again next tick rather than dropping them.
- Nothing rendered (all noise): the offset still advances; no write.

The caller saves the binding as it saves any other endpoint change.

### 4.3 `reconcileHeadless` (existing; one call added)

Directly after the planner refresh and the round-cap check, before
`tx.ReadLog`:

```
b = drainStream(rt, b)
```

Every tick, round open or not, process or not. The order is what makes the
rest of the function honest: the exit entry's `logTail` (spec §3.8),
`gateOnLimit`'s `limitText` and the `unmarked`/`noreport` decisions all read
a `.log` that already holds everything the builder printed before it
exited. A marker close (`closeOnMarker`, then `finishRound`'s `Round++`)
clears the process while the builder may still be flushing its result;
the ticks after it keep draining round `StreamRound`'s file, because
nothing on that path touches the cursor.

### 4.4 `startRound` (existing; cursor and stream path)

Before `Runner.Start`: if `b.Builder.StreamRound != b.Round`, set
`StreamRound = b.Round` and `StreamOffset = 0`. A mid-round switch calls
`startRound` on the same round and so keeps the cursor: both processes of
a round append to the same `.jsonl`. Passes
`StreamPath: rt.Store.BuilderStreamPath(b.Name, b.Round)` beside `LogPath`.
`Builder.LogPath` on the endpoint is still the `.log` -- it is what
`status`, the exit entry and `answer`'s hint point a human at. The exit
path's `ExitCode` call passes `BuilderStreamPath(b.Name, b.Round)`.

Known limit: a process that keeps writing to round N's stream after round
N+1 has started leaves those lines unrendered (the `.jsonl` has them).
Reaching it needs a process that outlives its own round close by a whole
delivery cycle plus the planner's next send; `Send` refuses while a
process is alive, and a stray with no open round is killed on the next
tick (headless spec §5.1).

### 4.5 `proc.Runner.Start` (existing; one file added)

Opens `spec.StreamPath` and `spec.LogPath` both `O_APPEND|O_CREATE|O_WRONLY`
`0o644`, before starting anything, after the checks that can refuse; a
refused Start still leaves nothing behind. `cmd.Stdout` is the stream,
`cmd.Stderr` the log. `StreamPath == ""` is refused (`proc: empty stream
path`) -- the trailer has nowhere to go.

### 4.6 `harness.Launch` (existing; table change)

§3.5. `PrintArgs` is unchanged; the placeholders sit where they did.

### 4.7 `ui.fetchTerminal` (existing; one fallback)

`clearProcess` blanks `Builder.LogPath` at round close, so the terminal
tab of a headless binding went blank the moment a round closed. Between
rounds the tab shows the log of `Builder.StreamRound` -- the last round
that had a process (§3.4) -- and says "no round has run yet" only when
that is 0. The tab reads; it never renders or writes.

## 5. High-level pseudocode

```
drainStream(rt, b):
    if b.Builder.StreamRound == 0: return b
    stream := rt.Store.BuilderStreamPath(b.Name, b.Builder.StreamRound)
    info := stat(stream); if missing: return b
    off := b.Builder.StreamOffset
    if off > info.Size: warn; off = 0
    if off == info.Size: return b
    data := read(stream from off to end); on error: warn, return b
    end := lastIndexByte(data, '\n'); if end < 0: return b       // partial line only
    data = data[:end+1]
    var out []string
    for each line in split(data, '\n') (dropping the final empty piece):
        out = append(out, transcript.Render(b.Builder.Kind, line)...)
    if len(out) > 0:
        appendLines(BuilderLogPath(b.Name, b.Builder.StreamRound), out); on error: warn, return b
    b.Builder.StreamOffset = off + end + 1
    return b

reconcileHeadless:
    ...planner refresh, round cap...
    b = drainStream(rt, b)
    ...as today; ExitCode(ctx, handle, BuilderStreamPath(name, round))...

startRound:
    if b.Builder.StreamRound != b.Round:
        b.Builder.StreamRound, b.Builder.StreamOffset = b.Round, 0
    ...Runner.Start(ProcSpec{..., LogPath, StreamPath: BuilderStreamPath(name, round)})...

transcript.Render(kind, line):
    if len(trim(line)) == 0: return nil
    obj, ok := parse JSON object; if !ok: return [string(line)]
    switch kind:
        "claude":   return renderClaude(obj)
        "agy":      return renderAgy(obj)
        "opencode": return renderOpencode(obj)
    return [unknownLine(obj)]
```

`appendLines` opens `O_APPEND|O_CREATE|O_WRONLY` `0o644` and writes the
joined lines plus a trailing newline in one `Write`, the same discipline as
`appendLogMarker`.

## 6. Error handling strategy

| condition | handling |
|---|---|
| claude launched without `--verbose` | cannot happen: `Launch` renders it; the harness test pins the argv |
| stream file unreadable | warn, cursor unchanged, retry next tick |
| log unwritable | warn, cursor unchanged, retry next tick (same lines rendered again -- no loss, no duplication, because the cursor moved only on success) |
| cursor past EOF (state rewritten, file truncated by hand) | warn once, render from 0 |
| partial last line | wait for the newline; the trailer's leading newline guarantees the trailer itself is never partial |
| event of unknown shape | `[type]`, never dropped silently, never an error |
| non-JSON on stdout | verbatim, so a harness that prints a plain error is still seen |
| stderr | untouched: the supervisor writes it straight to the `.log` as today; the "jetski: no output produced" line of #168 stays where it was |
| `ExitCode` when the stream has no trailer (killed, still running) | `ok == false`, as today for the log |

Nothing here can fail a tick, close a round, or change a binding's state.
The transcript is a courtesy to the reader.

## 7. Ordered implementation steps

1. **`internal/transcript`**: `Render` with the shared argument pick, the
   claude and agy tables, and the opencode table; fixtures
   from the 2026-09-16 probes as `testdata/{claude,agy}.jsonl` with the
   expected `.log` beside each; unit tests for rules 1, 2 and 5 and for
   each table row. Mutation: drop the `is_error` branch, the claude
   fixture test fails on the `-> error:` line.
2. **`Store.BuilderStreamPath`**, **`ProcSpec.StreamPath`**,
   **`Endpoint.StreamRound`/`StreamOffset`**; `proc.Start` opens both files, the
   supervisor's trailer goes to stdout with the leading newline;
   `proc_test`: a script that writes to both streams leaves stdout and the
   trailer in the stream file, stderr in the log, `ExitCode(streamPath)`
   reads the code, and the trailer is found after a stdout write with no
   final newline.
3. **`harness.Launch`** print table; existing harness tests updated to the
   new argv; a test that claude's print form contains `--verbose`.
4. **`drainStream`** and the call in `reconcileHeadless`; `startRound`
   moves the cursor and passes the stream path; the exit path's `ExitCode`
   reads it. `fakeRunner` tests: lines appended to the stream between
   ticks appear in the log in order; a partial line waits and is rendered
   whole on the next tick; the cursor survives a reload of the binding
   from the store; the exit tick's entry payload contains the final result
   text; a marker close followed by more stream lines still renders them
   on the next tick; the next round's `startRound` moves the cursor; a
   mid-round switch does not. Mutation: move the drain below `closeOnMarker`, the
   marker-close test fails.
5. **Spec amendments and README**: strike the scope boundary line in the
   headless spec, update §3.3/§3.4/§3.5 by reference to this document,
   README "Headless builders" names both files.
6. **opencode capture**: done 2026-09-17 (#173); fixture and table pinned.
