# #586: claude candidates take an effort; config init ships planner and lite-planner

Closes #586. Commit this plan as `docs/plans/2026-09-26-planner-actors-586.md`
in the same PR (last step).

## 1. System Overview

Two changes:

1. **Effort on claude candidates.** A claude candidate's model may end in
   `:<effort>`, where effort is one of `low`, `medium`, `high`, `xhigh`, `max`
   (the `claude --effort` vocabulary; checked against Claude Code 2.1.283). The
   claude launch then renders `--model <id> --effort <effort>`. A suffix outside
   that vocabulary is **not** an effort: the model is passed verbatim, so a
   Bedrock-style id such as `anthropic.claude-x-v1:0` keeps working. Codex keeps
   its current behaviour (`SplitEffort`, unchanged).
2. **`relevo config init` seeds two planner actors** beside the builder:
   - `planner`: agent `architect`, one candidate `claude/anthropic/opus:medium`,
     written only when `claude` is on PATH.
   - `lite-planner`: agent `architect`, one candidate
     `opencode/openrouter/deepseek/deepseek-v4.1-flash`, written only when
     `opencode` is on PATH.

   Both are reader actors (architect's shipped shape) with no tier and no
   check. The builder actor keeps exactly the candidates it lists today: one
   per harness on PATH, not the planner candidates.

Candidate naming needs no change. `candidate.DeriveNames`
(`internal/candidate/names.go`, `deriveBase`) already splits on `#` or `:`
for every harness. `opus:medium` is named `opus`, and
`deepseek/deepseek-v4.1-flash` is named `deepseek-v4.1-flash`. Usage needs no
change: `claudeCarry` takes its model from the stream's init event, not from
the candidate (`internal/usage/carry.go`).

## 2. File Structure (all edits, no new source files)

```
internal/harness/harness.go        claude case of Launch; new unexported claudeEffort split
internal/harness/harness_test.go   new TestLaunchClaudeEffort
internal/setup/setup.go            planner defaults table; Plan writes three actors
internal/setup/setup_test.go       update TestPlanFindsBinariesInHarnessOrder; new planner-actor tests
cmd/relevo/init.go                 init reports every actor it wrote
cmd/relevo/init_test.go            update TestInitReportsActors
README.md                          config init example output and description (lines ~150-170, ~375)
docs/plans/2026-09-26-planner-actors-586.md   this plan
```

## 3. Data Structures

`internal/setup/setup.go`:

- `type PlannerDefault struct { Actor, Kind, Provider, Model string }`
  - `Actor`: actor name written to the actors section (`planner`, `lite-planner`).
  - `Kind`: the harness kind whose binary must be on PATH.
  - `Provider`, `Model`: the candidate's fields, as in `Default`.
- `var PlannerDefaults = []PlannerDefault{ {"planner","claude","anthropic","opus:medium"}, {"lite-planner","opencode","openrouter","deepseek/deepseek-v4.1-flash"} }`.
  It is a slice, not a map, so output order is fixed: planner, then lite-planner.
- `Files`: keep every field. Update `Actors`'s comment to say it is the builder
  actor plus the planner actors whose harness is on PATH. Add
  `ActorOrder []string`, the actor names written, in the order `builder`, then
  `PlannerDefaults` order (only those written). init prints from it.

`internal/harness/harness.go`:

- `var claudeEfforts = map[string]bool{"low":true,"medium":true,"high":true,"xhigh":true,"max":true}`
  with a one-line comment saying it is `claude --effort`'s vocabulary.

## 4. Interface Definitions & Contracts

### `func claudeEffort(model string) (id, effort string)`

In `internal/harness/harness.go`, placed directly after `SplitEffort` (~line 369).
- Splits on the **last** `:`. When the suffix is in `claudeEfforts` and the
  prefix is non-empty, it returns `(prefix, suffix)`. Otherwise it returns `(model, "")`.
- It never errors, because an unknown suffix is part of the model id.
- Its doc comment says why the vocabulary check exists: a Bedrock model id
  contains `:`.

### `Harness.Launch`, claude case (`internal/harness/harness.go` lines 270-273)

- Before: `print = {"-p", <prompt>, "--model", model, "--agent", def, "--output-format", "stream-json", "--verbose"}`.
- After: `id, effort := claudeEffort(model)`. `print` uses `"--model", id`. When
  `effort != ""`, `"--effort", effort` is inserted directly after the model
  pair and before `"--agent"`. `promptAt` stays 1.
- Postcondition: a model with no recognised effort gives exactly today's argv.

### `setup.Plan(env harness.InstallEnv) (Files, error)` (`internal/setup/setup.go` lines 35-80)

- Preconditions: unchanged. No harness on PATH is the same error as today.
- Postconditions:
  - `Candidates` holds the per-harness builder candidates, in `harness.All()` order
    as today, followed by one candidate per `PlannerDefaults` entry whose `Kind`
    is on PATH, in `PlannerDefaults` order. No candidate carries `roles` or `tier`.
  - Names come from **one** `candidate.DeriveNames` call over the full list.
  - `builder` = `{Agent:"plan-executor", Tier:"yolo", Candidates: the names of the builder candidates only}`.
  - Each written planner actor = `{Agent:"architect", Candidates: [its candidate's name]}`, with no tier or check.
  - `ActorOrder` = `["builder", <written planner actors in PlannerDefaults order>...]`.
- Keep `Plan` at 70 lines or fewer: move the actor assembly into a helper,
  e.g. `func starterActors(builderNames []string, planners []PlannerDefault, plannerNames []string) map[string]roles.Actor`.

### `cmdInit` output (`cmd/relevo/init.go` lines 71-76, and `actorNames` lines 102-111)

- Replace the single `wrote actors (builder: a, b)` line with one line that
  covers every actor in `files.ActorOrder`:
  `wrote actors (builder: sonnet, glm-5.3-flash; planner: opus; lite-planner: deepseek-v4.1-flash)`.
  Each actor's names come from parsing `files.Actors` with `roles.ParseActors`.
- Delete `actorNames` (lines 102-111). Its job moves to a new helper
  `actorSummary(actors []byte, order []string) (string, error)`, which returns the
  text inside the parentheses. Drop any import that goes unused.

## 5. Pseudocode

```
Launch(claude):
  id, effort := claudeEffort(model)
  print := [-p <prompt> --model id]
  if effort != "": print += [--effort effort]
  print += [--agent def --output-format stream-json --verbose]

Plan(env):
  builderCands := for each h in harness.All() on PATH: Defaults[h.Kind]
  if none: error (unchanged)
  planners := PlannerDefaults filtered to Kind on PATH
  all := builderCands ++ candidates(planners)
  names := DeriveNames(all)
  builderNames, plannerNames := names[:len(builderCands)], names[len(builderCands):]
  actors := starterActors(builderNames, planners, plannerNames)
  encode candidates(all), policy (unchanged), actors
  ActorOrder := ["builder"] ++ planners[i].Actor
```

## 6. Error Handling

- No new error types. `claudeEffort` does not fail. `Plan`'s errors are the
  existing marshal errors and the no-binaries error. In init, a
  `roles.ParseActors` error from `actorSummary` is returned as is. It cannot
  happen with Plan's own output, but it is not swallowed.

## 7. Working Efficiently

- Every location is named above. Read `internal/harness/harness.go` 260-370,
  `internal/setup/setup.go`, `internal/setup/setup_test.go`,
  `cmd/relevo/init.go`, `cmd/relevo/init_test.go` 100-140 and `README.md` 145-175
  and 370-380 **in one parallel batch**, then edit.
- Make each file's changes in one edit call.
- Focused loop: `go test ./internal/harness/ ./internal/setup/ ./cmd/relevo/ -run 'Launch|SplitEffort|ClaudeEffort|Plan|Init'`.
  Fix everything it reports before rerunning.
- Full check once at the end: `make check` (gofmt, vet, golangci-lint, the
  comment, file-size and coverage checks, `go mod tidy`). Also run `gofmt -l .`
  locally.
- CI has no harness binaries and no network. The init tests already use stub
  binaries on PATH (`stubBinary`). Keep it that way, and execute no real
  harness in any test.

## 8. Ordered Implementation Steps

Stop and report instead of improvising if any step contradicts the code, e.g.
`roles.ParseActors` refuses an `architect` actor named `planner` or
`lite-planner`, or the claude argv tests pin a shape this plan does not
expect.

1. **Claude effort.** Add `claudeEfforts` and `claudeEffort`, and change the
   claude case of `Launch`. Add `TestLaunchClaudeEffort` next to
   `TestLaunchCodex` (~line 427), table-driven:
   - `opus:medium` → `--model opus --effort medium` placed before `--agent`;
   - `opus:max` → `--effort max`;
   - `sonnet` → today's argv exactly, with no `--effort`;
   - `anthropic.claude-x-v1:0` → `--model anthropic.claude-x-v1:0` verbatim, no `--effort`;
   - `opus:turbo` → verbatim, no `--effort`.
   Verify: the focused test passes. Then mutation-check it: make `claudeEffort`
   always return `(model, "")` and confirm `TestLaunchClaudeEffort` fails,
   then restore.
2. **Planner defaults in `setup.Plan`.** Depends on nothing. Add
   `PlannerDefault`, `PlannerDefaults` and `Files.ActorOrder`, the Plan changes
   and `starterActors`. Update `TestPlanFindsBinariesInHarnessOrder` (claude and
   opencode on PATH):
   - there are now 4 candidates (line 42 currently wants 2);
   - the builder's candidates are the first two derived names only;
   - replace the assertion at lines 77-82.
   Add tests:
   - `TestPlanSeedsPlannerActors`: claude and opencode on PATH give `planner` →
     agent architect, candidates `["opus"]`, and `lite-planner` → agent architect,
     candidates `["deepseek-v4.1-flash"]`. Neither has a tier. `ActorOrder` is
     `[builder planner lite-planner]`.
   - `TestPlanSkipsPlannerWithoutItsHarness`: only opencode on PATH gives no
     `planner` actor, no `opus:medium` candidate, and a `lite-planner` actor.
     Only codex on PATH gives neither planner actor.
   Verify: `go test ./internal/setup/`.
3. **init output.** Depends on 2. Add `actorSummary`, delete `actorNames`, and
   print the one `wrote actors (...)` line. Update `TestInitReportsActors`
   (`cmd/relevo/init_test.go:109`) to build the expected line from the stored
   actors in the order builder, planner, lite-planner, e.g.
   `wrote actors (builder: sonnet, glm-5.3-flash; planner: opus; lite-planner: deepseek-v4.1-flash)`.
   Verify: `go test ./cmd/relevo/ -run Init`.
4. **README.** Around line 157, the init description should say that init also
   writes a `planner` and a `lite-planner` actor for claude and opencode.
   Update the example output line 167 to the new `wrote actors (...)` line.
   Update line ~375 if it lists what init seeds. In the candidates section
   (~line 1146), add one sentence: a claude model may end in
   `:low|medium|high|xhigh|max` to set `--effort`, as a codex model ends in
   `:<effort>`. Keep the "relevo ships no candidates" framing: these are
   starter candidates to edit.
5. **Full check and plan.** Run `make check`, then `gofmt -l .`. Copy this plan
   to `docs/plans/2026-09-26-planner-actors-586.md`. Commit on the round's
   branch with a message referencing #586.

## Report must include

- `git diff --stat`.
- The mutation check from step 1, and its result.
- Whether the coverage baseline moved (it should not; do not regenerate it).

---

## Round 2

### #586 round 2: lite-planner at max effort; effort documented for opencode

Round 1 is committed on this branch (`faae6219`). This round adds one small
change on top of it. Stop and report instead of improvising if any location
below does not match.

### 1. System Overview

OpenCode already takes an effort: `opencode run -m provider/model#variant`.
relevo passes the candidate's model verbatim in `-m` (`internal/harness/harness.go`,
`case "opencode"` of `Launch`), and `candidate.DeriveNames` already strips
`#<variant>` when naming a candidate. This round needs no parsing change. It:

1. ships `lite-planner` at max effort: the `PlannerDefaults` entry's model
   becomes `deepseek/deepseek-v4.1-flash#max`. Its derived name stays
   `deepseek-v4.1-flash`;
2. pins, in a test, that an opencode launch keeps `#max` verbatim in `-m`;
3. documents effort for every harness in the README's candidates section.

### 2. Files

```
internal/setup/setup.go            PlannerDefaults lite-planner model -> "deepseek/deepseek-v4.1-flash#max"
internal/setup/setup_test.go       assert the lite-planner candidate's model carries "#max"
internal/harness/harness_test.go   new TestLaunchOpencodeKeepsVariant
README.md                          effort sentence covers claude, codex and opencode
docs/plans/2026-09-26-planner-actors-586.md   append this round as "## Round 2"
```

### 3. Contracts

- `setup.PlannerDefaults` (`internal/setup/setup.go`, the `lite-planner` row):
  the model is `deepseek/deepseek-v4.1-flash#max`. Nothing else in `Plan` changes.
- `TestPlanSeedsPlannerActors` (`internal/setup/setup_test.go`): it already
  checks the actor candidate names. Add an assertion that the candidate
  written for `lite-planner` has `Harness=="opencode"`,
  `Provider=="openrouter"` and `Model=="deepseek/deepseek-v4.1-flash#max"`,
  and that the `planner` one has `Model=="opus:medium"`. The derived names stay
  `opus` and `deepseek-v4.1-flash`.
