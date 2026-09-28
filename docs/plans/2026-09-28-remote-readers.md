# Plan for #607 (A5 R7): reader rounds on a remote server

`gh issue view 607` needed approval I didn't have in this round, so this plan works from the seed and the code. The code agrees with the seed on every point except three, flagged below. Two of those are real gaps that the seed's list of seams doesn't cover.

## Behaviour

- **Binding a reader on a server:** `relevo bind --actor <reader> --server <s>` works when the server advertises **`readers`**, the new feature token (`remote.FeatureReaders = "readers"`). If the server doesn't advertise it, the client refuses before contacting the server any further. The message stays "reader actors run locally only; bind without --server" and adds "(server <s> predates remote readers; upgrade it)".
- **Where the round runs on the server:** the server records the binding as `Shape: reader`. Each round runs in a scratch worktree cut from the served worktree at the round's baseline (`RoundBaselineHead`/`RoundBaselineTree`). The scratch is removed when the round closes. The served worktree and branch are never touched.
- **What the client gets at catch-up:** when the round is closed, the client lists the round's artifacts and downloads each file into `NNN-<actor>/`. The server's output file becomes the round's report. Its daemon then seals the directory into round_file rows, exactly like a local reader round. No diff is fetched or recorded, the bundle is expected empty (204), and the ack happens as today.
- **Existing writer bindings and older servers are unchanged.**

**Cases to cover:**
- A reader bind against a new server, and against an old one.
- A server with no roles registry, or an actor the client doesn't know. The server's `Shape` on the view is what counts.
- A closed reader round with nested artifacts such as `site/index.html`.
- A round whose output file is missing and was not stopped: it halts, as the writer "no report" path does.
- A stopped reader round.
- The artifact cap exceeded on the client.
- A malicious or garbled `rel` from the server (`..`, absolute path, backslash, empty): refused and nothing written.
- A reader round that was already sealed on the server before the client fetched it. `RoundArtifacts`/`ReadArtifact` read sealed rows, so this must still work.

## Contracts

**Feature token:** `FeatureReaders = "readers"` in `internal/remote/proto.go`, next to `FeatureLabels`. It is added to the list in `handleWhoAmI` (`internal/serve/routes.go:108`).

**Wire types** (new, in `proto.go`):
- `ArtifactFile{Rel string "rel"; Size int64 "size"; MTime time.Time "mtime"}`.
- `ArtifactList{Actor string "actor"; Output string "output"; Files []ArtifactFile "files"}`.
  - `Output` is the server's `relevo.OutputFile(rt,b)`, e.g. `findings.md`.
  - `Files` are sorted the way `relevo.RoundArtifacts` sorts them.

**New `BindingView` field:** `Shape string "shape,omitempty"`. The server sets it from `b.Shape`. An empty value means a writer, i.e. an older server.

**Routes** (in `routes.go`, next to `files/{kind}`):
- `GET /v1/bindings/{name}/rounds/{n}/artifacts` returns `ArtifactList` as JSON.
- `GET /v1/bindings/{name}/rounds/{n}/artifacts/{rel...}` returns the file's bytes as `application/octet-stream`.
- Both use the same checks as `handleRoundFile`: `loadBinding`, `Allowed(caller,"files",b)`, `1 ≤ n ≤ b.Round`, and 404 `round %d is not closed` when `n > Serve.ClosedRound`. A writer binding gets 404 `not a reader binding`. A rel the listing doesn't hold gets 404, via `errors.Is(err, relevo.ErrNoArtifact)`.

**Client:** two new methods on `RemoteClient` (`internal/relevo/runtime.go:411`) and on `*client.Client` (`internal/remote/client/rounds.go`):
- `RoundArtifacts(ctx, server, name, round) (remote.ArtifactList, error)`
- `RoundArtifact(ctx, server, name, round, rel) (io.ReadCloser, error)`

The rel is path-escaped one segment at a time, so the `/` separators are kept.

## Seams the seed missed (found in the code)

