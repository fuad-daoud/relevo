# relevo serve deletes a bare repo once no live binding uses it; every server unbind releases its refs

Date: 2026-09-25. Base: origin/main (1a0ab78 or later). One round, one PR.
Decision (user, 2026-09-25):

- **Delete a bare repo when unused.** Keeping it gains nothing today, because a binding's
  first send is always a full bundle.
- **Revoke leaves data as it is.**

**Stop rather than improvise.** If a step contradicts the code, or an existing test outside
§7.3 fails, halt and report it. Do not bend a test.

## 1. System overview

On a `relevo serve` host, `<state>/serve/repos/<owner-hex>/<repo-id>.git` is created by
`handleCreateBinding` (internal/serve/bindings.go ~118-127, `InitBare`). It is shared by every
binding that owner makes from the same repository, and nothing ever removes it. Worse, three
unbind paths archive a binding **without** releasing its branch and refs in that repo:

- the client's wire unbind, `handleUnbind` (bindings.go ~343-370);
- `GCAbandoned` (admin.go ~255-327);
- `AdminUnbind` (admin.go ~338-365).

`collectSettled` (cleanup.go ~69-139) does release them, but it only walks live records.

After this round:

1. **Every server unbind releases the binding's git state.**
   - The three paths above, after `relevo.Unbind` succeeds, delete the binding's branch and
     its `refs/relevo/<name>/*` refs from the bare repo.
   - They do this **only when Unbind did not keep a dirty worktree.** A kept worktree still
     has the branch checked out, and relevo keeps dirty work.
   - `collectSettled` does the same through the same helper (its behaviour is unchanged).
2. **The daemon tick prunes unused repos.**
   - After `collectSettled`, still under `s.mu`, the tick deletes every
     `repos/<owner>/<id>.git` that no **live** binding of that owner references
     (`Serve.BareRepo`, or `Repo` when `Serve` is nil).
   - The first tick after start also clears today's leftovers.
   - `handleCreateBinding` holds `s.mu` too (bindings.go ~68), so a create and a prune can
     never interleave.
   - A later bind of the same repository recreates it with `InitBare`, and its first send
     is a full bundle, as it always is.

**Safety rules (non-negotiable):**

- A repo is deleted only when that owner's `Store.List()` **succeeded** and no live binding
  names it. On any list error, that owner's repos are skipped for this tick.
- Only directories that are direct children of `<root>/repos/<valid owner dir>/` and whose
  names end in `.git` are ever deleted. Use `remote.IDFromDir` for the owner dir, as
  `collectSettled` does.
- Nothing outside `repos/` is deleted. Worktrees stay under `bindings/`, handled as today.

## 2. File structure

```
internal/serve/cleanup.go     releaseServedRefs + teardownServed (extracted from collectSettled); unusedRepos (pure); pruneUnusedRepos
internal/serve/daemon.go      Tick calls pruneUnusedRepos after collectSettled
internal/serve/bindings.go    handleUnbind releases refs after Unbind
internal/serve/admin.go       GCAbandoned (non-dry-run) and AdminUnbind release refs after Unbind
internal/serve/cleanup_test.go   N1-N4
internal/serve/serve_test.go (or admin_test.go)   N5-N7
README.md                     the serve collection passage (~770-780)
docs/plans/2026-09-25-serve-repo-prune.md   this plan, verbatim
```

## 3. Data structures

No store, JSON, golden or `BindingFormat` changes.

## 4. Interfaces and contracts (internal/serve)

### 4.1 `releaseServedRefs(ctx context.Context, rt relevo.Runtime, b store.Binding) error`

- Steps 2-3 of today's `collectSettled`, moved unchanged:
  - `rt.Git.DeleteBranch(ctx, bare, b.Branch)`;
  - `rt.Git.ListRefs(ctx, bare, "refs/relevo/"+b.Name+"/")`, then `DeleteRef` on each.
- `bare` is `b.Serve.BareRepo`. When `b.Serve == nil` or `BareRepo == ""`, return nil and do
  nothing.
- It returns the first error.

### 4.2 `teardownServed(ctx context.Context, rt relevo.Runtime, b store.Binding) error`

- Step 1 (`rt.Git.RemoveWorktree(ctx, bare, b.Worktree, true)`), then `releaseServedRefs`.
- `collectSettled`'s body at ~103-126 becomes one `teardownServed` call. It has the same
  log line and the same `continue` on error, so step 4 (`relevo.Unbind`) still does not run
  after a failure.
- Update its doc comment to name the helper. The numbered steps stay.

