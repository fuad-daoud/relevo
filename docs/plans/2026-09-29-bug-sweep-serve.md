# Plan: the serve create path — repo_id, owner-store lock, candidate refusal (#653, #656, #661)

One builder round, three commits on the round's branch, one issue per commit, **nothing pushed**. `make check` runs once at the end. Line numbers are from `64d2715b`; if a line has moved, find the named function.

## 0. Seed vs code before step 1

1. **#661 is not fixable inside `internal/serve` alone.** Decision 1 says the scope is "all in `internal/serve` plus tests", but the fallback decision 4 names is `PickServedCandidateFor` in `internal/relevo/served.go:233-250` (the `rt.Candidates.Resolve` call at 245). The fix lands there, and one unit test in `internal/relevo`; the create path that maps it to 422 is `internal/serve/bindings.go:139`. No other `internal/relevo` file changes. If the MasterMind wanted the fallback left in place, halt instead of inventing a narrower fix.
2. **`buildServedBinding`'s comment overstates.** `bindings.go:159-162` says "a refused create leaves no bare repo behind", but `InitBare` (185) runs before `pickServedTier` (194), so a tier or candidate refusal *does* leave the bare repo. This round does not reorder them (out of scope): the #661 test must not assert the bare repo is absent. The #653 guard must sit between the join (184) and `InitBare` (185), so no `MkdirAll` ever sees an escaping path.
3. **Ten create bodies in `serve_test.go` will break.** Every one of them carries `repo_id` `"repo123"`/`"repo1"` (lines 380, 513, 563, 698, 750, 807, 886, 916, 953, 997; joined bare paths at 535, 851). Step 1 retargets them to one canonical test id before the check tightens; the tests keep their own subjects and every other assertion.
4. The seed says `parseCreateRequest` "validates repo_id as exactly 64 lowercase hex characters" — today it only refuses `""` (78-80). That is the fix, not a contradiction.

## 1. Behaviour and cases

**#653 — repo_id.** `POST /v1/bindings` refuses a `repo_id` that is not exactly 64 lower-case hex characters with 400 `invalid` naming the field: empty, 63/65 chars, upper-case hex, any non-hex byte. Defence in depth after the join: `filepath.Clean(bare)` must be `repoRoot` itself or under it (`..` elements, sibling-prefix roots such as root `/r/a` and path `/r/ab/x`), refused 400 `invalid` **before** `InitBare`. The accepted shape is what `remote.RepoID` emits (`internal/remote/proto.go:393-400`).

**#656 — the owner-store lock.** `GET /v1/whoami` and `GET /v1/candidates` hold `s.mu` for every touch of `s.stores` (`s.runtime`, and whoami's `s.census`), exactly as the binding handlers do. Responses do not change; create, whoami and candidates run concurrently under `-race` with no report.

**#661 — explicit candidates.** When the tenant names a candidate and `resolveRole` refuses it — not in the actor's list (`ErrRoleNotServed`) or a `roles_missing` gate — the create answers 422 `invalid` carrying the refusal's message, stores nothing, and never falls back to `rt.Candidates.Resolve`. Naming no candidate keeps today's path: the actor's ranked pick, or an empty candidate when nothing serves the actor (that case is out of scope and stays).

## 2. Seams

