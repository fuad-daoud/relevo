# Plan: custom chains W4 (remote)

Base: origin/main `aea20c17`. This plan has 5 rounds, 1 new commit stack per round. The builder never amends or rebases. Round 5 saves the whole plan to `docs/plans/2026-10-02-custom-chains-w4.md`.

## Preamble

**Order.** R1 → R2 → R3, then R4 → R5.
- R1 is wire types and client methods. Everything else depends on it.
- R2 is the server accepting and validating a workflow, and R3 is the client sending one. R3 needs R2's feature token. R3 does not need R4/R5.
- R4 (server check and gate routes) is independent of R2/R3 and needs only R1. It can run in parallel with them.
- R5 needs R1 and R4.
- Servers are redeployed between chains only (spec section 10), so every client path is gated on a server feature.

**Out of scope.** Do not touch:
- fork logic (W3);
- `internal/ui` (W5);
- #864, meaning the pre-W2 chain-row conversion on a server and the `chain_served.go` view failing on a gone member (branch `relevo/pre-w2-chain-row`).

R2 and R3 touch `chain_served.go` and `chain_pull.go` near that fix. The builder rebases onto main before starting and keeps its edits to the lines named below.

**Spec gaps, each with a PROPOSED resolution.**

1. **Wire form of the definition.**
   - `internal/remote` does not import `internal/workflow`. So `CreateChainRequest.Workflow` and the two new `ChainView` fields are `json.RawMessage`.
   - The client sends the definition after `workflow.WithParams` has resolved it. `file:` seeds are already inlined by `ResolveWorkflow`/`readSeedFile` (`workflow_resolve.go:69`), so the definition is self-contained.
   - `shipped:` seeds stay as names. The server resolves them from its own binary.
   - `Settings` is omitted when `Workflow` is present. `settings` is still read when `Workflow` is absent (the old client).
2. **Client binding ids.**
   - Today `ClientBindingIDs` is keyed by the legacy parts (`validateChainBindingIDs`, `serve/chains.go:~125`).
   - A custom workflow has no legacy parts and the client has no authoritative member plan. The server owns actors and shapes, and the keeper writer depends on the shape.
   - Proposal: add `ClientActorIDs map[string]string`, keyed by actor name. The client mints one id per `workflow.UsedActors(def)` entry. The server maps actor → member.
   - `ClientBindingIDs` is kept, and is read only when `Workflow` is absent.
3. **Client-side validation for `--server`.**
   - The client cannot validate actors against its own registry (the server may know actors it does not).
   - Proposal: the client parses the definition, applies params and checks the structure with `workflow.Validate` over an `Env` whose `Actors` come from `RemoteClient.Actor` probes per used actor (the shape only). Rule failures that need actor outputs are the server's to report, as 400 `invalid`.
   - A `unknown_actor` 400 maps to `ErrUnknownRole`, as now (`chainServerCreateError`).
   - The builder may instead skip the client validation and rely on the server's answer, if the `Actor` probe turns out to cost more than it saves. The report says which.
4. **Mirror members.** Built from `view.Members` (authoritative), not from a client-side plan.
5. **Where the server keeps a check run.**
   - Proposal: an additive `omitempty` field on the served binding's stored record (a `CheckRun`: id, command, step, pid, started, result, log path). The check log is a file under the binding's round-file area.
   - One check at a time per binding. A second POST while one runs is 409 `check_running`.
   - A POST that repeats an id the server holds is idempotent.
   - The builder checks the BindingFormat bump rule (memory: bind format) before adding the field. If the field needs a bump, halt and report rather than bump silently.
6. **Gate route versus check route.**
   - After R5, a placed writer's check command travels in the check request. So a `--gate` change on resume needs no server state.
   - The route is still required (item 4), and it keeps a served binding's own `Gate`/`Regate` coherent for chains started before W4 (their served binding carries `Gate = <check command>` per spec 12.5).
   - Proposal: `POST /v1/bindings/{name}/gate` body `{gate *string, regate *int}`, behind feature `check` (no third token).
   - `chain --resume --gate` lifts the refusal only when the server has `check`. It calls the route only when the placed binding's stored `Gate` is non-empty.