### 4.3 Server unbind paths release refs

- In each of `handleUnbind`, `AdminUnbind`, and `GCAbandoned` when not `dryRun`:
  - keep the binding `b` loaded before the unbind (each already has it);
  - after `res, err := relevo.Unbind(ctx, rt, b.Name, true)` succeeds, and when
    `res.WorktreeKept == ""`, call `releaseServedRefs(ctx, rt, b)`.
- A release error is `slog.Warn("release served refs", "owner", …, "binding", …, "err", err)`
  and **does not** fail the unbind: the record is already archived. The repo prune removes
  the rest once the repo is unused.
- Check the exact field name for a kept worktree on `relevo.UnbindResult` (internal/relevo,
  `type UnbindResult`) and use it.
- `AdminUnbind` returns the same `UnbindResult` as today.

### 4.4 `unusedRepos(repos []string, live []store.Binding) []string` (pure)

- It returns the entries of `repos` that no binding in `live` references.
- A binding references path p when `b.Serve != nil && filepath.Clean(b.Serve.BareRepo) == filepath.Clean(p)`,
  or when `b.Serve == nil && filepath.Clean(b.Repo) == filepath.Clean(p)`.
- It keeps the input order.

### 4.5 `pruneUnusedRepos(ctx context.Context) int`

This is a method on `*Server`. The caller holds `s.mu`.

1. List `<s.cfg.Root>/repos`.
   - A missing dir returns 0.
   - Skip an entry that is not a directory or fails `remote.IDFromDir`, with a `slog.Warn`
     as `collectSettled` does.
2. For each owner dir:
   1. Get that owner's runtime: `s.runtimeAt(filepath.Join(s.cfg.Root, "bindings", ownerDir))`.
      It is the same runtime `collectSettled` uses. If the bindings dir for that owner does
      not exist, check what `runtimeAt` / `Store.List` do. The owner's records live in the
      shared DB, so List should still work. If it cannot, skip the owner, log it, and say
      so in the report.
   2. `live, err := rt.Store.List()`. **On error, `slog.Warn` and skip this owner.**
   3. Collect the owner's `*.git` child **directories**.
   4. For each path in `unusedRepos(repos, live)`, run `os.RemoveAll(path)`, then
      `slog.Info("pruned unused bare repo", "owner", id, "repo", filepath.Base(path))`.
      A failure is a `slog.Warn`.
   5. After pruning, `os.Remove(ownerDir)`. It removes only an empty dir, and any error is
      ignored.
3. Return the count of repos removed.

### 4.6 Tick (daemon.go ~51-58)

- After the `collectSettled` call and before `admit`, add `s.pruneUnusedRepos(ctx)`. It is
  still under `s.mu`, and the comment says so.
- It never fails the tick.

## 5. Pseudocode

```
Tick (s.mu held): owners' daemon ticks; collectSettled (teardownServed per settled binding, then Unbind);
                  pruneUnusedRepos: per owner, List ok? delete repos/<owner>/*.git no live binding names; remove empty owner dir
handleUnbind / AdminUnbind / GCAbandoned(!dry): Unbind; if no kept worktree: releaseServedRefs (warn on error)
```

## 6. Error handling

No new error types. Every failure in prune or release is a log line, never a failed tick
or failed unbind.

## 7. Ordered implementation steps

### 7.0 Working efficiently

- Read these in one parallel batch:
  - internal/serve/cleanup.go (whole)
  - daemon.go 1-110
  - bindings.go 60-130 and 335-375
  - admin.go 240-370
  - serve.go 180-230 (`runtimeAt`, `runtime`, `repoRoot`)
  - internal/relevo `type UnbindResult`
  - internal/serve/cleanup_test.go (whole)
  - `grep -n "func Test.*Unbind\|func Test.*GCAbandoned\|func TestCreateBinding" internal/serve/*_test.go`
  - README.md 760-790
- Line numbers are from origin/main 1a0ab78. Re-grep if they have moved.
- Iterate with the focused command `go test ./internal/serve/ -count=1`.
- Full check once at the end:
  - `make check`. If a hook blocks it, run its steps directly;
  - paste `gofmt -l $(git ls-files '*.go')`'s empty output;
  - then `make e2e`.
- A cmd/relevo test must not spawn a harness or reach the network. This round adds none.

### 7.1 Steps

1. `releaseServedRefs` and `teardownServed`; `collectSettled` uses them (§4.1-§4.2).
   `TestCollectSettledOnTick` must pass unchanged.
