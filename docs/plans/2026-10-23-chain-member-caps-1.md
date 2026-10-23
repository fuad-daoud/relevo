# Chain member round caps, plan 1 of 4 (#1317, #1331)

HARD RULES: do not push, do not run gh, do not touch any remote or PR; relevo owns the branch. Do only this plan, and end it in its own commit.

Goal: every chain member, not only the writer, gets a round cap scaled from the chain's plans, max-corrections and regate.

Behaviour:
- Per plan a member can take: builder `1+MaxCorrections+Regate` (build, one rebuild per correction, one repair per regate);
  reviewer `1+MaxCorrections+Regate` (one review per builder round, conservatively); planner `MaxCorrections+1` (one plan per
  correction, plus the security fix plan); security `1+MaxCorrections`. A part the table does not know (a custom workflow's
  actor) takes the builder's count. Cap = `plans * perPlan + slack`, floor `store.DefaultRoundCap` (never lowered below 20).
- Decision to keep: the formula's inputs are the same three chain facts (plans, `set.MaxCorrections`, `set.Regate`); no new key.

Seams and steps:
1. `internal/relevo/roundcap.go:30-48`: replace `chainWriterRoundCap(plans, set)` with `chainMemberRoundCap(part string, plans int, set chain.Settings) int`
   (rename `chainWriterCapSlack` to `chainMemberCapSlack`). `part` is a `chain.MemberBuilder|Reviewer|Planner|Security` string
   (`internal/chain/legacy.go:71-74`). Deliverable: one pure function; `go build ./...` shows every old caller now fails to compile.
2. Update the four callers, each setting the cap on *every* member, not only `m.writer`:
   - `internal/relevo/chain_start.go:357-364` (`chainBuildMembers`): move `b.RoundCap = chainMemberRoundCap(m.part, plans, set)` out of the `if m.writer` block
     (keep Gate/Regate inside it).
   - `internal/relevo/chain_served.go:304` (`servedChainMember`) and `:223` (`servedFacts.writerCap`): replace the single `writerCap` field with a call
     `chainMemberRoundCap(m.part, f.plans, f.settings...)`; give `servedFacts` the plan count and `chain.Settings` it needs (it already carries `gate`, `regate`).
     The `linkKey` workflow case uses `m.actor`; `m.part` is still the cap's key.
   - `internal/relevo/chain_remote.go:225`: the create request is the writer's: pass `chainMemberRoundCap(chain.MemberBuilder, ...)`.
   - `internal/relevo/chain_resume_swap.go:189,193` already go through `chainBuildMembers`: no change beyond compiling.
3. Tests in `internal/relevo/roundcap_test.go` (add; it is a pure function, no harness, no network):
   `TestChainMemberRoundCapScalesEveryPart`, `TestChainMemberRoundCapNeverBelowDefault`, `TestChainMemberRoundCapUnknownPartTakesBuilderCount`.
   Add one binding-level test next to the existing chain start tests (`chain_start_test.go`): `TestChainStartGivesEveryMemberAScaledCap`
   (a chain of 7 plans, max-corrections 3: the stored reviewer cap is above 20 and equals the formula; planner and security too).
   Update any existing test that named `chainWriterRoundCap`.
4. Run `go test ./internal/relevo/ -run 'ChainMemberRoundCap|ChainStartGivesEveryMember|ChainStart|ChainServed|ChainRemote' -count=1`, then `make check-affected`, then commit.

Report must include: the per-part table you used, any caller you could not convert, `git diff --stat`.



Also: no issue numbers or history in code comments or test names; this plan is a new commit, never an amend or rebase. Report the commit subject, `git diff --stat`, the focused test result and the `make check-affected` result.
