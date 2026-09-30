# Chains · round 1 — a chain's check is explicit, and "no check" is never "green"

Base: main at `40d749b4`. One-shot builder round, new commits only (no amend, no rebase of anything on the branch), `make check` green on its own, and the plan committed as `docs/plans/2026-10-01-chain-gate.md` in the same round.

## Seed vs code (said plainly, not guessed around)

The round seed says the reviewer's seed currently tells the reviewer "Check result: green" and names a nonexistent check log. The code does not do that. `chainSeedView` (`internal/relevo/chain.go:294-312`) never assigns `SeedView.GateResult` / `GateLogPath`, so today's reviewer and correction seeds render `Check result: ; its output is at .` — empty result, empty path. The mapping the seed names does exist (`chainGateResult`, `internal/relevo/chain.go:187-195`, turns a nil record into `green`), but nothing puts it into the seed. Spec §4.4 says the reviewer seed carries "the gate result and its output", and r1's `SeedView` already has the fields, so this round fills them and pins both the "no check" and the "check ran" renderings. A reviewer who thinks filling those fields is out of scope should halt the round.

This round also supersedes spec §2's "The builder's check and regate budget are the builder actor's… The chain does not own them" and the §3 usage block. The spec stays as the design record; the plan doc records the supersession (as rounds 6/9 did for their refusals).

## Deliverable

- `relevo chain --gate <cmd> | --no-gate` and `--regate <n>`, on start and on `--resume`, resolved exactly as bind resolves them (reuse `resolveGateFor` / `resolveRegate`), applied to the builder member only, stored in the chain's settings JSON.
- A third gate result, `none`, for "no check ran"; the builder event carries it; `Next` seeds the reviewer as it does for green.
- Reviewer and correction seeds say `No check ran for this round.` and name no log then; with a check they keep today's `Check result: <green|red>; its output is at <path>.`; the trace says `no check`.
- `relevo chain` prints `check: make check` / `check: none` at start; `--json` carries `"check"`.

## Scope

Changed: `internal/chain/chain.go`, `internal/chain/trace.go`, `internal/chain/seeds/{reviewer,correction}.md`, `internal/chain/{chain,trace,seeds}_test.go`, `internal/relevo/{chain_start,chain,chain_resume}.go`, `internal/relevo/{chain_start_test,chain_test,chain_resume_test,chain_trace_test}.go`, `cmd/relevo/{chain,registry_rows}.go`, `cmd/relevo/chain_test.go`, `cmd/relevo/testdata/contract/help-json.golden`.
Created: `docs/plans/2026-10-01-chain-gate.md`.
Untouched, deliberately: `internal/relevo/{bind,add}.go` and their gate resolution (lone bindings do not change), `internal/policy`, `internal/db` and migrations (the settings column is free JSON, `016_chains.sql:30`), `internal/ui`, `internal/e2e`, `docs/specs/2026-09-30-chains-design.md`.

## Pinned behaviour and seams

### 1. `internal/chain` — the third result

- `chain.GateNone = "none"` beside `GateGreen`/`GateRed` (`chain.go:68-73`); update the block comment and `Event.Gate`'s comment (`chain.go:126`).
- `Settings` (`chain.go:75-83`) gains `Gate string` and `Regate int`, untagged like its existing fields; it rides the existing settings JSON, no migration.
- `Next`/`builderClosed` (`chain.go:208-217`): no code change — a done round reaches the reviewer whatever the gate said. A new case pins `GateNone`.
- `gateWord` (`trace.go:92-97`): `GateNone → "no check"`, `GateRed → "check red after regate"`, else `"check green"`.

### 2. `internal/relevo/chain.go` — the wiring

- `chainGateResult` (`:187-195`): nil record → `chain.GateNone`; non-nil `pass` → green; anything else → red. It has no other caller.
- New `chainRoundGate(tx *store.Tx, builder string, round int) (*store.GateRecord, error)`: the `KindReport` entry for `round` in the builder's log (`tx.ReadLog`), returning its `Gate` (nil when the round had no check). The close writes that record on the entry (`reconcile.go:566`), and reads under the same lock see writes made earlier in the lock (`chainTerminal` already reads its own just-written trace rows).
- `chainSeedView` (`:294-312`) gains the record and, for `SeedReviewer`/`SeedCorrection` only, sets `GateResult = chainGateResult(rec)` and `GateLogPath = rec.LogPath` when `rec != nil`; the other seeds render no gate and need no lookup. `chainSeedText` (`:274-290`) gains the `tx` to fetch it.
- Call sites: `chainApply` (`:246`) and `chainResumeLocked` (`chain_resume.go:213`).

