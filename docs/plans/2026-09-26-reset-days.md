# The reset opencode really writes is a day component: read `d` in a duration reset (#311) (2026-09-26)

**For the builder.** A fresh headless process on `zen`, no dialogs, no memory of other rounds.
If a step is impossible as written, or the code contradicts what §1 quotes, STOP and report;
do not bend a test or the design to fit. Halt and report in particular if:

- `internal/availability/limit.go` does not hold the regexes, the `parseDurationReset` switch and the
  7-day window exactly as §1.1(a) quotes them (lines 25-32, 125-146, 285-303);
- `internal/availability/limit_test.go` does not hold `parseResetCases` at lines 28-151 and
  `TestParseReset` at 153 as §1.1(a) quotes them;
- the two rows of step 1 **pass before** step 2's fix (the day form already parses: the bug is fixed
  on your tree -- report that instead of implementing);
- anything in the tree already reads a `d`/`day` unit in the duration path;
- the parallel detection round's changes (`internal/harness/harness.go` opencode patterns,
  a `TestOpencodeLimitDetectedInRenderedStream`, `internal/transcript/testdata/opencode-errors/`)
  are on your tree: leave them alone, they do not conflict with this change;
- `make check` fails in a file this plan does not name: report, do not widen the scope.

**Declared scope, closed list** -- create or change exactly these three files, nothing else:

```
internal/availability/limit.go        the two duration regexes and parseDurationReset's unit switch (§3, §4.1)
internal/availability/limit_test.go   two rows in parseResetCases (§4.2)
docs/plans/2026-09-26-reset-days.md   this plan, verbatim (step 4)
```

No change to `internal/relevo`, `internal/harness`, `internal/transcript`, `internal/policy`,
`cmd/relevo` or the README. Anything else: halt and report.

**Verdict on the issue as written.** Issue #311 asks the limit gate to read a reset time from
opencode's **structured** fields (`retryAfterMs`, `rateLimit.reset`) in the raw
`NNN-builder.jsonl`. That cannot be built, and §1.1(b) proves it from two directions: no captured
opencode stream event has ever carried a reset field (0 events across 463 sealed streams), and
opencode's own session-error schema carries only `{type, message, status?}` -- the
`retryAfterMs`/`rateLimit` values are built from the response headers and dropped before the event is
written. Writing that parser would be writing it against a shape nothing produces, which the issue
itself forbids ("do not write the parser against a guessed shape").

What #311's *goal* needs, and what is real and visible in the captures, is smaller and in the text
half: opencode's weekly limit is written as **"resets in 1d 5h"**, and `parseReset`'s duration form
reads hours, minutes and seconds but **not days**, so the gate falls back to the policy default -- one
hour for a 29-hour reset, which is exactly the issue's complaint ("a 5-hour or weekly limit comes
back after an hour and fails again"). That reset extraction is what this round builds. It does not
depend on the parallel detection round to be implemented -- each change is green on its own -- but
its user-visible effect on an opencode 429 arrives with that round (§1.3).

---

## 1. System Overview

A builder hits a provider limit, exits without a report, and relevo's decision point scans the last
40 rendered lines of the round's stream; a limit pattern match produces
`LimitMatch{Line, Until, Parsed}` (`availability.MatchLimit`, `internal/availability/limit.go:65`),
`Until` comes from `parseReset` on the matched line when that succeeds, else from
`policy.LimitGateDefault()` (one hour). `gateOnLimit` (`internal/relevo/switch.go:212-248`) records
`Until` as the `rate_limited` ledger entry for the candidate's provider and switches the round
uncounted.

For an opencode provider 429 the message -- the only place the reset exists, §1.1(b) -- is rendered
verbatim as `  ⎿ error: <message>` (`internal/transcript/opencode.go:26-27`), and the message reads
`… The limit resets in 1d 5h …`. `parseReset`'s duration form is blind to `d`, so `MatchLimit` returns
`Parsed: false` and `Until = now + 1h` (§1.1(d), measured). This round teaches the duration form the
`d` unit: the whole change is the two package-level regexes plus one `case` in `parseDurationReset`.