7. **`ChainResumeRequest` on a server chain** is unchanged. `--param` on a server-chain resume stays refused as today. A workflow-aware server resume is a later slice.
8. **Compatibility matrix.** Each of these is a named test:
   - old client → new server (R2);
   - new client → old server (R3, R5);
   - new client → new server (R3, R5).

**Commands, for every round.**
- Focused: `go test ./internal/<pkg> -run '<TestNames>'`, fixing every reported error before the next run.
- Once at the end: `make check`, then `sh scripts/check-comments.sh` run directly.
- No coverage baseline is lowered. If a round moves code between packages, regenerate with `sh scripts/check-coverage.sh --write` and say so in the report.
- `cmd/relevo` tests must not spawn a harness or reach the network. Test rules as pure functions in `internal/relevo`. No R1-R5 step needs a `cmd/relevo` test. If a builder adds one, it says so in the report.
- Comments say why, with no history, no issue or round numbers, and no spec section numbers. Functions ≤70 lines, non-test files ≤600 lines, with no new exclusions.

**Every round's report includes:**
- the `git diff --stat` against this plan's declared scope;
- each named mutation and the test that failed under it;
- the two commands' results;
- any halt, and any gap resolved differently from the proposal.

---

=== ROUND 1: wire types, feature tokens and client methods ===

**Seams**
- `internal/remote/chain.go`:
  - `CreateChainRequest` (line ~35) gains `Workflow json.RawMessage` and `ClientActorIDs map[string]string`. Both are `omitempty`.
  - `ChainView` (line ~90) gains `Workflow` and `State`, both `json.RawMessage` `omitempty`.
  - Add `FeatureWorkflow = "workflow"`.
- `internal/remote/proto.go`, beside `FeatureChainMember`/`FeatureIsolation` (lines ~448-461). New types:
  - `FeatureCheck = "check"`.
  - `CreateCheckRequest {ID, Command, Step}`.
  - `CheckView {ID, Command, Step, Result, ExitCode, DurationMS, Note, LogTail, LogTruncated}`.
  - `SetGateRequest {Gate *string, Regate *int}`.
  - New `Code` `CodeCheckRunning` (409).
- `internal/remote/client/` (next to `chains.go`): methods `CreateCheck`, `GetCheck` and `SetGate`. Path scheme: `POST /v1/bindings/{n}/checks`, `GET /v1/bindings/{n}/checks/{id}`, `POST /v1/bindings/{n}/gate`.
- `internal/relevo/runtime.go:418` (`RemoteClient`): add the three methods.
- `internal/relevo/remote_test.go` (`fakeRemote`, ~line 319): add the three methods, recording calls.

**Steps**
1. Add the types, constants and field changes. Done when `go build ./...` passes.
2. Regenerate the goldens with `go test ./internal/remote -run Contract -update`. Extend `filledCreateChainRequest`/`filledChainView` (`contract_test.go:326,386`) so the new fields are non-zero. Done when the goldens `proto-CreateChainRequest`, `proto-ChainView` and the new ones for the three check/gate types exist, and a rerun without `-update` is green.
3. Implement the client methods via the existing signed-request helper that `ChainDone` uses. A 404 from `check`/`gate` on an old server surfaces as an `HTTPError`, unchanged.
4. Update `fakeRemote` and any other implementer of `RemoteClient` (the compiler lists them).

**Tests** (`internal/remote`, `internal/remote/client`)
- A golden contract test per new type.
- `TestCreateChainRequestWorkflowOmittedWhenEmpty`: the old-client JSON shape is byte-identical to the pre-change golden.
- A client httptest test per new method: path, verb, body, and decoding of a 409 `check_running`.

