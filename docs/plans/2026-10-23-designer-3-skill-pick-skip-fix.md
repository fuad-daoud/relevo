# Plan 3 correction 1: `bind` and `bind --resume` must pick over the skill gates

**Prerequisite**: round 4's code is committed on this branch (`29a79867`). Everything below is a new commit; never amend or rebase it.

**Goal.** A designer bind with no `--candidate` starts on the first candidate that can see the skill, and the pick log carries the `skipped ... (skill ...)` note. Today `create` (bind.go:695) and `resume` (bind.go:415) validate the pick over the skill-gated set, then `resolveBuilder` (bind.go:804-806) picks again over `availability.Gates(AvailabilityDeps(rt))` with no skill gates, and `create` builds the binding from that second pick (`res.Token()` at bind.go:737). The fix is the review's second option, so the pick keeps its `pickFor(rt)` account choice. Pinning the first result would lose it, because the first call passes no `pickFor`.

## Behaviour and cases

- `resolveBuilder` picks over `withSkillGates(rt, availability.Gates(AvailabilityDeps(rt)), opts.CWD)`. It has `opts.CWD` and `opts.Role` already.
- Consequences to hold in every caller of `resolveBuilder`:
  - `create` (bind.go:711) and `resume` (bind.go:430): no candidate pinned ⇒ the pick skips a skill-blind candidate, `res.How`/`Position`/`Skipped` carry the skip, and `BuilderCandidate` and the pick log entry come from it. For `resume`, `opts.CWD` is the repository the resume is issued from and `b.CWD` is the binding's directory; judge the binding's directory, so resume must call `resolveBuilder` with `opts.CWD = b.CWD`, or the first call and second call disagree. Check `opts.CWD` against `b.CWD` in `resume` and pin it only if they can differ.
  - `add.go:423` and `switch.go:297` pin the candidate they already judged, so their result does not change. A skill missing in the freshly cut worktree now refuses there, which is correct, and `add.go` already calls `rollback()` on that error.
  - `chain_start.go:352` (`chainBuildMembers`) pins the candidate `chainResolveActors` picked, and its `CWD` is the chain's worktree. A designer member whose picked candidate cannot see the skill is now refused at build time with the gate's note, before any member is written. It is not re-picked. Confirm that error path already unwinds like a `HeadlessLaunch` error from the same call.
- `stale.go:55` (`repickForRound`) re-picks an unpinned binding that has `b.CWD`; wrap its gates with `withSkillGates(rt, ..., b.CWD)`. `stale.go:86` (`repickLine`) only reads a scheduled gate and stays as it is.
- Left alone, as the report classified: `chain_start.go:319` (`chainResolveActors`, no directory at that point; the build-time refusal above is its backstop), `candidate.go:534` and `served.go:330` (explicit-token kind lookups), `runtime.go` reviewer pick, `placement.go`, `statsinputs.go`, views.
- A server bind still gets no skill gates: `createPlacement` routes it to `Add` with `Server` set, which returns before gates are built.

## Files

- Edit `internal/relevo/bind.go`: `resolveBuilder` gate set; `resume` directory as above. Correct the comment above `resolveBuilder` only if it says anything about gates.
- Edit `internal/relevo/stale.go`: line 55.
- Edit `internal/relevo/skillgates.go`: collapse the three wrappers into one (step 4).
- Edit `internal/relevo/skillgates_test.go` (new tests) and `README.md` (the chain-start sentence).
- Not touched: `internal/availability`, `internal/skill`, `internal/roles`, `add.go` logic, `chain_start.go` logic.

## Steps

1. **Fix `resolveBuilder`** to use the skill-gated gate set, and make `resume` hand it `b.CWD`. Deliverable: `go build ./...` passes and `go vet ./internal/relevo` is clean.
2. **Wire `stale.go:55`** with `withSkillGates(rt, ..., b.CWD)`. Deliverable: the re-pick of a stale unpinned designer skips a skill-blind candidate; covered by step 3.
3. **Tests through the real entry points** in `skillgates_test.go`, using `skillRuntime` with the skill on disk for opencode only. Pure, no harness spawned, as the repo rule for tests in `internal/relevo` already requires. Each asserts the picked `BuilderCandidate` and the pick log note (`skipped ... (skill design-runbooks not found for designer on claude ...)`), not just `resolveRole`:
   - `Bind` with `Role: "designer"`, `CWD: "/repo"`, no `--candidate` ⇒ opencode, with the skip note in the pick log. This reproduces the review's failing case.
   - `Bind` with `Resume: true` on a designer binding whose `BuilderCandidate` is stale or empty ⇒ same pick and note. Use whichever existing resume test helper builds such a binding.
   - `repickForRound` on a stale unpinned designer binding ⇒ opencode.
   - A mid-round switch (`switchGates`) and the `send --builder` path (`resolveSendBuilder` with `b.CWD`) each get one assertion if no existing test reaches them; otherwise name the existing test that does in the report.
   - A designer `Bind` with `--candidate` naming the blind candidate is refused with the note, through `Bind`.
   - An actor without skills (`builder`) still picks the first-ranked candidate through `Bind`.
4. **Collapse the wrappers** in `skillgates.go`: one `withSkillGates(rt, gates, dir, tracked)` where a nil `tracked` means disk only. Remove `withTrackedSkillGates` and `withProbedSkillGates`, and update every caller (`add.go` via `addSkillGates`, `bind.go`, `builder_change.go`, `inflight.go`, `send.go`, `stale.go`, the tests). Deliverable: the file is shorter and the existing skill tests pass unchanged apart from the call shape.
5. **README** (the paragraph under Skills): say that a chain start does not skip, because its members' picks are made before the chain's tree exists; a designer member whose picked candidate cannot see the skill is refused when the members are built. Keep the other sentences. Do not claim the pick always skips.
6. **Mutation check, recorded in the report.** Remove the skill gates from `resolveBuilder` (restore the old `availability.Gates(AvailabilityDeps(rt))` line) and run the new tests; the `Bind` and resume tests must fail by name. Also revert the `stale.go` wrap and show the `repickForRound` test fails. Restore both before committing.
7. **Gate.** Focused loop: `go test ./internal/relevo ./internal/availability -run 'Skill|Pick|Resolve|Bind|Resume|Repick|Chain'`, fixing every error before the next run. Then `make check-affected` once, without piping it through `tail`, and capture its exit code (for example `make check-affected; echo exit=$?`). A hook blocking a second run is not a reason to skip the exit code: run it once, correctly, the first time.
8. **Save this plan** verbatim to `docs/plans/2026-10-23-designer-3-skill-pick-skip-fix.md` and commit it with the code as a new commit.

## Style reminders

No history in comments (no issue or PR numbers, no "round N", no "used to"). Functions at most 70 lines, files at most 600. No new exclusions. No CLI test spawns a harness.

## The report must include

- The `resume` directory decision: whether `opts.CWD` and `b.CWD` can differ, and what you did.
- The behaviour of each other `resolveBuilder` caller after the change (`add`, `switch`, `chain_start`), one line each, with the chain build-time error path confirmed.
- The pick line the `Bind` test produces, and the test names for the `Bind`, resume and `repickForRound` cases.
- The mutation results from step 6: which named tests failed.
- The `make check-affected` exit code, captured.
- Files changed against the declared scope above.
- That the owner's env-sandbox real designer check (scratch static site with the skill project-local: three drafts, an index, screenshots, then a colour-only revise whose diff touches only new variant folders) is still to run, and that it also settles whether codex loads skills headlessly.
