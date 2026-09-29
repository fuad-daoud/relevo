# Cockpit mouse wheel (2026-09-25)

One round, one commit, on branch `relevo/ck-wheel`.

## 1. System overview

The cockpit (`relevo ui`, `internal/ui`) never turns on mouse reporting, so the wheel does nothing. This round turns
on cell-motion mouse reporting when the program starts. The shell then turns each wheel notch into **three** `up` or
`down` key messages and sends them to the top view. Every view that scrolls already handles `up`/`down`:

- fleet (`view_fleet.go:423`), `:rounds` (`dash/model.go:298`), `:log` (`view_log.go:573`) and `:stats`
  (`view_stats.go:337`) move their cursor or scroll their page;
- the round detail (`view_round.go:400`) passes the key to its `viewport`, which scrolls one line per key.

So no view changes. The wheel is a key the shell makes up, so the routing rules for keys still apply, with the
changes listed under "wheel routing" below. Every other mouse event is thrown away in the shell. Today an unknown
message falls through to `updateStack` and reaches every view, and clicks must not do that.

A side effect the PR description must state: while an app reports the mouse, the terminal's own text selection needs
**Shift+drag**. The help overlay's ANYWHERE column says so too.

**Out of scope:** `internal/pick` (the separate picker program), clicks, and horizontal wheel. Do not touch them.

## 2. File structure

```
internal/ui/
  source.go        RunSource: add tea.WithMouseCellMotion() to the tea.NewProgram options (line 155)
  shell.go         Model.Update: new `case tea.MouseMsg`; new method Model.updateMouse; new const wheelStep
  frame.go         globalKeys (line 445): two new entries
  shell_test.go    fakeView records every key it sees; new TestWheelRouting
  testdata/help.golden   regenerated (two new ANYWHERE rows)
docs/plans/2026-09-25-cockpit-mouse-wheel.md   this plan, copied in as the last step
```

## 3. Data structures

- `const wheelStep = 3`, in `shell.go` next to `updateMouse`. It is the number of `up`/`down` keys one wheel event
  becomes. It matches the bubbles viewport's default `MouseWheelDelta` of 3. Add a one-line comment saying so.
- `fakeView` (`shell_test.go:17`) gets a new field `seen []string`. `Update` appends `k.String()` for every
  `tea.KeyMsg`, after setting `last` as it does today. The existing tests keep using `last` and do not change.

## 4. Contracts

### `func (m Model) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd)`, in `shell.go`

Its job is to turn one wheel event into `wheelStep` arrow keys for the top view, and to throw away every other mouse
event.

- **Precondition:** it is called only from `Update`'s new `case tea.MouseMsg:`. That case goes right after
  `case tea.KeyMsg:`.
- **Postconditions:**
  - Unless `msg.Action == tea.MouseActionPress` and `msg.Button` is `tea.MouseButtonWheelUp` or
    `tea.MouseButtonWheelDown`, it returns `m, nil` with nothing changed. This covers motion, release, clicks, and
    wheel left/right. The event is **never** passed to `updateStack`.
  - It returns `m, nil` with nothing changed if any of these hold: `m.cmd.open`, `m.overlay != nil`, `m.help`,
    `m.top().Capturing()`. The wheel never reaches the command line, a modal, the help overlay or an open text input.
  - Otherwise it builds `k := tea.KeyMsg{Type: tea.KeyUp}` for WheelUp or `tea.KeyMsg{Type: tea.KeyDown}` for
    WheelDown. It then calls `m.updateTop(k)` `wheelStep` times, feeding each result's model into the next call, and
    returns the final model with the non-nil commands in a `tea.Batch`, or `nil` if there are none.
  - It does **not** clear `m.notice`. `updateKey` clears it at line 311, but the wheel is not a typed key.
- **Errors:** none.

### `RunSource` (`source.go:155`)

`tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithContext(ctx))`. Nothing else in the
function changes. Add a one-line comment above it: the wheel scrolls, and Shift+drag selects text.

### `globalKeys` (`frame.go:445`)

Append, in this order, after `{"q", "quit / back"}`:

- `{"wheel", "scroll"}`
- `{"shift+drag", "select text"}`

## 5. Pseudocode

```
Update(msg):
  switch msg type
    KeyMsg   -> updateKey(msg)                  (unchanged)
    MouseMsg -> updateMouse(msg)                (new)
    ...rest unchanged

updateMouse(msg):
  if msg.Action != Press: return m, nil
  if msg.Button == WheelUp:        k = KeyUp
  else if msg.Button == WheelDown: k = KeyDown
  else: return m, nil
  if cmd.open or overlay set or help shown or top view capturing: return m, nil
  cmds = []
  repeat wheelStep times:
    next, cmd = m.updateTop(k); m = next.(Model); if cmd != nil: cmds += cmd
  return m, batch(cmds) or nil
```

