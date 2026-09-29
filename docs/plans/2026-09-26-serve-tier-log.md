# serve: the startup builder-tier log resolves through the roles registry (#426) (2026-09-26)

## 1. Overview

At startup, `relevo serve` logs the builder tier once, and warns when it comes out as `harness`. It computes that tier
on a runtime built by `serveTierRuntime` (`cmd/relevo/serve.go:217-233`). That runtime has no `Registry`, so
`Runtime.RoleRegistry()` (`internal/relevo/runtime.go:287`) falls back to `legacyRegistry(candidates, policy)`. Since
0.13 that fallback carries no role tier, so the result depends on which candidate comes first, not on
`roles.json`/actors.

On contabo this logged a false warning, "headless builders at tier harness deny every tool…", while every served
round really ran at `yolo`. The request path (`Server.runtimeAt`, `internal/serve/serve.go:211-230`) already passes
`Registry`, so served rounds are correct. Only the startup log is wrong.

The fix: pass the registry `cmdServeRun` already holds (`reg`, from `L.Registry`, `cmd/relevo/serve.go` ~line 418)
into `serveTierRuntime`. Behaviour of served rounds does not change.

## 2. Files

- `cmd/relevo/serve.go`:
  - `serveTierRuntime` (lines 217-233) gains a registry parameter;
  - its one call site (~line 420) passes `reg`.
- `cmd/relevo/serve_test.go`: `TestServeTierRuntimeHasClock` (~lines 210-240) passes `nil` for the new parameter. Add
  one new test here (§4 step 2b).
- `internal/relevo/served_test.go`: one new test, next to `TestServedBuilderTier` (~line 93).
- `docs/plans/2026-09-26-serve-tier-log.md`: this plan, copied as the last step.

## 3. Contracts

### `serveTierRuntime`

- **New signature:**

  ```
  serveTierRuntime(candidates *candidate.Set, pol policy.Policy, reg *roles.Registry, root string, d *db.DB) relevo.Runtime
  ```

- It sets `Registry: reg` on the returned `relevo.Runtime`. Every other field is unchanged.
- A nil `reg` is allowed: `RoleRegistry()` then falls back exactly as today.
- Update the doc comment by one clause: the registry is passed so the logged tier is the tier served rounds get,
  because they resolve through the same registry.
- Remove the `(P5 §4.3)` citation from the comment while there, since that line is being edited anyway. Keep the
  sentence.
- `serve.go` may need the `internal/roles` import. Inside `cmdServeRun`, a local variable named `roles` shadows the
  package after ~line 428. That does not affect `serveTierRuntime`, which is a separate function.

### Call site (`cmdServeRun`, ~line 420)

- Change it to `serveTierRuntime(candidates, pol, reg, root, d)`. `reg` is already in scope from
  `candidates, pol, reg := L.Candidates, L.Policy, L.Registry`.

## 4. Steps

### 0. Working efficiently

**How to work:**
- In one batch, read:
  - `cmd/relevo/serve.go:1-40,210-235,395-432`;
  - `cmd/relevo/serve_test.go:1-30,205-245`;
  - `internal/relevo/served_test.go:1-110`;
  - `internal/relevo/actors_list_test.go:80-112`. This shows how to build a registry whose builder role has a tier,
    through `actors.ToRolesFile` and `roles.Build`.
  - `internal/relevo/served.go:180-235`.
- Make each file's change in one edit.

**Commands:**
- First run `mkdir -p $HOME/.cache/go-tmp`, then `export TMPDIR=$HOME/.cache/go-tmp GOTMPDIR=$HOME/.cache/go-tmp`.
- Focused: `go test ./internal/relevo/ -run 'ServedBuilderTier' -count=1`, then
  `go test ./cmd/relevo/ -run 'ServeTierRuntime' -count=1`.
- Final:
  - `go test ./internal/relevo/ ./cmd/relevo/ -count=1 -race`
  - `go vet ./...`
  - `test -z "$(gofmt -l $(git ls-files '*.go'))"`
  - `make lint`
  - `sh scripts/check-comments.sh`
  - `sh scripts/check-filesize.sh`
- If `git ls-files` fails in this worktree, run `gofmt -l` on the touched files by hand, and say so.
- Skip `make check`; the planner runs it.
- **The `cmd/relevo` test must not run `relevo serve` or any subcommand.** It calls `serveTierRuntime` directly, a
  pure function, like the existing `TestServeTierRuntimeHasClock`. CI has no harness and no network.
- Comments say *why*, with no issue numbers, `§` or plan references. Test names say what they pin. New tests call
  `t.Parallel()` when their neighbours do. Do not add entries to any `scripts/*.allow` file or to `.golangci.yml`.

### 1. `serveTierRuntime` and its call site (§3)

### 2. Tests

- **a. `internal/relevo/served_test.go`, `TestServedBuilderTierFollowsTheRoleRegistry`:**
  - Build a candidate set with one builder candidate that has **no** tier of its own.
  - Build a registry whose builder role has tier `yolo`. Build it the way `actors_list_test.go` does:
    `actors.ToRolesFile` with a `builder` actor listing that candidate at `Tier: "yolo"`, then
    `roles.Build(rf, set, policy.Policy{MaxTier: "yolo"})`.
  - With `Runtime{Candidates: set, Policy: policy.Policy{MaxTier: "yolo"}, Registry: reg, Now: <fixed clock>}`,
    `ServedBuilderTier(rt) == harness.TierYolo`.
  - The same runtime with `Registry: nil` does **not** return `TierYolo`. Assert `!= TierYolo` rather than a specific
    value, since the legacy fallback's answer is not what this test pins.
  - If `ServedBuilderTier` needs other fields (for example `Gates` or `GatesDir`) to avoid a nil dereference, copy
    how `TestServedBuilderTier` builds its runtime.
- **b. `cmd/relevo/serve_test.go`, `TestServeTierRuntimeCarriesTheRegistry`:**
  - Build a registry as in (a), or simply `roles.Build(nil, set, pol)`, since only identity matters here.
  - `serveTierRuntime(set, pol, reg, t.TempDir(), nil).Registry == reg`.
- **c.** In `TestServeTierRuntimeHasClock`, pass `nil` for the registry. Its assertions are unchanged.

**Required mutation:** in `serveTierRuntime`, drop `Registry: reg`. Test b must fail. Report the failing line, then
revert. Also report whether test a's second half (nil registry) returns something other than `yolo`. If it returns
`yolo`, the test does not pin the bug: halt and report the value.

### 3. Checks, the plan, the commit

1. Run the final commands listed in step 0.
2. Copy this plan to `docs/plans/2026-09-26-serve-tier-log.md`.
3. Make a new commit:

   ```
   git add -A && git commit -m "fix(serve): the startup builder-tier log resolves through the roles registry (#426)"
   ```

## 5. Deletions

None.

## 6. Stop rather than improvise

Halt and report if any of these happens:
- `cmdServeRun` has no `reg` in scope, or `serveTierRuntime` has more than one call site outside tests.
- In test a, the nil-registry runtime also yields `yolo`.
- Making test a pass needs a change to `internal/relevo` production code.
