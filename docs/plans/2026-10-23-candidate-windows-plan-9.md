HARD RULES: do not push, do not run `gh`, do not touch any remote or PR; relevo owns the branch. Do only this plan, and end it in its own commit.

Design: `docs/specs/2026-10-23-candidate-windows-design.md` in this tree; every § reference points there. Read the sections this plan cites before editing.

# Plan 9 — the doctor's rows

Deliverable: a schedule is not reported as a fault, and a refusal caused by a
window or a cap gets a fix that works.

1. Filter `ledgerChecks` (`cmd/relevo/doctor_checks.go:110-137`) to the kinds
   the ledger holds, so no row is built for a synthesised gate.
2. Carry the cause on `RoleRefusal` (`internal/relevo/policy_view.go:92-157`):
   whether the refusal is a window or a cap, set in `refusalFromErr`; use it in
   `refusalChecks` (`cmd/relevo/doctor_checks.go:248-272`) to pick the fix —
   wait for the edge, or wait for a round to finish, never `gate --clear`.
3. Write `TestDoctorHidesSynthesisedGates` and `TestRefusalFixNamesTheWindow`.
4. Run `go test ./cmd/relevo/... -run 'Doctor|Refusal'` — green, and
   `TestRefusalFixNamesTheWindow` fails if the fix is still `relevo gate
   --clear`.

Commit: `doctor: a window is not a fault, and a refusal names its fix`.

---
