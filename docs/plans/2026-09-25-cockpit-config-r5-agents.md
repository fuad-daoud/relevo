# Cockpit config views, round 5: `:agents` (2026-09-25)

Branch `relevo/ck-config`. It already has one commit, holding rounds 1-4: the backend, `:candidates` and its form, and
`:actors`. **Amend that commit**; do not add a second one.

## 1. System overview

`:agents` lists the agents actors can run: the four relevo ships (`plan-executor`, `reviewer`, `researcher`,
`architect`) and any custom ones in the config's `agents` section.
- Each agent shows, per installed harness, the state of its definition file. The data comes from
  `harness.AgentFiles`, built in round 1.
- `enter` pushes `agents › <name>`: one row per harness, with its file path, model pin and state.
  - `e` opens that file in the user's editor. The cockpit suspends while it runs.
  - `r` resets a file the user edited, or one relevo has a newer copy of, after a red y/n confirmation.
- `d` on the list deletes a **custom** agent, after a red confirmation. It is greyed on shipped agents.
  `relevo.DeleteAgent` already refuses shipped agents and agents an actor uses; show its refusal as a notice.

**Two facts differ from the canvas boards.** Build what is true, not the board:
1. relevo installs all four shipped agents on every harness it finds, so reviewer and architect show installed on agy
   and opencode too. The board had `·` there.
2. relevo never writes custom agents to disk, so deleting one removes it from the config only. The board listed files
   being removed.

`architect` appears as a row: it is shipped. It is used by no actor, so the row is faint.

The approved boards, at 132×34, corrected as above:

**`:agents`**
```
   4 agents   1 writer   3 readers

   AGENT                                             SHAPE   SOURCE   USED BY              AGY       CLAUDE    CODEX     OPENCODE
   plan-executor                                     writer  shipped  builder              ok        ok        ok        ok
   reviewer                                          reader  shipped  reviewer             ok        ok        ok        ok
   researcher                                        reader  shipped  researcher, builder  ok        ok        ok        ok
   architect                                         reader  shipped  no actor             ok        ok        ok        ok


   researcher   shipped · reader · used by researcher, builder
   agy       ~/.gemini/config/agents/researcher.md         up to date    model inherit
   …
 ↑↓ move   enter open   d delete   …
```

**`agents › researcher`**
```
   shipped · reader · used by researcher, builder   4 harnesses

   HARNESS   FILE                                                          MODEL                           STATE
   agy       ~/.gemini/config/agents/researcher.md                         inherit                         up to date
   claude    ~/.claude/agents/researcher.md                                haiku                           your edit
 ↑↓ move   e edit in $EDITOR   r reset   esc back
```

## 2. File structure

```
internal/ui/actions.go         Actions: + AgentFiles, ResetAgentFile, AgentEditor; plannerActions + fakeActions
internal/ui/view_agents.go     NEW: agentsView (list, detail block, d)
internal/ui/view_agent.go      NEW: agentView (pushed: files table, e, r)
internal/ui/view_agents_test.go NEW
internal/ui/cmdline.go         + {"agents", "", "agent definitions per harness", false}
internal/ui/view_rounds.go     execLine: + case "agents" (the nil-Actions notice, as candidates)
internal/ui/golden_test.go     + agents-132, agent-researcher-132, agent-reset-132
docs/plans/2026-09-25-cockpit-config-r5-agents.md
```

## 3. Data structures

Actions gains:
```go
AgentFiles(agent string) ([]harness.AgentFile, error)            // harness.AgentFiles over the real install env
ResetAgentFile(ctx context.Context, kind, agent string) Result   // harness.ResetAgentFile; Text "reset <kind>'s <agent>"
AgentEditor(path string) (*exec.Cmd, error)                      // $VISUAL, else $EDITOR, else vi, split with strings.Fields, path last
```
- `plannerActions` builds the install env the way `cmd/relevo/agent.go:22` (`agentInstallEnv`) does. Move that
  constructor into `internal/relevo` or `internal/harness` if cmd-only code blocks it, and **report** where it went.
