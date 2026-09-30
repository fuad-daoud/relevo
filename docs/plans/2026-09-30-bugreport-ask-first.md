# Plan: the guide asks whether to file with gh, explicitly (#722)

One docs round on `main` (`a15912e`), one commit. Separate from the running `bugreport-gh` round: this touches the three documentation surfaces, that one touches `cmd/relevo`. No shared file.

## Behaviour

**A. `internal/mastermind/guide.md`.** Replace the current bullet

```
- Something wrong in relevo itself -- an `internal` failure (its next is
  `relevo bugreport`), a stuck round, a status that lies -- is a bug: run
  `relevo bugreport`, with `--name <binding>` and `--round N` when one round is
  involved. The bundle is local and redacted and nothing is sent. Review it,
  then hand the human the printed `gh issue create` line -- never file as them.
  `--logs` adds that round's report, diff and transcript, for content the human
  agrees to share.
```

with (same voice, ask-first and explicit about both paths):

```
- Something wrong in relevo itself -- an `internal` failure (its next is
  `relevo bugreport`), a stuck round, a status that lies -- is a bug: run
  `relevo bugreport`, with `--name <binding>` and `--round N` when one round is
  involved; `--logs` adds that round's report, diff and transcript, so only for
  content the human agrees to share. The bundle is local and redacted and
  nothing is sent by default. Review it, then ask the human whether to file it
  with `relevo bugreport --gh`; if not, hand them the printed
  `gh issue create` line. Never file without asking.
```

**B. `claude-plugin/skills/planner-loop/SKILL.md`.** Replace its bugreport bullet with:

```
- A bug in relevo itself -- an `internal` failure, a stuck round, a wrong
  status -- runs `relevo bugreport` (`--name`, `--round`, and `--logs` only for
  content the human agrees to share). It writes a local, redacted bundle; review
  it, then ask the human whether to file it with `relevo bugreport --gh`; if
  not, hand them the printed `gh issue create` line. Never file without asking.
```

**C. Do not touch** `CONTRIBUTING.md` (human-facing, already points at the command), README, or any code.

## Steps

1. A and B as exact edits.
2. Regenerate the golden: `go test -count=1 ./internal/mcp/ -run 'Contract' -update`. Only `internal/mcp/testdata/contract/instructions.golden` may change, and only in the guide block. If any other file changes, stop and report.
3. Confirm the C12 plugin command-line scan still passes (it extracts `relevo <verb>` lines from the skill; `relevo bugreport --gh` is a valid verb+flag — `make check` covers this).
4. Focused: `go test -count=1 ./internal/mastermind/ ./internal/mcp/ ./cmd/relevo/`.
5. `make check` from the committed tree, exit 0; `gofmt` clean.
6. `docs/plans/2026-09-30-bugreport-ask-first.md`: this plan in the repo's shape. One commit; do not push.

## The report must include

- The two surface diffs and the golden's diff.
- The focused test output; the full `make check` output (exit 0).
- The commit hash and `git diff --stat`.
- One line: that no surface outside the three named changed.
