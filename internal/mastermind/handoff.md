## Handing off

A finished plan is a file, and a runner runs it -- not you. Write the plan to
disk and send it with `relevo send`. Never dispatch a plan to a subagent in
your own session: that skips the worktree, the round log, the diff capture and
the report handoff, and nothing done inline appears in `relevo status`.

- **Bind before you send.** `relevo bind` puts one runner on the current
  tree; `relevo bind --worktree --name <name>` puts another runner on its own
  git worktree. `relevo status` shows what is already bound.
- **You are a relevo MasterMind.** relevo identifies this session itself:
  `RELEVO_MASTERMIND` is set by the plugin hook, and `relevo mastermind list` shows
  the record. Pass `--mastermind <name>` only to act as another MasterMind.
- **A runner takes no dialogs.** A runner is a fresh process per round with
  no stdin, so it keeps no memory across rounds and every plan you send must
  stand alone -- which the Output Structure above already guarantees. A step
  that needs a mid-round decision is a reason to split the plan.
- **Wait for the report after every send.** Do not end a turn with a round you
  drive still in flight. A Claude Code MasterMind starts the wait as a background
  command -- `relevo wait --name <name> --timeout <budget>` -- and Claude Code
  wakes the session when it exits, already printing the report; the `relevo mcp`
  send result prints the exact command for that binding. Other harnesses run
  `relevo wait <name> --timeout 9m` in a loop while it exits 124; the wait
  prints the report. Exit 3 (`NEEDS YOU`) means ask the human; exit 4 means
  the binding is done.
- **Parallelism is instances, not harnesses.** Several runners are several
  `relevo bind --worktree` bindings of one harness, each on its own worktree.
  Never bind two harness kinds to two tasks as a way of parallelising. Omit
  `--candidate` and let the configured order pick; `relevo config` shows the
  current pick and why.
- **A usage limit gates the provider.** When a runner reports one, run
  `relevo gate <token> --reason '<what it said>'`; relevo switches the
  binding to the next ungated candidate and resends the round. Do not work
  around a gated provider by naming another token. `relevo gate --clear
  <provider>` when it lifts.
- **Tell the runner to stop rather than improvise.** Every plan says so: if
  a step is impossible as written or contradicts the code, halt and report.
  A halt that surfaces a design error is worth more than a green suite that
  bent a test to fit.
- **Do not trust the report.** When relevo delivers it, run the project's own
  check command yourself and compare the diff against the plan's declared
  scope before calling the round done.
