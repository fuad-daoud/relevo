# Plan: placement preference S2 — the server's `GET /v1/actors/{actor}` view

Seed: the agreed design at
`/home/fuad/projects/relevo/docs/specs/2026-09-30-placement-preference-design.md`
(read it from the main tree; this round does **not** copy it into its branch —
S1 ships the spec). Read §5 and §11 first; they are this round's contract.
Where this plan and the spec disagree, halt and say so; the spec is
authoritative.

**Scope: server-side only.** The read endpoint, its feature token, and the
create reorder so a refusal leaves nothing behind. This round changes **no**
client code, no config code and no resolution:

- no `internal/relevo` resolution, no `--local`, no bind changes (S3);
- no `internal/roles`, `internal/config`, `internal/view` changes (S1);
- the endpoint answers what a create would answer; it does not change create
  semantics.

## 1. What changes

1. **`internal/remote/proto.go`**
   - `FeaturePlacement = "placement"`: the WhoAmI feature token a server that
     serves `GET /v1/actors/{actor}` advertises. Doc comment in the house
     style; append it to the feature block.
   - The frozen response type:
     ```go
     type ActorView struct {
         Actor      string          `json:"actor"`
         Shape      string          `json:"shape"`      // writer | reader
         Accepted   bool            `json:"accepted"`
         Pick       string          `json:"pick,omitempty"`
         Reason     string          `json:"reason,omitempty"`
         Candidates []CandidateView `json:"candidates"`
     }
     ```
   - Field names are the wire contract; do not rename or add later without a
     spec change.
2. **`internal/serve/actors.go`** (new)
   - `handleGetActor`, shaped like `handleCandidates` (`internal/serve/candidates.go`):
     lock, `callerOf`, `s.runtime(caller)`; unauthenticated/unknown-caller
     behavior follows candidates exactly.
   - Actor from `r.PathValue("actor")`, normalized exactly as
     `buildServedBinding` does: `role := relevo.NormRole(...)`, then
     `roleName := role; if roleName == "" { roleName = "builder" }` (same
     `orText` rule, `bindings.go:203`).
   - `shape, err := relevo.ActorShape(rt, role)`; an unknown actor answers
     `404` with `remote.CodeNotFound` and the shape error text.
   - Pin: `r.URL.Query().Get("candidate")`.
   - `accepted`/`pick` come from
     `relevo.PickServedCandidateFor(rt, roleName, pin)` — the same call
     `pickServedTier` makes, which is the point: the probe cannot disagree
     with a create.
     - no pin: `accepted = token != ""`, `pick = token`; when empty, set
       `reason` to a short sentence ("no candidate serves <actor>").
     - pin, error non-nil: `accepted = false`, `reason = err.Error()`,
       `pick = ""`.
     - pin accepted: `accepted = true`, `pick = token`.
   - `candidates`: the actor's ranked candidates from
     `rt.RoleRegistry().Role(name).Ranked`, in order, `Off` entries included
     (an explicit pin may name one). Resolve name/kind through
     `rt.Candidates` exactly as `handleCandidates` does; a token missing from
     the set still lists with its token. Gate flags from
     `availability.Gates(relevo.AvailabilityDeps(rt))` the same way. Never
     null: `[]remote.CandidateView{}`.
3. **`internal/serve/routes.go`**
   - register `mux.HandleFunc("GET /v1/actors/{actor}", s.handleGetActor)`
     next to `/v1/candidates`;
   - append `remote.FeaturePlacement` to the WhoAmI feature list (`:115`).
4. **`internal/serve/bindings.go`**
   - `buildServedBinding`: move the `pickServedTier` call (and its failure
     return) **before** `Git.InitBare`, so a refused actor, candidate or tier
     leaves no bare repo behind — the function's own comment already promises
     this, the order contradicts it. Nothing else moves; keep the function
     within 70 lines or split minimally.
5. **Plan file**: save this plan as `docs/plans/2026-09-30-placement-s2-server.md`
   in this worktree and commit it. Do not copy the spec.

## 2. Ordered steps (done-when)

1. Proto types + feature const; package builds.
2. Handler + route; the new endpoint tests in §3 pass.
3. Feature list + `internal/serve/serve_test.go:356` exact-list update; that
   test passes.
4. Reorder in `buildServedBinding`; the no-bare-repo test passes.
5. `make check` once; commit on this binding's branch, message like
   `feat(serve): report an actor's served candidates over the wire` (no issue
   number).

## 3. Tests

New `internal/serve/actors_test.go` (follow the existing serve test harness;
if `candidates_test.go` exists, follow its setup exactly). Cases:

- enrolled `GET /v1/actors/builder` → 200: `actor` set, `shape` writer,
  `accepted` true, `pick` equals the test config's builder pick, `candidates`
  in ranked order with a known token and kind.
- unknown actor → 404, `remote.CodeNotFound`.
- `?candidate=<served token>` → accepted true, pick that token.
- `?candidate=<unknown token>` → accepted false, reason non-empty.
- a gated ranked token (record a gate in the availability ledger the test
  config uses) → `gated` true in the view; when every ranked candidate is
  gated, no pin → accepted false with a reason. If recording a gate in the
  serve harness is not possible without touching production code, say so in
  the report and cover the pin-refusal arm only.
- a reader actor → `shape` "reader".
- `internal/serve/serve_test.go`: the exact `wantFeatures` list gains
  `remote.FeaturePlacement` in the same position as `routes.go`.
- reorder: create with an unknown actor answers 4xx **and** no bare repo
  directory exists under the owner's repo root afterwards; if the harness
  cannot observe that path, add an init counter to the existing fake git (test
  code only) and assert zero inits after the refusal.

## 4. Mutation pins

Each mutation must make the named test fail; name each result in the report.

- Remove `remote.FeaturePlacement` from the WhoAmI list → the features test
  fails.
- Make the handler ignore the `candidate` query (always accepted) → the
  pin tests fail.
- Use `PickServedCandidate` (builder) instead of `...For(rt, roleName, pin)`
  → the reader-actor and unknown-actor tests fail.
- Revert the reorder → the no-bare-repo test fails.

## 5. Files this round touches (closed)

1. `internal/remote/proto.go`
2. `internal/serve/actors.go` (new)
3. `internal/serve/actors_test.go` (new)
4. `internal/serve/routes.go`
5. `internal/serve/bindings.go`
6. `internal/serve/serve_test.go`
7. `docs/plans/2026-09-30-placement-s2-server.md` (new, this plan)

Anything outside this list is a halt. In particular, do not touch
`internal/relevo`, `internal/roles`, `internal/config`, `internal/view` or
`cmd/`.

## 6. Rules

- `make check` is the gate: gofmt, vet, tests, comment and file-size checks.
  Run it once at the end, not per step.
- Comments say why, never what; no issue numbers, no "round N", no spec
  citations in code (repo rule).
- Functions ≤ 70 lines, non-test files ≤ 600; if a new exclusion seems needed,
  halt instead.
- Coverage: if `make check` reports the baseline check, say so in the report;
  never lower a baseline. Regenerate with `sh scripts/check-coverage.sh
  --write` only if the round moved enough code to justify it, and say so.

## 7. What the report must include

- `git diff --stat` compared against §5; anything outside the list is a halt.
- One example response body for the builder actor and one refusal, as tests
  observed them.
- For each mutation in §4: the mutation made and the named failing test.
- `make check` result, and whether the coverage baseline moved.
- Confirmation that this plan is committed in this branch.
