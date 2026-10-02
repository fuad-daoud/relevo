# Plan: `relevo done` on a server-chain mirror whose server chain is gone (#861)

Base: `origin/main` 8a660f20. One builder round. All code changes are in `internal/relevo`; no wire or schema change.

## Behaviour and cases

**1. `done <chain>` settles a mirror whose server chain is gone.** A 404 from the server for the chain — on the mirror pull or on `POST /v1/chains/<n>/done` — means the server already released it. `chainServerDone` then settles locally: release every member through the ordinary `Done` (a server-chain member already skips the server call at `done.go:65`), then close the row through `chainDoneRow`. Every other remote error still fails the verb exactly as today (409 running, 409 round_open, unreachable, etc.).

**2. The daemon stops polling a mirror the server reported gone.** `chainPullServers` skips a chain whose row is halted with the gone reason, the same way it already skips a done row. `chainPullGone` writes that reason; it also stamps it on an already-halted mirror (no second delivery), so a mirror that was halted before the server vanished is not read every tick.

**3. `show <chain> --trace` on a gone server chain is not-found.** `chainServerTrace` wraps `store.ErrNotFound` when `GetChain` answers 404, so `classifyReadErr` (`cmd/relevo/show.go:257`) maps it to the not-found code instead of the `internal: not found` the raw `HTTPError` produces.

Cases: gone mirror with 404 on GetChain and ChainDone → no error, row done, every member DONE, zero `Remote.Done` calls; same mirror with 200 → `ChainDone` posted exactly once; halted gone mirror → not GET again on the next pass; trace on a gone server chain → error wrapping `store.ErrNotFound`; a non-404 remote error → still fails.

## Bounded static check (asked by the seed)

A row without `WorkflowJSON` cannot reach `chainDoneRow`/`chainAdvance` **via the pull**: the pull calls neither, `chainRowFromView` (`chain_pull.go:517`) always writes `WorkflowJSON`/`StateJSON` through `workflow.FromLegacy`, and `chainApply` (`chain.go:148`) returns early for `chainOnServer`. `chainDoneRow` (`chain_verbs.go:246`) does require `WorkflowJSON` (`chain_engine.go:22`), so a server-chain mirror that never completed a pull would still fail `carries no workflow`; the real mirror was pulled while the server was alive and is converted. The tests pin the converted mirror, as the seed directs; no extra conversion is added.

## Seams

- `internal/relevo/chain_server_view.go` — `chainServerDone` :232–282 (pull call :236, `ChainDone` call :239–247); `chainServerTrace` :71–92 (view read :72–75).
- `internal/relevo/chain_pull.go` — `chainPullServers` :29–47 (filter :39); `chainPullGone` :80–110 (reason :91).
- `internal/relevo/chain_server.go` — two helpers beside `chainOnServer` :15: `chainGoneReason(name, server string) string` (the one wording) and `chainGoneMirror(c db.ChainRow) bool` (halted and reason is the gone reason). Add `fmt` and `internal/chain` imports.
- Tests: `internal/relevo/chain_server_view_test.go`, `internal/relevo/chain_pull_test.go`, reusing `chainPullFake`, `chainPullRuntime`, `seedServerChain`, `chainPullView`, `pullRounds`, `countCalls`, `exactCalls`.
- Docs: `docs/plans/2026-10-02-server-chain-done-gone.md`.

## Ordered steps

1. Add `chainGoneReason`/`chainGoneMirror` to `chain_server.go` and use `chainGoneReason` in `chainPullGone` (`chain_pull.go:91`). Done when `go build ./...` compiles and `TestChainPullHaltsAChainGoneFromTheServer` is unchanged.
2. In `chainPullGone`, stamp the gone reason on an already-halted row (no delivery); in `chainPullServers` (`chain_pull.go:39`) skip `chainGoneMirror(c)`. Done when a second pass over a halted gone mirror makes no `GetChain`.
3. In `chainServerDone` (`chain_server_view.go:236–247`): a 404 from `chainResultFromMirror` or from `ChainDone` is "already released" and falls through to the local release; any other error keeps the current return. Done when the 404 fixture test passes.
4. In `chainServerTrace` (`chain_server_view.go:72–75`): on `is404(err)` return `fmt.Errorf("chain %s: %w", c.Name, store.ErrNotFound)`. Done when the trace test passes.
5. Add tests (names say what they pin; no issue numbers): `TestChainDoneOnAGoneServerChainSettlesTheMirror` — convert the mirror with one halted 200 pull (`chainPullView("shop", halted, 0,0,0)`), then `getChainErr`/`chainDoneErr` 404; assert no error, row done, each of `shop`/`shop-rev`/`shop-plan` DONE, `countCalls(fr, "Done:zen:") == 0`. `TestChainDoneOnAServerChainStillPostsDoneAfterTheGonePath` — the same converted mirror, legacy wire view (no workflow fields), 200; `ChainDone` count 1. `TestChainPullDoesNotGetAGoneMirrorAgain` — 404, pull, `countCalls(fr,"GetChain:zen:shop")==1`, pull, still 1. `TestChainTraceOnAGoneServerChainIsNotFound` — 404, `errors.Is(err, store.ErrNotFound)`. Done when `go test ./internal/relevo/ -run 'ChainDone|ChainPull|ChainTrace' -count=1` is green. No `cmd/relevo` test: a CLI test must not reach the network (CLAUDE.md "Merging and CI").
6. Mutation-check each: (a) change the `!is404` gate to always error → step 5's settle test fails; (b) remove the `ChainDone` post → the 200 test fails; (c) `chainGoneMirror` returns false → the not-GET-again test fails; (d) return the raw trace error → the trace test fails. Done when each mutation fails exactly its test.
7. Run `make check` and `sh scripts/check-comments.sh` directly; both green. No baseline change, no new lint exclusion.
8. Save this plan to `docs/plans/2026-10-02-server-chain-done-gone.md` and commit it with the code as new commits (no amend or rebase of any commit already on a remote branch).

## Deleted behaviour

None. This round adds behaviour only; no code path is removed.

## Report must include

`git diff --stat` against this scope; the focused test command and output; `make check` and `sh scripts/check-comments.sh` output; the four mutations and the named test each failed; the bounded static-check finding and the no-workflow-mirror limitation; the new test names; and confirmation that no coverage baseline was lowered and no lint exclusion added.
