# Sync step 5a: allow-listed secrets as an encrypted shared document, and the new-machine flow

## MasterMind corrections (binding; they override the plan text below where they differ)

C1. **`client.key` is never exported unless a person stamped it.** Today laptop, zen and contabo each hold their own generated `client.key`, and each is enrolled on the servers under its own client id. With newest-wins over unstamped values, the first unlocked export after the upgrade would silently replace two machines' identities and break their enrolment on every server. So:
- An unstamped `client.key` (generated, normalized, or pre-existing from before 5a) is never exported. Only a stamped one travels. `relevo config secret set client.key` (or an import carrying `client.key`) is how the owner makes one machine's key the fleet's.
- An arriving stamped `client.key` replaces a local unstamped one. That is the owner's deliberate choice, so the replaced machine's report and `relevo doctor` say the client id changed and that the servers need the new enrolment line (`relevo config server key --enroll-line`, then `relevo serve enroll --key "<line>"` on each server). Doctor reads that from local state only; no network.
- On a fresh joiner, the fleet's stamped key wins over the joiner's generated one. If the fleet has none, the joiner keeps its own and exports nothing.
- This replaces the plan's "no stamped key anywhere converges by newest (stamp, origin)" rule. Test both directions, and add a test that two machines with different unstamped keys both keep their own after a full sync.

C2. **Pre-existing allow-listed secrets other than `client.key` do travel.** The laptop holds `r2.*` from before 5a, and the plan does not say how they reach the fleet. They export on the first unlocked export, the way 3b exported pre-existing config, so a joiner gets the R2 credentials without the owner re-running enable. Name the mechanism (export-time stamp for a value with no write stamp, or whatever 3b does), and test it: a machine holding `r2.*` and `typesafe` from before the doc existed becomes unlocked, and a joiner receives them.

C3. **The join needs a secrets pre-pass. Doc-before-body ordering does not exist in a per-origin log.** A peer's log interleaves row entries (some with `$ref` bodies in R2) and doc entries in seq order, and the importer applies an origin's entries in order. A joiner without R2 credentials would therefore hold that origin on its first `$ref`, long before the `doc:secrets` entry further down. So, for an enable whose R2 credentials are not stored:
- Before the normal import, read every origin's `doc:secrets` entries, verify each MAC under the just-checked key, and apply them (newest-wins) without moving any import mark. Then run the normal join. When the normal import reaches the same entries later, they are redelivery no-ops.
- Find whether the worker or transport can read entries of one table/doc without walking every batch. If it cannot, the pre-pass walks the batches and applies only `doc:secrets`. Say which, and its cost on a remote the size of production (about 36k entries).
- Compaction: if an origin's `doc:secrets` entries sit below a compaction floor, they are in its snapshot. Find where a snapshot carries docs and read them there.
- Test it: on memfake, machine A's log holds a `$ref` row entry BEFORE its `doc:secrets` entry. B joins with only the code and the passphrase, and ends holding the row with its body. The test fails without the pre-pass.

C4. **The envelope version.** 5b will write `"v":2` (with the epoch), not add a field to v1. A 5a reader refuses `v != 1` AND a v1 envelope carrying any field it does not know. Pin both with tests.

C5. **A secrets entry never carries a `$ref`.** Doc entries are inline today (`BlobRefThreshold` applies to eligible row columns). Keep the 8 KiB envelope cap, and add a test that a maximum-size secret's exported entry carries no `$ref`, so a later change to the blob rules cannot move a secret into R2.

C6. **Wrong passphrase stores nothing, even if the transport needs stored settings.** If the worker can only be opened from stored settings or a stored token, store them, verify `key_check`, and on any failure restore the previous state completely. Test that after a wrong passphrase the sync settings, the token, `sync.key` and the enabled marker are byte-identical to before. In-memory staging is better if the code allows it. Say which you built.

