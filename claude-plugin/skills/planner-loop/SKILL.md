---
name: planner-loop
description: Drive a relevo round end to end -- send a plan, wait for the runner, read its report, run the project's check, gate on a usage limit, and mark it done. Use when a relevo binding has a round open or a runner's report has just arrived.
allowed-tools: Bash(relevo:*)
---

# The planner loop

You are the MasterMind. You hand a runner one round at a time, and you own the
decision that closes it.

## 1. The loop

1. Send the plan: `relevo send --name <n> --file <plan>`.
2. Wait for the round in the background:
   `relevo wait --name <n> --timeout <budget>` (run it with
   run_in_background, then end your turn; 0 closes with the report, 3 is NEEDS
   YOU, 4 is DONE, 124 is the timeout).
3. Read it: `relevo show <n> --report`, a reader's artifact with
   `relevo show <n> --output`, and the shape of the change with
   `relevo show <n> --diff --stat`.
4. Verify it: run the project's check yourself.
5. Close it: `relevo done <n>`.

## 2. The rules

- Do not trust the report. The report is the runner's claim; the diff and the
  project's check are the evidence. Never call `relevo done <n>` on a report's
  arrival alone.
- Run the project's own check yourself -- in this repo, `make check`.
- Compare `relevo show <n> --diff --stat` against the plan's declared scope.
  The report's `changed_paths` and `not_done` are where a round overreaches or
  leaves work behind.

## 3. When it is stuck

A halted or blocked round, or a NEEDS YOU, is a decision, not a retry:

- Gate the provider when the runner reported a usage limit:
  `relevo gate <token> --reason '<what it said>'`, and lift it with
  `relevo gate --clear <provider>` when it passes.
- Repair it: `relevo send --name <n> --file <repair plan>`.
- A bug in relevo itself -- an `internal` failure, a stuck round, a wrong
  status -- runs `relevo bugreport` (`--name`, `--round`, `--title`, `--body`,
  and `--logs` only for content the human agrees to share). It writes a local,
  redacted bundle; review it, then ask the human whether to file it with
  `relevo bugreport --gh`; if not, hand them the printed `gh issue create`
  line. Never file without asking.
- Abandon it: `relevo stop <n>`.

## 4. Reading the CLI

- `relevo help --json` is the registry of every verb, its flags, its output
  document and its error codes; `relevo help --json <verb>` prints one entry.
- A failure carries a stable code and, when one exists, the next command to
  run. The human line ends with `next: <command>`, and `--json` puts it in the
  error document. Run the `next` command.
