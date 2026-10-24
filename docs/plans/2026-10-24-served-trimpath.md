# Plan: served rounds honour the repos trimpath opt-out (#1353)

## Behaviour

Carry the client's effective trimpath setting on the wire (route 1 in the
issue), the way the gate command already travels. The client resolves it from
its own config at create time; the server stores it on the served binding; the
served round's environment reads the stored value instead of asking a served
runtime whose `Config` is nil and which therefore always reads the default.

Absence is the default. A create that carries no trimpath value behaves
exactly as today: trimmed. Only an explicit opt-out produces untrimmed builds.

## Cases

1. Local rounds: unchanged. No stored value exists, so the existing
   `RepoSettingsFor` path answers as today.
2. Served binding created with the opt-out: every round's Go processes start
   without `-trimpath`.
3. Served binding created without the opt-out (unset or on): rounds start with
   `GOFLAGS=-trimpath`, as today.
4. Old client (sends no field) against a new server: trimmed. This is the
   specified backward-compatible default.
5. New client against an old server (ignores the field): trimmed. Safe
   degradation to the intended default; no refusal, unlike a dropped subdir or
   label which would misplace or mislabel the round.
6. Chain members: each stored member binding carries the value, so member
   rounds behave like a lone served binding's rounds.
7. Non-Go trees and a host `GOFLAGS` that already names `-trimpath`: unchanged
   (the existing `isGoTree` / `hasTrimpath` guards keep owning those).
8. Out of scope by seed: served *check* (gate-command) processes. They are
   prepared without a binding (`internal/relevo/gate_proc.go` ~line 34) and
   keep today's default. Name this boundary in the report; do not thread a
   binding into the check path in this round.

## Seams

- Wire: `internal/remote/proto.go` `CreateBindingRequest` (~lines 112-155,
  additive-field precedent `Gate`/`Regate`/`Author`/`Subdir`); chain create
  `internal/remote/chain.go` `CreateChainRequest` (~lines 44-58, `Subdir` at
  line 52 is the placement precedent). Recommended: a top-level additive field
  on both, mirroring `Subdir`, so `internal/chain.Settings` stays untouched;
  the field must distinguish absent from opt-out, with absent meaning trimmed.
- Client send: `internal/relevo/remote_add.go` ~line 239 (`repoSet` already
  computed; `createReq` at ~line 249) and
  `internal/relevo/chain_server_start.go` ~line 101 (`repoSet` already
  computed; `chainServerRequest` at ~line 338).
- Server store: `internal/serve/bindings.go` `buildServedBinding` (~lines
  245-316; `Serve` facts at ~lines 308-314) and, for chains,
  `internal/relevo/chain_served.go` `servedFacts` (~lines 76-95),
  `ServedChainCreate` (~lines 208-229) and `servedChainMember` (~lines 264-306;
  `Serve` facts at ~lines 290-296). Recommended home: `ServeFacts`
  (`internal/store/binding.go` ~lines 187-199), the served-only struct, per the
  `AuthorName`/`AuthorEmail`/`RepoID` precedent.
- Environment (the single GOFLAGS seam): `internal/relevo/envprep.go`
  `goFlagsEnv` (~lines 43-59), reached for rounds via `prepareProc`
  (`internal/relevo/procprep.go` ~line 49) from `startRound`
  (`internal/relevo/headless.go` ~lines 323-331), whose `procRequest` already
  carries the binding. When the request carries a binding with a stored value,
  that value decides; otherwise `RepoSettingsFor` answers as today.
- Additive pins: `internal/remote/contract_test.go`
  `TestGateFieldsAreAdditive` (~lines 553-572); goldens under
  `internal/remote/testdata/contract/` stay byte-identical via `omitempty`.

## Version / format decision

Bump nothing. `remote.Version` stays 1: additive `omitempty` wire fields never
bump it (every field cited above carries that comment). No new `Feature`
token: no side must refuse, because a dropped value degrades to the intended
default, trimmed (contrast `FeatureSubdir`/`FeatureLabels`, where a drop
misplaces the round). No `store.Binding.Format` change: an `omitempty` JSON
field on `ServeFacts` is ignored by old readers (the `AuthorName`/`Subdir`/
`Shape` precedent); `Format` refusal covers only unknown `Format` values
(`internal/store/binding.go` ~lines 203-205).

## Steps

1. Wire field: add the additive trimpath field to both create requests;
   deliverable is zero-value structs marshaling without the key (extend the
   additive contract test); know it worked when `go test ./internal/remote/`
   passes with goldens untouched.
2. Client send: populate the field from the already-computed `repoSet` on both
   create paths, sending nothing when unset; deliverable is create payloads
   carrying the opt-out only when the repo sets it; know it worked when the new
   `internal/relevo` unit tests plus `go test ./internal/relevo/` pass.
3. Server store: persist the wire value onto the served binding (lone creates
   and every chain member); deliverable is the stored value visible on `Load`;
   know it worked when new serve-fake tests in the
   `TestCreateBindingStoresAuthor` pattern (`internal/serve/admit_test.go`
   ~line 113) pass.
4. Round environment: resolve the effective value from the stored binding value
   when present, else `RepoSettingsFor`; deliverable is opt-out served rounds
   starting without `-trimpath` and all other cases byte-identical; know it
   worked when the new envprep/goflags tests pass and flipping the stored value
   back fails the new test (mutation pin).
5. Gate: run `make check-affected` and compare `git diff --stat` against this
   plan's scope; `make check` (with `-race` and coverage) stays the full local
   run owned by the CI gate, not this round.

Test rule (repo law): no `cmd/relevo` subcommand test that spawns a harness or
reaches the network; pin the rule as a pure function in `internal/relevo` plus
the existing serve fakes.

## Deleted behaviour

(None. No behaviour, flag, field, or test is removed by this round.)

1. (none)

## Report must include

- The wire field names chosen, the storage location, and the absent-means-trimmed rule.
- The version/format decision restated with the rule that demanded it.
- Each new or extended test name, the mutation-flip result, and the
  `make check-affected` output.
- `git diff --stat` against this plan's declared scope.
- The served-checks boundary restated as deliberately untouched.

## Note from the MasterMind: parallel rounds share files

Two other chains are editing `internal/serve/bindings.go` (`pickServedTier`) and `internal/relevo/chain_served.go` (`servedActorPick`, `PickServedCandidateFor` in served.go). Leave those functions alone; this round's edits in those files stay inside `buildServedBinding`, `servedFacts`, `ServedChainCreate` and `servedChainMember`.
