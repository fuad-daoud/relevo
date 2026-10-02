> MasterMind: preamble A option 2 (the shipped default keeps legacy member names) is ACCEPTED, and so is preamble C's default (keep the repeated-red pin). These are manual finishing rounds for halted plan 8, sent one at a time; your branch HEAD is your base. Do only your round. Save the preamble plus your round's section to the docs/plans file it names.

# Plan: rest of custom chains W2 plan 8, in five rounds (8b–8f)

Base: branch `relevo/cc-w2` at `3784f009`, with plan 8 step 1 (`ConvertLegacyChains`) committed. I read the code from `/home/fuad/.cache/relevo-chains/cc-w2-tree`. Every line number below is at that commit.

## Preamble

### A. Member names: choose option 2

**What decides it.**
- Members are found by actor, not by name. `chainFlowMemberName` (`internal/relevo/chain_engine.go:50-61`) looks up the `chain_member` row by actor. `ConvertLegacyChains` keeps every converted row's old binding names (`chain_convert.go:129-146`).
- So the engine does not care what a member is called. The name is only a creation-time rule, and the test churn follows from that rule alone.

**Churn counts.** These count matching lines from `grep -c` over the tree, so a few noise hits like `fix-plan` are included.

| | Option 1: `<chain>-<actor>` everywhere | Option 2: legacy names for the shipped default |
|---|---|---|
| Test lines asserting `-rev`/`-plan`/`-sec` that must change | About 300, in about 22 files: `relevo/chain_test.go` 103, `chain_secdiff_test.go` 26, `chain_resume_test.go` 21, `chain_send_test.go` 19, `chain_start_test.go` 14, `chain_served_test.go` 13, `chain_status_test.go` 12, `chain_verbs_test.go` 12, `chain_pull_test.go` 10, `chain_wait_test.go` 9, `chain_server_start_test.go` / `chain_server_view_test.go` / `chain_trace_test.go` / `show_test.go` 8 each, `chain_sweep_test.go` 7, e2e 14, `cmd/relevo` about 18, `serve/chains_test.go` 4 | About 8, in 2 files: `chain_members_test.go:30-35,74-88` and `chain_dryrun_test.go:45-46,67`. These pin the Q1/Q3 answers that option 2 reverses for the default. |
| Non-test sites | `chainMembersFor` (`chain_start.go:401-411`), the legacy security add (`chain_resume.go:463`), the server cap (`serve/chains.go:30`), the client mirror naming (`chain_server_start.go:95`) | None. These already produce the legacy names. |
| Chain-name cap | 27 drops to 19, a visible change, and the client and server caps disagree | Stays 27 for the default. A custom workflow gets 32 − 1 − its longest actor. |
| Resume on a converted row | **Bug.** `chainResumeAddMembers` (`chain_resume_wf.go:207-215`) keys `have` by *binding name*. A converted `shop-rev` does not match a planned `shop-reviewer`, so a resume would create a second reviewer member. | Names match, so no duplicate. |
| Client `--server` against an old-binary server | The two sides name members differently | They agree |

Option 1 costs about 300 assertion edits for no gain and adds a duplicate-member bug and a cap change. **I recommend option 2.**

**Where the rule lives.** One helper in `internal/relevo/chain_members.go`, used by both `chainMemberNames` (`:23-40`) and `chainWorkflowMembers` (`chain_start_wf.go:192-215`). The rule:
- It applies only when `def.Name == workflow.Default().Name`. For such a definition, a non-keeper member whose actor equals the string value of param `reviewer`, `planner` or `security` takes the suffix `-rev`, `-plan` or `-sec`.
- When one actor fills several parts, the first in reviewer → planner → security order wins. That is the order `chainLegacyColumns` (`chain_convert.go:19-21`) walks.
  - Today `chainWorkflowMembers:193-198` lets the *last* part win. The shared helper replaces that loop.
- Every other member, and every workflow with another name, takes `<chain>-<actor>`.
- A user workflow saved over the name `default` with `--force` replaces the default, so it inherits the legacy names. That is intended.

**How a converted row and a new row agree.**
- `FromLegacy` builds its definition as `WithParams(Default(), …)` (`workflow/legacy.go:79-99`). A converted row's stored definition therefore carries the name `default` and the reviewer, planner and security params from its settings.
- So `chainMemberNames` on that stored definition gives exactly the names the legacy start gave (`chainMembersFor`).
- Resume's add-members path (`chain_resume_wf.go:197`) calls the same function, so `--param scan=true` on a converted row with no security member creates `<chain>-sec`.
- `chainNameCap` is computed from the members' actual suffixes (the longest of `len(name) − len(chain)`), not from actor length.