**Mutations**
- Drop `omitempty` on `Workflow`: the old-shape test fails.
- Swap the check path segment: the client path test fails.
- Return nil on a 409 in `CreateCheck`: the 409-decoding test fails.

**Done when:** goldens committed, both commands green, and no `internal/serve` or `internal/relevo` behaviour has changed.

---

=== ROUND 2: server accepts, validates and reports a workflow ===

**Seams**
- `internal/serve/chains.go`:
  - `parseCreateChainRequest` (~line 35): when `Workflow` is non-empty, skip `validateChainSettings` and replace the part-keyed `validateChainBindingIDs` with a check that `ClientActorIDs` has non-empty, bounded keys.
  - When `Workflow` is absent, behaviour is exactly today's. Cap the definition's byte size with a named constant, and the id counts.
  - `chainMaxNameLen` (the `-plan` assumption) is replaced for workflow creates by the workflow's own cap, `chainNameCap` in `internal/relevo`.
- `internal/serve/chains_view.go`:
  - `servedChainRequestOf` (line ~34) carries `Workflow` and `ClientActorIDs`.
  - `chainRequestMatches` (line ~66) also compares the stored definition's canonical JSON against the request's (the retry rule).
  - `view.Settings != req.Settings` is skipped when the request carries a workflow.
- `internal/relevo/chain_served.go`:
  - `ServedChainRequest` (line 19) gains `Workflow []byte` and `ClientActorIDs`.
  - `ServedChainPreflight` (line 103) decodes the definition (`workflow` parse package), calls `chainValidateWorkflow` (`chain_start_wf.go:189`) with the server's own actors and `workflow.ShippedSeeds()` (this is item 2), derives members through `chainMemberNames`/`chainWriterKeeper`/`chainWorkflowMembers`, and picks per member via `servedActorPick`. Today it uses `chainMembersFor`+`servedChainDefinition`, so `chainValidateWorkflow` is skipped.
  - The settings-only path stays and is routed through `servedChainDefinition` (`chain_served_wf.go:19`) unchanged. That is the one-release mapping.
  - Reject `file:` seeds in a served definition, with 400 `invalid` ("file seeds must be inlined by the client").
- `internal/relevo/chain_served_wf.go`: `ServedChainCreate` already stores `plan.def`. Link ids are written per member from `ClientActorIDs` (via `servedMemberLink`, `chain_served.go:237`).
- `ServedChainView` (`chain_served.go:309`): fill `Workflow`/`State` from `c.WorkflowJSON`/`c.StateJSON`, only when the row has them.
- `internal/serve/routes.go:125`: add `FeatureWorkflow` to the advertised list.

**Steps**
1. Add the request fields, the parse and validation rules, and the preflight branch. Done when a hand-built `ServedChainRequest` with a custom definition yields a plan whose members match `chainMemberNames`.
2. Wire the view fields and the feature token. Done when a `GET /v1/chains/{name}` for a workflow chain returns `workflow` and `state`.
3. Update the retry comparison, then run the whole `internal/serve` and `internal/relevo` chain tests unchanged. Done when old tests pass without edits.

**Tests** (pure, `internal/relevo` and `internal/serve`; no harness, no network)
- Old-client/new-server: `TestServedCreateSettingsOnlyMapsToDefault` posts a `settings`-only create and expects the same stored definition as before, with no `workflow` required in the request.
- `TestServedCreateWorkflowValidatesAgainstServerActors`: an unknown actor gives 400 `unknown_actor`.
- A reader-as-writer or bad edge gives 400 `invalid`, with the first problem's text.
- `TestServedCreateResolvesShippedSeedFromServerBinary`: a bad `shipped:` name is refused, a good one runs.
- `TestServedCreateRefusesFileSeed`.
- `TestServedCreateRetryComparesDefinition`: the same definition with different JSON key order returns 200. A changed param returns 409.
- `TestServedChainViewCarriesWorkflowAndState`.
- `TestWhoAmIAdvertisesWorkflow`.

