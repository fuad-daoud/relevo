# Plan: `relevo setup` offers the muse plugin (2026-10-26)

Scope: `relevo setup` asks about and installs the relevo plugin for muse
exactly as it already does for Claude Code. Out of scope: doctor, README,
Detect/hooks, opencode, and any change to how the Claude Code plugin step
behaves.

## Behavior

- New yes/no question with key `muse-plugin`, recommended yes:
  "Install the relevo plugin into muse (the MasterMind side)?"
  Asked only when muse is on PATH (a `muse` entry exists in the probe
  inventory) and no `muse-plugin` answer is already stored. It is independent
  of the `plugin` (Claude Code) question: both appear when both binaries are
  present, each gated only on its own binary.
- New optional apply step `muse-plugin` running after `plugin`, before
  `service`. It runs only on yes (`yes`/`y`, same canonicalization as
  `setupStepYes`); `setupStepsFor` order becomes
  `config, agents, plugin, muse-plugin, service, smoke`.
- The `muse-plugin` step runs, in order, through the existing
  `setupCommandRunner` seam (`setupPluginRunner`):
  1. `muse plugins marketplace list`; when its output does not contain the
     marketplace ref, `muse plugins marketplace add relevo
     https://github.com/fuad-daoud/relevo.git`.
  2. `muse plugins install relevo@relevo`; exit 0 is success however its
     first line reads (an "already installed" first line at exit 0 is
     success — this mirrors `runSetupPlugin`, which checks only exit/rerr
     and never parses install stdout on success).
  3. `muse plugins approve relevo` (muse requires approval before hooks and
     MCP servers run); exit 0 required.
- Failure reporting mirrors the Claude Code step exactly: marketplace-add
  failure and install/approve failure each return
  `failNext(codeInternal, <fix>, <first-line-or-fallback>)` so the row names
  its fix. The fix string lives in `cmd/relevo` (new const, e.g.
  `setupMusePluginCommand = "muse plugins marketplace add relevo
  https://github.com/fuad-daoud/relevo.git && muse plugins install
  relevo@relevo && muse plugins approve relevo"`); it must NOT be added to
  `internal/doctor` (doctor is out of scope).
- `setupStepStarts` gains a `muse-plugin` start line naming muse, matching
  the existing `plugin` entry's shape.
- Reused as-is, no copies: `setupMarketplaceAdded` (the muse list line
  `relevo\tplugins=1\tsource=https://github.com/fuad-daoud/relevo.git`
  contains `setupMarketplaceRef`), `yesNoQuestion`, `setupStepYes`,
  `firstLine` failure-detail shaping.
- Standing answers: unchanged. `setupStandingAnswers` in
  `cmd/relevo/setup.go` keeps mapping only the Claude Code install state to
  `plugin=yes`; there is no muse installed-state detection (that would need
  a doctor check, out of scope), so `muse-plugin` drops only once answered.
- `internal/setup.Build`, `requiredPicks`, probe commands, harness
  definitions: unchanged. Probe already emits a `muse` status entry when the
  binary is on PATH (`Probe` walks `harness.All()`, and `muse` is registered
  in `internal/harness/harness.go`); `probeHarness` needs no `muse` case
  because the question gates on PATH presence, not login.
- Contract surfaces: `SetupQuestionsDoc`/`SetupResultDoc` shapes are
  unchanged (a new question id and step id are data, not shape);
  `testdata/contract/help-json.golden` lists verbs, not setup steps, so it
  should not change — verify with `git diff --stat` and update a golden only
  if a run proves it lists the steps.

## Seams (read before editing)

- `internal/setup/questions.go`: `Questions` (66-99), plugin gate (78-83),
  `claudeOnPath` (256-265), `yesNoQuestion` (205-216).
- `cmd/relevo/setup_apply.go`: `setupMarketplaceRef`/`setupPluginToken`
  (26-31), `setupStepsFor` (61-71), `setupApply` dispatch incl. plugin block
  (122-126), `setupStepStarts` (163-168), `runSetupPlugin` (256-275),
  `setupMarketplaceAdded` (278-283).
