# Plan: Go data for the band's next-move buttons and toasts (#957, Go half)

One builder round, Go only. CLAUDE.md and docs/runbook.md bind. TypeScript band is a parallel round; this plan covers only `relevo status --line --json` (`view.StatusLineDoc`).

## Behaviour and cases

1. `StatusLineRow.next` (object, omitempty): `{label, text}`. Set only when the row waits on the MasterMind, i.e. tone is `needs` or `report` (the rule in `rowStatus`); absent on every other row (running/active `phase`/`quiet`, paused, done).
   - Question (newest to-MasterMind entry is `KindQuestion`): label `answer`; text `Answer <name> r<round>'s question "<q>": `, where `<q>` is the question's first non-blank line from the same source `WaitingOn` uses, capped as `Waiting` lines are.
   - Report (delivered `KindReport`, tone `report`): short label naming the verify move; text names the move AND the plan's path, which is the staged `NNN-prompt.md` (see P3 below).
   - Halt / NEEDS YOU (tone `needs`): short label; text is the resolving move from the row's `Waiting.Hint`/`Waiting.Line` (see P1 below).
   - Chain stand-in rows: the tone rule governs uniformly; the report case never fires on a chain row (a chain row carries no prompt `LogEntry`, so there is no plan path); a `needs`-tone chain row takes the halt case sourced from its `Detail`/`Reason`.
2. Toast data: the band diffs between polls; fields needing no new key are `chain_progress` (exists) and `candidate` (exists as `StatusLineRow.Candidate` — a builder switch is a `Candidate` change). Gates are not in the doc (`Report.Gated` exists, `StatusLineDoc` carries nothing), so add `StatusLineDoc.gates` (omitempty): the machine's live gates `{token, provider, until, reason}` projected from the same source `relevo gate` lists (`availability.Gates`/`GatesFrom` over the shared ledger load): `provider` is `availability.ProviderOf(token)`, `reason` is the gate `Note` (fallback: the kind text), `until` is `Gate.Until` (zero = until cleared).
3. Additive only: `next` and `gates` are omitempty; every existing key unchanged; `relevo status --line` text byte-identical. Regenerate exactly the two JSON goldens (`statusline-json.golden`, `statusline-chain-json.golden`) and name them in the report.

## Premises in the seed that are false (as the code stands)

- P1 — "the move relevo's halt already suggests (its `next` / reason)": a halt carries no `next` field. The halt reason is `store.Binding.Halt`/`HaltAt` (stamped by `stampHalt`); the suggested move a row exposes is `Waiting.Hint` + `Waiting.Line` (e.g. `relevo status --name X`, `relevo bind --resume --name X --rebind`). CLI `next:` lines are the `clierror` catalog for command failures, unrelated to halts. Halt-case `next` derives from `Waiting`, not from a halt `next`.
- P2 — question rows are near-dead state: `KindQuestion` has no writer left (`internal/store/log.go:37-45` — nothing appends a to-planner question entry since the vocabulary settled; only pre-existing logs carry them). The question case must still be pinned, but it fires only on old logs. Deeper: the question text is NOT on `BindingStatus` — `StatusLineRows` is pure from `Report`, and the text lives in the `QuestionPath` file read only in `internal/relevo` via `questionFirstLine`. `next` for questions cannot be built in `view` alone.
- P3 — same plumbing gap for the report path, plus a recording fact: the only plan path relevo records is the staged `NNN-prompt.md` (prompt `LogEntry.Path = Store.PromptPath(name, round)` at send, `internal/relevo/send.go:565,647-653`); the original file handed to `send` is ephemeral (only `DryRun.PromptFrom`, never persisted). So "the file the round's prompt was sent from" available to the statusline is the staged prompt path — use that and say so. It is not currently on `BindingStatus` either (`PlanRound` is, the path is not).
- P4 — "this MasterMind's relevant gates": gates are machine-wide, not per-MasterMind (`availability.Gates` projects the ledger onto configured candidates — the same list `relevo gate` prints). The doc carries the machine-wide live set; per-row narrowing is by `Binding`/`Role` only for display. `UnusedProviderGates` stays out of JSON (`json:"-"`) — the doc's `gates` is the `GatesFrom` set, not the unused set.
- P5 — seed's example cites `relevo show <name> --round <n> --diff`: flag spelling unverified in this round; builder confirms against the verb registry before writing report-case text.

## Seams

