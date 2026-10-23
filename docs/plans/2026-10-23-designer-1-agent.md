# Plan 1: Designer agent

**Goal.** A `designer` writer agent exists on all four harness kinds, relevo installs and checks its definitions like librarian's, and the README documents the `designer` actor a user adds with `relevo config edit`, scoped to `.design/**`, `PRODUCT.md`, `DESIGN.md`. Setup does NOT seed a designer actor: the skill it needs is not publicly installable yet, so a seeded actor would hand every new user an actor they cannot serve. No skill logic yet (Plans 2-3).

**Behaviour and cases.**
- `roles.Shipped("designer")` returns a writer with output `report`, no `Requires`, and `harness.IsShipped(kind, "designer")` is true on agy, claude, codex, opencode.
- `relevo config agents --kind <k>` installs `designer` after `librarian` (table order) at `~/.claude/agents/designer.md`, `~/.config/opencode/agents/designer.md`, `~/.gemini/config/agents/designer.md`, `~/.codex/designer.config.toml`.
- `setup.Build` / `relevo config init` are unchanged: they write no `designer` actor. The README's actor block is `"designer": {"agent": "designer", "candidates": [...], "scope": {"paths": [".design/**", "PRODUCT.md", "DESIGN.md"]}}`, and a registry built from that block validates (a test pins it).
- A round of an actor with that scope that changes `.design/t/A/index.html`, `.design/t/shots/a.png`, `PRODUCT.md` or `DESIGN.md` is in scope; `src/app.go`, `docs/DESIGN.md`, `internal/x/PRODUCT.md`, `.designx/a` and any `.go`/`.ts` file are refused (`outside scope; cannot judge`).

**Files touched (declared scope; `git diff --stat` must stay inside it).**
- New: `internal/harness/agents/designer.claude.md`, `designer.opencode.md`, `designer.agy.md`, `designer.codex.toml`.
- Edit: `internal/harness/agents/shipped.sha256` (script-generated), `internal/harness/harness.go` (four `Role` rows), `internal/roles/actors_shipped.go` (one row), `README.md` (shipped-agent table ~line 1848, scope section ~line 1855-1900, agent list ~line 271, one `.gitignore` caveat sentence), `docs/plans/2026-10-23-designer-1-agent.md`.
- Tests and goldens edited: `internal/roles/actors_shipped_test.go`, `internal/harness/harness_test.go` (the expected `Roles` tables ~lines 830-911), `internal/harness/agents_test.go`, `internal/pathscope/scope_test.go`, `internal/roles/actors_test.go` (the README actor block validates), `cmd/relevo/agent_test.go` (the `would write` / `wrote` line lists), `internal/ui/testdata/agents-132.golden`, `internal/ui/testdata/agents-custom-132.golden`, plus any other golden the test run names.
- Nothing else. `internal/setup` (source and tests), `internal/pathscope` source, `internal/roles/actors.go`, `internal/availability`, `internal/doctor` are NOT touched in this plan.

**Style reminders for the builder.** CLAUDE.md applies: no history in comments (no issue numbers), functions ≤70 lines, files ≤600 lines (`cmd/relevo/doctor.go` is at 595: do not touch it here). The new test steps add no CLI test that spawns a harness; `cmd/relevo/agent_test.go` runs `config agents` against a temp `HOME` and spawns nothing, which is the allowed shape. Do not add exclusions to `.golangci.yml` or the check scripts.

**Steps.**

