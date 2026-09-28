```relevo-plan
# Plan: issue #645 — the live docs and two ui comments follow the prompt vocabulary

Round type: docs + comments. No code path, no test, no golden, no fixture, no
command output, no flag changes. #633 landed the vocabulary in the code and in
part of the docs; this round removes the retired spellings a grep still finds in
`README.md`, `docs/design.md`, four dated specs (amendment notes only) and the
two ui comment files.

Vocabulary (authority: `docs/plans/2026-09-27-round-vocabulary.md` §1, §8): a
round's input is its **prompt** — `NNN-prompt.md`, log kind `prompt`,
`relevo show --prompt`, the pane's `prompt` tab, statusline `prompt sent` /
`no prompt yet`. A reader round's artifact is its **output** label —
`plan.md` (architect/planner), `findings.md`, `notes.md` — with `summary.md`
only as the old read fallback. The word **plan** stays wherever a plan is what
it names: `docs/plans/`, `planner`/`lite-planner`, `plan-executor`, the `plan`
usage lane (`cost.plan`), `--mode plan` / `--permission-mode plan`, a
MasterMind's own `./plan.md` example file, "hand it the plan", `RetryPlan` and
the send picker's field.

## Seed note, surfaced not guessed around

Scope bullet 9 ("do not rewrite the dated bodies") and the verify sentence
("retired spellings ... only inside the amendment notes in specs") cannot both
hold literally: if the bodies keep their words, the retired spellings remain in
those bodies. The issue's Ask is the tie-breaker — a dated spec takes an
amendment note, README and design.md read as the current vocabulary. This plan
therefore verifies: retired spellings gone from README/design.md; the **new**
words (`prompt sent` / `no prompt yet`) present in the four specs only inside
the notes; every spec diff a pure insertion (0 deleted lines).

## Cases

1. README: a reader meets no retired input word; 293 stops naming one thing
   twice (`no plan staged ... no prompt`); 345 says exit 6 is
   `no prompt entry`; 1784-1785 stages `q.md` as the round's **prompt**, the
   final message is the round's **output** at `NNN-<actor>/<label>.md`; the
   label examples and `summary.md`-as-old-fallback sentence (1772-1775) stay
   byte-identical, and `summary.md` occurs once in README.
2. `docs/design.md` log-entry block: kind `"prompt"`; the nearby
   `MasterMind writes ./plan.md, then runs relevo send --file ./plan.md`
   example (198) stays — that is the plan the MasterMind writes.
3. The four dated specs: bodies untouched; each header gains the same dated
   note naming the vocabulary plan and the current words; the note is the only
   place `prompt sent` / `no prompt yet` appear.
4. Code comments: a cockpit reader sees the tabs named `prompt, artifacts,
   log, transcript` (reader) and `prompt, report, transcript, diff, log`
   (writer).
5. Non-cases: no CLI text, no statusline word, no plugin fixture, no prompt
   text, no test or golden — those are #642 and #644.

## Seams (HEAD of this tree; if a line moved, find the quoted text)

README.md, 18 sites, one word each (line: old → new):

| line | old | new |
| --- | --- | --- |
| 293 | `no plan staged, no log entry, no prompt, no process` | `no prompt staged, no log entry, no process` |
| 345 | `the round has no plan entry` | `the round has no prompt entry` |
| 604 | `beside the round's plan and` | `beside the round's prompt and` |
| 752 | `one round's plan, report, diff, drift, log or transcript` | `one round's prompt, report, ...` |
| 765 | `alongside the plan` | `alongside the prompt` |
| 1149 | `the last plan, report or question crossed` | `the last prompt, report or question crossed` |
| 1429 | `send` hands it a plan.` | `send` hands it the round's prompt.` |
| 1453-1454 | `the plan` / `path, the report path` | `the prompt` / `path, the report path` |
| 1531 | `the **same** round's plan.` | `the **same** round's prompt.` |
| 1659 | `next to the round's plan and report` | `next to the round's prompt and report` |
| 1708 | `names the round's plan, report, diff and gate log` | `names the round's prompt, report, ...` |
| 1745-1746 | `Its plan` / `file is written for the builder` | `Its prompt` / `is written for the builder` |
| 1750-1751 | `the new round's plan` / `entry is logged` | `the new round's prompt` / `entry is logged` |
| 1784 | `as the round's plan and runs the reader headless` | `as the round's prompt and runs the reader headless` |
| 1785 | `becomes the round's report at NNN-<actor>/<label>.md` | `becomes the round's output at NNN-<actor>/<label>.md` |
| 2135 | `relevo stages plans and reports under` | `relevo stages prompts and reports under` |
| 2171-2172 | `re-send the current plan` / `at the staged plan` | `re-send the current prompt` / `at the staged prompt` |
| 2182 | `send` the staged plan again` | `send` the staged prompt again` |

