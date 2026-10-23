HARD RULES: do not push, do not run `gh`, do not touch any remote or PR; relevo owns the branch. Do only this plan, and end it in its own commit.

Design: `docs/specs/2026-10-23-candidate-windows-design.md` in this tree; every § reference points there. Read the sections this plan cites before editing.

# Plan 3 — the window gate

Deliverable: a configured window blocks (or demotes) its candidate, in memory,
and every surface can say why.

1. Add `Scheduled` to the kinds (`internal/availability/ledger.go:23-44`) and
   `Demote bool` to `Gate` (`:217-234`).
2. Add `GateKindText`'s `Scheduled` arm and the two text helpers `GateText(k,
   until, note)` and `Gate.UntilText()` (`internal/availability/gates.go:458-511`).
3. Add `internal/availability/window.go`: `windowGates(d Deps) []Gate` — one
   gate per block window covering `now`, one `Demote` gate per demote window
   covering it, with the note and `Until` from `candidate.Windows`.
4. Append `windowGates(d)` in `GatesFrom`
   (`internal/availability/gates_projection.go:66-93`, after
   `rolesMissingGates`) and in `gatedNote` (`gates.go:516-535`).
5. Route every renderer of a gate's reason through `GateText`/`UntilText`:
   `internal/relevo/policy_view.go:222-226`, `internal/relevo/candidate.go:198-203`,
   `internal/view/render.go:281-290`, `internal/view/statusline_next.go:29-45`,
   `internal/view/candidates_list.go:171-198`,
   `internal/serve/admin.go:467-481`, `cmd/relevo/doctor_checks.go:132`.
6. Write `TestWindowGatesOnlyOutsideTheWindow`,
   `TestWindowGateUntilIsTheNextEdge`, `TestWindowGateIsNeverInTheLedger`,
   `TestGateTextNamesTheWindow` in `internal/availability/window_test.go`.
7. Run `go test ./internal/availability/... ./internal/view/... ./internal/relevo/...`
   — green, and `TestGateTextNamesTheWindow` fails if `GateText` falls back to
   "outside window until <time>".

Commit: `availability: synthesise a scheduled gate from a candidate's window`.
