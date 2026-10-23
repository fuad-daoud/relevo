# Runner liveness, plan 1 of 7 (#1310, #1330, #1311)

HARD RULES: do not push, do not run gh, do not touch any remote or PR; relevo owns the branch. Do only this plan, and end it in its own commit.

**Behaviour.** The nudge sent into a resumed opencode session also says: run
anything expected to take over 2 minutes in the foreground with an explicit
`timeout` (milliseconds) on the shell call. Other harnesses get the existing
text unchanged. Nothing else in the nudge changes (background-work-died wording
stays).

**Seams.**
- `internal/relevo/nudge.go`: `nudgePromptFormat` (lines 20-25), `nudgePrompt`
  (line 63), `nudgePromptFor` (line 74). The writer branch gets the builder's
  harness kind (`b.Builder.Kind`, "opencode"); add one constant for the extra
  sentence and append it only when the kind is opencode. Keep `nudgePrompt`'s
  signature if other callers use it (`grep -rn nudgePrompt internal cmd`); if
  they do not, change it freely. The reader branch is untouched.
- Tests in `internal/relevo/nudge_test.go` (find the existing nudge-prompt
  tests, `grep -n nudgePrompt internal/relevo/*_test.go`). Pure function tests in
  `internal/relevo`; no CLI test.

**Steps.**
1. Add the opencode-only sentence and wire it into `nudgePromptFor`.
   Done when `go test ./internal/relevo -run Nudge` passes.
2. Tests: `TestNudgePromptMentionsShellTimeoutOnOpencode` (opencode kind
   contains the timeout sentence and still names the report and done paths),
   `TestNudgePromptOmitsShellTimeoutForOtherHarnesses` (claude and agy kinds
   render exactly the old text). Mutation check: drop the kind condition, the
   second test must fail.
3. Run `make check-affected`.
4. Commit.

**Report must include:** the final nudge text for opencode, and whether any
existing test pinned the old exact text and had to change.


