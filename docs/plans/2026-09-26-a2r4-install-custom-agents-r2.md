# A2 round 4, part 2: the cockpit shows and resets custom agents' files

## 1. System Overview

Part 1, the commit before this one on this branch, made relevo render and install
**source** custom agents:
- `relevo.CustomAgentFiles`, `relevo.ResetCustomAgentFile`, `relevo.IsSourceAgent` and
  `relevo.MachineConfig` are in internal/relevo/customagents.go and installenv.go;
- `harness.CustomFiles` is in internal/harness/custom.go.

The cockpit still treats every custom agent as unmanaged:
- `:agents` shows `·` in every kind cell;
- the detail block says "relevo does not install custom agents yet";
- `:agents › <custom>` lists convention paths with no state and no `r reset`.

This round makes a **source** custom agent behave like a shipped one in both views: per-kind
states, the file lines in the detail block, and `r` to reset a file. The approved design
is the canvas board ":agents D2 · a custom agent (illustration)", shown as text in §5.

**Native** custom agents are unchanged: convention paths, no state, no reset.

It also fixes one part-1 flaw in `relevo config agents`. `--role` naming a **shipped**
agent also installed every custom agent. With any `--role`, only that agent is installed.

**If a step is impossible as written or contradicts the code, stop and report. Do not
improvise, and do not bend a test to fit.**

## 2. File Structure

```
cmd/relevo/agent.go            EDIT  skip the custom walk when --role names a shipped agent
internal/ui/actions.go         EDIT  AgentFiles and ResetAgentFile route source custom agents (lines ~498-525)
internal/ui/view_agents.go     EDIT  agentRows gives a source custom agent its files (lines 106-120); agentDetailLines (lines ~375-396)
internal/ui/view_agent.go      EDIT  rows(), Keys(), updateKey's r, resetCmd wording, the edit+newer detail wording
internal/ui/golden_test.go     EDIT  one golden case, agents-custom-132, plus its fixture
internal/ui/view_agents_test.go EDIT  tests (§7 step 3); port TestAgentViewCustomHasNoReset (line ~334) to a native agent
internal/ui/testdata/agents-custom-132.golden   NEW (generated)
docs/plans/2026-09-26-a2r4-install-custom-agents-r2.md   NEW (last step): this plan, verbatim
```

## 3. Contracts

### Actions (internal/ui/actions.go)

