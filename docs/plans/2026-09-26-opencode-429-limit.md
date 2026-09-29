# opencode provider 429s and out-of-credits failures are detected as usage limits (#616) (2026-09-26)

**For the builder.** A fresh headless process on `zen`, no dialogs, no memory of other rounds.
If a step is impossible as written, or the code contradicts what §1 quotes, STOP and report;
do not bend a test or the design to fit. Halt and report in particular if:

- `internal/harness/harness.go:158-166` does not hold the opencode entry exactly as §1 quotes it, or the
  opencode block of `harnessTableExpected` is not at `internal/harness/harness_test.go:752-776`;
- `transcript.Render("opencode", <fixture line 1 from §3.2>)` does not render to the exact line §1 quotes
  (`  ⎿ error: Error 429: ...`). This fix detects limits in the **rendered** stream tail, so a renderer
  change breaks its premise;
- `TestFixtures` (`internal/transcript/transcript_test.go:14`) turns out to glob into subdirectories: the new
  fixture must sit outside the top-level `testdata/*.jsonl` glob, exactly like `testdata/agy-errors/`;
- any of the three new patterns already exists on the tree you cut, or one of the shipped opencode patterns
  already matches fixture lines 1 or 2 (that would mean the bug is already fixed and this round should be
  reported as such, not implemented);
- `TestTableExactValues` (`internal/harness/harness_test.go:804`) fails after step 3 for any reason other than
  the mirror block being stale.

**Declared scope, closed list** -- create or change exactly these five files, nothing else:

```
internal/transcript/testdata/opencode-errors/results.jsonl   new fixture, 3 lines (§3.2)
internal/harness/harness.go                                  three patterns in the opencode entry (§3.1)
internal/harness/harness_test.go                             the opencode mirror block (§4.1)
internal/availability/limit_test.go                          one helper + one test (§4.2)
docs/plans/2026-09-26-opencode-429-limit.md                  this plan, verbatim (step 5)
```

No change to `cmd/relevo`, to `internal/relevo` runtime code, to `internal/transcript` code, or to
`parseReset`/the reset parsing in `internal/availability/limit.go`. Anything else: halt and report.

## 1. System Overview

On 2026-09-26 a round on `zen` (binding `zen-smoke`, round 2) walked every builder candidate. Two opencode
candidates hit provider limits and relevo neither recognised the limit nor gated the provider: each exit was
booked as `exited (code 1) without a report`, switched as a counted generic failure, and the final halt read
`exited without a report until cleared` -- as if the candidate were broken rather than out of quota. The agy
and codex candidates in the same round were detected correctly (their patterns match their text).

This round adds three regexes to opencode's shipped `LimitPatterns`. On the exit path the daemon scans the
last 40 rendered lines of the round's stream for those patterns; when one matches, `gateOnLimit` records a
`rate_limited` ledger entry for the candidate's **provider** (parsing the line's own reset when it names one)
and switches the round to the next candidate **uncounted**. That is the existing machinery; the only broken
link is that no opencode pattern matches the two provider errors in the wild.

### 1.1 Facts, verified against `0c8da53745d44aa9e64050adea3b10862b961c92` (= `origin/main`)

**(a) The real stream.** The failing round's sealed stream is
`/home/fuad/.local/state/relevo/relevo.db`, table `round_file`, record `01M3FKAGP7XHZTWA7RZ8KGG53R`, name
`001-builder.jsonl` (41 474 bytes, binding `zen-smoke`, round 2). It cannot be read from the builder's tree;
it is pasted here trimmed and anonymised (provider brand, session ids and the OpenRouter key URL redacted --
the OpenRouter message embeds a live workspace key, never paste it anywhere):

```
{"type":"error","timestamp":1790451670876,"sessionID":"ses_<redacted>","error":{"type":"provider.rate-limit","message":"Error 429: You have reached your weekly ExamplePass limit. The limit resets in 23m, please try again later.","status":429}}
{"type":"error","timestamp":1790451684929,"sessionID":"ses_<redacted>","error":{"type":"provider.quota","message":"This request requires more credits, or fewer max_tokens. You requested up to 131072 tokens, but can only afford 34217. (URL redacted)","status":402}}
```