### 1.1 Facts, verified against `0c8da53745d44aa9e64050adea3b10862b961c92` (= `origin/main`)

**(a) The reset extraction, quoted verbatim.** `internal/availability/limit.go`:

```go
// 25-27
// durationRe matches "resets in 2h48m52s", "try again in 5 min", "retry after
// 30s" and captures the duration component run.
var durationRe = regexp.MustCompile(`(?i)(?:resets?|try again|retry)\s+(?:in|after)\s+~?((?:\d+\s*(?:hours?|hr|h|minutes?|min|m|seconds?|sec|s)\s*)+)`)

// 29-32
// durationComponentRe pulls one "<number><unit>" component at a time out of the
// captured run above. Units are checked by first letter (h/m/s), so the
// alternation only needs to avoid a short form swallowing a longer one.
var durationComponentRe = regexp.MustCompile(`(?i)(\d+)\s*(hours?|hr|h|minutes?|min|m|seconds?|sec|s)`)

// 125-146
func parseDurationReset(line string, now time.Time) (time.Time, bool) {
	m := durationRe.FindStringSubmatch(line)
	if m == nil {
		return time.Time{}, false
	}
	var d time.Duration
	for _, c := range durationComponentRe.FindAllStringSubmatch(m[1], -1) {
		n, err := strconv.Atoi(c[1])
		if err != nil {
			continue
		}
		switch unit := strings.ToLower(c[2]); unit[0] {
		case 'h':
			d += time.Duration(n) * time.Hour
		case 'm':
			d += time.Duration(n) * time.Minute
		case 's':
			d += time.Duration(n) * time.Second
		}
	}
	return windowedReset(now.Add(d), now, limitWindowShort)
}
```

`parseReset` (`:114-122`) tries the duration form, then the absolute date, then a clock time. The
window that bounds every form, verbatim:

```go
// 285-290
func windowedReset(t, now time.Time, window time.Duration) (time.Time, bool) {
	if !inLimitWindow(t, now, window) {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// 292-294
// limitWindowShort bounds the vaguer reset forms -- a duration and a clock time,
// both easy to misread -- to (now, now+7d].
const limitWindowShort = 7 * 24 * time.Hour

// 296-298
// limitWindowDated bounds a reset that names a full date with a year: a full
// calendar date is hard to misread, so it earns the longer window.
const limitWindowDated = 31 * 24 * time.Hour

// 300-303
// inLimitWindow is parseReset's (now, now+max] bound.
func inLimitWindow(t, now time.Time, max time.Duration) bool {
	return t.After(now) && !t.After(now.Add(max))
}
```

So a day count of 7 or more is refused by the short window and the gate keeps the policy default; the
longer window belongs to a reset that names a full date, year included.

`internal/availability/limit_test.go:28-151` is `parseResetCases`, the table `TestParseReset` (`:153`)
walks; the duration rows are lines 38-79, the refusal rows 136-150.

**(b) What opencode's stream really carries.** Two captured events from the real zen round (binding
`zen-smoke` round 2, sealed in this machine's `~/.local/state/relevo/relevo.db`, table `round_file`,
record `01M3FKAGP7XHZTWA7RZ8KGG53R`, name `001-builder.jsonl`). The provider brand is genericised to
`ExamplePass`; session ids are redacted; one real message embeds a live API key URL and is dropped
here and only its opening words are shown:

```json
{"type":"error","timestamp":1790451670876,"sessionID":"ses_<redacted>","error":{"type":"provider.rate-limit","message":"Error 429: You have reached your weekly ExamplePass limit. The limit resets in 1d 5h, please try again later.","status":429}}
{"type":"error","timestamp":1790451684929,"sessionID":"ses_<redacted>","error":{"type":"provider.quota","message":"This request requires more credits, or fewer max_tokens. …","status":402}}
```

