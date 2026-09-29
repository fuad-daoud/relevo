# Cockpit mouse wheel, round 2 (2026-09-25)

Round 2 on branch `relevo/ck-wheel`. The planner has rebased that branch onto `origin/main`, so its one commit is now
`84deff7`. Amend that commit; do not add a second one.

## 1. Why

Round 1's `TestWheelRouting/other_mouse_events_are_dropped` asserts that no `fakeView` in the stack is affected. But
`fakeView.Update` (`internal/ui/shell_test.go`, the `fakeView` type near the top) records only `tea.KeyMsg`s, in `last`
and `seen`. The planner mutation-tested the rule. They changed `updateMouse`'s `default:` branch
(`internal/ui/shell.go`) from `return m, nil` to `return m.updateStack(msg)`, so every non-wheel mouse event reached
every view, and the test still passed. The rule "a mouse event is never forwarded to `updateStack`" is therefore not
pinned. This round pins it. **No production code changes.**

## 2. Changes (`internal/ui/shell_test.go` only)

1. `fakeView` gets one more field, `mice int`. Its `Update` increments `mice` whenever `msg` is a `tea.MouseMsg`,
   next to the existing `tea.KeyMsg` branch. Nothing else in `fakeView` changes.
2. In `TestWheelRouting/other_mouse_events_are_dropped`, for **both** pushed `fakeView`s, and after every event the
   subtest sends, also assert `mice == 0`, alongside the existing `seen` assertion.
3. In the three subtests "wheel down sends three downs", "a capturing view gets no wheel", and "help, overlay and
   command line get no wheel", also assert `mice == 0` on the top view. The wheel reaches a view only as a key, never
   as the raw mouse message.

## 3. Working efficiently

- Read `internal/ui/shell_test.go` once. Make all three changes in one edit pass. Do not read or change any other file.
- **Focused test:** `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -run 'TestWheelRouting|TestKeyRouting' -count=1`
- **Then:** `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -count=1`, and
  `test -z "$(gofmt -l $(git ls-files '*.go'))"`.
- Do **not** run `make check`: a hook on this machine refuses it.

## 4. Steps

1. Make the §2 changes.
   - Verify: the focused test passes.
2. **Mutation (required).** In `internal/ui/shell.go`, in `updateMouse`, change the `default:` branch's `return m, nil`
   to `return m.updateStack(msg)`.
   - Verify: `TestWheelRouting/other_mouse_events_are_dropped` now **fails**. If it does not fail, halt and report.
   - Restore `shell.go` exactly: `git diff internal/ui/shell.go` must be empty afterwards.
3. Run the full `internal/ui` test and gofmt (§3).
4. Copy `/home/fuad/projects/relevo/docs/plans/2026-09-25-cockpit-mouse-wheel-r2.md` into this worktree's `docs/plans/`.
   Then `git add internal/ui/shell_test.go docs/plans/2026-09-25-cockpit-mouse-wheel-r2.md` and
   `git commit --amend --no-edit`. The branch must still have exactly one commit on top of `origin/main`.

## 5. Stop rather than improvise

If a step is impossible as written, or the code contradicts this plan, halt and report what you found.