- `cmd/relevo/setup.go`: `setupGroundedAnswers` (176-185),
  `setupStandingAnswers` (233-248), `claudeConfigDir` (250+).
- `cmd/relevo/setup_docs.go`: `SetupQuestionsDoc`/`SetupResultDoc` shapes.

## Steps

1. Deliverable: `museOnPath` in `internal/setup/questions.go` mirroring
   `claudeOnPath`, plus the `muse-plugin` block in `Questions` after the
   `plugin` block. Know it worked: `go test ./internal/setup/ -run
   TestQuestions -count=1` passes (existing tests untouched).
2. Deliverable: `runSetupMusePlugin` + `setupMusePluginCommand` const +
   `muse-plugin` entries in `setupStepsFor` and `setupStepStarts` in
   `cmd/relevo/setup_apply.go`. Know it worked: `go build ./...` passes.
3. Deliverable: new tests in `cmd/relevo/setup_apply_test.go` reusing the
   `fakePluginRunner`/`withPluginRunner` seam, extended with approve-call
   recording: list-names-marketplace skips add then install then approve;
   missing marketplace runs list, add, install, approve; failed list still
   attempts add; install failure at non-zero exit surfaces its first line
   with the muse fix; install exit 0 reading "already installed" is success;
   approve failure fails the step naming the fix. Each pins its call order
   via `f.calls`. Know it worked: `go test ./cmd/relevo/ -run
   'TestMusePlugin|TestPlugin' -count=1` passes.
4. Deliverable: new tests in `internal/setup/questions_test.go` mirroring
   `TestQuestionsDropsPluginWhenInstalledOrClaudeAbsent`: `muse-plugin`
   asked with muse on PATH and none answered; dropped once answered;
   dropped with no muse on PATH; `plugin` and `muse-plugin` both present
   with both binaries on PATH. Plus a `setupStepsFor` case in
   `cmd/relevo/setup_test.go` pinning
   `config, agents, plugin, muse-plugin, service` order on all-yes. Know it
   worked: `go test ./internal/setup/ ./cmd/relevo/ -count=1` passes.
5. Deliverable: `git diff --stat` shows only `internal/setup/questions.go`,
   `cmd/relevo/setup_apply.go`, the two test files, and this plan; run the
   full gate. Know it worked: `gofmt -l` on the changed files is empty and
   `make check-affected` passes. (CI rule for this area: no `cmd/relevo`
   test may spawn a harness or reach the network — the new tests run only
   through the fake runner.)

## Must NOT change

1. `runSetupPlugin`, the `plugin` step, `setupMarketplaceRef`,
   `setupPluginToken`, `doctor.PluginInstallCommand` behavior.
2. `internal/doctor/*` (no muse fix const, no muse install-state check).
3. `README.md` and any docs outside `docs/plans/`.
4. `internal/setup/probe.go`, `build.go`, `answers.go`;
   `setupStandingAnswers` signature and semantics; Detect/hooks; opencode.
5. `testdata/contract/*.golden` unless a failing contract test proves a
   step list changed; never add a golangci-lint or comment/file-size
   exclusion to get green (per `CLAUDE.md`).

## Report must include

- The exact question prompt, key, and recommendation used.
- The exact muse commands run per step case (list/add/install/approve) and
  the measured first-line outputs relied on.
- How already-installed is detected for muse (exit 0, first line ignored)
  and where the claude step's equivalent handling lives
  (`runSetupPlugin`, exit-only check).
- `git diff --stat`, `gofmt -l` output, `make check-affected` result, and
  confirmation the help-json/contract goldens were or were not touched and
  why.

Save this plan as docs/plans/2026-10-26-muse-setup-plugin.md, `gofmt -l`
the changed files, `make check-affected`, commit, then report.
