# Plan: a served binding that `done` kept dirty stays usable (#1261)

## What the code does today (read before the steps; it corrects the issue's premise)

The issue (and the seed) blame the server's teardown on done. The code says otherwise:

- `relevo done <chain>` on a server chain -> `chainServerDone` (`internal/relevo/chain_server_view.go:258`) -> `Remote.ChainDone` -> `handleDoneChain` (`internal/serve/chains_view.go:189`) -> `relevo.ChainDone` -> `Done` per member (`internal/relevo/done.go:42`). `Done` already keeps a dirty worktree (`worktreeTeardown`, `internal/relevo/bind.go:852`, non-forced). The lone-binding route (`handleDone`, `internal/serve/bindings.go:415`) is the same and already answers 409 for an open or queued round. Neither `teardownServed` nor `releaseServedRefs` runs on done.
- The binding directory (`serve/bindings/<client>/<name>/`) is removed by the daemon's seal pass: `sealRounds` (`internal/relevo/daemon.go:633`, tail at ~line 660) removes the directory of every DONE binding once it is empty, whether or not the worktree was kept. That is why `send` fails at `stagePlan` ("no such file or directory"): the next server tick after done deleted the directory. `Store.Save` recreates it (`ensureBindingDir`, `internal/store/lifecycle.go:175`) but `send` stages the plan before it saves. The same hole exists for a local DONE binding with a kept worktree; the fix below covers both.
- `bind --resume` on a remote binding is `Remote.Resume` (`internal/relevo/bind.go:569`) -> `handleResume` (`internal/serve/resume.go:23`), whose `CheckoutWorktree` (`internal/git/worktree.go:86`) fails on an existing path. That is the issue's "already exists" error.
- `collectSettled` (`internal/serve/cleanup.go:52`) is a second, latent hole for decision 1: a DONE, acked binding (1 h) or un-acked (7 d) is collected with `teardownServed(... force=true)` (`RemoveWorktree(..., true)`), which deletes a dirty tree. It must not.
- The client never learns what the server kept: `Remote.Done` / `Remote.ChainDone` return only an error (`internal/relevo/runtime.go:518,529`; `internal/remote/client/bindings.go:119`, `chains.go:129`) and discard the server's JSON view. For a server-chain member `Done` skips the server call entirely (`done.go:80`, `serverChainMember`).