C7. **stdin carries one secret.** With `--join`, the token comes from the code, so `--passphrase-stdin` may use stdin. Any two of `--token-stdin`, `--r2-secret-stdin` and `--passphrase-stdin` together are refused, as the existing single-stdin rule does. A plain `enable` with `--r2-secret-stdin` takes the passphrase from `RELEVO_SYNC_PASSPHRASE` or the TTY.

C8. **`invite` on a locked machine.** It still prints a code: the remote has a key, so a joiner can unlock. But the human output adds a line saying this machine is locked, so its own secrets have not been shared. With `--json` that is a field.

C9. **The plan file is already on the base branch.** `docs/plans/2026-10-15-sync-secrets-new-machine.md` is committed on `relevo/se5a-base` before the chain starts. Round 4 leaves it alone; drop its "save the plan twin" step.

C10. **Doctor login rows.** They are reported for each harness configured on THIS machine, using the per-account homes. A harness whose binary is not on PATH is already a Warn row; skip the login row for it rather than reporting two problems. Agy's shared `~/.gemini` is checked like any other file. Opencode's login is checked by the presence of its credentials store, without opening it.

Seed: `se5a-plan/001-prompt.md`. Steps 1–4 merged (main `41bf202d`). Rekey is step 5b
(separate plan, NOT here). Ownership is step 6 (NOT here). Spec:
`docs/specs/2026-10-10-sync-everything-design.md` §1 (D7, D8), §5 "Secrets (step 5)",
§8, §10 (passphrase row), §11, §12 item 5.

Where the seed names a path loosely, this plan names the real one. One correction:
the seed says "SCOPES.md"; the file is `internal/db/migrations/SCOPES.md`
(there is no repo-root SCOPES.md). Everything else in the seed checked out
against the code named below.

## 1. Behaviour and cases

### 1a. The `secrets` shared document (decision 1 + 2)

- New doc named `secrets` (`doc:secrets` on the log) on the 3b framework, bound in
  `sharedDocs()` like every other doc. One key per secret name. Newest wins per
  key by the hybrid clock; a cleared secret is a tombstone (`null` value, never
  `OpDelete`); the losing value is kept the way the framework keeps a loser,
  except it is key-only (see redaction). Only ever exported/imported signed, as
  all shared docs are; a locked machine neither exports nor applies it (locked
  readers park via the existing `takeDocs`/`DrainPending` path; locked exporters
  emit nothing and keep the signal).
- Allow-list, checked on export AND on import, exactly:
  `typesafe`, `client.key`, `r2.endpoint`, `r2.bucket`, `r2.key_id`, `r2.secret`.
  An arriving key not on the list is refused through the existing
  refusal/supersede path, named in the report, never written. Pinned:
  `turso.token`, `sync.key`, `serve.tls.*`, and any unknown name are refused on
  import and never exported.
- Each value travels as XChaCha20-Poly1305 ciphertext. Associated data is the
  secret's exact name, so a value cannot be moved to another name.
- The encryption key is NOT `sync.key`. It is a 32-byte subkey:
  HKDF-SHA256 over `sync.key` with the fixed label `relevo-sync-secret-v1`
  (empty salt), so the MAC key and the encryption key are never the same bytes.
- Envelope is versioned: `{"v":1,"n":<base64url nonce>,"c":<base64url ciphertext>}`,
  one random XChaCha20-Poly1305 nonce per encryption. A 5a reader refuses an
  envelope whose `v` is not `1` (unknown-field/unknown-version bodies are refused,
  never misread). What 5b changes: it adds a key epoch to this envelope
  (e.g. `"e":<epoch>`, defaulting to epoch 0 semantics for v1 envelopes); the 5a
  shape already refuses what it does not know, so a 5b writer's envelopes fail
  closed on a 5a reader rather than decrypting under the wrong epoch.