- `TestLaunchOpencodeKeepsVariant` (`internal/harness/harness_test.go`, next to
  `TestLaunchClaudeEffort`): `Lookup("opencode").Launch("cline-pass",
  "cline-pass/deepseek-v4.1-flash#max", nil, builder, TierHarness)` gives a
  `Print` whose element after `-m` is exactly
  `cline-pass/cline-pass/deepseek-v4.1-flash#max`, and which has no `--effort`
  and no `--variant`.
- README:
  - Find round 1's sentence about a claude model ending in
    `:low|medium|high|xhigh|max` (after the codex-effort paragraph, ~line 1204).
    Make it one short paragraph covering all three:
    - codex: `model:<effort>`;
    - claude: `model:<low|medium|high|xhigh|max>`, which sets `--effort`; any
      other suffix stays part of the model id;
    - opencode: `model#<variant>`, passed to `opencode run -m`. Variants are
      per model, and opencode refuses an unknown one.
  - Update the init example output line if it shows model strings. It shows
    names only, so it probably needs no change.

### 4. Working Efficiently

- Read `internal/setup/setup.go`, `internal/setup/setup_test.go`,
  `internal/harness/harness_test.go` (around `TestLaunchClaudeEffort`) and
  `README.md` around the effort sentence in one parallel batch.
