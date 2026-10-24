# Plan: relevo-serve picks up `--local` config writes without restart (#1351 part 1)

## Behaviour

- Every write to the machine-local config layer moves the same version counter
  the reloaders already watch, so the next tick sees it.
- `relevo-serve`'s next `Refresh` after a `--local` write reloads the merged
  config (candidates, policy, registry, accounts) exactly as it does for a
  shared write today.
- The client daemon needs no separate change: `cmdDaemonRun`'s watcher
  (`relevo.ConfigWatcher.Refresh`) gates its full `Load` on the same
  `Version()` comparison over the same store type, so it inherits the fix.
- A tick with no write anywhere performs no full config load: the version
  check stays a single cheap query and an unchanged version returns the
  current copy untouched.
- Sync exports nothing new: a local-layer write still records no revision, no
  per-item write stamp, and no shared-body change, so no peer ever receives a
  machine-local change as a shared one.
- A test pins the two reload properties: a `--local` write is picked up by the
  next `Refresh`, and an unchanged tick does not reload.

## Cases

- `config set --local actors…` (and `config edit --local`, any `PutLocal`
  section): version moves, next `Refresh` (serve and daemon) reloads.
- Shared `Put`/`PutDoc` that also implies local patches: still exactly one
  visible reload on the next tick (a double-incremented counter is a counter
  moved, not two changes; do not add logic to suppress it).
- `ConfigLocalDelete` / null-patch clears: version moves like any local write.
- Unchanged tick on both refreshers: no `Load`, no warning, no client rebuild.
- Failed `Load` after a version move: keep serving the last good copy and warn
  once per distinct error (existing behaviour, unchanged; the new tests must
  not break it).
- Sync round-trip after a `--local` write: peer's merged config is unchanged
  and no new revision or write stamp exists for the local change.

## Seams

- `internal/db/config_local.go:76-93` — `Tx.ConfigLocalPut`,
  `Tx.ConfigLocalDelete`, and the "does not bump config_meta.version" comment.
  Bump via the existing `Tx.bumpConfigVersion` (`internal/db/config.go:68-73`).
- `internal/db/config.go:75-95` — `ConfigVersion` / `configVersion`: the
  counter both refreshers read; unchanged.
- `internal/config/sectionread.go:45` — `Store.Version`: unchanged, now moves
  on local writes because the store is `LocalOrSelf`-bound to the file holding
  both the local rows and the counter.
- `internal/serve/configreload.go:62-96` — `ConfigRefresher.Refresh`: unchanged
  logic; the version gate at lines 76-78 is what picks the local write up.
- `internal/relevo/reload.go:19-27,108-125` — `ConfigSource` interface and
  `ConfigWatcher.Refresh`: unchanged; daemon source is `rt.Config.As(...)`
  (`cmd/relevo/daemon.go:261-267`), the same store type as serve's
  (`cmd/relevo/serve_reload.go` via `config.Open(d).As(...)`).
- `internal/config/localmigrate.go:101-116` — `Store.PutLocal`: unchanged; it
  writes only `config_local` rows and calls no `record`/`stampWrites`, which is
  the property that keeps local writes out of sync.
- `internal/config/writestamp.go:20-44` — `stampWrites`: unchanged; per-item
  sync bookkeeping, deliberately not the reload signal.
- Tests live beside the code: `internal/db/config_local_test.go`,
  `internal/config/localfields_test.go` (merged-view helpers) plus one
  refresher-level pin in `internal/serve/configreload_test.go` (fake source
  pattern in `internal/relevo/reload_test.go:36-44` shows the counting-fake
  shape; CLI tests must stay pure per repo rule, so no `cmd/relevo` test that
  spawns a harness or touches the network).

## Decision

- Bump `config_meta.version` on a local-layer write; do not compare a second
  stamp. `config_write_stamp` (`internal/db/config_write_stamp.go`,
  `internal/db/migrations/028_config_write_stamp.sql`) records one stamp per
  syncable item for "newest wins" sync resolution, is written only for
  shared-doc changes (never by `PutLocal`), and is absent for
  unstamped/imported/peer-arrived writes — it is per-item sync bookkeeping, not
  a global change counter, so it cannot drive a reload gate and must stay
  untouched.
