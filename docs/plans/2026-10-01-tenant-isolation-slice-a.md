# #204 slice A — isolation config, wire, doctor, and the inert seam

**Status:** implemented on `relevo/iso-a`, one commit.
**Spec:** `docs/specs/2026-10-01-tenant-isolation-design.md` §3, §8, §10.
**Issue:** #204.

## The one rule

Slice A can run only `none`. A configured `user`/`container` is refused at
startup, before any side effect, and the doctor fails those two modes with the
same sentence. Starting and warning is rejected: §10 says a half-done switch
fails closed, never silently unisolated. Real prerequisite checks (root, unix
users, podman, image) stay in B/C.

## Config

`serve.isolation`: `"none" | "user" | "container"`; `""`/absent is `"none"`.
`serve.isolation_image`: string, required when `isolation` is `"container"`,
accepted but unused in the other modes. The keys are additive with no schema
bump, and `jsonshape` picks them up so an old server warns "unknown key" and
runs `none`.

- `internal/policy/policy.go:ServePolicy` — `Isolation string`,
  `IsolationImage string`. The field is the literal enum, not `isolate.Mode`:
  `policy` must not import `isolate` (isolate imports spawn, and spawn imports
  policy), so the cycle is avoided by keeping the string.
- `internal/policy/policy.go:ServeIsolation` / `ServeIsolationImage` — raw
  accessors; a nil serve block reads "".
- `internal/policy/validate.go:validateThresholds` — an unknown mode is refused
  naming `serve.isolation`, and `container` without an image naming
  `serve.isolation_image`; both wrap `ErrBadPolicy`.

## Resolve, refuse, log

- `internal/isolate/isolate.go:Parse` normalizes: `""`/`"none"` → none; `user`
  and `container` parse; anything else is refused naming `serve.isolation`.
- `internal/isolate/isolate.go:Mode.Available` — only none is available. The
  sentence `serve.isolation=<mode>: not available in this build (only "none"
  can run)` is what the command refuses with and the doctor prints.
- `internal/isolate/isolate.go:Wrap` — returns a boundary `Runner`. For a mode
  this build cannot run it returns that mode's `Available` error, and the
  boundary it returns also refuses at `Start` (defence in depth). For `none`,
  `boundary.Start` hands the base the identical `spawn.ProcSpec` (no field
  added, dropped or reordered); `Alive`/`ExitCode`/`Kill`/`Rusage` delegate.
  `internal/isolate/isolate.go:translate` is identity for `none`.
- `cmd/relevo/serve.go:cmdServeRun` resolves and refuses through
  `cmd/relevo/serve_isolation.go:resolveIsolation` immediately after
  `loadServeConfig` and before `installation.Load`/TLS/the listener. The
  refusal is `fail(codeNotAvailable, …)` naming `serve.isolation=<mode>`.
- The startup line (`cmd/relevo/serve.go:cmdServeRun`) is now
  `builders cap=<n> scopes=<text> isolation=<mode>`.

Nothing else changes a spawned process: the four spawn sites are untouched and
the session reaper stays unwrapped (B's job).

## Wire

- `internal/remote/proto.go:BuildersView` gains `isolation` (`string`,
  `omitempty`) and `image` (`string`, `omitempty`).
- `internal/remote/proto.go:FeatureIsolation = "isolation"` joins the tokens.
- `internal/serve/routes.go:handleWhoAmI` fills the view through
  `internal/serve/admin.go:isolationView` and advertises the token;
  `internal/serve/admin.go:AdminStatus` fills the same view, so `serve status`
  carries it.
- `internal/serve/serve.go:New` normalizes an empty mode to `none`, so every
  server emits `"isolation":"none"`; an absent key means an old server ("none
  (server predates isolation)"). Additive only.

For `serve status`, `cmd/relevo/serve.go:serveAdminConfigWithPolicy` stamps the
mode and image through `withIsolation`, so a refused `user`/`container` server
still reports what the admin asked for.

## Doctor

`internal/doctor/serve.go:ServeChecks` gains a raw `isolation string` param and
one `serve isolation` row (`serveIsolationCheck`), after `serve clients`, only
where the serve checks run. `serveClientCount` extracts the active-client
count, shared by both rows.

| Value | Active clients | Severity | Detail | Fix |
| --- | --- | --- | --- | --- |
| none | ≤ 1 | OK | `none` | — |
| none | ≥ 2 | Warn | names the count and the shared unix user | none |
| user / container | any | Fail | `Available`'s sentence | `relevo config set policy.serve '{"isolation":"none"}'` |
| unknown | any | Fail | `serve.isolation: <parse error>` | same |
| `serve.clients` unreadable/unparseable | — | Fail | `serve.clients unreadable` | fix the row |

Each case is pinned by `internal/doctor/serve_test.go:TestServeChecksIsolation`;
`none` OK/Warn also keep the existing `serveClientsCheck` behaviour.

## Tests

- `internal/isolate/isolate_test.go` — `TestParse`,
  `TestAvailable`, `TestWrapRefusesUnavailableModes`,
  `TestWrapNonePassesSpecThrough`.
- `internal/policy/policy_test.go:TestServeIsolationValidation` — both
  refusals name the key and wrap `ErrBadPolicy`.
- `internal/serve/serve_test.go:TestWhoAmIIsolation`,
  `internal/serve/serve_test.go:TestServedRunnerPassesSpecThrough` (the
  byte-identity test through `setupTestEnv`'s wrapped runner),
  `internal/serve/admin_test.go:TestStatusDocumentCarriesIsolation`.
- `cmd/relevo/serve_test.go:TestServeRunRefusesUnavailableIsolation` — the
  refusal at the command, with a bounded wait so a regression fails on timeout
  rather than hanging.

## Goldens

Four goldens moved, each only gaining the isolation fields:

- `internal/remote/testdata/contract/proto-BuildersView.golden` — `"isolation"`,
  `"image"`.
- `internal/remote/testdata/contract/proto-WhoAmI.golden` — the same two lines,
  through the nested `runners` view.
- `internal/serve/testdata/contract/status-document.golden` — the same two.
- `cmd/relevo/testdata/contract/serve-status.golden` — `"isolation": "none"`.

No help-json or config golden moved.

## Mutation checks

Each was applied, the named test failed, and the mutation was restored:

1. `translate` drops `Dir` → `TestWrapNonePassesSpecThrough` (internal/isolate).
2. `handleWhoAmI` stops setting `who.Builders.Isolation` →
   `TestWhoAmIIsolation` (internal/serve).
3. The isolation row treats `user` as clean → `TestServeChecksIsolation`
   (internal/doctor).
4. `cmdServeRun` skips the `Wrap` refusal →
   `TestServeRunRefusesUnavailableIsolation` (cmd/relevo).

## Verification

- Focused: `go test ./internal/isolate ./internal/policy ./internal/remote ./internal/doctor`
  and `go test -race ./internal/serve ./cmd/relevo` — all pass.
- `make check` — pass. `internal/isolate` reported "not in baseline (new
  package?)"; `testdata/coverage-baseline.txt` untouched.
- `make e2e` untouched.