- One edit call per file.
- Focused: `go test ./internal/setup/ ./internal/harness/ ./cmd/relevo/ -run 'Plan|Launch|Init'`.
- Full check once at the end: `make check`, then `gofmt -l .`.

### 5. Steps

1. Change the model in setup.go and extend `TestPlanSeedsPlannerActors`.
   Verify: `go test ./internal/setup/`.
2. Add `TestLaunchOpencodeKeepsVariant`. Verify with the focused command.
3. Update the README paragraph.
4. Run `make check` and `gofmt -l .`. Append this plan to
   `docs/plans/2026-09-26-planner-actors-586.md` under `## Round 2`. Commit on
   the branch as a new commit, not an amend, with a message referencing #586.

### Report must include

`git diff --stat HEAD~1`, and the `make check` result.

---

## Round 3

### #586 round 3: claude gives no builder candidate in config init

Rounds 1-2 plus a README fix are committed on this branch (HEAD `a8493d6a`).
This round is one more change in the same place. Stop and report instead of
improvising if a location below does not match.

### 1. System Overview

Decision (user, 2026-09-26): claude plans and never builds by default.
`relevo config init` no longer writes a claude builder candidate. Today
`setup.Defaults["claude"]` = `{"anthropic","sonnet"}` makes `sonnet` a builder
candidate whenever `claude` is on PATH.

- Other harnesses on PATH: the builder gets one candidate per non-claude
  harness, exactly as today.
- Claude is the only harness on PATH: the builder actor is written with **no
  candidates**. `roles.validateActor` accepts that, as a `planner` with no
  candidates is valid today. init prints one note telling the user to add a
  builder candidate.
- `claude` still counts as a harness found. Its role definitions are still
  installed (`Files.Kinds` keeps it), and it still gives the `planner` actor
  its `opus:medium` candidate.

### 2. Files

```
internal/setup/setup.go         remove Defaults["claude"]; skip kinds with no builder default; Files.CandidateNames
internal/setup/setup_test.go    updated counts; new claude-only test
cmd/relevo/init.go              candidates line lists names; claude-only note
cmd/relevo/init_test.go         updated count and output
README.md                       init description and example output
docs/plans/2026-09-26-planner-actors-586.md   append as "## Round 3"
```

### 3. Contracts

`internal/setup/setup.go`:
- `Defaults`: delete the `"claude"` entry. Update its comment: it maps each
  harness kind to its **builder** default, and `claude` has none because claude
  plans (a *why* comment, one line).
