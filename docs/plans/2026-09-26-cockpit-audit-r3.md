# Cockpit `:audit`, round 3: fit the cleanup's new checks (2026-09-26)

## 1. System overview

The branch is rebased onto `origin/main` `f06f2b4`, which includes #519: lint and comment tooling with a per-package
ratchet. `make check` now also runs `scripts/check-comments.sh` and `scripts/check-filesize.sh`, and this branch fails
both. This round fixes only that. **No behaviour changes.** Every test and golden must stay byte-identical.

Current failures:

| check | file | problem |
|---|---|---|
| check-comments | `internal/ui/view_audit.go` | 30 lines cite history (`#NNN` or `§`) |
| check-comments | `internal/ui/view_audit_test.go` | 10 lines |
| check-comments | `internal/relevo/configaudit.go` | 9 lines |
| check-comments | `internal/relevo/configaudit_test.go` | 1 line |
| check-filesize | `internal/ui/view_audit.go` | 706 lines, max 600 |
| check-filesize | `internal/ui/actions.go` | 632 lines, max 600 (573 on main; the audit round added 59) |

`golangci-lint run` on these packages already reports 0 issues. Keep it at 0.

## 2. Changes (closed list)

1. **Comments.** In the four files above, rewrite every flagged comment so it says *why* in plain words.
   - Remove `§x.y`, `#NNN`, "round N" and plan references.
   - If a comment only pointed at a section and says nothing else, delete it.
   - **Do not add any file to `scripts/check-comments.allow`.**
2. **`view_audit.go` ≤ 600 lines.** Move `auditRevView` (its type, constructor and methods) and the ChangeLine
   rendering helpers (the §5.2 renderer and anything only they use) into a new file `internal/ui/view_audit_rev.go`.
   - Moved code keeps its identifiers, bodies and order.
   - Both files must be ≤ 600 lines.
3. **`actions.go` ≤ 600 lines.** Move the four audit methods of `plannerActions` (`ConfigLog`, `ConfigChanges`,
   `RollbackPreview`, `Rollback`) into a new file `internal/ui/actions_audit.go`, with their doc comments.
   - The `Actions` interface stays in `actions.go`.
   - **Do not add anything to `scripts/check-filesize.allow`.**

A mechanical move is fine to do by hand, one edit per file. Do not reformat unrelated code.

## 3. Steps

### 0. Working efficiently

**How to work:**
- Run `sh scripts/check-comments.sh` and `sh scripts/check-filesize.sh` once to get the exact lines.
- Then, in one batch, read the four files plus `internal/ui/actions.go`.
- Make each file's changes in one edit.

**Commands:**
- Checks: `sh scripts/check-comments.sh && sh scripts/check-filesize.sh && sh scripts/check-comments_test.sh && sh scripts/check-filesize_test.sh`
- Lint: `golangci-lint run ./internal/ui/... ./internal/relevo/... ./internal/config/...`
- Tests: `mkdir -p $HOME/.cache/relevo-verify/tmp && env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/config/ ./internal/relevo/ ./internal/ui/ -count=1`
- `test -z "$(gofmt -l $(git ls-files '*.go'))"`, `go vet ./internal/...`, `sh scripts/check-name.sh`
- `make check` is refused here, so do not run it.

### 1. Comments (§2.1)

### 2. Split `view_audit.go` (§2.2)

### 3. Split `actions.go` (§2.3)

### 4. Verify

1. Every command in step 0 passes.
2. `git diff --stat HEAD` shows only the four comment files, `actions.go`, `view_audit.go` and the two new files.
3. No `testdata/*.golden` changed.

### 5. The plan and the commit

1. Copy this plan to `docs/plans/2026-09-26-cockpit-audit-r3.md`.
2. `git add -A && git commit --amend --no-edit`.

## 4. Deletions

- Comment text only: the history citations in §1's files.
- No code is deleted, and no test changes.

## 5. Stop rather than improvise

Halt and report if any of these happens:
- a check fails for a file this plan does not name;
- splitting needs a behaviour or signature change;
- `golangci-lint` reports new issues after the move.
