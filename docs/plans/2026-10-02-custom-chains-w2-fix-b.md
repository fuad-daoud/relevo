# W2 review fixes, round B: rendering, closing, params, and the file: seed symlink

**Base:** your branch HEAD, after fix round A. Constraints as in every W2 round: the gate, no `--write`, no exclusions, files ≤ 600, functions ≤ 70, comments say why, new commits.

## Fix (reproduced by the reviewer or the security reader unless marked)

1. **The repair prompt names no check log after a local red check.** In `chainRenderRepairSeed` (`internal/relevo/chain_render.go:209`), the log comes from `chainRoundGate(builder, failedRound)`. Chain writers carry no gate now, so for a local check that is nil.
   - Pass the state and read the failed check's log from `state.Results[<check step>].Artifacts["log"]`, as the review seed already does (`flowCheckResult(st)`).
   - The repair prompt then names the log and its tail again.
   - Test: `TestWorkflowLocalRepairSeedNamesTheCheckLog`.
2. **Converted chains' legacy trace rows don't decode.** `ChainTrace` (`chain_trace.go:79`) and the server mirror (`chain_server_view.go:86`) decode every row as a workflow event once the chain has a workflow.
   - Decode per row: try `workflow.DecodeEvent`, and fall back to the legacy decode (`internal/chain/legacy.go`) when it rejects the kind.
   - Test: `TestChainTraceOfAConvertedChainRendersItsLegacyRows`. Run `ChainTrace` on a row converted by `ConvertLegacyChains`, not on a hand-built document.
3. **`{{step.diff}}` points at the next round's diff.** `chainCloseArtifacts` (`chain_close.go:112`) uses `b.Round`, which `queueReport` has already advanced. Use the closed round.
   - Test: `TestWorkflowCloseKeysTheClosedRoundsDiff`, with a custom workflow whose seed is `{{build.diff}}` and resolves to the diff.
4. **Params: policy fills only the shipped default** (MasterMind decision). Today `chainParamsFor` (`chain_params.go:42`) writes policy over any param named `reviewer`, `planner`, `security`, `scan`, `gate`, `regate` or `max_corrections`.
   - The new rule: policy fills those params **only for the shipped `default` workflow**. A custom workflow's own param defaults win over policy. Flags and `--param` override both. A custom workflow param with no default stays required.
   - Update the doc comment.
   - Tests: `TestCustomWorkflowParamDefaultsBeatPolicy` and `TestDefaultWorkflowParamsComeFromPolicy`.
5. **Security S1: a `file:` seed follows symlinks.** `readSeedFile` (`internal/relevo/workflow_resolve.go:83-97`) uses `os.ReadFile`.
   - `Lstat` the target, require a regular file, and open with `O_NOFOLLOW` as the race backstop, mirroring `store.DiskRegularFile`.
   - Also refuse a path whose parent directory components are symlinks that resolve outside `dir`: `filepath.EvalSymlinks` on the directory, then a containment check.
   - Test: `TestEmbedFileSeedsRefusesASymlink` (a symlink to a file outside the directory, and a symlinked subdirectory).
6. **Minor: pulled mirror members get their `chain_member` row.** `chain_pull.go:157` writes only the legacy column. Write the `chain_member` row as well, so `chainReadMembers` and `chainEndMembers` include the member.
   - Test: `TestChainPullAddedMemberGetsAMemberRow`.
7. **Minor: `workflow edit` keeps stale problem comments.** `reopenWith` prepends `# …` problem lines on each reopen, and they get saved.
   - Strip the block relevo added (mark it with a fixed first line) before reopening and before saving.
   - Test: `TestWorkflowEditDoesNotSaveItsProblemComments`.

## Mutations for the MasterMind (run each, report each, restore each)

- `readSeedFile` uses `os.ReadFile` again: `TestEmbedFileSeedsRefusesASymlink` must fail.
- `chainParamsFor` applies policy to every workflow again: `TestCustomWorkflowParamDefaultsBeatPolicy` must fail.

## Gate

- `make check` and `make e2e` green.
- Save this file as `docs/plans/2026-10-02-custom-chains-w2-fix-b.md` and commit it with the code.