- `AgentEditor` mirrors `cmd/relevo/config.go:445` (`runEditor`) but returns the `*exec.Cmd` without running it.
- `fakeActions` scripts `files map[string][]harness.AgentFile` and records `resets [][2]string` and `edited []string`.
  Its `AgentEditor` returns `exec.Command("true")`.

View rows:
```go
type agentRow struct {
    name   string
    shape  string   // "writer" | "reader"
    source string   // "shipped" | "custom"
    usedBy []string // actors that run it, plus actors whose agent requires it (researcher: builder), sorted
    files  []harness.AgentFile // nil for a custom agent
}
```
- Row order: shipped in `actors.Shipped` table order, then custom by name.
- `usedBy`: for each actor, its agent, plus that agent's `Requires` (a shipped `Requires` from `actors.Shipped`; a
  custom agent requires nothing).

## 4. Contracts

- **`:agents` list**
  - Crumbs `{"agents"}`.
  - Context left: `   <n> agents   <w> writer(s)   <r> reader(s)`, plus `   <c> custom` when c > 0.
  - Columns: AGENT takes the rest; SHAPE 6; SOURCE 7; USED BY 19; then one 8-wide column per kind in `harness.All()`
    order, headed with the kind upper-cased.
    - A kind whose binary is not installed has no column. Derive the set from the union of `AgentFiles` results, or
      from `harness.All()` filtered by `exec.LookPath` inside plannerActions. Pick one and report it.
  - **Cell per kind:**
    - up to date: `ok`, green;
    - stale: `stale`, amber;
    - your edit: `edited`, amber;
    - missing: `·`, faint;
    - a custom agent: `·`, faint, on every kind.
  - A row with an empty `usedBy` is faint and its USED BY reads `no actor`.
  - Narrow widths: drop USED BY, then SOURCE, while AGENT would be under 14.
  - Detail block: two blanks, then `<name>   <source> · <shape> · used by <…>` (or `· no actor uses it`). Then one line per
    kind: kind (10, muted), path with `$HOME` shown as `~` (46), state (14, muted), then `model <pin>` when set.
    - A custom agent with Native entries lists `<kind>  <path of the native agent>` via `harness.DefinitionPath`.
    - A source agent says `relevo does not install custom agents yet` (muted).
  - **Keys:**
    - `↑↓`/home/end move.
    - `enter` pushes agentView.
    - `d` deletes:
      - Shipped: a greyed key; pressing it gives `notice("<name> ships with relevo; it can't be deleted")`.
      - Custom: `relevo.DeleteAgent(doc, name)`. An error gives a notice. Otherwise open a danger `confirmBox`,
        `Delete agent <name>?`, with lines `used by` + `no actor` and `removes` + `the agent from relevo's config`.
        `y` → `runAction(ctx, "delete agent", name, ApplyConfig(edit))`.
    - The footer shows `d delete` faint when the cursor row is shipped. Pass a disabled flag the way round 3's
      `formKeys` does, if the footer supports it. If not, list the key normally and report that.
  - Data: on build and on every `statusMsg`, load the doc (ConfigDoc) and each shipped agent's files (AgentFiles). File
    reads are cheap, so do them in the load command, not in `Body`.
