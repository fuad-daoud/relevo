# Sync step 3a: passphrase key and signed log entries

Four rounds, run as one chain. Each section below is one round prompt.

### Sync step 3a, round 1 of 4: log format 3, the remote key meta, and the canonical MAC

Spec: docs/specs/2026-10-10-sync-everything-design.md §5 "Key" and "Signing". Step 3a covers the passphrase key and signed log entries only. Shared docs and the hybrid clock are NOT part of 3a.
Threat model: someone holding the Turso token or R2 key but not the passphrase. Everything on the remote is untrusted, meta included.
This round builds the plumbing. Nothing signs or verifies yet, and no CLI verb is added.

### Deliverables

1. **Canonical MAC, pure functions.** Add a new file `internal/synclog/mac.go` with:
   - `func EntryMAC(key []byte, e Entry) []byte`, HMAC-SHA256 over a canonical encoding;
   - `func VerifyMAC(key []byte, e Entry) bool`, which uses `hmac.Equal`.

   The canonical encoding:
   - starts with the domain label `relevo-sync-entry-v1`;
   - then takes, in order, `Origin`, `Table`, `PK`, `Op`, decimal `SchemaVersion`, the body and `At`;
   - prefixes each field with its length as a big-endian uint32, so no field can run into the next.

   Two fields have a fixed form:
   - **Body:** `json.Compact` of `e.Body`, with empty for a delete. The pipe re-encodes a `json.RawMessage`, which compacts it, so the stored bytes are not the bytes the exporter held. Compacting on both sides makes signer and verifier agree. Body is the body *as stored in the log*: an R2 `$ref` object is signed as the ref, never the fetched content.
   - **At:** `At.UTC().Format(time.RFC3339Nano)`, which is exactly the string `insertEntry` stores.

   `Seq` and `Batch` are NOT covered, because the transport assigns them.
2. **Entry carries a MAC.** Add `MAC []byte` to `synclog.Entry` (`entry.go`) and to `syncworker.Entry` (`protocol.go`), with JSON name `mac` and `omitempty`. On the pipe it travels as base64, which is what encoding/json gives `[]byte`. `syncpipe/convert.go` carries it in both directions. `MemTransport` stores and returns it unchanged on `Pull`, `PullRange` and the snapshot reads, and `memfake_compact.go` keeps it on survivors.
3. **Format 3.** `logFormatVersion = 3`. The `log` table gains a nullable `mac BLOB` column, and `createStatements`, `insertEntry` and `scanEntry` carry it. At hello, an existing format-2 remote is upgraded in place (`ALTER TABLE log ADD COLUMN mac BLOB`, then meta `format` set to 3, in one transaction). Use the same `ensureLog`/`upgradeLog` discipline that took format 1 to 2, so a format-1 remote still reaches 3. A format above 3 is still refused, with the existing wording. Put any new SQL in `marks.go` or a new file if `turso.go` would pass 600 lines.
4. **Key meta verbs.** Add two worker verbs to `protocol.go`, the worker dispatch, `syncpipe/client.go` and `MemTransport`:
   - `VerbKeyMeta = "key_meta"` reads meta rows `kdf_salt` (base64), `kdf` and `key_check` (base64). It answers absent when there is no `kdf_salt`.
   - `VerbKeyInit = "key_init"` carries those three values. It runs `INSERT OR IGNORE` on each row in one transaction, then rereads and answers what the remote now holds. That way two machines racing `--new` both learn the winner's salt, and the caller compares its own key_check against the answer.
   - Expose both on `synclog.LogTransport` as `KeyMeta(ctx)` and `KeyInit(ctx, KeyMeta)`, with a `KeyMeta` struct of `Salt []byte`, `KDF string`, `Check []byte` and `Present bool`.
5. **signed_from, written by the worker.** In the append transaction (`writeBatch`), the worker inserts meta `signed_from.<origin>` = the batch's first assigned seq whose entry carries a MAC, using `INSERT OR IGNORE`. That writes it at most once, at the origin's first signed entry. `VerbReadMarks`'s answer, and `MarkState` in `transport.go`, gain a `SignedFrom map[string]int` read from those meta rows. The memfake does the same.

