# S4B builder-round plan — sync verbs, enable path, turn-off (spec §6 S4, issue #473)

Base: spec tip `0ba5e47d` (spec + slice plans), built after S4A lands.
Plan only: no file changed by the planning rounds; the builder round
changes code per §3.

Split from `docs/plans/2026-10-03-turso-sync-s4.md`: this half owns the
surface (verbs, enable path, turn-off). It consumes S4A's origin-gate
counter, preflight refusals, and seed-copy writer as named seams — if S4A
has not landed on the builder's base, the builder creates only the minimal
flagged fallback those names need and reports the substitution (same
pattern as the S2/S3 plans).

## 0. What S4B builds (spec §4, §5, §6 S4, §7)

- **`db sync` dispatcher + verbs** (`cmd/relevo`, beside
  `db_query.go:85-94` `cmdDB`): `enable/disable/status/push/pull` with
  registry parity. The tree has exactly one `db` subcommand today
  (`query`, `cmd/relevo/db_query.go:22-31`, `:85-94`; usage
  `cmd/relevo/main.go:71-74`, dispatch `:252-253`) — the builder creates
  the whole `sync` sub-dispatcher, its flag sets, and its owner-routed
  opens; nothing is extended in place. Read-only open fallback without
  starting the owner is `openDBQuery` (`cmd/relevo/db_query.go:260-300`);
  CLI errors render through `report`/`fail` (`cmd/relevo/main.go:133-174`).
- **Token plumbing (new surface owned by this slice)**: `config secret`
  allows only `typesafe|client.key` (`cmd/relevo/config.go:31-33`,
  `:73-74` dispatch). `turso.token` plus `--token-stdin`/env intake is new,
  stored via the existing `Tx.SecretPut/SecretDelete` seam
  (`internal/db/config.go:126-139`); `--token-stdin` beats env; neither
  present is a refusal; the value never appears in logs, error strings, or
  any payload (`TestTokenNeverLeavesMachine`, spec `:132-135`).
- **`SyncClient` seam + fake** (only if S3's interface is absent at build
  time: `Push`/`Pull`/`Stats`/`Checkpoint` per spec `:192-194`, fake
  recording calls). Constructor takes `TursoSyncDbConfig` with verified
  `BootstrapIfEmpty` semantics (`tursogo@v0.8.1/driver_sync.go:63`,
  `:142-143` — pointer, default true, nil means bootstrap; false skips it
  and the caller must pull explicitly, `:273`, `:315`).
- **Enable path** (S4A preflight → seed matrix → mark enabled → open
  handle). Seed matrix, fake-backed: empty cloud → first `Push` is the
  seed; existing local DB → refusal prints the documented Turso upload
  path and enable proceeds only after the asserted seed copy (S4A writer);
  new machine → `BootstrapIfEmpty=true` open + `Pull`. Never
  false-then-forgets-the-pull.
- **Turn-off flow, exact §5 order (spec `:139-148`)**: (1) one final push
  attempt, best-effort and bounded — failure does not block the rest;
  (2) mark disabled in the local `sync` section; (3) delete `turso.token`
  from local `secret`; (4) close the sync handle. Local files keep full
  history and stay servable; cloud never deleted.

## 1. Tests (all fake-backed, no network; one mutation per test; CLI-shape
tests as pure functions in `internal/relevo`, never harness-spawning or
network `cmd/relevo` tests per CLAUDE.md `:92-95`)

`TestEnableRefusesAlreadyEnabled`, `TestEnableRefusesBadTokenSource`,
`TestEmptyCloudFirstPushIsSeed`, `TestExistingDBUploadThenEnable`,
`TestNewMachineBootstrapPull`, `TestTokenNeverLeavesMachine`,
`TestDisableKeepsLocalUsable`, `TestDisableFinalPushFailureStillDisables`,
`TestDisableDeletesToken`, `TestReEnableIsFreshEnable` (re-enable runs full
preflight + seed decision, never resumes). Mutations: skip the
already-enabled read → its test fails; log the token source on refusal →
token test fails; set `BootstrapIfEmpty=false` without the explicit pull →
bootstrap test fails; delete the token before (not after) the disable mark
→ delete test fails; resume instead of fresh-enable → re-enable test fails.

## 2. Ordered steps (deliverable + how it is known to work)

1. **`SyncClient` seam + fake** (only if absent): — worked when the
   seed-matrix tests drive the fake and assert open-config + call order.
2. **Dispatcher + verbs** (beside `db_query.go:85-94`): — worked when `db`
   parity/help tests and each verb's pure-path test pass with no network
   and no real-home reads.
3. **Token plumbing** (`--token-stdin`/env → local `secret`): — worked
   when set/delete round-trips and the fixture token appears in no log,
   error, CDC payload, or shared-file byte.
4. **Enable path** (preflight → matrix → mark → handle): — worked when the
   three seed-matrix tests pass against the fake.
5. **Turn-off path** (§5 four steps in order, bounded final push): —
   worked when the four disable tests pass, each broken by exactly one
   mutation.
6. **Verification + report**: focused `go test ./internal/db/ ./cmd/relevo/
   -run 'TestEnable|TestDisable|TestToken|TestBootstrap|TestDBSync'`
   green, then `make check` green; report quotes commands/outputs, commits
   + base SHA, mutations, and S5 as left-out.

## 3. What is deleted / untouched

Deleted at runtime only: the §5 turn-off removes exactly the local
`turso.token` row and the enabled mark — never local history, never cloud
data. Nothing in schema, CLI surface (beyond additions), or stored history
is removed. Untouched: S4A counter/preflight/writer internals (consumed),
S5 `:sync` view, statusline tokens (§3, S2/S3), `BackfillOriginOnce`,
migration 015 rule, any config-section sync.
