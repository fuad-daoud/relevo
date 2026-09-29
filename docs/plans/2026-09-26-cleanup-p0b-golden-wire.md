# Cleanup P0b -- pin the remote wire protocol and `serve status --json`

Round of the codebase cleanup (spec: `docs/specs/2026-09-25-codebase-cleanup-design.md`,
§3 contracts C5 and C9). **Test-only round: no production code changes.**

## 0. Rules for this round

- If a step is impossible as written, or the code contradicts this plan, **stop and
  report** -- do not improvise, and never change production code to make a test
  possible.
- Only files named in §2 may be created or changed.
- New comments: *why* only where the code cannot say it; no issue numbers, no `§`,
  no history, no restating the code.
- `cmd/relevo` tests must not spawn a harness or reach the network (the package's
  TestMain isolates HOME/XDG). Servers in tests listen on loopback via
  `httptest` only, as the existing serve tests do.
- A parallel round deletes the **text** form of `relevo serve status` and
  `serve.RenderAdminStatus`. Pin only the JSON form (`serve.StatusDocument`).

## 1. System overview

A relevo client and `relevo serve` are installed separately (the server runs on
contabo), so a new client must talk to an old server and the reverse. The
refactor will move the remote code between packages. This round freezes the wire:
the JSON shape of every request and response type, the signed-request scheme,
the route table, and the `serve status --json` document that an outside
program (`servers/contabo/burst/internal/observe/observe.go`) parses.

## 2. File structure

```
internal/remote/contract_test.go              NEW  proto JSON shapes, auth canonical form + headers
internal/remote/testdata/contract/*.golden    NEW
internal/serve/contract_test.go               NEW  route table, StatusDocument, ErrorBody on a bad route
internal/serve/testdata/contract/*.golden     NEW
cmd/relevo/serve_contract_test.go             NEW  `relevo serve status --json` end to end
cmd/relevo/testdata/contract/serve-status.golden  NEW
docs/plans/2026-09-26-cleanup-p0b-golden-wire.md  NEW  a copy of this plan (last step)
```

A parallel round creates `cmd/relevo/contract_test.go` with helpers named
`normalize` and `assertGolden`. To avoid a duplicate declaration when both
merge, name this round's helpers in `cmd/relevo` `wireNormalize` and
`wireAssertGolden`, and its flag `updateWire` (`-update-wire`). In
`internal/remote` and `internal/serve` use `normalize`, `assertGolden` and a
package-level `update` flag, after checking with grep that neither package's
tests declare one already.

## 3. Data structures

Per package, test-local:
- `assertGolden(t, name string, got []byte)` -- write
  `testdata/contract/<name>.golden` under `-update`, else compare and fail naming
  the file and the regenerate command.
- `normalize(b []byte) []byte` -- only for values the fixture cannot fix
  (temp-dir paths, listener ports). Timestamps and nonces are fixed inputs here,
  not normalised.

Fixture values (fixed across runs): time `2026-01-02T03:04:05Z`; nonce
`"nonce-fixed-0001"`; an ed25519 `remote.Keypair` built from a fixed 32-byte seed
(read `internal/remote/key.go` for the constructor; if only a random generator
exists, build the keypair from `ed25519.NewKeyFromSeed` the way key.go's
parser would, inside the test).

## 4. Contracts to pin

| ID | What | How | Golden |
|---|---|---|---|
| C9a | JSON of every exported request/response type in `internal/remote/proto.go` (at least `FileRange`, `WhoAmI`, `GitIdentity`, `CreateBindingRequest`, `TagRef`, `BindingView`, `QueueView`, `DiffStat`, `LiveView`, `BuildersView`, `UnavailableRequest`, `AvailableRequest`, `AvailableResponse`, `CandidateView`, `CandidatesResponse`, `ErrorBody`) | build each with **every field set to a non-zero value** (nested structs and slices too), `json.MarshalIndent(v, "", "  ")`. Also decode each golden back into a zero value and re-marshal: bytes must match (round-trip). The list of types is explicit in the test; add a test that fails when `proto.go` gains an exported struct type the list lacks (parse the file with `go/parser` and compare names) | `proto-<type>.golden` |
| C9b | signed requests | `remote.Canonical` (auth.go:69) for GET with empty body and POST with a body; `remote.Sign` (auth.go:93) headers for the fixed keypair/time/nonce (header names and values, sorted); and `remote.Verify` (auth.go:150) accepting exactly that request and rejecting it with one header removed | `canonical-get.golden`, `canonical-post.golden`, `sign-headers.golden` |
| C9c | route table | for each `METHOD path` registered in `internal/serve/routes.go:60-78` (write the list literally in the test, with sample path values), send an **unsigned** request to the server's handler via `httptest`; assert it is refused by authentication (record the status and `ErrorBody`) and not by the not-found handler. One request to `/v1/nope` must reach the not-found handler. Build the server the way `internal/serve/testsupport_test.go` does | `routes.golden` (one line per route: method, path, status, error code) |
| C5a | `serve.StatusDocument` (internal/serve/admin.go:197) | hand-built `[]OwnerStatus` (two owners, one with an active and one with a done binding) and a `remote.BuildersView` with non-zero fields | `status-document.golden` |
| C5b | `relevo serve status --json --state <dir>` | seed a serve state with `seedServeOwnerState` (cmd/relevo/serve_test.go:66) for two owners; `run([]string{"serve","status","--json","--state",dir})` via `captureOutput` (main_test.go:15) | `serve-status.golden` |