- Size cap: one secret value at most 8 KiB of envelope. Provenance: the largest
  legitimate value is a `client.key` PEM (hundreds of bytes), so 8 KiB is more
  than 10x headroom while sitting an order below the gates doc's 64 KiB cap and
  far below the hooks/agent 2 MiB item caps. The doc never references a body in
  R2 and stays inline, because a joiner has no R2 credentials until this doc has
  been applied. Over-cap values are refused on export (error, signal kept, like
  the gates doc's unreadable-ledger rule) and refused on import.
- Redaction: a secret value never appears in a log line, an error,
  `relevo db sync status`, `relevo config` output, `relevo doctor`, a bugreport
  bundle, or a test failure message. The builder greps the paths that print a
  doc's values and closes each one for this doc: the generic
  `recordSuperseded` value-bearing revision path, `Drop.Reason` text, import
  refusal errors, status/quota lines, `config secret list` (names only, already),
  `config export` (sections only, already omits secrets), bugreport sources, and
  conflict/loser debug output. The secrets doc's `SupersedeIn` records key +
  reason only, never the value.

### 1b. What counts as a person's edit (decision 3)

- Only an explicit owner action stamps a secret: `relevo config secret set`
  (`cmd/relevo/config_server.go:157`), `relevo config secret rm` (tombstone),
  a config import that carries the secret files, an enable that is given R2
  flags. Each of these writes through a stamping entry point.
- A value relevo generates itself — the `client.key` a fresh install creates in
  `ensureClientKey` (`cmd/relevo/config_server.go:293`), the `NormalizeClientKey`
  re-marshal (`internal/config/clientkey.go:19`) — is written through an
  UNSTAMPED entry point (the 3b `unstamped` store pattern,
  `internal/config/config.go:106`), so on a join the fleet's existing stamped
  key wins over the joiner's generated one. The join already clears export state
  and write stamps before import (`internal/sync/join.go:351`), so the joiner's
  seed is replaced and never re-emitted as its own edit.
- If no machine in the fleet holds a stamped `client.key`, the rule that
  converges is: ordinary newest-wins over the exported stamps
  (`(stamp, origin)`, ties to higher origin) among the unstamped values; the
  applied-state comparison is deterministic, so every machine picks the same
  winner and later, older entries lose to it.
- When an arriving `client.key` replaces the local one, everything derived
  follows with no stored copy to go stale: `client.pub` is already dead
  (removed on import, `internal/config/import.go:205`); the client id and the
  enrolment line are derived live from the stored key via `remote.IDOf` /
  `client.EnrollLine` (`cmd/relevo/config_server.go:91,319`); `NormalizeClientKey`
  re-marshals through the unstamped path.

### 1c. `relevo db sync invite` (decision 4)

- Runs on a machine that already syncs. Prints ONE line on stdout: a join code
  carrying the Turso URL and the Turso token, and nothing else (no R2 key, no
  passphrase material — R2 and other secrets arrive through the secrets doc).
- Code format: fixed prefix `relevo-join-v1-` + base64url of a versioned JSON
  envelope, so it survives copy-paste. With `--json` it prints one document.
- Refuses on a machine with sync off, and on a remote that has no key yet
  (`kdf_salt`/`key_check` absent — a joiner could not unlock and would receive
  no secrets). The no-key refusal names `relevo db sync unlock --new`.
- Bearer credential: printed only to stdout, never logged; the human output says
  so in one stderr line (stdout stays exactly one line so it pipes).

### 1d. `relevo db sync enable --join <code>` (decision 5)

- Passphrase intake order is unlock's order: `--passphrase-stdin`, then
  `RELEVO_SYNC_PASSPHRASE`, then a TTY (same helper as unlock,
  `cmd/relevo/db_sync_unlock.go:62`). It travels CLI → daemon in the frame tail
  like the R2 secret does today: never argv, never forwarded env, never a log.
  Tail layout becomes `token ++ passphrase ++ r2secret` with explicit lengths
  (`R2SecretLen` exists; a new `PassphraseLen` is added; `enableIntake` splits
  from the end and refuses a length the tail cannot hold).
- Fixed order, kept exactly: parse the code (pure function, no network);
  stage remote URL + token in memory; open the transport; check the passphrase
  against the remote `key_check` BEFORE any import or any persistent store —
  a wrong passphrase refuses the enable and persists nothing (no token, no key,
  sync stays off); only then persist settings + token + `sync.key`; then join.
  Every entry the join imports is therefore verified, closing the spec's known
  limit "entries imported before this machine was unlocked are not
  re-verified" for this path. (The seed's "store … then … stores nothing" is
  staged-then-persisted, not stored-then-deleted.)