- `internal/view/statusline.go` (591 lines; cap is 600 via `scripts/check-filesize.sh` — do not extend): `StatusLineRow` `:415-470` (`next` field site), `StatusLineDoc` `:472-479` (`gates` field site), `StatusLineRows` `:488-503`, `statusLineRowOf` `:516-591` (tone/`needsYou`/`reportIn` rule, `LastPayload`), `ActivityWord` `:239-256`, `rowStatus` `:278-314`.
- New file `internal/view/statusline_next.go` (precedent: `statusline_band.go:1-18` holds band types out of the capped file): the `Next` type plus its fill.
- `internal/view/status.go`: `BindingStatus` `:36-241` (plumb site if question text / staged prompt path ride the row as additive omitempty fields), `LastEvent` `:277-287`, `PendingInfo` `:298-308`, `Report` `:311-328` (`Gated` `:322`, `Unused` `:327` stays `json:"-"`).
- `internal/view/waiting.go`: `questionEntry` `:59-66`, `WaitingOn` `:75-116` (`Hint`/`Line`/`Cause` = halt-case source), `capLine` `:40-55`.
- `internal/view/chain_statusline.go:19-53` `statusLineRowOfChain` (chain-row `next` decision); `internal/view/chain.go:12-53` `ChainFacts` (existing `chain_progress` source).
- `internal/relevo/status.go`: `statusRowWith` `:204-460` (`Waiting` wiring `:325-327`, `LastPayload` `:337-346`, `Pending` `:445-450`, ledger load `:177-179`); `internal/relevo/statusline.go:20-29` `MasterMindStatus`; `internal/relevo/push_live.go:7-12` (claim-read pattern for shared loads).
- `internal/relevo/waiting.go:14-28` `questionFirstLine` (question-text source); `internal/relevo/send.go:565,647-653` (staged-path source); `internal/relevo/reconcile.go:271-326` `haltBinding`/`stampHalt` (halt-reason source; no `next` field).
- `internal/availability/gates_projection.go:54-93` `Gates`/`GatesFrom` (doc-gates source, shared ledger load with `Report.Gated`); `ledger.go:217-234` `Gate`; `gates.go:41-46` `ProviderOf`; `internal/store/paths.go:162-172` `PromptPath`, `:291-293` `QuestionPath`; `internal/store/log.go:140-204` `LogEntry`.
- `cmd/relevo/status.go:243-306` `runStatusline` (JSON doc build `:289-305` = `gates` fill site; `Text` fill `:296-299` unchanged).
- Tests/goldens: `cmd/relevo/contract_test.go:141-210` `seedStatusFixture`, `:257-274` `TestContractStatusLine`; `cmd/relevo/testdata/contract/statusline{,-chain}.golden` (text — byte-identical, never regenerated) and `statusline{,-chain}-json.golden` (the only two regenerated); `internal/view/fixtures_test.go:16` `baseTime`; `statusline_band_test.go` (precedent for key presence/absence tests).

## Ordered steps

1. View `next`: add the `Next` type plus the tone-rule fill in new `internal/view/statusline_next.go`, with the P2/P3 inputs plumbed (additive omitempty row fields vs relevo-layer build — builder's choice, view stays pure from `Report` either way) — proved by `go test ./internal/view/ -count=1` pinning `next` on question/report/halt fixtures, absent on running/done/paused rows and JSON key presence/absence (no harness, no network — say so).
2. Relevo sourcing: wire question text via `questionFirstLine`, staged prompt path via the prompt `LogEntry.Path`, and halt `Waiting.Hint`/`Line` passthrough — proved by `go test ./internal/relevo/ -count=1` (no harness spawn, no network — say so).
3. CLI wire: fill doc `gates` from the shared ledger load in the `runStatusline` JSON branch; rows carry `next`; text branch untouched — proved by `go test ./cmd/relevo/ -run 'Contract|Statusline' -count=1` (no harness spawn, no network — say so) with text goldens byte-identical.
4. Mutations: drop `next` on report rows, then leak `next` onto a running row, naming the failing test from steps 1–3 for each — proved by the quoted failure output, then restore (no harness, no network — say so).
5. Goldens: regenerate only `statusline-json.golden` and `statusline-chain-json.golden` (`go test ./cmd/relevo -run 'TestContractStatusLine|TestStatusChainLineContract' -update`, exact test names verified first) — proved by `git diff --name-only` listing only those two.
6. Full gates: with `TMPDIR`/`GOTMPDIR` per `docs/runbook.md`, `make check` then `make e2e` green; if the `internal/consult` coverage gate trips, prove it on the base commit and never touch the baseline — proved by both commands' quoted green output.
7. Docs+commit: save the as-built plan (deltas noted) to `docs/plans/2026-10-09-band-next-go.md` and commit code+goldens+plan as new commit(s), never amending/rebasing a remote branch — proved by clean `git status` and new commit hash(es).

## Declared diff scope

New `internal/view/statusline_next.go` + view tests; additive row/doc fields and sourcing in `internal/relevo` + tests; `gates` fill plus CLI tests in `cmd/relevo/status.go`; two JSON goldens; `docs/plans/2026-10-09-band-next-go.md`. Untouched: `internal/view/statusline.go` line count, text rendering, text goldens, `tui.tsx`/`server.ts`, every other verb.

## Deleted behaviour (closed list)

1. Nothing is deleted: `next` and `gates` are omitempty-additive; existing keys, text output, and all verbs are unchanged; no rows or kinds are removed.

## Report must include

P1–P5 dispositions; per-step check outputs with the no-harness/no-network statement per step; golden `git diff --name-only` listing; named mutation failures; coverage-baseline action (or "unchanged"); commit hashes; honest `commands_run`/`not_done`.

## MasterMind corrections (review of this plan; these override anything above)

1. P1-P5 dispositions are accepted. Field shapes are FIXED (a parallel TS round codes against them): `rows[].next = {"label": string, "text": string}`; `gates = [{"token": string, "provider": string, "until": RFC3339 string or "" when until cleared, "reason": string}]`. Keep `candidate` as it is.
2. Report-case label: `make check, compare the diff`; text: `Verify <name> r<round>: run make check, then compare <the verified diff command> against <staged prompt path>`.

## As built (deltas)

- `next` inputs ride `BindingStatus` as `json:"-"` fields (`Question`, `PromptPath`), filled in `internal/relevo/status_next.go`; status JSON is unchanged. Remote-server rows do not carry them.
- Doc `gates` is `view.StatusLineGates(rep.Gated)`: `Report.Gated` is already the `GatesFrom` projection over the shared ledger load, so no second load. It includes roles-missing gates, as `relevo gate` does.
- Report case omits the " against <path>" clause when no staged prompt entry exists for the report's round.
- Halt label is `resolve the halt`; text is `Resolve <name> r<round> (<cause>: <line>): run <hint>`, or `Resolve <name>: <detail>` for chain and fork-child rows.
- Reader rows with a delivered report (tone `report`) also carry the report case, per the uniform tone rule.
- `go test ./cmd/relevo -run 'TestContractStatusLine|TestStatusChainLineContract' -update` regenerated the two JSON goldens.