The event's `error` object is `{type, message, status}` and nothing else. That is opencode's own
`Session.StructuredError` schema, which the opencode installed on the planner's machine
(`opencode v2.0.18`, Arch package `opencode-2.0.18-1`) defines as exactly
`{type, message, status?}` -- the provider error is mapped through a helper that keeps `type`,
`message` and `f.http?.status` and drops everything else, while the rate-limit error class itself does
carry `retryAfterMs` and `rateLimit{retryAfterMs,limit,remaining,reset}` built from
`x-ratelimit-*-{limit,remaining,reset}` and `anthropic-ratelimit-*-{limit,remaining,reset}`. The CLI
then writes the session error verbatim into the `--format json` stream. The reset exists inside
opencode and is dropped before the stream. The captured events above are the version-independent
half of this fact: whatever opencode wrote them emits `type`, `message` and `status`.

Census over every sealed stream on this machine (`round_file` rows whose name ends `.jsonl`: 463
files, 118,514 lines): **no event-level `error` carries `retryAfterMs` or `rateLimit`** -- zero hits;
the only 17 lines containing those tokens are tool and reasoning payloads from planning rounds that
discuss the field names. A grep of the live `~/.local/state/relevo/*/NNN-builder.jsonl` streams agrees.

**(c) The real reset messages.** Every distinct provider message in the sealed streams that contains
"reset" (22 in all): 20 are the agy form `Resets in 2h48m52s` / `Resets in 115h18m33s` (hours,
minutes, seconds -- all parseable today), and exactly 2 are the opencode weekly form with a **day**
component: `resets in 1d 5h` and `resets in 1d 4h`. No other unit is anywhere in the captures.

**(d) The measured gap.** Run against this tree (`go test ./internal/availability/ -run TestZZ…`,
a scratch probe, deleted after use):

```
parseReset("Error 429: You have reached your weekly ExamplePass limit. The limit resets in 1d 5h, please try again later.")   -> ok=false
parseReset("… The limit resets in 23m, …")                                                                                     -> ok=true, +23m
MatchLimit(<that weekly line, rendered>, <shipped opencode patterns + the detection round's three>, now, time.Hour)
    -> ok=true, Parsed=false, Until=now+1h        # detection lands, the duration is the default
```