### 3. Seeds

- `seeds/reviewer.md:6` and `seeds/correction.md:7`: the check line renders only when the view carries a log path (`{{if .GateLogPath}}…{{else}}No check ran for this round.{{end}}`); otherwise the exact sentence `No check ran for this round.` and no path. No other lines change — the e2e fake harness recognises a seed by its first line, which must stay.
- `seeds_test.go`: existing fixtures keep a log path and today's line; add the no-check rendering for both templates.

### 4. Start flags

- `ChainOptions` (`chain_start.go:24-53`) gains `Gate string`, `NoGate bool`, `Regate *int`, mirroring `BindOptions`.
- `chainSettings` (`:251-277`) becomes `chainSettings(pol policy.Policy, opts ChainOptions, checks bool) chain.Settings`; it resolves `Gate = resolveGateFor(opts.Gate, opts.NoGate, pol, checks)` and `Regate = resolveRegate(opts.Regate, pol)`. `chainResolveStart:132` calls it with `roleChecks(rt.RoleRegistry(), "builder")` — the same role word the old inline call used, so the policy default survives a flag-less start.
- `chainBuildMembers` (`:394-430`) takes the resolved `chain.Settings` and, for the writer only, sets `b.Gate`/`b.Regate` from it; its inline `resolveGateFor("", false, …)`/`resolveRegate(nil, …)` (`:424-425`) go away. `chainCreateSecurityMember` (`chain_resume.go:311`) passes its `set` too.
- `ChainResult` (`:55-62`) gains `Check string` — the builder's resolved check, `""` = none. Start fills it from the member it stored; resume from the member it loaded.

### 5. Resume flags

- `ResumeOptions` (`chain_resume.go:14-30`) gains `Gate string`, `NoGate bool`, `Regate *int`.
- `resumeSettings` (`:137-160`, now taking `rt`) applies only what was given: gate flags given → `resolveGateFor(opts.Gate, opts.NoGate, rt.Policy, roleChecks(rt.RoleRegistry(), "builder"))`, else the stored value; `Regate != nil` → `resolveRegate(opts.Regate, rt.Policy)`, else stored.
- `chainResumeLocked` (`:166-246`) takes the opts and, when any of the three flags was given, loads the builder member and writes only what was asked: `--gate`/`--no-gate` → `builder.Gate = set.Gate`; `--regate` → `builder.Regate = set.Regate`; then `tx.Save`. A chain started before this round has no `Gate` in its stored settings, so a `--regate`-only resume must not clear a member gate it never saw.
- A missing builder record while a gate flag is given is an error and fails the resume with nothing written; say so in the report.

### 6. CLI

- `chainFlagValues`/`chainFlagSet` (`cmd/relevo/chain.go:27-66`): `--gate` (`fs.String`), `--no-gate` (`fs.Bool`), `--regate` (`fs.Int`, default -1); help text mirroring bind's. `regateFlag(fs, v.regate)` gives the nil-means-unset `*int` and refuses a negative before any runtime.
- `chainOptions` (`:143-195`) and `chainResumeOptions` (`:202-240`) map the three into the options. They are settings flags: they must NOT join the start-only refusal list (`:217`). `--gate` + `--no-gate` together keep bind's precedence (no-gate wins; not refused).
- `ChainDoc` (`:70-76`) gains `Check string \`json:"check"\``, rendered `none` when the resolved check is empty; `chainStartedText` (`:302-314`) prints `  check: <text>` through a small `chainCheckText`. Resume's human text is deliberately unchanged.
- `registry_rows.go` chain entry: add `--gate` (after `--feature`), `--no-gate` (after `--no-feature`), `--regate` (between `--planner-actor` and `--resume`); the list stays sorted, as `TestRegistryShape` and `TestRegistryFlagsMatchFlagSets` require.
- Golden: `TestHelpJSONDocumentsTheSurface` (`cmd/relevo/contract_test.go:805`) asserts it via `assertGolden` (`:22-49`); regenerate only it with `go test ./cmd/relevo -run TestHelpJSONDocumentsTheSurface -update`, then `git diff --stat cmd/relevo/testdata` must show only `help-json.golden`.

## Tests (new, by name, with what each pins)

`internal/chain`:

- `TestNextBuilderCloseNoCheckSeedsReviewer` — a done round with `GateNone` steps to reviewing and sends the reviewer seed (pins "none behaves like green").
- `TestTraceLineDetailWords`, new row "a builder close with no check" — the detail word `no check`.
- `TestReviewerSeedSaysNoCheckRan` / `TestCorrectionSeedSaysNoCheckRan` — an empty `GateLogPath` renders `No check ran for this round.`, no `Check result:` and no path; the gated fixtures keep today's line.

