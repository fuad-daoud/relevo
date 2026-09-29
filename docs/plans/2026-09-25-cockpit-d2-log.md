# Cockpit D2, `:log` round 3: a persistent event log

Date: 2026-09-25. Worktree: `ck-d2-rounds`, branch `relevo/ck-d2-rounds`. Rounds 1-2 (`:rounds` in D2) are
the branch's one commit on base `9471682`. Build on it, and AMEND that commit at the end. Do not rebase, push or
open a PR.

**Stop rather than improvise.** If the code differs from what this plan quotes (a function missing, a
signature different, a line range holding something else), or a step is impossible as written, halt and
report what you found. Line numbers are from `9471682`; rounds 1-2 did not touch `shell.go`, `view.go`,
`actions.go`, `internal/db`, `internal/relevo` or `view_stats.go:2245`.

**Scope fence.** Change only:
- `internal/db/read.go`, `internal/db/types.go`, `internal/db/read_test.go`
- `internal/ui/view_log.go` (new), `internal/ui/view_log_test.go` (new)
- `internal/ui/shell.go`, `internal/ui/view.go`, `internal/ui/actions_test.go`
- `internal/ui/dash/render.go` (only to export `DayLabel`, §4.3)
- `internal/ui/testdata/log-132.golden` (new), `internal/ui/golden_test.go` (one new case)
- `docs/plans/`

No test in this round runs a CLI subcommand.

## 1. System overview

`:log` today is `logView` (`internal/ui/shell.go:399-460`): this session's action results as plain strings,
newest last. It is empty until you act in the cockpit. This round makes it the persistent event log the user
approved (canvas board "`:log` D2 · event log"): newest first, under day rules, merging four sources.

1. **Handoff events** from `relevo.db` (`event` table) for every binding over the last 7 days:
   - plans, picks and drifts fold into one `sent` row;
   - reports and their diffs fold into one `done` / `halted` / `blocked` / `reported` row;
   - `exit`, `switch`, `question` and `answer` each get their own row.
2. **Gates** from the availability history (`relevo.LoadHistory`, `internal/relevo/statsinputs.go:91`).
3. **Config saves** (`db.Revisions`, `internal/db/revision.go:71`).
4. **This session's cockpit actions:** the user asked for "a log for each action you take in the cockpit".
   They are in-memory for the session, shown with BINDING `you`.

```
  ◆ relevo    log                                                         v0.13.1-…    19:18
                                                                                                    (blank)
   84 events today   35 sent   30 done   6 halted   5 exited   6 switched   2 gated                       following
                                                                                                    (blank)
   TIME   BINDING         RND   EVENT      DETAIL
   today ┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈ 84 events
   19:14  oc-live         r1    sent       on gemini-3.8-flash-high (#1) · yolo
   19:09  haiku           r2    done       1 file +1 -0 · 1 commit · clean · 154k tokens · <1m
   19:04  question        r1    halted     at step 2 · 1 file +1 -0 · 0 commits · dirty · 138k tokens · 1m
   18:33  ck-d2-stats     r17   sent       yolo · edited since r16: 36 files +3187 -45
   18:26  oc-496          r2    switched   to glm-5.3-flash (#6) · exited without a report
   18:26  oc-496          r2    exited     without a report (code 0)
   18:11  —                     gated      cline-pass · weekly Clinepass limit reached, resets in 1d 4h
   17:40  you                   gate       gated google until 22:00
```

The same D2 rules as `:rounds` apply:
- a 3-cell margin on each side;
- a header row, with no captions;
- the cursor row drawn in the band;
- day rules `label ┈┈┈ N events`, where the label is `today`, `yesterday` or `sat 19 sep`.

## 2. File structure

```
internal/db/types.go      + EventLogRow
internal/db/read.go       + (*DB).RecentEvents
internal/db/read_test.go  + TestRecentEvents
internal/ui/view.go       Env gains ActionLog
internal/ui/shell.go      actionLog becomes []actionEntry; logMsg opens the new view; old logView deleted
internal/ui/view_log.go   NEW: logEntry, buildLogEntries (pure), eventLogMsg, fetchEventLog, logView
internal/ui/view_log_test.go  NEW: the fold rules and the view's keys
internal/ui/actions_test.go   two assertions ported (§8)
internal/ui/dash/render.go    dayLabel exported as DayLabel
internal/ui/golden_test.go + testdata/log-132.golden   one golden
docs/plans/2026-09-25-cockpit-d2-log.md
```