| Fix | File:line | What |
|---|---|---|
| #653 | `internal/serve/bindings.go:70-98`, gate 78-80 | tighten the repo_id check; `isHex` (29-36) accepts A-F, so the lower-case half is added here |
| #653 | `internal/serve/bindings.go:179-188`, join 184, `InitBare` 185 | the guard goes between join and InitBare |
| #653 | `internal/serve/serve.go:193-199` (`repoRoot`) | the containment helper lives beside it |
| #653 tests | `internal/serve/serve_test.go:380,513,563,698,750,807,886,916,953,997`, bare paths 535, 851 | retarget to one test id |
| #656 | `internal/serve/routes.go:99-121` (`handleWhoAmI`; `s.runtime` 108, `s.census` 112) | add `s.mu` |
| #656 | `internal/serve/candidates.go:12-56` (`handleCandidates`; `s.runtime` 14) | add `s.mu` |
| #656 | `internal/serve/serve.go:69,73-74,133-142,213-224`; `internal/serve/admit.go:43-96` | fields and callers that already assume the lock; unchanged |
| #656 test | `internal/serve/serve_test.go`, beside `TestWhoAmI` (317) | new `-race` test |
| #661 | `internal/relevo/served.go:233-250` (`PickServedCandidateFor`, fallback 243-248), 252-255, 224-231 | third return; delete the fallback |
| #661 | `internal/serve/bindings.go:136-157` (`pickServedTier`, call 139) | map the refusal to 422 `invalid` |
| #661 | `internal/serve/candidates.go:26` | third return value |
| #661 | `internal/serve/rounds.go:293-311` (298-299) | the 422 `invalid` shape to match |
| #661 tests | `internal/serve/serve_test.go:2136-2180` (`candidatesViewServer`), `helpers_test.go:412-420` (`builderCandidateSet`); `internal/relevo/served_test.go` | new tests |

Size: `internal/serve/bindings.go` is 589 lines against the 600 cap; the whole change adds about five there. If it would cross, move the guard's message onto the existing lines rather than adding a file.

## 3. Ordered steps

Every focused command runs with `-count=1`; every reported error is fixed before the next run.

**Commit 1 — #653**

1. **Add `const testRepoID` and retarget the create bodies.** In `serve_test.go`, one 64-lower-case-hex constant; replace the ten `RepoID:` literals and the two joined bare paths (535, 851). Done when: `go test -count=1 ./internal/serve/` passes on the untightened parser.
2. **Tighten the parser.** In `parseCreateRequest`, refuse unless the id is 64 chars, hex and lower-case, message "repo_id must be 64 lowercase hex characters". Done when: `go build ./...`.
3. **Add the guard.** New `insideRoot(root, p string) bool` beside `repoRoot` in `serve.go` (`filepath.Rel`, so a sibling prefix is not "inside"); in `buildServedBinding`, between the join and `InitBare`, refuse with 400 `remote.CodeInvalid`. Done when: `go test -count=1 -run 'TestCreate|TestServerRefuses|TestListIsOwnerScoped|TestGetTouchesLastSeen|TestOwnerDirIsFlatHex' ./internal/serve/` passes.
4. **Add the two pure tests.** `TestParseCreateRequestRepoID` drives `parseCreateRequest` with `httptest.NewRequest` bodies (no server, git or network — the same reason CI can run it) and accepts only the canonical row; `TestInsideRootRefusesEscapes` tables inside/equal/parent/absolute/sibling-prefix paths. Done when: `go test -count=1 -run 'TestParseCreateRequestRepoID|TestInsideRootRefusesEscapes' ./internal/serve/` passes.
5. **Mutations, then commit** `fix(serve): ...` with `Fixes #653` in the body.

**Commit 2 — #656**