**Mutations**
- Skip `chainValidateWorkflow` in the preflight: the unknown-actor test fails.
- Accept `file:`: the refusal test fails.
- Compare raw bytes instead of canonical JSON in `chainRequestMatches`: the key-order test fails.
- Drop the `Settings` skip: the retry test fails.

**Done when:** both commands green, the existing settings-create tests untouched and passing, and the report lists new files and line counts (the 600-line cap on `chain_served.go`, which is 385 lines now).

---

=== ROUND 3: client sends the workflow; mirror stops rebuilding it ===

**Seams**
- `internal/relevo/chain_server_start.go`:
  - `chainServerResolve` (line ~72): when `opts.Workflow` is set, resolve it as `chainResolveWorkflowStart` does (`ResolveWorkflow`, `chainParamsFor`, `workflow.WithParams`; `chain_start_wf.go:49-110`). Skip `chainFreeNames`/`chainMembersFor` as the plan of record. Keep `chainServerBranchFree` and `chainServerMasterMind`.
  - After `WhoAmI`, a custom definition on a server without `FeatureWorkflow` is refused: "server %s cannot run workflow %s (missing the \"workflow\" feature)". The error names the server and the feature, by the `chainFeatureList` shape.
  - The shipped default on a server without the feature keeps sending `settings`, which is the old path (new-client/old-server).
  - Add a field to `chainServerPlan` for the resolved definition.