### Tests (names say what they pin)
- `TestEntryMACCoversEveryFieldButSeqAndBatch`: change each covered field and the MAC changes; change Seq or Batch and it does not.
- `TestEntryMACIsStableAcrossBodyWhitespace`
- `TestEntryMACSignsARefNotTheFetchedBody`
- `TestScratchRemoteMACSurvivesThePipeAndTheLog`: in `turso_scratch_test.go`, sign an entry whose body has non-compact whitespace and a non-UTC At with nanoseconds. Send it through `entriesToWire`, a JSON encode/decode, the TursoBackend append, a pull and `entriesFromWire`. `VerifyMAC` must hold on what comes back.
- `TestHelloUpgradesFormatTwoToThreeInPlace`: rows kept, mac NULL on them.
- `TestHelloUpgradesFormatOneToThree`
- `TestHelloRefusesFormatFour`
- `TestKeyInitIsInsertIfAbsent`: a second init with a different salt answers the first salt.
- `TestKeyMetaAbsentOnAFreshRemote`
- `TestAppendWritesSignedFromOnceAtTheFirstSignedEntry`: unsigned batch, then signed, then signed. signed_from is the first signed seq and never moves.
- `TestMemTransportCarriesMACThroughPullRangeAndCompaction`
- `TestConvertCarriesMACBothWays`

### Constraints
- CI has no network and no harness: use local scratch remotes and the memfake only.
- Keep `testdata/coverage-baseline.txt` (`internal/syncworker 92.2`, `internal/synclog 94.0`, `internal/syncpipe 90.0`). Never lower it.
- Style per CLAUDE.md: functions of at most 70 lines, files of at most 600, comments that say why, and no history in code.
- Gate: `GOTOOLCHAIN=go1.27.2 make check`.

### Report
`git diff --stat` against this scope, the tests above passing, and the gate output.

### Sync step 3a, round 2 of 4: the passphrase key, `relevo db sync unlock`, locked status and disable

Spec: docs/specs/2026-10-10-sync-everything-design.md §5 "Key". Round 1 added the transport surface this round uses: `LogTransport.KeyMeta`/`KeyInit`, format 3 and `EntryMAC`.
Threat model: someone holding the Turso token or R2 key but not the passphrase. Everything on the remote is untrusted.
This round signs nothing and verifies nothing; round 3 does.

### Deliverables

1. **`internal/sync/key.go`** (new file, with `key_test.go`). Add `golang.org/x/crypto`, keeping `go mod tidy` clean.
   - **KDF string.** `KDFParams{Time, MemoryKiB, Threads}` parses and formats `argon2id;t=3;m=65536;p=4`, which is the default.
   - **Bounds.** Parse refuses anything outside t 1..10, m 19456..1048576 KiB, p 1..16 with `ErrKDFParams`. The remote chooses these numbers and is untrusted, and a huge m is a memory bomb.
   - **Key.** `DeriveKey(passphrase, salt, params) []byte` is 32 bytes.
   - **Key check.** `KeyCheck(key) []byte` = HMAC-SHA256(key, `relevo-sync-key-check-v1`). The label must differ from the entry label in round 1.
   - **Secret helpers.** `SecretKey = "sync.key"` with `SetKey`, `ReadKey` and `DeleteKey`, modelled on `token.go`.
2. **Daemon verb `unlock`.** Add `wire.SyncVerbUnlock = "unlock"` in `internal/db/wire/msg.go` and a `VerbRunner` case in `internal/relevo/syncverb.go`, run guarded. The passphrase travels as the existing secret byte payload (`Run(ctx, verb, token []byte)`), never in a field that is logged. `verb` carries `New bool`.
   - **Logic.** It lives in `internal/sync` as `Unlock(ctx, local, transport, passphrase, isNew, rand io.Reader)` so it is testable against the memfake.
   - **Remote has no salt, plain unlock:** refuse with `SyncCodeNoKDF`, whose next is `relevo db sync unlock --new`.
   - **Remote has no salt, `--new`:** generate a 16-byte salt and default params, derive, then call `KeyInit`. If the answer's salt is not ours (another machine won the race), derive again with the answer's salt and params, then check.
   - **Remote already has a salt, `--new`:** behaves as plain unlock against the existing salt. It never overwrites.
   - **Check fails:** refuse with `SyncCodeWrongPassphrase` and store nothing.
   - **Check passes:** `SetKey`, and write local kv `sync.remote_kdf = 1`.
   - **Sync off:** refuse with the existing sync-off code.
3. **CLI `relevo db sync unlock [--new] [--passphrase-stdin] [--json]`.** Put it in a new file `cmd/relevo/db_sync_unlock.go` and register it in `db_sync.go`'s usage, dispatch and classify. The passphrase comes from `--passphrase-stdin`, else env `RELEVO_SYNC_PASSPHRASE`, else a TTY prompt with echo off (`golang.org/x/term` is fine; check whether it is already a dependency).
   - **Readers.** Stdin, env and TTY are package vars so tests never touch real handles. Model them on `dbSyncTokenStdin`/`dbSyncGetenv`.
   - **Empty passphrase:** refused with `codeUsage`.
   - **Typo protection.** `--new` from a TTY asks twice and refuses a mismatch.
   - **Hygiene.** A trailing newline is trimmed, and nothing echoes the passphrase into an error.
   - **Output.** It prints `unlocked` (with `--json`: `{"unlocked":true,"created":<bool>}`).
