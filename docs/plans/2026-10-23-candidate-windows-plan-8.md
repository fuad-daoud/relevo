HARD RULES: do not push, do not run `gh`, do not touch any remote or PR; relevo owns the branch. Do only this plan, and end it in its own commit.

Design: `docs/specs/2026-10-23-candidate-windows-design.md` in this tree; every § reference points there. Read the sections this plan cites before editing.

# Plan 8 — a reset at now is lifted now (#1304)

Deliverable: a session limit recorded at its own reset time gates for zero
time, and the ledger stays valid. Independent of everything else; can run first
or in parallel.

1. In `parseClockReset` (`internal/availability/limit.go:237-262`), when the
   clock time is not after `now`, treat it as lifted now when it is within
   `clockResetGrace` (1 hour) in the past, and roll to tomorrow only beyond
   that. Name the constant with the reason.
2. Clamp in `Unavailable` (`internal/availability/gates.go:151-189`): an
   `until` before the entry's `At` becomes `At`, so a lifted-now gate can never
   be written as `until precedes at` (which `decode` refuses, taking the whole
   ledger with it).
3. Write `TestParseClockResetAtNowIsLifted`, `TestParseClockResetWellPastIsTomorrow`,
   `TestGateWithAnImmediateResetKeepsTheLedgerValid`,
   `TestReasonResetAtNowLiftsTheGate` in `internal/availability/limit_test.go`
   and `gates_test.go`; keep the existing clock rows green.
4. Run `go test ./internal/availability/... -run 'Clock|Reset|Ledger'` and
   `go test ./cmd/relevo/ -run Gate` — green, and
   `TestGateWithAnImmediateResetKeepsTheLedgerValid` fails if the clamp is
   removed.

Commit: `availability: a clock reset at now means the limit is lifting`.