- **`agents › <name>` (agentView)**
  - Crumbs `{name}`.
  - Context left: `<source> · <shape> · used by <…>` (`<…>` in text) `   <n>` bold ` harnesses`.
  - Columns: HARNESS 8; FILE takes the rest (the `~` form); MODEL 30; STATE 12.
  - STATE styles: up to date muted, stale amber, your edit amber, missing faint.
  - **Keys:**
    - `↑↓` move.
    - **`e`:**
      1. `cmd, err := env.Actions.AgentEditor(file.Path)`.
      2. Return `tea.ExecProcess(cmd, func(err error) tea.Msg { … })`. The callback returns a reload message on
         success, else a `noticeMsg` with the error. Copy `shellCmd` (confirm.go:715).
      3. A missing file gives `notice("<kind> has no <name> file yet")`.
    - **`r`:** enabled only when the state is `your edit` or `stale`; otherwise `notice("<kind>'s <name> is up to date")`.
      Enabled, it opens a danger confirmBox, `kind:"reset"`, titled `Reset <kind>'s <name>?`. Lines:
      - `overwrites` + the `~` path;
      - blank label + `with the copy this relevo ships; your edit is lost`, or `…; relevo has a newer copy` when stale.

      `y` → `runAction(ctx, "reset agent", kind+"/"+name, ResetAgentFile(ctx, kind, name))`, then a reload.
    - A custom agent's view has no `r`. Its `e` opens the native path.
    - The footer's `r reset` is faint unless enabled for the cursor row.

## 5. Pseudocode

```
agentView.Update:
  agentFilesMsg → v.files = msg.files
  statusMsg     → reload
  e → path → ExecProcess(editor, onExit → agentFilesReload)
  r → state ∈ {edited, stale} ? confirmBox(danger) : notice
```

## 6. Error handling

- An AgentFiles error renders in the body, like candidates' doc error.
- An editor error, a reset failure or a delete refusal is a notice (red via finishAction for actions).

## 7. Working efficiently

- **Read in one batch:**
  - `internal/ui/view_candidates.go`, `view_actors.go`, `view_actor.go`, `form_rows.go`, `actions.go`,
    `confirm.go:700-730`
  - `internal/harness/agentfiles.go`, `definition.go`, `install.go:340-380`
  - `cmd/relevo/agent.go:15-40`, `cmd/relevo/config.go:440-470`
  - `internal/actors/shipped.go`
- **Focused tests** (local; `make check` is refused on this machine by a hook):
  `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -run 'Agent|Golden|Cmdline' -count=1`
- **CI rule:** no test may run a real editor or touch `$HOME`. Use `fakeActions` only. Any test that needs an install env
  uses the round 1 fake, not `OSInstallEnvKV`.
- **Goldens:** `-run TestGoldenViews -update`. Only the three new files, `cmdline-open` and `command-real-132` may
  change.
- **Final:** `go vet ./internal/... ./cmd/...`, `test -z "$(gofmt -l $(git ls-files '*.go'))"`, `go mod tidy -diff` and
  `sh scripts/check-name.sh`.

## 8. Steps

1. **Actions:** the three methods, on plannerActions and fakeActions.
2. `view_agents.go`, and register the `agents` command.
3. `view_agent.go`.
4. **Goldens** (132×34). The fake files for all four agents × four kinds are `up to date`, with pins:
   - researcher: agy `inherit`, claude `haiku`, codex `gpt-5.6-luna`, opencode `openrouter/z-ai/glm-5.3-flash`;
   - reviewer: claude `opus`;
   - plan-executor: agy `inherit`.

   Cases:
   - `agents-132`: cursor on researcher;
   - `agent-researcher-132`: enter on researcher, cursor on claude;
   - `agent-reset-132`: claude researcher scripted `your edit`, `r` pressed.
5. **Unit tests:**
   - usedBy for researcher = `[builder researcher]`;
   - architect row faint;
   - `d` on researcher → the notice;
   - a custom native agent in the doc: `d` → confirm → `y` → `configEdits[0].Message == "delete agent <name>"`;
   - a custom agent used by an actor → the "used by" notice;
   - `r` on an up-to-date file → notice, no reset;
   - `r` → `y` on an edited file → `resets == [["claude","researcher"]]`;
   - `e` → `fakeActions.edited` holds the path.

   **Mutation (required):** enable `r` for `up to date`, and a named test must fail. Restore it.
6. **Final checks.** Copy this plan, `git add -A`, `git commit --amend --no-edit`.

## 9. Stop rather than improvise

If `agentInstallEnv` cannot move without touching unrelated code, if `harness.AgentFiles` returns kinds that are not
installed, or if a round 1-4 name differs, report it; halt only if the design cannot be met.
