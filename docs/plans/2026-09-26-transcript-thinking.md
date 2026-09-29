# Transcripts show builders' thinking, and the limit and denial scans ignore it (#502) (2026-09-26)

## 1. Overview

A round's transcript today shows the builder's text and tool calls, but none of its thinking. This round renders
thinking wherever relevo renders a transcript. Those places are `relevo show --transcript`, the cockpit's round
detail and the OpenCode plugin's transcript tab. All three go through `transcript.Render`, so the renderers carry
the change and the two viewers only style it.

Facts checked by the planner on 2026-09-26:

- **OpenCode (2.0.14 locally, 2.0.8 on contabo).**
  - Both versions support `opencode run --thinking` ("Show thinking blocks").
  - With it, the `--format json` stream carries events like
    `{"type":"reasoning","timestamp":…,"part":{"type":"reasoning","text":"Just answer.",…}}`, one per reasoning part.
    The text can span several lines.
  - relevo does not pass the flag today, so nothing reaches the stream. `transcript.FirstOutput` already counts
    `reasoning` events.
- **Codex.**
  - `codex exec --json` emits `{"type":"item.completed","item":{"type":"reasoning","text":"…"}}`.
  - `internal/transcript/codex.go` `renderCodexItem` drops it on purpose (`case "reasoning": return nil`).
- **Claude.**
  - `claude -p --output-format stream-json` assistant messages carry `{"type":"thinking","thinking":"…","signature":…}`
    blocks.
  - In every real headless stream the planner found, including claude-sonnet-5 builders on contabo, `thinking` is
    `""`, redacted to a signature.
  - relevo renders a non-empty one and stays silent on an empty one. Today that means nothing new shows for Claude.
    This is expected, not a bug in this round.
- **agy.** Its stream has no thinking text, only `thinking_tokens`. It is out of scope.

**The hazard this round must close.** The limit scan (`matchLimit`, `internal/relevo/limit.go:74`) and the denial
scan (`matchDenial`, `internal/relevo/denial.go:14`) run over the rendered tail of the stream (`builderTail`). A model
thinking "maybe this is a 429 / quota problem" would put a line in that tail that matches a limit pattern, and relevo
would gate a healthy provider. So:

- Every rendered thinking line carries a marker.
- Both matchers skip marked lines.

The change goes in the two matchers, not at their call sites: another round (`scan-540`) is editing those call sites
right now.

## 2. Files

- `internal/transcript/thinking.go` (new): the marker, `thinkingLines` and `IsThinking`.
- `internal/transcript/claude.go`: `renderClaude` (lines 5-52), a `thinking` block case.
- `internal/transcript/codex.go`: `renderCodexItem` (lines 24-43), `reasoning` renders.
- `internal/transcript/opencode.go`: `renderOpencode` (lines 5-32), a `reasoning` case.
- `internal/transcript/transcript_test.go`: port the codex case at ~line 330, and add cases.
- `internal/transcript/thinking_test.go` (new).
- `internal/harness/harness.go:329` and `internal/harness/resume.go:55`: add `--thinking` to opencode's argv.
- The argv assertions to update:
  - `internal/harness/harness_test.go:374,379`;
  - `internal/harness/resume_test.go:43,163`;
  - `internal/harness/tier_test.go:225`;
  - `internal/relevo/headless_test.go:109`.
- `internal/relevo/limit.go`: `matchLimit` (lines 74-110) skips thinking lines.
- `internal/relevo/denial.go`: `matchDenial` (lines 14-34) skips thinking lines.
- `internal/relevo/thinking_scan_test.go` (new). It is a separate file so it cannot conflict with `scan-540`'s edits
  to `limit_test.go`.
- `internal/ui/pane.go`: `colourTranscript` (~lines 62-90) styles thinking lines. Its test goes in
  `internal/ui/pane_test.go`, next to the existing `colourTranscript` test at ~line 433.
- `internal/harness/opencodeplugin/tui.tsx`: `transcriptSpecial` (~lines 1357-1395), a thinking branch.
- `README.md:639`: the opencode argv note gains `--thinking`.
- `docs/plans/2026-09-26-transcript-thinking.md`: this plan, copied as the last step.

## 3. Contracts

### `internal/transcript/thinking.go` (new)

- **`const ThinkingMarker = "∴"`.**
  - This is the first character of every rendered thinking line. A line with text is `"∴ " + text`; a blank line
    inside the thinking is exactly `"∴"`, so no rendered line has trailing whitespace.
  - The doc comment says why the marker exists. The transcript is shown to people, but the limit and denial scans
    also match on it, and they must be able to skip the model's own musings.