Each was the last event of its process; the process then exited 1 (trailer `relevo-exit:1`).

**(b) What the scan sees.** A limit scan reads `currentBuilderTail(rt, b, 40)`
(`internal/relevo/transcript.go:167-179`), which renders the stream bytes through
`transcript.Render(kind, line)`. opencode's renderer keeps only `error.message`
(`internal/transcript/opencode.go:26-27`):

```go
	case "error":
		return []string{errLine(str(asMap(obj["error"])["message"]))}
```

and `errLine` (`internal/transcript/transcript.go:95-100`) wraps it as `"  ⎿ error: " + msg` (truncated to 200
runes by `oneLine`). So the two lines above reach the scan as:

```
  ⎿ error: Error 429: You have reached your weekly ExamplePass limit. The limit resets in 23m, please try again later.
  ⎿ error: This request requires more credits, or fewer max_tokens. You requested up to 131072 tokens, but can only afford 34217.
```

The structured fields `error.type` (`provider.rate-limit`, `provider.quota`) and `status` never reach the
scan. That is the gap #311 is about; see §1.3.

**(c) Today's opencode patterns** (`internal/harness/harness.go:158-166`, quoted verbatim):

```go
	"opencode": {
		Kind:   "opencode",
		Binary: "opencode",
		LimitPatterns: []string{
			`(?i)rate.?limit(ed)? (reached|exceeded)`,
			`(?i)quota (exceeded|reached)`,
			`(?i)insufficient (credits|quota)`,
			`(?i)RESOURCE_EXHAUSTED`,
		},
```

None matches either rendered line: the first has no `rate limit reached`, no `quota exceeded|reached`, no
`insufficient credits`, no `RESOURCE_EXHAUSTED`; the second speaks of `more credits`, not `insufficient
credits`. Verified by running the exact chain on this tree (a scratch test, since deleted):
`transcript.Render("opencode", <line>)` then `availability.MatchLimit(rendered, <the patterns above>, now, 0)`
returns `ok=false` for both lines. With the three patterns of §3.1 appended it returns, for line 1,
`ok=true Parsed=true Until=now+23m`, and for line 2 `ok=true Parsed=false`.

**(d) The decision point.** On an exit without a report, `internal/relevo/headless.go:942` calls

```go
	next, _, handled, err := gateOnLimit(ctx, rt, tx, b, currentBuilderTail(rt, b, availability.LimitScanLines), false)
```