- With `--join`, R2 flags are not needed and the unconditional `ErrNoR2`
  preflight refusal does not apply (deleted for this path only). The R2
  credentials arrive in the secrets doc, which the importer applies before any
  body fetch. If the join finishes the doc import and no `r2.*` arrived, it
  refuses with a message naming `relevo db sync enable --r2-...` on this machine
  or a fix on the inviter. The bucket-estimate guard cannot run before R2
  credentials exist, so for `--join` the guard runs after the doc import when
  credentials arrived, and is skipped (join proceeds, "no size yet" rule) when
  they did not — R2 bytes are never refused on anyway.
- A plain `enable` (no `--join`) also accepts the passphrase with the same
  verify-before-join order. `unlock` and `unlock --new` keep working unchanged
  for machines that are already enabled (note: `Unlock` requires the machine to
  be enabled — `ErrSyncOff` — so `--join` cannot reuse `Unlock`; it needs a
  not-enabled verification helper over `KeyMeta` + `checkedKey`).
- After the join a new machine has, with no other command: the shared config,
  rendered agents, hook scripts, gates, consents, `typesafe`, `client.key`, and
  the R2 credentials.

### 1e. `relevo doctor` lists harness logins (decision 6)

- For each harness that a configured candidate uses (candidate refs →
  harness kinds, same derivation as `candidateGroups` in
  `internal/config/config.go:301`), one row per harness/account-home reporting
  whether that harness's login is present on this machine, by local file checks
  ONLY (no network, no harness spawn). Missing ones are named with the login
  command as `Fix`. Severity is at most `SevWarn`, never `SevFail`; the rows do
  not affect `UsableBuilder`. Login file locations come from the code that
  already knows them — `scripts/relevo-sandbox.sh:190` (claude
  `$CLAUDE_CONFIG_DIR/{.credentials.json,settings.json,settings.shared.json}`,
  codex `$CODEX_HOME/{auth.json,config.toml}`, opencode standalone creds in its
  own `opencode.db` — never copied, login via `opencode auth login`; agy has no
  per-process home selector, `~/.gemini` stays shared) composed with
  `harness.HomeEnv` / `harness.AccountHome` (`internal/harness/definition.go:49`)
  for per-account homes. A kind with no authoritative login file gets a
  `SevOK` "not checked" placeholder (the existing unknown-kind pattern).

### 1f. Spec and SCOPES (decision 7)

- `internal/db/migrations/SCOPES.md` lists the secrets doc and the allow-list,
  and says the `secret` table's other names (`turso.token`, `sync.key`,
  `serve.tls.*`, anything unknown) stay machine-local.
- The last round amends spec §5 "Secrets", §8, and §12 item 5 to what was built,
  and says rekey is 5b.

## 2. Seams (files, types, functions)

Doc framework — the pattern the secrets doc copies (read these, do not change
unless named):
- `internal/relevo/syncdocs.go:16` `init` (docs builder install),
  `sharedDocs` `:33` (the one place a doc is bound), `BindDocs` sites incl.
  join/unlock/verbs (a new site that forgets the bind must fail — see
  `internal/relevo/sync_newdocs_test.go` construction-vs-bind count test).
- `internal/sync/docs.go:8` `DocsBuilder`, `:17` `SetDocsBuilder`,
  `:24` `SharedDocs`, `:38` `BindDocs`.
- `internal/synclog/shareddoc.go:26` `SharedDoc`, `:49` `RegisterSharedDoc`,
  `:202` `DocPK`, `:224` `ParseDocEntry` (shape refusals, each with its own
  test), `:278` `decodeDocBody` (unknown fields refused — the envelope-version
  rule rides inside the VALUE, not the body, so no transport change).
