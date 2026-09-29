# The relevo guide

This is the guide to relevo, the tool that hands work between you and a runner.

## Using relevo

- `relevo bind [--worktree] --name <n> --feature <label>|--no-feature
  [--ticket <ref>]` starts a fresh runner on the current tree or its own git
  worktree; a fresh bind must name exactly one of `--feature`/`--no-feature`,
  and `--ticket` names the issue it serves (a number, `#N`, `owner/repo#N` or
  an issue URL, with or without a feature). `relevo status` shows what is bound
  and what it is doing.
- `relevo send --name <n> --file <path>` hands a runner a round; a round's
  input is its prompt, and a planner actor's prompt is a small seed.
- `relevo wait --name <n> --timeout <budget>` waits for a round: 0 closes with
  the round's output, 3 is NEEDS YOU -- ask the human, 4 is DONE.
- `relevo show <n> [--round N] --prompt|--report|--output|--diff|--transcript`
  reads a round; a reader's artifact is `<label>.md`, printed with `--output`.

## The recommended loop

- Seed a planner actor (`planner`, `lite-planner`) with the task and the
  decisions already made.
- Review its plan with `relevo show <name> --output`.
- Hand that file to a builder with
  `relevo send --name <builder> --file <the output path>`.
- When the round closes, run the project's own check and compare the diff
  against the plan before `relevo done`.

## When something is stuck

- `relevo gate <token> --reason '<what it said>'` when a runner reports a usage
  limit: relevo switches and resends; `relevo gate --clear <provider>` when it
  lifts.
- `relevo stop <name>` ends an open round.
- `relevo bind --resume --name <n>` restores a released worktree; `--feature`
  sets its label, `--no-feature` clears it, and naming neither keeps it.
- A binding that says NEEDS YOU is waiting on a human.
