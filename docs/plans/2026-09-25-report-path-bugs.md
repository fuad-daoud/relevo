# Report path bugs: multi-line flow lists in the tail (#438); log the push give-up once (#459)

## 1. System Overview

This plan fixes two small bugs on the report path. They land as one PR with two commits.

**#438:** a builder's trailing ```` ```relevo ```` block can write a list key as a
JSON-style flow list that spans several lines:

```
changed_paths: [
  "cmd/relevo/client.go",
  "internal/relevo/send.go"
]
```

`parseReportTail` (`internal/relevo/reporttail.go`) reads one line at a time.
With `changed_paths: [` it passes the value `[` to `parseListValue`. The next
line, `"cmd/relevo/client.go",`, has no colon, so the whole block is rejected
with `tail: line N has no ':'`. The round then reads as unstructured and
`relevo wait` exits 2, even though `status: done` is present. The fix: when a
list key's value opens a `[` that its own line does not close, join the
following block lines until the brackets balance, then parse the joined text
exactly as a one-line flow list is parsed today. A list that is still open at
the closing fence is rejected with a reason that names it.

**#459:** once a payload is older than `FallbackAfter` (30 s),
`OpencodeDeliverer.Deliver` and `AgyDeliverer.Deliver` both return
`OutcomeNotMine` with a "gave up" reason on every daemon tick, which is correct.
They also call `slog.Info("… push not confirmed; payload stays pending …")` on
every tick, which is the bug: one pending report produced 38 identical lines
in about 80 s. The fix: a small per-deliverer memo, shared by both
deliverers, that allows the give-up log line once per payload, with at most one
more line per hour while the payload stays pending. The returned outcome and
reason do not change. Only the log line is rate-limited.

## 2. File Structure

```
internal/relevo/reporttail.go            MODIFIED  parseReportTail: join a multi-line flow list; new helper flowListDepth
internal/relevo/reporttail_test.go       MODIFIED  new subtests under TestParseReportTail
internal/relevo/giveuplog.go             NEW       giveUpLog: once-per-key log gate with hourly re-log and pruning
internal/relevo/giveuplog_test.go        NEW       unit test of giveUpLog
internal/relevo/deliver_opencode.go      MODIFIED  OpencodeDeliverer gains field gaveUp giveUpLog; give-up branch logs through it
internal/relevo/deliver_agy.go           MODIFIED  AgyDeliverer gains field gaveUp giveUpLog; give-up branch logs through it
internal/relevo/deliver_opencode_test.go MODIFIED  one new test: the give-up line is logged once across ticks
internal/relevo/deliver_agy_test.go      MODIFIED  one new test: the same for agy
docs/plans/2026-09-25-report-path-bugs.md NEW      this plan, committed with the change
```

Nothing else changes. `cmd/relevo/main.go` needs no edit: `newDeliverers()`
(main.go ~546-575) builds each deliverer once per daemon, with named-field
literals, so a new unexported zero-value field is picked up automatically.

## 3. Data Structures & Type Definitions

### giveUpLog (new, `internal/relevo/giveuplog.go`, unexported)

| field | type | purpose |
|---|---|---|
| `mu` | `sync.Mutex` | guards `last`. Deliver runs on the daemon tick goroutine, but tests and future callers may not, so it is locked anyway |
| `last` | `map[string]time.Time` | key -> when the give-up line for that key was last logged. nil until the first use, so the zero value is ready to use |

Package constant: `giveUpRelogAfter = time.Hour`. Once a key's line was logged
this long ago, the line may be logged again, and an entry this old is pruned.

Key format, built by the caller (the deliverer), never parsed:
`<session or conversation id> + "\x00" + strconv.FormatInt(queuedAt.UnixNano(), 10) + "\x00" + firstPayloadLine(payload)`.
The key includes `queuedAt`, so a new payload to the same session gets its own
line. It includes the origin line, so two payloads queued in the same
nanosecond stay distinct.

### Changes to existing structs