- `plannerActions.AgentFiles(agent)`:
  - keep `harness.AgentFiles(env, agent)`;
  - when it answers `nil, nil` (every non-shipped name), return
    `relevo.CustomAgentFiles(a.runtime().Config, env, agent)`. That call answers nil for
    a native or unknown name.
  - Use `a.runtime().Config` (the cockpit's live config), **not** `MachineConfig`.
  - Update the doc comment: it no longer says custom agents have no state.
- `plannerActions.ResetAgentFile(ctx, kind, agent)`:
  - when `a.runtime().Config` is non-nil, its `Load()` succeeds, and
    `relevo.IsSourceAgent(loaded.Agents, agent)`: call
    `relevo.ResetCustomAgentFile(cfg, env, kind, agent)`;
  - otherwise keep `harness.ResetAgentFile` as today;
  - the success text is unchanged.

### `:agents` (internal/ui/view_agents.go)

- `agentRows`: a custom row whose doc entry is a source agent
  (`relevo.IsSourceAgent(doc.Agents, name)`) gets `r.files = files[name]`, as shipped
  rows do. A native custom row keeps nil files.
- `agentStateCell` needs no change: with files, it gives ok, stale or edited, and a
  missing file stays `·`.
- `agentDetailLines`: a source custom agent lists its files exactly as a shipped agent
  does (the loop at the end of the function). The "relevo does not install custom agents
  yet" line goes. A native agent keeps its `customAgentRows` lines.
- The kind columns are the union of loaded file states (view_agents.go:160-174), so they
  now include a source custom agent's kinds with no change.

### `:agents › <agent>` (internal/ui/view_agent.go)

- `rows()`: use `v.files` for shipped agents **and** source custom agents, and
  `customAgentRows` only for native ones. Test with
  `relevo.IsSourceAgent(v.doc.Agents, v.name)`.
- `Keys()` and `updateKey`'s `r`: allowed for shipped **and** source custom agents.
- Wording where a source custom agent's text differs: every place that says
  "this relevo ships" or "the copy this relevo ships" says instead
  "relevo renders from your config". That covers:
  - the three `resetCmd` notes (view_agent.go:~330-340);
  - the edit + newer detail line: "you edited this file, and relevo renders a newer copy
    from your config".

  Shipped agents keep today's words exactly. Build the text with one helper,
  `copyPhrase(custom bool)`, not with duplicated branches.

### `relevo config agents` (cmd/relevo/agent.go)

In the non-source branch, run the custom walk **only when `*role == ""`**. With a shipped
`--role`, only that shipped agent is installed.

## 4. Pseudocode

```
AgentFiles(agent):  fs = harness.AgentFiles(env, agent); if fs == nil -> relevo.CustomAgentFiles(live cfg, env, agent)
ResetAgentFile(kind, agent): source agent? -> relevo.ResetCustomAgentFile : harness.ResetAgentFile
```

## 5. The approved board, as text

```
   AGENT                SHAPE   SOURCE   USED BY              AGY  CLAUDE  CODEX  OPENCODE
   plan-executor        writer  shipped  builder              ok   ok      ok     ok
   ...
   security-reviewer    reader  custom   no actor             ok   ok      ok     ok       <- cursor


   security-reviewer   custom · reader · no actor uses it
   agy       ~/.gemini/config/agents/security-reviewer.md   up to date
   claude    ~/.claude/agents/security-reviewer.md          up to date
   codex     ~/.codex/security-reviewer.config.toml         up to date
   opencode  ~/.config/opencode/agents/security-reviewer.md up to date
```

## 6. Working Efficiently

- Read these in one batch:
  - internal/ui/actions.go 490-530;
  - internal/ui/view_agents.go (all);
  - internal/ui/view_agent.go (all);
  - internal/ui/golden_test.go 720-880;
  - internal/ui/view_agents_test.go (all);
  - internal/relevo/customagents.go;
  - cmd/relevo/agent.go 23-120;
  - internal/agentsrc/source_test.go 1-60 (a valid reader source text to copy as the
    fixture, renamed `security-reviewer`).
- Make each file's changes in one edit.
- Focused loop:
  `go build ./... && go test -count=1 ./internal/ui/ -run 'Agent|Golden' && go test -count=1 ./cmd/relevo/ -run Agent`
- Full check, once at the end, on this server:
  `export TMPDIR=$HOME/.cache/go-tmp GOTMPDIR=$HOME/.cache/go-tmp; mkdir -p $TMPDIR; make check`

## 7. Ordered Steps

**Step 1: the `--role` fix** in cmd/relevo/agent.go, per §3. Add no cmd/relevo test that
runs `relevo config agents`, because CI has no harness binaries.

**Step 2: actions and views** per §3. Depends on step 1.
- Verify: `go test -count=1 ./internal/ui/`. Every existing golden must pass unchanged.
  Their fixtures have no source custom agents, so nothing they draw may move. If one
  moves, stop and report.

**Step 3: fixture, golden and tests.** Depends on step 2.
- Fixture:
  - `candFixtureDoc`'s `Agents` (view_candidates_test.go:71) is empty. For the new cases
    only, build a doc copy with `security-reviewer` as a source agent (shape reader);
  - `fakeActions.files["security-reviewer"]` holds four `up to date` files at the four
    convention paths under the fixture's home, in `harness.All()` order.
- Golden `agents-custom-132`: the agents view with the cursor on `security-reviewer`,
  modelled on the existing agents golden at golden_test.go:~869. Generate it with
  `-update`, read it, and check it against §5. Report its full text.
- Tests:
  1. `TestAgentViewSourceCustomAgentResets`: on `:agents › security-reviewer`, `r` on a
     file opens the confirm. Its note says "relevo renders from your config". Confirming
     calls `fakeActions.ResetAgentFile(kind, "security-reviewer")`.
  2. Port `TestAgentViewCustomHasNoReset` (view_agents_test.go:~334) to a **native**
     custom agent, so it still pins "no reset for an agent relevo does not write". Say
     in the report that it was ported, not deleted.
  3. `TestAgentsDetailListsASourceCustomAgentsFiles`: the detail block has one line per
     file with its state, and no "does not install" text.
- **Required mutations.** Run each, confirm the named test fails, then restore. Report
  all three.
  - (a) `agentRows` gives no files to source custom agents: `agents-custom-132` fails.
  - (b) `Keys`/`updateKey` allow `r` only for shipped agents: test 1 fails.
  - (c) `copyPhrase` ignores `custom`: test 1 fails.

**Step 4: full check.** Run `make check` as in §6. It must pass.
- Never lower a coverage baseline.
- Add no allowlist or lint exclusion.
- Comments say why, and carry no history, "§", issue numbers or "round N".

**Step 5: ship.** Copy this plan verbatim to
`docs/plans/2026-09-26-a2r4-install-custom-agents-r2.md`. Commit as **one new commit**:
`feat(cockpit): :agents shows and resets custom agents' files`. Never amend, and never
rebase.

## Report

The report covers:
- `git diff --stat HEAD~1`;
- the mutations, each with its failing test;
- the golden text;
- the ported test;
- `make check`'s last lines;
- anything that did not match the code.
