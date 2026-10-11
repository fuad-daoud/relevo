# MasterMind adjustments (read first; they override the plan below where they conflict)

1. Server-run chains: `relevo stop <chain>` on a halted server-run chain answers today "its awaited member had no open round". The halted-stop answer (`chain X is halted: \`relevo done X\` closes it`) must hold for both local and server-run chains; find the server-chain stop path (`internal/relevo/chain_server_view.go` is owned by a parallel lane: if the answer can only be fixed there, make the smallest change in that file, one function, and say so in the report).
2. A parallel lane edits `internal/relevo/chain_check.go`, `chain_engine.go`, `chain_pull.go`, `chain_server_view.go`, `served_check.go` and the server resume request. Keep out of them except per item 1. `chain_terminal_wf.go` is yours (payload text only).
3. If the predecessor-check lookup needs a helper in `internal/workflow`, put it there as a pure function with its own test; do not change transition logic in `next.go`.

---

# Plan: halted chains — resume with a new gate, and say how to close (slug: chain-halt-ways-out)

Base: `origin/main 19e25095`. Both premises hold on the code read (evidence below). If either premise fails on the builder's checkout, halt and report — do not reinterpret.

## 1. Behaviour

### 1a. Resume with a new gate reruns the check (owner decision)

Today a zero-budget halt at `repair` re-halts on resume even with a new `--gate`, and the resume answers `internal` repeating the halt reason:

- `internal/workflow/enter.go:127-160` — `applyBudget` returns `HaltTarget(zeroBudgetReason(...))` when `renderLimit < 1 && redBudgetArm(id) && Then.Kind == TargetStep`; `zeroBudgetReason` (`:158-159`) reads `"<id> has no budget left (<expr> renders to 0), so the failure it was meant to fix is unaddressed; the run halts rather than continuing past it. Raise the budget, or rerun the check by hand"`.
- Shipped topology `internal/workflow/shipped/default.yaml:19-22` — `check: { check: "{{params.gate}}", on: { green: review, red: repair } }`, `repair` carries `budget: { max: "{{params.regate}}", ... }`. With `regate=0`, entering `repair` halts with the above reason, `At=repair` (`enter.go:51-58`, `next.go:386-395` `route` sets `At=from`).
- Resume path `internal/relevo/chain_resume_wf.go:20-199` — `chainResumeWorkflow` merges new params, resets `before.Visits={}` (`:86`), then calls `workflow.Resume(def, before, {From: opts.From, Closed})` (`:124`). `internal/workflow/resume.go:33-47` `resumeAt` re-enters `s.At` (`repair`) and `enter` re-fires the same zero-budget halt because `regate` still renders 0. The new `gate` param never gets a check run.
- The re-halt is returned as a plain error `chain_resume_wf.go:186-187` (`fmt.Errorf("chain %s: %s", ...)`), which `cmd/relevo/args.go:252-327` `writeError` maps to `codeInternal` (no sentinel matches). That is the reported "`internal`, repeating the halt reason".

Required behaviour after the fix:

1. `relevo chain --resume --name <chain> --gate <cmd>` (and `--no-gate`, and `--param gate=...`) on a chain halted at a repair arm with a zero-budget reason goes back to that arm's check step and reruns it under the new gate; the chain leaves `halted` (status `running`, awaiting the check with the new command). Same for the `fix-check`/`fix-repair` arm and for custom workflows whose check `red`/`result=red` edge names the halted step.
2. `--from` wins when explicitly given: no reroute, resume at the named step.
3. Halt text no longer says "rerun the check by hand"; it names the real verb/flags (`--resume` with `--gate`, `--regate` to raise the budget), in generic-flag form inside `workflow` (which knows no chain name) plus the chain-named command in the chain payload.
4. Any resume that still ends halted (gate unchanged, budget still zero, send cannot start, any other refusal) answers in `usage`/`conflict` (`refused` or `conflict` code), never `internal`.

Cases the builder must pin:

- Halted at `repair` (zero-budget), resume `--gate new-cmd` → running, awaiting `check` with `new-cmd` (check actually re-runs; old red result does not short-circuit).
- Halted at `fix-repair`, resume `--gate new-cmd` → running, awaiting `fix-check`.
- Custom workflow: check `C` with `red → R`, halted at `R` zero-budget, resume `--gate` → awaiting `C`.
- Resume `--gate` with explicit `--from <other>` → honours `--from`, no check reroute.
- Resume with gate change while halted somewhere that is *not* a red-budget arm (e.g. at `review`): normal resume at `At`; new gate is stored and applies to future checks (do not reroute, do not refuse). If the builder finds current code refuses this, plan halts — report the evidence.
- Resume with no gate change on a zero-budget halt → stays halted, error class is `refused`/`conflict`, message still carries the halt reason, code is not `internal`.
- `--param gate=...` counts exactly as `--gate` for reroute and for the placed-writer gate path (see gap below).

Known gap the plan fixes: `chain_resume_wf.go:157` computes `gateChanged := opts.Gate != "" || opts.NoGate`, missing `--param gate=`; `chain_resume.go:129-138` `chainResumeChangesGate` already covers the param (and the wire shape). The reroute and the staged-prompt walkback must use the `chainResumeChangesGate` definition, not the two-flag test.

### 1b. Halted chains say how to close; `stop` on halted names `done`

Today neither the halt's next move nor the status row names `relevo done <chain>`, and `stop` on a halted chain with no open member round answers "nothing to stop":

- Payloads: `internal/relevo/chain_terminal_wf.go:78-90` (`chainTerminalWFPayload` appends only `" relevo chain --resume --name %s resumes it."` on halt); `internal/relevo/chain.go:284-296` (`chainTerminalPayload`, legacy path, same single-hint shape).
- Status row: `internal/relevo/chain_status.go:122-140` (`viewChainRow` sets `Detail = Reason` on halted); `internal/view/chain_statusline.go:40-56` (chain row `Next = haltNext(name, 0, nil, detail)`); `internal/view/chain_fork_statusline.go:27-50` (child row, same); `internal/view/statusline_next.go:87-101` (`haltNext` renders `"Resolve <name>...: <detail>"`, no `done` hint).
- Stop: `internal/relevo/chain_verbs.go:85-107` (`chainStopHaltedMember` returns `ErrNothingToStop` when no open member round, for halted and stopped alike); `cmd/relevo/done.go:16-24` (`runStopChain` prints `"nothing to stop: chain %s is not running"`); `internal/relevo/text.go:77-92` (`StopText` default `"has no open round; nothing to stop"`); `internal/ui/actions.go:157-173` (`Stop` renders `"nothing to stop: %s has no open round"` for `ErrNothingToStop`).

Required behaviour after the fix:

1. Every halted-chain next move names both ways forward: resume after fixing the cause (`relevo chain --resume --name X`, with the gate/budget hint where the reason is a zero-budget halt) and abandon (`relevo done X` closes it). Surfaces: workflow-chain end payload, legacy end payload (only if the builder confirms a legacy halt still reaches it — otherwise leave untouched and say why), chain status row `Next`, fork-child row `Next`.
2. `relevo stop <halted-chain>` (no open member round) answers `chain X is halted: \`relevo done X\` closes it` — human line, JSON document, and cockpit action text agree. Exit/code class is the halted answer's class (conflict family), not "nothing to stop" and not `internal`. `stop` on a *stopped* chain with nothing open keeps today's "nothing to stop" answer.
3. `relevo done <halted-chain>` path itself is unchanged (it already closes halted chains via `ChainDone`, `chain_verbs.go:203-276`).

## 2. Seams (hand over locations, not searches)