### B. How the rounds are split, and the one interim dual path

**Start order.** `ChainStart` already runs the engine when `opts.Workflow != ""` (`chain_start.go:155-157`). The split relies on that:
- **8b** puts the shipped default on legacy names and moves the local and remote e2e onto the engine with `Workflow: "default"`. This proves the e2e pins on the engine before anything switches.
- **8c** does the same for the largest unit-test files, through a test helper that sets `Workflow: "default"`.
- **8d** switches the default start, removes that helper, rewrites the rest of the local tests, and deletes the legacy *start*.
- **8e** moves served chains onto the engine.
- **8f** moves pending-send and the read surfaces onto the state.

**Interim dual path, and how plan 9 removes it.**
- The `len(c.WorkflowJSON) == 0` dispatch stays in every caller through plan 8. Plan 8 step 2 said to remove it; the split keeps it. The sites are `chain.go:213`, `chain_resume.go:100`, `chain_sweep.go:71`, `chain_trace.go:79`, `chain_verbs.go:66`, `chain_status.go:41`, `chain_check.go:136,171` and `reconcile.go:440`.
- After 8d, a legacy row is reached only by:
  - a served chain on a server, until 8e;
  - a pulled mirror, which `chainOnServer` never advances;
  - a test that plants a row with `testChainRow`.
- After 8e, nothing creates one.
- Plan 9 deletes, together with the old `Next`:
  - the dispatch branches;
  - the legacy `chainApply` arm (`chain.go:224-…`);
  - `applyChainLegacy` and the legacy column projection;
  - the tests that plant legacy rows.

**Legacy columns.** `applyChainLegacy` (`chain_engine.go:272-287`) still projects the state onto the row's phase, step, plan, corrections and awaiting columns on every save.
- So **chain-row** column assertions stay valid in plan 8, unless a test finds a difference. That difference is then judged as behaviour or vocabulary (see the halts).
- Only **trace-row** assertions change vocabulary. New rows store the step id in `Step`, an empty `Phase`, and `workflow.EncodeEvent`/`EncodeAction`.

### C. One design gap to decide before 8c (default given)

- `TestChainBuilderRedWithUnchangedGateOutputSeedsReviewer` (`chain_test.go:509`) pins a behaviour the legacy path has and the engine does not: a red gate whose output signature matches the previous red's skips the remaining repair budget. The legacy code is `repairDecision`/`gateSignature` (`chain.go:246-248`, `repair.go`). Nothing in `internal/workflow` or the engine files has it, and the spec does not mention it.
- **Default:** keep the pin.
  - `workflow.Event` (`workflow/state.go:85-96`) gains a flag saying a red check's output repeats the previous red.
  - Entering a budgeted step on such an event counts as over budget, so the step's `then` target is taken. The seams are `applyBudget` (`workflow/enter.go:42-44,103-…`).
  - The relevo adapter sets the flag where it feeds `check_closed`, for both the local run and the pulled check (`chainFlowPullCheck`, `chain_engine.go:202-225`). It compares `gateSignature` of the new log with that of `State.Results[<check step>].Log` from before the event, so no new storage is needed.
- **Alternative:** list it as deleted behaviour. If you want that, say so before 8c, and 8c step 3 becomes a deletion entry.

### Constraints every round obeys

- **Gate:** `make check` and `make e2e` are green at the round's end. Run the round's focused command first.
  - Never run `scripts/check-coverage.sh --write` or touch `testdata/coverage-baseline.txt`.
  - Add no lint, comment or filesize exclusion.
- **Size:** files ≤ 600 lines and functions ≤ 70. `chain.go` has 4 lines to spare and `chain_start.go` 15, so new code goes in new files.
- **Comments:** they say why. No history, issue numbers, "W2", "round N" or "used to" in code or tests.
- **CLI tests:** no `cmd/relevo` test runs a subcommand that spawns a harness or reaches the network. Rules are tested as pure functions in `internal/relevo`.
- **Commits:** new commits on `relevo/cc-w2`. Never amend or rebase a pushed commit. Each round's last step saves its own section (this preamble plus the round) to `docs/plans/2026-10-02-custom-chains-w2-r8<letter>.md` and commits it with the code.
- **Coverage risk:** moving tests off the legacy path, while that code stays until plan 9, can lower `internal/relevo` coverage (baseline 85.2).
  - Each round records `go test -cover ./internal/relevo/` before and after.
  - A drop of more than one point is a halt, reported with both numbers, not a baseline edit. The MasterMind's fallback is to pull plan 9's deletion of the legacy apply path forward.