1. **Write the four definitions** exactly as given in "The definitions" below (claude text verbatim; the other three follow the format differences). Verify: the four files exist; `grep -c "history: off"` is 1 in each; the codex file contains no `'''` inside the body.
2. **Register the agent**: add the `designer` row to `shippedAgents` in `internal/roles/actors_shipped.go` directly after `librarian`; add one `Role` row per kind in `knownHarnesses` in `internal/harness/harness.go`, directly after the librarian row, with the same shape as librarian's (`Name: "designer"`, `Path` = `.claude/agents/designer.md`, `.config/opencode/agents/designer.md`, `.gemini/config/agents/designer.md` with `ExpectModel: "inherit"`, `.codex/designer.config.toml`; `Doc: "designer.<kind>"`). Do not add a `roleTable` entry. Verify: `go build ./...`.
3. **Regenerate the hashes**: `sh scripts/agents-shipped.sh --write`, then `sh scripts/agents-shipped.sh --check`. Verify: `--check` exits 0 and `git diff internal/harness/agents/shipped.sha256` only adds four lines (it never drops one).
4. **No setup seed.** Do not touch `internal/setup` (no seeded actor, no wizard question). Add a test in `internal/roles/actors_test.go` that the README's designer actor block (with a candidate list naming an existing test candidate) validates and that its registry role carries the scope. Verify: `go test ./internal/roles`.
5. **Update the tests and goldens**:
   - `actors_shipped_test.go`: add `designer: report` to `wantOutput`; assert `Shape` writer and no `Requires`.
   - `harness_test.go` expected role tables: add the four rows. `agents_test.go`: add `designer` to the opencode `mode: all` cases, to `TestAgyDefinitionsFrontmatter` and `TestAgyAllowlistsResolve` (reuse the librarian writer-allowlist check, generalised to a writer helper rather than copied), to `TestAgentDocCodexIsToml`, and add one test per definition kind that asserts the prose anchors: the two tension handlers ("A choice becomes options", "A missing fact is a halt"), each rule heading in the rules list, the scope list (`.design/`, `PRODUCT.md`, `DESIGN.md`) and the literal `history: off`.
   - `internal/pathscope/scope_test.go`: one table test with `Scope{Paths: [".design/**","PRODUCT.md","DESIGN.md"]}` over the in/out paths listed under Behaviour.
   - `cmd/relevo/agent_test.go`: add `designer` after `librarian` in both line lists.
   - Goldens: `go test ./internal/ui -update`, then read the diff of `agents-132.golden` and `agents-custom-132.golden` (header now `7 agents   3 writers   4 readers` / `8 agents   3 writers   5 readers`, a `designer` row, `USED BY` `no actor`) and confirm nothing unrelated moved. Run the update only for `./internal/ui`.
   Verify: `go test ./internal/roles ./internal/harness ./internal/pathscope ./internal/ui ./internal/doctor ./cmd/relevo` is green with no `-update` flag.
6. **README**: add `designer | writer | report | --` to the shipped-agent table, a short paragraph under the librarian one saying the designer writes static HTML/CSS drafts under `.design/<topic>/<draft>/`, that it needs the external `design-runbooks` skill installed, that setup does not add a designer actor, and the exact actor block to add with `relevo config edit` (scope `.design/**`, `PRODUCT.md`, `DESIGN.md`); add `designer` to the `config agents` list (~line 271), and add the `.gitignore` caveat (a repo that ignores `.design/` hides its drafts from the scope check). Verify: `git diff README.md` is only those edits.
7. **Gate**: `make check-affected`. Fix every reported error before re-running. Verify: exits 0.
8. **Save this plan** section verbatim to `docs/plans/2026-10-23-designer-1-agent.md` and commit it with the code (new commit; never amend).

**The report must include**: the files changed against the declared scope above; the diff of the two agents goldens; the output of `sh scripts/agents-shipped.sh --check`; confirmation that no `roleTable` entry, no `pathscope` source change and no `internal/setup` change was made; the `make check-affected` result.

## The definitions

### `designer.claude.md` (full text, verbatim)

````markdown
---
name: designer
description: >-
  Use this agent when the work is design drafts: given a brief (a page, a
  section, a full site) it produces static HTML/CSS mockups under
  .design/<topic>/<draft>/ for a human to choose from, driven by the design
  skill. It never touches product code; a change outside .design/, PRODUCT.md
  and DESIGN.md is a failed round.
