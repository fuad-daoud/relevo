# relevo cleans up its branches and refs once they are pushed

Date: 2026-09-25. Base: origin/main 58f8e603 (#446).
Follows the file-writes spike (docs/specs/2026-09-24-file-writes-spike.md, §4.2 D10).
There is one round in this plan. Loose git objects are **out of scope**: git's own
automatic GC handles them.

**Stop rather than improvise.** If a step is impossible as written, if the code contradicts
a fact stated here, or if an existing test fails, halt and report what you found. Do not
bend a test to make it pass.

## 1. System overview

relevo leaves two kinds of git refs behind in the user's repository:

- **The binding's branch `relevo/<name>`.** `bind --worktree` and fork cut it, and a
  `--server` binding absorbs it.
- **The client refs `refs/relevo/<name>/*`** of a remote binding:
  - `out` is a send-side transport handle;
  - `round-N` points at the server's commit of uncommitted work at round close.

Today relevo deletes a branch in zero places (README ~1041), and it never deletes client
refs. In this repo that has left 37 `relevo/*` branches and 15 `refs/relevo/*` refs.

This round adds one rule and applies it in two places.

**The rule: delete only when nothing unique is lost.** A candidate ref is deleted only if
its commit is contained in some remote-tracking ref (`refs/remotes/*`), which means it was
pushed. Every other candidate is kept, with a reason that is printed.

- A branch is deleted with `git branch -D`. That refuses a branch checked out in any
  worktree, which is a second safety net. It is never deleted with `update-ref -d`.
- A branch relevo did not create (`ExistingBranch`, adopted with `bind --branch`) is never
  a candidate.

**Where it applies:**

1. **`relevo unbind --done`** is `relevo.GC`. After each DONE binding's worktree teardown,
   GC cleans that binding's candidates. If the teardown **kept** the worktree, no ref of
   that binding is touched. `relevo.Unbind` (plain unbind, `--pick`, the UI, the server's
   own cleanup) is **unchanged**.
2. **`relevo unbind --sweep [--dry-run]`** is a new, hand-run command for refs that already
   exist. In the repository at the current directory, it applies the same rule to every
   `refs/heads/relevo/<name>` and `refs/relevo/<name>/*` whose `<name>` has no live
   binding.

## 2. File structure

```
internal/git/client.go          + RefOnRemote
internal/git/client_test.go     + TestRefOnRemote (real git)
internal/relevo/runtime.go      Git interface + RefOnRemote
internal/relevo/fake_test.go    fakeGit + RefOnRemote (fields refOnRemote, refOnRemoteErr, refOnRemoteCalls)
internal/relevo/refclean.go     NEW: RefOutcome, repoDirOf, bindingRefCandidates, cleanRefs, SweepRefs, RefLines
internal/relevo/refclean_test.go NEW: tests in §7.2
internal/relevo/gc.go           GCResult.Refs; GC calls the ref cleanup after teardown
internal/relevo/bind.go         doc comments only: worktreeTeardown line ~661, the file comment ~22
cmd/relevo/main.go              cmdUnbind: --sweep flag, validation, runSweep; runGC prints RefLines
cmd/relevo/main_test.go         + TestUnbindSweepTakesNoBinding (flag validation only)
README.md                       unbind usage (~407), teardown rule (~943), "zero places" (~1041-1043), --server note (~370-372)
docs/plans/2026-09-25-ref-cleanup.md        this plan, verbatim (step 9)
docs/plans/2026-09-24-diff-findings-to-db.md the previous round's plan (step 9)
```

## 3. Data structures

**`RefOutcome`** (refclean.go). This is what happened to one ref.

- `Ref string json:"ref"`: the full ref name, e.g. `refs/heads/relevo/api` or
  `refs/relevo/api/round-3`.
- `Deleted bool json:"deleted,omitempty"`
- `WouldDelete bool json:"would_delete,omitempty"`: set on a dry run only.
- `Reason string json:"reason,omitempty"`: why it was kept.

Exactly one of `Deleted`, `WouldDelete` or `Reason != ""` holds. The reasons are exact
strings:

- `"not on any remote-tracking ref"`
- `"check failed: " + brief(err)`
- `"delete failed: " + brief(err)`
- `"live binding"`: sweep only.

**`GCResult`** (gc.go:21-32) gains `Refs []RefOutcome json:"refs,omitempty"`. No other field
changes.

**`SweepResult`** (refclean.go):

- `Dir string`: the repository directory swept.
- `Refs []RefOutcome`: in the order the refs were listed, heads first, then `refs/relevo/`.

No store, database or golden-file changes. If `store/testdata/binding-shape.golden` changes,
halt.

## 4. Interfaces and contracts

### 4.1 `(*git.Client).RefOnRemote` (internal/git/client.go, next to ListRefs ~788)

`func (c *Client) RefOnRemote(ctx context.Context, dir, ref string) (bool, error)`

- It runs `git for-each-ref --count=1 --format=%(refname) --contains <ref> refs/remotes/`
  in dir.
- Result: true exactly when the output has a non-empty line.
- An unresolvable ref is an error (git reports a malformed object name). Return it wrapped,
  the way `ListRefs` returns its errors.
- Doc comment: "RefOnRemote reports whether ref's commit is contained in some remote-tracking
  ref, i.e. whether the work it points at was pushed. It reads local refs only: no fetch, no
  network. A stale remote-tracking ref still counts." List the errors as `ListRefs` does.
- Add it to the `Git` interface in `internal/relevo/runtime.go` next to `ListRefs`, with a
  one-line comment: "the ref cleanup's safety check (nothing unpushed is deleted)".

### 4.2 fakeGit (internal/relevo/fake_test.go)

- New fields: `refOnRemote map[string]bool`, `refOnRemoteErr error`,
  `refOnRemoteCalls []refOnRemoteCall` (a `{Dir, Ref string}` struct).
- The method records the call, then:
  - returns `refOnRemoteErr` if it is set;
  - otherwise returns `refOnRemote[ref]`. A nil map, or a missing key, means **false**.
- This follows the existing `deleteRefCall` / `deleteRefErr` pattern (fake_test.go ~42, ~426).

### 4.3 internal/relevo/refclean.go

**`repoDirOf(b store.Binding) string`**

- Returns `b.Repo` if it is non-empty.
- Else returns `b.RepoRef.CommonDir` when `b.RepoRef != nil` and it is non-empty.
- Else returns "".
- Never use `b.CWD`: for a worktree binding it is the worktree, which teardown may just
  have removed (bind.go:670-671).

**`bindingRefCandidates(ctx, rt Runtime, dir string, b store.Binding) ([]string, error)`**

The candidates for one binding, deduplicated and in this order:

1. `"refs/heads/relevo/" + b.Name`, when either:
   - `!b.ExistingBranch && b.Branch == "relevo/"+b.Name` (a branch relevo cut), or
   - `b.Builder.Remote() && b.Branch != "relevo/"+b.Name` (the server ref an adopted remote
     binding absorbs beside the user's branch; README ~370-372).
2. Every ref from `rt.Git.ListRefs(ctx, dir, "refs/relevo/"+b.Name+"/")`. The trailing
   slash stops `api` from matching `api2`.

`b.Branch` itself is never a candidate unless it equals `relevo/<name>` and
`!ExistingBranch`. A `ListRefs` error is returned as is.

**`cleanRefs(ctx, rt Runtime, dir string, refs []string, dryRun bool) []RefOutcome`**

For each ref, in order:

1. `on, err := rt.Git.RefOnRemote(ctx, dir, ref)`
   - On error: keep, with reason `"check failed: "+brief(err)`.
   - If `!on`: keep, with reason `"not on any remote-tracking ref"`.
2. If `dryRun`: `WouldDelete`.
3. Otherwise:
   - a ref under `refs/heads/` uses `rt.Git.DeleteBranch(ctx, dir, ref)` (it trims
     `refs/heads/`, client.go ~467);
   - every other ref uses `rt.Git.DeleteRef(ctx, dir, ref)`.
   - On error: keep, with reason `"delete failed: "+brief(err)`. Otherwise `Deleted`.

cleanRefs never returns an error: a ref that cannot be cleaned is kept and reported.

**`SweepRefs(ctx context.Context, rt Runtime, dir string, dryRun bool) (SweepResult, error)`**

- Returns an error when `rt.Git == nil`. Use the existing `ErrGitUnavailable` if the
  relevo package exposes one; otherwise
  `errors.New("git is unavailable; the sweep needs it")`.
- Runs entirely inside `rt.Store.WithLock`, so a bind cannot create `relevo/<name>` in the
  middle of the sweep. GC holds the lock for the same reason (gc.go:40-45).
- Steps:
  1. `live`: the set of names from `tx.List()`. That covers every live record, **any state
     including DONE**, and every planner, because the local store's owner is "".
  2. `heads := ListRefs(dir, "refs/heads/relevo/")`, then
     `others := ListRefs(dir, "refs/relevo/")`. Any error is returned.
  3. Derive `<name>`:
     - for `refs/heads/relevo/<rest>`, name = `<rest>`;
     - for `refs/relevo/<seg>/<rest>`, name = `<seg>`.
     - Skip (omit from the result entirely) any ref whose name fails `store.ValidName`, or
       whose `<rest>` is empty.
  4. A ref whose name is in `live` gets the outcome `{Ref, Reason: "live binding"}`.
  5. All remaining refs go through `cleanRefs(ctx, rt, dir, remaining, dryRun)`.
  6. The result keeps list order (heads first).

**`RefLines(refs []RefOutcome) []string`** (pure, for the CLI)

One line per outcome:

- `"deleted      <ref>"`
- `"would delete <ref>"`
- `"kept         <ref> (<reason>)"`

### 4.4 GC (internal/relevo/gc.go:43-92)

Insert after the teardown fields are copied (after `res.WorktreeGone = outcome.Gone`) and
**before** the `if opts.DryRun` block:

```
if rt.Git != nil && outcome.Kept == "" {
    if dir := repoDirOf(b); dir != "" {
        refs, err := bindingRefCandidates(ctx, rt, dir, b)
        if err != nil { res.Refs = []RefOutcome{{Ref: "refs/relevo/"+b.Name+"/", Reason: "check failed: "+brief(err)}} }
        else          { res.Refs = cleanRefs(ctx, rt, dir, refs, opts.DryRun) }
    }
}
```

- This is a contract written as pseudocode. Implement it in that shape.
- GC's error behaviour is unchanged: ref cleanup never fails GC.
- Update GC's doc comment. Add a paragraph saying GC also deletes the binding's branch and
  client refs once they are on a remote-tracking ref, never an adopted branch, and never
  while the worktree is kept.

### 4.5 CLI (cmd/relevo/main.go, cmdUnbind ~1644-1692, runGC ~1697-1756; re-grep, lines move)

**The new flag:**
`sweep := fs.Bool("sweep", false, "delete relevo/<name> branches and refs/relevo/<name>/* refs of bindings that no longer exist, once they are on a remote-tracking ref")`.

Change the `--dry-run` help to `"with --done or --sweep: list what would be cleared, change nothing"`.

**Validation, before `--done` handling:** if `*sweep` is set together with any of `*done`,
`*delete`, `*archive`, `*pickFlag`, `*name != ""` or `len(fs.Args()) > 0`:

- print to stderr: `relevo: --sweep takes no binding and no other flag except --dry-run`
- `return exitCodeErr{code: 2}`

**`runSweep(dryRun bool) error`:**

1. `newRuntime()`, then `dir, err := os.Getwd()`, then
   `relevo.SweepRefs(ctx, rt, dir, dryRun)`.
2. If there are no refs, print `no relevo refs in <dir>`.
3. Otherwise print every `RefLines` line.
4. Then print a summary line:
   `N deleted, M kept` (or `N would be deleted, M kept` on a dry run).

**`runGC`:** after the existing worktree lines of each binding, in the dry-run, archived
and deleted branches alike, print each `RefLines(r.Refs)` line indented by 12 spaces,
matching the existing continuation lines.

**CLI test rule:** a cmd/relevo test must not run a subcommand that spawns a harness or
reaches the network. The rule itself is tested as pure and fake-git functions in
internal/relevo. The only CLI test is flag validation, which exits before `newRuntime`.

### 4.6 Doc comments and README

- **bind.go ~661** (worktreeTeardown): "The branch is never removed: …" becomes
  "worktreeTeardown never removes a branch; GC removes a relevo-created branch once it is
  on a remote-tracking ref (refclean.go)."
- **bind.go ~22**: adjust the same claim if it says branches are never deleted.
- **README ~407** (unbind usage):
  - add `relevo unbind --sweep [--dry-run]`;
  - say that `--done` now also deletes each cleared binding's `relevo/<name>` branch and
    `refs/relevo/<name>/*` refs once each is on a remote-tracking ref.
- **README ~943** (teardown rule): "and never removes the branch" becomes "and removes the
  branch only through `unbind --done`, once it is pushed".
- **README ~1041-1043** ("relevo deletes a branch in **zero** places"). Rewrite it as:
  - relevo deletes a branch in two places:
    - `unbind --done` and `unbind --sweep`, for a `relevo/<name>` branch relevo created,
      only when its commit is on a remote-tracking ref;
    - the existing `bind --server` rollback exception, which stays.
  - a branch adopted with `--branch` is never deleted;
  - `unbind`, `done` and a kept (dirty) worktree never delete anything.
- **README ~370-372:** "relevo deletes neither" becomes "relevo never deletes the adopted
  branch; the server's `relevo/<name>` ref is deleted by `unbind --done` once it is on a
  remote-tracking ref".

Do not edit docs/specs, CLAUDE.md or `internal/harness/agents/*.md`.

## 5. Pseudocode

```
GC (unbind --done), under WithLock, for each DONE binding b:
  outcome = worktreeTeardown(b, dryRun)                       # unchanged
  if git available and outcome.Kept == "" and repoDirOf(b) != "":
     candidates = [refs/heads/relevo/<name> if relevo-owned] + ListRefs(refs/relevo/<name>/)
     res.Refs = cleanRefs(candidates, dryRun)
       each ref: RefOnRemote? no -> keep; dry -> would delete; branch -> branch -D; other -> update-ref -d
  archive or delete the record                                # unchanged

unbind --sweep [--dry-run], in cwd:
  under WithLock:
    live = names of every live record (any state)
    refs = ListRefs(refs/heads/relevo/) + ListRefs(refs/relevo/)
    for each ref with a valid binding name:
       name live -> kept "live binding"
       else      -> cleanRefs rule
  print one line per ref and a summary
```

## 6. Error handling

- Nothing in the ref cleanup fails GC. Every git error becomes a kept outcome with a reason.
- `SweepRefs` fails only if git is unavailable, the store lock fails, or listing refs
  fails. In those cases the command exits non-zero with that error.
- A branch checked out anywhere (a live worktree, or the main checkout after
  `gh pr checkout`) makes `branch -D` fail. The outcome is kept, with reason
  `delete failed: …`. That is intended.

## 7. Ordered implementation steps

### 7.0 Working efficiently

- Read these in one parallel batch, and do not re-search what this plan locates:
  - gc.go (whole)
  - bind.go:640-700 and the file's top 40 lines
  - runtime.go:30-110
  - fake_test.go:25-110, 320-440, 570-580
  - client.go:455-480 and 755-810
  - client_test.go:1-40, 1480-1500, 1590-1605 and 2545-2600
  - main.go (grep `func cmdUnbind` and `func runGC`)
  - main_test.go (grep `TestUnbindDoneTakesNoBinding`)
  - README.md 366-374, 400-410, 938-946, 1030-1046
- Make every change to a file in one edit call.
- Iterate with these focused commands, fixing every error before the next run:
  - `go test ./internal/git/ -run 'RefOnRemote|DeleteBranch|ClientRefs' -count=1`
  - `go test ./internal/relevo/ -run 'GC|Unbind|Ref|Sweep|ExistingBranch' -count=1`
  - `go test ./cmd/relevo/ -run Unbind -count=1`
- Run `make check` once at the end. The local laptop may block `make check` behind a hook.
  If it does, run its steps directly and say so in the report:
  - `gofmt -l $(git ls-files '*.go')` (must print nothing)
  - `go vet ./...`
  - `go test -race -count=1 ./...`
  - the `go mod tidy` diff check

### 7.1 Steps

1. **RefOnRemote.**
   - Change: §4.1 (client, interface) and §4.2 (fakeGit).
   - Add N1.
   - Done when N1 passes and `go build ./...` is clean.
2. **refclean.go.**
   - Change: §4.3.
   - Add N2-N6.
   - Done when they pass.
3. **GC.**
   - Change: §4.4.
   - Add N7-N10.
   - Done when N7-N10 pass and every existing GC, Unbind and ExistingBranch test passes
     **unchanged**.
4. **CLI.**
   - Change: §4.5.
   - Add N11.
   - Done when the cmd/relevo Unbind tests pass.
5. **Docs.**
   - Change: §4.6.
6. **Full check:** see §7.0.
7. **Mutations:** §7.4, one at a time, restoring after each.
8. **Manual smoke (read-only):** run the built binary as
   `go run ./cmd/relevo unbind --sweep --dry-run` **from your worktree**. Paste its summary
   line and at most 10 of its lines into the report.
   - Do **not** run it without `--dry-run`.
   - Do not run `unbind --done`.
9. **Plans in the PR.**
   - Save this plan verbatim at `docs/plans/2026-09-25-ref-cleanup.md` in your worktree.
   - Copy `/home/fuad/projects/relevo/docs/plans/2026-09-24-diff-findings-to-db.md` (the
     previous round's plan, which missed its PR) to the same path in your worktree,
     unchanged.
10. **Commit and PR.**
    - One commit: `feat(unbind): delete relevo's branches and refs once they are pushed;
      unbind --sweep for the ones already there`.
    - Push the branch and open a PR against main. Put the §7.4 results in the PR body.

### 7.2 New tests

- **N1 `TestRefOnRemote`** (internal/git/client_test.go, real git; reuse `initRepo` and
  `runGit`):
  - Setup: a bare repo made with `git init --bare` in a `t.TempDir()`, added as `origin`.
    Commit on the default branch, push it, then fetch so `refs/remotes/origin/<branch>`
    exists.
  - Cases:
    - (a) a local branch at the pushed commit gives true;
    - (b) a branch with one extra local commit gives false;
    - (c) a branch at an ancestor of the pushed commit gives true;
    - (d) a ref under `refs/relevo/x/round-1` created with `update-ref` at the pushed
      commit gives true;
    - (e) a nonexistent ref gives an error.
- **N2 `TestRepoDirOf`** (table):
  - Repo set gives Repo;
  - Repo empty with `RepoRef.CommonDir` set gives CommonDir;
  - both empty gives "";
  - never CWD (set CWD in every case and assert it is not returned).
- **N3 `TestBindingRefCandidates`** (table with fakeGit `listRefsResult`):
  - a relevo-cut local branch gives `refs/heads/relevo/<n>` plus the listed refs;
  - `ExistingBranch` local gives the listed refs only;
  - adopted remote (`Branch` "feature/x", `ExistingBranch`, `Remote()`) gives
    `refs/heads/relevo/<n>` plus the listed refs;
  - duplicates are removed.
- **N4 `TestCleanRefs`** (fakeGit):
  - on remote: a branch uses `deleteBranchCalls` and a non-branch uses `deleteRefCalls`,
    and both outcomes are `Deleted`;
  - not on remote: kept with the exact reason, and no delete call;
  - `refOnRemoteErr`: kept with `check failed: …`;
  - `deleteBranchErr`: kept with `delete failed: …`;
  - `dryRun`: `WouldDelete` and no delete calls.
- **N5 `TestSweepRefs`** (fakeGit plus a real store):
  - Save a live binding `api`, including one in `StateDone`.
  - Set `listRefsResult` per prefix. If needed, extend the fake with
    `listRefsByPrefix map[string][]string`. That is allowed; say so.
  - Refs:
    - `refs/heads/relevo/api` gives "live binding";
    - `refs/heads/relevo/old` (on remote) is deleted;
    - `refs/relevo/old/round-2` (not on remote) is kept;
    - `refs/relevo/Bad.Name/out` and `refs/heads/relevo/` are omitted.
  - Heads come first in the result.
- **N6 `TestRefLines`**: the three line forms, exactly.
- **N7 `TestGCDeletesPushedRelevoBranch`**:
  - a DONE binding with `Repo` "/repo", `Branch` "relevo/web", a clean worktree, and
    `refOnRemote` true for `refs/heads/relevo/web`.
  - After GC: `deleteBranchCalls` holds exactly `{Dir: "/repo", Branch: "refs/heads/relevo/web"}`
    (or the trimmed form the client receives; match what cleanRefs passes), and
    `res.Refs[0].Deleted`.
- **N8 `TestGCKeepsUnpushedBranch`**: the same binding with `refOnRemote` false. No delete
  call, and the reason is `not on any remote-tracking ref`.
- **N9 `TestGCKeptWorktreeTouchesNoRef`**: a dirty worktree, so teardown keeps it. With
  `refOnRemote` true there are no `RefOnRemote`, `DeleteBranch` or `DeleteRef` calls, and
  `Refs` is empty.
- **N10 `TestGCDryRunDeletesNoRef`**: `refOnRemote` true and `DryRun`. The outcome is
  `WouldDelete`, with zero delete calls.
- **N11 `TestUnbindSweepTakesNoBinding`** (cmd/relevo/main_test.go, next to
  `TestUnbindDoneTakesNoBinding`):
  - `--sweep` combined with `--done`, `--delete`, `--archive`, `--pick`, a name, or
    `--name x` each exits 2.
  - Validation runs before `newRuntime`, so no git, harness or network is involved.

### 7.3 Existing tests: fenced

**No existing test may be modified.** The fence covers every existing test in the repo,
including:

- `TestUnbindExistingBranchNeverDeletes` (add_test.go:582). It must pass unchanged: an
  adopted branch is never a candidate.
- every test in gc_test.go;
- bind_test.go `TestUnbindTeardown*`;
- remote_test.go `TestGCRemoteOnlyWhenDone` (2173), which runs with `rt.Git == nil`. The
  cleanup must be skipped.

The only existing test file you may edit is fake_test.go: fakeGit's new method and fields.
If any other existing test fails, halt and report it.

### 7.4 Mutation checks (run each, report pass/fail, restore)

| # | Break | Must fail |
|---|---|---|
| M1 | cleanRefs: skip the RefOnRemote check (treat every ref as on remote) | N4 "not on remote" case, N8 |
| M2 | bindingRefCandidates: drop the `!b.ExistingBranch` condition | `TestUnbindExistingBranchNeverDeletes` or N3 |
| M3 | SweepRefs: drop the live-name check | N5 |
| M4 | cleanRefs: ignore dryRun | N4 dry-run case, N10 |
| M5 | GC: drop the `outcome.Kept == ""` guard | N9 |
| M6 | cleanRefs: delete branches with DeleteRef instead of DeleteBranch | N4 (branch uses deleteBranchCalls) |
| M7 | client.go RefOnRemote: drop `--contains <ref>` | N1 (b) |

If a mutation does **not** make a named test fail, report it. Do not strengthen tests
beyond this plan.

## 8. Scope check for the reviewer

- `git diff --stat` should show only the §2 files.
- No golden or migration changes.
- New exported names: `RefOnRemote` (client and interface), `RefOutcome`, `SweepResult`,
  `SweepRefs`, `RefLines`, and `GCResult.Refs`.