- `Plan` loop (currently `d := Defaults[h.Kind]`, ~line 54): a kind on PATH is
  appended to `kinds` as today. A builder candidate is appended **only when**
  `Defaults` has an entry for the kind (`d, ok := Defaults[h.Kind]`).
  `builderCount` is the number of builder candidates appended (it may be 0).
- The no-binaries error is unchanged (it keys on `kinds`, not on candidates).
- `Files` gains `CandidateNames []string`: the names from the single
  `candidate.DeriveNames` call, in candidate order. The builder actor gets
  `names[:builderCount]`, which may be empty. The builder actor is always written.

`cmd/relevo/init.go` (`cmdInit`, ~lines 71-77):
- The candidates line becomes
  `wrote candidates (<len(CandidateNames)>: <names joined ", ">)`, e.g.
  `wrote candidates (3: glm-5.3-flash, opus, deepseek-v4.1-flash)` for
  claude and opencode.
- `actorSummary`: an actor with no candidates renders as `builder: none`.
- When the builder actor has no candidates, print right after the actors line:
  `note: no builder candidate (claude only plans); add one: relevo config set actors.builder.candidates '["<name>"]'`.

### 4. Working Efficiently

- Read `internal/setup/setup.go`, `internal/setup/setup_test.go`,
  `cmd/relevo/init.go`, `cmd/relevo/init_test.go` and `README.md` 150-175 and
  370-380 in one parallel batch. One edit call per file.
- Focused: `go test ./internal/setup/ ./cmd/relevo/ -run 'Plan|Init'`.
  Stub binaries only: CI has no harness binaries and no network.
- Full check once at the end: `make check`, then `gofmt -l .`.

### 5. Steps

1. **setup.** Make the changes in §3. Update the tests:
   - `TestPlanFindsBinariesInHarnessOrder` (claude and opencode): `Kinds` is still
     `[claude opencode]`; there are 3 candidates; the builder's candidates are
     exactly `["glm-5.3-flash"]`; `CandidateNames` equals `DeriveNames(cands)`.
   - `TestPlanSeedsPlannerActors`: adjust any count or index that assumed a
     claude builder candidate. Keep the model and name assertions.
   - New `TestPlanClaudeOnlyHasNoBuilderCandidate`: only claude on PATH gives
     no error, `Kinds == [claude]`, a builder actor with agent `plan-executor`
     and zero candidates, and a `planner` actor with `["opus"]`. No candidate
     has model `sonnet`.
   - New `TestPlanNeverMakesClaudeABuilder`: every harness on PATH gives
     builder candidates none of which has `Harness == "claude"`.
   Verify: `go test ./internal/setup/`. Mutation-check it: temporarily re-add
   `"claude": {"anthropic","sonnet"}` to `Defaults`, confirm
   `TestPlanNeverMakesClaudeABuilder` fails, then restore.
2. **init.** Make the changes in §3.
   - `TestInitWritesConfigAndRoles`: the candidates count becomes 3.
   - `TestInitReportsActors`: keep it building the expected actors line from
     the stored actors. Also assert the candidates line
     `wrote candidates (3: glm-5.3-flash, opus, deepseek-v4.1-flash)`.
   - New `TestInitClaudeOnlyNotesMissingBuilder`: stub only `claude` on PATH.
     Output contains `builder: none` and the `note: no builder candidate` line.
     Use `initRoot`/`stubBinary`/`captureOutput` like the existing tests.
   Verify: `go test ./cmd/relevo/ -run Init`.
3. **README** (~lines 150-175 and ~375):
   - The init description says it writes one builder candidate per harness
     **except claude**, which only plans (it gets the `planner` actor).
   - The example output becomes:
     `wrote candidates (3: glm-5.3-flash, opus, deepseek-v4.1-flash)` and
     `wrote actors (builder: glm-5.3-flash; planner: opus; lite-planner: deepseek-v4.1-flash)`.
   - Add one sentence on the claude-only case: the builder has no candidates,
     and init says how to add one.
4. **Full check and commit.** Run `make check` and `gofmt -l .`. Append this plan
   under `## Round 3` in `docs/plans/2026-09-26-planner-actors-586.md`. Commit
   as a new commit (no amend) referencing #586.

### Report must include

`git diff --stat HEAD~1`, the step-1 mutation check and its result, and the
`make check` result.
