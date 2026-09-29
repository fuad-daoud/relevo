# Cockpit wave 1: integration round

The branch `cockpit/wave-1` is `main` plus six verified slices of the cockpit spec
(`docs/specs/2026-09-24-cockpit-design.md`, on this branch), merged without textual
conflicts:

| slice | adds |
|---|---|
| A1 | candidate names |
| A2a | `internal/agentsrc` |
| A3a | config revisions, `config log` / `rollback` |
| A5a | the scratch worktree |
| B1 | the k9s-style shell for `relevo ui` |
| C2a | `internal/stats` and the new `history --stats` |

Their plans are in `docs/plans/2026-09-24-cockpit-*.md`. Each slice was tested alone.
This round makes them work **together**. Nothing else.

**If a step is impossible as written or contradicts the code, stop and report. Do not
improvise.** CI has no harness and no network. No `cmd/relevo` test may run a verb that
spawns a harness or reaches a server, and none may run `relevo ui` or bare `relevo`.

## What breaks today

`go test ./cmd/relevo/` fails three tests on this branch, and they pass on their own
slices:

- `TestConfigLogListsRevisions`
- `TestConfigLogJSON`
- `TestConfigRollbackYes`

The cause: A1's `EnsureCandidateNames` (`internal/config/names.go:21-82`) runs in every
`newRuntime`. When a candidates body lacks names, it writes them back through an
unlabelled `s.Put` (names.go:77). Under A3a every write is a revision, so:

- a user's `config set candidates` is followed by an extra revision with source
  `unknown` (`candidates[0].name add`);
- after `config rollback`, the names are re-added as yet another `unknown` revision.

## Steps

**W1. Names are filled on every write of the candidates section.**
- Deliverable in `internal/config`: move the name-filling transform out of
  `EnsureCandidateNames` into a pure func:

  ```
  func fillCandidateNames(body []byte) (filled []byte, changed bool, err error)
  ```

  It decodes the body as `[]map[string]json.RawMessage` and `[]candidate.Candidate`,
  runs `candidate.DeriveNames`, and sets `"name"` only where it is missing. It
  re-encodes with the same indent `EnsureCandidateNames` uses today, or returns the
  input unchanged when nothing was missing.
- `Put(Candidates, body)` (config.go:281) and `PutDoc` (for the candidates key,
  config.go:322) call it **before** `Validate`, then store the filled body. The body
  the user wrote gains names in the same write, and the same revision.
- A body that fails to decode is passed through unchanged so that `Validate` reports
  its error. `fillCandidateNames` never masks a validation error.
- `EnsureCandidateNames` then only matters for data written before names existed. Its
  write becomes `s.As("migration", "candidate names derived").Put(...)`.
- Tests:
  - `TestFillCandidateNames`: missing names get filled, present names are kept, bad
    JSON passes through.
  - Extend `TestEnsureCandidateNamesWritesOnce` so the revision it writes has source
    `migration`.
  - `TestPutCandidatesRecordsOneRevision`: one `Put` of a nameless candidates body
    gives exactly one revision, whose changes include the `candidates` add with names.

**W2. The three failing CLI tests pass.**
- Re-run the three tests after W1. They must now pass.
- If an assertion must change because the stored candidates body now carries
  `"name"` (a diff line in the rollback output, for example), change **only** that
  assertion, and quote the before and after in the report.
- If a test still sees an `unknown` revision, W1 is incomplete. Fix W1; never the
  test.

**W3. Names reach the three surfaces A1 could not touch.**
- **`history --stats`:** `cmd/relevo/history.go:469` passes identity to
  `stats.Render`. Pass `rt.Candidates.NameOf` instead.
- **The fleet's ON column:** `internal/ui/view_fleet.go:411-421` `candidateText`
  returns `b.BuilderName` when it is non-empty (A1 fills it). Otherwise keep the
  current fallback. Delete the "A1 round 2" comment.
- **The dashboard:**
  - `internal/ui/dash`'s `Model` gains an exported
    `Names func(token string) string`. A nil value means identity.
  - The builder column (`dash/render.go` `roundColumns`) and a `by:builder` group
    key (`groupLine`) print `Names(token)`.
  - `internal/ui/view_rounds.go` sets it from `env.Src.Base().Candidates.NameOf`.
    `NameOf` is nil-safe, so a serve source works too.
- **Grammar:** the fleet context line (`view_fleet.go:210`) says `1 binding` for
  one, as `1 needs you` already does.
- **Goldens:**
  - Regenerate only the ui and dash goldens whose builder cells now show names:
    `go test ./internal/ui/... -run TestGoldenViews -update`.
  - The fixtures' tokens may not resolve, because their candidate sets may be nil.
    In that case nothing changes; say so.
  - Paste any golden diff into the report.

**W4. Full check.**
- `make check` and `make e2e` must both pass.
- Mutation check: in W1, stop calling `fillCandidateNames` from `Put`.
  `TestPutCandidatesRecordsOneRevision` and at least one of the three W2 tests must
  fail. Name them, then revert.
- Report:
  - each step's status;
  - the changed functions with their line ranges;
  - any assertion changed in W2, with the reason;
  - `git diff --stat`, which must touch only `internal/config`, `cmd/relevo`,
    `internal/ui` and `internal/ui/dash`.

## Working efficiently

Read `internal/config/names.go`, `config.go` (`Put`, `PutDoc`), `revision.go`
(`record`), `cmd/relevo/config_test.go` (the three tests), `cmd/relevo/history.go:469`,
`internal/ui/view_fleet.go`, `internal/ui/view_rounds.go` and `internal/ui/dash/render.go`
once, in one batch. Make each file's edits in one call. Iterate on
`go test ./internal/config/... ./cmd/relevo/ -run 'Config|Names|History' ./internal/ui/...`.
Run `make check` and `make e2e` once at the end.
