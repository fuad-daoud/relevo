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

## Output and errors

- Every verb takes `--json`: stdout is then one document (`show --log` and a
  transcript stream are NDJSON, one object per line), and the human output stays
  the default.
- A failure is data: human stderr is `relevo: <code>: <message>` then
  `next: <command>`; with `--json` it is
  `{"error":{"code":"<code>","message":"<message>","next":"<command>"}}`. Run
  the `next` command.
- `relevo help --json` is the registry of every verb, its flags, its output
  document and its error codes; `relevo help --json <verb>` prints one entry.

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
- Something wrong in relevo itself -- an `internal` failure (its next is
  `relevo bugreport`), a stuck round, a status that lies -- is a bug: run
  `relevo bugreport`, with `--name <binding>` and `--round N` when one round is
  involved. The bundle is local and redacted and nothing is sent. Review it,
  then hand the human the printed `gh issue create` line -- never file as them.
  `--logs` adds that round's report, diff and transcript, for content the human
  agrees to share.
- A binding that says NEEDS YOU is waiting on a human.
