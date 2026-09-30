# Plan — #718: upgrading past #664 with a relay-era `client.key` label

## What I reproduced, and what changes

In this worktree (two throwaway tests, created, run, deleted — tree is clean):

- `remote.ParsePrivate` on a PEM block of type `RELAY ED25519 PRIVATE KEY` returns `ErrKeyType` (`invalid key type; ed25519 required`).
- `cmdDaemon(["--preflight"])` on a temp `XDG_STATE_HOME` whose `relevo.db` holds a legacy-labelled `secret.client.key` plus a non-empty `servers` section prints `relevo: invalid key type; ed25519 required` and returns exit 1 — the exact zen failure.

`--preflight`'s load is `newRuntimePeek` (`cmd/relevo/wire.go:269`) → `loadConfigReadOnly` (`:294`, `db.OpenReadOnly` at `internal/db/config.go:188`, `mode=ro`, no migration) → `buildRuntime` (`:342`) → `newRemoteClient` (`:382`) → `relevo.NewRemoteClient` (`internal/relevo/remoteclient.go:19`; the key is parsed only when `servers` is non-empty) → `remote.ParsePrivate`. The refusal is upstream of every repair path, so acceptance in the reader is the fix, and convergence is a write that only the daemon can do.

Behaviour and cases after the work:

1. **Read** — `ParsePrivate` accepts exactly two PEM types and nothing else: the current `RELEVO ED25519 PRIVATE KEY` and the pre-rename `RELAY ED25519 PRIVATE KEY`. Both yield the identical `Keypair` and `IDOf` id. Any other type is still `ErrKeyType`; a wrong-length block is still `ErrKeyFormat`; non-PEM is still `ErrKeyFormat`; trailing data after the block is still ignored. `MarshalPrivate` still writes only the current label.
2. **Write** — every write of `client.key` through `config.Store.PutSecret` stores the re-marshalled bytes, so any accepted input lands under the current label (a current-labelled but hand-wrapped key is also canonicalised; the stored value of a `MarshalPrivate`-produced key is byte-identical).
3. **Boot** — one daemon startup pass rewrites a stored legacy-labelled row once, through `config.Store`, recording a config revision; a current-labelled row is left byte-identical; a missing key is a no-op. `--preflight`/`--check` stay read-only and leave the label as it found it.
4. **No schema migration and no new doctor row** (decision 3, 4): preflight cannot migrate before it reads, and a data-only migration would add a schema version for nothing.

## Seams

| Seam | Location |
|---|---|
| Parse change | `internal/remote/key.go:21` (const), `:100-120` `ParsePrivate` (type check `:108-110`), doc comment; add the pre-rename const and a `IsLegacyPrivatePEM([]byte) bool` predicate beside it |
| Parse tests | `internal/remote/key_test.go:84-104` (reject table), new accept test; literal lines need `// name-guard: legacy` (`scripts/check-name.sh:19`) |
| Store write | `internal/config/config.go:470-488` `PutSecret` |
| Pass function | `internal/config/clientkey.go` (new, ~25 lines) — `func (s *Store) NormalizeClientKey() (bool, error)` |
| Pass test | `internal/config/clientkey_test.go` (new), helpers `openStore` (`internal/config/helpers_test.go:17`), direct-row seed idiom as in `seedNamelessCandidates` (`:108-117`), `WithClock`/`revAt`/`revChanges` (`:173-184`) |
| Boot call | `cmd/relevo/daemon.go`, the pass block `:240-315`, inserted after the origin-backfill block (after `:315`), gated and logged like its neighbours |
| Boot test | `cmd/relevo/main_test.go:1434-1481` (existing `TestDaemonPreflight*`), new test after them |
| Plan doc | `docs/plans/2026-09-30-key-label-upgrade.md` (new) |

Untouched on purpose: `cmd/relevo/wire.go`, `internal/relevo/remoteclient.go`, `internal/config/import.go`, `internal/db/migrations/**`.

## Ordered steps

