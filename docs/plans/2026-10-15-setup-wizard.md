# Setup wizard — implementation plan (3 builder rounds, one branch, in a row)

Spec: `docs/specs/2026-10-15-setup-wizard-design.md` (approved; do not redesign, do not edit).
Each round leaves `make check` green. New commits only; never amend/rebase a pushed commit.
Final round saves this plan as `docs/plans/2026-10-15-setup-wizard.md`.

Shared context each builder must read first: the spec in full; `CLAUDE.md`
(style §Code style, coverage § stunt, `make check` + `make e2e` notes);
`internal/setup/setup.go` (150 lines); `cmd/relevo/init.go` (168 lines);
`internal/harness/harness.go` (`knownHarnesses` lines 118–228, `All` 436–445)
and `internal/harness/install.go` (`InstallEnv` 17–31, `Install` 89–126);
`cmd/relevo/main.go` (`run` dispatch 196–295, `report` 140–181),
`cmd/relevo/registry.go` + `cmd/relevo/registry_rows.go`, `cmd/relevo/args.go`
(`noticeWriter` 242–247, `isTerminal` 355–358, `parseFlags` 174–206).

Conventions used below: "pure" = no terminal, no harness binary, no network
(CI has neither; no `cmd/relevo` test may exec a harness or reach the
network — test rules as pure functions). "Pin" = a test whose name says what
it pins and which fails if the logic is broken (mutation-check non-trivial ones).

Per-round constraints (all three rounds):
- CLAUDE.md code style: comments say why only, no issue/PR numbers or history;
  functions ≤ 70 lines, files ≤ 600 lines (`scripts/check-comments.sh`,
  `scripts/check-filesize.sh` enforce it).
- CI has no harness binary and no network: no `cmd/relevo` test may run a
  harness or reach the network; test rules as pure functions with fake runner output.
- Coverage baseline: if code moves between packages, regenerate with
  `sh scripts/check-coverage.sh --write` and say so in the report.
- The spec file is already committed; do not edit it.

Focused test command per round is named in that round; full gate is always
`make check` at the end. Optionally compare coverage locally with
`RELEVO_REQUIRE_COVERAGE=1`.

---

## R1 — Engine (`internal/setup`) + `config init` on `Build`

### Behaviour
- `Probe(env) Inventory`: for claude, codex, agy — on PATH (via `LookPath`,
  as `Plan` does today), logged in, models offered — through an injected
  runner seam (the way `harness.InstallEnv` injects `LookPath`), with a
  10-second timeout per probe command so a hung CLI cannot stall setup.
  Exact commands (spec §Probe): `claude auth status` → JSON `"loggedIn": true`;
  `codex login status` → exit 0; `codex debug models` → entries with
  `"visibility": "list"` in ascending `priority`; `agy models` → exit 0, first
  tab-separated field per line after any `Fetching ...` line. Model→candidate:
  claude → provider `anthropic`, alias as model; codex → provider `openai`,
  slug; agy → provider `google` (first of agy's `Providers`, cf.
  `harness.go:134`). Fixed aliases for claude: `sonnet`, `opus`, `haiku`.
- Notes (not questions): harness on PATH but not logged in →
  `<harness>: on PATH, not logged in -> <login command>` (`claude auth
  login`, `codex login`, `run agy once to sign in`); opencode on PATH →
  `opencode: not covered by setup yet; configure it with relevo config`.
  No harness logged in → stop before writing anything: code `not_available`,
  message listing each login command, `next: relevo setup`. No harness on PATH
  at all → keep `Plan`'s existing message (`setup.go:79`,
  `"no harness binaries on PATH (agy, claude, codex, opencode); install one first"`).
- `Questions(inv, answers) []Question`: ids `builder` (ordered multi),
  `planner`, `tier`, `plugin` (only when claude on PATH and plugin not
  installed), `service` (only when no unit installed), `smoke`; each with
  prompt, choices (value, label, one-line why), recommended value(s), multi
  flag. Recommendations exactly per spec §Questions (codex first-listed at
  `:high`; agy first `gemini-*-flash-high` else first; claude `sonnet`;
  planner claude `opus:medium` else codex first at `:high` else agy first
  `claude-opus-*` else agy first; tier `yolo`; plugin yes; service yes; smoke
  yes). Inapplicable questions are dropped.
