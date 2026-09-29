# cmd/relevo tests: isolate XDG_DATA_HOME and the harness-identity env (#463)

## 1. System Overview

`cmd/relevo`'s `TestMain` (`cmd/relevo/main_test.go:28-39`) points `HOME`,
`XDG_CONFIG_HOME` and `XDG_STATE_HOME` at a temp root (#235). It leaves two
kinds of input inherited from the calling shell:

1. `XDG_DATA_HOME`, which `opencodeDBPath()` (`cmd/relevo/main.go:495`) reads,
   so a test reaches the real `~/.local/share/opencode/opencode.db`.
2. Harness-identity and override variables: `CLAUDECODE`, `CLAUDE_*`,
   `RELEVO_*`, `ANTIGRAVITY_*` and `TYPESAFE_API_KEY`.

Under opencode (`RELEVO_HARNESS=opencode`, and `XDG_DATA_HOME` set),
`TestAddBranchDerivesName` (`main_test.go:687`) detects a real planner and
fails. Inside Claude Code, `CLAUDE_ENV_FILE` would let a hook test append to
Claude Code's real env file. Builders run the suite from inside a harness, so
their environment differs from CI's.

The fix: move the isolation into a helper, `isolateTestEnv(root)`, which
`TestMain` calls. It sets the three existing variables plus `XDG_DATA_HOME`,
and unsets every harness-identity and override variable, so the suite starts
from CI's environment. A test pins the helper. CLAUDE.md's note is updated.

## 2. File Structure

```
cmd/relevo/main_test.go       MODIFIED  TestMain calls isolateTestEnv; new helper and TestIsolateTestEnv
CLAUDE.md                     MODIFIED  the "A cmd/relevo test never reads..." bullet names the new isolation
docs/plans/2026-09-25-test-env-isolation.md   NEW  this plan, committed with the change
```

Nothing else changes. No production code changes. Do not edit
`TestAddBranchDerivesName`: its explicit `t.Setenv` clears become redundant
but stay harmless.

## 3. Data Structures & Type Definitions

**The unset rule** (a closed rule, defined once in the helper): unset an
environment variable when its NAME

- equals `CLAUDECODE`, or
- starts with `CLAUDE_`, or
- starts with `RELEVO_`, or
- starts with `ANTIGRAVITY_`, or
- equals `TYPESAFE_API_KEY`.

Enumerate `os.Environ()` and split each entry on the first `=`. Nothing else
is unset. In particular `PATH`, `GOFLAGS`, `GOCACHE`, `TMPDIR` and `GIT_*`
survive, because the build and git-using tests need them.

**The set rule:** `HOME=root`, `XDG_CONFIG_HOME=root/config`,
`XDG_STATE_HOME=root/state`, `XDG_DATA_HOME=root/data`. The first three are
exactly what `TestMain` sets today.

## 4. Interface Definitions & Component Contracts

`func isolateTestEnv(root string)` lives in `cmd/relevo/main_test.go`, since
it is test-only.

- Precondition: `root` is an absolute directory path.
- Postcondition: the four set-rule variables equal their values, and no
  variable matching the unset rule is present in `os.Environ()`.
- It never fails. It ignores `os.Setenv` and `os.Unsetenv` errors, as
  `TestMain` does today.
- Doc comment: says it makes the package start from CI's environment
  whatever harness runs `go test`, names #235 and #463, and lists the rules.

`TestMain` becomes: create the temp root (unchanged), call
`isolateTestEnv(root)`, run, remove the root, exit. Update `TestMain`'s doc
comment (lines 24-27) so it mentions `XDG_DATA_HOME` and the harness variables.

**`func TestIsolateTestEnv(t *testing.T)`.** Place it directly after
`TestMain`. It is a pure environment test: no harness, no network, and no
`run()` call (CLAUDE.md's CI rule).

```
first, t.Setenv every variable this test touches, so cleanup restores
  TestMain's environment afterwards:
    HOME, XDG_CONFIG_HOME, XDG_STATE_HOME, XDG_DATA_HOME  -> "/polluted"
    CLAUDECODE=1, CLAUDE_ENV_FILE=/polluted/env, CLAUDE_PID=1,
    RELEVO_HARNESS=opencode, RELEVO_PLANNER=p, ANTIGRAVITY_CONVERSATION_ID=x,
    TYPESAFE_API_KEY=k
    CLAUDEX_KEEP=1            -- near-miss: no underscore after CLAUDE, so it must survive
    RELEVO=1                  -- near-miss: no trailing underscore, so it must survive
root = t.TempDir()
isolateTestEnv(root)
assert HOME == root, XDG_CONFIG_HOME == root/config, XDG_STATE_HOME == root/state,
       XDG_DATA_HOME == root/data
for each polluted identity var above: os.LookupEnv reports it absent
assert CLAUDEX_KEEP and RELEVO are still present
assert no entry in os.Environ() matches the unset rule
```

Note on `t.Setenv` semantics: it restores the value from *before* the
`t.Setenv` call, so every variable that `isolateTestEnv` changes must have
been `t.Setenv`'d first, or the test would leak its changes into later tests.
The list above covers everything the helper touches. The helper may also
unset variables inherited from the real environment. That's fine: `TestMain`
already cleared them.

## 5. High-Level Pseudocode

```
isolateTestEnv(root):
  for each "NAME=value" in os.Environ():
    if matchesUnsetRule(NAME): os.Unsetenv(NAME)
  os.Setenv HOME, XDG_CONFIG_HOME, XDG_STATE_HOME, XDG_DATA_HOME per section 3
```

`matchesUnsetRule` may be a small unexported test-file function, or it can be
inlined. Either is fine.

**CLAUDE.md**, bullet under "Merging and CI" that starts with
`- A cmd/relevo test never reads the user's real config or state:`. Replace
its first sentence so the bullet reads:

> - A cmd/relevo test never reads the user's real config, state or data, and
>   never sees the calling harness: the package's TestMain points HOME,
>   XDG_CONFIG_HOME, XDG_STATE_HOME and XDG_DATA_HOME at a temp root and
>   unsets CLAUDECODE, CLAUDE_*, RELEVO_*, ANTIGRAVITY_* and TYPESAFE_API_KEY
>   (#235, #463). A test that needs its own config writes it under a
>   t.TempDir() it sets as XDG_CONFIG_HOME; a test that needs a harness
>   variable t.Setenv's it.

## 6. Error Handling Strategy

There are no runtime error paths; this is test setup only. If unsetting these
variables breaks an existing test, that test was relying on the calling
shell's environment, which CI does not have. Do not re-export anything to
make it pass. Halt and report the test name and failure.

## 7. Working Efficiently

Each model step costs a round trip, so:
- Read `cmd/relevo/main_test.go` lines 1-60 and 680-702, and CLAUDE.md's
  "Merging and CI" section, in one parallel step.
- Make the `main_test.go` change in one edit (TestMain, its comment, the
  helper and the new test together), and the CLAUDE.md change in one edit.
- Focused check: `go test ./cmd/relevo/ -run 'TestIsolateTestEnv|TestAddBranchDerivesName' -count=1`.
- Full check, once, at the end: `make check`. If this machine blocks heavy
  commands, use `dev run make check`. If `dev run` fails on a missing `dist/`
  or `.git`, say so and rely on the PR's CI.

If any step is impossible as written or contradicts the code, stop and report.
Do not bend the plan to fit.

## 8. Ordered Implementation Steps

**Step 0: sync.** `git fetch origin && git merge --ff-only origin/main`.
Confirm `TestMain` at `cmd/relevo/main_test.go` still sets exactly `HOME`,
`XDG_CONFIG_HOME` and `XDG_STATE_HOME`. If not, halt.

**Step 1: reproduce the reported failure (best effort).** Run
`RELEVO_HARNESS=opencode go test ./cmd/relevo/ -run TestAddBranchDerivesName -count=1`
with your real `XDG_DATA_HOME` set, for example
`XDG_DATA_HOME=$HOME/.local/share`. Record whether it FAILS. It fails only
where a real opencode database has a session for this directory. If it
passes here, note that in the report and continue. It is not a halt.

**Step 2: implement.** Make the section 4 and 5 changes to `main_test.go`.
Verification: the focused check passes, and the Step 1 command now passes.

**Step 3: pin the helper.** Mutation check: temporarily remove the
`ANTIGRAVITY_` prefix from the unset rule and confirm `TestIsolateTestEnv`
fails. Then remove the `XDG_DATA_HOME` set and confirm it fails. Restore both.
Report both failures.

**Step 4: CLAUDE.md.** Make the section 5 edit.

**Step 5: full check.** `make check`. It must pass. Also run it once with
`CLAUDECODE=1 RELEVO_HARNESS=opencode RELEVO_PLANNER=x go test ./cmd/relevo/ -count=1`.
It must pass, since the suite now ignores the caller's harness.

**Step 6: ship.** Copy this plan to
`docs/plans/2026-09-25-test-env-isolation.md` if it isn't there already.
Commit all three files as one commit,
`test(cmd): isolate XDG_DATA_HOME and harness-identity env in TestMain (#463)`,
with `Fixes #463` in the body. Push, and open a PR against `main` whose body
has `Fixes #463`. Don't merge. Never use `git stash`: the stash stack is
shared with other sessions. Use a temporary commit or a file copy instead.

The report states the Step 1 result, the Step 3 mutation failures, the Step 5
results, the PR number and `git diff --stat`.