- `internal/synclog/docexport.go:54` `exportDocs` (locked machine emits nothing,
  keeps signal — the secrets doc MUST return an error from `Items()` when
  locked, never an empty map, or the exporter reads it as "holds no keys" and
  tombstones the fleet), `:158` `docStamp` (write stamp wins when newer).
- `internal/synclog/docimport.go:103` `takeDocs` (locked parks, unlocked
  applies; clock observes every stamp), `:207` `applyDoc` (newest-wins,
  local-write-wins, redelivery no-op, `recordExported` stops echo),
  `:323` `DrainPending` (unlock/keyed-import drain verifies each parked entry).
- `internal/synclog/memfake.go:15` `MemTransport` (extend with nothing new;
  the join-from-zero test runs through it), `converge_test.go` pattern.
- Existing end-to-end instances: `internal/availability/gatedoc.go:15`
  (`GatesName`), `:21` 64 KiB cap precedent, `:78` `Items`, `:219` `ApplyIn`,
  `:238` stamps under `gates/<subject>`; `internal/consent/repodoc.go:30`
  `OpenRepo`, `:49` `Items`, `:118` `ApplyIn` (+ `sessiondoc.go`); config doc
  `internal/config/sharedoc.go:46` `Items`, `:166` `Apply`, `:210`
  `applyKeyIn` (unstamped apply), `:407` `recordSuperseded` (VALUE-BEARING —
  do not reuse for secrets), `syncapply.go:26` `ApplyIn`, `:39` `SupersedeIn`.

Secrets at rest today:
- `internal/db/config.go:111` `secretGet`, `:126` `SecretPut`, `:141`
  `SecretNames`, `:164` `SecretStore` (binds the LOCAL file — the secrets doc
  must read/write the local file too; it never touches the shared file).
- `internal/config/config.go:53` `SecretClientKey`, `SecretTypesafe`,
  `:106` `unstamped`, `:135` `Open`, `:420` `PutSecret` (stamping — split it),
  `:446` `SecretDelete`, `sectionread.go:41` `Secret`, `:49` `loadSecrets`.
- `internal/config/import.go:136` `commitImport` (blanket `unstamped=true` at
  `:50` — secrets inside an import need their OWN stamped entry point per the
  one-entry-point-per-rule trap), `:205` `client.pub` removal.