1. **Widen the reader** — `internal/remote/key.go`: add `legacyPEMTypePrivate` (with `// name-guard: legacy` on the literal line) and `IsLegacyPrivatePEM`, accept both types in `ParsePrivate`, and make the doc comment say both accepted types and *why* the old one is read (a key stored before the rename was never rewritten, and preflight — which cannot write — parses it). Worked when: `go build ./... && go test ./internal/remote/ -count=1` is green.
2. **Pin the parse change** — `internal/remote/key_test.go`: `TestParsePrivateAcceptsLegacyType` (legacy-labelled block of a generated key → same `Keypair`, same `IDOf`, and `MarshalPrivate` of the result is current-labelled); add a `RELEVO ED25519 PUBLIC KEY` near-miss row to `TestParsePrivateRejectsWrongType` while keeping its two rows. Worked when: the new test is red before step 1 and green after, and `git grep -n 'name-guard: legacy' internal/remote` shows the marked literal lines.
3. **Normalise the write** — `internal/config/config.go:470-488`: `PutSecret` parses the client key, re-marshals it with `remote.MarshalPrivate`, and stores the re-marshalled bytes; comment says why (one label survives a re-store). Worked when: `go test ./internal/config/ -count=1` is green and a legacy PEM handed to `PutSecret` comes back out current-labelled (step 4's test).
4. **Add the pass function and its tests** — new `internal/config/clientkey.go`: `NormalizeClientKey` reads `SecretClientKey`; absent → `(false, nil)`; `!remote.IsLegacyPrivatePEM` → `(false, nil)` and no revision; else `s.As("daemon", "client key PEM label updated").PutSecret(SecretClientKey, raw)` and `(true, nil)`. Tests in `internal/config/clientkey_test.go`: (a) legacy raw row seeded through `t.SecretPut` converges to the current label with the same key bytes and id, and records one revision with source `daemon` and change `secret.client.key set`; (b) a `PutSecret`-seeded current row is byte-identical and records no revision; (c) no key is a no-op with no error; (d) `PutSecret` given a legacy PEM stores the current label. Worked when: `go test ./internal/config/ -run 'NormalizeClientKey|PutSecret' -count=1` is green.
5. **Call it at daemon start** — `cmd/relevo/daemon.go`, in the `rt.DB != nil` pass block after `:315`: `if wrote, err := rt.Config.NormalizeClientKey(); err != nil { slog.Warn("relevo daemon: client key label not updated", "err", err) } else if wrote { slog.Info("relevo daemon: client key PEM label updated") }`; the `rt.DB != nil` guard is the daemon's existing "may write this database" flag (`daemonRuntimeHandle`, `:454`) and keeps a newer schema read-only. Worked when: `go build ./... && go vet ./cmd/relevo/` is clean and `go test ./cmd/relevo/ -run TestDaemonPreflight -count=1` is green.
6. **Pin the boot path** — `cmd/relevo/main_test.go`: `TestDaemonPreflightAcceptsAStoredLegacyClientKey`; it sets its own `XDG_CONFIG_HOME`/`XDG_STATE_HOME` under `t.TempDir()`, opens `store.New(root).DB()`, seeds a non-empty `servers` section and a raw legacy-labelled `secret.client.key` through `t.SecretPut` (not `PutSecret`, which now normalises), closes, then `cmdDaemon([]string{"--preflight"})` must return nil with stdout starting `ok `, and a re-open must show the stored bytes still legacy (preflight writes nothing). It spawns no harness and touches no network — only client construction. Worked when: red before step 1 (exit 1 with `invalid key type`), green after step 5, and `go test ./cmd/relevo/ -run TestDaemonPreflight -count=1` passes.
7. **Mutation pin** — restore the original condition in `ParsePrivate` (`block.Type != pemTypePrivate` only) and confirm `TestParsePrivateAcceptsLegacyType` fails, then restore the fix. Worked when: the named test fails on the reverted condition and passes again.
8. **Ship the plan doc, then the full check** — copy the round's plan to `docs/plans/2026-09-30-key-label-upgrade.md` and commit it with the code; run `make check`, then `make e2e` (CI runs it and this touches daemon startup). Worked when: `make check` prints its guards green, `check-coverage` does not report a package more than a point below baseline, and `gofmt -l .` prints nothing.

## Closed list of what is deleted

None. This round adds; no file, test, exclusion or allow-list entry is deleted, and none is weakened. Explicitly: the acceptance removed by `docs/plans/2026-09-29-drop-relay-migration.md`'s closed-list item 15 (`TestParsePrivateAcceptsLegacyType`) is deliberately restored here by owner decision 1, and nothing else from that list is reversed. If any existing test fails for a reason other than the new expectations, halt and report instead of editing it.

## The mutation pin

Break `ParsePrivate`'s type check back to `block.Type != pemTypePrivate`; `TestParsePrivateAcceptsLegacyType` (`internal/remote/key_test.go`) must fail. Restore.

## Risks — what I checked for the old label elsewhere

- `git grep -n 'RELAY ED25519'` over tracked files: only the three `docs/plans/*` files (name-guard-exempt history). No live code, script, unit or testdata carries it; `internal/legacy` is gone.
- Printed output: the private PEM is never printed — `printClientKey`/`config server key`/`config server list`/`doctor` print only `client.EnrollLine` (`ed25519 <pub>`); `ensureClientKey` returns the raw stored bytes but no caller writes them out.
- Other install paths: serve stores TLS material under `serve.tls.key`/`serve.tls.cert` (different PEM type) and client *public* keys; nothing else writes a private-key PEM. `bugreport` only redacts PEM-looking text.
- **One third writer, left as designed:** `internal/config/import.go:142` (`commitImport`) still stores the imported `client.key` file bytes raw, so a machine whose `~/.config/relevo/client.key` is imported during a daemon start lands legacy-labelled; the daemon pass in the same start rewrites it (import runs in `newRuntimeWith`, the pass after), and a CLI-only import converges at the next daemon start. Worth one line in the report for the owner; not a third change per decision 2.
- Accepted reading of decision 2: the pass uses no kv once-row and no database backup (unlike `DedupeMirrorOnce`/`CompressHistoryOnce`/`BackfillOriginOnce`, whose work is expensive and destructive). The trigger *is* the stored label, so the pass is self-limiting, cannot go stale across a downgrade, and costs one secret read per start; if the owner wants the once-row idiom instead, it is a one-line guard, not a redesign.
- Revision honesty: the pass writes through `PutSecret`, so the log gets one row — source `daemon`, message `client key PEM label updated`, path `secret.client.key`, op `set`, snapshot of the doc at the unchanged config version. Secrets are never in snapshots, so a later rollback cannot restore the old label (as today).
- Out of repo: zen (and any machine already in the broken state) needs this build; with it, `daemon --preflight` passes and the daemon's first start under the new binary rewrites the row. No pre-install manual key surgery is required any more.

## What the report must include

- `git diff --stat` and the commit, matching the declared scope: `internal/remote/key.go`, `internal/remote/key_test.go`, `internal/config/config.go`, `internal/config/clientkey.go` (new), `internal/config/clientkey_test.go` (new), `cmd/relevo/daemon.go`, `cmd/relevo/main_test.go`, `docs/plans/2026-09-30-key-label-upgrade.md`.
- The focused command per step and the final `make check` result (plus `make e2e` if run), and every error fixed before the next run.
- The before/after reproduction: the preflight error text and exit code before, nil and `ok ` after; the stored label before/after for the pass test.
- The mutation check: the exact reverted condition, the failing test's name, and the restore.
- Tests added, one line each on what it pins, and that no test was deleted, weakened or retargeted, and no `.golangci.yml` / `check-comments.allow` / `check-filesize.allow` entry was added.
- Coverage for `internal/remote`, `internal/config`, `cmd/relevo` against `testdata/coverage-baseline.txt`; a baseline is regenerated only for deletion drift, never lowered.
- `sh scripts/check-name.sh` green, naming the new `// name-guard: legacy` lines.
- The risks above restated for the MasterMind: the `import.go:142` raw write and its convergence, and the out-of-repo install on zen.

## Halt conditions

- An existing test outside the added cases fails and can only be fixed by editing or deleting it.
- `PutSecret`'s re-marshal moves a stored value that an existing test pins as non-canonical beyond the new expectations.
- The pass cannot be written through `config.Store` on the daemon's shared handle without a second database open.