- `internal/workflow/enter.go:112-160` — `applyBudget`, `redBudgetArm`, `zeroBudgetReason`, `renderLimit`.
- `internal/workflow/resume.go:15-47` — `Resume`, `resumeAt` (re-enters `At`, resets only that step's visits).
- `internal/workflow/shipped/default.yaml:17-22,35-38` — check/repair and fix-check/fix-repair topology the tests drive.
- `internal/relevo/chain_resume_wf.go:20-199` — `chainResumeWorkflow` (merge, visits reset, `Resume` call, send/run, re-halt return at `:184-188`); `:201-247` `chainResumeFlagParams`; `:318-360` `chainResumeRunAction` / `chainResumeStagedText` (stale-repair walkback, `gateChanged` use).
- `internal/relevo/chain_resume.go:92-138` — `resumeSettings`, `chainResumeChangesGate`; `:153-220` placed-writer gate refusal/push (read-only for this lane).
- `internal/relevo/chain_terminal_wf.go:17-48,75-90` — `chainTerminalWF`, `chainTerminalWFPayload`.
- `internal/relevo/chain.go:280-309` — `chainTerminalPayload`, `chainTerminalVerb` (legacy; touch only if reachable).
- `internal/relevo/chain_verbs.go:61-107` — `ChainStop`, `chainStopHaltedMember`; `:109-140` `chainStopWorkflow` (read-only context).
- `internal/relevo/chain_engine.go:45-82` — `chainResumeWorkflowRow` refusal shape (the pattern to follow: `refuse(...)`, never plain `fmt.Errorf` for input/state refusals).
- `internal/relevo/text.go:77-92` — `StopText`.
- `internal/relevo/verbclass.go:21-75` — `verbClassify` (one added case max; error-codes lane owns the rest).
- `internal/relevo/chain_status.go:118-140` — `viewChainRow` detail.
- `internal/view/statusline_next.go:48-101` — `nextOf`, `haltNext`.
- `internal/view/chain_statusline.go:19-56` — `statusLineRowOfChain`.
- `internal/view/chain_fork_statusline.go:20-50` — `ChainChildRow`.
- `cmd/relevo/done.go:13-40` — `runStopChain`; `:152-195` `cmdStop` (read the chain-vs-binding branch).
- `cmd/relevo/chain.go:76,97,124-154,302-361` — `chainFlagSet` (`--resume`, `--from`), `cmdChain`, `chainResumeOptions` (no behaviour change expected; read for `--from`-wins rule).
- `cmd/relevo/args.go:252-327` — `writeError` (where `refused`/`conflict` vs `internal` is decided).
- `cmd/relevo/clierror.go:14-42,54-82` — catalog (`usage`/`refused`/`conflict`/`internal` exits and next hints).
- `internal/ui/actions.go:157-173` — cockpit `Stop` result text.
- Read-only context (do not touch without a halt note): `internal/relevo/stop.go:296-338` (`closeDeadMemberRound`), `internal/relevo/chain_render.go:200-225` (repair seed), `internal/relevo/chain_check.go`, `chain_server_view.go`, `served_check.go`, `internal/serve/*`, `internal/remote/*`, `internal/db/chain.go`, `internal/ingest/*`, `cmd/relevo/gate.go`, candidate code.

## 3. Ordered steps (each: deliverable + how to know it worked)

1. Zero-budget halt wording: reword `zeroBudgetReason` (`enter.go:155-160`) to name `--resume` with `--gate`/`--regate` instead of "rerun the check by hand" (generic flags; no chain name, no history in text); deliverable is the new halt string; it worked when the wording test fails on the old string and passes on the new.
2. Gate-change reroute on resume: in `chainResumeWorkflow` (`chain_resume_wf.go:20-199`), when the resume changes the gate (via `chainResumeChangesGate`, not the two-flag test) and `From == ""` and the halted `At` is a red-budget arm, resume `From` at its predecessor check (the check whose `red`/`result=red` edge names `At`; refuse in `usage` class if zero or more than one predecessor); deliverable is a running chain awaiting the check under the new gate; it worked when the gate-reroute test fails without the `From` override and passes with it.
3. Unify `gateChanged` on `chainResumeChangesGate`: replace the `:157` two-flag test so the reroute and the staged-prompt walkback (`:318-360`) also fire for `--param gate=`; deliverable is param-gate parity; it worked when a `--param gate=` resume test fails on the old test and passes on the unified one.
4. Resume re-halt class: wrap the `chain_resume_wf.go:184-188` halted-after-resume return (and any other remaining resume refusal this lane introduces) in `refuse(...)` or a conflict sentinel so `writeError` yields `refused`/`conflict`, never `internal`; deliverable is the coded error; it worked when the code test asserts non-`internal` and fails on a plain `fmt.Errorf`.
5. `verbclass.go` single case: add at most one `verbClassify` case for the new halted/resume sentinel if the wire needs it; deliverable is one added case, no other edits; it worked when the sync-classification test passes and the diff shows one case.
6. Halt payload both-ways hint: extend `chainTerminalWFPayload` (`chain_terminal_wf.go:78-90`) so a halt names `relevo chain --resume --name X` and `relevo done X` closes/abandons; touch `chain.go:284-296` only if the builder proves a legacy halt still emits it; deliverable is the two-hint payload; it worked when the payload test fails without the `done` clause.
7. Status-row next-move both-ways hint: extend the chain row (`chain_statusline.go:40-56`), fork-child row (`chain_fork_statusline.go:27-50`), and the shared `haltNext` (`statusline_next.go:87-101`) so a halted chain's `Next` names resume and `relevo done X`; keep helpers ≤70 lines and `statusline.go` (at the 600-line cap) untouched — put helpers in `statusline_next.go`/`chain_statusline.go`; deliverable is the two-hint `Next`; it worked when the row test fails on resume-only text.
8. Halted-stop answer: give `ChainStop` on a halted chain with no open member round its own answer (`chain_verbs.go:85-107` new sentinel/refusal, not `ErrNothingToStop`; stopped chains keep `ErrNothingToStop`); render it in `runStopChain` (`done.go:16-24`), the chain JSON document, `StopText`/`text.go:77-92` only if the builder proves the chain path reaches it, and cockpit `Stop` (`actions.go:157-173`) with identical wording `chain X is halted: \`relevo done X\` closes it`; deliverable is the uniform halted-stop answer; it worked when the stop-on-halted test fails on "nothing to stop".
9. Pure-function CLI coverage: put every new CLI-asserted rule behind a pure `internal/relevo` (or `internal/view`) function so no `cmd/relevo` test spawns a harness or touches the network; deliverable is tests calling the pure function; it worked when the new tests run with network blackholed.
10. Full check and baseline: iterate on the focused suite (`go test ./internal/relevo ./internal/workflow ./internal/view -run 'Halt|Resume|Stop|Chain' -count=1`, fixing every failure before the next run), then run `make check` once; if code moved between packages regenerate the coverage baseline with `sh scripts/check-coverage.sh --write` and say so in the report, never lowering a baseline to get green; deliverable is a green `make check`; it worked when both commands pass in order.
11. Plan file and two commits: save this plan as `docs/plans/2026-10-11-chain-halt-ways-out.md` and commit with the code, one commit per issue with `Fixes #1106` / `Fixes #1107` in the message (messages may name issues; code and tests must not); deliverable is the committed plan plus two code commits; it worked when `git log --oneline -3` shows plan + two `Fixes` commits and `git diff --stat` matches the declared scope.

## 4. What is deleted (closed list)

1. The halt clause "rerun the check by hand" in `zeroBudgetReason` — replaced by the `--resume`/`--gate`/`--regate` wording.
2. The resume-at-`At` behaviour for gate-changing resumes on zero-budget repair halts — replaced by resume-at-predecessor-check.
3. The two-flag `gateChanged` test (`opts.Gate != "" || opts.NoGate`) as the definition of a gate change — replaced by `chainResumeChangesGate(opts)` everywhere in this lane.
4. The plain-`fmt.Errorf` (→ `internal`) return for a resume that re-halts — replaced by a `refused`/`conflict`-class error.
5. The `"nothing to stop: chain %s is not running"` answer for halted chains — replaced by `chain X is halted: \`relevo done X\` closes it` (stopped chains keep "nothing to stop").
6. Resume-only halt next-moves (payload and status-row `Next` without a `done` hint) — replaced by resume-plus-`done` hints. Nothing else is removed; `ChainDone`, `closeDeadMemberRound`, and the placed-writer gate refusal/push are unchanged.

## 5. Report must include

- Which predecessor-check rule was implemented (red-edge lookup, zero-vs-multiple handling, `--from`-wins) and the file:line of the lookup.
- The exact new halt string and halted-stop string.
- For each fix: the test name and the mutation it kills (old string restored / `From` override removed / plain error restored / `ErrNothingToStop` restored / `done` clause dropped).
- `writeError`/`verbclass` mapping for the new error(s) and proof the code is not `internal` (CLI code assertion output).
- `git diff --stat` vs the declared scope, why any out-of-scope file was touched (or "none"), function/file-size check results, coverage-baseline handling, focused-command plus `make check` transcripts, and the two `Fixes` commit hashes.
