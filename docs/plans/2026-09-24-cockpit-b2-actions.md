# Cockpit B2: the cockpit acts on rounds

Spec: `docs/specs/2026-09-24-cockpit-design.md` §6.2 and §6.3 (Actions, the human
planner, stderr capture) and §4.3 (the fleet row's keys). Mockups: the `:fleet`,
`confirm · stop a round` and `round detail` boards of
https://claude.ai/artifact/7W1oBWL1BC5Zym4G3F2JaA. This builds on `cockpit/wave-1`
(B1's shell), with line numbers as on that branch.

**This plan runs in two rounds. Each send states which round it is. Do only that
round's steps, then stop and report.**

- **Round 1:** the Actions seam, the human planner, the confirm and prompt widgets,
  stderr capture, and the actions that need no file: stop, done, unbind, gate, ungate
  and shell.
- **Round 2:** send (a file, or `$EDITOR`), bind, retry-on, and delivering reports to
  the human planner.

**If a step is impossible as written or contradicts the code, stop and report. Do not
improvise.**

CI has no harness and no network. Every key → confirm → action path is tested
against a **fake `Actions`**. The real `plannerActions` is a thin adapter over
`internal/relevo` functions. It is compiled and vetted but never run by a test that
would spawn a builder, and the plan says which parts get pure tests.

A parallel plan (C2b) adds a `stats` row to `internal/ui/cmdline.go`'s command table.
Keep your cmdline edits to your own rows and dispatch cases.

## 1. System overview

Today the cockpit only reads. B2 adds a `ui.Actions` interface whose implementation
calls the same functions the CLI verbs call. The fleet and the round view get keys
for them, and each destructive action goes through a one-line confirm that names the
target and anyone affected.

The TUI acts as a planner record named **`you`**, with harness kind `human` and
session `tui` (spec §6.3). Bindings it creates belong to `you`. Reports addressed to
`you` show as "report ready", and are marked delivered when the round view opens
them.

While the cockpit runs, `os.Stderr` and the default slog logger are redirected into
the footer, so no internal write can corrupt the screen.

`serve ui` passes no Actions. Its action keys are hidden, and pressing one does
nothing.

## 2. File structure

```
internal/ui/actions.go         NEW (r1)  Actions interface, Result, plannerActions (adapter), ensureYou
internal/ui/confirm.go         NEW (r1)  confirm overlay + prompt overlay (single-line input, optional choice list)
internal/ui/stderr.go          NEW (r1)  captureStderr: pipe + slog handler -> stderrMsg lines
internal/ui/view_fleet.go      (r1) action keys on rows; (r2) b, s, r
internal/ui/view_round.go      (r1) x, D, u, g, o on the viewed binding; (r2) s, r, report-ready pull
internal/ui/shell.go           (r1) overlay routing, actionMsg handling, immediate refetch after an action
internal/ui/frame.go           (r1) overlays drawn over the body's last rows; footer shows "working: <verb> <name>…"
internal/ui/cmdline.go         (r1) + `ungate <provider|candidate>`
internal/ui/ui.go / source.go  (r1) Options.Actions; Run builds plannerActions; RunSource wires stderr capture
internal/relevo/pull.go        (r2) exported Pull (wraps pullPending)
internal/relevo/retry.go       NEW (r2) RetryPlan: the plan bytes of the round to retry (pure over Store)
internal/ui/actions_test.go, confirm_test.go, stderr_test.go   NEW
internal/relevo/retry_test.go  NEW (r2)
internal/ui/golden_test.go     + confirm-stop, prompt-gate (r1); prompt-send, report-ready (r2)
```

## 3. Data structures

```
// internal/ui/actions.go
type Actions interface {
    Stop(ctx context.Context, key string) Result
    Done(ctx context.Context, key string) Result
    Unbind(ctx context.Context, key string) Result                       // always archives
    Gate(ctx context.Context, subject string, forDur time.Duration, reason string) Result
    Ungate(ctx context.Context, subject string) Result
    Shell(key string) (*exec.Cmd, error)                                 // a shell in the binding's tree
    // round 2:
    Send(ctx context.Context, key, planFile string) Result
    Bind(ctx context.Context, in BindInput) Result
    Retry(ctx context.Context, key, candidate string) Result
    Pull(ctx context.Context, key string) (text string, ok bool, err error)
    Candidates(role string) []string                                     // names, in the role's order
}
type Result struct {
    Text    string // what the CLI would print on success (StopText, DoneText, ...), multi-line allowed
    Err     error
    Refresh bool   // refetch status now
}
type BindInput struct{ Name, Candidate, Feature string }   // Candidate "" = the policy pick

type plannerActions struct {
    rt   relevo.Runtime
    repo string        // os.Getwd() at start; "" when not inside a git repo (bind refuses)
    you  string        // the human planner's id, from ensureYou
}

// actionMsg is what an action's tea.Cmd returns.
type actionMsg struct{ verb, key string; res Result }
// stderrMsg carries one captured stderr/slog line.
type stderrMsg struct{ line string }
```

**Confirm and prompt overlays** (`confirm.go`):

```
type overlay interface{ update(tea.KeyMsg) (overlay, tea.Cmd, bool /*closed*/); view(width int) []string }
type confirmBox struct{ title string; lines []string; onYes tea.Cmd }            // y runs onYes; n/esc cancel
type promptBox struct {
    title   string
    input   textinput.Model
    choices []string    // optional: tab cycles the input through them
    onEnter func(value string) tea.Cmd                                            // esc cancels
}
```

The shell holds at most one overlay (`Model.overlay overlay`). While it is set, every
key goes to it first. This is rule 2.5, between cmdline and help in §5.2's order.

## 4. Contracts

### 4.1 The human planner: `ensureYou(rt) (string, error)`

- Call `planner.Init(rt.Planners, planner.InitInput{Kind: "human", SessionID: "tui",
  CWD: <home dir>, Name: "you", Now: rt.Now()})` and return the record's ID.
  `Record.Validate` already accepts any non-empty kind except the opencode rule.
- It is idempotent: a second call returns the same ID. Verify that `Init` looks the
  record up by (kind, session). If it does not, stop and report rather than creating
  duplicates.
- It is called lazily, on the first action that needs an owner (bind and send in
  round 2), not at startup, so a read-only session never creates the record.
- It is never pruned, because `HostPID` is 0 (`internal/planner/prune.go:26`).

### 4.2 `plannerActions` (round 1)

Each method runs `rt, name, ok := src.Runtime(key)` (`ok` false gives
`Result{Err: "unknown binding"}`), then calls the verb's `internal/relevo` function
exactly as the CLI does. The research that mapped them:

| action | calls | Result.Text |
|---|---|---|
| Stop | `relevo.Stop(ctx, rt, name, relevo.StopOptions{})` | `relevo.StopText(name, res)`. `ErrNothingToStop` gives `Text = "nothing to stop: <name> has no open round"`, `Err = nil` (cmd/relevo/main.go:2353-2382). |
| Done | `relevo.Done(ctx, rt, name)` | `relevo.DoneText`. On `ErrStopFailed`, return both the text and the error (main.go:2337-2344). |
| Unbind | `relevo.Unbind(ctx, rt, name, true)` | `relevo.UnbindText` |
| Gate | `candidate` subject: `relevo.Unavailable(rt, token, until, reason)`, then `relevo.ForwardUnavailable(ctx, rt, token, reason)` | `gated <provider> (N candidates) <GateUntilText>`, then one line per `BindingsOnProvider` switch note, then the forward lines. The same text as `gateUnavailable` (main.go:1004-1044, A1 version on this branch). |
| Ungate | `relevo.Available(rt, subject, relevo.ClearedByPlanner)`, then `relevo.ForwardAvailable` | `cleared <p> (N entries)` or `nothing was gating <p>`, then the forward lines (main.go:1048-1084) |
| Shell | — | Returns `exec.Command(shell)` with `Dir` set to the binding's `Worktree`, or `CWD` when `Worktree` is empty. `shell` is `$SHELL`, else `/bin/sh`. A remote binding gives the error `a remote binding has no local tree`. |

**Gate's subject** from a fleet row is the row's candidate. Pass the stored token;
`Unavailable` resolves names or tokens (A1). `forDur` 0 means "until cleared".

**Every action returns `Refresh: true`.**

### 4.3 Keys and confirms (round 1)

These apply on a fleet row and in a round view (the viewed binding), only when
`Actions != nil`.

| key | overlay | on yes / enter |
|---|---|---|
| `x` | confirm `Stop <name> round <N>?` | `Actions.Stop` |
| `D` | confirm `Mark <name> done?` | `Actions.Done` |
| `u` | confirm `Unbind <name>? The binding is archived; its branch <branch> is kept.` | `Actions.Unbind` |
| `g` | prompt `gate <provider> for (e.g. 2h; empty = until cleared):`, then prompt `reason (optional):` | `Actions.Gate` |
| `o` | none | `tea.ExecProcess(Actions.Shell(key), …)`. The terminal is released and restored. An error becomes a notice. |

**Confirm lines** name who else is affected:

- When the row's planner is not `you` and not empty: `planner <PlannerName> is waiting
  on this round` for stop, or `planner <PlannerName> owns this binding` for done and
  unbind.
- Stop adds `<actor> on <candidate> · <now text> · <spend>` from the row.
- The last line is `y <verb> · n cancel`.

**The gate prompt** validates the duration with `time.ParseDuration`, which must be
positive, on enter. An invalid value keeps the prompt open with an `errorStyle`
message under the input.

**Command line:** add `ungate <provider|candidate>`, which runs `Actions.Ungate` with
no confirm. Completion candidates are the providers and names of `report.Gated`.

**While an action runs:** the footer's right side shows `working: <verb> <name>…`
until its `actionMsg` arrives. A second action on the same binding is refused with the
notice `<name>: <verb> still running`.

**On `actionMsg`:** the notice is the first line of `Text`, or the error in
`errorStyle`. The full multi-line text is appended to a new **`:log` view**: a simple
scrollback of every action result this session, newest last, with no persistence.
Its command row is `log`, "this session's action results". Then, when `Refresh` is
set, fetch status immediately.

### 4.4 Stderr capture (round 1, `stderr.go`)

```
func captureStderr(send func(tea.Msg)) (restore func(), err error)
```

1. Create an `os.Pipe`, save `os.Stderr` and `slog.Default()`, and set `os.Stderr` to
   the pipe's writer.
2. Set `slog.SetDefault(slog.New(slog.NewTextHandler(pipeWriter, nil)))`.
3. A goroutine scans lines from the reader and calls `send(stderrMsg{line})`.
4. `restore` closes the writer, waits for the goroutine, and puts `os.Stderr` and the
   default logger back.

`RunSource` calls it after `tea.NewProgram` with `send = p.Send`, and defers
`restore`, including on panic.

The shell turns a `stderrMsg` into the sticky notice (faint) and appends it to `:log`.

**Test:** capture, write to `os.Stderr` and to `slog.Info`, both arrive through `send`,
restore, and `os.Stderr` is the original again.

### 4.5 Round 2: send, bind, retry, pull

| key | where | overlay | action |
|---|---|---|---|
| `s` | fleet row, round view | prompt `plan file:` pre-filled with `<binding tree>/docs/plans/` when that dir exists; `tab` completes a path (list the dir, cycle matches); enter checks the file exists | confirm `Send <file> to <name> as round <N+1>? <planner line>`, then `Actions.Send` |
| `E` | fleet row, round view | none: create `<state dir>/tui-plans/<name>-<unix>.md` with a one-line header comment, `tea.ExecProcess($EDITOR …)` | on exit, if the file is non-empty and changed, the same send confirm; otherwise the notice `nothing sent` |
| `b` | fleet | prompts in order: `name:` (validated with `store.ValidName`), then `candidate (tab: …; empty = policy pick):` with `choices = Actions.Candidates("builder")`, then `feature (optional):` | confirm `Bind <name> on a new worktree of <repo> as builder on <candidate or "the policy pick">?`, then `Actions.Bind` |
| `r` | fleet row, round view | prompt `retry on (tab cycles):` with `choices = Actions.Candidates(role)`, excluding the current candidate | confirm `Stop <name> round <N> and resend its plan on <candidate>?` (or `Resend <name>'s last plan as a new round on <candidate>?` when no round is open), then `Actions.Retry` |

**`plannerActions` round 2:**

- **Send:** `relevo.Send(ctx, rt, name, file, relevo.SendOptions{})`. The text is
  `res.Pick`, then `res.Drift` (if non-empty), then `sent round <N> to <name>`.
- **Bind:**
  - Refuse when `repo == ""`, with `start relevo ui inside a git repository to bind`.
  - Otherwise `relevo.Add(ctx, rt, relevo.AddOptions{Name, Candidate, PlannerID:
    you, Repo: repo, Feature})`, where `you` comes from `ensureYou`.
  - The text is `added <name>: builder <NameOf> on <worktree>`, plus the gated note
    and the pick note (`relevo.PickText`), the way `runAdd` prints them
    (main.go:1460-1555).
- **Retry:**
  1. If a round is open, run `relevo.Stop`, then use `StopResult.Round`. On
     `ErrNothingToStop`, use the highest round that has a plan.
  2. Get the plan with `relevo.RetryPlan(rt, name, round)`, write it to a temp file,
     and call `relevo.Send(ctx, rt, name, tmp, relevo.SendOptions{Builder:
     candidate})`.
  3. The builder change **persists**, as with `send --builder`. Put that in the
     confirm line: `the binding keeps <candidate> for later rounds`.
- **Pull:** `relevo.Pull(ctx, rt, name, "tui")`.
- **Candidates(role):** the role's candidate names, in order, from
  `rt.RoleRegistry()` and `rt.Candidates.NameOf`.

**`relevo.RetryPlan(rt Runtime, name string, round int) ([]byte, error)`** is
`rt.Store.ReadFile(rt.Store.PlanPath(name, round))`. `Store.ReadFile` falls back to
sealed `round_file` rows (`internal/store/seal.go:32-57`). A missing plan gives
`no plan recorded for <name> round <N>`.

**`relevo.Pull(ctx, rt, name, route string) (string, bool, error)`** is exported
over `pullPending` (`internal/relevo/pull.go`).

**Report ready:**
- A fleet row whose `PlannerName == "you"` and `Pending != nil` shows NOW as
  `report ready · <age>` in `stateNeedsYouStyle`. It counts toward the header's
  needs-you number.
- Opening its round view calls `Actions.Pull(key)` once. A found text is shown in the
  report tab, the fleet refetches, and the binding is no longer "report ready".

## 5. Pseudocode: a confirmed stop

```
fleet key "x" on row b:
  if env has no Actions: return nil
  lines = stopConfirmLines(b)        // pure, tested
  return openOverlay(confirmBox{"Stop " + b.Key() + " round " + N + "?", lines,
                     runAction("stop", b.Key(), func(ctx) Result { return A.Stop(ctx, b.Key()) })})
shell on key while overlay set: overlay.update(k) -> y: close + onYes cmd
runAction(verb, key, f) = tea.Batch(workingMsg{verb,key}, func() tea.Msg { return actionMsg{verb, key, f(ctx)} })
shell on actionMsg: clear working; notice(first line); log(append text); if Refresh: fetchStatus
```

## 6. Error handling

| case | result |
|---|---|
| An action returns `Err` | A red notice with `Err.Error()`, and a `:log` entry. Nothing crashes, and the fleet refetches anyway. |
| `Actions == nil` (serve ui) | The action keys are absent from `Keys()` and do nothing when pressed. |
| A second action on the same key while one runs | Refused with a notice. |
| `ExecProcess` fails | A notice. The terminal is restored by bubbletea. |
| The bind prompt gets an invalid name | Kept open, with the validator's message. |
| Retry with no plan recorded | The notice from `RetryPlan`. Nothing is stopped (check the plan first, then stop). |

## 7. Tests

### Round 1

- **`actions_test.go`** defines `fakeActions`, which records every call with its
  arguments and returns scripted `Result`s.
  - `TestStopKeyConfirmsThenCalls`: `x`, then `y` calls `Stop(key)`. `x`, then `n`
    calls nothing.
  - `TestConfirmNamesTheOwningPlanner`: the planner line is present when the owner is
    not `you`.
  - The same pattern for `D` and `u`.
  - `TestGatePromptValidatesDuration`.
  - `TestUngateCommand`.
  - `TestActionResultBecomesNoticeAndLog`.
  - `TestSecondActionOnSameBindingRefused`.
  - `TestActionKeysHiddenWithoutActions`.
  - `TestShellKeyExecs`: use the fake, which returns `exec.Command("true")`, and
    assert the returned cmd is an exec cmd. Do not run it.
- **`TestEnsureYouIdempotent`**, with a `planner.DBRegistry` on a temp DB: two calls
  give one record, of kind `human`, named `you`.
- **`stderr_test.go`** as in §4.4.
- **Goldens:** `confirm-stop` (140x40, the fleet with the `x` overlay open on
  `atlas`) and `prompt-gate`.
- **Mutation check:** make the confirm's `y` handler ignore the key, so it never
  calls `onYes`. `TestStopKeyConfirmsThenCalls` must fail. Report it, then revert.

### Round 2

- `TestSendPromptThenConfirm`: the file must exist. Use `t.TempDir()`.
- `TestEditorSendSkipsEmpty`: `ExecProcess` is not run in the test. Test the pure
  "file changed and non-empty?" helper.
- `TestBindPromptChain`: an invalid name, a tab-cycled candidate, enter, then
  `Actions.Bind` receives the `BindInput`.
- `TestRetryConfirmText`: the open-round and no-open-round variants, pure.
- `TestReportReadyRowAndPull`: a row with `PlannerName "you"` and `Pending` shows
  report ready, and opening it calls `Pull` once.
- `internal/relevo`:
  - `TestRetryPlanReadsSealed`: store a plan and seal it with the store test helpers
    `internal/relevo` already uses. `RetryPlan` still returns it.
  - `TestPullMarksTuiRoute`: queue a pending entry through the store helpers, `Pull`
    returns its text, and the entry's route is `tui`.
- **Goldens:** `prompt-send` and `report-ready`.
- **Mutation check:** make Retry send without `Builder`. The fake records it, and the
  retry test must fail. Report it, then revert.

## 8. Working efficiently

A builder pays one round trip per step whatever the step does, so:

- Batch the reads: `view.go`, `shell.go`, `frame.go`, `cmdline.go`, `view_fleet.go`,
  `view_round.go`, `source.go`, `ui.go`, `golden_test.go`, the cmd verbs named in
  §4.2 (for their exact output text), `internal/relevo/pull.go`, and
  `internal/planner/hook.go` (`Init`).
- Write each new file in one call.
- Iterate on `go test ./internal/ui/... ./internal/relevo/ -run 'Retry|Pull' ./internal/planner/...`.
- Run `make check` once per round.

## 9. Ordered steps

### Round 1

**1.1 `actions.go`.**
- Deliverable: the interface, `Result`, `plannerActions` round-1 methods, `ensureYou`,
  and `Options.Actions`. `Run` builds `plannerActions` with `repo` from `os.Getwd()`,
  or `""` when `git rev-parse` fails. Use `rt.Git.HeadCommit(ctx, cwd)`, and treat an
  error as not a repo.
- Verify: `go build ./...`.

**1.2 `confirm.go` and the shell overlay routing.**
- Deliverable: rule 2.5 and the frame drawing.
- Verify: `go test ./internal/ui/`.
- Depends on 1.1.

**1.3 The keys.**
- Deliverable: fleet and round view keys, the `ungate` command, the working
  indicator, and the `:log` view.
- Depends on 1.2.

**1.4 `stderr.go` and its `RunSource` wiring.**
- Depends on 1.1.

**1.5 Tests, goldens, the mutation check, `make check`.**
- Report the functions with their line ranges and paste the `confirm-stop` golden.
- Depends on 1.3 and 1.4.

### Round 2

**2.1 `relevo.Pull` and `relevo.RetryPlan`**, with their tests.

**2.2 `plannerActions` round-2 methods** and `Candidates`.

**2.3 The keys `s`, `E`, `b`, `r`, and report-ready.**
- Depends on 2.1 and 2.2.

**2.4 Tests, goldens, the mutation check, `make check` and `make e2e`.**
- Report the functions with their line ranges, the pasted goldens, and
  `git diff --stat`, which is `internal/ui/**` and `internal/relevo/{pull,retry}*`
  only.