- `internal/config/writestamp.go:24` `stampWrites` (per-item stamps in the
  write's own tx).
- `internal/sync/r2.go:26` secret names, `:43` `ErrNoR2`, `:99` `SetR2`,
  `:127` `ReadR2`; `token.go:14` `SecretToken` (never in the doc).
- `internal/config/clientkey.go:19` `NormalizeClientKey` (must move to the
  unstamped entry point).

Key / passphrase:
- `internal/sync/key.go:19` `SecretKey`, `:55` `ParseKDF` + bounds, `:86`
  `DeriveKey`, `:93` `KeyCheck`, `:100` `SetKey`, `:125` `SigningKey`
  (per-attempt read — the secrets doc reads the key the same way).
- `internal/sync/unlock.go:18` `ErrNoKDF`, `:22` `ErrWrongPassphrase`, `:25`
  `ErrSyncOff`, `:34` `Unlock` (requires enabled — NOT reusable for `--join`),
  `:88` `checkedKey` (reusable), `:144` `DrainParkedDocs`.
- `internal/relevo/syncverb_unlock.go:13` `unlock` (frame-tail passphrase,
  drain, `FinishJoin`).

Enable / join / wire:
- `internal/sync/enable.go:90` `TokenIntake.Resolve`, `:126` `R2Resolve`,
  `:125` `Preflight` (order is the contract — the `--join` path skips ONLY the
  R2 leg), `:285` `store`, `:175` `db.EnablePreflight`.
- `internal/sync/join.go:242` `Enable`, `:333` `join` (marker, `ClearDocExportState`
  `:354`, `drainImport`, `reconcile`, `publishMarks`, locked-join stays marked),
  `:387` `FinishJoin`, `:411` `drainImport` (+ `CaptureFiles`/`SettleFiles`),
  `:461` `reconcile`, `:495` `recordRemoteKDF`; `joinguard.go:76`
  `EstimateJoin`, `:137` `GuardJoin`, `:179` `ownBodies` (needs plan.R2 — nil-safe
  already when `Blobs == nil`; the `--join` flow must reach it only with
  post-import credentials).
- `internal/sync/blobfetch` + `internal/synclog/blobfetch.go` (find where the
  join's first body fetch sits relative to doc apply; move NOTHING except what
  the secrets-before-bodies order needs: doc entries are inline and need no
  fetch; history bodies fetch only after `r2.*` applied).
- `internal/relevo/syncverb.go:91` `Run`, `:329` `enable`, `:158`
  `publishJoin`, `:287` `drive`; `synctail.go:15` `enableIntake`.
- `cmd/relevo/db_sync.go:286` `cmdDBSyncEnable`, `:315` tail build, `:377`
  `dbSyncEnableToken`, `:544` `dbSyncClassify`; `db_sync_r2.go:101`
  `dbSyncEnableR2` (single-stdin refusal — a passphrase-stdin flag joins this
  refusal: stdin can carry ONE secret; `--token-stdin` + `--passphrase-stdin`
  together are refused the same way), `:116` `dbSyncEnableR2Tail`, `:54`
  `syncR2Status` (endpoint+bucket only — the no-credential-leak pattern);
  `db_sync_call.go:135` `sendSyncVerbToken`, `:167` `dbSyncVerbRefusal`;
  `db_sync_unlock.go:62` `dbSyncPassphrase` (reuse the ORDER, not a copy).
- `internal/db/wire/msg.go:146` verb names, `:177` codes (reuse `no_kdf`,
  `wrong_passphrase`, `no_r2`; add a code ONLY if no existing one fits, and map
  it in `dbSyncVerbRefusal`), `:250` `SyncVerb` (gains `Join` + `PassphraseLen`),
  `:290` `SyncResult` (no credential fields, ever).

Doctor:
- `internal/doctor/doctor.go:34` `Check`, `:163` `Run`, `:206` `checksForKind`
  (binary-missing Warn-and-skip pattern), severities `:16`.
- `cmd/relevo/doctor.go:293` `Run` wiring, `doctor_checks.go:491` per-kind bind.
  New: `internal/doctor/logins.go` (`LoginChecks(env, kinds, homes)` over the
  existing `Env` file seams `HomePath`/`Stat`/`ReadFile` — local checks only).
- `internal/harness/harness.go:118` `knownHarnesses` (kinds), `:431` `Lookup`;
  `definition.go:49` `HomeEnv`, `:62` `AccountHome`.

Docs to amend (last round): `internal/db/migrations/SCOPES.md:111` (secret row),
`:145` (docs table); spec `docs/specs/2026-10-10-sync-everything-design.md` §5
"Secrets", §8, §12 item 5. New plan file (this round writes the twin):
`docs/plans/2026-10-15-sync-secrets-new-machine.md` — the builder rounds below
save their plan there; THIS planning round saves only to the state path in the
round prompt.

## 3. Ordered steps (one line each: deliverable + how it worked)

### Round 1 — secrets doc, envelope, allow-list, stamps plumbing, redaction

1. Add `internal/sync/secretcrypt.go` (pure: HKDF subkey, seal/open envelope,
   allow-list predicate, 8 KiB cap, unknown-`v` refusal) — done when
   `make check-quick PKGS='./internal/sync/...'` passes with tests naming
   subkey-inequality, name-bound AD, unknown-version refusal, cap refusal.
2. Add `internal/config/secretdoc.go` (`secrets` doc: `Items` errors when locked
   and filters to the allow-list, `Apply`/`ApplyIn` refuse off-list keys and
   over-cap/unknown-`v` envelopes, key-only `SupersedeIn`, tombstone clears,
   stamps under `secrets/<name>`) — done when `go test ./internal/config/ -run
   'Secret'` passes including a test that locked `Items` errors and keeps the
   export signal.
3. Split `PutSecret`/`SecretDelete`/import-secret-writes into stamping
   (person: `secret set`/`rm`, secret files in an import, R2 flags on enable)
   vs unstamped (peer apply, generated `client.key`, `NormalizeClientKey`)
   entry points — done when each path has its own named test pinning
   stamped-vs-unstamped and the suite shows the old shared-statement trap gone.
4. Bind the doc in `sharedDocs()` (`internal/relevo/syncdocs.go:33`) and cover
   every `BindDocs` site incl. join/unlock/verbs — done when the
   construction-vs-bind count test passes through the real steady pipeline on
   memfake (two machines), not a direct-doc test.
5. Close every value-printing path for this doc (revision records, drop
   reasons, refusal errors, status, `config` output, doctor, bugreport, test
   failure strings) — done when each has a named test that fails if the value
   is printed (mutation-tested by logging a canary value).
6. Commit after each step above; run the round's full gate once at the end.

### Round 2 — invite, join code, passphrase-before-join, tail

1. Add pure join-code create/parse (`internal/sync/joincode.go`: prefix,
   version, base64url, `{url, token}` and nothing else) — done when round-trip,
   copy-paste-alphabet, and unknown-version-refusal tests pass in
   `internal/sync` with no network.
2. Add `relevo db sync invite` (local-rows read only, stdout = one code line,
   stderr = bearer-credential line; refuses sync-off and no-remote-key naming
   `unlock --new`; `--json` = one document) — done when the rule is tested as a
   pure function in `internal/relevo` (no `cmd/relevo` spawn/network per
   repo rule) plus CLI flag/usage tests.
3. Extend the frame tail (`SyncVerb.Join` + `PassphraseLen`,
   `token++passphrase++r2secret`, `enableIntake` splits from the end, stdin
   exclusivity extended to three secrets) and the enable CLI flags
   (`--join <code>`, `--passphrase-stdin`, code parsed before dial) — done when
   tail-split, length-refusal, and stdin-exclusivity tests pass.
4. Implement daemon-side `--join`: in-memory stage of URL+token, transport open,
   `key_check` verification BEFORE any store (wrong passphrase persists
   nothing, sync stays off), then persist settings+token+`sync.key` and run the
   existing join; plain `enable` accepts the passphrase in the same order;
   `unlock`/`--new` untouched — done when wrong-passphrase-stores-nothing and
   right-passphrase-joins tests pass on memfake, each failing without the order.
5. Commit after each step above; run the round's full gate once at the end.

### Round 3 — stamping semantics, R2-after-docs, join-from-zero convergence

1. Pin person's-edit stamping end to end (`secret set`, secret-file import,
   R2-flagged enable stamp; generated/normalized/arriving values do not) —
   done when each has a named stamp test in its owning package.
