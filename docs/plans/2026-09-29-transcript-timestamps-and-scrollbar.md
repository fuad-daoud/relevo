# Plan: transcript timestamps and a round-pane scrollbar

Seed: *"we need timestamps for the transcripts so we know how much each took, and a scroll bar so we know where we are"*. Decisions 1–4 are kept as written; nothing in them is impossible. One decision is pinned below where the seed left a choice (which line carries a duration), and one seed claim about the archived path is corrected, not bent.

## Seed-vs-code notes (leads corrected; no halt)

1. **`show.go:510` is not the archived transcript path.** It is `showDB`, the fallback for a binding with database rows but neither a live directory nor a sealed record (`show.go:437-439`, "adopted by name only after the fact"). A sealed record's `--transcript` goes through `showSections` → `RoundTranscript` over the **sealed round files** (`show.go:329-341`, `readBytes` = `ArchivedFile`, `show.go:428-430`), i.e. it re-renders the sealed stream at read time. And no production code writes round transcript rows any more — mirroring round files into transcript rows was stopped in #423. `streamTranscriptRecords` survives to re-derive rows for mirror dedupe (`ingest/dedupe.go:346-380`) and for legacy rows already in the database.
2. **`Render` has more call sites than the seed lists.** Non-test: `relevo/transcript.go:45` (live and `show`), `relevo/headless.go:443` (legacy `NNN-builder.log` drain), `ingest/transcript.go:31` (database rows); through `RoundTranscript`, `serve/roundfiles.go:70` (served round file) inherits the same bytes. Tests: `availability/limit_test.go:262,301`, `relevo/show_test.go:725`, `ui/hist_test.go:270`, plus `internal/transcript` itself.
3. **claude.** Real streams carry `"timestamp":"2026-09-26T00:43:03.719Z"` (RFC3339 with millis) on assistant, user and system events; `usage.StreamSteps`'s claude branch already reads it. The transcript fixture `testdata/claude.jsonl` has none, so it pins the *missing-time* rule, not the clock.
4. **codex** `exec --json` carries no time field at all (fixture and `codexSteps` agree), so codex renders exactly as today.
5. **`IsThinking` has two scan callers**, not one: `availability/limit.go:72` and `relevo/denial.go:23`.
6. **Budget.** `internal/ui/round_pane.go` is 538 lines (limit 600); the scrollbar goes in a new file. `internal/relevo/transcript.go`, `headless.go` and `internal/ui/round_pane.go` are in `scripts/check-comments.allow`; the new files are not, so their comments must not cite `§` or `#NNN`.

## Behaviour

### A. The stamp — one rendering, both surfaces

**Spelling.** Every element a table renders from an event with a time or duration is prefixed with one plain-text stamp (never ANSI):
`<clock> <duration> <entry>`, exactly one space between parts and before the entry's own bytes:

- `12:41:03 +4.2s ● shell go test ./...` — both
- `12:41:03 ● shell go test ./...` — clock only
- `+0.9s   ⎿ ok: ok  github.com/… 0.4s` — duration only (the result's own two-space indent is untouched)

`clock` is the event's own time in the **rendering machine's** local zone, `15:04:05` (8 cells). `duration` is `+` + the span in seconds to one decimal + `s` (`+0.0s`, `+0.9s`, `+4.2s`, `+75.3s`). One stamp per rendered element; a multi-line element (assistant prose) carries it on the first physical line only. Neither part ⇒ the entry is byte-for-byte what today's renderer produces: no `--:--:--`, no zero, no placeholder. Non-JSON lines, trailers and unknown kinds get no stamp.

**Per harness.**

| kind | clock from | duration from | which line carries the duration |
|---|---|---|---|
| claude | event `timestamp` (RFC3339Nano string) | matching `tool_result` event's `timestamp` − the `tool_use` block's event `timestamp` | the `  ⎿ ok`/`  ⎿ error` line; the `●` call line carries the clock only |
| opencode | event `timestamp` (number, epoch ms) | `part.state.time.end − part.state.time.start` (ms), same event | the `●` call line; its result line carries the clock only |
| agy | none — the stream has no wall clock | `step_update.duration_seconds`, same event | the `  ⎿ ok`/`  ⎿ error` line (the only line the event renders); the `ACTIVE` call line gets nothing |
| codex | none | none | — |

- claude's pairing key is `tool_use_id` → the `tool_use` block's `id`; unknown/cross-pass ids behave as absent.
- A duration is omitted when either end is absent, unparseable, or the end precedes the start. A genuine equal pair reads `+0.0s`: it is a measurement, not a stand-in for missing data.
- Nothing that renders as nothing gains a line: opencode `step_start`/`step_finish` and agy `agent_response` steps stay noise, and `Render`'s `[type]` fallback now carries its event's clock when there is one.
- A missing time or duration is never supplied from another event.

**Mechanism.** `transcript.Render` (stateless) becomes `transcript.NewRenderer()` + `(*Renderer).Render(kind, line)`; the free `Render` is deleted so there is exactly one semantics. The renderer carries per-kind state: claude's pending `tool_use` id → the event time that emitted it (map bounded by unanswered calls, cleared on use). A **pass** owns one renderer: one `renderStreamFrom` call (whole file for `RoundTranscript`/`show`, or a `streamTail` window), one `drainStream` pass, one `streamTranscriptRecords` batch (dedupe re-derives a whole sealed round in one call). A claude pair split across passes loses that one duration (absent); clocks never depend on another line, so a rendered prefix never changes as the stream grows — `serve/roundfiles.go`'s `from=` offsets and the client mirror depend on that.

**Prefix hazards.** New `transcript.SplitStamp(line) (stamp, body string)`. `IsThinking` (`thinking.go:13`) checks `body` for `∴`, so a stamped musing is still skipped by both scans; `colourTranscript` (`ui/pane.go:70`) matches its `● `/`  ⎿ `/thinking cases on `body` and renders the stamp in `faintStyle`. The scans keep their meaning; `capLine` of a stamped line carrying the clock is harmless.

**Archived.** Sealed record: re-rendered from the sealed stream at read time, so old and new rounds show the stamp; `show.go` unchanged. Database rows (`showDB`, legacy/mirror only): the stamp lives inside `Rendered` at write time because ingest calls the same renderer; rows written before this change keep today's bytes. No read-time composition from `TS`; `tsFromRecord` stays string-only (its only readers are the column and mirror identity, and widening it would perturb dedupe identity for no visible gain). `logOnlyTranscriptRecords` (a legacy log) stays verbatim.

**Cost, stated.** The renderer is no longer stateless across passes; the spec sentence "Stateless: a daemon restart loses nothing" (`docs/specs/2026-09-17-headless-transcript-design.md` §4.1) must be amended to the one real cost: a claude duration whose two events straddle a restart/tick boundary.

**Timezone rule.** Rendering happens where it happens (ingest may run on the serve host), so the clock is that machine's local time. No test asserts a clock without controlling the zone: clock cases set `time.Local = time.UTC` (defer-restored).

### B. The scrollbar — round pane viewport only

- One cell, the pane's last (`width−1`). `contentWidth()` already leaves it free (5-cell indent, `width−6`), so **no content column is eaten** and `vp.Width` is unchanged.
- Track `│` in `borderStyle`, thumb `█` in `mutedStyle` — existing palette only (`styles.go` names every colour).
- Drawn on every viewport row, including the blank rows the viewport pads to its height; never on the head rows, the source line, or a blocked builder's hint row.
- Pure geometry, from the viewport's own numbers after `view()` sets width and height: `total = vp.TotalLineCount()`, `h = vp.Height`, `y = vp.YOffset`. Hidden when `h <= 0` or `total <= h` (fits, empty, height 0). Otherwise `size = max(1, h*h/total)` and `top = y*(h−size)/(total−h)` (integer; `y` is already clamped to `[0, total−h]`); row *i* is `█` when `top ≤ i < top+size`, `│` otherwise.
- Every round-pane tab gets it (prompt, report, transcript, diff, log, artifacts). No other view, no new key.

## Seams

- `internal/transcript/renderer.go` (new): `Renderer`, `NewRenderer`, `(*Renderer).Render(kind, line)`, the stamp type and its formatting, `SplitStamp`.
- `internal/transcript/transcript.go:23`: `Render` goes; the dispatch moves into the renderer.
- `internal/transcript/claude.go:5-55`: stamp per event; record `tool_use` ids on assistant events; consume them on the `user`/`tool_result` path.
- `internal/transcript/opencode.go:5-33`: clock from `timestamp`; duration from `state.time` on the call line; `step_start`/`step_finish` stay `nil`.
- `internal/transcript/agy.go:5-55`: duration on the DONE/ERROR lines.
- `internal/transcript/codex.go`: no change (no time in the stream); only the plumbed signature.
- `internal/transcript/thinking.go:13-15`: split the stamp first.
- `internal/relevo/transcript.go:37-52`: one `transcript.NewRenderer()` per pass; comment at 26-29 reworded (prefix stability now rests on state being earlier-lines-only).
- `internal/relevo/headless.go:436-444`: renderer created before `drainFile`, one per pass; comment at 405-410 notes it.
- `internal/ingest/transcript.go:20-45`: renderer per batch; comment at 16-19 updated.
- `internal/ingest/dedupe.go:346-380`: untouched; a pre-change row no longer re-derives equal and `streamLinesCover` (`:241-278`) already covers that case.
- `internal/availability/limit.go:72`, `internal/relevo/denial.go:23`: no change; they inherit `IsThinking`.
- `internal/ui/pane.go:70-91`: split the stamp, style it `faintStyle`, style the body as today.
- `internal/ui/scrollbar.go` (new): `barCells(total, height, offset int) []string` — one cell per viewport row, nil when hidden.
- `internal/ui/round_pane.go:518-526`: append the cells after `"     " + l`.
- `internal/relevo/show.go`, `internal/serve/roundfiles.go:52-75`, `internal/ui/fetch.go:293-312, 334-493`: no change; they carry the same bytes.
- `README.md:596-600`: one sentence and one example for the stamp.
- `docs/specs/2026-09-17-headless-transcript-design.md` §4.1: the vocabulary amendment and the stateless sentence.

## Steps (in order, test first)

1. **transcript tests** — new `internal/transcript/stamp_test.go`: a per-harness stamp table (the four rows above, both-part/clock-only/duration-only/absent), `IsThinking` on a stamped thinking line, `SplitStamp`, and a sequence case for claude's call→result duration. Update `TestOpencodeTable`'s one timestamped case; port every `transcript.Render(...)` call in that package to a renderer. Force `time.Local = time.UTC` in the clock cases. *Done when* the new tests fail against the current code: `go test ./internal/transcript/ -run 'Stamp|Claude|Opencode|Agy|Codex|Fixtures' -count=1`.
2. **renderer implementation** (seams above, files kept under 600 lines and functions under 70). *Done when* `go test ./internal/transcript/ -count=1` passes.
3. **fixtures** — `go test ./internal/transcript/ -run TestFixtures -update`, then `git diff --stat internal/transcript/testdata`: `opencode.log` gains the clock on every rendered line, `agy.log` gains `+…s` on the DONE/ERROR lines, `claude.log` and `codex.log` are byte-identical.
4. **scans** — `internal/availability/limit_test.go` (add a stamped thinking line that must be skipped; update the pinned opencode-errors render at `:303`), `internal/relevo/denial_test.go` likewise. *Done when* `go test ./internal/availability/ ./internal/relevo/ -run 'Limit|Denial' -count=1` passes.
5. **live and legacy render sites** — `internal/relevo/transcript.go`, `internal/relevo/headless.go`, plus `show_test.go:725`. *Done when* `go test ./internal/relevo/ -run 'Transcript|RenderStream|Drain' -count=1` passes, including the drain-equals-`renderStream` and prefix-stability assertions at `transcript_test.go:56-103`, extended with a claude call/result pair proving the duration survives one pass and the earlier prefix does not move.
6. **ingest** — `internal/ingest/transcript.go`; add a whole-round-one-batch claude case to `transcript_test.go` and leave the dedupe tests as they are. *Done when* `go test ./internal/ingest/ -count=1` passes.
7. **UI stamp styling** — `internal/ui/pane.go` + `pane_test.go`: a stamped thinking/call/result line keeps today's styling with a faint stamp. *Done when* `go test ./internal/ui/ -run 'ColourTranscript|BodyOf' -count=1` passes.
8. **scrollbar** — `internal/ui/scrollbar.go`, `round_pane.go:518-526`, and geometry tests in `pane_test.go`: hidden when the body fits, thumb at the top/middle/bottom as `YOffset` moves, thumb at least one cell, pane width and `vp.Width` unchanged. *Done when* `go test ./internal/ui/ -run 'Scrollbar|PaneView' -count=1` passes.
9. **goldens** — `go test ./internal/ui/ -run TestGolden -update`; `git diff` must show only `round-reader-artifacts-100.golden` and `round-reader-artifacts-132.golden` gaining the bar column (the golden harness strips ANSI, so the characters are what is pinned); every other golden byte-identical. *Done when* `go test ./internal/ui/ -count=1` passes.
10. **docs** — README sentence; spec §4.1 amendment. *Done when* both match the Behaviour section.
11. **gate** — run `make check` once; coverage baseline, `.golangci.yml` and every allow-list untouched; `git diff --stat` matches the declared list. *Done when* it is green.
12. **plan copy** — copy this plan to `docs/plans/2026-09-29-transcript-timestamps-and-scrollbar.md`; one commit, no push.

Commands: focused `go test ./internal/transcript/ ./internal/availability/ ./internal/ingest/ ./internal/relevo/ ./internal/ui/ -count=1`; regeneration `go test ./internal/transcript/ -run TestFixtures -update` and `go test ./internal/ui/ -run TestGolden -update`; full `make check`.

## Deleted behaviour (closed list)

1. `transcript.Render(kind, line)`, the stateless free function; `Renderer.Render` replaces it.
2. The renderer's statelessness across passes — a claude duration whose call and result fall in different passes is no longer shown (absent, per the missing-duration rule). No clock and no line is lost.
3. `IsThinking`'s raw first-character test on the whole line; it now tests the body after the stamp.
4. `colourTranscript`'s direct prefix match on the raw line; same, after the stamp.

## Out of scope

- A scrollbar anywhere but the round pane's viewport (fleet, stats, lists, dialogs).
- `relevo status`'s log rows and `relevo log --follow` — they inherit the stamp, no change.
- Codex timestamps, agy step lines, opencode `step_start`/`step_finish` lines.
- Mastermind session transcripts (`RenderRecord`, `show --owner`).
- `db.TranscriptRecord.TS`, `tsFromRecord`, read-time composition, rewriting rows already stored.
- `headlessLogLines`, `wrapBody`, the pane indent, new keys, new colours.
- The fleet table, `:rounds`, the log and diff tabs, every other view.

## The report must include

- The plan path, and `git diff --stat`, which must hold only: `internal/transcript/{renderer.go,stamp_test.go,transcript.go,claude.go,opencode.go,agy.go,thinking.go,transcript_test.go,thinking_test.go}`, `internal/transcript/testdata/{opencode,agy}.log`, `internal/relevo/{transcript.go,headless.go,transcript_test.go,show_test.go,denial_test.go}`, `internal/ingest/{transcript.go,transcript_test.go}`, `internal/availability/limit_test.go`, `internal/ui/{pane.go,scrollbar.go,round_pane.go,pane_test.go,golden_test.go,testdata/round-reader-artifacts-{100,132}.golden}`, `README.md`, the spec, and the plan copy. Anything else is a departure to report, not to hide.
- The seed-vs-code notes above, one line each, especially the `show.go:510`/mirror-rows correction and that claude/codex fixtures have no times to show.
- The regenerated `opencode.log` and `agy.log` diffs pasted; a statement that `claude.log`, `codex.log` and every golden but the two reader-artifacts ones are byte-identical.
- The `make check` result.
- Two mutation tests, each with the test that fails: (a) make claude's result line skip the pending lookup; (b) make `barCells` ignore `YOffset`.
- Confirmation that `testdata/coverage-baseline.txt`, `.golangci.yml`, `scripts/check-comments.allow` and `scripts/check-filesize.allow` were not touched.
- Any decision that could not be kept, as a halt rather than an improvisation.

## MasterMind amendments (bind these too; the rest of the plan stands)

A1. **Timezone determinism in the fixtures.** `TestFixtures` renders the
opencode fixture, so it asserts clocks. It must set `time.Local = time.UTC`
(defer-restore) exactly as the stamp unit cases do, and step 3 regenerates
`testdata/opencode.log` under that zone. Without this the checked-in fixture
passes only in one machine timezone. Any other test that asserts stamped bytes
pins the zone the same way, and the report says which.

A2. **Session records stay unstamped.** `internal/transcript/record.go`
(`RenderRecord`, the mastermind session-record path behind `show --owner`) is out
of scope: it must produce today's bytes byte-for-byte. Add the seam (how a
record render stays stamp-free) and a test that pins an unstamped record
rendering; name it in the report.

A3. **Every deleted-function call site.** Deleting `transcript.Render` breaks
`internal/ui/hist_test.go` (~line 270), which the step and diff lists do not
name. Port every reference the compiler finds, and the report's
`git diff --stat` list includes each such file.

A4. **The split-pair cost is stated, not hidden.** Step 5 must also pin a
`streamTail`/`files/log?from=` window that starts between a claude call and its
result: that window's result line renders without the duration. The report
states this as the accepted cost of Deleted behaviour 2, and confirms a
rendered prefix from byte 0 never moves as the stream grows (the opencode and
claude cases).