- **`func IsThinking(line string) bool`:** `strings.HasPrefix(line, ThinkingMarker)`.
- **`func thinkingLines(text string) []string`** (unexported):
  - Split `text` on `"\n"` and trim a trailing `"\r"` from each line.
  - Drop leading and trailing blank lines (a blank line is empty after `strings.TrimSpace`).
  - Map each remaining line: a blank line becomes `ThinkingMarker`; any other line becomes
    `ThinkingMarker + " " + line`.
  - Return nil when nothing is left. Lines are not capped: thinking is prose and is shown in full, like assistant
    text.

### Renderers

- **`renderClaude`, the `"assistant"` case:** add `case "thinking":` inside the block switch, which appends
  `thinkingLines(str(blk["thinking"]))...`. Block order is preserved. An empty `thinking` adds nothing.
- **`renderCodexItem`:** `case "reasoning": return thinkingLines(str(item["text"]))`.
- **`renderOpencode`:** `case "reasoning": return thinkingLines(str(asMap(obj["part"])["text"]))`, placed before
  `default`.
- `Render`'s contract ("never errors, never panics") still holds.

### Harness argv

- `internal/harness/harness.go:329` and `internal/harness/resume.go:55`: insert `"--thinking"` directly before
  `"--standalone"`.
- In the `harness.go` comment block above line 329, add one line saying why: without the flag, opencode leaves
  reasoning out of the json stream.
- Every test argv listed in §2 changes the same way. Do it with the scripted edit in step 1.

### Matchers (`internal/relevo`)

- **`matchLimit`:** inside the backwards loop, `continue` on `transcript.IsThinking(line)` before trying the patterns.
  Extend the doc comment by one clause: thinking lines are skipped, because a model reasoning about a limit is not
  hitting one.
- **`matchDenial`:** the same skip and the same clause.
- Nothing else in either function changes, and no call site changes.

### Viewers

- **`colourTranscript` (`internal/ui/pane.go`):**
  - Add a first case: `transcript.IsThinking(l)` → `dimStyle.Italic(true).Render(l)`.
  - Add the `internal/transcript` import. `internal/transcript` imports nothing from `internal/ui`, so there is no
    cycle; if the compiler reports one, halt.
  - Extend the doc comment's list with "a thinking line is dim italic".
- **`transcriptSpecial` (`tui.tsx`):**
  - As the first branch, a line matching `/^∴/` returns `<box flexDirection="row"><text fg={mutedColor}>{line}</text></box>`.
    This follows the shape of the existing muted `⎿` branch.
  - Update the comment above it from "four styled branches" to "five".
  - No other plugin change.

### README

- At `README.md:639`, the sentence about `--standalone` gains a short clause that `--thinking` is also passed, so
  reasoning reaches the transcript. Keep the paragraph's style.

## 4. Steps

### 0. Working efficiently

**How to work:**
- In one batch, read:
  - `internal/transcript/{transcript,claude,codex,opencode}.go` (whole);
  - `internal/transcript/transcript_test.go:1-60,300-360`;
  - `internal/relevo/limit.go:70-112`;
  - `internal/relevo/denial.go` (whole);
  - `internal/relevo/denial_test.go:1-40`;
  - `internal/ui/pane.go:55-95`;
  - `internal/ui/pane_test.go:425-460`;
  - `internal/harness/opencodeplugin/tui.tsx:1350-1400`;
  - `internal/harness/harness.go:315-332`;
  - `README.md:630-650`.
- Make each file's change in one edit.

**Commands:**
- First run `mkdir -p $HOME/.cache/go-tmp`, then `export TMPDIR=$HOME/.cache/go-tmp GOTMPDIR=$HOME/.cache/go-tmp`.
- Focused: `go test ./internal/transcript/ ./internal/harness/ -count=1`, then
  `go test ./internal/relevo/ -run 'Limit|Denial|Thinking|Headless' -count=1`, then
  `go test ./internal/ui/ -run 'Transcript|Colour|Pane' -count=1`.
- Final:
  - `go test ./internal/transcript/ ./internal/harness/... ./internal/relevo/ ./internal/ui/... -count=1 -race`
  - `go vet ./...`
  - `test -z "$(gofmt -l $(git ls-files '*.go'))"`
  - `make lint`
  - `sh scripts/check-comments.sh`
  - `sh scripts/check-filesize.sh`
  - `sh scripts/check-coverage.sh`
- If `git ls-files` fails in this worktree, run `gofmt -l` on the touched files by hand, and say so.
- Skip `make check`; the planner runs it. **No test in `cmd/relevo`**, and no test that runs a real harness binary.
- New tests call `t.Parallel()` wherever the neighbouring tests in that file do (a concurrent round is adding it for CI speed).
- Comments say *why*, with no issue numbers, `§` or plan references. Test names say what they pin. Do not add
  entries to any `scripts/*.allow` file or to `.golangci.yml`. `internal/transcript` is already under the full lint.

### 1. The argv, scripted

Run this once from the worktree root:

```
sed -i 's/"json", "--standalone"/"json", "--thinking", "--standalone"/' \
  internal/harness/harness.go internal/harness/resume.go \
  internal/harness/harness_test.go internal/harness/resume_test.go \
  internal/harness/tier_test.go internal/relevo/headless_test.go
```

Then `grep -rn -- '"--standalone"' internal/`. Every hit must be preceded by `"--thinking",`. Add the one-line
comment in `harness.go` by hand. Run `go test ./internal/harness/ -count=1`. Any other failure there is a halt:
report it.

### 2. `thinking.go` and the three renderers (§3)

### 3. The two matchers (§3)

### 4. The two viewers and the README (§3)

### 5. Tests

**`internal/transcript/thinking_test.go`:**
- **a. `TestThinkingLinesMarksEveryLine`:**
  - The input `"\n  \nfirst\n\nsecond\r\n\n"` gives `["∴ first", "∴", "∴ second"]`.
  - `""` and `"  \n "` give nil.
- **b. `TestIsThinking`:** true for `"∴ x"` and `"∴"`; false for `"x ∴"`, `"● bash ls"` and `""`.

**`internal/transcript/transcript_test.go`.** Add these to the per-harness tables in the style already there.

- **c.** OpenCode `{"type":"reasoning","part":{"type":"reasoning","text":"plan\nthen act"}}` gives
  `["∴ plan", "∴ then act"]`.
- **d.** OpenCode reasoning with `"text":""` gives nil.
- **e.** Claude: an assistant message whose content is `[thinking "weigh it", text "done"]` gives
  `["∴ weigh it", "done"]`, in that order.
- **f.** Claude: a thinking block with `"thinking":""` and a signature gives nil. The existing `testdata/claude.jsonl`
  fixture already covers this through any whole-fixture test; keep it passing unchanged.
- **g.** Codex: port the existing case `"item.completed reasoning is noise"`. Rename it to
  `"item.completed reasoning renders as thinking"`; its want changes from `nil` to `[]string{"∴ thinking"}`. This is a
  port, not a deletion (§5 item 1).
- **h.** Codex reasoning with empty text gives nil.

**`internal/relevo/thinking_scan_test.go`:**
- **i. `TestMatchLimitSkipsThinkingLines`:**
  - The pattern is `regexp.MustCompile("(?i)quota")`.
  - The text is `"working\n∴ maybe the quota is exhausted\n● bash ls\n  ⎿ ok: a"`. It gives `ok == false`.
  - The same text with a last line `"error: quota exceeded, resets in 2h"` gives `ok == true` and `Line` is that line.
- **j. `TestMatchDenialSkipsThinkingLines`:** the same shape with a denial-style pattern and `matchDenial`.

**`internal/ui/pane_test.go`:**
- **k. `TestColourTranscriptDimsThinking`:**
  - The body is `"∴ considering\n● Bash ls"`.
  - Line 0, stripped of ANSI, equals `"∴ considering"`, and the raw line differs from the input line (it is styled).
  - Line 1 is still the styled tool line. Follow what the existing test at ~line 433 asserts.
  - If the test renderer strips colour so that styled and unstyled compare equal, assert through the same mechanism the
    existing `colourTranscript` test uses.

**Required mutations.** Run each, report the failing test names, then revert:
1. Remove the `IsThinking` skip from `matchLimit`. Test i must fail.
2. Remove it from `matchDenial`. Test j must fail.
3. Make `thinkingLines` return the lines without the marker. Tests a, c, e and i must fail.
4. Remove `"--thinking"` from `harness.go:329` only. `harness_test.go`'s opencode case must fail.

### 6. Checks, the plan, the commit

1. Run the final commands listed in step 0. If `sh scripts/check-coverage.sh` reports a drop, report it; do not
   regenerate the baseline.
2. Copy this plan to `docs/plans/2026-09-26-transcript-thinking.md`.
3. Make a new commit:

   ```
   git add -A && git commit -m "feat(transcript): show builders' thinking; limit and denial scans skip it (#502)"
   ```

## 5. Deletions

Only this one:

1. The codex renderer's deliberate drop of `reasoning` items (`case "reasoning": return nil`). Its test
   `"item.completed reasoning is noise"` is ported (step 5g), not removed.

Nothing else is removed. No test is deleted.

## 6. Stop rather than improvise

Halt and report if any of these happens:
- A `"--standalone"` in `internal/` is not preceded by `"json",` (the sed missed it), or a harness test fails for a
  reason other than the new flag.
- A whole-fixture transcript test changes output for `testdata/opencode.jsonl`, `codex.jsonl` or `claude.jsonl`.
  The fixtures hold no reasoning text, so none should.
- `matchLimit` or `matchDenial` is not at the lines §2 names, or its call sites would have to change.
- Importing `internal/transcript` from `internal/ui` creates a cycle.
- `make lint` flags something in `internal/transcript` that cannot be fixed without restructuring a renderer.