## 3. Data structures & type definitions

### 3.1 `db.EventLogRow` (internal/db/types.go, after `Event`, line 102)

| field | type | meaning |
|---|---|---|
| `TS` | `time.Time` | `event.ts` |
| `Seq` | `int` | `event.seq` (tie-break) |
| `Kind` | `string` | `event.kind` |
| `Note` | `*string` | `event.note` |
| `EntryJSON` | `string` | `event.entry_json` |
| `BindingName` | `string` | `binding.name` |
| `RoundID` | `*string` | `event.round_id` |
| `Round` | `*int` | `round.number`, nil when the event has no round |
| `Tokens` | `*int64` | the sum of the round's four token columns; nil when all four are null or there is no round |
| `DurationMS` | `*int64` | the round's `closed_at - started_at` in ms; nil when it is open or there is no round |

### 3.2 `actionEntry` (internal/ui/shell.go, replaces `actionLog []string` at line 54)

```
type actionEntry struct {
    At   time.Time // when the result arrived (m.now())
    Verb string    // actionMsg.verb
    Key  string    // actionMsg.key (the binding key acted on; may be "")
    Text string    // the result text, or the error text
    Err  bool
}
```

### 3.3 `logEntry` (internal/ui/view_log.go)

```
type logEntry struct {
    At      time.Time
    Binding string         // "" draws as "—"; "you" for a cockpit action
    Round   int            // 0 = none
    Word    string         // the EVENT cell
    Style   lipgloss.Style // the EVENT cell's style
    Detail  string         // one line; newlines replaced by " · "
    Err     bool           // a failed action: Detail in redStyle
}
```

### 3.4 `logView` (internal/ui/view_log.go)

```
type logView struct {
    events   []db.EventLogRow
    hist     history.History
    revs     []db.RevisionRow
    loaded   bool
    err      error
    fetching bool
    lastFetch time.Time
    cursor   int        // index into the rendered entry list (day rules excluded; see §5.3)
    top      int        // first body line drawn
    follow   bool       // true: the cursor pins to the newest entry on every refresh
    filter   string     // applied filter ("" = none)
    input    textinput.Model
    editing  bool
}
```

## 4. Interface definitions & component contracts

### 4.1 `func (d *DB) RecentEvents(since time.Time, limit int) ([]EventLogRow, error)`

- Reads every event with `ts >= since`, across all bindings, ordered `ts DESC, seq DESC`.
- `limit <= 0` means no limit.
- One query: `event` JOIN `binding` ON `binding_id`, LEFT JOIN `round` ON `round_id`. Tokens and duration come
  from the round row, the way `RoundRow`'s are read elsewhere in read.go; follow that file's time and
  nullable-scan helpers (`parseTime`, `sql.Null*`).
- Errors wrap as `fmt.Errorf("db: recent events: %w", err)`, through `mapBusy` like `Revisions` does.
- A database with no `event` table returns `nil, nil` (use `isMissingTable` as `Revisions` does).

### 4.2 `func buildLogEntries(events []db.EventLogRow, hist history.History, revs []db.RevisionRow, actions []actionEntry, since time.Time, name func(token string) string) []logEntry`

A pure function (§5.1). It returns the entries newest first; entries with an equal time keep the source order
events, gates, revisions, actions. `name` maps a candidate token to its display name (callers pass
`env.Src.Base().Candidates.NameOf`); a nil `name` is the identity.

### 4.3 dash

Rename the `dayLabel` rounds 1-2 wrote to exported `DayLabel(t, now time.Time, loc *time.Location) string`. If round 1
(or round 2) gave it a different signature (a method on Model), add a package-level `DayLabel` with this signature that the
method calls. The rule is unchanged: `today`, `yesterday`, or the date formatted `"Mon 02 Jan"` and
lower-cased.

### 4.4 The view

```
func newLogView(env Env) (View, tea.Cmd)       // follow = true; returns fetchEventLog
func fetchEventLog(ctx context.Context, rt relevo.Runtime, now time.Time) tea.Cmd
type eventLogMsg struct { events []db.EventLogRow; hist history.History; revs []db.RevisionRow; err error; at time.Time }
```

`fetchEventLog` is asynchronous and reads:
- `rt.DB.RecentEvents(now.Add(-7*24*time.Hour), 3000)`;
- `relevo.LoadHistory(rt)`;
- `rt.DB.Revisions(0)`.

A nil `rt.DB` gives `err = relevo.ErrNoDatabase`. A history error is not fatal: that source is left empty and
the other two are still used.