Reflow only the wrap of a changed clause; keep every other word, backtick and
em dash. Do not touch: the `plan/report handoff` tagline (8, 11), `hand it the
plan` (247), the dry-run sample `staged from ./plan.md` (302), `re-send the
plan` (1626), file names `plan.md`/`ui_plan.md`/`api_plan.md`, agent and actor
names, the `plan` cost lane (1878, 1909), `--mode plan` (1579-1580, 1799), the
planner sections (1833-1844, 1974-1978).

docs/design.md: 187 only — `kind: "plan"|` → `kind: "prompt"|` in the
`{ ts, round, direction, kind, path, ... }` block. 183-212 holds no other
retired word; 198's `./plan.md` example and the 289 sample row stay (the plan
to report back is not this round's).

The four specs — insert one identical note as the last paragraph of the header
block, immediately before the first `##`, after: 2026-09-13 line 17,
2026-09-14 line 11, 2026-09-24-statusline-redesign line 9,
2026-09-24-opencode-tui-plugin line 6. Note text, 4 lines, verbatim:

```
**Amended 2026-09-28 by `docs/plans/2026-09-27-round-vocabulary.md`:** a
round's input is now its prompt; the current words are `prompt sent` and
`no prompt yet`, and the log kind is `prompt`. The sections below stay as
written.
```

Bodies untouched; no line of a body moves (pure insertion).

Code comments, comments only:

- `internal/ui/round_pane.go:34-35` — `// plan, artifacts, log and transcript
  (round 5b)` → `// prompt, artifacts, log and transcript (round 5b)`.
- `internal/ui/round_pane.go:49-51` — reader list `plan, artifacts, log and
  transcript` and writer list `today's plan, report, transcript, diff and log`
  → `prompt` in both.
- `internal/ui/round_pane.go:320-321` — `(#183): plan, report, terminal and log`
  → `prompt, report, terminal and log` (keep `(#183)`; the file is
  `scripts/check-comments.allow`-listed).
- `internal/ui/round_head.go:85-86` — `its own tabs: plan, artifacts N, log and
  transcript` → `prompt, ...`.

