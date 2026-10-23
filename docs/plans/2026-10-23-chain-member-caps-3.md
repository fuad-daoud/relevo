# Chain member round caps, plan 3 of 4 (#1317, #1331)

HARD RULES: do not push, do not run gh, do not touch any remote or PR; relevo owns the branch. Do only this plan, and end it in its own commit.

Goal: a chain refuses to open a round for a member that has no round left, and halts the chain with the cap reason, with no round opened and no process spawned.

Behaviour:
- `Send` already refuses `b.Round > b.RoundCap` (`send.go:403`, `:536`) but the chain's send path `sendChainRound` (`chain_send.go:43`) and `chainStageRemote` (`chain_send_remote.go:41`) do not; the cap is only caught later by `reconcileHeadless` (`headless.go:804`), after the prompt entry and the process already exist. That send-then-halt left the open rows of #1331.
- The check goes in `chainSendMember` (`internal/relevo/chain_send_remote.go:23`), the one helper every member send goes through (start, advance and resume: `chain_engine.go:228`, `chain_resume_wf.go:378`), before the remote/local split: if `b.RoundCap > 0 && b.Round > b.RoundCap` return `roundCapRefusal(b.Name, b.RoundCap)` (`roundcap.go:34`). `chainFlowSendText` (`chain_engine.go:228-232`) already turns a send error into a chain halt "member X could not start: ...", so the reason carries the cap text and `errors.Is(err, ErrRoundCap)` stays true underneath.
- Keep `reconcileHeadless`'s check as the backstop for a human `Send`.

Steps:
1. Add the check in `chainSendMember`; add a short comment on why it sits before the split (no staged file, no prompt entry, no spawn). Deliverable: the helper returns the typed refusal.
2. Tests in `internal/relevo/chain_send_test.go` (or `chain_engine_flow_test.go`, whichever holds the engine send tests; fake runner, no harness):
   `TestChainSendRefusesMemberAtRoundCap` (no prompt entry, no staged prompt file, runner never started, `RoundOpen` false),
   `TestChainHaltsWithCapReasonWithoutOpeningARound` (drive a chain step whose member is at cap: chain status halted, reason contains the cap text, the member's `Round` and log unchanged),
   `TestChainSendAllowsMemberOnItsLastRound` (boundary: `Round == RoundCap` still sends).
3. Run the focused tests, `make check-affected`, commit.

Report must include: the exact halt reason string produced, and whether any existing test relied on the send-then-halt order.



Also: no issue numbers or history in code comments or test names; this plan is a new commit, never an amend or rebase. Report the commit subject, `git diff --stat`, the focused test result and the `make check-affected` result.