and `gateOnLimit` (`internal/relevo/switch.go:212-248`) does exactly: `patterns := availability.LimitPatterns(...)`;
`m, ok := availability.MatchLimit(text, patterns, now, rt.Policy.LimitGateDefault())`; if matched, append one
`RateLimited` entry (`Subject` = the candidate's provider, `Until` = `m.Until`, `Note` = `m.Line`), then
`switchBuilder(ctx, rt, tx, b, "rate-limited: "+m.Line, closeOld=false, counted=false)` -- an uncounted switch.
`availability.LimitPatterns` (`internal/availability/limit.go:315-346`) merges the **harness defaults** with
the candidate's own `limit_patterns`, so a harness-table addition covers every opencode candidate.

### 1.2 Design decision: patterns on the rendered line, not the structured event

The issue prefers the structured event (`"type":"provider.rate-limit"` / `"status":429`) where opencode emits
one. On this tree the scan cannot see it: the structured fields are dropped before the scan (§1.1(b)), and
reading them back means reading the raw `NNN-builder.jsonl` tail instead of the rendered tail -- which is
`#311`'s step 1 ("the limit decision point reads the tail of NNN-builder.jsonl before the rendered-log
regex"), planned separately and explicitly out of scope for this round. Within the rendered text, the
strongest available signals are the HTTP status opencode writes into the message (`Error 429`) and the two
provider sentences; the three patterns of §3.1 key on exactly those, and on nothing else.

Rejected alternative (recorded so it is not re-litigated): making `internal/transcript/opencode.go` surface
`error.type` in the rendered line so a pattern could match `provider.rate-limit`. It would change every
opencode error line a human reads (and `transcript_test.go:270-272` plus the `opencode.log` golden), and
`internal/transcript` is presentation-only by contract (`internal/transcript/transcript.go:1-4`). The
structured match belongs on top of #311's raw-tail read, as a small follow-up, not here.

### 1.3 Deliberate deviation: the out-of-credits line gates for the policy default, not "until cleared"

The issue asks that the OpenRouter line gate `openrouter` "until cleared". This round does **not** make the
gate indefinite. A match with no parseable reset takes the policy fallback
(`MatchLimit` -> `now.Add(fallback)`, `internal/availability/limit.go:60-65`; default one hour,
`policy.DefaultLimitGate`, `internal/policy/policy.go:169`). A zero `Until` would mean "until cleared"
(`availability.Entry.Expired`, `internal/availability/ledger.go:60-62`), but the design deliberately refuses
an indefinite gate for a text match -- "garbage in a line that happened to match the limit pattern must not
gate a provider indefinitely" (`internal/availability/limit.go:107-113`). Making a credits match indefinite
is a change to the gate's expiry contract (a second, quota-only pattern list, or a per-pattern expiry mode)
and belongs with the reset/window work (#311, #312), not with detection.

What the round delivers for that line: the `openrouter` provider **is** gated, with the matched line as its
note, and the exit is a rate-limit switch, not a counted generic failure; the duration is the policy's
`limit_gate_default_ms`. The new test pins `Parsed == false` and `Until == now + fallback`, so a later change
to that contract has to be deliberate.

## 2. File Structure

```
internal/
  harness/
    harness.go                          + 3 strings on the opencode entry's LimitPatterns (161-166); 1 why-comment
    harness_test.go                     the opencode block of harnessTableExpected (752-776) mirrors them
  availability/
    limit_test.go                       + opencodePatterns helper (after codexPatterns, ~205)
                                        + TestOpencodeLimitDetectedInRenderedStream (after TestAgyLimitDetectedInRenderedStream, ~233)
  transcript/
    testdata/
      opencode-errors/
        results.jsonl                   new: 3 raw opencode error events (§3.2), read by the availability test
docs/
  plans/
    2026-09-26-opencode-429-limit.md    this plan, saved verbatim (step 5)
```

Untouched, and the fix does not need them: `internal/transcript/opencode.go` (renderer),
`internal/availability/limit.go` (`MatchLimit`, `parseReset`, `LimitPatterns`),
`internal/relevo/{headless,switch,transcript}.go` (the decision point), the README (it describes the scan
generically, it does not list defaults), and every `cmd/relevo` file.

## 3. Data Structures

### 3.1 The three added patterns (`internal/harness/harness.go`, opencode `LimitPatterns`)

Exact strings, appended after `(?i)RESOURCE_EXHAUSTED`:

| pattern | what it matches in the rendered line |
| --- | --- |
| `(?i)error 429` | opencode writes an HTTP failure as `Error <status>: <message>`; this is the status half of the structured event, as it survives into the text |
| `(?i)requires more credits` | the out-of-credits (quota) failure; its status (`402`) is not in the text at all |
| `(?i)reached your .* limit` | the weekly/monthly-limit sentence, for a 429 whose message does not carry `Error 429` |

All three compile, all are case-insensitive by `(?i)` and no `.*` can span a line (`MatchLimit` matches one
line at a time). One comment above them says *why these and not the structured fields* -- the structured
`error.type`/`status` never reach the log -- with no issue number and no history. `TestPatternsSetAndCompile`
(`internal/harness/harness_test.go:287`) will hold them to compiling.

### 3.2 The fixture: `internal/transcript/testdata/opencode-errors/results.jsonl`

Three lines, one JSON event each, newline-terminated, exactly as below (generic provider name, redacted
session ids, OpenRouter URL dropped; the shapes and the message text are the captured ones):

```
{"type":"error","timestamp":1790451670876,"sessionID":"ses_a","error":{"type":"provider.rate-limit","message":"Error 429: You have reached your weekly ExamplePass limit. The limit resets in 23m, please try again later.","status":429}}
{"type":"error","timestamp":1790451684929,"sessionID":"ses_b","error":{"type":"provider.quota","message":"This request requires more credits, or fewer max_tokens. You requested up to 131072 tokens, but can only afford 34217.","status":402}}
{"type":"error","timestamp":1790452426340,"sessionID":"ses_c","error":{"type":"provider.no-route","message":"Model unavailable: example/model","status":404}}
```

Line 1 and line 2 are the two real failures (brand genericised); line 3 is the guard: a provider error that
is *not* a limit, so the test also pins that the new patterns are not a catch-all. The file lives in a
subdirectory because `TestFixtures` (`internal/transcript/transcript_test.go:14-45`) globs `testdata/*.jsonl`
top-level and expects a sibling `<kind>.log` golden for every hit; `testdata/agy-errors/results.jsonl` is the
precedent.

### 3.3 What the test asserts on (`internal/availability/limit.go`, existing)

`LimitMatch{Line string, Until time.Time, Parsed bool}` -- `Line` is the matched rendered line capped at 200
runes, `Until` is UTC, `Parsed` says whether `Until` came from the line's own reset text. No new fields, no
new types, no signature changes.

## 4. Interface Definitions & Component Contracts

### 4.1 `harness.Harness.LimitPatterns []string` (existing field; new values)

Doc comment on the field (`internal/harness/harness.go:95-99`): default regexes for provider-quota text; every
default must compile; case-insensitivity written in with `(?i)`.
- **Preconditions:** none at runtime.
- **Postconditions:** for kind `opencode`, the three §3.1 strings are present; the mirror
  `harnessTableExpected["opencode"]` at `internal/harness/harness_test.go:752-776` carries the identical
  block, or `TestTableExactValues` fails.
- **Dependency:** none.
- **Error types:** none; an uncompilable default is a test failure (`TestPatternsSetAndCompile`), never a
  runtime error.

### 4.2 New test artifacts in `internal/availability/limit_test.go`

Both in package `availability` (internal test), the same shape as the agy pair right above them. No
production symbol is added.

```
func opencodePatterns(t *testing.T) []*regexp.Regexp
```
Compiles `harness.Lookup("opencode").LimitPatterns` through `regexp.MustCompile`; fails the test if the kind
is missing. Mirrors `agyPatterns` (`:179`) and `codexPatterns` (`:194`).

```
func TestOpencodeLimitDetectedInRenderedStream(t *testing.T)
```
- **Reads:** `filepath.Join("..", "transcript", "testdata", "opencode-errors", "results.jsonl")`.
- **Setup:** `now := testNow()`; `fallback := time.Hour`; `patterns := opencodePatterns(t)`.
- **Per row** (index into the file, in order):
  `rendered := strings.Join(transcript.Render("opencode", []byte(rawLine)), "\n")`, then
  `m, ok := MatchLimit(rendered, patterns, now, fallback)`.
- **Row expectations (pins):**
  | row | ok | Parsed | Until |
  | --- | --- | --- | --- |
  | 1 (rate-limit, 429) | true | true | `now.Add(23 * time.Minute)` |
  | 2 (quota, 402) | true | false | `now.Add(fallback)` |
  | 3 (no-route, 404) | false | -- | -- |
- **Also asserts:** the rendered text of row 1 equals
  `"  ⎿ error: Error 429: You have reached your weekly ExamplePass limit. The limit resets in 23m, please try again later."`
  -- so a renderer change that stops putting the message in the line fails this test instead of silently
  passing it.
- **Preconditions:** the fixture exists with three lines; `harness.Lookup("opencode")` resolves.
- **Postconditions:** the bug is pinned red before §3.1 lands (step 2) and green after (step 3).
- **Error types:** none; `t.Fatal` on the fixture or a missing kind.

### 4.3 `availability.MatchLimit`, `availability.LimitPatterns`, `relevo.gateOnLimit` (existing, unchanged)

Contract restated because the plan hinges on it; **no code change** to any of them.
- `MatchLimit(text string, patterns []*regexp.Regexp, now time.Time, fallback time.Duration) (LimitMatch, bool)`
  -- scans `text` line by line from the last line backwards, skips thinking lines, returns the first (most
  recent) matching line; `Until = parseReset(line, now)` when that succeeds, else `now.Add(fallback)`.
- `LimitPatterns(d Deps, token string) []*regexp.Regexp` -- harness defaults for the token's kind, then the
  candidate's own `limit_patterns`, compiled. A harness addition therefore applies to every opencode
  candidate, including `cline-pass` and `openrouter`.
- `gateOnLimit(ctx, rt, tx, b, text, closeOld)` -- unchanged; on a match it records one `RateLimited` entry
  (`Subject = ProviderOf(b.BuilderCandidate)`, `Until = m.Until`, `Note = m.Line`, `Source = "relevo"`) and
  switches uncounted. A ledger write failure prints to stderr and the switch proceeds.

## 5. High-Level Pseudocode

The exit-without-report decision point, with the fix in place and the parts it skips marked:

```
on exit of an opencode builder, no report on disk:
    codeText := exit code from the stream trailer            # 1
    ... exit log entry, stop / oom / lost-to-restart / escape checks ...   # unchanged
    tail := currentBuilderTail(rt, b, 40)                    # rendered lines (Facts (b))
    patterns := harness(opencode).LimitPatterns + candidate.limit_patterns
    m, ok := MatchLimit(tail, patterns, now, policy.LimitGateDefault())
    if ok:
        RateLimited entry: provider(cline-pass|openrouter), Until=m.Until, Note=m.Line
        switchBuilder(reason="rate-limited: "+m.Line, closeOld=false, counted=false)
        return                                                   # the round moves on, uncounted
    # unreachable for the two real lines once §3.1 lands:
    nudgeResume(...) / RoundExcluded += candidate / counted switch / halt
```

Before the fix, `ok` is false for both lines and the marked lines run -- that is the reported bug. The
`Until` for line 1 is `now+23m` because `parseReset`'s duration form reads `resets in 23m`
(`internal/availability/limit.go:124-146`); line 2 has no reset and takes the fallback (§1.3).

## 6. Error Handling Strategy

No new error types, no signature changes, no new failure path. What the round relies on, and what it leaves
alone:

| failure | behaviour |
| --- | --- |
| a new pattern does not compile | `TestPatternsSetAndCompile` fails; cannot ship |
| the rendered line changes shape (renderer upgrade) | the new test's rendered-text assertion fails; step 2/3 revise nothing, they report |
| ledger write on a match fails | existing path: stderr line, switch proceeds (unchanged) |
| false positive (limit-shaped text, another cause) | one gate and one uncounted switch, both visible in `relevo policy` / `relevo log`; undone with `relevo available <provider>`; the guard row of the fixture keeps the patterns from drifting into a catch-all |
| no reset parses | policy fallback, `Parsed=false` (§1.3) |

Observability is unchanged: `slog.Warn("provider rate-limited", ...)` on a match, the `rate-limited: <line>`
switch note, the ledger note carrying the matched line.

## 7. Working Efficiently

This section is before step 1 on purpose: a round costs one model step per tool call, so batch.

- **Batch the reads first, one batch, and read only what the plan names:** `internal/harness/harness.go:90-200`,
  `internal/harness/harness_test.go:280-320,700-816`, `internal/availability/limit.go:60-130,300-347`,
  `internal/availability/limit_test.go:175-235`, `internal/transcript/opencode.go` (whole, 33 lines),
  `internal/transcript/transcript.go:14-100`, `internal/transcript/transcript_test.go:14-45`,
  `internal/relevo/switch.go:199-248`, `internal/relevo/headless.go:770-800,930-950`. Do not grep for what
  this plan already located.
- **One edit per file.** `harness.go` (patterns + comment), `harness_test.go` (mirror block), `limit_test.go`
  (helper + test, one edit), the fixture (one `write`). No edit to any other file.
- **Focused loop:** `go test ./internal/availability/ -run TestOpencodeLimitDetectedInRenderedStream -count=1 -v`
  and `go test ./internal/harness/ -run 'TestPatternsSetAndCompile|TestTableExactValues' -count=1`. Fix every
  reported error before the next run; do not run the full suite until step 5.
- **Full check, once, at the end:** `gofmt -l $(git ls-files '*.go')` (must print nothing),
  `sh scripts/check-comments.sh`, `sh scripts/check-filesize.sh`, then `make check`. If `make check` is slow or
  a temp dir is missing, `mkdir -p "$HOME/.cache/go-tmp"` and `export TMPDIR="$HOME/.cache/go-tmp"
  GOTMPDIR="$HOME/.cache/go-tmp"` first. `make check` runs `go test -race -count=1 -cover ./...` plus the
  coverage baseline; the change adds executor statements only to `internal/harness`'s table literal (init-run)
  and strings, so the baseline is not regenerated.
- Comments say *why* only: no issue numbers, no `§`, no plan references in `.go` files. Test names say what
  they pin. Fixture names are generic.

## 8. Ordered Implementation Steps

### Step 1 -- the fixture

Create `internal/transcript/testdata/opencode-errors/results.jsonl` with exactly the three lines of §3.2
(one line each, trailing newline at EOF).

- **Depends on:** nothing.
- **Verification:** `go test ./internal/transcript/ -count=1` passes (the new file is a subdirectory, so
  `TestFixtures` must not pick it up); `git status --short` shows only the new file untracked.

### Step 2 -- the test, red

In `internal/availability/limit_test.go`: add `opencodePatterns` after `codexPatterns` (ends line 205) and
`TestOpencodeLimitDetectedInRenderedStream` after `TestAgyLimitDetectedInRenderedStream` (ends line 232),
per §4.2.

- **Depends on:** step 1.
- **Verification:** `go test ./internal/availability/ -run TestOpencodeLimitDetectedInRenderedStream -count=1 -v`
  **fails** on rows 1 and 2 (`matches = false, want true`) and does not fail row 3. Paste that failure text in
  the report: it is the recorded proof that the bug exists on this tree. A green run here is a halt (§ preamble).

### Step 3 -- the patterns

`internal/harness/harness.go:161-166`: append the three §3.1 strings to the opencode `LimitPatterns`, with one
line of why-comment. `internal/harness/harness_test.go:752-776`: mirror the exact same three strings in the
opencode block of `harnessTableExpected`.

- **Depends on:** step 2.
- **Verification:** `go test ./internal/availability/ -run TestOpencodeLimitDetectedInRenderedStream -count=1 -v`
  passes, all three rows; `go test ./internal/harness/ -run 'TestPatternsSetAndCompile|TestTableExactValues' -count=1`
  passes; `git diff` for these two files shows only the §3.1 strings, the comment and the mirror.

### Step 4 -- mutation check

Break the condition the round turns on, confirm the named test fails, restore:

1. Delete `(?i)requires more credits` from `harness.go`. Expected: `TestOpencodeLimitDetectedInRenderedStream`
   fails on row 2 (`matches = false, want true`). Restore, re-run: green.
2. Replace `(?i)error 429` and `(?i)reached your .* limit` with `(?i)zzz-never-matches` (both, together).
   Expected: the same test fails on row 1, and row 3 still passes. Restore, re-run: green.

- **Depends on:** step 3.
- **Report:** the test name, the row that failed, the failure line, and that the restored run is green.
- If either mutation leaves the test green, HALT and report: the test does not pin the change.

### Step 5 -- full check, the plan, the commit

1. Run the §7 full check once: `gofmt -l $(git ls-files '*.go')`, `sh scripts/check-comments.sh`,
   `sh scripts/check-filesize.sh`, `make check`. All must pass; a lint or coverage failure is fixed in the
   touched files, never by an exclusion (CLAUDE.md).
2. Save this plan verbatim as `docs/plans/2026-09-26-opencode-429-limit.md` (drop the trailing `relevo` status
   block if the file you were given carries one).
3. Commit everything in one commit, message:
   `fix(opencode): a provider 429 or an out-of-credits failure gates the provider (#616)`.
   Nothing left uncommitted; no other files in the commit.
4. Report: `git diff --stat` against the five declared files, the focused test output, the mutation results,
   and the `make check` tail.

## 9. Stop rather than improvise

Beyond the preamble: if `MatchLimit` is not reached at all on the exit path (a refactor moved the scan), if
`currentBuilderTail` is no longer what feeds `gateOnLimit`, or if the two fixture lines stop being the exit
path's last rendered lines because the tail window or the renderer changed -- stop and report with the
`file:line` you found. A halt that surfaces a design error is worth more than a green suite that bent a test
to fit.
