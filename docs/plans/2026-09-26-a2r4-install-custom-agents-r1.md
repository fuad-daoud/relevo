# A2 round 4, part 1 (resend): relevo installs custom agents

Round 1 halted correctly: `internal/actors` was folded into `internal/roles` (#573). The
plan below is the round-1 plan with that fixed: `roles.AgentEntry`, in
`internal/roles/actors.go`. Nothing else changed; every other anchor was re-checked
against the current HEAD. The working tree is clean from round 1. Start at step 1.

---


## 1. System Overview

A **custom agent** is an `roles.AgentEntry` in the config's `agents` section
(internal/roles/actors.go:35-50). There are two kinds:
- a **source agent** (`Source` set): one agentsrc text that `agentsrc.Render(src, kind)`
  (internal/agentsrc/render.go:24) turns into each kind's file;
- a **native agent** (`Native` set): it points at an existing harness-native agent, and
  relevo never writes it.

Today relevo never writes a source agent's files. So every actor that uses one is gated
`roles_missing` on every kind, with a note saying "install … yourself".

This round makes relevo **render and install source agents**, using the same
manifest-based decision table it uses for shipped agents (`installBytes`,
internal/harness/install.go:255-304). It covers four surfaces:
- `relevo config agents`, including `--role <custom>`, `--kind`, `--force` and
  `--dry-run`;
- the daemon's once-per-start refresh;
- the roles-missing gate note;
- doctor's fix text.

The cockpit views come in part 2, a separate round.

Out of scope:
- **Native agents:** never written.
- **Removing files when an agent is deleted:** deleting keeps its files.
- **Carrying custom agents to a remote server:** the seed has no `agents` section.
- **Checking `Requires` names.**

**If a step is impossible as written or contradicts the code, stop and report. Do not
improvise, and do not bend a test to fit.**

## 2. File Structure

```
internal/harness/custom.go            NEW   CustomDoc, InstallCustom, CustomFiles
internal/harness/custom_test.go       NEW
internal/relevo/customagents.go       NEW   CustomAgentDocs, InstallCustomAgents, CustomAgentFiles, ResetCustomAgentFile, IsSourceAgent
internal/relevo/customagents_test.go  NEW
cmd/relevo/agent.go                   EDIT  cmdAgentInstall also installs custom agents (lines 23-78)
cmd/relevo/daemon.go                  EDIT  refreshRoles also installs custom agents (lines 241, 334-366)
internal/relevo/ledger.go             EDIT  rolesMissingNote's custom fix text (lines ~350-376)
internal/relevo/roles_gate_test.go    EDIT  port the asserted wording (lines ~82, 157-171)
internal/doctor/roledefs.go           EDIT  customRoleCheck's fix text (lines 79-98)
internal/doctor/doctor_test.go        EDIT  port TestCustomRoleRow's asserted fix string (lines ~1075-1125)
docs/plans/2026-09-26-a2r4-install-custom-agents-r1.md   NEW (last step): this plan, verbatim
```

`harness` must not import `agentsrc`, because `agentsrc` imports `harness`. So harness
takes rendered **bytes**, and `internal/relevo` does the rendering.

## 3. Data Structures

`harness.CustomDoc` (custom.go):

| field | type | meaning |
|---|---|---|
| `Kind` | string | harness kind (`claude`, `opencode`, `agy`, `codex`) |
| `Name` | string | the agent name; the file goes to `DefinitionPath(Kind, Name)` |
| `Bytes` | []byte | the rendered file |

## 4. Contracts

### harness (internal/harness/custom.go)

- `InstallCustom(env InstallEnv, opts InstallOptions, docs []CustomDoc) ([]InstallResult, error)`
  - It loads the manifest once, as `Install` does (install.go:85-88).
  - For each doc in the given order:
    - skip it when `opts.Kind != ""` and differs from `doc.Kind`;
    - skip it when `opts.Role != ""` and differs from `doc.Name`;
    - skip it when the kind is unknown (`Lookup` fails) or its binary is not on PATH
      (`env.LookPath(h.Binary)` fails), as `installKinds` does for shipped agents;
    - when `IsShipped(doc.Kind, doc.Name)`, give an `OutcomeError` result with Err
      `"<name> is a shipped agent"` and install nothing for it;
    - otherwise `homeRel, _ := DefinitionPath(doc.Kind, doc.Name)` and
      `installBytes(env, opts, InstallResult{Kind, Role: Name, Path: homeRel}, homeRel, doc.Bytes, manifest, "")`.
  - It saves the manifest once, when changed and not a dry run, with the same error
    handling as install.go:113-119.
  - It never touches shipped files.
- `CustomFiles(env InstallEnv, docs []CustomDoc) ([]AgentFile, error)`
  - This is the dry-run counterpart of `AgentFiles` (agentfiles.go:26-47):
    `InstallCustom(env, InstallOptions{DryRun: true}, docs)`, then the existing
    `agentFilesFromResults`.

### relevo (internal/relevo/customagents.go)

- `IsSourceAgent(agents map[string]roles.AgentEntry, name string) bool`: the entry
  exists and its `Source != ""`.
- `CustomAgentDocs(agents map[string]roles.AgentEntry, only string) ([]harness.CustomDoc, error)`
  - It walks the source agents in name order. Native entries and names other than
    `only` (when `only` is non-empty) are skipped.
  - For each: `agentsrc.Parse([]byte(entry.Source))`, then one doc per kind in
    `agentsrc.RenderedKinds(src)`, with bytes from `agentsrc.Render(src, kind)`.
  - A parse or render error is returned as `fmt.Errorf("agent %s: %w", name, err)`.
    The config already validated the source, so this is unexpected.
- `InstallCustomAgents(cfg *config.Store, env harness.InstallEnv, opts harness.InstallOptions) ([]harness.InstallResult, error)`
  - A nil `cfg` gives `nil, nil`.
  - Otherwise: `cfg.Load()`, then `CustomAgentDocs(loaded.Agents, opts.Role)`, then
    `harness.InstallCustom(env, opts, docs)`.
- `CustomAgentFiles(cfg *config.Store, env harness.InstallEnv, name string) ([]harness.AgentFile, error)`
  - `nil, nil` for a nil cfg or a non-source name. Otherwise
    `harness.CustomFiles(env, docs for name)`.
- `ResetCustomAgentFile(cfg *config.Store, env harness.InstallEnv, kind, name string) (harness.InstallResult, error)`
  - `InstallCustomAgents(cfg, env, InstallOptions{Kind: kind, Role: name, Force: true})`.
  - It errors `"<kind> has no <name> definition"` when no result comes back.
- How `cmd/relevo` and the UI get the config store: add
  `func MachineConfig() (*config.Store, error)` next to `AgentInstallEnv`
  (internal/relevo/installenv.go). It opens the same machine DB the same way
  (`store.DefaultRoot`, `store.New(root).DB()`) and returns `config.Open(d)`. Compose no
  path by hand (CLAUDE.md).

### `relevo config agents` (cmd/relevo/agent.go:23-78)

- When `--role` names a source agent (`IsSourceAgent` over the loaded config), skip
  `harness.Install`, because it would return `ErrUnknownRole`. Run only
  `InstallCustomAgents`.
- Otherwise, run `harness.Install` as today, then `InstallCustomAgents` with the same
  `Kind`, `Force` and `DryRun`, and `Role` left empty, since the shipped branch already
  took any shipped role.
- Print every result line with `r.Line()`, the shipped results first. The exit codes
  are unchanged: an `OutcomeError` anywhere exits 1.
- A `MachineConfig` or `Load` error is one stderr line
  `relevo: custom agents not installed: <err>` and does not change the exit code, so
  shipped installs keep working when config cannot be read.
- Update the usage text: "Installs relevo's shipped agents and the custom agents in
  your config."

### The daemon (cmd/relevo/daemon.go)

- `refreshRoles()` becomes `refreshRoles(cfg *config.Store)`, called at line 241 with
  `rt.Config`.
- After the shipped `Install`, it runs
  `relevo.InstallCustomAgents(cfg, env, harness.InstallOptions{})` and logs its results
  with the same switch at lines 353-364.
- Errors are warnings, never fatal, as today.

### Wording

- `rolesMissingNote` (ledger.go): the custom fix becomes
  `"run relevo config agents --kind <kind> for a custom agent relevo renders, or install <paths> yourself"`.
- `customRoleCheck` (roledefs.go:94): the Fix becomes
  `"run relevo config agents --kind <kind>, or install your agent definition at <homeRel>"`.
- Port the two tests' asserted strings to the new wording. Delete no test.

## 5. Pseudocode

```
relevo config agents [--kind K] [--role R] [--force] [--dry-run]
  cfg = MachineConfig()                       # error -> one stderr line; shipped still runs
  if R != "" and IsSourceAgent(cfg.agents, R):
      results = InstallCustomAgents(cfg, env, {K, R, force, dry})
  else:
      results = harness.Install(env, opts) ++ InstallCustomAgents(cfg, env, {K, "", force, dry})
  print lines; exit 1 on any OutcomeError
```

## 6. Working Efficiently

- Read these in one batch:
  - internal/harness/install.go (all);
  - internal/harness/agentfiles.go;
  - internal/harness/definition.go;
  - internal/harness/helpers_test.go 60-110 (the fake env);
  - internal/agentsrc/source.go 20-60 and 235-270;
  - internal/agentsrc/render.go 1-70;
  - internal/agentsrc/source_test.go 1-60 (valid source texts to reuse as fixtures);
  - internal/roles/actors.go 30-60;
  - internal/relevo/installenv.go;
  - cmd/relevo/agent.go;
  - cmd/relevo/daemon.go 230-366;
  - internal/relevo/ledger.go 300-380;
  - internal/relevo/roles_gate_test.go 70-175;
  - internal/doctor/roledefs.go 75-100;
  - internal/doctor/doctor_test.go 1070-1130.
- Make each file's changes in one edit.
- Focused loop:
  `go build ./... && go test -count=1 ./internal/harness/ ./internal/relevo/ -run 'Custom|RolesMissing|Gate' && go test -count=1 ./internal/doctor/ -run Custom`
- Full check, once at the end, on this server:
  `export TMPDIR=$HOME/.cache/go-tmp GOTMPDIR=$HOME/.cache/go-tmp; mkdir -p $TMPDIR; make check`

## 7. Ordered Steps

**Step 1: harness.** Write custom.go per §4, and custom_test.go with the fake env
(`freshEnv()`, internal/harness/helpers_test.go:99). Tests:
1. `TestInstallCustomWritesTheRenderedFileAtTheConventionPath`: claude on PATH; writes
   `/home/u/.claude/agents/<name>.md` with the given bytes; the manifest records its sha.
2. `TestInstallCustomKeepsIdenticalAndUpdatesItsOwnOlderCopy`:
   - a second run with the same bytes gives `OutcomeKeptIdentical`;
   - after the manifest records bytes A, with the file holding A, a run with bytes B
     gives `OutcomeUpdated`.
3. `TestInstallCustomKeepsAnEditUnlessForced`: the file holds bytes the manifest does
   not record, giving `OutcomeKeptDiffers`; with Force it gives `OutcomeOverwrote`.
4. `TestInstallCustomRefusesAShippedName`: `reviewer` on claude gives `OutcomeError`,
   and nothing is written.
5. `TestInstallCustomSkipsKindsNotOnPathAndFilters`:
   - a codex doc with no codex binary gives no result;
   - `Kind` and `Role` filters drop the other docs.
6. `TestInstallCustomDryRunWritesNothing`: no writes, and the manifest is not saved.
7. `TestCustomFilesStates`: missing, up to date and your edit, through `CustomFiles`.
- Verify: `go test -count=1 ./internal/harness/`.

**Step 2: relevo.** Write customagents.go and `MachineConfig` per §4. Depends on step 1.
Tests in customagents_test.go:
1. `TestCustomAgentDocsRendersSourceAgentsOnly`:
   - agents = one source agent whose frontmatter lists `kinds: [claude, codex]`, plus
     one native agent;
   - the docs are exactly (claude, name) and (codex, name), with bytes equal to
     `agentsrc.Render`;
   - the native agent is absent.
2. `TestCustomAgentDocsOnlyFiltersByName`.
3. `TestInstallCustomAgentsFromAStore`:
   - a real `config.Store` on a temp DB (`db.Open`, then `config.Open`);
   - put an agents section with one source agent through the store's own write path
     (`PutDoc`, or `Put`, whichever validates an agents section; read config.go);
   - a harness fake env, or a real OS env over a temp HOME; use whichever the relevo
     package's existing install tests use, and stop and report if neither is reachable;
   - assert the claude file is written.
- Verify: `go test -count=1 ./internal/relevo/ -run Custom`.

**Step 3: CLI, daemon and wording.** Edit agent.go, daemon.go, ledger.go and
roledefs.go per §4, and port the two tests' strings. Depends on step 2.
- Add **no** cmd/relevo test that runs `relevo config agents`: CI has no harness
  binaries. The pure logic is covered in internal/relevo.
- Verify: the focused loop in §6.

**Step 4: required mutations.** Run each, confirm the named test fails, then restore.
Report all four.
- (a) `InstallCustom` never saves the manifest: harness test 2 fails.
- (b) `InstallCustom` passes `Force: true` always: harness test 3 fails.
- (c) `CustomAgentDocs` includes native entries: relevo test 1 fails.
- (d) `InstallCustom` does not check `IsShipped`: harness test 4 fails.

**Step 5: full check.** Run `make check` as in §6. It must pass.
- Never lower a coverage baseline.
- Add no allowlist or lint exclusion.
- New files must pass `scripts/check-comments.sh` unlisted. Comments say why, and carry
  no history, "§", issue numbers or "round N".

**Step 6: ship.** Copy this plan verbatim to
`docs/plans/2026-09-26-a2r4-install-custom-agents-r1.md`. Commit everything as **one new
commit**: `feat(agents): relevo renders and installs custom agents`. Never amend, and
never rebase.

## Report

The report covers:
- `git diff --stat`;
- each mutation, with its failing test;
- the ported test strings, old and new;
- `make check`'s last lines;
- anything that did not match the code.