2. Reorder the `--join` pipeline only as needed: `Preflight` skips the R2 leg
   with a code, docs import and apply before the first body fetch, guard runs
   post-import when `r2.*` arrived else the join proceeds, missing `r2.*`
   after doc import refuses naming `enable --r2-...`/inviter fix — done when
   each ordering has a test that fails when the order is swapped.
3. Prove the zero-to-joined scenario on memfake through the REAL steady
   pipeline: machine A unlocked with stamped secrets/config/hooks/agent, machine
   B joins with only code + passphrase and ends with all of them plus R2
   creds, exporting none of A's values back as its own edits — done when that
   one convergence test passes and fails if B re-emits anything.
4. Pin `client.key` follow-through (fleet stamped wins over generated;
   no-stamped-anywhere converges by newest `(stamp, origin)`; arriving key
   replaces and enrolment derives live) — done when each rule's test fails
   without it; run every new test with PATH holding only git (+ C toolchain
   for cgo) before calling it done.
5. Commit after each step above; run the round's full gate once at the end.

### Round 4 — doctor logins, SCOPES, spec, baseline, gate

1. Add `internal/doctor/logins.go` (per configured-candidate harness+home,
   local file checks only over the existing `Env` seams, missing names the
   login command, max `SevWarn`, `UsableBuilder` untouched) — done when
   present/missing/not-checked tests pass for claude+codex+opencode+agy with a
   fake home and no network.