4. **Remote-has-key fact.** At every steady and join hello, the daemon calls `KeyMeta` and records local kv `sync.remote_kdf` as 1 or 0. A failure leaves the value unchanged. Status reads only local state.
5. **Status.** `db sync status`, human and `--json`, gains `key: "unlocked" | "locked" | "none"`:
   - `unlocked` when `sync.key` is present;
   - `locked` when there is no key and `sync.remote_kdf=1`, and the human line then says `locked: run relevo db sync unlock`;
   - `none` otherwise.
6. **Disable.** Delete `sync.key` and `sync.remote_kdf` beside `DeleteToken`, as a named step in `Steps`.

### Tests
- `TestDeriveKeyIsDeterministicPerSaltAndParams`
- `TestKDFParamsRefuseOutOfBounds`, for each bound in both directions.
- `TestUnlockRefusesWithoutSaltUnlessNew`
- `TestUnlockNewCreatesSaltAndStoresKey`
- `TestUnlockNewLosingTheRaceAdoptsTheWinnersSalt`: the memfake pre-seeded via a second KeyInit.
- `TestUnlockNewNeverOverwritesAnExistingSalt`
- `TestUnlockRefusesAWrongPassphraseAndStoresNothing`
- `TestUnlockReadsStdinOverEnvOverTTY`: injected readers.
- `TestUnlockNewFromTTYRefusesAMismatchedRepeat`
- `TestStatusSaysLockedWhenTheRemoteHasAKeyAndThisMachineDoesNot`
- `TestDisableDeletesTheSyncKey`

CLI tests are pure functions with injected readers. They must not spawn a worker, a harness or the network (CLAUDE.md). The unlock logic is tested in `internal/sync`.

### Constraints
- Keep the coverage baseline (`internal/sync 77.5`) and never lower it.
- Style per CLAUDE.md: functions of at most 70 lines, files of at most 600, and no history in comments.
- Gate: `GOTOOLCHAIN=go1.27.2 make check`.

### Report
The diff stat against this scope, the tests passing, and the gate output.

### Sync step 3a, round 3 of 4: sign on export, verify on import

Spec: docs/specs/2026-10-10-sync-everything-design.md §5 "Signing". Rounds 1 and 2 built `EntryMAC`/`VerifyMAC`, the `MAC` field, format 3, `MarkState.SignedFrom`, `sync.key` (`ReadKey`) and unlock.
Threat model: someone holding the Turso token or R2 key but not the passphrase. Everything on the remote is untrusted, meta included.

### Deliverables

1. **Signing on export.** `Exporter` (`internal/synclog/export.go`) takes an optional key, read from `sync.key` per attempt in `internal/sync/steady.go` and `join.go`, so an unlock while the daemon runs takes effect on the next tick.
   - **With a key:** sign every entry with `EntryMAC` after the bodies are staged and their `$ref`s substituted, just before `Append`. The MAC covers the body as it will be stored.
   - **Reconcile.** Entries that reconcile re-emits for own rows are signed at the same point. Find the site where `NewUpsert`/`NewDelete` entries are built in `reconcile.go`.
2. **Locked machine, signed origin.** A machine with no key, whose own origin has `SignedFrom[ownOrigin]` set on the remote, must NOT append. Unsigned entries at or above that seq are refused by every unlocked reader.
   - The export attempt refuses with a new trouble, `ErrLockedSignedOrigin`, worded `this machine's log is signed; run relevo db sync unlock`.
   - The outbox is kept, not truncated.
   - Status shows that trouble.
   - Import continues; a locked reader imports as before.
3. **Verification on import, unlocked machines only.** In `Importer` (`import.go`) and the snapshot path (`snapshot.go`), verify each entry BEFORE `resolveRefs`/`fetchAndApply`, on the entry exactly as pulled. The MAC covers the `$ref` form, and the existing sha256/length check stays the gate on fetched bytes. For each entry from origin O:
   - **Bad MAC** (present but `!VerifyMAC`): refuse.
   - **Missing MAC** and `seq >= signedFrom(O)`: refuse.
   - **Missing MAC** and O has no signed-from: apply as today.
   - **Valid MAC:** apply. If this machine has no local record for O yet, write local signed-from(O) = this seq in the same import transaction.

   A refusal holds origin O and latches with the reason, the same shape and path as `ErrBlobMissing`. Use a new sentinel `ErrBadSignature`, with sentences naming the origin and seq, never key material:
   - `entry <seq> from <origin> has a bad signature`
   - `entry <seq> from <origin> is unsigned but <origin> signs from <n>`