`internal/relevo`:

- `TestChainStartResolvesTheBuilderCheck` — subtests: policy `gate.default`/`gate.regate` fill an unset flag; `--gate` beats the policy; `--no-gate` beats the policy; `--regate` beats the policy; no flags and no policy leaves no check; the reviewer and planner members hold no gate in every case; stored settings and `ChainResult.Check` match.
- `TestChainReviewerSeedSaysNoCheckRan` — no gate: the trace event carries `GateNone`, the staged reviewer prompt says `No check ran for this round.` and names no `GateLogPath`; a correction-seed sibling after a `changes` verdict.
- `TestChainReviewerSeedNamesTheCheckThatRan` — an armed passing gate (helper beside `chainArmFailingGate`): `Check result: green; its output is at <GateLogPath>` in the staged reviewer prompt; with a red gate that spent its regate budget, `Check result: red; its output is at <GateLogPath>`.
- `TestChainResumeGateFlagsUpdateTheBuilder` — `--resume --gate`/`--regate` write both the stored settings and the builder member; `--no-gate` clears both; a resume with none of the three leaves the stored check untouched.

`internal/relevo`, kept and must stay green (they already pin the red path): `TestChainBuilderRedGateRepairsBeforeTheReviewer`, `TestChainBuilderRedAfterRegateSeedsReviewer`, `TestChainBuilderRedWithUnchangedGateOutputSeedsReviewer`.

`cmd/relevo` — parse and refusal only; CI has no harness and no network, so the option tests call `chainFlagSet`/`chainOptions`/`chainResumeOptions` directly and the `run()` tests stop before a runtime:

- `TestChainFlagSetDefinesGateFlags`.
- `TestChainGateFlagsParseIntoOptions` — `--gate`/`--regate` land in `ChainOptions`.
- `TestChainResumeGateFlagsParseIntoOptions` — the same three land in `ResumeOptions`.
- `TestChainRegateRefusesANegativeValue` — `run()` with `--regate -1` on both arms: usage, naming `--regate`.
- `TestChainResumeGateFlagsAreSettings` — `run(["chain","--resume","--name","missing","--gate","make check"])` reaches the store and reports `binding_not_found`, not a refusal (the existing `TestChainResumeRefusesStartOnlyFlags` style).
- `TestChainDocCarriesTheCheck` and `TestChainStartedTextPrintsTheCheck` — `"check":"make check"` / `"check":"none"` and the human `check:` line (empty member list, no runtime).

## Existing tests that change, and why

1. `TestChainBuilderCloseSeedsReviewer` (`internal/relevo/chain_test.go:227`) — it closes a chain whose builder has no gate and pins `ev.Gate == GateGreen`; that expectation IS the old "green for no gate". It becomes `GateNone`, comment included.
2. `TestShowTraceRendersEveryStepInOrder` (`internal/relevo/chain_trace_test.go:41-45`) — the same no-gate chain's trace text, where `check green` becomes `no check`.

Both edits are this change's own pin, not a weakened test. `TestChainStartStoresTheResolvedSettings` needs no edit: it compares whole `chain.Settings` literals whose new fields default to zero. No other existing test pins green-for-no-gate; every other chain, bind and gate test stays untouched and green.

## Ordered steps

