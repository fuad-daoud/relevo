# Cockpit D2 round detail, round 3: a pushed view's first fetch must not race its push

Date: 2026-09-25. Worktree: ck-d2-round, which holds rounds 1-2 **uncommitted** (card + tabs, markdown,
the shared card). Build on it; do not reset or commit. One round.

**Stop rather than improvise.** If anything differs from what is quoted, halt and report.

## 1. The bug (reproduced, root cause found)

Opening a round often shows `loading…` forever, on the installed binary too. The planner instrumented
a build and captured this sequence:

```
fetchPlan done ref-cleanup r1
shell tabMsg ref-cleanup t0 stack=1     <- the reply arrives while only :fleet is on the stack; dropped
shell pushMsg stack=1                   <- the round view is pushed afterwards
```

`push` (internal/ui/view.go:67-68) is
`tea.Batch(func() tea.Msg { return pushMsg{v} }, init)`. Batch runs both commands concurrently, so a fast
first fetch (a local plan file) can deliver its `tabMsg` before `pushMsg`. `updateStack` hands the
reply to the views on the stack, and the round view is not there yet, so it is lost. The pushed view
keeps `tabInFlight == true`, so neither the tick nor invalidate ever fetches again. It stays on
`loading…` for good.

The same pattern exists for `root`: `tea.Batch(root(...), init)` at view_rounds.go:185, 206 and 221.

## 2. Fix: the shell runs a view's init after the stack change

- view.go:
  - `type pushMsg struct{ v View; init tea.Cmd }`.
  - `type rootMsg struct{ vs []View; init tea.Cmd }`.
  - `push(v, init)` becomes `func() tea.Msg { return pushMsg{v, init} }`: **one** message, no Batch.
  - Add `func rootThen(init tea.Cmd, vs ...View) tea.Cmd { return func() tea.Msg { return rootMsg{vs, init} } }`.
    `root(vs ...View)` returns `rootThen(nil, vs...)`.
- shell.go:
  - the `case pushMsg:` arm (lines 176-178) appends, then returns `m, msg.init`;
  - the `case rootMsg:` arm replaces the stack as today, then returns `m, msg.init` (together with whatever it already
    returns; read the arm, and if it already returns a cmd, `tea.Batch` the two).
- view_rounds.go:
  - the three `tea.Batch(root(...), init/cmd)` sites (lines 185, 206, 221) become `rootThen(init/cmd, ...)` with the same views
    in the same order;
  - line 173 (`root(newFleetView(...))`, no init) is unchanged.
- The other `push(v, cmd)` callers (view_fleet.go:447, view_rounds.go:104 and 107, view_stats.go:271) need no change.
  They get the fix through `push`.
- Grep for any other `pushMsg{` or `rootMsg{` literal, including in tests, and update it to the new struct shape.

## 3. Also fix: card rows overflow the border

In a real capture, the round card's tokens row (`tokens live · gemini-3.8-flash-high · 8m · in 1.2M ·
… · unknown: no price for google/gemini-3.8-fl`) ran past the right border and hid it. `renderCard`
(card.go) must cut every row to `cardW-2` cells (ANSI-aware: `lipgloss.NewStyle().MaxWidth(n).Render`,
or the `clipName` helper, which adds `…`) **before** padding. The row is then always exactly `cardW-2` wide,
and the right `│` always shows. This change must leave the fleet goldens byte-identical: the fleet rows fit.

## 4. Tests

- `TestPushDeliversInitAfterPush`:
  - Run `push(v, init)`, where init returns a sentinel message. The result is a `pushMsg`, not a `tea.BatchMsg`.
  - Feed it to `Model.Update`. The stack grows by one, and the returned cmd yields the sentinel.
- `TestRootThenDeliversInitAfterRoot`: the same for `rootThen`.
- `TestOpenRoundReplyNeverBeatsPush`:
  - From `:fleet` with a working row and a plan file on a temp store, press enter. Execute the returned cmd once.
  - The message must be a `pushMsg`, with no `tabMsg` among the messages that cmd produces.
  - Feed it to Update, run the returned init, and feed its tabMsg to Update. The plan tab is loaded and `tabInFlight` is false.
- `TestCardRowNeverOverflows`: `renderCard(60, …)` with a 200-cell row. Every output line has `lipgloss.Width` of at most 60, and the second
  line ends with `│` after stripping ANSI.
- The existing `drain` helper already unpacks BatchMsg, so current tests should keep passing. If a test built a `pushMsg{v}` or
  `rootMsg{vs}` literal, update the literal only.
- Goldens: regenerate. None should change except a round golden whose card row was too long. Name any diff in the report.

## 5. Commands

- Loop: `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -count=1`.
- Final: `gofmt -l internal/ui/*.go`, `go vet ./internal/ui/`, and
  `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ ./cmd/relevo/ -count=1`. Not `make check`.

## 6. Report

List:
- the files changed;
- every call site changed;
- the golden diffs;
- the check results.
