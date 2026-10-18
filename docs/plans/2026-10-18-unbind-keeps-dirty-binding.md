# Plan: unbind keeps a binding whose worktree is kept (dirty), unless forced (#1273)

Note: `gh issue view 1273` returned nothing in the planning sandbox (no network); the plan rests on the seed and the code.

## What the code actually does today (the seed's premise is partly wrong)

The seed says a LOCAL `relevo unbind --done` keeps and retries a dirty tree. The code says otherwise, so **local and server already agree -- both archive/delete the binding when the worktree is kept**:

- `relevo.Unbind` (`internal/relevo/bind.go:911-1000`): `worktreeTeardown` (L852) may return `Kept` (git nil, dirty-check error, dirty tree, remove failure), then `Unbind` archives (`rt.Store.Archive`, ~L990) or deletes unconditionally. Tests pin this: `internal/relevo/bind_test.go` ~L1195-1320 ("fork-dirty", "fork-dirty-err", git unavailable, "fork-fail" assert "binding state should still be deleted").
- `relevo unbind --done` is `runGC` (`cmd/relevo/unbind.go:178`) -> `relevo.GC` (`internal/relevo/gc.go:70-135`): for a DONE binding it runs `worktreeTeardown`, skips only ref cleanup when `Kept`, then archives/deletes anyway (L121-127). `cmd/relevo/unbind.go:239,252` prints "kept worktree ... remove by hand". `gc_test.go` `TestGCWorktreeDirtyCheckError` (~L430-470) asserts `Deleted == true` with a kept tree. So the `DoneText` promise "relevo unbind --done retries when it is clean" (`internal/relevo/text.go:47`) and CLAUDE.md's "a dirty tree ... is kept and `unbind --done` retries" are false for the binding record today (GC only visits non-archived DONE bindings, so after the archive there is nothing to retry).
- Server: `handleUnbind` (`internal/serve/bindings.go:477-505`) calls `relevo.Unbind(..., true)` and answers `200 {}`; `AdminUnbind` (`internal/serve/admin.go:342-375`) and `gcArchive` (`admin.go:~258`) do the same. The client `Client.Unbind` (`internal/remote/client/bindings.go:130`) discards the body (`postOnce`), and `Unbind` in `bind.go:931-946` asks the server first, treating only 404 as benign.

Decision (made, pre-answered here): the keep rule goes into the shared core, so local plain unbind, local `unbind --done`, the server route, and the admin verbs all follow it. Forcing needs an explicit `--force`.

## Behaviour

1. `Unbind` (guarded): after the builder process is stopped and the worktree teardown has run, if the teardown outcome is `Kept` (any reason: uncommitted changes, dirty check failed, git unavailable, remove failed) and force is false, the binding is **not** archived/deleted. Unbind returns the result plus a refusal error (`ErrWorktreeKept`, unwraps to `ErrRefused`) whose message names the binding, the kept worktree and the reason, and ends with the force command (`relevo unbind <name> --force archives it anyway`). Clean worktree, gone worktree, no worktree: unchanged.
2. With force the old behaviour is unchanged (archive/delete, kept worktree reported).
3. `GC` (`unbind --done`): a DONE binding whose teardown is `Kept` is left in place (state DONE), `GCResult.BindingKept = true`, neither `Archived` nor `Deleted`, no ref cleanup (already skipped). Dry run reports `BindingKept` too. `GCOptions.Force` restores the archive/delete. This makes the "retry when clean" text true.
4. Remote binding, client side: `Unbind` asks the server first. A `409` with code `worktree_kept` aborts before any local change and returns the same `ErrWorktreeKept` refusal carrying the server's message prefixed with the server name. Other errors/404 as today.
5. Server: `handleUnbind` reads `?force`; without it a kept worktree answers `409` `worktree_kept` (message = the refusal text) and the binding stays. With `?force=1` it archives as today. `AdminUnbind`'s existing `force` is **reused**: it already means "do not stop for a guard" (running round); it now also bypasses the keep guard (passed through to the core). `relevo serve unbind --force` therefore needs no new flag; update its usage text and the error text. `gcArchive` (abandoned-binding GC) deliberately keeps today's behaviour (force true): it is an age-based retention policy, not an unbind a human asked for; say so in a comment-free way by passing `true` explicitly.
6. CLI: new flag `relevo unbind --force` (no existing unbind force flag; `send --force` is unrelated, `serve unbind --force` is the admin twin and the name is reused). It applies to named unbind, `--pick`-less paths and to `--done`. Refused with `--sweep` (add to the existing refusal at `cmd/relevo/unbind.go:60`). On the refusal the CLI prints the error and `next: relevo unbind <name> --force` (built in `cmdUnbind` via `failNext(codeRefused, ...)`, exit code of `codeRefused`). `--done` kept lines print "kept <name> ... worktree X kept: reason / commit or discard it, then run relevo unbind --done again, or --force to archive it anyway" and the dry-run line says "would keep". JSON: `gcDoc` carries `binding_kept` via the `GCResult` tag (additive).
7. `relevo pick` unbind (`internal/pick/model.go:89`) uses the guarded `Unbind`: the refusal text (which already ends with the force command) is shown as the verb error; no pick force option.
8. Wire compatibility (server one release behind):
   - Old server + new client: ignores `?force`, archives as before (no protection until the server is redeployed; accepted, state in report). No feature probe added.
   - New server + old client: old client sends no `force`; a kept worktree answers 409 `worktree_kept`; the old client surfaces it as `<server>: <message>` and changes nothing locally (`bind.go:936-946` generic branch). Safe direction. The message therefore names `relevo serve unbind --owner <label> <name> --force` on the server and `relevo unbind <name> --force` for a current client.
   - New code `remote.CodeWorktreeKept`; no `FeatureX` token.