Deliberately not edited (name them in the report, do not touch): the same class
in `internal/ui/fetch.go:52` and `:800`, `internal/ui/markdown.go:7`; the
kind-word comments in `internal/view/status.go:40-42,98,245` (#642's subject);
`docs/design.md:289`.

New file: `docs/plans/2026-09-28-docs-prompt-vocabulary.md` — this plan, fenced
in a leading ```relevo-plan` and closing fence like
`docs/plans/2026-09-27-round-vocabulary.md`, with the trailing relevo block
dropped.

## Ordered steps

1. **README.md, all 18 sites, one edit call.** Deliverable: the table applied,
   no other byte moved. Verify: `git grep -n -E 'no plan staged|no plan entry|
   plan sent|no plan yet|plan entry|round.s plan|staged plan|current plan|
   last plan|plans and reports|alongside the plan|the plan path|hands it a plan|
   Its plan|kind: "plan"' -- README.md docs/design.md` prints nothing (exit 1)
   after step 2, and `git diff -- README.md` shows only the table's words.
2. **docs/design.md:187, one edit call.** Deliverable: `kind: "prompt"|`.
   Verify: `git grep -n 'kind: "plan"' -- docs/design.md` prints nothing.
3. **Four spec notes, one edit call per file.** Deliverable: the note above,
   once per file, before the first `##`. Verify:
   `git diff --numstat -- docs/specs/2026-09-13-statusline-design.md
   docs/specs/2026-09-14-statusline-edges-design.md
   docs/specs/2026-09-24-statusline-redesign-design.md
   docs/specs/2026-09-24-opencode-tui-plugin-design.md` is `4 0` per file, and
   `git grep -c -E 'prompt sent|no prompt yet'` is `1` per file.
4. **Two ui comment files, one edit call per file.** Deliverable: the four
   comment sites above. Verify: `git grep -n 'plan'
   -- internal/ui/round_pane.go internal/ui/round_head.go` prints nothing;
   `gofmt -l internal/ui/round_pane.go internal/ui/round_head.go` prints
   nothing.
5. **Full check.** Deliverable: `make check` green with the docs and comments in
   the tree (its gofmt, vet, golangci-lint, comment, filesize, tidy, name and
   script legs all still see the change); `make e2e` is not needed — no code
   path moved — say that in the report.
6. **Plan + one commit.** Copy this plan to
   `docs/plans/2026-09-28-docs-prompt-vocabulary.md` (fence as above), one
   commit with subject `docs: the live docs and two comments follow the prompt
   vocabulary` and body `Plan: docs/plans/2026-09-28-docs-prompt-vocabulary.md`.
   Verify: `git status --porcelain` empty; `git show --stat HEAD` lists exactly
   README.md, docs/design.md, the four specs, the two ui files and the plan.

## Closed list of what this round removes

Nothing behavioural is deleted: no code path, no test, no golden, no fixture, no
spec body, no file. The closed list of wordings that stop appearing:

1. README `no plan staged` (and its duplicate `no prompt` clause).
2. README `no plan entry` → `no prompt entry`.
3. README `the round's plan` / `the **same** round's plan` → `... prompt`.
4. README `the staged plan` / `the current plan` → `the staged prompt` /
   `the current prompt`.
5. README `the last plan` → `the last prompt`; `alongside the plan` →
   `alongside the prompt`.
6. README `hands it a plan` → `hands it the round's prompt`; `the plan path` →
   `the prompt path`; `Its plan file is written` → `Its prompt is written`;
   `the new round's plan entry` → `the new round's prompt entry`.
7. README `stages plans and reports` → `stages prompts and reports`.
8. README `as the round's plan` → `as the round's prompt`; `the round's report
   at NNN-<actor>/<label>.md` (a reader's artifact) → `the round's output at`.
9. `docs/design.md` `kind: "plan"` → `kind: "prompt"`.
10. The four ~`plan`~ tab/prompt words in the two ui comments.

Every other `plan` in README/design.md stays, because a plan is what it names
(listed under Seams and in the report).

## Report must include

- `git diff --stat` and the file list; the commit hash and subject.
- Step 1's grep output (empty) and the per-spec `numstat` (`4 0` each) and
  `git grep -c` (`1` each); `git grep -c 'summary.md' README.md` = 1; the
  step 4 greps (empty).
- `make check`'s result and the statement that `make e2e` was not run because no
  code path changed.
- The seed note above: how the grep verification is read and why.
- The deliberately left sites and retained `plan` uses, one line each with the
  reason (`fetch.go:52,800`, `markdown.go:7`, `internal/view/status.go`,
  `docs/design.md:289`).
- Anything found that this plan's list does not cover.

Artifact: `docs/plans/2026-09-28-docs-prompt-vocabulary.md` plus the doc and
comment edits in one commit. Riskiest step: step 1 — deciding which README
`plan` uses are the round's input and which are a plan the MasterMind wrote;
the kept list above is the fence.
```