4. **The signed-from rule.** signedFrom(O) = the lower of remote `MarkState.SignedFrom[O]` and the local record. A remote that deletes or raises its meta value cannot switch checking off after this machine has seen one signed entry from O.
   - **Local record:** add a migration `internal/db/migrations/NNN_sync_signed_from.sql` (next free number) with `sync_signed_from(origin TEXT PRIMARY KEY, seq INTEGER NOT NULL)` in the LOCAL database, beside `sync_import_mark`, with accessors in `internal/db/`.
   - **Lowering only:** an existing record is lowered, never raised.
   - **Disable** clears the table, and the plan says so.
5. **Locked reader.** No verification and no local records. History applies as today.
6. **Compaction.** `compact.go` and the worker compaction keep survivors' MACs byte-identical, and the MAC plays no part in what is dropped. Assert this in a test; no code change is expected.

### Tests
- `TestExportSignsEveryEntryWhenUnlocked`
- `TestExportLeavesEntriesUnsignedWhenLocked`
- `TestExportSignsTheRefNotTheBody`
- `TestReconcileReemitsSignedWhenUnlocked`
- `TestExportRefusesWhenLockedAndOwnOriginIsSigned`: the outbox is kept.
- `TestImportRefusesABadMACAndHoldsTheOrigin`
- `TestImportRefusesUnsignedAtOrAboveSignedFrom`
- `TestImportAppliesUnsignedBelowSignedFrom`
- `TestImportAppliesUnsignedFromAnOriginThatNeverSigned`
- `TestImportRecordsLocalSignedFromOnTheFirstVerifiedEntry`
- `TestSignedFromTakesTheLowerOfRemoteAndLocal`
- `TestARemoteThatDropsSignedFromCannotSwitchCheckingOff`
- `TestLockedReaderImportsWithoutVerifying`
- `TestSnapshotRegionRefusesABadMAC`
- `TestCompactionKeepsSurvivorMACs`
- `TestVerificationHappensBeforeAnyBodyFetch`: the memfake blob store counts gets, and a bad-MAC `$ref` entry causes zero gets.

**Mutation check.** For each refusal (bad MAC, unsigned at or above signed-from, local-record minimum, locked-signed-origin export), remove or invert the check, confirm the named test fails, restore it, and list the results in the report.

### Constraints
- CI has no network: use the memfake and scratch remotes.
- Keep the coverage baselines (`internal/synclog 94.0`, `internal/sync 77.5`, `internal/db`) and never lower them. Regenerate with `sh scripts/check-coverage.sh --write` only if code moved between packages, and say so.
- Style per CLAUDE.md.
- Gate: `GOTOOLCHAIN=go1.27.2 make check`.

### Report
The diff stat against this scope, the tests, the mutation table and the gate output.

### Sync step 3a, round 4 of 4: amend the spec

Rounds 1-3 built signed log entries: format 3, `key_meta`/`key_init`, `relevo db sync unlock [--new]`, `sync.key`, sign on export, verify before fetch, and the signed-from rule.

### Deliverables

1. **Amend `docs/specs/2026-10-10-sync-everything-design.md` §5**, "Key" and "Signing" only, to say what was built. Use plain prose in the spec's existing voice, with no history and no round numbers.
   - **Unlock.** The passphrase is entered with `relevo db sync unlock`. `--new` creates `kdf_salt`/`kdf`/`key_check` insert-if-absent, a racing machine adopts the winner's salt, and the passphrase comes from `--passphrase-stdin`, then `RELEVO_SYNC_PASSPHRASE`, then a TTY. `enable` takes the passphrase only from step 5 on.
   - **KDF bounds.** Parameters are refused outside fixed bounds, because the remote chooses them.
   - **What a MAC covers.** It covers origin, table, pk, op, schema version, the compacted body as stored (a `$ref`, not the fetched body) and At in RFC3339Nano UTC. It does not cover Seq or Batch.
   - **signed-from.** The worker writes `signed_from.<origin>` once, at the origin's first signed entry. A reader takes the lower of that value and its own local record of the first entry it verified, so a remote that rewrites meta cannot switch checking off.
   - **Verification order.** It runs before any body fetch.
   - **Locked machine, signed origin.** Such a machine holds its export and says `run relevo db sync unlock`.
   - **Known limits.** A holder of the Turso token can replay an old signed entry at a new seq, or delete entries; signing detects neither.
   - **Rollout.** Format 3 is refused by older binaries, so every machine installs the release before anyone runs unlock.
2. The plan doc `docs/plans/2026-10-12-sync-signing.md` is already on the base branch. Leave it as it is.

### Gate
`GOTOOLCHAIN=go1.27.2 make check`.

### Report
Quote the §5 text before and after.
