# Unbind kept message, plan 1 of 3 (#1289)

HARD RULES: do not push, do not run gh, do not touch any remote or PR; relevo owns the branch. Do only this plan, and end it in its own commit.

Goal: the refusal for a kept worktree carries the reason (and the path), no hint of any verb. Callers that print it add their own hint; the server's 409 stops embedding both force forms.

Behaviour:
- `keptRefusal` (`internal/relevo/bind.go:920-926`) returns `binding <name> kept: worktree <path> kept (<reason>)` plus the existing `; its builder process (pid N) was stopped` clause when a process was stopped, and nothing after it. The `; commit or discard the work, ...` tail is removed.
- `handleUnbind` (`internal/serve/bindings.go:497-500`) sends `err.Error()` as the 409 message, with no `; on the server: relevo serve unbind ... --force` suffix. The `fmt` import stays only if still used.
- Named `relevo unbind X` is unchanged in structure: `cmd/relevo/unbind.go:116-118` already adds `next: relevo unbind X --force`, so the output is the reason plus `next:` with one hint.
- `relevo serve unbind` (`cmd/relevo/serve_admin_write.go:276`) is unchanged: it prints the reason plus `next: relevo serve unbind --owner ... --force`, now with no client form in the message.

Steps:
1. Edit `keptRefusal` in `internal/relevo/bind.go` (do not add lines there beyond a net decrease; the file is over the size limit).
2. Edit `handleUnbind` in `internal/serve/bindings.go`.
3. Update `internal/relevo/unbind_keep_test.go:56`: the assertion that the message contains `relevo unbind kept --force` becomes an assertion that it contains the worktree path and the reason and does NOT contain "--force". Rename nothing unless the name stops saying what it pins.
4. `internal/serve/unbind_keeps_test.go`: in `TestUnbindDirtyWorktreeAnswers409` add `TestUnbindKeptAnswerCarriesTheReasonOnly` (a new test beside it, same seeding): the 409 `Message` contains "uncommitted changes" and contains neither "--force" nor "serve unbind".
5. Add `TestServeUnbindKeptPrintsOnlyTheServerForm` in `cmd/relevo` only if an existing test already drives `serve unbind` without a harness or network (check `cmd/relevo/serve_*_test.go` for `AdminUnbind` use). If none does, skip it and say so; `internal/serve/admin_test.go:397` already pins `ErrWorktreeKept`.
6. Run `go test ./internal/relevo/ ./internal/serve/ -run 'Unbind|Kept|GC'` until green, then `make check-affected`, then commit "unbind: the kept-worktree refusal carries the reason only".

Report: the new message text from a real run of the test (print it), and that `pick` and the cockpit now print the reason without a hint (B3 restores their hint).