Seed vs code: the seed says "find where the server tears down on done". There is none on done; the plan puts the dirty/open rule on the two real removers (seal pass, `collectSettled`), makes the server say what it kept, and adds resume adoption. Open-round behaviour needs no change (already 409 on the wire; `Done`'s local rule covers the rest).

## Behaviour and cases

1. Done on a served binding (lone or chain member) whose worktree is dirty: record stays DONE, worktree stays, binding directory stays (seal pass), refs/bare repo stay (collector skips it). `send` and `bind --resume` work afterwards. The done output says `kept worktree <path> (on <server>: uncommitted changes); relevo unbind --done retries when it is clean`, for both `relevo done <binding>` and `relevo done <chain>`.
2. Done on a clean served binding: worktree removed as today, empty directory removed by the seal pass as today, later collected as today.
3. Seal pass, any binding (local or served): a DONE binding's empty directory (and its empty `out/`) is removed only when `b.Worktree == ""` or the recorded path no longer exists (`Lstat` not-exist). A DONE binding whose worktree is still on disk keeps its directory.
4. `collectSettled`: a settled binding whose worktree path exists and is dirty (or whose dirty check errors) is skipped this tick (debug log, no archive, no ref/branch delete, no error). A clean or missing worktree is collected exactly as today. A skipped binding stays until `unbind --done`; `unusedRepos` keeps its bare repo because the row is live.
5. `handleResume` adoption: if `b.Worktree` does not exist -> `CheckoutWorktree` as today. If it exists and is a worktree of the binding's bare repo on the binding's branch -> adopt it (no git add), then re-activate and save as today. Anything else at that path -> refuse, no state change, HTTP 409 `remote.CodeInvalid`, message naming the path, the bare repo and the branch and saying to move the directory aside or `relevo unbind`. "Is a matching worktree" = `rt.Git.ShowPrefix(path)==""`, `rt.Git.RepoFacts(path)` commonDir equals `EvalSymlinks(bare)`, `rt.Git.CurrentBranch(path)` equals `b.Branch` minus any `refs/heads/`; all three exist on the `Git` interface, so no interface or fake changes. A git error on any probe is "not a matching worktree".
6. Wire: `remote.BindingView` and `remote.ChainView` gain `worktree_kept` and `kept_reason` (both `omitempty`). `handleDone` sets them from `relevo.Done`'s `DoneResult` (today discarded); `handleDoneChain` sets them from `relevo.ChainDone`'s `DoneResult` (the builder's, per its doc). Old servers send nothing -> old output; old clients ignore the keys. No feature flag needed.

## Seams

- `internal/relevo/daemon.go` ~655-672 (`sealRounds` DONE tail): add the worktree-kept guard around both removals; small helper in the same file.
- `internal/serve/cleanup.go` `collectSettled` loop (~line 78-90): dirty skip before `teardownServed`. Keep `teardownServed` as is.
- `internal/serve/resume.go`: `handleResume` line 49; add an unexported adoption helper in this file; keep `resumeSeam` placement (after checkout/adopt, before save).
- `internal/remote/proto.go` `BindingView` (~185) and `internal/remote/chain.go` `ChainView` (~121): two fields each.
- `internal/serve/bindings.go` `handleDone` (443-465) and `internal/serve/chains_view.go` `handleDoneChain` (189-247): keep the `DoneResult`, set the view fields before `writeJSON`.
- `internal/remote/client/bindings.go:119` and `chains.go:129`: `Done` returns `(remote.BindingView, error)`, `ChainDone` returns `(remote.ChainView, error)` (decode like `Resume`/`ChainResume`; still no retry). `internal/relevo/runtime.go:518,529`: interface to match. `internal/relevo/remote_test.go:287,339`: `fakeRemote` follows (add `doneResp`/`chainDoneResp` fields).
- `internal/relevo/done.go:80-100` and the result switch ~167: keep the view's kept fields in a local; in the switch add a case before `b.Worktree == ""` that sets `WorktreeKept = view.WorktreeKept`, `KeptReason = "on <server>: " + view.KeptReason`. `internal/relevo/chain_server_view.go:265-295`: use `ChainView` fields when the member loop's `out` has no `WorktreeKept` of its own (mirror members have no worktree). `DoneText` (`internal/relevo/text.go:37`) and `doneDocOf` (`cmd/relevo/writejson.go:125`) need no change; they render `WorktreeKept`/`KeptReason`.

## Steps (in order; all new commits, nothing amended)

1. Wire + client plumbing: `proto.go`, `chain.go`, `client/bindings.go`, `client/chains.go`, `runtime.go`, `remote_test.go` fake. Done when `go build ./... && go vet ./internal/...` pass; `TestGateFieldsAreAdditive`'s sibling contract tests still pass (new keys are `omitempty`).
2. Server sets the fields: `handleDone`, `handleDoneChain`. Done when a served-test (step 7) sees `worktree_kept` on a dirty done.
3. Client uses them: `done.go` switch, `chain_server_view.go`. Done when `DoneText` for a remote done with a kept view contains "kept worktree" and "on <server>".
4. Seal-pass guard (`daemon.go`). Done when a DONE binding with an existing worktree keeps its directory in a focused test, and the existing `TestSealPassEmptiesADoneDirOnly` still passes.
5. `collectSettled` dirty skip (`cleanup.go`). Done when a dirty settled binding survives a tick with worktree, branch, refs and row intact.
6. Resume adoption (`resume.go`). Done when a matching existing worktree resumes 200 and a plain directory gets 409 with the worktree untouched.
7. Tests (below).
8. Plan file: save this plan verbatim as `docs/plans/2026-10-17-served-done-keeps-dirty.md`; commit it with the code (one PR; commit message ends with the repo's attribution line).

## Tests (names say what they pin; no harness spawn, no network)

All in `internal/serve` use `setupTestEnv`/`seedServedBinding`/`env.gitClient`/`env.srv.Tick` (`helpers_test.go`, `cleanup_test.go` patterns; real git on temp dirs). The `internal/relevo` test uses a bare `store.New(t.TempDir())` like `seal_test.go:262`. No test is added to `cmd/relevo` (CI has no harness binary or network; the output wording is covered by a pure `DoneText`/`Done` test in `internal/relevo`).

- `internal/relevo/seal_test.go`: extend the done/active table: DONE + worktree path exists -> dir stays; DONE + worktree path missing -> dir gone; DONE + no worktree -> gone (existing case).
- `internal/serve/cleanup_test.go`: dirty settled binding (write an untracked file in the seeded worktree, age past the acked grace as `TestCollectSettledOnTick` does) survives `Tick`: row not archived, worktree and the file present, branch and `refs/relevo/<name>/*` present; the clean twin in the same test is still collected.
- `internal/serve/` new `done_keeps_test.go`: `POST /v1/bindings/{name}/done` on a dirty seeded ACTIVE binding with no open round -> 200, view carries `worktree_kept`/`kept_reason`, worktree still on disk, and after `Tick` the binding directory still exists; the same on a clean binding -> worktree removed, no `worktree_kept`.
- `internal/serve/resume_test.go` (new): resume of a DONE binding whose seeded worktree still exists -> 200, state ACTIVE, the worktree's uncommitted file intact; resume where the recorded path is a plain directory (and one that is a worktree on another branch) -> 409, state still DONE, directory untouched; resume where the path is gone -> restored as before.
- `internal/relevo` (beside `remote_test.go`/`done_test.go` fakes): `Done` on a remote binding whose fake `Done` returns a kept view -> `DoneResult.WorktreeKept/KeptReason` set and `DoneText` says kept; with an empty view -> unchanged text. Same for `chainServerDone` with the fake `ChainDone` view.
- Mutation (each must turn a named test red, then be restored): drop the worktree-exists condition in the seal guard; drop the dirty check in `collectSettled`; make adoption skip the branch check; skip the commonDir check; skip the "is a worktree" probe (ShowPrefix); drop the handler's copy of `DoneResult` into the view; drop the client switch case.

## Verify

Focused: `go test ./internal/serve ./internal/relevo ./internal/remote/... -run 'Seal|Settled|Collect|Resume|DoneKeeps|DoneText|ServerDone' -count=1`; fix every error before rerunning. Final, once: `make check` (add `RELEVO_REQUIRE_COVERAGE=1` only if comparing the baseline locally; no package moves, so no baseline regeneration expected).

## Declared scope (files touched; anything else is drift)

`internal/relevo/daemon.go`, `internal/relevo/done.go`, `internal/relevo/chain_server_view.go`, `internal/relevo/runtime.go`, `internal/relevo/remote_test.go`, `internal/relevo/seal_test.go`, `internal/relevo/done_test.go` (or a new `done_remote_kept_test.go`), `internal/remote/proto.go`, `internal/remote/chain.go`, `internal/remote/client/bindings.go`, `internal/remote/client/chains.go`, `internal/serve/cleanup.go`, `internal/serve/resume.go`, `internal/serve/bindings.go`, `internal/serve/chains_view.go`, `internal/serve/cleanup_test.go`, `internal/serve/done_keeps_test.go` (new), `internal/serve/resume_test.go` (new), `docs/plans/2026-10-17-served-done-keeps-dirty.md` (new). Code style: no issue numbers or history in code or tests; comments say why only; functions <= 70 lines; `scripts/check-comments.sh` and `check-filesize.sh` must pass with no new exclusions (`cleanup.go` and `daemon.go` stay under 600 lines; check).

## Deleted

Nothing. No behaviour is removed; `teardownServed` and `releaseServedRefs` are unchanged.

## Report must include

- That the issue's premise (teardown on done) was wrong and which two removers were fixed (seal pass; `collectSettled`'s forced removal), with the failing and passing test names.
- The mutation list with the test that failed for each.
- Server-side changes (seal guard, collector skip, resume adoption, done view fields) reach zen/contabo only on redeploy of `relevo serve`; the client output line additionally needs the client upgraded. Until the server is redeployed, an already-DONE dirty binding's directory is still removed on that server; a binding already hit by the bug is recovered by `bind --resume` after the redeploy (Save recreates the directory).
- Trade-off: a dirty DONE served binding now stays on the server (slot-free, DONE bindings do not count in admit) until `relevo unbind --done`; note this in the runbook only if the builder finds a doc that says DONE bindings are always collected.
- Follow-up (out of scope, file as an issue): give a halted chain's builder one more round without closing the chain (`stop` is refused on a halted chain; `chain --resume` re-runs the same plan). Also noted: `unbind --done` archives the record even when it keeps a dirty worktree (`Unbind`, `bind.go:911`), so "retries later" is by directory/branch, not by the record; worth a separate look.
