HARD RULES: do not push, do not run `gh`, do not touch any remote or PR; relevo owns the branch. Do only this plan, and end it in its own commit.

Design: `docs/specs/2026-10-23-candidate-windows-design.md` in this tree; every § reference points there. Read the sections this plan cites before editing.

# Plan 5 — the demoted order

Deliverable: a `demote` window moves its candidate to the end of the ranked
order, and `relevo config` shows it there.

1. Add the shared partition in `internal/relevo/candidate.go`:
   `rankedRoleFor(reg, set, role, gates)` = `rankedRole` then move the entries
   with a `Demote` gate to the end; keep `rankedRole` as the no-gates form and
   `rankedList` as the legacy form, both delegating.
2. Use it in `resolveRoleBase` (`candidate.go:358`), `FormatPolicyFor`
   (`policy_view.go:331`), `RoleRefusalsFor` (`:426`) and `FormatPolicy` via
   `rankedList` (`:285`).
3. Split `skipsFor` into `skipGates` (everything but `Demote`) for the walk and
   the refusal, and keep `skipsFor` for the explicit pick's advisory record
   (`candidate.go:170-179`, `:369-402`).
4. Carry the gate's note on the skip: `Skip.Note` and `skipText` through
   `GateText` (`candidate.go:56-63`, `:198-203`).
5. Write `TestDemotedCandidateMovesLast`, `TestDemoteNeverRefusesTheSoleCandidate`,
   `TestDemoteRowShowsAtTheDemotedPosition`.
6. Run `go test ./internal/relevo/... -run 'Demote|Policy'` — green, and
   `TestDemotedCandidateMovesLast` fails if the partition is applied to the
   walk but not to the rows.

Commit: `relevo: a demote window moves a candidate to the end of the order`.