`logView` implements `View`:
- `Crumbs() = {"log"}`.
- `Capturing() = editing`.
- `Context`, `Keys`, `Update`, `Body` are as in §5.

## 5. High-level pseudocode

### 5.1 buildLogEntries: the fold rules

```
key(e) = (e.BindingName, deref(e.RoundID))
index picks, drifts, diffs by key (first seen wins; the input is newest first)

for each event e:
  switch e.Kind:
  "plan":   parts = []
            pick := picks[key]: note matches `picked (\S+) for builder: order #(\d+)` -> "on <name(tok)> (#N)"
            tier := entry_json "tier" (string) -> append if non-empty
            drift := drifts[key]: "edited since r<Round-1>: " + note with "," removed      (only when Round > 1)
            entry(word "sent", accentStyle, strings.Join(parts, " · "))
  "report": outcome := entry_json "outcome":
              done -> "done" greenStyle | halted -> "halted" warnStyle | blocked -> "blocked" warnStyle
              anything else -> "reported" mutedStyle
            parts: entry_json "halted_at" -> "at <x>" (when outcome is halted and it is set)
                   diff := diffs[key] note, cut at " paths:"; split at the first "; " into files and rest;
                     files with "," removed; rest split on ", ", each trimmed, "no commits" -> "0 commits"
                   Tokens != nil -> stats.ShortTokens(*Tokens) + " tokens"
                   DurationMS != nil -> shortDurationText(*DurationMS)  ("<1m", "14m", "1h05m")
  "exit":   "exited" redStyle, "without a report (code N)" where N from note `code (\d+)`; no match -> "without a report"
  "switch": note matches `switched builder \((.*?)\): picked (\S+) for builder: order #(\d+)`:
              why := group 1; if it starts "rate-limited: " -> statsGateReason(rest)
                    any `exited (code \d+) without a report` -> "exited without a report"
              "switched" warnStyle, "to <name(tok)> (#N) · <why>"
            no match -> "switched" warnStyle, the note clipped to 80 cells
  "question": "needs you" warnStyle, "blocked at a dialog"
  "answer":   "answered" mutedStyle, deref(note)
  "pick", "drift", "diff": no entry of their own (folded above)
  default:    word = e.Kind, mutedStyle, deref(note)

for each hist event h with h.At >= since:
  rate_limited | spawn_failed -> Binding h.Binding, "gated" warnStyle, "<provider> · " + statsGateReason(h.Note)
  cleared                     -> Binding h.Binding, "cleared" greenStyle, provider
  other kinds                 -> string(kind) mutedStyle, provider
for each revision rv with rv.At >= since: Binding "", "config" mutedStyle, "rev <n> · <message>"
for each action a: Binding "you", Word a.Verb (textStyle), Detail = a.Text with newlines -> " · ", Err = a.Err

sort newest first, stable
```

`statsGateReason` is `internal/ui/view_stats.go:2245`. Reuse it, and do not copy it.

### 5.2 Context and keys

```
Context(env):
  err != nil && !loaded      -> left "   " + errorStyle(err.Error())
  !loaded                    -> left "   " + mutedStyle("loading…")
  else left = "   " + items over TODAY's entries (DayLabel == "today"):
        "<n> events today", then per word in [sent, done, halted, exited, switched, gated] when > 0: "<n> <word>"
        number textStyle.Bold, label mutedStyle, joined by 3 spaces
  right = (filter != "" ? mutedStyle("/ " + clip(filter, 40)) + "   " : "")
          + (follow ? accentStyle("following") : faintStyle("paused"))
          + "   "
Keys(): ↑↓ move · enter open round · / filter · f follow
```

### 5.3 Body (exactly height lines, none wider than width; a 3-cell margin each side)

```
line 0: editing ? "   / " + input : header row TIME(5) BINDING(14) RND(4) EVENT(9) DETAIL(rest), faint bold, 2-cell gaps
entries := buildLogEntries(..., env.ActionLog, ...) filtered by filter
           (case-insensitive substring of Binding, Word or Detail)
lines   := for each run of entries with the same DayLabel:
             day rule: mutedStyle(label) + " " + gridStyle("┈"*fill) + faintStyle(" " + plural(n, "event")),
                       exactly cw = width-6 wide
             then one line per entry
cells: TIME At.Local() "15:04" muted; BINDING ("—" faint when "", "you" accent, else text; bold on the
       cursor row); RND "r<n>" muted or ""; EVENT in its Style; DETAIL muted (redStyle when Err), clipped
