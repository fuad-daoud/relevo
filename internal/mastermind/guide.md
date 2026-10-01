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
- `relevo db query '<SQL>' [--json]` reads relevo.db with one read-only
  statement (SELECT, WITH or a read-only PRAGMA), through the daemon when it
  runs; use it instead of `sqlite3`, which the daemon's lock keeps out.
  RECURSIVE is refused: the owner cannot interrupt a statement, so a recursive
  CTE could run until the process is killed.

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

## Chains

- When several reviewed plans run in a row with no MasterMind turn between
  rounds, run them as one chain instead of driving each round by hand.
- Start it with `relevo chain --name <n> --plan r1.md --plan r2.md --feature
  <label> [--security]`; a planner actor writes the plans first, as above.
- `relevo wait --name <n>` returns once -- 0 the chain finished, 3 it halted or was stopped; then `relevo show <n> --trace`, and `relevo chain --resume --name <n>` after a halt.
- Never drive a running chain's members by hand: `send`, `done` and `unbind` on a member are refused until the chain is stopped.

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
  involved, and `--title T`/`--body FILE` to set the issue title and open the
  bundle with your own description; `--logs` adds that round's report, diff and
  transcript, so only for content the human agrees to share. The bundle is
  local and redacted and nothing is sent by default. Review it, then ask the
  human whether to file it with `relevo bugreport --gh`; if not, hand them the
  printed `gh issue create` line. Never file without asking.
- A binding that says NEEDS YOU is waiting on a human.