- Answer parsing/validation: candidate values are
  `<harness>/<provider>/<model>` (cf. `candidate.ParseRef`,
  `internal/candidate/candidate.go:20–38`); ordered pick; yes/no. An answer
  naming a non-choice is a usage error naming the valid values; an answer for
  a dropped question is ignored (caller prints a stderr note — engine returns
  the fact, front end prints it in R3).
- Derived actors (printed in summary, never asked): `librarian` = builder list
  with `@docs`+comments scope (as init writes today, `setup.go:130–135`);
  `lite-planner` + `reviewer` = cheapest picked model preferring claude
  `sonnet`, then codex's last listed model, then agy's first `*-flash-*`;
  `security` = planner's candidate. Default chain gains `reviewer`,
  `security`, `lite-planner`. Names via `candidate.DeriveNames`
  (`internal/candidate/names.go:23–48`); one model picked twice = one
  candidate listed by both actors. Policy `max_tier` from the `tier` answer;
  writer actors (builder, librarian) carry the same tier.
- `Build(inv, answers) (Files, error)`: same `Files` shape `Plan` returns
  today (`setup.go:50–57`: `Kinds`, `Candidates`, `Policy`, `Actors`,
  `ActorOrder`, `CandidateNames`); candidates JSON carries no `roles` key and no
  tier (cf. `setup_test.go:312–320` pattern); policy JSON only `max_tier`.
- `config init` becomes `Build(Probe(env), every recommended answer)`: same
  probed lineup, no personal models, derived actors included; skips plugin /
  service / smoke steps; keeps `--force`, `--no-agents`, `--json`; prints the
  tier on its own line verbatim:
  `tier: yolo (builders run commands without asking) -- relevo config set policy.max_tier edit to restrict`.

