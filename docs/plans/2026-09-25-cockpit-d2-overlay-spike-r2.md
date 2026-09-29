# Cockpit D2 overlay spike, round 2: one set of buttons, no empty card part

Date: 2026-09-25. Worktree: the ck-d2-overlay worktree, which holds round 1's **uncommitted**
changes (modal confirm: compose.go, the `modalOverlay` wiring, confirmBox `kind`/`yes`/`danger`).
Build on them; do not reset or commit. One small round.

**Stop rather than improvise.** If the code differs from what is quoted, halt and report.

## 1. What is wrong

1. Every confirm modal shows its buttons twice. The new button row (`y stop the round   n cancel
   any other key cancels`) comes from `confirmBox.modal`, but each confirm's `lines` still end with the
   old text line `y <verb> · n cancel`.
2. The fleet card prints an empty part (`·    ·`) when a binding has neither `Branch` nor `CWD`.

## 2. Changes (internal/ui, line numbers in the worktree now)

**confirm.go: delete the six legacy button lines** (closed list; nothing else in these functions
changes):

| line | text deleted |
|---|---|
| 217 | `lines = append(lines, "y stop · n cancel")` |
| 227 | `lines = append(lines, "y done · n cancel")` |
| 237 | `lines = append(lines, "y unbind · n cancel")` |
| 428 | `lines = append(lines, "y send · n cancel")` |
| 521 | `lines: []string{"y bind · n cancel"},` (the bind confirm then has no `lines` field) |
| 588 | `lines = append(lines, "y retry · n cancel")` |

**confirm.go: give each confirmBox literal its `yes` label** (stop already has
`kind: "stop"`, `yes: "stop the round"`, `danger: true`):

| constructor (confirmBox literal) | kind | yes | danger |
|---|---|---|---|
| doneCmd (line 270) | `done` | `mark done` | false |
| unbindCmd (line 282) | `unbind` | `unbind and archive` | true |
| send from prompt (line 355) | `send` | `send the plan` | false |
| send from $EDITOR (line 465) | `send` | `send the plan` | false |
| bind (line 519) | `bind` | `bind` | false |
| retry (line 559) | `retry` | `stop and retry` | true |

**view_fleet.go `cardLines` (lines 716-720):** append the branch part only when `branch != ""`
after the `repoCell` fallback.

## 3. Tests

- **Port:** any test that asserted `y stop · n cancel` or similar text in a confirm's `lines`/view. Assert the
  modal's button row instead (`stop the round`, `cancel`). Cite this plan §2 in the report.
- **New** `TestConfirmModalOneButtonRow`: for the stop, done and unbind confirms, the stripped modal
  text contains `n` + `cancel` exactly once, and contains no `· n cancel`.
- **New** `TestFleetCardNoEmptyPart`: a row with no Branch and no CWD. The stripped card contains no
  `·    ·` (two separators around blanks).
- **Goldens:** regenerate them. Only confirm and fleet goldens may change. For fleet goldens, only rows
  whose fixture lacks Branch and CWD may change. Anything else is a halt.

## 4. Commands

- Loop: `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -count=1`, plus `-run Golden -update`
  for the goldens.
- Final: `gofmt -l internal/ui/*.go`, `go vet ./internal/ui/`, and
  `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ ./cmd/relevo/ -count=1`. Not `make check`.

## 5. Report

List:
- the files changed;
- the ported tests;
- the golden diffs;
- `confirm-stop-real-132.golden` verbatim;
- the check results.
