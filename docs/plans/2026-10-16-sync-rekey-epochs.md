# Sync step 5b: `relevo db sync rekey` with key epochs

## MasterMind corrections (binding; they override the plan text below where they differ)

C1. **A never-signing origin keeps today's posture.** Answer B's "unsigned entries above the cut are refused" is dropped. An origin with no signed-from boundary (a machine that has never unlocked) may still append unsigned history and unlocked readers still accept it below and above its cut, exactly as today: spec §10's row "Wrong or missing passphrase ... history still syncs" stands, and a rekey must not silently stop a locked machine's history from reaching the fleet. The cut rule applies to signed entries: an entry of an origin that signs, above that origin's cut, must verify under an epoch >= the cut's. Add this to the known-limits list (a token holder can still append unsigned entries under an origin that never signed; unchanged by rekey).

C2. **One protocol bump.** `ProtocolVersion` goes 2 -> 3 once, in round 1 with `key_rekey`; round 2 adds `key_cut` under the same version, because nothing is released between the rounds. The `bytes_test.go` pin is updated once.

C3. **`invite` on a stale machine** (newest held epoch < recorded remote epoch) still prints the code, and, like the locked note, the human output adds one line saying this machine holds an old key and must run `relevo db sync unlock` with the new passphrase; `--json` carries it as a field. Round 3.

C4. **The plan file is already on the base branch.** `docs/plans/2026-10-16-sync-rekey-epochs.md` is committed on `relevo/se5b-base` with these corrections before the chain starts. Round 4 leaves it alone; drop its "save the plan twin" step.

C5. **Re-seal scope.** There is no separate re-seal pass: after the rekey, `Items()` seals under the newest epoch, and the exporter re-emits each key under the stamp it already carried (answer C) the next time the document is signalled. The rekey signals it once locally without stamping. A test pins that two machines' re-emits of the same value under the kept stamp converge (no ping-pong between them on later signals).

C6. **Stale state is visible beyond status.** `relevo db sync status` (human and `--json`) carries the stale state; the steady attempt's trouble line names `relevo db sync unlock`. No change to doctor in this step.

Seed: `se5b-plan/001-prompt.md`. Steps 1–4 and 5a merged (main `4940e11b`).
Ownership (step 6) is NOT here. Spec:
`docs/specs/2026-10-10-sync-everything-design.md` §5 ("Key", "Signing",
"Secrets (step 5a)", the "Rotation (step 5b, not built)" line), §10 (passphrase
row), §12 item 5. The plan lands at `docs/plans/2026-10-16-sync-rekey-epochs.md`
(round 4 saves the twin; MasterMind corrections, if any, override the text
below and ride into the twin).

Two seed path corrections, checked against the code: `EntryMAC` and the
signed-from tag live in `internal/synclog/mac.go` (`EntryMAC:25`,
`BoundaryTag:47`), not `sign.go`; the passphrase intake order lives in
`cmd/relevo/db_sync_unlock.go:72` (`dbSyncPassphrase`), while
`internal/relevo/syncverb_unlock.go:13` is the daemon-side verb. Everything
else in the seed checked out.

Style for all rounds: no history in code or test names; functions ≤ 70 lines;
non-test files ≤ 600 lines (`.golangci.yml`, `scripts/check-comments.sh`,
`scripts/check-filesize.sh`); mutation-test every new rule; tests on memfake;
no `cmd/relevo` test spawns a worker, a harness or reaches the network — rules
are pure functions in `internal/relevo` (CLAUDE.md:110-113). New work is new
commits only — never amend or rebase a commit on a remote binding's branch.
While working run `make check-quick PKGS='./internal/<pkg>/...'` or
`go test ./internal/<pkg>/ -run <Name>`; the full gate
`RELEVO_REQUIRE_COVERAGE=1 GOTOOLCHAIN=go1.27.2 make check` runs once per
round end.

## 0. The five answers (with evidence)

### A. How an entry carries its epoch

**Decision: no per-entry field. The epoch binds per origin through a cut row
in the remote `meta` table — the signed-from boundary's pattern — plus trial
verification under the reader's keyring. The log format stays 3; no DDL runs.**

- A per-entry epoch field would be a new `log` column, i.e. log format 4
  (`internal/syncworker/format.go:22` `logFormatVersion = 3`, `upgrades:60`,
  `ensureLog:98`). A new binary upgrades a remote in place the moment it opens
  it, so format 4 would lock every 5a binary off the remote — via
  `errNotALog:268`, "it is not a relevo sync log" — before anyone ran rekey.
  That contradicts decision 2, which says a 5a machine learns of a rekey
  through a `key_check` mismatch: that sentence only makes sense if 5a
  binaries keep opening the remote until a rekey actually happens. It would
  also touch `EntryMAC` (`mac.go:25`), whose exact byte encoding is what every
  already-signed entry on the log verifies under.