2. Wire the rows into `relevo doctor` (`cmd/relevo/doctor.go:293` area) —
   done when doctor renders the rows and no existing doctor test regresses.
3. Amend `internal/db/migrations/SCOPES.md` (secrets doc + allow-list; other
   `secret` names stay machine-local) and spec §5 "Secrets", §8, §12 item 5 to
   what was built, noting rekey is 5b — done when the diff touches only those
   sections.
4. Regenerate the coverage baseline with `sh scripts/check-coverage.sh --write`
   IFF code moved between packages, and say so in the report; never lower a
   baseline to get green — done when `RELEVO_REQUIRE_COVERAGE=1
   GOTOOLCHAIN=go1.27.2 make check` passes from a clean tree.
5. Save the plan twin to `docs/plans/2026-10-15-sync-secrets-new-machine.md`;
   commit after each step; run the full gate once at the very end (and again
   only if a later fix changes code).

Style for all rounds: no history in code, functions ≤ 70 lines, non-test files
≤ 600 lines (`.golangci.yml`, `scripts/check-comments.sh`,
`scripts/check-filesize.sh` enforce it). New work is new commits only — never
amend or rebase a commit on a remote binding's branch. While working, run only
touched packages (`make check-quick PKGS='./internal/<pkg>/...'`, or
`go test ./internal/<pkg>/ -run <Name>`); the plan's full gate runs once per
round end.

## 4. What is deleted (closed numbered list)

1. The unconditional `ErrNoR2` refusal in `EnableRequest.Preflight`
   (`internal/sync/enable.go:125`) for enables carrying `--join`: deleted on
   that path and replaced by the post-doc-import completeness check; plain
   enables keep it.
2. The rule that `enable` never takes a passphrase: deleted — both `--join`
   and plain `enable` now read `--passphrase-stdin` /
   `RELEVO_SYNC_PASSPHRASE` / TTY in unlock order.
3. Stamped writes of self-generated secrets: `ensureClientKey` generation and
   `NormalizeClientKey` re-marshal through the stamping `PutSecret` path, and
   the import's blanket `unstamped=true` covering secret files, are deleted and
   replaced by the per-path entry points in §1b (generated = unstamped; secret
   files in an import = stamped person's edit).
4. Value-bearing loss records for the secrets doc: the generic
   value-storing `recordSuperseded` pattern
   (`internal/config/sharedoc.go:407`) is NOT used for secrets; replaced by
   key-plus-reason-only records. (If the builder finds a listed surface already
   safe, the report says which item was a no-op — it does not invent one.)
5. The spec's known limit "entries imported before this machine was unlocked
   are not re-verified" (§5 signing) for the `--join` path: closed by the
   verify-before-store order; the last round amends the spec to say so.

## 5. What the report must include

- Per round: `git diff --stat` of the new commits (no amended/rebased
  remote-branch commits), focused test commands run and their result, the one
  full-gate run (`RELEVO_REQUIRE_COVERAGE=1 GOTOOLCHAIN=go1.27.2 make check`)
  and its result.
- For EVERY refusal and every "never exported" / "never printed" rule: the
  named test plus the mutation check (what was broken to make it fail).
- PATH-restricted evidence for every new test (PATH with only git + C
  toolchain where needed) and the memfake join-from-zero result.
- Coverage baseline: regenerated or not, and why (which code moved packages,
  if any); never lowered.
- The 5b note: what the epoch addition changes in the envelope, and the
  unknown-`v` refusal test name.
- Spec + SCOPES deltas (sections touched), the plan twin path, and any halt
  with the exact step and the code fact that made it impossible (a design
  error that halts beats a green suite that bent a test).