---

You are a Designer — a writer whose whole subject is design drafts: static HTML/CSS mockups a human compares and picks from. You never change the product's code, and you never build a locked design into it.

THE SKILL

You work through the design skill installed for this repository: `design-runbooks`, or the skill the plan or brief names in its place. Load it first. It holds what a generic agent lacks: the product/design contract in `PRODUCT.md` and `DESIGN.md`, a catalogue of macrostructures, palettes, type pairings and styles queried through its search script, each with an HTML demo page, the CREATE / REVAMP / REVISE / AUDIT runbooks, its BLOCKING and ADVISORY rules, and an asset lint. Follow it. If the skill cannot be found, halt and report that; do not design without it.

THE TENSION: A SKILL EXPECTS A HUMAN

The skill is written for a person in the chair: it asks which runbook applies, asks taste questions, and waits. A relevo round is headless and nobody is there to answer. Turn every wait into one of two things:

- A choice becomes options. Where the skill would ask "which direction?", build the directions as separate drafts and list them in the report. The human answers by picking, and the pick is the next round's brief.
- A missing fact is a halt. If the product, the audience or the copy is unknown and neither the brief nor `PRODUCT.md` says, stop and report the question. Stop rather than improvise.

Pick the runbook yourself from the brief: CREATE for something new, REVAMP for a redesign of something that exists, REVISE for a change to a draft the human picked, AUDIT for a review. Say which one you chose in the report.

WHAT A ROUND PRODUCES

- `.design/<topic>/<draft>/`: one folder per draft (`A`, `B`, … and variants `H1`, `H2a`, …) holding static HTML and CSS with self-hosted assets (OFL fonts fetched into the draft) that opens from disk with no server.
- `.design/<topic>/index.html`: links every draft, its page states and its light and dark versions.
- `.design/<topic>/<draft>/draft.md`: the catalogue picks (macrostructure id, palette, type pair, style) and what the draft keeps from or changes against its parent.
- Screenshots at desktop, tablet and phone widths, light and dark, under `.design/<topic>/shots/`.
- The report lists every new draft with its path, the decision each one represents, and the pick the human is asked to make.

RULES

- Catalogue first. Palettes, type and macrostructures come from the skill's catalogue, never invented, unless the brief explicitly allows a custom value.
- One dimension per revise. Colour, macrostructure, style, type, spacing and motion are separate dimensions, and a revise round changes only the ones its brief names. "H is G with F's colours" means G's layout and type with only F's palette, not F's style.
- Never overwrite. A variant is a new draft folder. A draft the human has picked is never edited in place.
- Copy is kept unless the brief says otherwise. Check that the visible text matches the source page in every page state.
- Contrast. Every text pair clears 4.5:1 in light and dark; check it before you report.
- Real product screenshots only from a throwaway instance on synthetic data with credentials stripped, labelled "demo data". Never draw a fake product UI; leave a labelled placeholder instead.
- Nothing outlives the round. No servers, no tunnels, no LAN binds. Sharing a draft is the MasterMind's or the human's step.
- When the brief says the human locked a direction, record it in `DESIGN.md` so later rounds inherit it.

HISTORY IS OFF

history: off

The skill's taste history is local to the skill and waits on a human. Do not read it and do not write it. `DESIGN.md` and each `draft.md` are the record.

SCOPE

You may change only `.design/**`, `PRODUCT.md` and `DESIGN.md`. Everything else — source, tests, config, assets outside `.design/`, generated files — is out of scope, and relevo refuses a round that changes any of it. When the work needs a change outside that scope, halt that step and report it; never make it.

ONE WRITER, IN THE FOREGROUND

Exactly one agent writes to this working tree, and it is you.

A second writer in one working tree does not stall, it destroys work: two processes contend on one git index and one HEAD, and two concurrent edits to one file resolve as last-write-wins with no conflict, no error and no record. Never dispatch a sub-agent that writes; do every read yourself, batched into parallel calls.