- `OpencodeDeliverer` (`deliver_opencode.go`, struct at ~lines 35-46 on
  origin/main): add `gaveUp giveUpLog` after the `posted` map. The comment
  says it rate-limits the give-up log line (#459).
- `AgyDeliverer` (`deliver_agy.go` ~lines 47-60): add `gaveUp giveUpLog` as
  the last field, with the same comment.

`giveUpLog` holds a mutex, so both deliverers must only ever be used through a
pointer. They already are (`&relevo.OpencodeDeliverer{…}` and
`&relevo.AgyDeliverer{…}` in main.go, and `d := &…` in tests). `go vet`'s
copylocks check enforces it.

### ReportTail: unchanged

## 4. Interface Definitions & Component Contracts

### `func (g *giveUpLog) shouldLog(key string, now time.Time) bool`

- Responsibility: decide whether the give-up line for `key` is logged at `now`.
- Returns true when `key` has never been recorded, or when its last record is
  at least `giveUpRelogAfter` before `now`. In that case it records
  `last[key] = now` before returning. Otherwise it returns false and changes
  nothing for `key`.
- Pruning: whenever it returns true, it also deletes every *other* entry whose
  last record is at least `giveUpRelogAfter` before `now`. That keeps the map
  bounded by the number of payloads that gave up within the last hour.
- It never errors or panics on the zero value.

### `func flowListDepth(s string) int` (new, `reporttail.go`, unexported)

- Responsibility: the net bracket depth of `s`, i.e. the count of `[` minus the
  count of `]`, counting only characters outside `"…"` / `'…'` quotes. It uses
  the same quote rule as `splitListElements`: a quote opens with `"` or `'` and
  closes with the same character, and there is no escape handling.
- Pure. It may return a negative number, and the caller treats <= 0 as closed.

### `parseReportTail` (existing; behaviour change only for list keys)

For the keys `changed_paths`, `commands_run` and `not_done`, when `val` (after
`stripComment` and `TrimSpace`) starts with `[` and `flowListDepth(val) > 0`:

- Take block lines `i+1, i+2, …` (strictly before `closeIdx`). Apply
  `stripComment` + `TrimSpace` to each, as the loop already does to every line.
  Append each non-empty one to `val` with a single space separator, and add its
  depth, until the running depth is <= 0. Then set the loop index `i` to the
  last line consumed, so the outer loop resumes after it.
- If `closeIdx` is reached while the depth is still > 0, return
  `ReportTail{}, false, fmt.Sprintf("tail: line %d: %s list is not closed", openLine+1, key)`,
  where `openLine` is the index of the key's own line. This matches the
  existing `i+1` line-number convention.
- Then call `setList(&tail, key, parseListValue(joined))` as the one-line flow
  path already does. A trailing comma before `]` already yields an empty
  element, which `parseListValue` drops, so `["a",\n"b",\n]` parses to `[a b]`.

Everything else keeps its current behaviour: one-line flow lists, block lists
(`- item`), `status` / `halted_at`, unknown keys, and every existing reason
string.

Note on `stripComment`: it tracks `inBracket` only within one line. On a
continuation line such as `"a.go",`, a `#` outside quotes is treated as a
comment. That is the rule every other line already follows, and it is accepted.

## 5. High-Level Pseudocode

### parseReportTail list branch (#438)

```
case "changed_paths", "commands_run", "not_done":
    if key == "changed_paths": tail.ChangedPathsSet = true
    if val == "":                       -- unchanged: block list opens
        setList(key, nil); openList = key
    else if hasPrefix(val, "[") and flowListDepth(val) > 0:
        openLine := i
        depth := flowListDepth(val)
        joined := val
        for depth > 0:
            i++
            if i >= closeIdx: return reject("tail: line %d: %s list is not closed", openLine+1, key)
            cont := TrimSpace(stripComment(lines[i]))
            if cont == "": continue
            joined += " " + cont
            depth += flowListDepth(cont)
        setList(key, parseListValue(joined))
    else:                               -- unchanged: one-line flow or scalar
        setList(key, parseListValue(val))
```

`parseListValue` requires the joined string to end with `]`. If the builder
wrote text after the closing `]` on the same line (e.g. `] # done`),
`stripComment` has already removed the comment. Any other trailing text makes
`parseListValue` treat the whole string as one scalar item. That is the
behaviour a one-line list with trailing junk has today, and it is accepted.

### Deliverers' give-up branch (#459), same shape in both

```
if !queuedAt.IsZero() && now - queuedAt > fallbackAfter:
    reason := "<kind> push gave up after <fallbackAfter>"        -- unchanged
    key := sessionOrConv + "\x00" + unixNano(queuedAt) + "\x00" + firstPayloadLine(payload)
    if d.gaveUp.shouldLog(key, d.now()):
        slog.Info(<unchanged message>, <unchanged attrs>)
    return OutcomeNotMine, reason, nil                          -- unchanged, every tick
```

Use `d.now()`, the injected clock, never `time.Now()`, so tests control the
re-log window.

## 6. Error Handling Strategy

- No new error types. The tail rejects an unclosed multi-line list with a
  reason string (the `ok == false` path that already exists), which `relevo
  status` shows as `(tail: …)`. It is not recoverable within the round, and
  the planner sees it as it sees every other tail rejection.
- `giveUpLog` cannot fail. A suppressed log line loses nothing:
  `Delivery.Reason` still carries the reason on every tick.

## 7. Working Efficiently

Each model step costs a round trip, so:

- Batch the reads into one step, as parallel reads:
  - `internal/relevo/reporttail.go` (whole file, ~300 lines);
  - `internal/relevo/reporttail_test.go` lines 1-40 and 290-433;
  - `internal/relevo/deliver_opencode.go` lines 1-140;
  - `internal/relevo/deliver_agy.go` lines 40-150;
  - `internal/relevo/deliver_opencode_test.go` lines 285-325;
  - `internal/relevo/deliver_agy_test.go` lines 200-240, plus a grep for `func newAgyRig`;
  - `internal/relevo/daemon_test.go` lines 215-230, which show how an
    existing test captures `slog` with `slog.SetDefault` and restores it.
- Make each file's changes in one edit call.
- Focused tests:
  - `go test ./internal/relevo/ -run 'TestParseReportTail|TestGiveUpLog|TestOpencodeDeliver|TestAgyDeliver' -count=1`
- Full check, once, at the end: `make check`. If this machine blocks heavy
  commands, use `dev run make check`. If `dev run` fails because `dist/` or
  `.git` is missing on the mirror, say so in the report and rely on the PR's
  CI.
- CI has no harness binary and no network. Every test here is a pure-function
  or fake-backed test in `internal/relevo`, with nothing in `cmd/relevo`.
- Tests that call `slog.SetDefault` must not call `t.Parallel()`, and must
  restore the previous default with `defer`, as `daemon_test.go:223-224` does.
  Build the handler with `&slog.HandlerOptions{Level: slog.LevelInfo}` so the
  Info line is captured.

If any step is impossible as written or contradicts the code, stop and report.
Do not bend the plan, or a test, to fit.

## 8. Ordered Implementation Steps

**Step 0: sync and confirm the premises.** Run
`git fetch origin && git merge --ff-only origin/main`. Then confirm all of the
following, and halt if any is false:
- `parseReportTail` in `reporttail.go` has the `case "changed_paths", "commands_run", "not_done":`
  branch with `setList(&tail, key, parseListValue(val))` in its else-arm;
- `deliver_opencode.go` contains `opencode push gave up after` followed by a
  `slog.Info` in the same `if` block;
- `deliver_agy.go` contains `agy push gave up after` followed by a `slog.Info`
  in the same `if` block;
- `firstPayloadLine` exists in package `relevo`.

**Step 1: the tail tests first (#438), and watch them fail.** Add these
subtests to `TestParseReportTail` in `reporttail_test.go`, after the last
existing subtest:
1. `"flow list over several lines"`: the example from section 1, with
   `status: done`, `halted_at: ""`, and `changed_paths` over four lines, and
   `commands_run: ["make check"]` on one line after it. Expect ok, status
   `done`, `ChangedPathsSet` true, `ChangedPaths` = the two paths in order, and
   `CommandsRun` = `["make check"]`. The line after the list must be parsed,
   which proves the loop index resumed correctly.
2. `"flow list over several lines with a trailing comma and the first item on the key line"`:
   `not_done: ["a",` / `"b",` / `]`. Expect `NotDone` = `[a b]`.
3. `"flow list whose items contain brackets inside quotes"`:
   `changed_paths: [` / `"docs/[draft].md",` / `"x.go"` / `]`. Expect both
   items. This pins that quoted brackets do not count toward the depth.
4. `"unclosed flow list"`: `status: done` / `changed_paths: [` / `"a.go",` then
   the closing fence. Expect ok == false and reason exactly
   `tail: line 3: changed_paths list is not closed`. Start the input with the
   ```` ```relevo ```` line itself, with no prose before it, exactly as the
   existing `"rejected block reports why"` subtest does. Line numbers are
   counted across the whole report, so the key is then on line 3.

Run the focused tests. Verification: subtests 1-4 fail and every other test
passes. Record the failure lines for the report.

**Step 2: implement #438.** Add `flowListDepth` and change the list branch of
`parseReportTail` per sections 4 and 5. Verification: the focused tests pass.

**Step 3: commit #438.** Commit the two files as
`fix(reporttail): a flow list may span several lines (#438)`, with
`Fixes #438` in the body.

**Step 4: giveUpLog with its unit test (#459).** Create `giveuplog.go`
(section 3, section 4) and `giveuplog_test.go` with `TestGiveUpLog`, which
asserts on one zero-value `giveUpLog` with a fixed base time `t0`:
- `shouldLog("a", t0)` is true, then `shouldLog("a", t0+1s)` is false and
  `shouldLog("a", t0+59m)` is false;
- `shouldLog("b", t0+1s)` is true, because a different key is independent;
- `shouldLog("a", t0+60m)` is true, the hourly re-log at exactly
  `giveUpRelogAfter`;
- pruning: after `shouldLog("c", t0+3h)` returns true, `len(g.last)` is 1
  (only `c`). `a` and `b` were last logged at least an hour before and are
  gone. The test is in package `relevo`, so it may read `g.last`.

Verification: `go test ./internal/relevo/ -run TestGiveUpLog -count=1` passes.

**Step 5: the deliverer tests (#459), and watch them fail.**
- In `deliver_opencode_test.go`, add
  `TestOpencodeDeliverLogsGiveUpOncePerPayload`. Build the deliverer exactly as
  `TestOpencodeDeliverFallsBackAfterFallbackAfter` does, but with
  `Now: func() time.Time { return now }` over a variable `now` the test
  advances. Capture slog at Info into a buffer. Then:
  1. call Deliver three times with the same payload and `queuedAt`, with
     `now` = queuedAt+31s, +32s and +33s;
  2. assert that every call returned `OutcomeNotMine` with the reason
     containing `opencode push gave up after`;
  3. assert that the buffer contains `push not confirmed` exactly once;
  4. call once more with a *different* `queuedAt` (queuedAt+1s) and a
     `now` past its fallback, and assert the count is 2;
  5. set `now` one hour past the first call and call with the first payload
     again, and assert the count is 3.
- In `deliver_agy_test.go`, add `TestAgyDeliverLogsGiveUpOncePerPayload`,
  built on `newAgyRig` like `TestAgyDeliverGaveUpAfterFallback`, covering
  sub-steps 1-3 only, with `FallbackAfter = time.Second` and a reason exactly
  `agy push gave up after 1s` on every call.

Run the focused tests. Verification: both new tests fail on the count (3, not
1), and everything else passes. Record the failure lines.

**Step 6: implement #459.** Add the `gaveUp giveUpLog` field to both structs,
and wrap each give-up `slog.Info` in `if d.gaveUp.shouldLog(key, d.now())`,
with the key from section 3. The message text, the attrs, the returned outcome
and the returned reason all stay exactly as they are. Verification: the
focused tests pass.

**Step 7: full check.** `make check` (section 7). It must pass.

**Step 8: ship.** Copy this plan to `docs/plans/2026-09-25-report-path-bugs.md`.
Commit the #459 files plus the plan as
`fix(deliver): log a push give-up once per payload, not on every tick (#459)`,
with `Fixes #459` in the body. Push the branch and open one PR against `main`
titled
`fix: multi-line flow lists in the report tail (#438); log a push give-up once (#459)`,
with `Fixes #438` and `Fixes #459` in the body. Don't wait for CI and don't
merge.

The report states the step 1 and step 5 failure lines, the focused and full
check results, the PR number, and `git diff --stat origin/main`.

## Round 2: quoted-bracket subtest

Round 1 (commits 927a9f7 and d5a8a54 on `relevo/rpbugs`, PR #491) is
verified, except for one test. The planner mutation-tested `flowListDepth` in
`internal/relevo/reporttail.go` by deleting its quote handling, so that every
`[` and `]` counts, even inside quotes. `TestParseReportTail` still passed.
The subtest `"flow list whose items contain brackets inside quotes"` uses
`"docs/[draft].md"`, whose brackets balance, so the depth is the same with or
without quote handling. The subtest pins nothing.

This round changes only that subtest's input and expectation, so that an
unbalanced bracket inside quotes decides the outcome. No production code
changes.

Step 2 result: with the quote handling deleted from `flowListDepth`, the
focused test fails at the mutated code:

```
--- FAIL: TestParseReportTail (0.00s)
    --- FAIL: TestParseReportTail/flow_list_whose_items_contain_brackets_inside_quotes (0.00s)
        reporttail_test.go:472: expected ok=true, got false
FAIL
```

The subtest therefore pins quote handling. `reporttail.go` was restored with
`git checkout -- internal/relevo/reporttail.go`.