Postconditions: every Contract test passes with `-count=2` and without `-update`.

## 5. Pseudocode

```
C9a: for each T in protoTypes:
        v := filledT()
        b := MarshalIndent(v)
        assertGolden("proto-"+T, b)
        var back T; Unmarshal(b, &back); require MarshalIndent(back) == b
     names := exportedStructs(parse("proto.go")); require names ⊆ protoTypes
C9b: c := Canonical(method, target, fixedTime, fixedNonce, sha256(body)); assertGolden
     h := Sign(fixedKey, method, target, sha256(body), fixedTime, fixedNonce); assertGolden(sorted h)
     require Verify(h, ...) == client id; require Verify(h minus one header) errors
C9c: for each route: resp := serve(unsigned request); record status + ErrorBody; assertGolden(all lines)
```

## 6. Error handling

If a type cannot be filled without unexported fields, fill what is exported and
note it. If `Sign` reads the clock or nonce itself instead of taking them as
arguments, stop and report (do not change it). If the route test cannot tell
an auth refusal from not-found by status and body alone, stop and report.

## 7. Working efficiently

- Read `internal/remote/proto.go`, `auth.go`, `key.go`, `internal/serve/routes.go`,
  `admin.go:147-230`, `testsupport_test.go` and `cmd/relevo/serve_test.go:39-110`
  in one batch. Do not search further for these.
- One edit per new file.
- Focused loop: `go test ./internal/remote ./internal/serve -run Contract -count=1`
  and `go test ./cmd/relevo -run ServeContract -count=1`; `-update` / `-update-wire`
  to write goldens, then rerun without.
- Full check once at the end: `make check`.

## 8. Ordered steps

1. **C9a** proto shapes + round-trip + completeness check. Verify with the focused command.
2. **C9b** canonical form, headers, verify (depends on nothing). Verify.
3. **C9c** route table (depends on nothing). Verify.
4. **C5a** StatusDocument, **C5b** the CLI end to end. Verify both.
5. **Mutation check.** Temporarily rename one JSON tag in `proto.go`, change one
   header name in `auth.go`, and drop one route in `routes.go`; confirm each
   makes a named test fail; revert. Record them in the report.
6. **Full check.** `make check` passes; `git diff --stat` shows only §2 files.
7. **Save the plan.** Copy this plan to
   `docs/plans/2026-09-26-cleanup-p0b-golden-wire.md` and commit it with the tests.

Report: goldens written (file -> contract ID), anything not pinned and why, the
three mutation checks.

## Round 2

# Cleanup P0b (round 2) -- make the `serve status --json` golden machine-independent

Round 2 of `cl-golden-wire`. Round 1 (commit `148b30a`) added the wire and
`serve status --json` goldens. **Test-only round: no production code changes.**

### 0. Rules

- If a step is impossible as written, or the code contradicts this plan, stop
  and report -- do not improvise, and do not change production code.
- Only `cmd/relevo/serve_contract_test.go` and
  `cmd/relevo/testdata/contract/serve-status.golden` may change (plus the plan
  copy in step 4).
- Comments: *why* only; no issue numbers, no `§`, no history.
- `cmd/relevo` tests must not spawn a harness or reach the network.

### 1. The defect

`cmd/relevo/testdata/contract/serve-status.golden` records `"builders": {"cap": 3, ...}`.
That cap is not a fixture value: with no policy, `policy.MaxBuildersOrDefault()`
(`internal/policy/policy.go:377-385`) returns `max(1, runtime.NumCPU()-1)`.
The golden passed only because the machine that wrote it has 4 CPUs; it fails
on a laptop with 8 and on a 3-CPU CI runner.

### 2. The fix

Make the test's serve state carry an explicit `serve.max_builders` in its
policy, so the cap is a fixture value, and keep the golden's `"cap": 3`.

- Read how `cmdServeStatus` (`cmd/relevo/serve.go:~720`) builds its config:
  `adminRoot(fs)` -> `serveAdminConfig(root, d)` -> `serve.New(...)`, and where
  that path loads the policy (`pol.MaxBuildersOrDefault()` at `serve.go:~485`
  is the `serve run` path; find the equivalent on the admin path).
- Write the policy the admin path reads, with `serve.max_builders: 3`, in the
  test's temp state or config before calling `run`. Use the same mechanism the
  existing serve tests use to give a server a policy (grep `max_builders` and
  `MaxBuilders` in `cmd/relevo/*_test.go` and `internal/serve/*_test.go`).
- If the admin path offers no way to set the cap from a fixture without a
  production change, fall back to normalising: in `wireNormalize`, replace the
  `"cap": <n>` value with `"cap": "<CAP>"` and regenerate the golden. Say which
  route you took.

### 3. Verification

- `go test ./cmd/relevo -run ServeContract -count=2` passes without `-update-wire`.
- Prove machine independence: run it once with the CPU count forced low and
  once high -- `GOMAXPROCS` does not change `runtime.NumCPU`, so use
  `taskset -c 0 go test ./cmd/relevo -run ServeContract -count=1` (1 CPU) and a
  plain run; both must pass. If `taskset` is unavailable, say so.
- `make check` passes.

### 4. Steps

1. Implement §2. 2. Verify §3. 3. `git diff --stat HEAD~1` is only §0's files.
4. Append a "Round 2" section to `docs/plans/2026-09-26-cleanup-p0b-golden-wire.md`
   containing this plan, and commit everything in one commit.

Report: the route taken (fixture policy or normalisation), the two runs from §3.
