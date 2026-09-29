# A5 R4a (fresh base with R1, R2 and R3; earlier attempts lacked them): a reader round runs in a scratch copy, with a reader prompt

Spec:
- `docs/specs/2026-09-24-cockpit-design.md` D5, D6 and §3.4;
- the owner decisions in A5: local-only readers; a reader may share a writer's tree;
  reader agents write only into their artifact directory.

These are in `main` now:
- R1: artifact dirs, `store.ArtifactDir`, `SummaryPath` and `ArtifactRel`;
- R2: `relevo.CreateScratchFrom`, `RemoveScratch`, `SweepScratch` and
  `Store.ScratchWorktreePath`;
- R3: reader bindings, with `Binding.Shape == "reader"`, and `send` refusing them with
  `ErrReaderRoundsNotYet`.

**This round (R4a):** starting a reader round. **The next round (R4b):** closing it (the
summary, cleanup and e2e). Keep R4a to launch only; anything about close is R4b's.

**If a step is impossible as written or contradicts the code, stop and report. Do not
improvise, and do not bend a test to fit.**

## 1. The round's tree

- Add `roundTree(rt Runtime, b store.Binding) string`:
  - `b.CWD` for a writer;
  - `rt.Store.ScratchWorktreePath(b.Name, b.Round)` for a reader.
- Use it at **every** place a runner process is launched or relaunched for the round.
  Find them all with
  `grep -rn 'HeadlessLaunch(\|ResumeBuild(\|Dir: *b\.CWD\|composePrompt(' --include='*.go' internal | grep -v _test`.
  Known sites:
  - headless.go `startRound` (~203) and `startProcess` (`Dir:`, ~287), and the resume
    at ~344;
  - send.go ~315 (the preflight argv);
  - the mid-round switch (switch.go), the queue admit, the nudge, and the relaunch after
    a lost daemon restart.

  List every site you changed.
- Writers are unaffected: `roundTree` returns `b.CWD` for them, so every existing writer
  test must pass unchanged.

## 2. Create the scratch when a reader round starts

- Where a round is started, in `Send` and in the queue admit path (whichever calls
  `startRound` for a new round), for a reader binding:
  - after the send-time baseline (`capture.Baseline`, which sets `RoundBaselineTree` and
    `RoundBaselineHead`), call
    `CreateScratchFrom(ctx, rt, b, round, b.RoundBaselineHead, b.RoundBaselineTree)`;
  - read capture.go first. If the baseline is not taken for this path, take it the same
    way as for writers.
- **If the scratch cannot be created**, the round does **not** start.
  - A synchronous `send` returns the error, wrapping `ErrScratch`, with its step.
  - A queued admit leaves the binding NEEDS YOU with that reason, the same mechanism
    other admit failures use.
  - **Never** fall back to `b.CWD`.
- A mid-round switch, a nudge or a relaunch **reuses** the round's existing scratch; it
  does not recreate it.
  - If the scratch is missing at that point (a crash deleted it), recreate it with
    `CreateScratchFrom` from the same baseline. If that fails, go to NEEDS YOU.
- Delete R3's `ErrReaderRoundsNotYet` refusal and its test. Replace the test with the
  ones in §6.

## 3. The reader prompt (internal/relevo/send.go, composePrompt)

For a reader binding, compose `readerPrompt` instead of `builderPrompt`:

```
Your working tree is: <scratch path>
It is a throwaway copy of <b.CWD> for this round: read anything in it, run
anything read-only, change nothing you need to keep -- it is discarded when
the round ends, and nothing in it is ever committed.

Read: <plan path>
Write every file you produce into this directory (create it): <artifact dir>
Your final message is your <output>: it is saved as <artifact dir>/summary.md.
End that final message with this block, filled in honestly:

```relevo
status: done            # done | halted | blocked | deferred
halted_at: ""           # which step, when halted or blocked
changed_paths: []       # files you wrote into the artifact directory
commands_run: []        # commands you ran
not_done: []            # what you deliberately left
```
Then, as the very last thing you do, create this empty file: <done marker>
```

This is the literal prompt text. Keep the first line exactly `Your working tree is: `,
because the e2e fake parses it.
- `<artifact dir>` is `rt.Store.ArtifactDir(b.Name, round, actor)`, and `actor` is
  `bindingRole(b)`.
- `<output>` is the actor's agent output label. Add `internal/relevo/actoroutput.go`
  with exactly this content, which comes from another planner's branch that the server
  cannot see, adapted so it no longer needs `internal/consult`:

```go
package relevo

import (
	"github.com/fuad-daoud/relevo/internal/agentsrc"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// defaultOutput words a reader's prompt when its agent names no output label.
const defaultOutput = "notes"

// ActorOutput is the output label of the agent actor plays: the shipped
// agent's label, else a custom source agent's `output:`, else defaultOutput.
func ActorOutput(actors map[string]roles.Actor, agents map[string]roles.AgentEntry, actor, definition string) string {
	agent := definition
	if a, ok := actors[actor]; ok {
		agent = a.Agent
	}
	if shipped, ok := roles.Shipped(agent); ok {
		return shipped.Output
	}
	if entry, ok := agents[agent]; ok && entry.Source != "" {
		if src, err := agentsrc.Parse([]byte(entry.Source)); err == nil && src.Output != "" {
			return src.Output
		}
	}
	return defaultOutput
}

// actorOutput loads the config for ActorOutput; a missing or unreadable config
// falls back to the shipped labels, because the label only words the prompt.
func actorOutput(rt Runtime, actor, definition string) string {
	if rt.Config == nil {
		return ActorOutput(nil, nil, actor, definition)
	}
	loaded, err := rt.Config.Load()
	if err != nil {
		return ActorOutput(nil, nil, actor, definition)
	}
	return ActorOutput(loaded.Actors, loaded.Agents, actor, definition)
}
```

  If `roles.Shipped`, `roles.Actor`, `roles.AgentEntry`, `agentsrc.Parse` or
  `Source.Output` do not exist with these names, stop and report. `definition` is the
  resolved agent definition name for the binding's harness kind; pass `bindingSpec`'s
  `Definition`. Write a small test for the shipped labels: reviewer gives `findings`,
  researcher gives `notes`, architect gives `plan`.

## 4. Reader tier

A reader must be able to write its artifact directory, so at bind (R3's reader branch in
`bind.go` and `add.go`):
- a resolved tier of `harness` or `read` becomes `edit`;
- if the policy `max_tier` is below `edit`, refuse the bind: "a reader actor needs tier
  edit; max_tier is <x>".

Also check the harness tier table (`internal/harness/tier.go`). If `edit` is refused for
a harness (e.g. opencode), use that harness's lowest tier that allows writing, and say so
in the report.

## 5. Skip writer-only steps at send

For a reader:
- no drift check (send.go:~534);
- no check or gate;
- no verify.

The escape check and diff capture at close are R4b's. Do not touch them here.

## 6. Tests (no harness is spawned; use the existing fake Runner)

1. `TestReaderRoundRunsInItsScratch`: sending to a reader binding creates
   `ScratchWorktreePath(name, 1)`, and the fake Runner's `Dir` and prompt name that
   path, not `b.CWD`. The prompt contains the artifact dir and "your findings" for a
   reviewer. Use a real git temp repo if the scratch needs one; the R2 tests show how.
2. `TestReaderScratchFailureDoesNotStartTheRound`: with a Git fake whose
   `AddDetachedWorktree` fails, `send` returns an error wrapping `ErrScratch`, nothing
   is spawned, and `b.CWD` is untouched.
3. `TestReaderRelaunchReusesTheScratch`: a mid-round switch starts the new process in
   the same scratch path.
4. `TestWriterRoundStillRunsInCWD`: an unchanged writer path.
5. `TestReaderTierAtLeastEdit`.
- **Required mutations.** Run each, confirm the named test fails, then restore. Report
  all three.
  - (a) `roundTree` returns `b.CWD` for readers: test 1 fails.
  - (b) On a scratch failure, fall back to `b.CWD`: test 2 fails.
  - (c) The switch recreates the scratch instead of reusing it: test 3 fails. If
    recreating is observably identical, strengthen test 3 with a marker file written
    into the scratch before the switch, and say so.
- No network. cmd/relevo tests only parse flags.

## 7. Working efficiently

- Batch-read these:
  - internal/relevo/{send,headless,switch,queue,nudge,tier,writer_role,bind,add,scratch}.go;
  - internal/capture/capture.go;
  - internal/store/paths.go;
  - internal/harness/tier.go;
  - the tests next to send and headless.
- Focused loop: `go build ./... && go test -count=1 ./internal/relevo/ -run 'Reader|Scratch|Send|Switch|Queue|Nudge|Headless|Tier|Output' && go test -count=1 ./internal/spawn/ ./internal/store/`
- Full, once at the end, on this server:
  `export TMPDIR=$HOME/.cache/go-tmp GOTMPDIR=$HOME/.cache/go-tmp; mkdir -p $TMPDIR; make check`

## 8. Commit

Copy this plan verbatim to `docs/plans/2026-09-26-a5-r4a-reader-launch.md`. Commit as
**one new commit**: `feat(a5): a reader round runs in a scratch copy with a reader prompt`.
Never amend, and never rebase.

## Report

The report covers:
- `git diff --stat`;
- every launch site changed;
- the tier finding;
- the mutations;
- `make check`'s last lines;
- anything that did not match.
