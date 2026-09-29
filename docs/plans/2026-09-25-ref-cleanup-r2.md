# ref-cleanup round 2: gofmt and rebase

Date: 2026-09-25. Binding: ref-cleanup, branch relevo/ref-cleanup, PR #452.
Round 1 (docs/plans/2026-09-25-ref-cleanup.md) is otherwise accepted. This round changes
**no behaviour**.

**Stop rather than improvise.** If a step fails in a way this plan does not describe, for
example a rebase conflict in a file other than the ones named here, halt and report it.

## Findings from verification

1. `gofmt -l $(git ls-files '*.go')` prints `internal/relevo/refclean_test.go`. The round 1
   report said gofmt was clean. It was not. CI's `make check` runs gofmt first, and the
   ubuntu jobs on PR #452 failed at about 16 s.
2. origin/main moved on by two commits: e762242e (#450) and f0b4c4bc (#451). They drop the
   feat-count drift rule from `scripts/check-plugin-version.sh`, which is the
   "11 feat commits since v0.13.0" failure round 1 hit. Rebasing onto origin/main removes
   that failure. Do not touch the script yourself.

## Working efficiently

- One step per command where possible.
- Do not re-read the round 1 code: nothing in it changes except formatting.
- Focused check: `gofmt -l $(git ls-files '*.go')` must print nothing.
- Full check once at the end:
  - `go vet ./...`
  - `go test -race -count=1 ./...`
  - `scripts/check-plugin-version.sh`
  - the go mod tidy diff check (the same four steps as `make check`, if the hook blocks
    `make check`).

## Steps

1. **Fix the formatting.**
   - `git fetch origin`
   - `git rebase origin/main`. A conflict is not expected: #450 and #451 touch only the
     script, its test and CI. If one occurs, halt.
   - Run `gofmt -w internal/relevo/refclean_test.go`.
   - Then `gofmt -l $(git ls-files '*.go')` must print nothing.
2. **Full check.**
   - Run the four checks in "Working efficiently". Every one must pass.
   - Report the exact output of `gofmt -l` (empty) and `scripts/check-plugin-version.sh`.
3. **Save this plan.**
   - Save it verbatim at `docs/plans/2026-09-25-ref-cleanup-r2.md` in the worktree.
4. **Commit and push.**
   - Amend the round 1 commit so the PR stays one commit: `git commit --amend --no-edit`
     with the formatted file and this plan added.
   - Then `git push --force-with-lease origin relevo/ref-cleanup`.
   - Report `git log --oneline origin/main..HEAD` (exactly one commit) and
     `git diff --stat origin/main...HEAD`. It should be round 1's 13 files plus this plan.

## Fenced

- No change to any `.go` file except `gofmt` on `internal/relevo/refclean_test.go`.
- No test edits beyond that formatting. No README or other doc edits.