- The bump is safe for sync because the counter itself is never exported: sync
  exchanges shared bodies plus per-item stamps, and `PutLocal` writes neither
  (no `writeSection`, no `record`), so a bumped version with no stamp and no
  revision is invisible to peers by construction.
- The counter and the local rows live in the same machine-local file behind
  the same `LocalOrSelf` binding, so the bump commits atomically with the
  write it advertises; no cross-file ordering hazard.

## Steps

1. Bump the version in `Tx.ConfigLocalPut` and `Tx.ConfigLocalDelete`
   (`internal/db/config_local.go`), reusing `bumpConfigVersion`, and update
   the now-false doc comment — deliverable: the two-line behaviour change;
   know it worked when `internal/db` tests pass and a local put visibly moves
   `ConfigVersion`.
2. Add a db-level pin that a local put/delete moves `ConfigVersion` while a
   no-op read does not — deliverable: extended `internal/db/config_local_test.go`;
   know it worked when the named test fails with the step-1 change reverted
   (mutation check) and passes with it.
3. Add a store-level pin that `PutLocal` moves `Store.Version()` and the next
   `Load` returns the merged override — deliverable: a test beside the local
   layer tests in `internal/config`; know it worked when it fails against the
   pre-fix `ConfigLocalPut` and passes with the fix.
4. Add a refresher-level pin that a version move triggers exactly one reload
   carrying the merged value and an unchanged tick performs no `Load` —
   deliverable: a test in `internal/serve/configreload_test.go` using the
   existing fake-source shape; know it worked when removing the production
   change leaves the "picks up local write" assertion red and the "no load on
   unchanged tick" assertion green throughout.
5. Confirm the daemon inherits the fix by reading (not changing)
   `internal/relevo/reload.go:108-125` against the new counter behaviour and
   running its existing suite — deliverable: a one-paragraph statement in the
   report naming the shared `Version()` gate; know it worked when
   `internal/relevo` tests pass unmodified.
6. Confirm sync still exports nothing for a `--local` write (no revision, no
   write stamp, peer's merged doc unchanged) with the existing sync-suite
   coverage — deliverable: green `internal/config` sync tests plus the
   `never_syncs` test; know it worked when they pass without any new
   exclusion or baseline edit.
7. Run the builder gate `make check-affected` (Go 1.x per repo toolchain;
   `RELEVO_REQUIRE_COVERAGE=1` only if touching the coverage comparison
   locally) — deliverable: gate output green; know it worked when the command
   exits 0 with no new linter/file-size/comment exclusions.

## Deleted behaviour

1. The `ConfigLocalPut` "does not bump `config_meta.version`" rule
   (`internal/db/config_local.go:76-78`) is deleted; nothing else is removed.
   No flag, command, file, table, column, revision kind, or sync message is
   deleted, and no test is deleted or skipped — existing tests that assert the
   old no-bump behaviour, if any, are updated to the new contract rather than
   removed.

## Report must include

- Which files changed and the one-sentence behaviour of each change.
- The mutation check outcomes for the new pins (red without the fix, green
  with it).
- The daemon-inherits statement naming the shared `Version()` gate.
- The sync-safety statement: what was verified to stay unexported (no
  revision, no stamp, no shared-body write).
- The exact `make check-affected` result (exit code and any warnings), and
  whether `make check` / `make e2e` were run or left to CI.
- Anything deliberately left: full-suite `make check`, race/coverage CI legs.

## Note from the MasterMind: check why the old rule existed

Before step 1, find why `ConfigLocalPut` was written NOT to bump the version (`git log -S "does not bump" -- internal/db/config_local.go`, and the callers of `ConfigVersion`). In particular, `config_revision` rows carry a `version`, and `config rollback` / `config history` may assume the version advances only with a recorded revision. If a bump without a revision breaks any of them (a gap in the sequence, a rollback target that no longer matches, a test asserting revision.version == current version), handle it within this round and pin it with a test, or halt and report if it needs a design decision. Name what you found in the report.