1. `internal/chain` gains `GateNone`, `Settings.Gate/Regate`, the trace word; add `TestNextBuilderCloseNoCheckSeedsReviewer` and the trace row; change `chainGateResult` to none and update the two existing expectations above — check: `go test ./internal/chain && go test ./internal/relevo -run 'TestChainBuilderCloseSeedsReviewer|TestShowTraceRendersEveryStepInOrder'`.
2. Seeds: templates + `chainRoundGate` + `chainSeedView` fields + `seeds_test.go` no-check cases + the two new seed tests — check: `go test ./internal/chain -run Seed && go test ./internal/relevo -run 'TestChainReviewerSeed|TestChainCorrectionSeed'`.
3. Start: options/settings/members/`Check` + `TestChainStartResolvesTheBuilderCheck` — check: `go test ./internal/relevo -run TestChainStart`.
4. Resume: options/settings/member update/`Check` + `TestChainResumeGateFlagsUpdateTheBuilder` — check: `go test ./internal/relevo -run TestChainResume`.
5. CLI: flags, mapping, doc, print, registry rows, the cmd tests, regenerate the golden — check: `go test ./cmd/relevo -run 'TestChain|TestRegistry|TestHelpJSON'` and `git diff --stat cmd/relevo/testdata` shows only `help-json.golden`.
6. Full: `make check`, then `make e2e` (the chain e2e reads the reviewer seed's first line only, so the wording change must not disturb it); write `docs/plans/2026-10-01-chain-gate.md` with this plan's text and commit the change and the doc together, as new commits.

## Deletions (closed list)

1. `chainGateResult`'s nil → `green` default (`internal/relevo/chain.go:191`).
2. The builder's inline `resolveGateFor("", false, …)` / `resolveRegate(nil, …)` in `chainBuildMembers` (`chain_start.go:424-425`).
3. The reviewer/correction seeds' unconditional `Check result:` line (`seeds/reviewer.md:6`, `seeds/correction.md:7`).

Nothing else is deleted; no test is deleted.

## Mutation checks (each names the test that must fail)

| # | behaviour | mutation | test that must fail |
|---|---|---|---|
| 1 | no gate → `none` | `chainGateResult` returns `GateGreen` for a nil record | `TestChainReviewerSeedSaysNoCheckRan`; `TestShowTraceRendersEveryStepInOrder` |
| 2 | a check that ran reaches the seeds | drop the `GateResult`/`GateLogPath` assignments in `chainSeedView` | `TestChainReviewerSeedNamesTheCheckThatRan` |
| 3 | no check says so | render the `Check result:` line unconditionally | `TestChainReviewerSeedSaysNoCheckRan` / `…CorrectionSeedSaysNoCheckRan` |
| 4 | `--gate` reaches the builder | drop `opts.Gate` in `chainSettings` | `TestChainStartResolvesTheBuilderCheck` |
| 5 | policy `gate.default` still fills an unset flag | drop the `roleChecks`/policy fallback | `TestChainStartResolvesTheBuilderCheck` (policy subtest) |
| 6 | `--no-gate` wins | ignore `opts.NoGate` | same test (no-gate subtest) |
| 7 | readers stay ungated | set the gate for every member | same test (reader assertion) |
| 8 | `--resume --gate` updates the member | skip the builder update in `chainResumeLocked` | `TestChainResumeGateFlagsUpdateTheBuilder` |
| 9 | a resume without gate flags keeps the member | apply `set.Gate` on every resume | `TestChainResumeGateFlagsUpdateTheBuilder` (keeps subtest) |
| 10 | the trace says `no check` | remove the `GateNone` case in `gateWord` | `TestTraceLineDetailWords`; `TestShowTraceRendersEveryStepInOrder` |
| 11 | `Next` still seeds the reviewer on `none` | halt on `GateNone` in `builderClosed` | `TestNextBuilderCloseNoCheckSeedsReviewer` |
| 12 | the CLI flag reaches the options | drop `Gate: *v.gate` in `chainOptions` | `TestChainGateFlagsParseIntoOptions` |
| 13 | start prints and documents the check | drop `Check` from `ChainDoc`/the started text | `TestChainDocCarriesTheCheck`; `TestChainStartedTextPrintsTheCheck` |

## Halt conditions (unverified premises)

1. If `tx.ReadLog` under the close's lock does not see the report entry just appended (the seed's check test finds an empty record) — halt; the fix then passes the close's own record down, and the report says so.
2. If the gate record turns out not to be on the report entry for every gated close — halt and report.
3. If `-update` rewrites any golden besides `help-json.golden` — halt; that drift is not this round's.
4. If `make e2e` fails on the seed wording (not the first line, which must stay) — halt and report rather than editing the harness.
5. If `make check` surfaces a third test pinning green-for-no-gate beyond the two listed, change it only if its own comment would call it old-behaviour; otherwise halt.
6. If `--resume --gate` on a chain whose builder record is gone is read to mean "skip the update" instead of failing the resume — halt and report.

## The report must include

- The `relevo` tail (`status: done`, `changed_paths`, `commands_run`, `not_done`) and the commit(s) made — new, never amended.
- The mutation table above, filled with what actually failed for each mutation, and the focused commands run per step.
- `make check` green and the `make e2e` result.
- Which existing tests changed and why (the two above), and confirmation that no other chain/bind gate test moved.
- The seed-vs-code finding: the merged wiring never filled `GateResult`/`GateLogPath`, so the pre-round seed read `Check result: ; its output is at .` (not "green"); this round fills them.
- Coverage: `scripts/check-coverage.sh` passed and the baseline was untouched (no package moved).
- Deliberately not done: the resume human line does not print the check; no regate-without-gate note on a chain start; resume does not reset `RepairCount`/`LastGateSig` when the gate command changes; the spec is not edited.
