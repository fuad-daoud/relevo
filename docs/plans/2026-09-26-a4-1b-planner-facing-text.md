# A4-1b: the words planners and builders read

Spec: `docs/specs/2026-09-26-a4-state-rename-design.md`, §2 and §3.6.

**Vocabulary:**
- An **actor** is config.
- A **runner** is the live process that plays one actor.
- A **candidate** is the model a round runs on.
- `builder` stays valid only as the seeded writer actor's name.

Decision D1 is a clean break: the flag is `--candidate`, and `--builder` is gone.

This round runs in parallel with A4-1a, which owns `cmd/relevo/*`,
`internal/mcp/tools.go`, and the error and help texts in `internal/relevo` and
`internal/doctor`. **Edit only the files listed here.**

**If a step is impossible as written or contradicts the code, stop and report. Do not
improvise, and do not bend a test to fit.**

## 1. The architect "Handing off" section

The four copies are `internal/harness/agents/architect.claude.md`,
`architect.opencode.md`, `architect.agy.md` and `architect.codex.toml`.
`internal/planner/handoff.md` must stay byte-identical to the section, which
`internal/planner/handoff_test.go` `TestHandoffRulesMatchShippedArchitectCopies` checks.

- "builder" meaning the process that runs a round becomes "runner". Examples:
  - "a builder runs it" becomes "a runner runs it";
  - "A builder takes no dialogs" becomes "A runner takes no dialogs";
  - "another builder on its own git worktree" becomes "another runner on its own git
    worktree";
  - "Several builders are several `relevo bind --worktree` bindings" becomes "Several
    runners are …";
  - "When a builder reports one" becomes "When a runner reports one".
- `--builder` becomes `--candidate`. Check every mention.
- "Tell the builder to stop rather than improvise" becomes "Tell the runner to stop
  rather than improvise".
- Do not change anything outside the "Handing off" section in these files.
- Then run `sh scripts/agents-shipped.sh --write` to append the new hashes to
  `internal/harness/agents/shipped.sha256`, and `sh scripts/agents-shipped.sh --check`.

## 2. MCP instructions (`internal/mcp/instructions.go`, lines ~12, 32, 58, 80)

- "a builder's round closed" becomes "a runner's round closed".
- "hand a binding's builder a new round" becomes "hand a binding's runner a new round".
- Regenerate `internal/mcp/testdata/contract/instructions.golden` with the contract
  test's `-update`, and report the diff.

## 3. Delivery and wait lines

- `internal/delivery/origin.go:14,17,19`: "to builder %q" becomes "to runner %q", and
  "about builder %q" becomes "about runner %q". The constant `store.DirToBuilder` is
  **not** renamed here (A4-2 owns directions).
- `internal/relevo/wait.go:~125`: "was never sent to %s's builder" becomes "was never
  sent to %s's runner".
- The payload texts:
  - `internal/relevo/stop.go:~185,187`: "Builder was stopped" becomes "The runner was
    stopped";
  - `internal/relevo/remote_catchup.go:~169`: "Builder finished round %d on %s" becomes
    "The runner finished round %d on %s";
  - `internal/relevo/reconcile.go:~335,392`: "Builder finished round %d" becomes "The
    runner finished round %d".
- **Check before editing:** reconcile.go:~392 builds a `prefix` from this text. Find
  every place that **matches** these payload strings (prefix or contains checks, dedupe,
  ingest).
  - If any code matches a stored old payload, such as a log entry written by an older
    binary, the match must accept **both** the old and the new text.
  - Add a test for the old text.
  - If you cannot tell whether stored text is matched, stop and report.
- Port every test and golden asserting these strings (`wait-*.golden`,
  `show-log*.golden` if they carry them, and the unit tests), and list each one.

## 4. Plugin and docs

- `claude-plugin/.claude-plugin/plugin.json` description: "Planner/runner handoff:
  runner reports and NEEDS YOU pushed into this session; status/send/done as tools".
  Run `sh scripts/check-plugin-version.sh`. If it demands a version bump for a
  description change, stop and report; do not bump.
- `README.md` and `docs/design.md`: only the **flag and verb** changes of A4-1:
  - `--builder` becomes `--candidate` (on bind and send);
  - `--role` becomes `--actor` (bind and ask);
  - `config agents --role` becomes `--agent`;
  - `config init --no-roles` becomes `--no-agents`.

  Do not rewrite prose about the concept, and do not touch the JSON field docs (A4-2
  owns them). Find the sites with `grep -n -- '--builder\|--role\|--no-roles' README.md docs/design.md`.
- `CLAUDE.md`: leave it unchanged. The owner edits it.

## 5. Checks

This round runs on a laptop that cannot take race suites. Do **not** run `make check`,
`go test ./...` or `-race`. Run:
- `gofmt -l $(git ls-files '*.go')`, which must print nothing;
- `go build ./...`;
- `go vet ./internal/mcp/ ./internal/delivery/ ./internal/relevo/ ./internal/planner/ ./internal/harness/`;
- `sh scripts/check-comments.sh`;
- `sh scripts/check-filesize.sh`;
- `sh scripts/check-plugin-version.sh`;
- `sh scripts/agents-shipped.sh --check`;
- `go test -count=1 ./internal/mcp/ ./internal/delivery/ ./internal/planner/ ./internal/harness/`;
- `go test -count=1 ./internal/relevo/ -run 'Wait|Stop|Reconcile|Remote|Catchup|Deliver|Origin'`;
- `go test -count=1 ./cmd/relevo/ -run 'Contract|Wait|Show'`.

## 6. Commit

Copy this plan verbatim to `docs/plans/2026-09-26-a4-1b-planner-facing-text.md`. Commit
as **one new commit**: `feat(a4): planner- and model-facing text says runner and --candidate`.
Never amend, and never rebase.

## Report

The report covers:
- `git diff --stat`;
- every changed sentence, old and new;
- the goldens that moved;
- the payload-matching finding from §3;
- the check outputs.
