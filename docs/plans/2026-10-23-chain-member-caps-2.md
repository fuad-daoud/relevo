# Chain member round caps, plan 2 of 4 (#1317, #1331)

HARD RULES: do not push, do not run gh, do not touch any remote or PR; relevo owns the branch. Do only this plan, and end it in its own commit.

Goal: `relevo chain --resume` (with or without `--max-corrections`/`--regate`) leaves every local member with a cap at least the recomputed one, so a chain started before plan 1, or resumed with a larger budget, is not stranded.

Behaviour:
- On a resume, after the settings are resolved, each local member binding's `RoundCap` becomes `max(current, chainMemberRoundCap(part, plans, set))`. Never lowered, so a hand-set higher cap survives.
- A member halted by the cap (state NEEDS YOU with the cap reason) is not touched here; the resume's own send revives it (`sendChainRound` sets it active), and the raised cap means `reconcileHeadless` (`headless.go:804`) no longer re-halts it.

Seams and steps:
1. New function `chainResumeRaiseCaps(tx *store.Tx, c db.ChainRow, plans int, set chain.Settings) error` in a new file `internal/relevo/chain_resume_caps.go`
   (`chain_resume_wf.go` is near the 600-line limit; do not add to it). It walks `chainFlowMembers(tx, c)` (`chain_engine.go:94`), loads each binding,
   skips `b.Owner != ""` and remote (`b.Builder.Remote()`) members, derives `part` with `chainPartOf(c, name)` (same helper `chain_send.go:chainStepNote` uses),
   and `tx.Save`s only when the cap rose.
2. Call it in `chainResumeLocked` (`internal/relevo/chain_resume_wf.go:~203`) right after `row.SettingsJSON = settingsJSON`, passing
   `len(before.Iter["plans"].Items)` and `set`. A failure goes through `snap.restore(tx, err)` like its neighbours (check `chainResumeSnapshot` in
   `chain_resume_undo.go` covers member bindings; if it does not, raise the caps *after* the existing member writes instead, and say so in the report).
3. Server chains (`chainOnServer(c)` -> `chainServerResume`, `chain_resume.go:63`) and placed writers are out of scope: leave them and list in the report that their cap is fixed
   at create until a wire change exists (compare `resumeRemoteGatePush`). Do not add a protocol field.
4. Tests (pure store-level, in `internal/relevo/chain_resume_caps_test.go`, using the helpers in `chain_flow_helpers_test.go` / `chain_resume_test.go`;
   none spawns a harness):
   `TestChainResumeRaisesMemberCapsToRecomputed`, `TestChainResumeWithMaxCorrectionsRaisesCaps`, `TestChainResumeNeverLowersAMemberCap`,
   `TestChainResumeLeavesServedMemberCapAlone`.
5. Run the focused tests, `make check-affected`, commit.

Report must include: whether the snapshot covered member saves, the server-chain gap stated plainly.



Also: no issue numbers or history in code comments or test names; this plan is a new commit, never an amend or rebase. Report the commit subject, `git diff --stat`, the focused test result and the `make check-affected` result.
