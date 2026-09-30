# Plan: the MasterMind surfaces recommend `relevo bugreport` and say how to use it (#719)

One docs-only round on `origin/main` (`bfd1d196`), one commit. `relevo bugreport` shipped in #716; the surfaces a MasterMind reads barely mention it. Fix that in three files plus the goldens they pin.

## Behaviour

**A. `internal/mastermind/guide.md`.** In "When something is stuck", replace the single bullet

```
- An `internal` failure's next is `relevo bugreport`: run it, and pass the printed
  `gh issue create` line to the human.
```

with this block (keep the guide's voice: short, no issue numbers, no history):

```
- Something wrong in relevo itself -- an `internal` failure (its next is
  `relevo bugreport`), a stuck round, a status that lies -- is a bug: run
  `relevo bugreport`, with `--name <binding>` and `--round N` when one round is
  involved. The bundle is local and redacted and nothing is sent. Review it,
  then hand the human the printed `gh issue create` line -- never file as them.
  `--logs` adds that round's report, diff and transcript, for content the human
  agrees to share.
```

The guide is injected into every session, so do not grow it beyond this block.

**B. `claude-plugin/skills/planner-loop/SKILL.md`.** In "When it is stuck", add one bullet after the "Repair it" bullet:

```
- A bug in relevo itself -- an `internal` failure, a stuck round, a wrong
  status -- runs `relevo bugreport` (`--name`, `--round`, and `--logs` only for
  content the human agrees to share). It writes a local, redacted bundle and
  prints a `gh issue create` line: hand it to the human, never file as them.
```

**C. `CONTRIBUTING.md` "Reporting bugs".** Replace the two-line text with:

```
Run `relevo bugreport`: it assembles a redacted diagnostic bundle locally, writes
it under the state root, and prints the `gh issue create` line that files it. Add
`--logs` for a round's report, diff and transcript when those can be shared.
`relevo version` and `relevo status --json` remain the short answers a maintainer
may ask for.
```

## Steps

1. A–C as exact edits above.
2. Regenerate the goldens the guide embeds: `go test -count=1 ./internal/mcp/ -run 'Contract' -update`. `internal/mcp/testdata/contract/instructions.golden` embeds `mastermind.Guide()`; the diff must be exactly the new guide block. Search for any other golden that pins guide text (`grep -rn 'internal.*failure' internal --include='*.golden' --include='*.go'`) and update only what the tests require.
3. Confirm the plugin command-line scan still passes (`make check` covers it) and the guide reads as a MasterMind acting on a bug, not just an error code.
4. Focused: `go test -count=1 ./internal/mastermind/ ./internal/mcp/ ./cmd/relevo/`.
5. `make check` from the committed tree, exit 0; `gofmt` clean.
6. `docs/plans/2026-09-30-bugreport-guide.md`: this plan, in the repo's plan shape. One commit: code/docs + plan. Do not push.

## The report must include

- The three files' diffs as applied, and the regenerated golden's diff.
- The focused test output and the full `make check` output (exit 0).
- The commit hash and `git diff --stat`.
- One line: whether any other surface beside the three named pins guide text.
