# Plan 2, finishing round: a loaded workflow carries its stored definition

You halted round 3 correctly, at its own halt clause. Your diagnosis is right, and the MasterMind authorizes the fourth-file change it needs.

## Base

Your worktree, as you left it:
- HEAD `af63901b`;
- your four uncommitted files from round 3 (the step 1–3 work).

Keep them. They are correct.

## The real defect (yours, confirmed)

`config.Load` builds `StoredWorkflow` through `parseWorkflowEntry` (`internal/config/workflows.go:73-95`). That returns `workflow.Parse(entry.Source)`, the `file:` literal. The stored JSON body, which holds the embedded seed, is used only for the `definitionMatches` / `syncFileSeeds` check and is then discarded.

So every reader of a loaded workflow sees a bare `file:<path>`:
- `show --json`;
- `edit`;
- chain start, from round 7 on.

## Change (authorized: `internal/config/workflows.go`, plus its tests)

1. `parseWorkflowEntry` returns `StoredWorkflow{Source: entry.Source, Definition: <the stored JSON body's definition>}`.
   - The body must still pass the existing source-vs-definition check (`definitionMatches` / `syncFileSeeds`) before it is returned.
   - A body that fails the check is refused exactly as today.
   - Keep the function within 70 lines, and the file within 600.
2. Tighten the check the reviewer flagged as too loose. For a step whose source seed is a `file:` reference, the stored definition's seed must NOT itself be a `file:` literal: a stored workflow always carries embedded contents. A body that regressed to the literal is refused with a problem naming the step.
3. Tests, in `internal/config`'s workflows tests:
   - `TestLoadReturnsTheEmbeddedFileSeed`: an add-shaped entry whose source says `file:prompt.txt` and whose body embeds `"hello seed\n"` loads with `Definition.Steps["build"].Seed == "hello seed\n"`.
   - `TestLoadRefusesABodyThatRegressedToTheFileLiteral`.
4. Then finish your round-3 plan from step 3. `TestConfigWorkflowEditKeepsEmbeddedFileSeed` must now pass, along with `TestConfigWorkflowEditSavesAChange`. `relevo config workflow show custom --json` after `add` must print the embedded contents. Check it with temp XDG roots, as you did.
5. Step 4 of your round-3 plan:
   - **The mutation.** Make `parseWorkflowEntry` return the re-parse of the source again. `TestLoadReturnsTheEmbeddedFileSeed` and `TestConfigWorkflowEditKeepsEmbeddedFileSeed` must both fail. Report it, then restore.
   - `make check` and `make e2e` green.
   - Never run `check-coverage.sh --write`. Never touch `testdata/coverage-baseline.txt`.
   - Commit as new commits, including this plan saved as `docs/plans/2026-10-02-custom-chains-w2-r2-fix.md`.

## Halt if

- The stored body cannot be trusted without a schema or format change. Report what is missing.
- Any test outside `internal/config`, `internal/relevo/workflow_edit*` and `cmd/relevo/config_workflow*` has to change.