- `chainServerRequest` (~line 266): send `Workflow` (and `ClientActorIDs`) when the server has the feature and the definition is resolved. Otherwise send `Settings`.
- `chainServerBindingIDs` (~line 288): mint ids per used actor for workflow creates.
- `chainMirrorRow`/`chainMirrorMembers`/`chainMirrorMember` (~lines 320-420): when the view carries `Workflow`/`State`, store them verbatim on the row. Build members from `view.Members` (part, name, actor, shape from the view's binding).
- `internal/relevo/chain_pull.go`:
  - `chainRowFromView` (line ~525) uses `v.Workflow`/`v.State` when present.
  - `workflow.FromLegacy` stays only as the fallback for a view without them (an old server, a pre-W4 chain). Keep `chainViewLegacy`/`chainViewBuilderActor` for that fallback only.
- `ChainOptions.Workflow` reaches `chainStartServer`: today `ChainStart` (`chain_start.go:149`) dispatches to the server path before the default naming. Make sure the name `default` is not silently applied twice.

**Steps**
1. Resolve and validate the definition in `chainServerResolve`, with the feature refusal. Done when a custom workflow against a `fakeRemote` lacking `workflow` is refused before any `CreateChain` call.
2. Build the request (workflow versus settings), mint the actor ids, and send. Done when a `fakeRemote` capture shows a `workflow` field and no `settings`.
3. Build the mirror from the view. Done when the stored row's `WorkflowJSON`/`StateJSON` equal the view's, byte for byte.
4. Switch `chainRowFromView` to the view's fields, with the fallback. Done when an old-server view still converts, as it does today.

**Tests** (`internal/relevo`, using `fakeRemote`; no spawn)
- `TestServerChainRefusesCustomWorkflowWithoutFeature`: it names the server and the feature, and `CreateChain` is not called.
- `TestServerChainDefaultOnOldServerSendsSettings` (new-client/old-server).
- `TestServerChainSendsWorkflowWhenFeaturePresent` (new-client/new-server).
- `TestMirrorTakesWorkflowAndStateFromView`, which does not call `FromLegacy`. Use a state that `FromLegacy` cannot produce (a custom step id).
- `TestChainRowFromViewFallsBackToLegacy`.
- `TestServerChainMembersComeFromView`.

**Mutations**
- Skip the feature check: the refusal test fails.
- Send `Settings` always: the workflow-sent test fails.
- Keep calling `FromLegacy` unconditionally: the custom-state test fails.
- Drop the fallback: the old-server view test fails.

**Done when:** both commands are green, and the diff touches no fork code and no `internal/ui`.

---

=== ROUND 4: server check and gate routes ===

**Seams**
- `internal/serve/routes.go:60-79`: register `POST /v1/bindings/{name}/checks`, `GET /v1/bindings/{name}/checks/{id}` and `POST /v1/bindings/{name}/gate`. Advertise `FeatureCheck` in the list at line 125.
- A new `internal/serve/checks.go` (new file, ≤600 lines), with handlers that follow `handleStop`/`handleResume`:
  - They load the binding through `loadBinding` (the tenant guard, so one tenant never reaches another's binding).
  - They refuse a reader binding (400 `invalid`) and a binding with no worktree.
  - The POST body is bounded and the command length is capped by a named constant.
  - A POST with the same `id` as the stored run returns it (idempotent). A different id while one runs gives 409 `check_running`.
- `internal/relevo/` (new `served_check.go`):
  - Pure functions: `ServedCheckStart(ctx, rt, name, req)`, `ServedCheckGet`, and `ServedSetGate`.
  - Reuse `startGateProc`/`advanceGateProc` (`gate_proc.go`) with a `gateProcSpec` on the binding's worktree. The unit name comes from `scopeUnitNameFor(scopeGate, owner, name+"-check", …)` as `chainCheckSpec` does at `chain_check.go:387`. The timeout comes from policy.
  - Persist the run on the binding (gap 5). The daemon tick that advances gate runs also advances this one. The tick is the same one `gate.go:62` uses.
  - `LogTail` is capped (64 KiB, a named constant), with `LogTruncated` set when it cuts.
  - `ServedSetGate` mutates the served binding's `Gate`/`Regate` under the lock, and refuses a reader.

**Steps**
1. Add the store field and the state transitions, with unit-level pure tests. Done when the transitions (start, running, pass, fail, timeout, lost-to-restart re-run) are covered through `fake` runners.
2. Add the three handlers and the routes. Done when an httptest call sequence of POST, a daemon tick and GET returns `pass`.
3. Advertise `FeatureCheck`.

**Tests** (`internal/serve`, `internal/relevo`; use the fake runner, never a real harness or network)
- `TestServedCheckPassFailTimeout`.
- `TestServedCheckIdempotentPost`.
- `TestServedCheckSecondRunConflicts`.
- `TestServedCheckRefusesReaderAndOtherTenant`.
- `TestServedCheckLogTailCapped`.
- `TestServedCheckRerunsOnceAfterDaemonRestart`.
- `TestServedSetGateUpdatesBindingAndRefusesReader`.
- `TestOldClientIgnoresCheckFeature`: `whoami` still decodes with an unknown extra token, so an old client is unaffected.
- `TestWhoAmIAdvertisesCheck`.

**Mutations**
- Drop the tenant check: the other-tenant test fails.
- Return a fresh run on a repeated id: the idempotency test fails.
- Remove the tail cap: the cap test fails.
- Skip the single re-run: the restart test fails.

**Done when:** both commands green, and the server route list in the server README/docs is updated only if a doc already lists routes (no new docs file).

---

=== ROUND 5: client uses the check route; `--resume --gate` on a placed writer; saves the plan ===

**Seams**
- `internal/relevo/chain_engine.go` `chainFlowCheck` (~line 237): for a placed writer on a server with `FeatureCheck`, POST the check (client-minted id, the rendered command, the step) through `rt.Remote.CreateCheck`. Record the run id on `next.Awaiting.Run`. The pulled-record answer (`chainFlowPullCheck`, line 300) stays only as the fallback for a server without `check`, for chains started before W4.
- `internal/relevo/chain_check.go` `chainTickPlacedCheck` (line 351): poll `GetCheck` for the awaited run. On a result, feed `check_closed` exactly as the local check does (`chainAdvance`, with `Result`, `Log`, and `RepeatRed` from `chainRepeatRedCheck`). Seal the returned log tail through the same step `chainCheckSealLog` uses, so a red's signature and the repeat-red rule work. An unreachable server leaves the chain awaiting (no halt); a 404 for the check halts with the reason.
- `internal/relevo/chain_start_wf.go`:
  - `chainRefuseTwoCheckCommandsOnPlacedWriter` (line ~252) is replaced by a feature probe. A placed chain with a `check` step refuses to start when the server lacks `check`: "chain %s places a writer on server %s, which lacks the \"check\" feature".
  - The refusal is made while `chainRemotePreflight` already holds `WhoAmI`; reuse it rather than calling twice.
  - Delete `chainSingleCheckCommand` and the single-command restriction if nothing else uses them. The grep lists callers first.
- `internal/relevo/chain_resume.go` `resumeRemoteGateRefusal` (line 144): replace the refusal. With `FeatureCheck`, allow the resume; if the placed binding's stored `Gate` is non-empty, call `SetGate` before the engine runs. Without the feature, keep a refusal whose text now names the server and the missing `check` feature (drop "W4 adds it"). It needs `rt.Remote.WhoAmI`.
- `chain_resume_wf.go:27` calls it unchanged in shape.
- A spec edit: `docs/specs/2026-10-01-custom-chains-design.md` sections 6.3 and 12.5 are updated to the shipped behaviour, with W4 removed as pending. The spec edits ship with this PR (the plans-ship-with-code rule).
- **Last step:** save this whole plan to `docs/plans/2026-10-02-custom-chains-w4.md` and commit it with this round.

**Steps**
1. Start the placed check through the route, with the fallback. Done when a `fakeRemote` records the POST and the chain advances on a polled `pass`/`fail`.
2. Replace the start refusal with the feature probe. Done when a two-command placed workflow now starts against a `check` server.
3. Lift the resume refusal as above, then the spec edit, then save the plan. Done when `chain --resume --gate` on a placed chain succeeds against a `check` server and is refused with the feature named against one without it.

**Tests** (`internal/relevo` only; no spawn, no network, and no `cmd/relevo` test)
- `TestPlacedCheckRunsViaRoute`.
- `TestPlacedCheckRedFeedsRepeatRed`.
- `TestPlacedCheckUnreachableKeepsAwaiting`.
- `TestPlacedCheckFallsBackToPulledRecordWithoutFeature` (new-client/old-server).
- `TestPlacedStartRefusesWhenServerLacksCheck`: the refusal names the server and the feature.
- `TestPlacedStartAllowsTwoCommandsWithCheck`.
- `TestResumeGateOnPlacedWriterCallsSetGate`: it calls only when the stored gate is non-empty.
- `TestResumeGateOnPlacedWriterWithoutCheckFeatureRefused`.
- `TestOldPlacedChainStillAnswersFromPulledGate`.

**Mutations**
- Always use the pulled record: the route test fails.
- Halt on an unreachable poll: the awaiting test fails.
- Skip the feature probe at start: the refusal test fails.
- Call `SetGate` unconditionally: the empty-gate test fails.
- Keep the old resume refusal: the resume test fails.

**Done when:**
- both commands are green;
- `git diff --stat` matches the declared scope: `internal/relevo`, the spec and the plan file. It names no `internal/ui` and no fork code;
- the report states the final server-and-client compatibility matrix (three rows, each pointing at its test);
- the report confirms no coverage baseline was lowered.

---

## Deletes

The only deletions are in R5, as a closed list:
1. `chainRefuseTwoCheckCommandsOnPlacedWriter` and its call, replaced by the feature probe.
2. `chainSingleCheckCommand` and `chainCheckCommands`, only if no other caller remains. Grep before deleting, and keep either if one is used elsewhere.
3. The "W4 adds it" wording in `resumeRemoteGateRefusal`.
4. The `FromLegacy` use in `chainRowFromView` as the primary path. It remains only as the old-server fallback.