9. Rollback callers keep discarding: `chain_remote.go:261` and `remote_add.go:291` pass force=true (fresh, clean server bindings being rolled back).
10. Out of scope, mention in report: `GC` of a local DONE *mirror* of a remote binding never contacts the server (unchanged); `GCAbandoned` age policy unchanged.

## Seams

- `internal/relevo/refusals.go` (or `bind.go`): `ErrWorktreeKept` sentinel built with `refusalSentinel`; a small error type carrying message, unwrapping to the sentinel.
- `internal/relevo/bind.go`: `Unbind(ctx, rt, name, archive)` becomes a wrapper for new `UnbindForce`-style entry `unbindWith(ctx, rt, name, archive, force)`; export it as `UnbindWith(ctx, rt, name string, archive, force bool)`. `Unbind` = `UnbindWith(..., false)`. Keep the 7 other callers (`setup_smoke.go`, tests, `prune_race_test.go`) on `Unbind`. Reorder inside: stopProcess -> `worktreeTeardown` -> **guard** -> abandoned-session deletion -> `removeReaderScratch` -> archive/delete. The guard returns before sessions are deleted so a kept binding stays resumable. Verify `removeReaderScratch` does not need to precede teardown (reader bindings have no `Worktree`); if it does, leave it before the guard and note it. Function must stay <= 70 lines: extract the remote-ask block (L931-946) and the session/scratch cleanup into helpers. Remote branch: map `HTTPError{Status:409, Body.Code: CodeWorktreeKept}` to the refusal.
- `internal/relevo/gc.go`: `GCOptions.Force`, `GCResult.BindingKept` (`json:"binding_kept,omitempty"`); in the loop skip archive/delete when `outcome.Kept != "" && !opts.Force`.
- `internal/relevo/runtime.go:519`: Remote interface `Unbind(ctx, server, name string, force bool) error`; update `internal/relevo/remote_test.go:294` fake (record force; add a way to return a given error) and callers `bind.go`, `chain_remote.go:261` (true), `remote_add.go:291` (true).
- `internal/remote/proto.go` (~L405): `CodeWorktreeKept Code = "worktree_kept"` with a one-line why-comment (409, binding kept).
- `internal/remote/client/bindings.go:130`: `Unbind(ctx, server, name, force)`; append `?force=1` to the path when set (query is signed via `u.RequestURI()`, `client.go:219`; precedent `rounds.go:194`).
- `internal/serve/bindings.go:477`: read `r.URL.Query().Get("force") != ""`; call `relevo.UnbindWith(ctx, rt, name, true, force)`; on `errors.Is(err, relevo.ErrWorktreeKept)` -> `writeErr(w, 409, remote.CodeWorktreeKept, err.Error())`; the process-stopped side effect already happened, so the message says so. Keep `releaseServedRefs` only when archived with no kept worktree. If the function nears 70 lines extract.
- `internal/serve/admin.go:342`: pass `force` into `UnbindWith`; the error from a kept tree reaches `cmd/relevo/serve_admin_write.go:272` (update its flag help: "--force: unbind a running round or a kept worktree"). `gcArchive` (~L258): `UnbindWith(..., true, true)`.
- `cmd/relevo/unbind.go`: `force *bool` in `unbindFlagValues`/`unbindFlagSet`; wire to `UnbindWith` (L110) and `GCOptions.Force` (`runGC`, L178-195; signature grows); `--sweep` refusal; `errors.Is(err, relevo.ErrWorktreeKept)` -> `failNext`; GC printing branches (L220-260; extract a helper so `runGC` stays <= 70 lines and the file stays <= 600). Check `unbind_test.go` and the `help --json` contract tests (`cmd/relevo/contract_write_test.go`) for the flag list and fix their expectations.
- `internal/relevo/text.go`: `UnbindText` unchanged for the forced path; add the force hint to the refusal message only (not to `DoneText`, whose retry sentence becomes true as written).
- `internal/pick/model.go:89`: no change expected; confirm.
- Docs: README.md L513-525 (unbind flags: add `--force`, say a kept worktree keeps the binding), `docs/specs/2026-09-19-remote-builders-design.md` ~L227 (unbind: `409 worktree_kept` unless `?force`), CLAUDE.md sentence on `unbind --done` stays true (leave). Keep `internal/mastermind/guide.md` untouched unless it states the old behaviour (grep shows no such line).