## 6. Error handling

There are no new errors. Every mouse event either becomes arrow keys or is dropped; nothing is logged.

## 7. Working efficiently

- Every location is named above: `source.go:155`, `shell.go` `Update` (line 160) with `case tea.KeyMsg` at line 162,
  `frame.go:445`, and `shell_test.go:17`. Read those four files once, in one parallel batch. Do not search for them.
- Make each file's change in one edit.
- **Focused test:** `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -count=1`. Fix every failure
  before the next run.
- **Golden:** `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -run TestGolden -update -count=1`. Then
  check with `git diff --stat internal/ui/testdata` that **only** `help.golden` changed, and that its diff is exactly
  the two new ANYWHERE rows. If any other golden changes, stop and report: that means this plan is wrong about
  something. If the golden test's name is not `TestGolden`, find the test that reads `help.golden` in
  `golden_test.go` and use its name.
- **Final checks.** Do **not** run `make check`: a hook on this machine refuses it. Run these instead:
  - `test -z "$(gofmt -l $(git ls-files '*.go'))"`
  - `go vet ./internal/ui/`
  - `go mod tidy -diff`
  - `sh scripts/check-name.sh`

  The planner runs the full race suite on the server.

## 8. Steps

1. **`fakeView.seen`.** Add the field and the append (§3), in `shell_test.go`.
   - Verify: the focused test passes and nothing else changes.
2. **`wheelStep` and `updateMouse`**, plus the `case tea.MouseMsg:` in `Update` (§4), in `shell.go`.
   - Verify: it compiles, and the existing tests still pass.
3. **`TestWheelRouting`** in `shell_test.go`. Build the shell with `splitModel(t, 140, 40, …)` as
   `TestKeyRoutingRules` does, and push a `fakeView` on top. Use subtests:
   - "wheel down sends three downs": a `tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown}`
     leaves the top view's `seen` equal to `["down","down","down"]`.
   - "wheel up sends three ups": the same with WheelUp.
   - "wheel keeps the notice": set `m.notice = "x"`, send wheel down, and `m.notice` is still `"x"`.
   - "a capturing view gets no wheel": push `fakeView{capture: true}`, and `seen` is empty afterwards.
   - "help, overlay and command line get no wheel": three cases. Open help with `?` and the command line with `:`, as
     `TestKeyRoutingRules` does. For the overlay, add a test-only `fakeOverlay struct{}` to `shell_test.go` that
     implements `overlay` (`confirm.go:36`: `update` returns itself, nil and false; `view` returns nil), and set
     `m.overlay = fakeOverlay{}`. After a wheel
     event the state is unchanged (the help still shown, the command line still open with nothing typed, the overlay
     still set), and the top view's `seen` is empty.
   - "other mouse events are dropped": a left-button press, a release, a motion event and a `MouseButtonWheelLeft`
     press each leave `seen` empty. Also, **a `fakeView` lower in the stack** (push two) sees nothing: this pins the
     "never to `updateStack`" postcondition.
   - Verify it is a real test: the planner will mutation-test it. Change `wheelStep` to 1, and the first two subtests
     must fail. Delete the `Capturing()` check, and the capturing subtest must fail. Do both mutations yourself, see
     them fail, and restore the code.
4. **`globalKeys`** (§4, `frame.go`), then regenerate `help.golden` as §7 says.
   - Verify: only `help.golden` changed, by exactly the two rows.
5. **`RunSource`** gets `tea.WithMouseCellMotion()` and its comment (§4, `source.go`).
   - Verify: it compiles. Nothing tests it, because starting the program needs a tty (see the comment on
     `runResult`). The planner checks it on a real screen.
6. **Final checks** (§7). Then copy `/home/fuad/projects/relevo/docs/plans/2026-09-25-cockpit-mouse-wheel.md` into
   this worktree's `docs/plans/`, and make **one** commit:

   ```
   feat(cockpit): the mouse wheel scrolls every view; shift+drag selects text
   ```

   Put a short body under the subject that states the Shift+drag trade-off.

## 9. Stop rather than improvise

If a step is impossible as written, or the code contradicts this plan (a line number is far off, a view does not
handle `up`/`down` as §1 says, a golden other than `help.golden` changes, a mutation does not fail), **halt and
report** what you found. Do not bend a test to fit.