1. **The served binding never gets a Shape.** `buildServedBinding` (`internal/serve/bindings.go:160-257`) builds `store.Binding` with no `Shape`, so it defaults to writer (`store/binding.go:340`). Every reader branch on the server keys off `b.Shape`: `send.go:539`, `queue.go:58`, `headless.go:292`, `reconcile.go:319/380/548/621`, `summary.go` and the daemon sweep at `daemon.go:444`. The server must set `Shape: shape` from the `relevo.ActorShape` result it already computes at line 170.
2. **The client's remote binding never gets a Shape either.** `remote_add.go:300-326` has the same gap. Without it, `closeClause`, `reportPathFor`, `show --output` and `queueReport`'s reader branches all treat the round as a writer. Set it from `view.Shape`.
3. **The report outcome is lost across the wire.** At close, the server's `queueReport` strips the relevo block from the reader's output file (`reconcile.go:545-553`). The file the client downloads therefore has no block. Parsing it in the client's `queueReport` (`reconcile.go:392-409`) yields `unstructured`, and `view.ReportOutcome` carries the real status. For a remote reader, the client must use `view.ReportOutcome` whenever parsing the body gives no structured tail. `halted_at`, `changed_paths`, `commands_run` and `not_done` cannot be recovered, because the view doesn't carry them. Recover the status only, and say so in the report.
4. **The output label can differ between client and server.** Each side resolves `readerOutputLabel` from its own config. The client installs the server's `list.Output` file at its own `reportPathFor(rt,b)` and every other rel verbatim, so `show --output`/`--report` and `closeClause` stay consistent on the client.
5. **`internal/relevo/remotefetch.go` is exactly 600 lines**, which is the file-size cap, and `remote_catchup.go` is 215. The new client fetch and apply code goes in a new file, `internal/relevo/remote_artifacts.go`. Nothing is added to `remotefetch.go` except the one branch in `fetchCatchUpFiles`, paid for by moving code if needed. No allow-list entry is added.
6. **`internal/serve` is fully linted.** It has no exclusion in `.golangci.yml`, so the new handlers must pass golangci-lint as they are. `internal/relevo` and `internal/remote/*.go` are excluded, but the 70-line function cap still applies by convention.
7. **The server-side refusal can't sit "behind the feature check" as the seed says.** The server is the side that advertises the feature, so a server that has this change simply accepts readers. The refusal at `bindings.go:175-179` is therefore deleted, not gated. The client's refusal at `add.go:130-137` is the one that becomes feature-gated.
8. **The fakes must implement the two new methods:** `fakeRemote` in `internal/relevo/remote_test.go:51`, and any other `RemoteClient` fake the compiler finds.

## Ordered steps