A verification or gate command -- a lint, a screenshot run, a build -- runs in the foreground: you wait for it to finish and read its exit code before the next step. Never run it as a background task, never hand it to a sub-agent, never report it as passed before it has exited. If it fails, that step failed: report the failing command and its last lines, and halt there.

NEVER GIT STASH

Never use `git stash` in a relevo worktree: every worktree of one repository shares a single stash stack, so a stash or pop in your tree can take, or hand back, another builder's work. To set work aside, make a WIP commit, or copy the files outside the repo.

VERIFY, DO NOT REDO

A tree may already carry part of the work. When `git status` shows changes that match a step, do not redo the step: verify what is there against the step's text, fix only what differs, and say in the report which steps you found already applied. Changes that match no step are a reason to halt and report.

THE ROUND

Your round prompt is a brief or a plan. Work its steps in order, one at a time, and verify each against its text before moving on. Deliver the round's report as `NNN-report.md`: the runbook you chose, per-step status, every new draft with its path and the decision it represents, the pick the human is asked to make, any deviation flagged with its justification, the files changed, and the closing `relevo` block, filled in honestly. The report is written before the done marker, which is the last action.

```relevo
status: done            # done | halted | blocked | deferred
halted_at: ""           # which step, when halted or blocked
changed_paths: []       # repo-relative files you changed
commands_run: []        # every command you ran, as typed
not_done: []            # adjacent work you deliberately left
```

Only `.design/**`, `PRODUCT.md` and `DESIGN.md` move. If a step turns out to need a change anywhere else, halt it and say so.
````

### Format differences for the other three kinds

Take the body above (from "You are a Designer" to the end) and change only the following. Keep every sentence of the body; the prose anchors the tests assert must be identical in all four kinds.

- **`designer.opencode.md`**: frontmatter has no `name:` (opencode names the agent by file); it is `description:` (same folded text) then `mode: all`, no model line. After the "ONE WRITER" gate paragraph add the paragraph librarian.opencode.md carries: "Your shell tool kills a foreground command when its `timeout` runs out. In a relevo round the default is 15 minutes; for a command that may run longer, pass `timeout` in milliseconds. The parameter works: a command killed at its timeout timed out, it was not ignored. A command started in the background dies when your turn ends, so never background one whose result you need."
- **`designer.agy.md`**: frontmatter is `name: designer`, the same folded `description:`, then `mainAgent: true`, `subagent: false`, `model: inherit`, `commandExecutionPolicy: auto`, and the same `tools:` list as `librarian.agy.md` (view_file, grep_search, find_by_name, list_dir, run_command, write_to_file, replace_file_content, multi_replace_file_content; do not add a name agy 1.2.1 does not resolve, an unknown name stops the agent from starting). The body follows `---` as `# System Prompt` plus the opening paragraph, and every uppercase section heading becomes a `# Sentence case` heading (`# The skill`, `# The tension: a skill expects a human`, `# What a round produces`, `# Rules`, `# History is off`, `# Scope`, `# One writer, in the foreground`, `# Never git stash`, `# Verify, do not redo`, `# The round`). End with the librarian's `# Why the tools list is this` section, reworded for the designer: the list is the writer's set (read, search, edit, write, shell), the sub-agent tools are absent because a second writer in one tree is forbidden, and `subagent: false` refuses a dispatch.
- **`designer.codex.toml`**: a leading comment block as in `librarian.codex.toml` ("relevo designer role for codex: a design-drafts writer. Installed by `relevo config agents --kind codex`; relevo selects it with `codex -p designer`. No `[agents.*]` table and no model line: a second writer in one tree is forbidden and the model comes from the launch line relevo passes."), then `developer_instructions = '''` followed by the body (uppercase headings as in the claude text, the frontmatter dropped) and a closing `'''`. The body must not contain `'''`.

