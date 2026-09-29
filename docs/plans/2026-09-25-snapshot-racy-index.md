# SnapshotTree: keep the index's mtime on the temp copy (#461)

## 1. System Overview

`git.Client.SnapshotTree` (`internal/git/client.go:124`) snapshots a working
tree by copying `.git/index` to a temp file, then running `git add -A` and
`git write-tree` with `GIT_INDEX_FILE` pointing at the copy. `io.Copy` gives
the copy a fresh mtime. Git re-reads an index entry's content only when the
entry's mtime is not older than the index file's own mtime (racy-clean
detection). With a fresh mtime on the copy, a same-size edit written in the
same timestamp tick as the last real index write looks clean, and the snapshot
keeps the OLD blob.

This is the cause of the macOS flake in `TestScratchRealGit`
(`scratch a.txt = "one\n", want the dirty edit "two\n"`). It can also drop an
edit from the round-diff capture (`internal/relevo/capture.go:127,167`).

The fix: after copying, set the temp index's atime and mtime to the repo
index's, so git's racy check sees what it would see on the real index. The
planner reproduced this with the git CLI:

| index mtime == file mtime, same-size edit | result |
|---|---|
| plain copy + `add -A` + `write-tree` | old content (bug) |
| copy with the mtime preserved | new content (correct) |

## 2. File Structure

```
internal/git/client.go        MODIFIED  SnapshotTree: preserve the repo index's times on the temp copy
internal/git/client_test.go   MODIFIED  new TestSnapshotTreeRacyCleanEntry (deterministic regression test)
docs/plans/2026-09-25-snapshot-racy-index.md   NEW  this plan, committed with the change
```

Nothing else changes. Specifically, not `TestScratchRealGit` or any caller of
`SnapshotTree`.

## 3. Data Structures & Type Definitions

None.

## 4. Interface Definitions & Component Contracts

`SnapshotTree(ctx, dir) (string, error)`: the signature is unchanged. The
contract gains one postcondition. Add this sentence to its doc comment, in the
Postconditions paragraph:

> The temp index carries the repository index's mtime, so git's racy-clean
> check treats every entry exactly as it would on the real index: a same-size
> edit made in the index's own timestamp tick is still captured (#461).

The **error contract** for the new step: if `os.Stat` of the repo index or the
`os.Chtimes` on the temp index fails, return a wrapped error:
`fmt.Errorf("preserve index mtime: %w", err)`. That follows the existing
`"copy index: %w"` style. Do not silently continue, because a snapshot that
may miss edits is worse than a failed one. When `.git/index` does not exist
(the `os.IsNotExist` branch, unborn repo), there is nothing to preserve, and
behaviour is unchanged.

## 5. High-Level Pseudocode

**`internal/git/client.go`, `SnapshotTree`,** inside the `if err == nil {`
branch that starts at line 156 (`src, err := os.Open(repoIndex)`). After the
existing `copyErr` / `closeErr` checks, still inside that branch:

```
info = stat(src)            -- use src.Stat() before src.Close(), or os.Stat(repoIndex)
on error -> return "", wrap("preserve index mtime", err)
chtimes(tempIndex, atime = info.ModTime(), mtime = info.ModTime())
on error -> return "", wrap("preserve index mtime", err)
```

Using ModTime for both atime and mtime is fine: git only reads mtime. Add a
short comment above it naming the reason (racy-clean detection, #461).

**`internal/git/client_test.go`, new `TestSnapshotTreeRacyCleanEntry`.** Place
it right after `TestSnapshotTreeDirtyTreeCapturesChanges` (which ends near
line 2724). Use the existing helpers `requireGit`, `initRepo`,
`writeGitFile` and `runGit`, and `NewClient("git", 5*time.Second,
DefaultMaxPatchBytes)` as the neighbouring tests do.

```
T = a fixed past time, e.g. time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
repo = initRepo(t)
writeGitFile(repo, "a.txt", "one\n");  os.Chtimes(repo/a.txt, T, T)
runGit add a.txt; runGit commit -m first        -- the index entry now records mtime T, size 4
writeGitFile(repo, "a.txt", "two\n");  os.Chtimes(repo/a.txt, T, T)   -- same size, same mtime
os.Chtimes(repo/.git/index, T, T)               -- the index was "written in the same tick"
-- DO NOT run any git command via runGit between here and SnapshotTree:
-- runGit does not set GIT_OPTIONAL_LOCKS=0, so e.g. `git status` would rewrite
-- the index and smudge the racy entry, hiding the bug.
tree = client.SnapshotTree(ctx, repo)  -- must not error
blob = runGit cat-file -p tree + ":a.txt"
assert blob == "two\n", with a message naming #461 and racy-clean
```

Put a comment on the test explaining the setup: the entry's mtime equals the
index's, so git must re-read the content; a temp index with a fresh mtime
defeats that.

## 6. Error Handling Strategy

Covered in section 4. There are no new error types. The new failure is
wrapped like the existing index-copy failures.

## 7. Working Efficiently

Each model step costs a round trip, so:
- Read `internal/git/client.go` lines 108-190 and
  `internal/git/client_test.go` lines 1585-1610, 2183-2195 and 2689-2725 in one
  step, as parallel reads.
- Make the `client.go` change in one edit, and add the test in one edit.
- Focused check: `go test ./internal/git/ -run 'TestSnapshotTree' -count=1`.
- Full check, once, at the end: `make check`. If this machine blocks heavy
  commands, use `dev run make check`. If `dev run` fails because `dist/` or
  `.git` is missing on the mirror, say so in the report and rely on the PR's
  CI.
- CI runners have git but no harness binary or network. This test only uses
  git, which the neighbouring `SnapshotTree` tests already rely on.

If any step is impossible as written or contradicts the code, stop and report.
Do not bend the plan to fit.

## 8. Ordered Implementation Steps

**Step 0: sync.** `git fetch origin && git merge --ff-only origin/main`. Then
confirm that `internal/git/client.go` still has `src, err := os.Open(repoIndex)`
followed by an `io.Copy` into `tempIndex` inside `SnapshotTree`. If not, halt.

**Step 1: write the test first and watch it fail.** Add
`TestSnapshotTreeRacyCleanEntry` as in section 5, and run the focused check.
Verification: it FAILS with the snapshot containing `one\n`. Record the failure
line for the report. If it passes before the fix, halt and report. That would
mean the test does not reproduce the bug, and the plan's premise needs
rechecking.

**Step 2: fix `SnapshotTree`.** Make the section 5 change and the section 4
doc-comment addition. Verification: the focused check passes, and the new
test passes 20 times in a row:
`go test ./internal/git/ -run TestSnapshotTreeRacyCleanEntry -count=20`.

**Step 3: confirm the original flake's test.** Run
`go test ./internal/relevo/ -run TestScratchRealGit -count=20`. It must pass.

**Step 4: full check.** `make check` (see section 7). It must pass.

**Step 5: ship.** Copy this plan to
`docs/plans/2026-09-25-snapshot-racy-index.md` if it isn't there already.
Commit everything as one commit,
`fix(git): keep the index's mtime on SnapshotTree's temp copy so racy-clean edits are captured (#461)`,
with `Fixes #461` in the body. Push, and open a PR against `main` whose body
includes `Fixes #461`. Don't wait for CI and don't merge.

The report states the step 1 failure output, the step 2 and step 3 pass
counts, the PR number, and `git diff --stat`.