2. `unusedRepos` and `pruneUnusedRepos`, wired into the tick (§4.4-§4.6), with N1-N4.
3. The three unbind paths (§4.3), with N5-N7.
4. README (~770-780), where the serve collection is described. Add:
   - every server unbind releases the binding's branch and refs;
   - a bare repo is deleted once no live binding uses it, and the next bind of that
     repository recreates it.
5. Full check, then the §7.4 mutations, one at a time, restoring after each.
6. Save this plan verbatim at `docs/plans/2026-09-25-serve-repo-prune.md`.
7. Commit and PR.
   - One commit: `feat(serve): delete a bare repo once no live binding uses it; every
     server unbind releases its branch and refs`.
   - Push with `git push -u origin <branch>`. Open a PR against main with the §7.4 results
     in the body.

### 7.2 New tests

- **N1 `TestUnusedRepos`** (table, pure):
  - a repo named by a live binding's `Serve.BareRepo` is kept;
  - one named by nobody is returned;
  - `Serve == nil` with `Repo` set counts as a reference;
  - path cleaning, e.g. a trailing slash, still matches;
  - the input order is kept.
- **N2 `TestTickPrunesAnUnusedRepo`**, with real git as the existing cleanup tests use,
  starting from `seedServedBinding`:
  - owner O has repo A with a live binding, and repo B with only an archived binding
    (`relevo.Unbind(…, true)` on it);
  - also create an unrelated file `repos/<O>/notes.txt` and a non-owner dir
    `repos/not-an-owner/x.git`;
  - after `s.Tick`: A exists, B is gone, and `notes.txt` and `not-an-owner/x.git` still
    exist.
- **N3 `TestTickPruneRemovesTheEmptyOwnerDir`:** an owner whose only repo is unused. After
  Tick, both the repo and `repos/<O>` are gone.
- **N4 `TestCreateAfterPruneRecreatesTheRepo`:** prune a repo (via Tick), then create a
  binding with the same RepoID through the create handler. The bare repo exists again and
  the binding records it.
- **N5 `TestWireUnbindReleasesRefs`:**
  - seed a served binding;
  - create `refs/heads/relevo/<name>` and `refs/relevo/<name>/out` plus `round-1` in its
    bare repo with `git update-ref`;
  - DELETE the binding through the handler;
  - the branch and refs are gone, and a sibling live binding's refs in the same repo are
    untouched.
- **N6 `TestAdminUnbindAndGCAbandonedReleaseRefs`:**
  - the same assertion through `AdminUnbind`, and through `GCAbandoned` with
    `dryRun=false`;
  - `GCAbandoned` with `dryRun=true` touches nothing.
- **N7 `TestUnbindKeepingADirtyWorktreeKeepsItsRefs`:**
  - make the binding's worktree dirty, so Unbind keeps it;
  - after the wire unbind, the branch and refs are still present. No DeleteBranch is
    attempted, or if one is, the refs still exist.
  - If a dirty served worktree cannot be produced in the test fixtures, say so and use the
    closest fixture.

### 7.3 Fenced tests

No existing test may be modified. `TestCollectSettledOnTick`, `TestSettled`,
`TestCreateBinding`, `TestOwnerDirIsFlatHex` and every other serve, relevo and e2e test must
pass unchanged. If an existing test relies on a bare repo surviving with no live binding,
halt and report it.

### 7.4 Mutation checks (run each, report pass/fail, restore)

| # | Break | Must fail |
|---|---|---|
| M1 | unusedRepos: return every repo (ignore live) | N1, N2 |
| M2 | pruneUnusedRepos: on a List error, treat the owner as having no live bindings (delete) | a test you add to N2: make List fail for one owner, e.g. with a runtime seam or an unreadable store if one exists. If no seam exists, report that M2 cannot be tested without new seams, and do not add one. |
| M3 | pruneUnusedRepos: delete any child of `repos/<owner>`, not just `*.git` dirs | N2 (`notes.txt`) |
| M4 | handleUnbind: skip releaseServedRefs | N5 |
| M5 | the unbind paths: release even when a worktree was kept | N7 |
| M6 | Tick: prune before collectSettled | none required. Report whether any test notices. Pruning first delays a repo's removal by one tick; it is not unsafe. |

If a mutation does not make its named test fail, report it. Do not strengthen tests
beyond this plan, except the M2 addition.

## 8. Scope check

- `git diff --stat` shows only the §2 files.
- No client-side (internal/relevo) code changes, apart from reading `UnbindResult`.
- Revoke is unchanged.