### Seams (files, types, functions)
- `internal/setup/setup.go` (rewrite; stays ≤ 600 lines, split files if needed):
  `type Runner interface { Run(name string, args ...string) (stdout []byte, exit int, err error) }`
  (timeout enforced inside the OS impl, not by callers);
  `type SetupEnv interface { harness.InstallEnv; Runner }` (embeds today's
  `InstallEnv` so `Plan`'s `LookPath` seam is kept);
  `type Inventory struct { Harnesses []HarnessStatus; Notes []string }`,
  `type HarnessStatus struct { Harness string; OnPath, LoggedIn bool; Models []string; Login string }`;
  `func Probe(env SetupEnv) Inventory`;
  `type Choice struct { Value, Label, Why string }`,
  `type Question struct { ID, Prompt string; Multi bool; Recommended []string; Choices []Choice }`;
  `func Questions(inv Inventory, answers map[string][]string) []Question`;
  `type Answers map[string][]string`;
  `func ParseAnswer(q Question, raw string) ([]string, error)`;
  `func Build(inv Inventory, answers Answers) (Files, error)`;
  `func RecommendedAnswers(inv Inventory) Answers`;
  OS runner constructor for production (R3 wires it; R1 needs it to exist):
  `func OSEnv() (SetupEnv, error)` backed by `exec.LookPath` + `os/exec` with
  the 10 s timeout, or reuse `harness.OSInstallEnvKV`-style construction —
  builder's choice, one place only.
- `cmd/relevo/init.go`: `cmdInitRun` (42–140) calls
  `setup.Build(setup.Probe(env), setup.RecommendedAnswers(...))` instead of
  `setup.Plan(env)`; prints the tier line; keeps `initFlagSet` (30–36),
  `PutDoc` block (83–88), `--force`/`--no-agents`/`--json` behaviour,
  `actorSummary` (146–168). `agentInstallEnv` (`cmd/relevo/agent.go:47–49`)
  supplies the env.
- Untouched in R1: `internal/serviceunit/*`, `cmd/relevo/daemon_service.go`,
  `cmd/relevo/service.go`, `cmd/relevo/doctor.go`, `internal/doctor/*`,
  `cmd/relevo/main.go` dispatch + `usage`, registry files, README, guide.

### Tests (names say what they pin; all on fake runner output, no binaries)
- `internal/setup`: table tests —
  `TestProbeMarksLoggedOutHarnessWithLoginCommand` (each of the three login
  strings incl. agy's `run agy once to sign in`),
  `TestProbeParsesCodexModelsByPriority` (visibility filter + ascending priority),
  `TestProbeParsesAgyModelsAfterFetchingLine` (first tab field),
  `TestProbeParsesClaudeLoggedInJSON` (true/false/malformed JSON),
  `TestProbeHungCommandTimesOut` (runner that blocks; probe returns logged-out,
  fast — bound the test's own clock),
  `TestProbeNoHarnessKeepsExistingMessage` (pins today's
  `no harness binaries…` text),
  `TestQuestionsRecommendOneModelPerLoggedInHarness` (codex→agy→claude order,
  `:high` suffixes, `sonnet`),
  `TestQuestionsPlannerFallsBackWithoutClaude`,
  `TestQuestionsDropsPluginWhenInstalledOrClaudeAbsent`,
  `TestQuestionsDropsServiceWhenUnitInstalled`,
  `TestParseAnswerRejectsNonChoiceNamingValidValues`,
  `TestBuildDerivesReviewerSecurityLitePlanner` (cheapest-pick rule + planner
  candidate for security),
  `TestBuildSharedModelIsOneCandidate` (DeriveNames sharing),
  `TestBuildTierFlowsToPolicyAndWriters` (`max_tier` + builder/librarian tier;
  `edit` variant),
  `TestBuildCandidatesCarryNoRolesKey` (keeps today's pin).
- `cmd/relevo` (`init_test.go` helpers `initRoot`/`storedConfig` reused):
  `TestInitPrintsTierLine` (exact tier sentence), plus updates of the existing
  init tests to the probed lineup (see deletions). No harness executed, no
  network (state the seam: fake `SetupEnv` or stub binaries + fake runner —
  never a real CLI).
- What must not change: `Files` JSON shapes (candidates/policy/actors
  encoding); `harness.Install` behaviour; service unit bytes; doctor output;
  registry; help text.

### Ordered steps
1. Engine types + `Probe` with injected runner + fake-runner table tests —
   done when `go test ./internal/setup/` passes with no binary on PATH.
2. `Questions` + recommendations + drop rules + tests — done when the
   recommendation/drop table tests pass.
3. `ParseAnswer` + validation + tests — done when usage-error text names the
   valid values.
4. `Build` (candidates/policy/actors incl. derived actors) + tests — done when
   actor/tier/naming tests pass and no `roles` key appears in candidates JSON.
5. `cmdInitRun` on `Build(Probe, RecommendedAnswers)` + tier line; rewrite
   init tests — done when `go test ./cmd/relevo/ -run 'TestInit'` passes.
6. `make check` green; regenerate coverage baseline only if code crossed
   packages (`sh scripts/check-coverage.sh --write`, said in report).

### Deleted in R1 (closed list)
1. `internal/setup/setup.go`: `Defaults` (21–25), `PlannerDefaults` (38–47),
   `Plan` (60–122) superseded by `Probe`/`Questions`/`Build`, `BuilderKinds`
   (28–36) if orphaned once the init note goes (verify by grep; keep iff
   another caller uses it), `starterActors`/`candidateEntries` superseded by
   `Build`'s assembly.
2. `cmd/relevo/init.go`: claude-only note block (107–109) — claude now builds.
3. Tests: `TestPlanFindsBinariesInHarnessOrder`,
   `TestPlanSeedsPlannerActors`, `TestPlanSeedsLibrarianActor`,
   `TestPlanClaudeOnlyHasNoBuilderCandidate`, `TestPlanNeverMakesClaudeABuilder`,
   `TestPlanSkipsPlannerWithoutItsHarness`, `TestPlanNoneIsAnError`,
   `TestBuilderKindsListsBuilderHarnessesInOrder` (superseded by the new
   probe/build tests), `TestInitClaudeOnlyNotesMissingBuilder`,
   and the personal-lineup expectations inside `TestInitWritesConfigAndRoles`
   / `TestInitReportsActors` (glm/opus/deepseek seeds).

### Report must include
- `Probe` command table as implemented (command → parsed signal per harness);
  runner seam name and where the 10 s timeout lives; `Build` actor assembly
  summary; init's new output lines; `make check` result; whether the coverage
  baseline was regenerated; anything in the seed that contradicted the code.

---

## R2 — Service unit template + unconfigured-machine hint + doctor row

### Behaviour
- `internal/serviceunit`: the shipped `relevo.service`
  (`internal/serviceunit/units/relevo.service`, now hardcoding
  `ExecStart=%h/.local/bin/relevo` line 15 and fixed `PATH` line 14) becomes a
  template like the plist (`units/com.github.fuad-daoud.relevo.plist.in` with
  `@BIN@`/`@HOME@`): `@BIN@` in `ExecStart`, `@PATH@` in `Environment=PATH=`.
  Render substitutes `@BIN@` with `os.Executable()` and `@PATH@` with the
  current fixed list plus the directory of every harness binary `LookPath`
  finds, deduplicated, in that order. Reading an installed unit canonicalises
  it back (binary path, then the `Environment=PATH=` value) before hashing,
  mirroring `CanonicalPlist` (`decide.go:52–64`); the stale/edited decision
  (`DecideUnitAction`, `decide.go:34–44`) and the doctor drift check keep
  working. The shipped-unit index (`units/shipped.sha256` via `shipped.go`)
  keeps the template's hash, so a unit an older release wrote is still stale,
  not edited. Applies to `relevo service install` and the daemon's
  start-time refresh (`refreshServiceUnitAtStart`,
  `cmd/relevo/daemon_service.go:157–175`) too, not only setup.
- `cmd/relevo/daemon_service.go`: `unitSpec.shipped()` (68–73) returns the
  service template on Linux; `rendered()` (76–81) renders it (PATH assembled
  via `LookPath` over `harness.All()` binaries); `read()` (85–100)
  canonicalises Linux units like the darwin arm does; `decide`/`write`/`refresh`
  unchanged in shape. macOS path byte-identical in behaviour.
- Unconfigured-machine hint: when no candidates section exists, a verb that
  needs config prints one stderr line,
  `relevo is not set up yet: run relevo setup`, and continues as today.
  `--json` output stays clean (hint on stderr only — cf. `noticeWriter`,
  `args.go:242–247`, and `jsonRequested`, `main.go:187–194`). `setup`,
  `config`, `help`, `version`, `doctor` do not print it. Single choke point
  (builder picks the exact call site, e.g. beside `newRuntime` in `wire.go`
  or the `run` dispatch in `main.go:228–294`); one pure predicate so tests
  need no daemon.
- Doctor row: `relevo doctor` shows the same as a row whose fix is
  `relevo setup`. The candidates row is built in `cmd/relevo/doctor.go:346–353`
  (today fix `relevo config set candidates …`); change that fix to
  `relevo setup` (keep the row, keep `NoCandidates` semantics for the summary
  line at `doctor.go:197–198` unless the summary must name setup too —
  builder decides, tests pin it).

### Seams
- `internal/serviceunit/units/relevo.service` (template placeholders);
  `internal/serviceunit/embed.go` (`ShippedService`, `ShippedPlistTemplate`,
  23–29); `internal/serviceunit/decide.go` (+ `RenderService(template, bin,
  path) []byte`, `CanonicalService(installed, bin, path) []byte` beside
  `RenderPlist`/`CanonicalPlist` 47–64); `internal/serviceunit/shipped.go`
  (`ShippedUnitBefore` 48–53) — no signature change;
  `scripts/serviceunit-shipped.sh --write` regenerates
  `units/shipped.sha256` (lines never dropped); `scripts/serviceunit-dist.sh
  --write` refreshes `dist/relevo.service` (`--check` runs in `make check`).
- `cmd/relevo/daemon_service.go`: `unitSpec` (21–26), `newUnitSpec` (32–40),
  `osUnitSpec` (42–56, `os.Executable` source of `@BIN@`), `shipped`,
  `rendered`, `read`, `decide` (102–108); `installServiceUnit`
  (`service.go:89–108`) and `refreshServiceUnit` (137–154) unchanged in shape.
- Hint: new pure helper, e.g. `func needsSetupHint(verb string, hasCandidates
  bool) bool` with the exempt list (`setup`, `config`, `help`, `version`,
  `doctor`); call site in the verb path; stderr only.
- Doctor: `cmd/relevo/doctor.go` `doctorReport` candidates block (346–354).

### Tests
- `internal/serviceunit` (extend `decide_test.go` patterns):
  `TestServiceRenderReplacesPlaceholders` (no `@BIN@`/`@PATH@` left;
  harness dirs appended after the fixed list, deduped, in `harness.All()` order),
  `TestServiceCanonicalRoundTrip` (mirrors `TestPlistCanonicalRoundTrip`:
  `CanonicalService(RenderService(t)) == t`; freshly rendered decides
  `ActionCurrent`),
  `TestOldServiceUnitIsStillStale` (bytes of the pre-template unit from git
  history hash into the regenerated index → `ActionStale`, never `Edited`),
  `TestHandEditedServiceUnitIsEdited`.
- `cmd/relevo`: `TestServiceInstallRendersCurrentBinary` (fake `unitSpec`
  with temp bin/home + fake `Runner` via `newServiceRunner` swap
  (`service.go:13`); asserts written bytes contain the bin path and harness
  dirs; decides current on re-read),
  `TestUnconfiguredHintOnStderrOnly` (no candidates → exact stderr line,
  clean stdout/`--json`; exempt verbs silent),
  `TestDoctorSetupRowFix` (no candidates → row fix `relevo setup`).
  No `systemctl`/`launchctl` executed; no network.
- What must not change: plist template bytes and macOS behaviour; `Runner`
  interface (`runner.go:9–19`); `installServiceUnit` edited-unit rule
  (conflict without `--force`); `DecideUnitAction` semantics; doctor rows
  other than the candidates fix.

### Ordered steps
1. Template + `RenderService`/`CanonicalService` + round-trip/stale tests —
   done when `go test ./internal/serviceunit/` passes.
2. Regenerate index (`serviceunit-shipped.sh --write`) + `dist`
   (`serviceunit-dist.sh --write`); old-unit-stale test proves it — done when
   both `--check` modes pass.
3. `daemon_service.go` render/canonicalise wiring (install + start-time
   refresh) + install test — done when install writes a unit that re-reads as
   current.
4. Hint predicate + call site + stderr/`--json` tests — done when exempt
   verbs stay silent and documents stay clean.
5. Doctor row fix + test — done when the row's fix is `relevo setup`.
6. `make check` green (includes `serviceunit-dist.sh --check`); coverage
   baseline regen only if code crossed packages, said in report.

### Deleted in R2 (closed list)
1. The hardcoded `ExecStart=%h/.local/bin/relevo` and fixed `PATH` lines of
   `units/relevo.service` (replaced by the `@BIN@`/`@PATH@` template lines).
   No `shipped.sha256` line is deleted (the script unions history).

### Report must include
- Template diff summary; `@PATH@` assembly order + dedup rule; index/dist
  regeneration commands run; hint call site + exempt list; doctor row change;
  `make check` result; any seed/code contradiction found.

---

## R3 — `relevo setup` front end (`cmd/relevo/setup.go`) + help + docs

### Behaviour
- New verb `relevo setup`: (1) probe, print one inventory line, e.g.
  `codex: logged in · claude: logged in · agy: on PATH, not logged in ->
  run agy once to sign in`; (2) take repeatable `--answer <id>=<value>` flags;
  (3) if questions still open: on a TTY (stdin and stdout both terminals —
  cf. `isTerminal`, `args.go:355–358`, and `bareArgs` usage in
  `main.go:200`) and without `--json`, ask each as a numbered prompt where
  Enter takes the recommended pick (ordered pick typed `1,3`; yes/no `y`/`n`);
  otherwise print the questions document to stdout and exit 3
  (`exitCodeErr{code: 3}` — the same mechanism `wait.go:156` uses for its
  protocol codes; the spec's exit-3 questions contract), writing nothing.
  (4) Apply in order, one line per step (`ok` or failure with `next:` fix);
  a failed step stops later steps. Then `next: relevo doctor`, and for the
  MasterMind `next: open Claude Code in a repository and ask it to plan something`.
- `--answer` value syntax: choice values; candidate values
  `<harness>/<provider>/<model>`; invalid → usage error naming valid values
  (R1's `ParseAnswer`); answer for a dropped question ignored with a stderr note.
- `--json` questions-open document exactly per spec §The JSON contract
  (`inventory` with `on_path`/`logged_in`/`models`/`login`, `notes`,
  `questions` with `id`/`prompt`/`multi`/`recommended`/`choices`, `next`
  `relevo setup --answer builder=<...> --answer tier=<...>`), exit 3. All
  answered + `--json` → result document (`steps` with
  `step`/`ok`/`detail`/`next`), exit 0 when every step passed; a failed step
  exits 1 with that step's own error code (`internal` when it has none);
  earlier steps stay applied and the document says which failed.
- Apply steps (reuse, do not duplicate):
  1. `config`: R1 `Build`, then one `PutDoc` of candidates+policy+actors as
     one revision (cf. `init.go:83–88`).
  2. `agents`: `harness.Install` per logged-in harness (cf.
     `init.go:113–128`, `agent.go:cmdAgentInstallRun`).
  3. `plugin` (yes only): run `claude plugin marketplace add
     fuad-daoud/relevo`, then `claude plugin install relevo@relevo` (the
     doctor's fix command, `internal/doctor/mastermind.go:221`); an
     already-added marketplace is not a failure. Exec through an injected
     seam so tests never run it (CI has no network).
  4. `service` (yes only): what `relevo service install` does
     (`installServiceUnit`, `service.go:89–108`) after the R2 fix.
  5. `smoke` (yes only): throwaway git repo under the state root
     (`$XDG_STATE_HOME/relevo/setup-smoke/repo` — cf. `store.DefaultRoot`
     in `wire.go`, `captureOutput`-style isolation in tests); bind the
     builder actor on it, current tree, `--name setup-smoke --no-feature`,
     first builder candidate (reuse `bind.go` route/helpers); MasterMind
     resolved as bind resolves it, else register one named `setup` (cf.
     `mastermind.go` `init`); send a plan creating committed `HELLO.md`
     containing `relevo works` + report (reuse `send.go` path); wait with a
     10-minute budget printing a dot line (reuse `relevo.Wait`,
     `wait.go:119–121`); pass = round closed with a report and `HELLO.md`
     holds the line (print builder + duration); cleanup on pass (unbind,
     remove repo + binding state); on failure keep everything and print
     `relevo show setup-smoke --round 1 --transcript` (+ `relevo doctor`
     when the round never started). A leftover `setup-smoke` binding is
     unbound first.
- Rerun on a configured machine: current values are the recommended picks
  (front end reads current config for recommendations); apply writes a new
  revision (old recoverable via config revision history); no `--force`.
- Help: `relevo help --json` registers the verb, flags, both documents, codes
  (registry entry + `verbFlagSets` + usage line + dispatch case).
- Docs: README first-run becomes install → `relevo setup` → open Claude Code;
  `config init` moves to a "scripted install" note. Relevo guide
  (`internal/mastermind/guide.md`, near the agent loop §49–64) gains the
  two-line agent rule: show each question with the recommended choice marked,
  never answer tier yourself, rerun with every answer.
- Save this plan as `docs/plans/2026-10-15-setup-wizard.md` in this round.

### Seams
- New `cmd/relevo/setup.go`: `setupFlagSet` (flags `--answer` repeatable,
  `--json`; analysed against the `1,3`/`y`/`n` TTY syntax) returning
  `*setupFlagValues`; `cmdSetup`; pure helpers
  `setupQuestionsDocOf(inv, questions) SetupQuestionsDoc`,
  `setupResultDocOf(steps) SetupResultDoc`,
  `parseSetupAnswers(flags []string, questions []Question) (Answers, error)`,
  `setupInventoryLine(inv) string`; step runners calling `internal/setup`,
  `harness.Install`, `installServiceUnit`, and the bind/send/wait paths.
- `cmd/relevo/main.go`: `case "setup": return cmdSetup(args[1:])` in `run`
  (228–294) + one `usage` line (36–110).
- `cmd/relevo/registry_rows.go`: `setup` entry in name order (after
  `service uninstall`, before `show`): flags `--answer --json`, outputs both
  documents, exits `[0,1,2,3]`, errors include `conflict internal
  not_available policy_refused refused usage` (builder trims to what the
  code can actually emit); `cmd/relevo/registry.go`: `"setup":
  installer(setupFlagSet)` in `verbFlagSets` (29–118).
- `README.md` first-run block (130–180) + `config init` mentions (238–241,
  405; the candidates/actors prose at 141–160, 1720 stays truthful for both
  verbs or is reworded — builder's call, tests unaffected).
- `internal/mastermind/guide.md` (+2 lines).
- `docs/plans/2026-10-15-setup-wizard.md` (this plan, saved verbatim in
  structure; content identical).

### Tests
- `cmd/relevo` (pure; fake inventory/env; no harness, no network):
  `TestSetupFlagSurface` (flag parity pattern like `initFlagSet` tests),
  `TestSetupQuestionsDoc` (fake inventory → exact JSON shape incl. `next`
  line; exit 3 via `exitCodeErr`),
  `TestSetupQuestionsDocTTYRule` (TTY+`--json` still prints the document;
  non-TTY prints it and writes nothing),
  `TestSetupAnswerValidationNamesChoices` (bad value → usage naming valids;
  dropped-question answer → ignored),
  `TestSetupResultDoc` (steps shape; failed step's code; earlier steps kept),
  `TestSetupPluginStepSkipsWhenNo` / `…WhenClaudeAbsent` (question dropped →
  step skipped),
  `TestSetupSmokePlanContent` (the sent plan text pins `HELLO.md` +
  `relevo works` + commit + report).
- Registry parity: existing `registry_test.go` walkers (dispatchers incl.
  `main.go:run`, `verbFlagSets` one-installer-per-entry) cover `setup`
  once added; `contract_*` registry assertions updated for the new entry.
- Live check (report-only, sandbox per CLAUDE.md — never `make install` a
  test build): env sandbox from the branch, setup on a TTY and as an agent
  would (`--json`, rerun with answers), smoke round included.
- What must not change: bind/send/wait/doctor/service/config behaviour and
  their documents; `exitCodeErr` protocol codes elsewhere; the spec file.

### Ordered steps
1. Flags + questions-doc builders + doc/exit-3 tests (fake inventory, nothing
   written) — done when doc shape + exit 3 pinned without touching disk.
2. TTY prompts + `--answer` parsing + validation tests — done when
   Enter-takes-recommended, `1,3`, `y`/`n`, and stderr-note rules pinned.
3. Apply steps `config`→`agents`→`plugin`→`service` (exec seams faked) + per-step
   line/doc tests — done when order, stop-on-failure, and already-added-marketplace
   rules pinned.
4. Smoke round (bind/send/wait reuse, throwaway repo, pass/fail/cleanup rules)
   + plan-content test — done when pass criteria and keep-on-failure pinned.
5. Registry entry + dispatch + usage line + parity tests — done when
   `relevo help --json setup` shows the verb and registry walkers pass.
6. README first-run + guide two lines + save plan to
   `docs/plans/2026-10-15-setup-wizard.md` — done when README names setup
   first and init only under scripted install.
7. `make check` green; live sandbox run reported (TTY + `--json` + smoke).

### Deleted in R3 (closed list)
1. README first-run `config init`-first wording (130–160 block replaced by
   install → `relevo setup` → Claude Code; `config init` retained only as a
   scripted-install note).
2. Nothing else: `config init` stays; no verb, flag, or registry entry is
   removed.

### Report must include
- Flag surface; TTY rule call sites; which code paths each apply step reuses
  (file + function); smoke pass/fail/cleanup behaviour; registry entry
  (exits/errors as shipped); README/guide diffs; plan save path; live
  sandbox result (TTY run, `--json` rerun, smoke included); `make check`
  result; coverage baseline status; any seed/code contradiction found.
