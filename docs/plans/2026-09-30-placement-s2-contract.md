# Plan: placement preference S2 round 2 — the contract pins, the pick flag, and the commit

Round 1 of this binding implemented the whole of
`docs/plans/2026-09-30-placement-s2-server.md` §1 and verified it, then halted
at §2 step 5 because `make check` failed on two contract-pin tests that §5's
closed list omitted. That omission was the plan's defect, not the round's: the
repo deliberately pins every exported wire struct and every route, so adding
`ActorView` and `GET /v1/actors/{actor}` must amend those pins. This round
finishes the change.

**Input:** the uncommitted round-1 work in this worktree (4 modified files,
3 new). Do not revert or rewrite it. Read the round-1 plan and its report
(they are in the worktree and the round history). If the worktree does not
match the round-1 report's file list, halt and say so.

**This round completes:** the two contract pins and their goldens, one small
handler correction (the per-candidate `Pick` flag), `make check`, the commit.
It changes nothing else.

## 1. What changes

1. **`internal/remote/contract_test.go`**
   - Add a `filledActorView()` helper next to `filledCandidatesResponse()`:
     `Actor: "builder"`, `Shape: "writer"`, `Accepted: true`,
     `Pick: "agy/openai/gpt-4"`, `Reason: ""`,
     `Candidates: []CandidateView{filledCandidateView()}` — the non-nil slice
     keeps the golden from containing `null`.
   - Add `{"ActorView", filledActorView(), func() any { return new(ActorView) }},`
     to `protoCases`, in the same relative position `ActorView` has in
     `proto.go` (after the `CandidatesResponse` case).
2. **`internal/remote/testdata/contract/proto-ActorView.golden`** (new,
   generated, never hand-written): `go test ./internal/remote -run Contract -update`,
   then read the golden to confirm every field name and shape, then re-run
   without `-update` to confirm stability.
3. **`internal/serve/contract_test.go`**
   - Add `{"GET /v1/actors/{actor}", "GET", "/v1/actors/test-actor"},` to
     `registeredRoutes` directly after the `/v1/candidates` entry.
4. **`internal/serve/testdata/contract/routes.golden`** (regenerated with
   `go test ./internal/serve -run Contract -update`; the new line is expected
   to read `GET /v1/actors/<path>  401 not_enrolled` — confirm the actual
   line, then re-run without `-update`).
5. **`internal/serve/actors.go`** (and, if the doc comment needs the
   generalization, `internal/remote/proto.go`)
   - The actor view's candidate entries set `CandidateView.Pick`: exactly the
     entry whose token equals the returned `pick` when `accepted` is true;
     all false otherwise. `/v1/candidates` sets this flag for its pick, and
     the frozen struct always emits it, so a meaningful value is better than
     a constant `false`. If `proto.go`'s doc comment on `Pick` says "for the
     builder actor", generalize it to the actor the view was requested for
     (`/v1/candidates` always requests builder, so the wording stays true).
6. **`internal/serve/actors_test.go`**
   - Pin the flag: for the accepted builder case, exactly one candidate entry
     has `Pick == true` and its token equals the top-level `pick`; for the
     refusal case, every entry is false.
7. **`docs/plans/2026-09-30-placement-s2-contract.md`** — save this plan in
   the worktree and commit it. Do not copy the spec (S1 ships it).

## 2. Ordered steps (done-when)

1. Contract cases + helpers; regenerate both goldens; `go test
   ./internal/remote ./internal/serve -run Contract -count=1` passes without
   `-update`.
2. The `Pick` flag in the handler and its pin test pass.
3. The round-2 mutation pins in §3 all fail their named tests, then are
   reverted.
4. `make check` once, at the end: green.
5. Commit **all** round-1 and round-2 work in one commit on this binding's
   branch, message `feat(serve): report an actor's served candidates over the
   wire` (no issue number). The commit carries:
   `internal/remote/proto.go`, `internal/remote/contract_test.go`,
   `internal/remote/testdata/contract/proto-ActorView.golden`,
   `internal/serve/actors.go`, `internal/serve/actors_test.go`,
   `internal/serve/routes.go`, `internal/serve/contract_test.go`,
   `internal/serve/testdata/contract/routes.golden`,
   `internal/serve/bindings.go`, `internal/serve/serve_test.go`,
   `docs/plans/2026-09-30-placement-s2-server.md`,
   `docs/plans/2026-09-30-placement-s2-contract.md`.
   `git status` clean afterwards.

## 3. Mutation pins (round 2)

Each mutation must make the named test fail, then be reverted.

- Remove the `ActorView` case from `protoCases` →
  `TestContractProtoTypesComplete` fails.
- Remove the actors route entry from `registeredRoutes` →
  `TestContractRoutesComplete` fails.
- Force the per-candidate `Pick` assignment off (always false) → the new
  flag pin fails.

## 4. Files this round touches (closed)

1. `internal/remote/contract_test.go`
2. `internal/remote/testdata/contract/proto-ActorView.golden` (generated)
3. `internal/remote/proto.go` (only the `Pick` doc comment, if generalized)
4. `internal/serve/contract_test.go`
5. `internal/serve/testdata/contract/routes.golden` (regenerated)
6. `internal/serve/actors.go`
7. `internal/serve/actors_test.go`
8. `docs/plans/2026-09-30-placement-s2-contract.md` (new, this plan)

The round-1 files are already in the required state; only commit them. Halt
on anything outside this list plus the round-1 list.

## 5. Rules

- `make check` is the gate: gofmt, vet, tests, comment and file-size checks.
  Run it once, at the end.
- Comments say why, never what; no issue numbers, no "round N", no spec
  citations in code.
- Functions ≤ 70 lines, non-test files ≤ 600; no new exclusions.
- Coverage: report whether the baseline moved; never lower it. Round 1
  measured `internal/serve` 80.4 (baseline 79.9) and `internal/remote` 86.0
  (baseline 86.7, inside the 1-point tolerance). The regenerated goldens and
  the pin test must not push either package past tolerance; if they do, say
  so rather than adding exclusions.
- Round 1's notes 2 and 3 are accepted as written (the refused-candidate
  subtest is the arm the reorder decides; the unknown-actor pin is reported as
  it actually behaves). No action.

## 6. What the report must include

- `git diff --stat` for the final commit's parent, compared against §4 plus
  the round-1 list; the full commit hash and subject.
- The new golden's content (both goldens).
- For each round-2 mutation: the mutation and the named failing test.
- `make check` result and the coverage numbers.
- Confirmation that `git status` is clean and the spec was not copied.