So: `d` is why a weekly limit is gated for an hour. Messages that stay in hours, minutes and seconds
(the agy form, and opencode's own `23m`) are already right, and the 7-day window has room:
1d5h = 29h.

**(e) Where the reset is consumed (context; not touched).** `gateOnLimit`
(`internal/relevo/switch.go:212-248`) calls `availability.MatchLimit(text, patterns, now,
rt.Policy.LimitGateDefault())` and records `m.Until` in the ledger entry; the exit decision point is
`internal/relevo/headless.go:942` (and `:744` for a report without a marker); the round-budget scan is
`internal/relevo/reconcile.go:243`. The scan reads the rendered stream tail
(`currentBuilderTail`, `internal/relevo/transcript.go:167`).

### 1.2 Verdict: the issue's structured half cannot be built; the text half can

- **Not buildable, not built**: reading `retryAfterMs` / `rateLimit.reset` from the jsonl. There is no
  such field in any captured event and none in opencode's event schema (§1.1(b)). The upstream gap is
  real and worth reporting: opencode's session-error mapper drops the values its own rate-limit error
  class holds, so a future opencode that includes them (a captured fixture first, as the issue
  requires) is the precondition for that half. If it ever lands, the read belongs at the gate
  (`gateOnLimit`, `internal/relevo/switch.go:212`), over a raw sibling of `currentBuilderTail`
  (`internal/relevo/transcript.go:167`) -- a separate round, no code for it here.
- **Buildable now, built here**: the day component of the one reset form opencode actually writes.
  Without it, any change that makes opencode's 429 a limit match (§1.1(d)) gates 29 hours of quota for
  one hour and re-fails. This is the smallest change that makes the gate honour the reset the provider
  names, and it is pure `parseReset` -- no detection, no plumbing, no new dependency.

### 1.3 The boundary with the parallel detection round (issue #616)

That round adds three patterns to opencode's shipped `LimitPatterns` and touches a testdata fixture;
it explicitly leaves `parseReset` and `internal/relevo` alone. This round touches `parseReset`'s
duration grammar only. Neither needs the other to be *implemented*: each is green on its own. They
meet only in what a user sees -- with detection in place the weekly line matches and `Until` becomes
`now+29h` instead of `now+1h`; without detection, no opencode pattern matches that line at all and
this change is dormant for opencode (it still matters for a candidate whose own `limit_patterns`
match a day-unit reset). Land either first; the merge is a trivial append in the one shared test file.

## 2. File Structure

```
internal/
  availability/
    limit.go        durationRe and durationComponentRe gain the day unit (§4.1, lines 25-32);
                    parseDurationReset gains case 'd' (line 137); two doc comments kept true
    limit_test.go   two rows in parseResetCases, inserted after line 67 (§4.2)
docs/
  plans/
    2026-09-26-reset-days.md   this plan, saved verbatim (step 4)
```

No new file, no new function, no new type: `parseDurationReset` already *is* the reset-extraction
function, and the two regexes and its switch are its only inputs. A new file would not be natural.

### 2.1 Functions and values this round touches (the conflict map for §1.3)

| symbol | file:line | this round |
| --- | --- | --- |
| `durationRe` | `internal/availability/limit.go:27` | value extended with `days?|d` |
| `durationComponentRe` | `internal/availability/limit.go:32` | value extended with `days?|d` |
| `parseDurationReset` | `internal/availability/limit.go:125-146` | one `case 'd'` added, nothing else |
| `parseReset` | `internal/availability/limit.go:114-122` | unchanged (its duration branch gains day support through the above) |
| `MatchLimit`, `LimitPatterns`, `LimitMatch` | `internal/availability/limit.go:19-23,65-86,315-346` | unchanged |
| `gateOnLimit`, `currentBuilderTail`, `transcript.Render` | `internal/relevo/switch.go:212`, `internal/relevo/transcript.go:167`, `internal/transcript/opencode.go:5` | unchanged |
| `parseResetCases`, `TestParseReset` | `internal/availability/limit_test.go:31,153` | two rows appended to the table |

The sibling round writes `internal/harness/harness.go`, `internal/harness/harness_test.go`,
`internal/transcript/testdata/opencode-errors/` and appends a *new* function to
`internal/availability/limit_test.go` after ~line 205. The only shared file is that test file, and the
two edits do not overlap: this plan inserts into the table at line 67.

## 3. Data Structures

**No new types, no new fields.** `LimitMatch{Line string, Until time.Time, Parsed bool}` is unchanged,
and so is `parseResetCases`' anonymous row struct `{name, line string; now, want time.Time; ok bool}`.

The values that change:

| value | today | after |
| --- | --- | --- |
| `durationRe`'s unit alternation | `hours?|hr|h|minutes?|min|m|seconds?|sec|s` | `days?|d|hours?|hr|h|minutes?|min|m|seconds?|sec|s` |
| `durationComponentRe`'s unit alternation | as above | as above |
| unit → duration mapping in `parseDurationReset` | `h`=1h, `m`=1m, `s`=1s | the same, plus `d` = 24h |

Constraints kept: the first letter decides the unit, and `d`, `h`, `m`, `s` are distinct, so no
alternative can swallow another (the comment at `limit.go:30-31` says exactly that and is updated to
`(h/m/s/d)`). The `(now, now+7d]` window (`limitWindowShort`) is unchanged: `1d 5h` = 29h passes,
`8d` is refused.

## 4. Interface Definitions & Component Contracts

### 4.1 `parseDurationReset(line string, now time.Time) (time.Time, bool)` -- `internal/availability/limit.go:125`

Single responsibility: read the `resets in <run>`/`try again in <run>`/`retry after <run>` duration
form out of one line and turn it into the reset instant.
- **Signature unchanged**; only the accepted grammar grows (the day unit) and the mapping gains `d`.
- **Preconditions:** `now` is the caller's clock.
- **Postconditions:** returns `(t, true)` with `t` in UTC and `now < t <= now+7d`, or `(zero, false)`.
  A duration that lands outside the window, a zero-length duration, or an unparseable number is
  `(zero, false)` -- never a partial or negative gate.
- **Error types:** none; it never errors and never panics on any input.
- **Dependencies:** `durationRe`, `durationComponentRe`, `windowedReset`, `limitWindowShort` -- all
  package-private and unchanged except the two regex values and the switch.
- **Callers:** `parseReset` only. No behaviour change for hour/minute/second inputs.

### 4.2 The two new rows of `parseResetCases` (`internal/availability/limit_test.go`)

Inserted between line 67 (`},` of the `long duration within 7 days` row) and line 68 (`{` of the
`try again in minutes` row). Exact text:

```go
	{
		name: "weekly limit resets in a day and hours",
		line: "Error 429: You have reached your weekly ExamplePass limit. The limit resets in 1d 5h, please try again later.",
		want: parseResetNow.Add(29 * time.Hour),
		ok:   true,
	},
	{
		name: "day component past the short window",
		line: "try again in 8d",
		ok:   false,
	},
```

- Row 1 is the real captured weekly-limit message (§1.1(b)), brand genericised. It pins that a
  day-and-hours duration is one reset of 1d+5h = 29h -- today it fails (`ok = false`), which is the
  bug, and it is the row the mutation step names.
- Row 2 pins the other half: the new unit does **not** widen the short window, so a day count past
  7 days is still refused and the caller falls back to the policy default (this row is green both
  before and after the fix).
- Both rows run under the existing `TestParseReset` (`:153`); no new test function and no fixture
  file. The `line` is the message the renderer puts after `"  ⎿ error: "`; the reset grammar does not
  see that prefix.

### 4.3 Unchanged contracts this plan relies on (restated, no edits)

- `parseReset(line, now)`: duration form, then absolute date, then clock time; first success wins
  (`limit.go:107-122`).
- `MatchLimit(text, patterns, now, fallback)`: scans lines backwards, skips thinking lines; on a
  pattern match, `Until = parseReset(line, now)` or `now.Add(fallback)`, and `Parsed` says which
  (`limit.go:60-86`).
- `gateOnLimit`: on a match, one `RateLimited` entry with `Until = m.Until`, then an uncounted switch
  (`switch.go:212-248`). A match that names no readable reset keeps today's one-hour default.

## 5. High-Level Pseudocode

```
parseReset(line, now):                       # unchanged order
    duration form  -> parseDurationReset     # now understands d
    date form      -> parseDateReset
    clock form     -> parseClockReset

parseDurationReset(line, now):               # the whole change lives here
    m := durationRe(line)                    # alternation now includes days?|d
    if no match: return (zero, false)
    d := 0
    for each "<number><unit>" in m's captured run:      # durationComponentRe, now day-aware
        n := atoi(number)                               # unparseable -> skip this component
        switch first letter of lowercased unit:
            'd': d += n * 24h                           # new
            'h': d += n * 1h
            'm': d += n * 1m
            's': d += n * 1s
    return windowedReset(now + d, now, 7d)   # (now, now+7d] or refusal, unchanged

end to end, opencode 429 exits without a report:
    tail := rendered last 40 lines               # "  ⎿ error: … The limit resets in 1d 5h …"
    m, ok := MatchLimit(tail, patterns, now, policy.LimitGateDefault())
    # after the sibling detection round: ok = true
    #   before this round: Parsed = false, Until = now+1h
    #   after this round:  Parsed = true,  Until = now+29h
    gateOnLimit: ledger entry Until = m.Until, uncounted switch to the next candidate
```

## 6. Error Handling Strategy

No new error types, no new failure path, no signature change. What the round relies on:

| failure | behaviour |
| --- | --- |
| the day unit is not added correctly (only one of the two regexes) | row 1 of §4.2 fails: `durationComponentRe` without `d` drops the day component and reads `1d 5h` as 5h; `durationRe` without `d` refuses the line |
| `case 'd'` missing while the regex accepts it | row 1 fails with a value mismatch (`+5h`, not `+29h`) -- that is the named mutation of step 3 |
| a nonsense or huge day count | `strconv.Atoi` failure skips the component; an out-of-window or non-positive total is refused by `windowedReset`, so the gate keeps the policy default (pre-existing, unchanged for h/m/s too) |
| a day reset larger than 7 days (a monthly limit) | refused by `limitWindowShort`; the policy default stands. Widening the window for declared monthly limits is a separate piece of work and out of this round's scope |
| the reset is available but nothing matched the line | unchanged: detection is the parallel round's; this change only improves `Until` once a match exists |

Comments follow the repo rule: say *why*, never cite an issue, a PR, a spec section or history. The
two comments this round rewrites are the `durationRe` example list and the `durationComponentRe`
first-letter list -- both would otherwise be false about the accepted grammar.

## 7. Working Efficiently

- **One batch of reads, nothing else, no searching.** Read only:
  `internal/availability/limit.go:24-46,107-146,283-303`;
  `internal/availability/limit_test.go:28-56,60-79,136-177`;
  `internal/relevo/switch.go:212-248` and `internal/transcript/opencode.go:1-33` (context only).
  Every location this plan needs is named; do not grep for it.
- **Two edits, one per file.** `internal/availability/limit_test.go` gets one insert (§4.2, between
  lines 67 and 68). `internal/availability/limit.go` gets three hunks (lines 25-27, 29-32, 137); do
  them as one edit of the file if the tool allows, otherwise three, then `gofmt`.
- **Iterate on the focused command, name the failure.** `go test ./internal/availability/ -run TestParseReset -count=1 -v`
  (drop `-v` once the row names are known). Fix every reported line before the next run; the row name
  `TestParseReset/weekly_limit_resets_in_a_day_and_hours` is the one that moves.
- **Full check once, at the end:** `make check`. Individual gates if you want them separately:
  `gofmt -l $(git ls-files '*.go')` and `sh scripts/check-comments.sh`. `make e2e` is not part of
  check and is not needed.
- If the module cache or tmp is not writable on `zen`, first `mkdir -p $HOME/.cache/go-tmp` and
  `export TMPDIR=$HOME/.cache/go-tmp GOTMPDIR=$HOME/.cache/go-tmp`.
- Do not commit the plan separately from the change: step 4 commits both together.

## 8. Ordered Implementation Steps

### Step 1 -- the red rows (deliverable: the failing test)

- **File:** `internal/availability/limit_test.go`, one insert after line 67, exact text in §4.2.
- **Depends on:** nothing.
- **Verification:** `go test ./internal/availability/ -run TestParseReset -count=1` fails with
  `--- FAIL: TestParseReset/weekly_limit_resets_in_a_day_and_hours` and
  `parseReset("Error 429: You have reached your weekly ExamplePass limit. The limit resets in 1d 5h, please try again later.") ok = false, want true`;
  the `day component past the short window` row passes. If row 1 passes, the tree is already fixed:
  halt and report.

### Step 2 -- the day unit (deliverable: green)

- **File:** `internal/availability/limit.go`:
  1. lines 25-26, `durationRe`'s comment: replace the two lines with
     `// durationRe matches "resets in 2h48m52s", "try again in 5 min", "resets in` /
     `// 1d 5h", "retry after 30s" and captures the duration component run.`
     (same two lines, so the line numbers below do not move);
  2. line 27, `durationRe`: replace the unit alternation
     `hours?|hr|h|minutes?|min|m|seconds?|sec|s` with
     `days?|d|hours?|hr|h|minutes?|min|m|seconds?|sec|s`;
  3. lines 29-31, `durationComponentRe`'s comment: `(h/m/s)` becomes `(h/m/s/d)`;
  4. line 32, `durationComponentRe`: the same alternation swap;
  5. line 137, `parseDurationReset`: insert `case 'd':` with `d += time.Duration(n) * 24 * time.Hour`
     immediately before `case 'h':`.
  Apply each hunk by its text; the line numbers are the ones this plan quotes from `origin/main`, and
  a wrap in a comment above would shift them.
- **Depends on:** step 1 (the rows pin the behaviour).
- **Verification:** the focused command passes every row; `gofmt -l internal/availability/limit.go`
  prints nothing; `sh scripts/check-comments.sh` prints `check-comments: ok`.

### Step 3 -- mutation check (deliverable: a named failure, then restored)

- **Do:** delete the `case 'd': d += …` lines inserted in step 2 (leave the regexes).
- **Run:** `go test ./internal/availability/ -run TestParseReset -count=1`
- **Verification:** `TestParseReset/weekly_limit_resets_in_a_day_and_hours` fails with a value
  mismatch (`2026-09-14 01:13:00 +0000 UTC, want 2026-09-15 04:13:00 +0300 EEST` on the shared clock).
  Restore the case, re-run: green. Report the subtest name and the failure line. Deleting the regex
  alternation instead of the case fails the same named subtest (in its `ok = false` form), so either
  mutation is acceptable; name which one you ran.

### Step 4 -- full check, the plan, the commit (deliverable: one commit)

- **Depends on:** steps 1-3.
- **Run `make check`** (from the repo root). Everything it reports must pass; a failure in a file this
  plan does not name is a halt-and-report, not a fix.
- **Then:** copy this plan verbatim to `docs/plans/2026-09-26-reset-days.md`.
- **Commit:** `git add -A && git commit -m "fix(availability): read a day component in a duration reset (#311)"`.

## 9. Stop rather than improvise

Halt and report, with the file, line and what you saw, if any of these is true:

- the quotes of §1.1(a) do not match the tree at those line numbers;
- the two rows of §4.2 do not behave as step 1 says (row 1 already passing means the fix is already
  on `main`; report instead of implementing);
- the `d`/`day` unit is already accepted by the duration path;
- a shared file (`internal/availability/limit_test.go`) carries the sibling round's edits in a way
  that makes the §4.2 insert impossible as written;
- `make check` fails for a reason outside the three declared files;
- the change cannot stay inside `internal/availability/limit.go` and
  `internal/availability/limit_test.go` without hurting a nearby contract -- the boundary is the
  point of this round, so stop and say why instead of widening it.

## 10. Deliberately left undone (for the report, not the code)

1. Reading `retryAfterMs` / `rateLimit.reset` from the stream: impossible today -- no captured event
   and no opencode event schema carries them; the upstream gap (opencode drops them when it maps its
   rate-limit error into the session error) and the place a future round would read them (§1.2) are
   named above.
2. Structured detection (`error.type` = `provider.rate-limit` / `provider.quota`, `status` = 429/402):
   the parallel round's scope; this plan does not add a second detection mechanism.
3. The 7-day window for monthly/declared limits: unchanged, not this round.
4. The out-of-credits line (`provider.quota`, no reset in its text) keeps the policy default, by
   design: a text match must not gate indefinitely.

---

## Addendum from the planner (part of this plan; save it verbatim with the rest)

Real opencode streams on the planner's machine contain `resets in 123456789012345d` (a test
fixture a builder printed) next to `resets in 1d 5h`. A component that large overflows
`time.Duration` when multiplied by `24*time.Hour` (int64 nanoseconds wrap past ~106751 days), and
the wrapped sum could land inside `(now, now+7d]` and be accepted. The same wrap already exists for
a huge `h`/`m`/`s` count.

- In `parseDurationReset`, refuse the whole reset (return `time.Time{}, false`) when any component's
  number exceeds what the window could ever accept -- for example reject `n` greater than
  `int(limitWindowDated / unit)` for its unit -- before multiplying, and when the running sum would
  pass `limitWindowDated`. Keep it a small check with a why-comment (the int64 wrap), not a new helper
  file.
- Add a third row to `parseResetCases`: `"The limit resets in 123456789012345d."` -> not parsed.
  Also one row with a huge hour count, e.g. `"resets in 99999999999999h"` -> not parsed.
- Mutation: remove the overflow check and confirm the 123456789012345d row fails (if it does not
  fail on its own because the wrapped value lands outside the window, say so in the report and keep
  the check anyway -- the wrap is value-dependent).