cursor: over entries only (a day rule is never the cursor); the cursor row's content is wrapped in selBandStyle
window: body lines after line 0 scroll; top keeps the cursor's line visible (the page follows the cursor)
empty (loaded, no entries): "   no events in the last 7 days" in emptyStyle
```

### 5.4 Update

```
tickMsg: if !fetching && env.Now.Sub(lastFetch) >= 5s -> fetching = true; return fetchEventLog(...)
eventLogMsg: fetching = false; lastFetch = msg.at
             msg.err != nil -> err = msg.err (keep old rows)
             else store the three sources; loaded = true; err = nil
             follow -> cursor = 0, top = 0; else clamp the cursor
keys (not editing):
  up/k, down/j: move 1 (down or up sets follow = false, except up when the cursor is already 0)
  pgup/pgdown/space: move one page; home: cursor 0 and follow = true; end: last; f: follow = true, cursor 0, top 0
  enter: the cursor entry has Binding != "" && Binding != "you" && Round > 0
         -> return openRound(env, dash.JumpMsg{BindingName: Binding, Round: Round})
         (view_rounds.go:136; roundOpenMsg then pushes, handled here exactly as roundsView.Update does at
          view_rounds.go:98-104); other entries: no-op
  "/": editing = true, input set to filter, focus
keys (editing): enter applies (filter = trimmed value, cursor 0), esc cancels, else input.Update
```

### 5.5 Shell (internal/ui/shell.go)

- `actionLog []actionEntry` (line 54).
- `finishAction` (lines 263-279) appends `actionEntry{At: m.now(), Verb: msg.verb, Key: msg.key, Text: …, Err: …}`.
  The text is the error when `Err`, else `msg.res.Text`.
- `stderrMsg` (lines 212-217) STOPS appending to the log. It still sets the notice exactly as today (§8
  item 2).
- `logMsg` (lines 219-224): `v, cmd := newLogView(m.env())`, then `m.stack = []View{fleet…, v}` as today,
  and `return m, cmd`.
- `env()` (lines 120-134) sets `ActionLog: m.actionLog`.
- `Env` (view.go:30-48) gains `ActionLog []actionEntry // the session's cockpit actions, oldest first (:log)`.

## 6. Error handling strategy

- A fetch error before the first load shows on the context row (Error style), and the body stays empty.
- A later fetch error keeps the rows on screen, and the next tick retries.
- `LoadHistory` failing only empties the gates source.
- `RecentEvents` on a database with no `event` table returns nothing.
- The pure fold never fails. An unparseable note falls back to the note text, and bad `entry_json` counts as
  `{}`.
- No panics on nil pointers: every `*string`/`*int` has a fallback.

## 7. Working efficiently

A round costs one round trip per step, so:
- Batch independent reads, searches and edits as parallel tool calls in one step.
- Read the files once, at the line ranges named here.
- Write `view_log.go` and `view_log_test.go` whole, each in one Write, and make each other file's changes in
  one edit.
- Iterate with the focused command, fixing every reported error before the next run:
  `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/db/ ./internal/ui/ ./internal/ui/dash/ -count=1`
- Create the new golden with `-run TestGoldenViews -update`, then read it against §1.
- Run the full check once at the end (step 7).
- Do NOT run `make check`: it is refused on this machine.

## 8. Deletions (closed list; everything else survives)

1. `logView` and `newLogView` in `internal/ui/shell.go:399-460` (the old session-only view). The new
   `newLogView` has a different signature and lives in `view_log.go`.
2. Appending stderr lines to the action log (`shell.go:216`). The notice they set survives.
3. `actionLog []string`, replaced by `[]actionEntry`.

`actions_test.go:385-386` and `401-402` assert on `m.actionLog` strings. PORT them to assert on the entries'
`Text`, `Err` and `Verb`, and do not delete them. A test asserting that stderr lines reach the log, if one
exists, is ported to assert that they set the notice, citing item 2.

## 9. Ordered implementation steps

1. **db.** Add `EventLogRow` and `RecentEvents` (§3.1, §4.1).

   Add `TestRecentEvents` in `read_test.go`, in the style of `TestEventsRoundZeroIsAll` (line 562):
   - two bindings;
   - a reported round with tokens, and an open round;
   - four events at distinct times, one of them before `since`.

   Assert:
   - only the three since `since` come back, newest first;
   - the binding names are correct;
   - `Round` is set;
   - `Tokens` is the sum;
   - `DurationMS` is nil for the open round;
   - `limit` caps the result.

   *Verify:* `go test ./internal/db/ -run TestRecentEvents -count=1`.
