HARD RULES: do not push, do not run `gh`, do not touch any remote or PR; relevo owns the branch. Do only this plan, and end it in its own commit.

Design: `docs/specs/2026-10-23-candidate-windows-design.md` in this tree; every § reference points there. Read the sections this plan cites before editing.

# Plan 6 — re-pick between rounds

Deliverable: a binding follows the window across round boundaries, and never
mid-round.

1. Turn `repickStale` into `repickForRound` (`internal/relevo/stale.go:10-38`):
   the stale branch it has today, plus the window branch of §1.5 (no token
   named, not pinned, at least one window configured, resolution differs).
2. Call it from the three seams: `sendPreflight` (`send.go:325-334`),
   `chain_send` (`chain_send.go:85`), `startRepairRound` (`repair.go:153`).
3. Print the reason on the pick line (`send.go:668-675`): a window re-pick says
   which candidate it left and why, like the stale line does.
4. Make the advisory gate and the dry-run note kind-aware
   (`send.go:449-457`, `:876-882`): a window or a cap says the round runs here
   and the next round re-picks; `RateLimited` and `RolesMissing` are unchanged.
5. Write `TestSendRepicksOffAClosedWindow`, `TestSendRepicksOntoAnOpenedWindow`,
   `TestNoWindowConfiguredNeverMovesARateLimitedBinding`,
   `TestPinnedBindingDoesNotRepick` (the pin is plan 7's, already landed),
   `TestChainMemberAndRepairRepickForWindows`,
   `TestServedSendRepicksOnTheServer`, `TestDryRunWindowNoteDoesNotClaimASwitch`,
   and `TestWindowGateDoesNotSwitchARunningRound` +
   `TestRateLimitStillSwitchesWithAWindowGateLive` in
   `internal/relevo/switch_test.go`.
6. Run `go test ./internal/relevo/... -run 'Repick|Window|DryRun'` — green, and
   `TestWindowGateDoesNotSwitchARunningRound` fails if `gatedBuilder` is widened
   to any gate kind.

Commit: `relevo: re-pick the builder for a window between rounds`.