## Ordered steps (each: deliverable -> how to know)

1. Core: `ErrWorktreeKept`, `UnbindWith`, reorder + helpers in `bind.go` -> `go test ./internal/relevo -run 'Unbind|Teardown'` green after step 2's test edits.
2. Update existing tests that pinned the old behaviour: `bind_test.go` ~L1195-1320 (dirty, dirty-err, git unavailable, fail: now `ErrWorktreeKept`, binding still loadable, sessions untouched; add a forced variant per case that keeps the old assertions via `UnbindWith(..., true)`), `headless_test.go` ~L2712/2738 if they use a kept tree, `gc_test.go` `TestGCWorktreeDirtyCheckError` (now `BindingKept`, not Deleted; forced run deletes).
3. GC: `Force`, `BindingKept`, loop change -> `go test ./internal/relevo -run GC`.
4. Remote interface + client + fake: signature change compiles across `internal/relevo`, `internal/remote/client`; `go vet ./...`.
5. Wire: `CodeWorktreeKept`, `handleUnbind` 409/force, client mapping in `UnbindWith` -> new serve test and relevo remote test below.
6. Admin: `AdminUnbind` pass-through, `gcArchive` explicit force -> `go test ./internal/serve -run 'Admin|Unbind|GC'`.
7. CLI: `--force`, `next:` hint, GC kept lines, JSON -> `go test ./cmd/relevo -run 'Unbind|Contract|Help'`. A test in `cmd/relevo` must not spawn a harness or reach the network: pin the keep rule as pure functions in `internal/relevo` (done in steps 1-3) and test only flag parsing/refusal wording in cmd without running a binding with a builder.
8. Docs edits (README, remote design spec), then save this plan to `docs/plans/2026-10-18-unbind-keeps-dirty-binding.md` and commit it with the code (new commits only; no amend/rebase).
9. Gate: `make check-affected` (focused loop first: `go test ./internal/relevo ./internal/serve ./internal/remote/... ./cmd/relevo ./internal/pick`), then one full `make check-affected`. If coverage baseline moves, say so; do not lower it.

## Regression tests and mutations