6. **Lock both handlers.** `handleWhoAmI` and `handleCandidates` take `s.mu` for the whole body (`defer s.mu.Unlock()`, the file's style); `Clients` takes only its own mutex and never calls back, so the nesting is safe. Done when: `go test -count=1 ./internal/serve/` passes.
7. **Add the race test.** `TestWhoAmIAndCandidatesDoNotRaceWithCreate`: one server, one enrolled owner, create bodies with distinct names plus whoami/candidates requests, a start barrier and a `WaitGroup`; build the signed requests on the test goroutine (`signedRequest` calls `t.Fatalf`) and let workers only `ServeHTTP`. Done when: `go test -race -count=1 -run TestWhoAmIAndCandidatesDoNotRaceWithCreate ./internal/serve/` passes.
8. **Mutation, then commit** `fix(serve): ...` with `Fixes #656` in the body.

**Commit 3 — #661**

9. **Refuse the explicit pick at its source.** `PickServedCandidateFor` (and `PickServedCandidate`) grows an error return: an explicit token `resolveRole` refuses is returned as that error, and the `rt.Candidates.Resolve` fallback is deleted; the empty token still resolves to `("", "")` as today. Adapt `ServedBuilderTier` (served.go:225) and `handleCandidates` (candidates.go:26). Done when: `go build ./... && go test -count=1 ./internal/relevo/`.
10. **Answer 422 in the create path.** In `pickServedTier`, a non-nil error is written as 422 `remote.CodeInvalid` with the refusal's own message (the same status and code the round path writes for an explicit pick at `rounds.go:298-299`); no `ErrBadBuilder` wrapper, whose "send --candidate" prefix belongs to the send verb, not the wire create. Done when: `go test -count=1 ./internal/serve/`.
11. **Add the tests.** `TestCreateRefusesACandidateTheRoleDoesNotServe` (a two-candidate set where the named one does not serve the requested actor, plus a `roles_missing` row with a stub `harness.RoleChecker`) asserts 422 `invalid`, a message naming the candidate, and no stored binding; `TestPickServedCandidateRefusesAnUnservedExplicitToken` pins the pure refusal in `internal/relevo/served_test.go`. Done when: `go test -count=1 -run TestCreateRefusesACandidateTheRoleDoesNotServe ./internal/serve/ && go test -count=1 -run TestPickServedCandidateRefusesAnUnservedExplicitToken ./internal/relevo/`.
12. **Mutation, then ship.** Restore both mutations, copy the round's plan verbatim (no edits) to `docs/plans/2026-09-29-bug-sweep-serve.md`, run `make check` once, commit `fix(serve): ...` with `Fixes #661` in the body, and stop — no push.

## 4. Mutation checks

| Mutation in the throwaway tree | Test that must fail |
|---|---|
| #653a: put `req.RepoID == ""` back in `parseCreateRequest` | `TestParseCreateRequestRepoID` (the "repo1", 63-char and upper-case rows are accepted) |
| #653b: make `insideRoot` return `true` | `TestInsideRootRefusesEscapes` (deleting the call instead leaves the function unused, which `make check`'s `unused` linter rejects) |
| #656: delete the `s.mu` lines from both handlers | `go test -race -count=1 -run TestWhoAmIAndCandidatesDoNotRaceWithCreate ./internal/serve/` → `DATA RACE` |
| #661: restore the `rt.Candidates.Resolve` fallback | `TestCreateRefusesACandidateTheRoleDoesNotServe` (201, not 422) and `TestPickServedCandidateRefusesAnUnservedExplicitToken` |

Restore each before the next step.

## 5. Closed list of what this round deletes

1. The `if token != ""` fallback block in `PickServedCandidateFor` (`internal/relevo/served.go:243-248`) — the `rt.Candidates.Resolve` call and its comment.
2. The `"repo_id is required"` message and the non-empty-only check it guards (`internal/serve/bindings.go:78-80`).

Nothing else. No test is deleted; the ten create bodies are retargeted, not removed; no source file is added; no lint, size or coverage exclusion is touched.

## 6. Report must include

- The three commits, subject and SHA, with `git diff --stat` each; `git status` clean and nothing pushed.
- Every focused command above with its pass output, then `make check`.
- Each mutation: the exact edit, the named test that failed with its first failure line, and that it was restored.
- The ten create bodies and two bare paths retargeted, old → new id, and confirmation every other assertion is unchanged.
- `internal/serve`'s statement coverage from `.coverage.txt` before and after, and that `testdata/coverage-baseline.txt` was not rewritten (79.7 today; one point of slack).
- §0 items 1 and 2 restated, so the MasterMind sees the `internal/relevo` scope deviation and the unchanged bare-repo order.
- Anything the builder halted on, instead of improvising.
