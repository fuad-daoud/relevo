# #463 round 2: rebase PR #465 over #464's TestMain change

## 1. System Overview

PR #465 (branch `relevo/test-env`, one commit `535094c5`) conflicts with
`main` after #464 (`499b6e1e`) merged. #464 made one change to
`cmd/relevo/main_test.go`: in `TestMain`, a loop that `os.Unsetenv`s six
names (`RELEVO_PLANNER`, `RELEVO_HARNESS`, `CLAUDECODE`, `CLAUDE_PID`,
`CLAUDE_CODE_SESSION_ID` and `ANTIGRAVITY_CONVERSATION_ID`), plus a
four-line paragraph added to `TestMain`'s doc comment.

#465's `isolateTestEnv` unsets a superset: every one of those six names
matches `matchesUnsetRule`, as `CLAUDECODE` or by the `CLAUDE_`, `RELEVO_` or
`ANTIGRAVITY_` prefix. The resolution keeps #465's design and drops #464's
now-redundant loop.

## 2. File Structure

```
cmd/relevo/main_test.go                         MODIFIED  conflict resolved per section 5
docs/plans/2026-09-25-test-env-isolation-r2.md  NEW       this plan
```

`CLAUDE.md` and the round-1 plan keep their #465 content unchanged.

## 3. Data Structures & Type Definitions

None.

## 4. Interface Definitions & Component Contracts

Unchanged from round 1: `isolateTestEnv(root)`, `matchesUnsetRule(name)` and
`TestIsolateTestEnv`. One addition: `TestIsolateTestEnv` also `t.Setenv`s
`CLAUDE_CODE_SESSION_ID` (to any value) and asserts it's unset afterwards, so
every name #464 listed is pinned by the test.

## 5. High-Level Pseudocode

The resolved `TestMain` is exactly #465's form:

```
root = os.MkdirTemp(...)   (panic on error)
isolateTestEnv(root)
code = m.Run(); os.RemoveAll(root); os.Exit(code)
```

There is no separate unset loop; #464's loop is deleted because
`isolateTestEnv` covers it.

`TestMain`'s doc comment is #465's text, followed by #464's paragraph. Keep it
verbatim, except change its last sentence to point at the helper:

> It also clears the planner identity a harness injects into the shell that
> runs the tests (a Claude Code session, an agy conversation, an OpenCode shell
> marked by the relevo plugin's server hook), so no test resolves the planner
> of whoever happens to run `go test`. isolateTestEnv holds the rule; a test
> that needs one of these variables sets it itself.

## 6. Error Handling Strategy

If the rebase conflicts anywhere other than the `TestMain` region of
`cmd/relevo/main_test.go`, halt and report the conflicting files and hunks.

## 7. Working Efficiently

- `git fetch origin && git rebase origin/main`, resolve the single conflict
  in one edit, then `git add` + `git rebase --continue`.
- Focused check: `go test ./cmd/relevo/ -run 'TestIsolateTestEnv|TestAddBranchDerivesName' -count=1`.
- Full check, once: `make check`.
- Never use `git stash`: the stash stack is shared with other sessions.

If any step is impossible as written or contradicts the code, stop and report.
Do not bend the plan to fit.

## 8. Ordered Implementation Steps

**Step 1: rebase.** In the worktree on `relevo/test-env`, rebase onto
`origin/main`. It must include `499b6e1e`. Resolve per section 5, and add the
`CLAUDE_CODE_SESSION_ID` assertion from section 4. Verification:
`grep -c 'os.Unsetenv(v)' cmd/relevo/main_test.go` is 0, and TestMain calls
`isolateTestEnv(root)`.

**Step 2: check.** Run the focused check, then `make check`. Both must pass.
Also run
`CLAUDECODE=1 RELEVO_HARNESS=opencode RELEVO_PLANNER=x go test ./cmd/relevo/ -count=1`.
It must pass.

**Step 3: ship.** Add this plan as
`docs/plans/2026-09-25-test-env-isolation-r2.md` in a second commit,
`docs(plans): #463 round 2, rebase over #464`. Then
`git push --force-with-lease origin relevo/test-env`. Verification:
`gh pr view 465 --json mergeable --jq .mergeable` prints `MERGEABLE` (retry
for up to a minute while it says `UNKNOWN`). Don't merge.

The report states the resolved `TestMain` (verbatim), the check results, and
the `mergeable` value.