- `internal/relevo`: `TestUnbindKeepsBindingWhenWorktreeKept` (table: dirty, dirty-check error, git nil, remove failure) -- binding still loads, error `errors.Is ErrWorktreeKept` and `ErrRefused`, `AbandonedSessions` not deleted (fake reaper called 0 times), message contains `--force`. Mutation: drop the `outcome.Kept != "" && !force` guard (or make force ignored) -> fails.
- `TestUnbindForceArchivesKeptWorktree` -- `UnbindWith(..., true, true)` archives and reports `WorktreeKept`. Mutation: invert force check -> fails.
- `TestGCKeepsBindingWithKeptWorktree` and `TestGCForceArchivesKeptWorktree` (dry run reports `BindingKept` too). Mutation: remove the `!opts.Force`/Kept condition in the loop -> first fails.
- `internal/relevo` remote: `TestUnbindRemoteWorktreeKeptLeavesLocalBinding` using the fake returning `HTTPError{409, worktree_kept}` -> local binding intact, error is `ErrWorktreeKept`, fake got force=false; `TestUnbindRemoteForcePassesForce`. Mutation: treat the 409 as success -> fails.
- `internal/serve`: `TestUnbindDirtyWorktreeAnswers409` (model on `done_keeps_test.go:36`, `doSigned` POST `/v1/bindings/dirty/unbind`; dirty tree -> 409, code `worktree_kept`, binding dir and worktree still on disk) and `TestUnbindForceArchivesDirtyWorktree` (`?force=1` -> 200, archived). Mutation: remove the 409 branch or the force read -> fails. Also `TestAdminUnbindKeptWorktreeNeedsForce` (extend `admin_test.go` near L367). Existing `TestAdminUnbindByLabelAndId`/cleanup tests keep passing (clean trees).
- `internal/remote/client`: test that `Unbind(..., true)` requests `?force=1` and `false` requests the bare path (model on `client_test.go:375`).
- `cmd/relevo`: flag/refusal test for `--sweep --force` and the contract/help listing of `--force`.

## Scope (git diff --stat must stay inside)

`internal/relevo/{bind.go,gc.go,refusals.go,runtime.go,chain_remote.go,remote_add.go,text.go?}` and tests (`bind_test.go,gc_test.go,remote_test.go,headless_test.go?` plus new tests); `internal/remote/proto.go`; `internal/remote/client/{bindings.go,client_test.go or bindings_test.go}`; `internal/serve/{bindings.go,admin.go}` and tests; `cmd/relevo/{unbind.go,serve_admin_write.go,writejson.go?}` and tests; `README.md`; `docs/specs/2026-09-19-remote-builders-design.md`; `docs/plans/2026-10-18-unbind-keeps-dirty-binding.md`. Nothing else; no CHANGELOG/baseline edits unless coverage requires `scripts/check-coverage.sh --write` (say so).

## Report must include

- That the seed's premise was wrong (local also archived) and what changed for local unbind, `unbind --done` and the server.
- Whether `removeReaderScratch` ordering mattered and where the guard sits relative to the process stop (the builder is already stopped when a kept binding is refused).
- The old-server caveat (no protection until redeploy; `relevo serve` redeploy needed) and the old-client behaviour.
- `make check-affected` output tail and the mutation results for each named test.

## Round 2

`relevo unbind --done` (`GC`) now asks a remote DONE binding's server to unbind it, with `opts.Force`, before archiving or deleting the local record; the local record of a remote binding has no `Worktree`, so round 1's local teardown never saw the server's dirty tree.

- Server answers `ErrWorktreeKept`: the binding stays DONE, the result has `BindingKept`, `WorktreeKept` and `KeptReason`, and the CLI prints the existing kept line.
- nil or 404: archive or delete locally. Any other error (unreachable, 5xx, no remote client): archive locally and record the error in `GCResult.ServerErr` (`server_err`), which the CLI prints as a note.
- Dry run never contacts the server.
- No network call under `WithLock`: the locked scan handles local bindings and collects remote DONE ones; the lock is released and each server asked; a second locked pass re-loads each released binding and skips one that is no longer DONE or no longer exists.
- Local bindings behave as in round 1. `TestGCRemoteOnlyWhenDone` now asserts the single unbind call for the DONE binding instead of none.
- Tests: `internal/relevo/gc_remote_test.go`. README unbind entry updated.