1. **Wire types and token.** In `internal/remote/proto.go`, add `FeatureReaders`, `ArtifactFile`, `ArtifactList` and `BindingView.Shape`. Add both new types to `protoCases` in `internal/remote/contract_test.go`, and extend the `BindingView`/`WhoAmI` cases so `shape` and `readers` appear. Then run `go test ./internal/remote -run Contract -update`, followed by the same command without `-update`. **Done when:** the two new goldens exist and the `BindingView` golden gains `shape`.
2. **Server creates reader bindings.** In `internal/serve/bindings.go:167-180`, remove the reader refusal and set `Shape` on the binding that `buildServedBinding` returns. In `internal/relevo/served.go`'s `ServedView`, set `Shape: b.Shape`. **Done when:** a create with actor `reviewer` returns 201 with `shape: "reader"`. Update the row at `serve_test.go:797` to that expectation.
3. **Server: the round runs in a scratch worktree.** No new code should be needed, because `relevo.Send` with `Defer:true` followed by admit already reaches `queue.go:58`. Verify it with a serve test: start a reader round, run admit, and assert that the spawn's `Dir` is `rt.Store.ScratchWorktreePath(name,1)`, not `b.CWD`. Then close with `closeRound`/`finishRound` and assert the scratch is gone and `ClosedRound==1`. If admit or the spawn does not go through the scratch, **halt and report** rather than patching around it.
4. **Server: artifact routes.** Add `handleRoundArtifacts` and `handleRoundArtifact` in a new file `internal/serve/artifacts.go`, using `relevo.RoundArtifacts`, `relevo.ReadArtifact` and `bindingRole(b)`/`OutputFile` (export a `relevo.BindingActor` if the actor name isn't reachable). Register both routes in `routes.go:54-72`, advertise `FeatureReaders` at `routes.go:108`, add both routes to `registeredRoutes` in `internal/serve/contract_test.go:73`, and regenerate with `go test ./internal/serve -run Contract -update`. **Done when:** `TestContractRoutes*` and the WhoAmI features test at `serve_test.go:351` pass with `readers` appended.
5. **Client transport.** Add `RoundArtifacts` and `RoundArtifact` to `internal/remote/client/rounds.go`, following the `RoundFile` pattern at lines 157-175, and add both to the `RemoteClient` interface and the fakes. Test with the existing `httptest` TLS helpers in `internal/remote/client/helpers_test.go`. **Done when:** listing decodes, a nested rel round-trips, and a 404 surfaces as `*client.HTTPError`.
6. **Client add.** In `internal/relevo/add.go:130-137`:
   - When the client registry says the actor is a reader, call `WhoAmI` and refuse only if `FeatureReaders` is missing.
   - Apply the reader-only refusals (`--gate`, `--regate`) that the local path applies at lines 146-155 before calling `addRemote`.
   - In `remote_add.go`, set `b.Shape` from `view.Shape` (empty means writer).

   Tests go in the `remote_test.go` style with `fakeRemote`: one against a server without the feature, where no `CreateBinding` call is made, and one against a server with it, where the binding is saved with `Shape==reader`.
7. **Client catch-up for readers.** Create `internal/relevo/remote_artifacts.go`. In the fetch half (no lock), for `b.Shape==reader` skip the `report` and `diff` kinds and fetch the artifacts instead. Keep `log` and `stream`.
   - Validate every rel: reject empty, absolute, backslash and any `..` element. On a bad rel, set Abort and log.
   - If the listed total exceeds `rt.Policy.ArtifactMaxBytes()`, write nothing and mark the fetch so the apply half halts with `artifactCapReason`.
   - Download each file into a temp file beside its final path.
   - If `list.Output` is not in the list, set `ReportMissing`.

   The apply half renames every temp file into `rt.Store.ArtifactDir(name,n,actor)`, with the output going to `reportPathFor(rt,b)`. `applyCatchUpReport` (`remote_catchup.go:140`) must pass `reportPathFor(rt,b)`, not `ReportPath`. Carry `view.ReportOutcome` through to `queueReport` as the fallback outcome from seam 3. Extend `release()` to remove the artifact temp files. **Done when:** a `fakeRemote` test closes a remote reader round and then (a) the files exist under `NNN-reviewer/`, (b) the report entry's `Path` is the output path, its `Outcome` is `view.ReportOutcome`, and no diff entry exists, (c) one daemon `Tick` seals the directory (`RoundFiles` still lists it after the files leave disk), and (d) a `..` rel writes nothing.
8. **Plan file.** Save this plan to `docs/plans/2026-09-28-remote-readers.md` as the last step. This is the user rule that plans ship with their implementation.

Iterate with a focused command:
`go test ./internal/remote/... ./internal/serve ./internal/relevo -run 'Contract|Reader|Remote|Artifact|WhoAmI|CreateBinding'`
Fix every failure before the next run. Run `make check` once at the end, with `/home/fuad/go/bin` on PATH. If any package moved coverage, run `sh scripts/check-coverage.sh --write`, and never lower a baseline to get green.

## Tests to add or change (names say what they pin)

- **`internal/remote`:** contract cases for `ArtifactFile`, `ArtifactList`, and `BindingView` with `shape`.
- **`internal/serve`:**
  - A reader create is accepted and records `Shape==reader`; this replaces the refusal row.
  - A reader round's process runs in the scratch worktree, which is removed at close.
  - Artifact list and download for a closed reader round, including a nested rel.
  - 404 for an open round, a writer binding, an unlisted or `..` rel, and another owner.
  - The artifacts are still served after the round is sealed.
  - WhoAmI advertises `readers`.
  - The routes golden is updated.
- **`internal/remote/client`:** the list and download round trip.
- **`internal/relevo`:**
  - A remote reader add is refused without the feature and makes no create call.
  - A remote reader add records the reader shape.
  - Remote reader catch-up installs the artifacts and the output report.
  - Remote reader catch-up uses the view's outcome.
  - Remote reader catch-up refuses a traversing rel.
  - Remote reader catch-up over the cap halts and writes nothing.
  - A remote reader round seals on the next tick.
- **E2E:** `internal/e2e/reader_test.go` is unchanged. A remote e2e would need a served harness and is out of scope; don't add one.

All tests stay pure: in-process serve over `httptest` TLS or `fakeRemote`. No `cmd/relevo` test runs a harness or touches the network.

## Deleted behaviour (closed list)

1. The server's refusal of reader actors (`internal/serve/bindings.go:175-179`) and the test row expecting it (`serve_test.go:797`).
2. The client's unconditional refusal of `--server` readers (`internal/relevo/add.go:130-137`). It becomes conditional on the missing feature.

## What the report must include

- The feature-token name as shipped, and the exact routes.
- How the remote reader's outcome is carried, and which tail fields (`halted_at`, `changed_paths`, `commands_run`, `not_done`) are not.
- Whether step 3 needed any server code or passed as is.
- Output-label handling when the client and server labels differ.
- Where the client catch-up code lives, and the before/after line counts of `remotefetch.go` and `remote_catchup.go`.
- Whether the coverage baseline was regenerated, and for which packages.
- `git diff --stat`, both test commands, and the `make check` result.
