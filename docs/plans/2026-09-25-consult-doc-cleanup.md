# Cleanup: the reviewer role and two comments still describe the findings/ask files

Date: 2026-09-25. Base: origin/main (0efca3c or later). One round, one PR. **Text only: no
behaviour change.**

**Stop rather than improvise.** If a step contradicts the files, or any test fails, halt
and report it.

## Why

- **#446:** a consult's findings are its final message. relevo records them, and the
  prompt says "Do not write a findings file".
- **#480:** the question travels inside the prompt, and falls back to a staged file only
  when it is large.

The shipped reviewer definitions still tell the reviewer to write findings to a path and
reply with only that path, and two code comments are stale too.

## Changes

### 1. Reviewer role definitions (internal/harness/agents/)

Files: `reviewer.claude.md`, `reviewer.opencode.md`, `reviewer.codex.toml` and
`reviewer.agy.md`. Make the same six edits in each. Keep each file's own format:

- the claude, opencode and codex files use UPPERCASE plain headings;
- agy uses `# …` markdown headings and may wrap lines differently.

Re-wrap to each file's existing width. Change nothing else.

- **E1, the description.** The frontmatter `description:` in claude, opencode and agy, and
  the first comment line in the codex file if it says the same, becomes:
  `Read-only reviewer of a diff or a question, spawned by relevo ask. Answers with its findings as its final message; relevo records them. Never edits anything.`
- **E2, the opening.** "…usually a diff, described in the file the prompt names." becomes
  "…usually a diff, described in the question the prompt carries (inline, or in a file it
  names when the question is too large to inline)."
- **E3, the heading.** "WHAT A GOOD FINDINGS FILE LOOKS LIKE" becomes "WHAT GOOD FINDINGS
  LOOK LIKE". In agy: "# What a good findings file looks like" becomes
  "# What good findings look like".
- **E4, the first bullet.** "Write your findings to the path named in the prompt, and
  reply with only that path. The caller reads nothing else you say: the file is the entire
  deliverable, and its existence is the only thing that tells relevo you finished."
  becomes:
  "Give your findings as your final message, complete, in markdown. Do not write a
  findings file: relevo records your final message, and it is the entire deliverable."
- **E5, one-shot.** "You are one-shot: relevo takes your findings once the findings file
  exists, and an answer from a sub-agent would arrive after that." becomes:
  "You are one-shot: relevo takes your findings from your final message when you exit,
  and an answer from a sub-agent would arrive after that."
- **E6, researcher contrast.** "…asked by the planner through `relevo ask`, and hands back
  a file path." becomes "…asked by the planner through `relevo ask`, and hands back its
  findings as its final message."

Then run `sh scripts/agents-shipped.sh --write`. It appends the new hashes to
`internal/harness/agents/shipped.sha256`, and only appends: report the added lines. Then
run `sh scripts/agents-shipped.sh --check`, which must pass.

### 2. Code comments

- **internal/relevo/verify.go ~199**, in `startVerifyConsult`'s postconditions: "its
  question staged at the binding's ask path" becomes "its question recorded at the
  binding's ask path (passed in the prompt and kept in round_file, or staged as a file
  when too large to inline)".
- **internal/relevo/usage.go ~79:** the trailing comment
  `// consults have no stream file today; the reader notes "no stream"` becomes
  `// the consult's stream file: stdout, stderr and the exit trailer (#420)`.

## Steps

1. Section 1, then run the two script commands.
2. Section 2.
3. Search the repo for any test that pins the old reviewer text:
   `grep -rn "file path the prompt names\|findings file exists" --include=*.go .`.
   If one exists, **halt and report it**. Do not change the test.
4. Full check:
   - `make check`. If a hook blocks it, run its steps directly;
   - paste `gofmt -l $(git ls-files '*.go')`'s empty output;
   - make sure the agents-shipped check is among the steps.
5. Save this plan verbatim at `docs/plans/2026-09-25-consult-doc-cleanup.md`.
6. Commit and PR.
   - One commit: `docs(reviewer): findings are the final message and the question comes
     in the prompt; two stale comments`.
   - Push with `git push -u origin <branch>`. Open a PR against main.

## Scope

- Only the four reviewer files, `shipped.sha256`, verify.go, usage.go and this plan.
- No test changes. No other agent definitions (architect, plan-executor, researcher).
