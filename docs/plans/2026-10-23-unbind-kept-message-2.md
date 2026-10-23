# Unbind kept message, plan 2 of 3 (#1289)

HARD RULES: do not push, do not run gh, do not touch any remote or PR; relevo owns the branch. Do only this plan, and end it in its own commit.

Goal: a served binding's kept line reads `worktree on <server> kept: <reason>` and a named unbind prints that with one `next:`, for a new server and for an old server whose message is the long form.

Behaviour and cases (a pure function in a new file, `internal/relevo/unbind_kept.go`):
- New server body: `binding api kept: worktree /p/api kept (uncommitted changes)` gives reason `uncommitted changes`.
- With the process clause: `...kept (uncommitted changes); its builder process (pid 9) was stopped` gives `uncommitted changes`. The pid is the server's, not useful to the client.
- Old server body (v0.19.0 and earlier on the server): `binding api kept: worktree /p/api kept (uncommitted changes); commit or discard the work, then unbind again, or \`relevo unbind api --force\` archives it anyway; on the server: relevo serve unbind --owner SHA256:abc api --force` gives `uncommitted changes`. The hints are cut at `; commit or discard the work`.
- A reason containing parentheses, such as `dirty check failed: exit status 1 (fatal: x)`, is kept whole (take everything between the first ` kept (` and the last `)` of the trimmed text).
- A message that matches nothing (`binding api kept`, as the existing tests send) gives the trimmed message unchanged; never an empty reason.

Steps:
1. New `internal/relevo/unbind_kept.go`: `func KeptReason(msg string) string` implementing the cases, and move the kept-error type here: give `worktreeKeptError` (`bind.go:910-915`) a `reason string` field and an exported accessor `KeptReasonOf(err error) string` (uses `errors.As`; returns "" when the error carries no reason). Keep `NewWorktreeKept(msg)` as it is for the local path, and add an unexported constructor with the reason. Remove the moved definitions from `bind.go`, so it shrinks.
2. `askServerUnbind` (`bind.go:944-945`): build the error with message `worktree on <server> kept: <reason>` and the reason set, from `KeptReason(httpErr.Body.Message)`. The old `<server>: <body message>` form is gone.
3. `askRemoteServers` (`internal/relevo/gc.go:209-212`): `res.KeptReason` becomes `KeptReasonOf(err)` (fall back to `err.Error()` if empty). `res.WorktreeKept` stays `on <server>`. `printGCResult` (`cmd/relevo/unbind.go:255-257`) then prints `worktree on sbsrv kept: uncommitted changes` plus the one existing hint line; do not change that print code.
4. Tests in `internal/relevo`: `TestKeptReasonReadsNewAndOldServerMessages` (a table over the five cases above, pure, no I/O); `TestUnbindOfAServedBindingNamesTheServerAndTheReason` (extend the shape of `unbind_keep_test.go:95-107`: body is the old long message, error text equals `worktree on zen kept: uncommitted changes`, contains no "--force" and no "serve unbind"); `TestGCRemoteKeptLineCarriesTheBareReason` (in `gc_remote_test.go` beside `TestGCKeepsRemoteBindingWhenServerKeepsWorktree`: `KeptReason == "uncommitted changes"` for the long body and `WorktreeKept == "on <server>"`). Update `unbind_keep_test.go:103`, which asserts `zen: binding api kept`, to the new text.
5. CLI tests in `cmd/relevo` must not run a subcommand that reaches a server or a harness. This plan adds none: the rule is the pure function above plus the two `internal/relevo` tests; the print code is untouched.
6. Run `go test ./internal/relevo/ -run 'Kept|Unbind|GC'` until green, then `make check-affected`, then commit "unbind: a served binding's kept line names the server and the reason".

Report: the printed kept line for an old-server body and for a new-server body, side by side.