2. **Export `DayLabel`** (§4.3).
3. **Shell plumbing** (§3.2, §5.5): `actionEntry`, `Env.ActionLog`, `finishAction`, stderr, `logMsg`. Delete
   §8 items 1-3. Port the `actions_test.go` assertions.
4. **`view_log.go`** (§3.3, §3.4, §4.2, §4.4, §5.1-§5.4).
5. **Tests** in `view_log_test.go`. Each is a pure `buildLogEntries` test with literal rows, unless it says
   otherwise:
   - `TestLogFoldsSend`: a plan, pick and drift on one binding and round give ONE entry:
     `sent` / `on sonnet (#5) · yolo · edited since r16: 36 files +3187 -45`. The `name` func maps
     `claude/anthropic/sonnet` to `sonnet`.
   - `TestLogFoldsReport`:
     - a halted report plus its diff `1 file, +1 -0; no commits, dirty`, on a round with 138240 tokens and
       66000 ms, gives `halted` / `at step 2 · 1 file +1 -0 · 0 commits · dirty · 138.2k tokens · 1m`;
     - a done report whose diff note has a ` paths:` tail drops that tail.

     Take the token text from `stats.ShortTokens` whatever it prints, and assert against that call's output.
   - `TestLogSwitchAndExit`: the two real notes from §1 (code 0) give
     `to glm-5.3-flash (#6) · exited without a report` and `without a report (code 0)`. A rate-limited switch
     reason is shortened by `statsGateReason`.
   - `TestLogGatesRevisionsActions`:
     - a history `rate_limited` event with an `Error 429: weekly Clinepass limit reached, resets in 1d 4h`
       note gives `gated` and a detail starting `cline-pass · `;
     - an event older than `since` is dropped;
     - a revision gives `config` / `rev 4 · config set candidates`;
     - an action gives Binding `you`, Word = its verb, and `Err` carried through.
   - `TestLogNewestFirst`: mixed sources come out in time order, newest first.

   View tests, through a `logView` fed an `eventLogMsg`:
   - `TestLogViewDayRulesAndMargins`, at 132×30:
     - the body has a `today` rule ending with `N events`, 3 cells before the edge;
     - every line is at most 132 wide and starts with 3 spaces;
     - the cursor is never on a rule line.
   - `TestLogViewFollow`:
     - `down` sets follow false;
     - a new `eventLogMsg` with a newer event keeps the cursor on the same entry index. Say so in the test
       name or comment if it only keeps the index; that is acceptable.
     - `f` sets follow true and the cursor to 0.
   - `TestLogViewEnterOpensRound`: `enter` on a `sent` entry returns a non-nil command, and `enter` on a gate
     entry returns nil.
   - `TestLogViewFilter`: `/`, type `oc-496`, `enter` leaves only entries whose binding is `oc-496`.

   Golden: add `log-132` to `internal/ui/golden_test.go`'s `TestGoldenViews` (132×34), built like the rounds
   golden: the shell with `Start: "log"`, then an `eventLogMsg` with ~8 literal events across two days, one
   gate, one revision and one action. Use no real names containing the old project name. *Verify:* the focused
   command is green.
6. **Mutation checks** (report both results, and check each mutated build compiles):
   1. Make the `"pick"` case emit its own entry. `TestLogFoldsSend` must fail.
   2. Remove the `" paths:"` cut. `TestLogFoldsReport` must fail.

   Restore both afterwards.
7. **Full check**, once:
   `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/db/ ./internal/ui/... ./internal/stats/ ./internal/histq/ ./cmd/relevo/ -count=1 && go vet ./internal/db/ ./internal/ui/... && test -z "$(gofmt -l $(git ls-files '*.go'))" && sh scripts/check-name.sh`
8. **Amend.** Copy `/home/fuad/projects/relevo/docs/plans/2026-09-25-cockpit-d2-log.md` into `docs/plans/`. Then
   run `git add -A && git commit --amend -m "feat(cockpit): :rounds and :log in D2 -- day rules, outcome words, tokens-first groups, a persistent event log"`.

## 10. Report

Include:
- each step's result;
- the new golden in full, ANSI stripped;
- the mutation results, with the build line of each mutated build;
- any test removed or ported, citing §8;
- `git diff --stat 9471682 HEAD`.