- `meta` needs no DDL: it already carries `kdf_salt`, `kdf`, `key_check`,
  `signed_from.<origin>` (`keymeta.go:22-27`) and `floor.<origin>`
  (`marks.go:177`). New rows are invisible to a 5a binary: its `KeyMeta`
  selects exactly three keys (`keymeta.go:42-44`), `readSignedFrom:175` reads
  only `signed_from.%`, `readMetaPositions:223` only the floor prefix.
- The binding: per origin, `epoch_cut.<origin>` = `"<E> <seq> <base64 tag>"`,
  tag = HMAC-SHA256(key of epoch E, label `relevo-sync-epoch-cut-v1`, origin,
  decimal E, decimal seq), each field length-prefixed (`writeMACField:76`
  pattern; a fresh label, so a cut tag can never double as an entry MAC or a
  signed-from tag — the separation `macDomain:16` and `boundaryDomain:44`
  already keep). Reader rule: an entry above the cut must verify under a held
  epoch ≥ E; an entry at or below verifies under any held key, and
  HMAC-SHA256 verifies under at most one — the per-entry binding is
  unambiguous. With no cut row the default is (newest held epoch, 0), which at
  an epoch-1 fleet reduces to today's single-key verification exactly.
- **Rollout consequence:** shipping 5b changes no wire byte (an epoch-1 fleet
  makes byte-identical MACs and v1 envelopes); only a rekey does. After a
  rekey a 5a binary refuses rather than misreads: epoch-2 entries fail
  `VerifyMAC` under its epoch-1 key (`verify.go:84` → `signatureError` → that
  origin held), a v2 envelope is refused ("this build reads only 1",
  `secretcrypt.go:335`, pinned by 5a's C4 tests), unlock/join with the old
  passphrase is `ErrWrongPassphrase` (`checkedKey`, `unlock.go:88`), and the
  new meta rows are rows it never reads. A 5a binary that unlocks with the
  NEW passphrase derives the newest key and its own appends still verify
  everywhere (above its cut, newest key), but it cannot unwrap the chain, so
  epoch-1 history fails and it holds every origin — loud, never a misread.
  Spec sentence (round 4): every machine installs the release before anyone
  runs rekey, mirroring signing's unlock rollout (§5 "Rollout").

### B. The security property after a rekey

Property: an attacker holding an old epoch's key and the Turso token must not
get a new entry accepted by an unlocked, current reader.

**The rule.** The rekey to epoch E writes, in its own transaction, a cut for
EVERY origin at that origin's head as the rekeyer observed it, tagged under
key_E (predict-and-verify inside the transaction, exactly
`writeSignedFrom:140-168`: a head that moved between the prediction read and
the transaction refuses the whole rekey and it retries). A current reader
holds epochs 1..E. Per origin its effective cut is the max of its local record
and every remote cut whose tag verifies under the cut's epoch key; verified
cuts are recorded locally at once and never regress — the `sync_signed_from`
pattern (`024_sync_signed_from.sql`, `db/sync_signed_from.go:33-39`,
`loadSigned:31`), so deleting, lowering or replaying an older remote row
changes nothing. Entries above the cut must verify under an epoch ≥ E key;
the attacker cannot compute such a MAC (no key_E), cannot forge or raise a cut
(tag under key_E), and cannot regress one (monotone local record). Unsigned
entries above the cut are refused, so the cut also closes today's hole where a
token holder appends unsigned entries under an origin that never signed.

**The race.** An origin that appended under the old key before it learned of
the rekey: its entries land above its cut and every reader refuses them — the
origin is held, per origin per run, reported, not latched (the existing
`holdsOrigin`/`signatureError` machinery, `verify.go:12-24`). On its next
attempt the origin sees the epoch moved (the remote-epoch fact recorded at the
start of every attempt, `recordRemoteKDF`, `steady.go:403`, `join.go:397`),
stops signing and exporting whole (new sentinel `ErrEpochMoved`, the
`ErrLockedSignedOrigin` pattern, `sign.go:11`), and status says the passphrase
changed. **What its stuck entries need:** the operator runs `relevo db sync
unlock` with the new passphrase — which stores the newest key and every older
one it unwraps, wrong passphrase stores nothing — and the machine then
publishes its own cut extension: seq = the highest seq its own transport
confirmed appending (a new local marker written after every successful
append), tag under the newest key, forward-only and bounded by its log head on
the worker side. The tail it vouches for verifies as old-epoch history;
anything above its own confirmed head stays refused.

**Known limits (list; the report repeats it).**
1. Entries the origin's own transport appended between the rekey and its
   discovery are indistinguishable from forgeries a token holder interleaved
   into the same seq window below the extension seq; the extension vouches
   for both. On a healthy machine the window is one attempt.
2. A machine that never learned of the rekey (unupgraded, or offline while it
   kept appending — offline machines append nothing, so only the unupgraded
   case) vouches for its whole old-key tail the same way when it finally
   unlocks; everything it appends after it stops is refused.
3. A token holder could already replay an old signed entry at a new seq or
   delete entries (spec §5 signing limits); cuts neither add nor remove that.
4. A crash between a successful append and the own-head marker write leaves
   honest entries above the extension: a visible hold, and the owner's way out
   is a further rekey, whose cuts cover them as history. An origin whose
   stuck tail it cannot vouch for stays held until such a rekey — the way out
   is an owner act, never automatic.
5. Rekey protects the future, not the past: old keys stay derivable from the
   wrapped chain by anyone holding a newer key, and a leaked old passphrase
   still reads every old entry and old envelope. Nothing re-encrypts history.
6. A locked machine (no key at all) still applies history unverified, as
   today; rekey does not extend what it accepts and does not close that
   pre-existing posture.

### C. Re-sealed secrets vs a concurrent newer edit

After a rekey the rekeyer's `Items()` (`secretdoc.go:95`) seals every held
secret under the newest epoch — v2 bytes, same plaintext — so the exporter's
digest differs and it re-emits every key (`docEntries:111`). **Rule: a re-seal
re-emits under the stamp the key already carried (its export-state stamp),
never a fresh clock stamp.** Today `docStamp` (`docexport.go:158-182`) would
give a digest-changed key with no newer write stamp a clock stamp moved past
its last — which would rank the re-seal above a concurrent peer edit and
override it. A person's edit still carries its write stamp
(`ItemWriteStamp`, `secretdoc.go:336`), so the concurrent edit — stamped
newer, exported once its machine unlocks — wins on every reader including the
rekeyer, and the re-seal lands superseded or as a redelivery. Redelivery
detail: a reader that already applied the original sees the re-seal at equal
(stamp, origin) and skips it, keeping the same plaintext; after its own unlock
its own re-export converges, because the deterministic nonce
(`sealNonce:211`) makes one plaintext under one epoch seal to one envelope.
Cost: one rekey emits at most machines × allow-listed-secrets redundant
entries, each under the 8 KiB cap; compaction ages them out. Scope: the
stamp-keeping rule applies to the secrets doc only (an optional doc
interface); the gates doc's aging behaviour — a digest change with no write
stamp exported under an advanced clock stamp — is unchanged. The rekey signals
the secrets doc once locally (`SignalDoc`, the `StampSecretWrites:70-84`
pattern) without stamping, so the next export diffs.

### D. Compaction snapshots and the join pre-pass under epochs

`Importer.verify` (`verify.go:67`) is the single choke point: the normal batch
path, the snapshot path (`snapshot.go:99`) and the pre-pass
(`docprepass.go:106`) all call it, so teaching `verify` the keyring and the
cut rule covers all three, each with its own epoch test. Compaction itself
stays epoch-blind: cuts are meta rows and never compact; floors, marks and
the drop rule are untouched. A snapshot region below a floor verifies per
entry (trial under held keys; above-cut entries newest-only); a compacted
origin's surviving `doc:secrets` entry — v1 or v2 — opens under its epoch's
key, which a joiner holds because `VerifyJoinKey` (`unlock.go:117`) unwraps
the whole chain. The pre-pass reads seq 1 to head, i.e. every epoch mixture,
and verifies each entry under its epoch's key — decision 3's "a joiner
verifies the whole history". A locked machine's pre-pass stays a no-op
(`docprepass.go:57`).

### E. Interruption: rekey written to the remote, crash before the local store

The remote holds epoch E+1, the new salt/kdf/check, `key_wrapped.E` and every
origin's cut; the crashed machine still holds key_E and its epoch fact says E.
It is exactly decision 4's stale machine, with no special recovery: its next
attempt sees the epoch moved, stops signing and exporting, holds its import,
and status says the passphrase changed. **The owner runs `relevo db sync
unlock` with the new passphrase**: `checkedKey` passes against the new check,
the keyring is stored (newest plus every older key unwrapped), its own cut
needs no extension (the rekey transaction cut its own origin at its head, and
the verb held the sync slot so nothing of its own appended concurrently),
parked docs drain and any owed join finishes — the existing unlock tail
(`syncverb_unlock.go:22-30`). A wrong passphrase stores nothing.

## 1. Behaviour and cases (decisions 1–7 as rules)

1. **Epochs.** Every key has an epoch; the key every remote holds today is
   epoch 1 and every entry and v1 envelope on a log is epoch 1. Remote
   `key_epoch` absent = 1; present = canonical decimal ≥ 2, anything else
   refused (`ErrKDFParams` family — the remote is untrusted, the
   `decodeKeyValue:79` precedent: a malformed row is refused, never skipped).
   `rekey` makes E+1.
2. **Remote meta after a rekey.** `kdf_salt` (fresh 16 bytes), `kdf`,
   `key_check` describe the newest epoch. New rows: `key_epoch`;
   `key_wrapped.<e>` per older epoch = XChaCha20-Poly1305 seal of key_e under
   a wrap subkey HKDF-SHA256(key_{e+1}, label `relevo-sync-keywrap-v1`),
   nonce derived as `sealNonce:211` does (own label
   `relevo-sync-keywrap-nonce-v1`, length-prefixed (e, key_e)), associated
   data = length-prefixed (wrap label, e, e+1) — the epoch bound in. A machine
   holding the newest key unwraps E→1; nothing unwraps forwards. A wrapped row
   that will not open under a checked key is a refusal, never a skip.
3. **Entries name their epoch** through cuts + trial (answer A). The importer
   verifies each entry under that epoch's key; a joiner verifies the whole
   history.
4. **Machines on the old key.** Held newest epoch < recorded remote epoch
   (`sync.remote_epoch` kv fact beside `KeyRemoteKDF:23`, written by the
   existing per-attempt `RecordRemoteKDF:153` extended, and left as it was on
   an unreachable remote): the machine stops signing and exporting AND holds
   its import whole — a stale reader must not apply entries current readers
   refuse, or it diverges — one sentinel (`ErrEpochMoved`), one trouble line
   per attempt (the locked pattern, `steady.go:149-151`), status says the
   passphrase changed. `KeyState` (`unlock.go:169`) gains a `stale` value;
   `dbSyncKeyClause` (`cmd/relevo/db_sync_unlock.go:156`) renders
   ", key: the passphrase changed: run relevo db sync unlock". Unlock with the
   new passphrase stores the newest key and every older one it unwraps, then
   publishes the own cut extension; a wrong passphrase stores nothing.
5. **Secrets envelope v2:** `{"v":2,"e":<epoch>,"n":…,"c":…}`; subkey derived
   from that epoch's key (same `secretKeyLabel:120`), nonce derived as in v1
   under that epoch's nonce key. Readers open v1 (epoch 1) and v2 for any
   epoch they hold; v2 naming an epoch they do not hold, any other `v`, or any
   unknown field is refused (`readSecretEnvelope:314` extended). A build at
   epoch 1 keeps sealing v1 — byte-identical to today, so upgrading alone
   re-emits nothing; v2 only from epoch 2 on. The rekeying machine re-seals
   its held secrets (answer C).
6. **`rekey` intake.** `relevo db sync rekey`. The machine must be unlocked
   (no local key: refusal, new code `machine_locked`, naming unlock) and
   enabled (`ErrSyncOff:25`). The current passphrase is checked against
   `key_check` before anything is written (`checkedKey:88`; wrong =
   `ErrWrongPassphrase`, existing code `wrong_passphrase`; a remote with no
   key = `ErrNoKDF`, code `no_kdf`, naming `unlock --new`). Intake: current
   from `--passphrase-stdin`, then `RELEVO_SYNC_PASSPHRASE`, then a TTY; new
   from `RELEVO_SYNC_NEW_PASSPHRASE`, then a TTY asked twice — stdin carries
   at most one secret, so no stdin flag exists for the new passphrase. Empty
   either: refused. New equal to current: refused — rotation answers a leaked
   passphrase, and the same passphrase against the new public salt lets the
   leak re-derive the new key. Both travel CLI → daemon in the frame tail with
   explicit lengths (`PassphraseLen` pattern, `wire/msg.go:275`; rekey tail =
   current ++ new behind a new `NewPassphraseLen`), never argv, never a log,
   never formatted into an error. Two machines rekeying at once: exactly one
   wins (compare-and-set on `key_epoch` in the worker transaction); the loser
   changes nothing and says so — new code `epoch_race`, message: another
   machine rekeyed first, this machine now holds an old key, run
   `relevo db sync unlock` with the winner's new passphrase.
7. **Local keyring rows (named).** Secret table of the machine-local file:
   `sync.key` (newest epoch's key — name unchanged, every existing reader
   untouched), `sync.key.epoch` (canonical decimal newest epoch; absent = 1;
   a secret-table row so the `SecretReader` surface, `key.go:121-132`, reaches
   it without a KV handle), `sync.key.old.<e>` one per older epoch. Kv facts:
   `sync.remote_epoch`, `sync.own_head.v1` (highest seq this origin's own
   transport confirmed appending, written after every successful append).
   Local table `sync_epoch_cut(origin, epoch, seq)` — migration
   `030_sync_epoch_cut.sql`, machine-local and trigger-free beside
   `sync_signed_from`, monotone guard in the statement. All of it
   machine-local secrets: never on the allow-list (`allowedSecrets:135` stays
   the six names), never exported (allow-list checked both ways), never
   printed. `DeleteKey:155` removes all of it plus `KeyRemoteKDF` and
   `sync.remote_epoch`. Pinned: `sync.key.old.1` and `sync.key.epoch` are
   refused on import and never exported.

Worker/transport surface: new verbs `key_rekey` (compare-and-set; writes
kdf rows + `key_epoch` + `key_wrapped.<E>` + every origin's predicted cut in
one transaction after a fresh pull, refuses whole on a moved head or a moved
epoch, answers with a read-back like `KeyInit:96-124`) and `key_cut` (own
origin only — the worker knows the caller's origin from hello — forward-only,
seq bounded by the origin's log head; authenticity is the reader's tag check).
`ProtocolVersion` (`protocol.go:24`) goes 2 → 3 with the two new verbs (the
step-2 precedent; the pin at `bytes_test.go:176` updates). `LogTransport`
gains `KeyRekey` and `PublishEpochCut` (the `KeyInit` precedent,
`transport.go:97-104`), so MemTransport, the syncpipe Client/Supervisor
(`convert.go:122-171`, `supervisor.go:177-197`, verb list `client.go:71`) and
`StopTransport` (`internal/sync/stop.go`) each grow the two methods. `KeyMeta`
(`transport.go:114`) gains `Epoch` and `Wrapped map[int][]byte`; `MarkState`
(`transport.go:140`) gains `EpochCuts map[string]EpochCut{Epoch,Seq,Tag}`,
read by a `readEpochCuts` sibling of `readSignedFrom:175` and mirrored in
memfake (`memfake_key.go`, `memLog` fields `memfake.go:39-56`). A malformed
cut row comes back unparseable and holds its origin like a bad boundary — the
`readSignedFrom` comment's rule.

## 2. Seams (files, types, functions)

Read, do not change unless named:
- `internal/synclog/mac.go` — `EntryMAC:25` stays byte-identical;
  `BoundaryTag:47`/`writeMACField:76` are the tag pattern the cut tag copies.
- `internal/synclog/entry.go:46` `Entry` — no new field (answer A).
- `internal/syncworker/format.go` — `logFormatVersion:22` stays 3; no DDL.
- `internal/synclog/docimport.go` (`takeDocs`, `applyDoc`, `DrainPending`) —
  newest-wins and redelivery semantics unchanged; they inherit epochs through
  `verify`.
- `internal/synclog/reconcile.go` — its append path must update
  `sync.own_head.v1` too; find whether it flows through `keyed.appendTo`
  (`sign.go:101`) or appends directly, and route both through one helper.

Change:
- `internal/sync/key.go` — `SetKeys`/`ReadKeyring`/`ReadKeyringFrom` beside
  `SetKey:100`/`ReadKeyFrom:126`; `SigningKey:138` unchanged (newest only);
  `DeleteKey:155` extended.
- New `internal/sync/keywrap.go` — pure seal/open of an epoch key (answer to
  decision 2), the `secretcrypt.go` derivation pattern.
- `internal/sync/unlock.go` — `Unlock:34` and `VerifyJoinKey:117` unwrap and
  store the keyring; `RecordRemoteKDF:153` records `sync.remote_epoch`;
  `KeyState:169` gains `stale`; `DrainParkedDocs:191` verifies under the
  keyring.
- New `internal/sync/rekey.go` — `Rekey(ctx, local, transport, current,
  newPass, rand)`: order is decision 6's; it reads heads via `ReadMarks`,
  computes cut tags under the new key, calls `KeyRekey`, stores the keyring,
  signals the secrets doc.
- `internal/sync/secretcrypt.go` (356 lines; keep under 600 or split) —
  envelope v2, epoch-aware `SealSecret:245`/`OpenSecret:267`,
  `readSecretEnvelope:314` v1+v2 rules; allow-list untouched.
- `internal/config/secretdoc.go` — `Items:95` seals under the newest epoch;
  `check:173`/`applyIn:201` open under the keyring; `readKeyFrom:360` →
  keyring read; the stamp-keeping optional interface for answer C.
- `internal/synclog/verify.go` — `loadSigned:31` grows cut loading (tag
  check, monotone local record, badCut hold); `verify:67` grows the two-branch
  epoch rule and the unsigned-above-cut refusal.
- `internal/synclog/sign.go` — `ErrEpochMoved` sentinel beside
  `ErrLockedSignedOrigin:11`; the export gate reads the two local epoch facts
  (no extra transport call).
- `internal/synclog/docexport.go` — `docStamp:158` stamp-keeping branch,
  scoped by the optional interface.
- `internal/synclog/transport.go`, `memfake_key.go`, `memfake.go` — as §1.
- `internal/syncworker/keymeta.go` (269 lines; watch 600) — `KeyRekey`
  backend (pull, predict, one transaction, push, read-back), `key_cut`
  backend, `readEpochCuts`, server handlers beside `keyMeta:248`/`keyInit:260`;
  new refusal code beside `CodeBoundary`.
- `internal/syncworker/protocol.go` — verbs + `ProtocolVersion:24` = 3;
  request/response fields for the rekey spec and the cut.
- `internal/syncpipe/client.go:71`, `convert.go`, `supervisor.go`;
  `internal/sync/stop.go` — the two new methods.
- `internal/sync/steady.go` — `exchange:117` gates export and import on the
  stale facts (the locked branch :135-151 is the pattern) and publishes the
  own cut extension when `sync.own_head.v1` is above the effective cut;
  `drainImport:236` and `join.go:508-519`, `joincreds.go:64-83`,
  `unlock.go:191` switch `SetKey` → `SetKeyring`.
- `internal/db` — migration `030_sync_epoch_cut.sql` (024's pattern) +
  `sync_epoch_cut.go` (`EpochCuts`, `RaiseEpochCut` with the guard in the
  statement, `LowerSignedFrom:39` pattern);
  `internal/db/migrations/SCOPES.md` rows.
- `internal/db/wire/msg.go` — `SyncVerbRekey`, codes `epoch_race` and
  `machine_locked` (reuse `no_kdf`, `wrong_passphrase`), `NewPassphraseLen`;
  `internal/relevo/synctail.go` — a rekey intake splitter;
  `internal/relevo/syncverb.go:105` dispatch + new `syncverb_rekey.go`
  beside `syncverb_unlock.go:13`.
- `cmd/relevo/db_sync_rekey.go` + `db_sync.go:99`-area registration +
  `dbSyncKeyClause:156` stale case; intake reuses `dbSyncPassphraseTTY:38`
  and the package-var fakes (`db_sync_unlock.go:30-36`).

## 3. Ordered steps

### Round 1 — key material: epochs at rest, wrap chain, worker rekey verb

1. Add `internal/sync/keywrap.go` (pure: wrap subkey, derived nonce, AD
   binding, seal/open) — done when named tests pin round-trip, wrong-epoch AD
   refusal, and that an old key opens nothing newer.
2. Extend `KeyMeta` with `Epoch`/`Wrapped` through `transport.go`,
   `memfake_key.go`, `keymeta.go` reads (`key_epoch` canonicality,
   `key_wrapped.%`), syncpipe convert/supervisor/client and `StopTransport` —
   done when the existing key suite passes unchanged on an epoch-1 remote
   (absent row reads as 1) and new read tests pin the malformed-row refusal.
3. Add the local keyring: `SetKeys`/`ReadKeyring`/`ReadKeyringFrom`,
   `sync.key.epoch`, `sync.key.old.<e>`, extended `DeleteKey`, allow-list
   pins — done when round-trip tests pass and named tests refuse
   `sync.key.old.1`/`sync.key.epoch` on import and never export them.
4. Store the keyring on unlock and join: `Unlock`, `VerifyJoinKey` (answers
   with the unwrapped chain), `syncverb_join.go:106` `SetKey` → `SetKeys` —
   done when epoch-1 unlock/join tests pass byte-identical and a fabricated
   epoch-2 memfake remote unwraps to a full keyring; wrong passphrase stores
   nothing (mutation: skip the check → test fails).
5. Add worker `key_rekey`: pull, predict cuts from
   `SELECT origin, MAX(seq) FROM log GROUP BY origin`, one transaction with
   the CAS on `key_epoch`, all rows and the cuts, refuse whole on a moved head
   or moved epoch, push, read-back answer; `ProtocolVersion` 3; server handler
   and refusal code — done when memfake and worker tests pin: the winner
   writes, the loser changes nothing, a moved head refuses everything, and
   `key_epoch` advances exactly once under two racing rekeys.
6. Commit after each step; full gate once at the round end.

### Round 2 — epochs in the exchange: cuts, verification, stale stop, extension

1. Add migration `030_sync_epoch_cut.sql` + `internal/db/sync_epoch_cut.go`
   (monotone `RaiseEpochCut`) + the SCOPES row — done when db tests pin that
   a lower (epoch, seq) never replaces a higher one.
2. Add `MarkState.EpochCuts`: worker `readEpochCuts`, memfake cuts, syncpipe
   conversion — done when read tests pass, including a malformed row holding
   its origin like a bad boundary.
3. Teach the importer: `SetKeyring`, cut loading in `loadSigned` (tag check
   under the cut's epoch key, monotone local record, badCut hold), and
   `verify`'s two-branch rule plus the unsigned-above-cut refusal — done when
   the whole existing suite passes (epoch-1 degeneration) and named memfake
   tests pin: an old-key forgery above the cut is refused, history verifies by
   trial below it, and deleting/lowering/replaying the remote cut row changes
   nothing (each mutation-checked).
4. Add the stale stop: `RecordRemoteKDF` records `sync.remote_epoch`;
   `exchange` refuses export with `ErrEpochMoved` and holds the import whole
   while stale, one trouble line; `KeyState` `stale` + status clause — done
   when a two-machine memfake test pins that after a rekey the stale machine
   appends nothing, applies nothing, and its status names the passphrase
   change (mutation: drop the gate → test fails).
5. Add `sync.own_head.v1` (every successful append, both append paths) and
   the cut extension: worker `key_cut` (own origin, forward-only, head-
   bounded), published on unlock and on steady attempts while the own head is
   above the effective cut — done when the race test pins: an origin that
   appended under the old key before discovery is held, unlock vouches its own
   tail and the fleet applies it, and a forged entry above its own head stays
   refused (each mutation-checked).
6. Switch every verification site to the keyring (`steady.go:246`,
   `join.go:518`, `unlock.go:197`, `joincreds.go:77`; snapshot and pre-pass
   inherit through `verify`) — done when the full suite passes and one named
   test runs the snapshot path and one the pre-pass over a two-epoch log.
7. Commit after each step; full gate once.

### Round 3 — secrets v2, re-seal, rekey end to end

1. Add envelope v2 to `secretcrypt.go`: the `e` field, seal choice (v1 at
   epoch 1 byte-identical, v2 from epoch 2), keyring open, refusal of an
   epoch not held — done when named tests pin v1 bytes unchanged, v2
   round-trip under any held epoch, unheld-epoch refusal, and unknown
   version/field refusals (the 5a-direction refusal among them).
2. Wire the doc: `Items` seals under the newest epoch, `check`/`applyIn` open
   under the keyring, `readKeyFrom` → keyring — done when doc tests pass at
   both epochs and a locked doc still errors rather than emptying.
3. Add the re-seal stamp rule (`docStamp` branch scoped by the optional
   interface; `SignalDoc` at rekey, no stamp) — done when the concurrent-edit
   test pins that a re-seal never overrides a newer peer edit (mutation:
   restore the clock stamp → test fails) and that gates' aging stamps are
   unchanged.
4. Add `internal/sync/rekey.go`, wire verb `rekey` (`NewPassphraseLen`, tail
   splitter, codes `epoch_race`/`machine_locked`), `syncverb.go` dispatch and
   `syncverb_rekey.go` — done when memfake tests pin: the happy rekey (remote
   epoch 2, wrapped chain, every cut, local keyring, secrets signalled),
   wrong current passphrase writes nothing anywhere, a locked machine is
   refused, and the loser of a race changes nothing and says so.
5. Add `cmd/relevo/db_sync_rekey.go` + registration + intake (current:
   stdin/env/TTY; new: env/TTY twice; one stdin secret; equal-passphrase
   refusal) — done when the intake rules are pure-function tests in
   `internal/relevo` and flag tests in `cmd/relevo` over the package-var
   fakes, with no worker spawned and no network (CLAUDE.md rule, said here as
   required).
6. Prove the scenarios on memfake through the real steady pipeline: three
   machines — A rekeys while B edits a secret and C is offline; B and C
   unlock afterwards and the fleet converges on B's edit with every machine's
   secrets readable under the newest epoch; a joiner after the rekey verifies
   the whole history and reads every secret; the interruption case (remote
   written, local store skipped) recovers through unlock alone — done when
   each convergence test passes and fails when its rule is mutated.
7. Commit after each step; full gate once.

### Round 4 — docs, spec, plan twin, gate

1. Amend `internal/db/migrations/SCOPES.md`: the `sync_epoch_cut` table, the
   keyring rows under `secret`, the remote meta rows — done when the diff
   touches only those sections.
2. Amend the spec: §5's rotation line (built: epochs, cuts, wrapped chain, v2
   envelope, the rollout sentence, the limits list from answer B), §10's
   passphrase row (the stale machine's behaviour), §12 item 5 (5b built) —
   done when the diff touches only those sections.
3. Save the plan twin to `docs/plans/2026-10-16-sync-rekey-epochs.md`,
   including any MasterMind corrections — done when the file matches the plan
   the rounds ran from.
4. Regenerate the coverage baseline with `sh scripts/check-coverage.sh
   --write` IFF code moved between packages, and say so in the report; never
   lower a baseline to get green — done when
   `RELEVO_REQUIRE_COVERAGE=1 GOTOOLCHAIN=go1.27.2 make check` passes from a
   clean tree.
5. Commit after each step; full gate once at the very end (and again only if
   a later fix changes code).

## 4. What is deleted (closed numbered list)

1. The rule that a sealed secret reads only envelope version 1
   (`readSecretEnvelope`, `secretcrypt.go:335`): deleted, replaced by the
   v1+v2 epoch-bound rules; the unknown-version refusal itself stays and is
   what a 5a reader keeps.
2. Single-key import verification (`Importer.SetKey` + one-key `VerifyMAC` at
   `verify.go:84`): deleted on every verification path, replaced by the
   keyring trial under the cut rule; `SetKey` survives only as the exporter's
   newest-key signing input.
3. The rule that an unlock or a join stores exactly one key (`SetKey` at
   `unlock.go:59` and `syncverb_join.go:106`): deleted, replaced by `SetKeys`
   storing the newest key plus every older key it unwraps, and `DeleteKey`
   deleting the whole set.
4. The clock-advanced re-export stamp for a secrets key whose digest changed
   with no newer write stamp (the `clock.Now()` branch of `docStamp`,
   `docexport.go:178-181`): deleted for the secrets doc only — a re-seal
   carries the stamp the key already had; every other doc keeps the rule.
5. The spec's "Rotation (step 5b, not built)" line (§5): deleted in round 4,
   replaced by the built behaviour.
6. The `ProtocolVersion == 2` pin (`bytes_test.go:176-177`): replaced by 3,
   for the `key_rekey` and `key_cut` verbs.

## 5. What the report must include

- Per round: `git diff --stat` of the new commits (no amended or rebased
  remote-branch commits), the focused test commands run and their results, and
  the one full-gate run (`RELEVO_REQUIRE_COVERAGE=1 GOTOOLCHAIN=go1.27.2 make
  check`) and its result.
- For EVERY new rule — the cut rule (both branches), the unsigned-above-cut
  refusal, cut tag verification, cut monotonicity, the CAS rekey, the
  predict-verify head check, the stale export/import stop, the own-head
  bound on the extension, the wrap AD binding and direction, the v2 envelope
  epoch binding, the re-seal stamp rule, wrong-passphrase-stores-nothing on
  rekey and unlock, and each allow-list pin for the new local rows: the named
  test plus the mutation check (what was broken to make it fail).
- The 5a-compatibility evidence: the named tests that a v2 envelope, an
  epoch-2 entry and an old-passphrase unlock are refused, not misread.
- The three-machines convergence result, the joiner-after-rekey result and
  the interruption recovery result, all on memfake, no network; PATH-restricted
  evidence for new tests where the repo rule applies.
- The known-limits list from answer B, verbatim, and any place the builder
  found a rule unimplementable as written: the exact step and the code fact —
  a halt that surfaces a design error beats a green suite that bent a test.
- Coverage baseline: regenerated or not, and why; SCOPES and spec deltas
  (sections touched); the plan twin path.
