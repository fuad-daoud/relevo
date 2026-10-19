# Chain resume, check log and fence fixes

Three chains on zen ran these plans in order, one small plan per build. The MasterMind fixed #1298 (systemd-run expanding `$` in a scope's argv) and #1302 (the `db sync` usage) directly.

- `fence-eof` (#1296): A1, A2
- `resume-fixes` (#1295): B1, B2, B3
- `check-halt` (#1297, #1299): C1, C2, C3

---

## A1: a relevo block that runs to end of file closes at EOF

Issue #1296 (`gh issue view 1296`; only the part before "## Environment" matters).

A builder's report ended without the closing fence of its ```` ```relevo ```` status block. The block was complete and parsed, but the chain halted with "tail: unclosed fence".

### Change (internal/reporttail/reporttail.go)

1. `FindRelevoBlock`: when no closing fence follows the last ```` ```relevo ```` opening fence, return `closeIdx = len(lines)` and an empty reason instead of "tail: unclosed fence". End of file is the only thing that can follow it, so there is no prose-after-fence check in that case.
2. `RelevoBlocks`: yield that trailing unclosed block too, as `{Open: i, Close: len(lines)}`, so it agrees with `FindRelevoBlock` on the same bytes. Fix its doc comment.
3. Check every index use of `closeIdx` in this package (`scanTail`, `ParseWithReason`, `StripTail`, `BlockValue` in blockvalue.go). None may index `lines[closeIdx]` when it equals `len(lines)`.
4. A block whose content does not parse is still an error with a clear reason: for example, a flow list still open at EOF is reported by `applyListKey`.

### Tests (internal/reporttail)

- A complete block with no closing fence at EOF parses: status done, its lists read.
- An unclosed block whose content is broken (an unclosed flow list) is still an error.
- `RelevoBlocks` returns the trailing unclosed block. Update `TestRelevoBlocksReturnsEveryBlockInOrder`, which pins the old drop.
- `BlockValue` and `ParseWithReason` agree on a key inside a trailing unclosed block.
- `StripTail` strips a trailing unclosed block. Pin it if no test does yet.

### Rules for every plan in this chain

- Do only this plan. Later plans in the chain cover the rest; do not start them.
- Code style per CLAUDE.md: no issue numbers or history in code, comments or test names; a test name says what it pins; a comment says why, never what; functions at most 70 lines; non-test files at most 600 lines (split into a new file rather than grow one past it).
- Verify with `make check-affected` before you finish. Commit your work (a new commit; never amend or rebase). Do not save this plan into docs/: the MasterMind commits the plan files.
- In your report, list each new condition with a mutation: the exact line you broke and the named test that failed. Restore the code afterwards.

Commit message: `reporttail: a relevo block that runs to end of file closes there`

---

## A2: every caller of the fence parser agrees on an unclosed final block

Issue #1296. The previous plan in this chain made `reporttail.FindRelevoBlock` and `RelevoBlocks` treat end of file as the close of a final unclosed ```` ```relevo ```` block. This plan checks the callers outside the package and pins them.

### Change

Audit each caller and decide whether it needs a code change. Most should not, since they call the parser. Callers:
- `internal/workflow/outcomes.go` (`BlockValue`), used by the chain close path `chainEventFromCloseWF` in `internal/relevo/chain_close.go`;
- `internal/relevo/reconcile.go` (`ParseWithReason` for the close outcome, `StripTail`);
- `internal/relevo/reader_deliverable.go` (`ParseWithReason`);
- `internal/relevo/summary.go` (`StripTail` via `stripKeptChainBlock`);
- `internal/consult/verify.go` `parseVerdict`: it maps `closeIdx == -1` to the unstructured verdict. It must inherit the same rule, so an unclosed but parseable verdict block parses. Do not special-case it.
- `grep -rn "FindRelevoBlock\|RelevoBlocks\|StripTail\|ParseWithReason\|BlockValue" --include='*.go' internal cmd` for any others.

Where code indexes the line after `closeIdx` or assumes a closing fence line exists, fix it.

### Tests

- A chain builder close whose report ends in a complete but unclosed block advances the chain (it reaches check), instead of halting with "unclosed fence". Use the chain close test helpers in `internal/relevo` (`chainDoneBody` and friends; drop the final fence).
- `parseVerdict` on an unclosed but parseable verdict block returns the structured verdict.

### Rules for every plan in this chain

- Do only this plan. Later plans in the chain cover the rest; do not start them.
- Code style per CLAUDE.md: no issue numbers or history in code, comments or test names; a test name says what it pins; a comment says why, never what; functions at most 70 lines; non-test files at most 600 lines (split into a new file rather than grow one past it).
- Verify with `make check-affected` before you finish. Commit your work (a new commit; never amend or rebase). Do not save this plan into docs/: the MasterMind commits the plan files.
- In your report, list each new condition with a mutation: the exact line you broke and the named test that failed. Restore the code afterwards.

Commit message: `chain: a report whose status block runs to end of file closes the round`

---

## B1: a gate-changing resume of a stopped or halted red arm re-runs the check

Issue #1295, second case (`gh issue view 1295`; only the part before "## Environment" matters).

A chain's repair round was stopped with `relevo stop`. `relevo chain --resume --name X --gate '<cmd>'` then re-sent the chain's ORIGINAL build plan to the builder, instead of re-running the check under the new gate, which is what the halt text promises ("Resume with --gate <command> to rerun the check").

### Cause

- `chainResumeReroute` (`internal/relevo/chain_resume_reroute.go`) reroutes to the check only when the chain is `StatusHalted` AND the step is a zero-budget arm (`workflow.IsZeroBudgetArm`). A stopped chain at `repair` re-enters `repair`.
- Then `chainResumeStagedText` (`internal/relevo/chain_resume_wf.go`), with `gateChanged`, walks back past the repair prompts (`isRepairPlan`, `internal/relevo/repair.go`) to round 1's build plan and re-sends it.

### Change

1. `chainResumeReroute`: a resume that changes the gate (`chainResumeChangesGate`) on a chain that is stopped OR halted at a step that `workflow.RedArmChecks` (`internal/workflow/redarm.go`) resolves re-enters that check. That includes any red arm, not only a zero-budget one.
   - An explicit `--from` still wins.
   - Keep the existing refusals and their wording for a step that no check reaches, and for one that several checks reach.
   - Is a non-red-arm step stopped or halted elsewhere (build, review)? Then keep today's behaviour.
2. `chainResumeStagedText`: the `gateChanged` walk-back must never land on a prompt that is not a repair plan and re-send it as the step's own. If the walk-back moves off the awaited round and lands on a non-repair prompt, return no staged text (ok=false), so the engine renders the step's seed fresh. When the walk-back does not move, re-sending the awaited round's own prompt stays as is.
3. Update the reroute's doc comment to match.

### Tests (internal/relevo, alongside chain_resume_gate_test.go)

- A stopped repair resumed with a new gate starts a check run and sends no builder round. Extend `TestResumeWithANewGateRerunsTheHaltedArmsCheck`, which pins the halted zero-budget half.
- A chain halted at a repair arm with budget left, resumed with a new gate, re-runs the check.
- A gate-changed walk-back that would land on a non-repair prompt yields no staged text.
- `--from` with a new gate still enters the named step.

### Rules for every plan in this chain

- Do only this plan. Later plans in the chain cover the rest; do not start them.
- Code style per CLAUDE.md: no issue numbers or history in code, comments or test names; a test name says what it pins; a comment says why, never what; functions at most 70 lines; non-test files at most 600 lines (split into a new file rather than grow one past it).
- Verify with `make check-affected` before you finish. Commit your work (a new commit; never amend or rebase). Do not save this plan into docs/: the MasterMind commits the plan files.
- In your report, list each new condition with a mutation: the exact line you broke and the named test that failed. Restore the code afterwards.

Commit message: `chain: a gate-changing resume of a stopped or halted repair re-runs the check`

---

## B2: one named clause for the uncommitted-work halt

Issue #1295, first case (`gh issue view 1295`). This plan only prepares the next one.

When a chain's builder round closes `done` with uncommitted changes, the chain halts. `chainDirtyCloseReason` and its wordings live in `internal/relevo/chain_close.go`: `chainDirtyReason`, `chainMirrorDirtyReason` and `chainDirtyUnreadReason`. The next plan makes `relevo chain --resume` recognise that halt from the chain's stored halt reason, which is free text. So the recognisable part must be one constant, not a copy of the wording.

### Change

1. Define one unexported constant holding the stable clause both "dirty tree" wordings share. Today that is "the chain does not advance over uncommitted work". Build `chainDirtyReason` and `chainMirrorDirtyReason` from it. Do NOT use it in `chainDirtyUnreadReason` (a tree that could not be read is not recoverable by a commit).
2. Add a predicate `chainHaltedOverUncommittedWork(reason string) bool` that reports whether a stored halt reason came from those two wordings. It must not match the unread wording.
3. Do not change the user-visible wording beyond what using the constant requires.

### Tests

- The predicate matches the reason `chainDirtyReason` builds.
- It matches the reason `chainMirrorDirtyReason` builds.
- It does not match `chainDirtyUnreadReason`'s reason, an empty reason, or the zero-budget halt reason.
- Build each reason with the real function. Never hard-code the text in the test.

### Rules for every plan in this chain

- Do only this plan. Later plans in the chain cover the rest; do not start them.
- Code style per CLAUDE.md: no issue numbers or history in code, comments or test names; a test name says what it pins; a comment says why, never what; functions at most 70 lines; non-test files at most 600 lines (split into a new file rather than grow one past it).
- Verify with `make check-affected` before you finish. Commit your work (a new commit; never amend or rebase). Do not save this plan into docs/: the MasterMind commits the plan files.
- In your report, list each new condition with a mutation: the exact line you broke and the named test that failed. Restore the code afterwards.

Commit message: `chain: the uncommitted-work halt names its clause once`

---

## B3: a resume after committing the work advances instead of re-sending the plan

Issue #1295, first case (`gh issue view 1295`).

A chain halted at **build** because the builder left its work uncommitted: "...closed done with changes left uncommitted ... the chain does not advance over uncommitted work; commit it ...". The human committed the work and ran `relevo chain --resume`. The chain re-sent the whole original plan as round 2 instead of advancing to `check`.

The previous plan added `chainHaltedOverUncommittedWork(reason)` in `internal/relevo/chain_close.go`. Use it.

### Cause

- `workflow.Resume` → `resumeAt` (`internal/workflow/resume.go`) re-enters the halted step.
- `chainResumeRunAction` / `chainResumeStagedText` (`internal/relevo/chain_resume_wf.go`) re-send the awaited round's own staged prompt.
- `chainResumeClosed` only routes a NEWER closed round (`newest <= st.Awaiting.Round` returns nil), so the halted round's own close is never re-evaluated.

### Change

In `chainResumeLocked` (`chain_resume_wf.go`), before `workflow.Resume`, handle this case. It applies only when the chain is halted, at a run step, with a stored reason for which `chainHaltedOverUncommittedWork` is true, and only on the trees the halt applied to (`chainDirtyGateApplies`). Mirror and remote members keep today's behaviour. Put the new code in a new file (for example `chain_resume_dirty.go`) if `chain_resume_wf.go` would pass 600 lines.

1. **Cause gone:** the member's tree is clean (`rt.Git.Dirty` false), and HEAD has moved past the round's recorded baseline (`RoundBaselineHead` on the member record).
   - If the git seam can test ancestry, require the baseline to be an ancestor of HEAD. Otherwise HEAD != baseline. Say which one you used in your report.
   - Feed that round's own close back through the close path, built from its stored report entry the way `chainResumeClosed` builds a newer one (`memberReportEntry`, `chainEventFromCloseWF`). The chain advances (to `check` in the default workflow), and nothing is sent to any builder.
   - `workflow.resumeClosed` refuses a close whose round is not newer. Do not loosen that in general, since a foreign close must never advance a chain. Add a narrow same-round path used only by this recovery (a `ResumeOpts` field or a dedicated function).
2. **Cause still there** (still dirty, or nothing committed since the baseline): the resume refuses with the same advice the halt gave, and sends nothing.

### Tests (internal/relevo, in the style of chain_resume_wf_test.go; fakeGit in fake_test.go)

- Resume after the halted round's work is committed: the chain is at check (a check run started, or the check step awaited) and no builder round was sent.
- Resume while the tree is still dirty: refused with the uncommitted-work advice, and nothing was sent.
- Resume with a clean tree but HEAD still at the baseline: refused.
- In `internal/workflow`: the same-round path is unusable without its flag; an equal-round close given the ordinary way is still refused. Add a test if none pins it.

### Rules for every plan in this chain

- Do only this plan. Later plans in the chain cover the rest; do not start them.
- Code style per CLAUDE.md: no issue numbers or history in code, comments or test names; a test name says what it pins; a comment says why, never what; functions at most 70 lines; non-test files at most 600 lines (split into a new file rather than grow one past it).
- Verify with `make check-affected` before you finish. Commit your work (a new commit; never amend or rebase). Do not save this plan into docs/: the MasterMind commits the plan files.
- In your report, list each new condition with a mutation: the exact line you broke and the named test that failed. Restore the code afterwards.

Commit message: `chain: a resume after the work is committed advances past the uncommitted-work halt`

---

## C1: `relevo show --gate` reads a chain check's sealed log

Issue #1297 (`gh issue view 1297`; only the part before "## Environment" matters).

When a chain's check fails, the MasterMind cannot read why. `relevo show b598-mr --gate` prints "no gate for round 2".

### What is already true (do not change it)

- `chainCheckSealLog` (`internal/relevo/chain_check.go`) seals each check run's log into a `round_file` row and removes the file. The row is the record.
- The key is `rt.Store.CheckLogPath(member, round, run)` (`internal/store/paths.go`), i.e. `<member>/<NNN>-check-<RRR>.log`.
- `rt.Store.ReadFile` returns a sealed row's body.

### Important: run numbers count across the whole chain

- `run` comes from `tx.ChainCheckNextRun(chain)`, and the round comes from `chainCheckLogTarget`: the member's newest closed round at the moment the run starts.
- So round 2 may hold runs 3, 4 and 5 and no run 1. A real example: `b598-ctl/002-check-005.log` exists without `002-check-001.log`.
- Do NOT probe run = 1, 2, … until the first miss. Find a round's check logs by what is stored. Either:
  - the chain's `chain_check` rows whose `log` resolves (`CheckLogTarget`) to this member and round; or
  - the round's sealed round-file names matching `NNN-check-RRR.log`.
  Pick whichever the store seam supports cleanly.

### Change

- In the gate section of `relevo show` (`showSections`, `ShowGate` branch, `internal/relevo/show.go`, plus the archived/DB show paths if they have their own gate branch): when the round has no gate log of its own, show the round's chain check log with the highest run number, under a label naming the run (for example "chain check run 5").
- `show.go` is at 597 lines, so put the helper in a new file (for example `show_chain_gate.go`).
- A round with neither a gate log nor a check log still says what it says today.

### Tests

- A round whose only gate output is a sealed chain check log, held as run 3 (no runs 1 or 2 for that round): `show --gate` prints that log's body with its run label.
- With runs 3 and 5 on the same round, it shows run 5.
- A round with no gate log and no check log still reports the section missing.

### Rules for every plan in this chain

- Do only this plan. Later plans in the chain cover the rest; do not start them.
- Code style per CLAUDE.md: no issue numbers or history in code, comments or test names; a test name says what it pins; a comment says why, never what; functions at most 70 lines; non-test files at most 600 lines (split into a new file rather than grow one past it).
- Verify with `make check-affected` before you finish. Commit your work (a new commit; never amend or rebase). Do not save this plan into docs/: the MasterMind commits the plan files.
- In your report, list each new condition with a mutation: the exact line you broke and the named test that failed. Restore the code afterwards.

Commit message: `show: --gate reads the chain check log a round holds`

---

## C2: a halt after a failed chain check names the check and quotes its log

Issue #1297 (`gh issue view 1297`).

When a chain's check fails and the chain halts or stops, the halt reason is the generic zero-budget sentence: "repair has no budget left (0 renders to 0), so the failure it was meant to fix is unaddressed; ...". It does not say the check failed, or why. The previous plan made `relevo show <member> --round N --gate` read the check's sealed log.

### Change

- When a chain becomes halted or stopped and its newest `chain_check` row is a settled non-pass, append a detail to the halt reason. A non-pass is `fail` here; `timeout` and `error` come in the next plan, but structure the code so they slot in.
- The detail holds:
  - the check's result and exit code;
  - the `relevo show <member> --round N --gate` command that reads it (use `showCommand` in `internal/relevo/text.go`; member and round from `rt.Store.CheckLogTarget(row.Log)`);
  - the last 20 non-empty lines of its log (reuse `tailLines` in `internal/relevo/gate.go`, read through `rt.Store.ReadFile`). Give the 20 a named constant with a why-comment; `gateTailLines = 5` serves a different payload.
- Append it in `internal/relevo`, at the point where a chain's terminal halt reason is produced or rendered (`chainTerminalWF` in `chain_terminal_wf.go`, or where the reason is stored). Never teach `internal/workflow` about logs.
- `chain_check.go` is at 570 lines, so put new helpers in a new file (for example `chain_check_halt.go`).
- Only a halt whose newest check is a non-pass, and which happened after that check, gets the detail. Halts with no failed check behind them keep their exact wording: the uncommitted-work halt, a reviewer budget halt, a stop with no check. Read the round/step order carefully: a red check followed by a repair round that halts on budget is the main case and must get the detail.

### Tests (internal/relevo)

- A chain whose check failed and then halted at the zero-budget repair arm carries in its halt reason the check result, the show command, and the tail of the log.
- A tail longer than 20 lines is cut to the last 20.
- A halt with no failed check behind it (for example the uncommitted-work halt) keeps its exact wording.

### Rules for every plan in this chain

- Do only this plan. Later plans in the chain cover the rest; do not start them.
- Code style per CLAUDE.md: no issue numbers or history in code, comments or test names; a test name says what it pins; a comment says why, never what; functions at most 70 lines; non-test files at most 600 lines (split into a new file rather than grow one past it).
- Verify with `make check-affected` before you finish. Commit your work (a new commit; never amend or rebase). Do not save this plan into docs/: the MasterMind commits the plan files.
- In your report, list each new condition with a mutation: the exact line you broke and the named test that failed. Restore the code afterwards.

Commit message: `chain: a halt after a failed check names the check and quotes its log`

---

## C3: a timed-out chain check says it timed out

Issue #1299 (`gh issue view 1299`; only the part before "## Environment" matters).

A chain check that ran past its timeout halted with a reason that read like a red gate. Nothing said it had timed out, or which setting controls it.

### What is already true

- The chain check timeout is `rt.Policy.GateTimeout()` (`chainCheckSpec` in `internal/relevo/chain_check.go`), which reads `policy.gate.timeout_ms` (`internal/policy/policy.go`). It is configurable already; the reporter set a non-existent key. No new flag or param.
- A timed-out run's `chain_check` row has result `timeout` and its `duration_ms` (`advanceGateProc` in `internal/relevo/gate_proc.go`).

### Change

- Extend the halt-reason detail the previous plan added:
  - `timeout`: say "timed out after <duration>" and name `policy.gate.timeout_ms` as the setting that raises it (for example "raise it with relevo config set policy.gate.timeout_ms <ms>"). Check the real `relevo config set` syntax in cmd/relevo before writing the hint. Do not call it a failure.
  - `error`: name the row's note.
  - `fail` stays as the previous plan made it.
- Keep the show command and the log tail on all three. A timed-out run's log is still useful.

### Tests

- A chain whose check timed out carries a halt reason that says "timed out", names `policy.gate.timeout_ms`, and does not use the fail wording.
- A check that errored names its note.

### Rules for every plan in this chain

- Do only this plan. Later plans in the chain cover the rest; do not start them.
- Code style per CLAUDE.md: no issue numbers or history in code, comments or test names; a test name says what it pins; a comment says why, never what; functions at most 70 lines; non-test files at most 600 lines (split into a new file rather than grow one past it).
- Verify with `make check-affected` before you finish. Commit your work (a new commit; never amend or rebase). Do not save this plan into docs/: the MasterMind commits the plan files.
- In your report, list each new condition with a mutation: the exact line you broke and the named test that failed. Restore the code afterwards.

Commit message: `chain: a timed-out check says so and names the setting that raises it`