- **Every round's report includes:**
  - the focused and full command outputs;
  - `git diff --stat` compared with the round's declared files;
  - every new test by name;
  - every changed assertion as old → new, grouped by file;
  - every gap the engine had to close, with its file and line;
  - the coverage numbers before and after;
  - every deviation from the plan, with its reason.

---

## Round 8b

Legacy member names for the shipped default, and the local and remote e2e moved onto the engine.

**Files:**
- `internal/relevo/chain_members.go`
- `internal/relevo/chain_start_wf.go`: `chainWorkflowMembers` at 192-215, and the cap refusal at 88-90
- `internal/relevo/chain_members_test.go`
- `internal/relevo/chain_dryrun_test.go`
- one new test file, `internal/relevo/chain_members_default_test.go`
- `internal/e2e/chain_test.go`
- `internal/e2e/chain_remote_test.go`

**Steps**
1. **Name rule.** Add the default-name helper (preamble A) to `chain_members.go`. Use it in `chainMemberNames` and `chainWorkflowMembers`, deleting the latter's own parts loop.
   - Done when `TestMemberNamesBuilderKeepsChainName` (rewritten to `shop`, `shop-rev`, `shop-plan`, `shop-sec`) passes, along with the new tests:
     - `TestMemberNamesCustomWorkflowUsesActorSuffix`: a definition not named `default` that declares a `reviewer` param gives `shop-reviewer`;
     - `TestMemberNamesDefaultSharedActorTakesFirstPart`.
2. **Cap.** `chainNameCap` takes the longest real suffix. Both refusals read "exceeds N characters (the longest member suffix is -X)", which is the legacy wording (`chain_start.go:358`).
   - Rename `TestChainNameCapFromLongestActor` to `TestChainNameCapFromLongestMemberSuffix`, with the default at 27 and a custom workflow with `lite-planner` at 19.
   - Every workflow-path test asserting the old "longest actor it runs" wording is updated and listed in the report.
3. **Converted and new rows agree.**
   - `TestConvertedRowAndNewRowNameMembersAlike`: convert a legacy row whose members are `shop`, `shop-rev`, `shop-plan` and `shop-sec`. `chainMemberNames` on its stored definition returns those four names.
   - `TestResumeAddsSecurityMemberOnConvertedRowAsSec`: a converted row with scan off, resumed with `scan=true`, gains exactly one member, `shop-sec`.
4. **Dry run.** `chain_dryrun_test.go:45-46,67` expects `shop-rev` and `shop-plan`.
5. **E2E.** `TestChainE2E` and `TestChainRemoteBuilderE2E` pass `Workflow: "default"` to `relevo.ChainStart`.
   - Rewrite their trace expectations (`chain_test.go:311-321,414`; `chain_remote_test.go:207-211,240`) to `workflow.DecodeEvent`/`DecodeAction` kinds and step ids.
   - Keep every pin:
     - the send sequence per member;
     - the seed contents;
     - the single delivery;
     - the stream-read verdict;
     - the plan advance;
     - the correction reset;
     - the remote gate and its repair, now as the `repair` step answered from the pulled gate.
   - Chain-row `Phase`, `Plan` and `Corrections` assertions stay as they are, since they are projected.
6. Save this section as `docs/plans/2026-10-02-custom-chains-w2-r8b.md` and commit it with the code.

**Commands**
- Focused: `go test ./internal/relevo/ -run 'MemberNames|ChainNameCap|DryRun|Convert|ResumeAdds' -count=1`, then `go test ./internal/e2e/ -run 'TestChainE2E|TestChainRemoteBuilderE2E' -count=1`
- Full: `make check`, then `make e2e`

**MasterMind mutation:** drop the `def.Name == workflow.Default().Name` condition from the rule. `TestMemberNamesCustomWorkflowUsesActorSuffix` must fail.

**Halt if**
- an e2e pin, not just its vocabulary, would change. Examples: a different member receives a round, a round count changes, or a seed loses a path it named;
- a projected chain-row column differs from what the legacy path wrote at the same point;
- the remote e2e's repair cannot be expressed as the `repair` step.

**Deleted:** nothing. The Q1/Q3 answers for the shipped default are reversed: names are `-rev`, `-plan` and `-sec`, and the cap is 27.

**Report also lists:** each e2e trace row as old → new.
