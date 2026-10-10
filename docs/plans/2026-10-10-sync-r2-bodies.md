# Sync everything, step 1: bodies in R2 (round plans)

Spec: `docs/specs/2026-10-10-sync-everything-design.md` §12 step 1. Drafted by a lite-planner round (se1-plan), then reviewed by the MasterMind: reconcile added to upload-before-append, head-hash agreement on the ref form, R2 credentials routed through enable and hello, the work rebalanced into four rounds, and the weekly object cleanup moved to step 2. The S3 client is hand-written SigV4 (five calls, no new modules). klauspost/zstd has no numeric level 9, so the seal level is `EncoderLevelFromZstd(9)` (SpeedBetterCompression).


---

## Step 1, round 1 of 4: blob store, S3 client, $ref shape, seal level

You are building step 1 of `docs/specs/2026-10-10-sync-everything-design.md`
(read §6, §7 "Counting", §11, §12 step 1). The four round plans are in
`docs/plans/2026-10-10-sync-r2-bodies.md`; this is round 1. Both files are
already committed on this branch; do not re-save them.

## Goal

Pure building blocks, no change to what sync does yet: the `BlobStore`
interface with an in-memory fake and a real S3/R2 client, the `$ref` shape and
4 KiB threshold, and the seal-time zstd level. `make check` green.

Out of scope: worker verbs, protocol version, exporter/importer/reconcile
wiring, staging I/O, credentials, byte counting, status, cleanup, any
`cmd/relevo` change.

## Declared scope

- NEW `internal/blobstore/blobstore.go`: `BlobStore`, `ErrNotFound`, `Key`
- NEW `internal/blobstore/mem.go`: `MemBlobStore`
- NEW `internal/blobstore/s3.go`: `S3Store`, a hand-written SigV4 client
- NEW `internal/blobstore/*_test.go`
- NEW `internal/synclog/blobref.go` and `blobref_test.go`
- `internal/db/codec.go` (encoder level only) and its test

## Steps

1. `internal/blobstore/blobstore.go`. Package comment: "blobstore holds the
   bodies sync moves outside the log: one object per stored value, addressed
   by its origin and digest."
   ```go
   var ErrNotFound = errors.New("blobstore: object not found")
   type BlobStore interface {
       Put(ctx context.Context, key string, body io.Reader, size int64) error
       Get(ctx context.Context, key string, w io.Writer) (int64, error) // ErrNotFound on 404
       Has(ctx context.Context, key string) (bool, error)
       List(ctx context.Context, prefix string) ([]Object, error)      // Object{Key string; Size int64; Modified time.Time}
       Delete(ctx context.Context, key string) error
   }
   func Key(origin, sha256hex string) string // "<origin>/<sha256hex>"; validates both (origin non-empty, no '/', digest 64 lowercase hex)
   ```
2. `mem.go`: `MemBlobStore` with a mutex, keeping a copy of the bytes and a
   `Modified` time from an injectable `now func() time.Time`. It counts
   `Puts` and `Gets` for tests.
3. `s3.go`: `S3Store{Endpoint, Bucket, KeyID, Secret string; Client
   *http.Client; now func() time.Time}`, with `NewS3Store(cfg S3Config)`.
   - Path-style URLs: `<endpoint>/<bucket>/<key>`.
   - AWS SigV4 with region `auto` and service `s3`, using
     `x-amz-content-sha256` for the payload hash. `Put` hashes the body it
     sends: buffer it, since bodies are a few MB at most.
   - Operations: PUT, GET, HEAD (`Has`), DELETE, and ListObjectsV2 with
     `prefix` and continuation tokens.
   - Status handling: 404 → `ErrNotFound`. Any other non-2xx is an error
     carrying the status and the S3 `<Code>` from the XML body. Never include
     the secret in an error.
   - No new module: `net/http`, `crypto/hmac`, `encoding/xml` only.
     `go mod tidy` must show no diff.
4. Tests for `s3.go`, against `httptest.Server` only:
   - Pin the canonical request and signature for one fixed request, using
     the AWS SigV4 documented example values for `GET` with an empty
     payload.
   - Check that `Put` sends `x-amz-content-sha256` equal to the body's
     digest.
   - Check that a 404 maps to `ErrNotFound`.
   - Check that List follows a continuation token.
   - Check that the secret never appears in an error string.
5. `internal/synclog/blobref.go`:
   ```go
   const BlobRefThreshold = 4 << 10 // stored (post-zstd) bytes; larger values travel as a $ref
   type BlobRef struct { SHA256 string `json:"sha256"`; Bytes int64 `json:"bytes"`; Codec int `json:"codec"` }
   func NeedsRef(stored []byte) bool                       // len(stored) > BlobRefThreshold
   func RefFor(stored []byte, codec int) BlobRef            // sha256 of stored bytes, exactly as stored
   func EncodeRef(r BlobRef) json.RawMessage                // {"$ref":{"sha256":…,"bytes":…,"codec":…}}
   func DecodeRef(v json.RawMessage) (BlobRef, bool, error) // ok=false when v is not a $ref object
   ```
   Add a why-comment on the threshold: it was measured on 2026-10-10. On the
   laptop's round files, 4 KiB moves 97% of bytes in 45% of rows; it also
   keeps tiny values out of the bucket.
6. `internal/db/codec.go`: build the shared encoder with
   `zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(9))`. klauspost has no
   numeric level 9; this maps to `SpeedBetterCompression`.
   - Add a why-comment: about 12% smaller than the default in a measurement
     on real runner streams, for a cost paid once at seal.
   - Frames written at the old level must still decode.

## Tests and mutations (name them exactly)

| Test | Mutation that must make it fail |
|---|---|
| `TestBlobRefThresholdPins4KiBStored`: 4096 stored bytes stay inline, 4097 become a ref; codec carried | `BlobRefThreshold = 8 << 10` |
| `TestDecodeRefRejectsMalformed`: wrong digest length, non-hex, negative bytes | drop the length check |
| `TestS3SignatureMatchesKnownVector` | change the canonical header order |
| `TestS3NotFoundIsErrNotFound` | return a generic error on 404 |
| `TestMemBlobStoreRoundTrip` | none needed |
| `TestCodecOldFramesStillDecode`: a frame made at the default level decodes after the level change | none needed |

## Rules

- `make check` green: gofmt, vet, golangci-lint, `go mod tidy`,
  `scripts/check-comments.sh`, `scripts/check-filesize.sh`, coverage within
  one point of `testdata/coverage-baseline.txt`.
- Functions ≤ 70 lines, files ≤ 600; no new exclusions.
- A new package needs no baseline change unless the coverage script asks;
  say what you did.
- CI has no network: tests use `httptest` and the fake only.
- Comments say why, never restate code. No issue numbers or history in code.

## Report

`git diff --stat` against the declared scope; the `make check` output; each
named test and each mutation tried, with before and after results; anything
you could not determine.

---

## Step 1, round 2 of 4: worker protocol v2, blob verbs, credentials, Turso bytes

You are building step 1 of `docs/specs/2026-10-10-sync-everything-design.md`
(§6, §7 "Counting", §9 "Worker protocol version 2"). The round plans are in
`docs/plans/2026-10-10-sync-r2-bodies.md`; this is round 2. Round 1 added
`internal/blobstore` (`BlobStore`, `MemBlobStore`, `S3Store`, `Key`) and
`internal/synclog/blobref.go`.

## Goal

The worker can move bodies to and from R2 through a staging folder, reports
exact Turso and R2 bytes, and gets R2 credentials from the machine's secrets.
The daemon side gains client calls for all of it, but the exporter and
importer don't use them yet. `make check` green.

Out of scope: exporter, reconcile and importer changes; byte accumulation in
kv; status display of bytes; cleanup; `publish_marks`/`compact`.

## Declared scope

- `internal/syncworker/protocol.go`, `worker.go`
- NEW `internal/syncworker/blob.go`
- `internal/syncworker/driver_turso.go`, `turso.go` (only where `Stats` and
  `hello` need it)
- `internal/syncpipe/client.go`, `convert.go`, `wiring.go`
- `internal/sync/enable.go`, `join.go`, `settings.go`, `token.go`, or a NEW
  `internal/sync/r2.go` for the credentials
- `cmd/relevo/db_sync.go` (flags only)
- tests for each

## Steps

1. **Protocol.** In `protocol.go`, set `ProtocolVersion = 2`.
   - Add verbs `put_blob` and `get_blob`.
   - Add request fields: `Key string` (`json:"key,omitempty"`) and
     `Staging string` (`json:"staging,omitempty"`), an absolute file path.
     Bodies never cross the pipe.
   - Add the R2 settings to `hello`: `R2 *R2Config` with
     `json:"r2,omitempty"`, where `R2Config{Endpoint, Bucket, KeyID, Secret}`.
     Like the token, it travels only in `hello`.
   - Add `Bytes int64` to the reply for `put_blob`/`get_blob`, and a
     `Skipped bool` for `put_blob` when the object already existed.
   - Add to `Stats`: `TursoSent`, `TursoReceived`, `R2Put`, `R2Get` (int64,
     snake_case JSON). They are cumulative for this worker's life.
   - Update the golden wire test.
2. **Version refusal.** In `worker.go`, `hello` refuses any version other
   than 2 with a message naming both versions, as today. Without an
   `R2Config`, the blob verbs fail with a clear "no R2 settings" error.
3. **Blob work.** NEW `blob.go`:
   - `putBlob(ctx, store, key, staging)`: `Has`, then `Put` if missing. It
     returns the bytes sent and whether it skipped. Before uploading, it
     checks that the staging file's sha256 equals the digest in the key, so
     a wrong file can never be published under a digest.
   - `getBlob(ctx, store, key, staging)`: `Get` into `staging + ".part"`,
     fsync, then rename. 404 → reply with code `blob_missing`, a new
     refusal code the client maps to a sentinel.
   - Bounded concurrency is not needed inside one call; the importer will
     issue calls a few at a time.
4. **Turso bytes.** In `driver_turso.go`, `Stats` returns
   `TursoSyncDbStats` instead of discarding it. Add
   `bytesDelta(before, after TursoSyncDbStats) (sent, recv int64)`, which
   treats a counter that went down as a reset and returns `after`. The
   backend snapshots stats around each push or pull and adds the deltas to
   its cumulative counters. Test it with a fake `syncDb` through the
   existing `newSyncDb` var.
5. **Client.** In `internal/syncpipe/client.go`, add
   `PutBlob(key, staging) (bytes int64, skipped bool, err error)` and
   `GetBlob(key, staging) (int64, error)`, plus their `callBound` entries
   (10 minutes, like export). Map `blob_missing` to a new
   `synclog.ErrBlobMissing`, defined in round 2 in `internal/synclog`.
   `Stats` carries the four counters through.
6. **Staging folder.** `func BlobStagingDir(sharedPath string) string` in
   `wiring.go` returns `filepath.Join(filepath.Dir(sharedPath),
   "sync-blobs")`, beside `ReplicaPath`. It's created with mode 0700 on
   first use.
7. **Credentials.**
   - Machine-local secrets `r2.endpoint`, `r2.bucket`, `r2.key_id`,
     `r2.secret`, stored and read like `turso.token`.
   - `relevo db sync enable` gains `--r2-endpoint`, `--r2-bucket` and
     `--r2-key-id`. The secret comes from env `RELEVO_R2_SECRET`, or from
     `--r2-secret-stdin`, which is refused together with `--token-stdin`
     (one stdin).
   - Preflight refuses enable when any of the four is missing and none is
     stored. Its `next` names the flags.
   - Disable deletes them along with the token.
   - The supervisor reads them and passes `R2Config` in `hello`.
   - Rule tests are pure functions in `internal/sync`. **CI has no network
     and no harness: no `cmd/relevo` test may run `db sync enable` or spawn
     a worker.** Test the flag-to-settings rule in `internal/sync` instead.
8. **Status.** `db sync status` shows R2 as configured or missing, with
   endpoint and bucket. It never shows the key id or the secret.

## Tests and mutations

| Test | Mutation that must make it fail |
|---|---|
| `TestWorkerRefusesWrongProtocolVersion` | accept `Version == 1` |
| `TestPutBlobRefusesDigestMismatch` | skip the sha256 check of the staging file |
| `TestPutBlobSkipsExisting` (`Skipped` true, no second `Put`) | none needed |
| `TestGetBlobMissingIsBlobMissing` | return a generic error on 404 |
| `TestDriverStatsDeltaCountsTursoBytes` | return zeros |
| `TestEnableRefusesWithoutR2` (pure preflight rule) | none needed |
| `TestStatusNeverShowsR2Secret` | none needed |

## Rules

- `make check` green: gofmt, vet, golangci-lint, `go mod tidy`,
  `scripts/check-comments.sh`, `scripts/check-filesize.sh`, coverage within
  one point of the baseline.
- Functions ≤ 70 lines, files ≤ 600 (`turso.go` is near the limit: put new
  code in `blob.go`); no new exclusions.
- CI has no network: tests use `httptest`, `MemBlobStore` and fake
  backends only.
- Comments say why, never restate code. No issue numbers or history in code.

## Report

`git diff --stat` against the declared scope; the `make check` output; each
named test and each mutation tried, with results; the exact wire shapes as
built; anything you could not determine.

---

## Step 1, round 3 of 4: export and reconcile send large bodies as $ref

You are building step 1 of `docs/specs/2026-10-10-sync-everything-design.md`
(§6 "Export"). The round plans are in
`docs/plans/2026-10-10-sync-r2-bodies.md`; this is round 3.
- Round 1 added `internal/blobstore` and `internal/synclog/blobref.go`
  (`BlobRef`, `NeedsRef`, `RefFor`, `EncodeRef`, `DecodeRef`).
- Round 2 added worker protocol v2: `PutBlob` and `GetBlob` on the
  `syncpipe` client, `BlobStagingDir`, and `synclog.ErrBlobMissing`.

## Goal

The exporter and reconcile never put a stored value over 4 KiB in an entry.
They upload it first and append a `$ref` in its place. `head`'s hash and
reconcile's comparison agree on that ref form, so a large row isn't
re-exported on every reconcile. `make check` green.

Out of scope: the importer (round 4), byte accumulation and status (round 4),
cleanup (step 2).

## Declared scope

- `internal/synclog/codec.go`, `export.go`, `reconcile.go`, `hash.go`
- NEW `internal/synclog/blobmove.go`
- `internal/synclog/memfake.go` (a fake mover over `MemBlobStore`)
- `internal/syncpipe/supervisor.go` / `wiring.go` (pass the mover and the
  staging folder)
- tests

## Steps

1. **The ref value in the codec.** In `codec.go`, add a column value type
   `RefValue{BlobRef}`.
   - `EncodeBody` writes a `RefValue` as `{"$ref": {...}}`, next to the
     existing `{"$blob": …}` tag.
   - `DecodeBody` returns a `RefValue` for a `$ref` object, never bytes.
   - Unknown tags stay errors.
   - Add a round-trip test.
2. **One rule for which values move.** In `blobmove.go`, the eligible
   columns are exactly `round_file.body`, `transcript.record_json` and
   `transcript.rendered`. Name them in one table so step 2's cleanup can
   reuse it. Then:
   ```go
   type BlobMover interface {
       PutBlob(key, staging string) (bytes int64, skipped bool, err error)
       GetBlob(key, staging string) (int64, error)
   }
   // refColumns rewrites an eligible []byte value over BlobRefThreshold into a RefValue
   // and returns the values that must be uploaded before the entry may be appended.
   func refColumns(origin, table string, row db.ExchangeRow) (db.ExchangeRow, []pendingBlob)
   type pendingBlob struct{ Key string; Bytes []byte; Ref BlobRef }
   func uploadPending(m BlobMover, stagingDir string, blobs []pendingBlob) error
   ```
   - The ref's codec comes from the row's own `*_codec` column. Read the
     column names in `internal/db/compress.go`.
   - `uploadPending` writes each value to `<stagingDir>/<sha256>` with mode
     0600, calls `PutBlob`, and removes the staging file afterwards, success
     or not.
3. **Export.** In `export.go`, after the drain builds each upsert and before
   `Append`:
   - Run `refColumns` on every row.
   - Upload all pending blobs of the batch.
   - Only then call `Append`.
   - If any upload fails, the batch is not appended and the outbox rows stay,
     exactly as a failed `Append` behaves today.
   - Deletes carry no body and never move.
4. **Reconcile.** In `reconcile.go`:
   - Hash the **ref form**: build the row, apply `refColumns`, then compute
     `BodyHash` over that body. The worker hashes the appended body
     (`turso.go` `bodyHash`), which is already the ref form, so the two
     agree.
   - Upload pending blobs before each chunk's `Append`, as in export.
   - Reconcile already reads every owned row, so hashing large values here
     costs one sha256 per value. Say so in a comment.
5. **Wiring.** The supervisor gives the exporter and reconcile the syncpipe
   client as their `BlobMover`, plus `BlobStagingDir`. `MemTransport` tests
   use a `MemBlobMover` over `blobstore.MemBlobStore`, keyed with
   `blobstore.Key`.

## Tests and mutations

| Test | Mutation that must make it fail |
|---|---|
| `TestExportUploadsBeforeAppend`: a mover that fails `PutBlob` leads to no `Append` and the outbox rows kept; a passing mover leads to the object existing before the entry | append first, upload after |
| `TestExportKeepsSmallValuesInline`: 4096 stored bytes inline, 4097 as a ref | none needed (round 1 pins the threshold) |
| `TestReconcileStableWithRefs`: reconcile, then reconcile again with no change, emits zero entries for a row holding a 50 KiB body | hash the local body without `refColumns` |
| `TestRefValueRoundTripsThroughCodec` | none needed |
| `TestStagingFileRemovedAfterUpload` | none needed |

## Rules

- `make check` green: gofmt, vet, golangci-lint, `go mod tidy`,
  `scripts/check-comments.sh`, `scripts/check-filesize.sh`, coverage within
  one point of the baseline.
- Functions ≤ 70 lines, files ≤ 600; no new exclusions.
- CI has no network: tests use `MemTransport` and `MemBlobMover` only. No
  `cmd/relevo` test spawns a worker.
- Comments say why, never restate code. No issue numbers or history in code.

## Report

`git diff --stat` against the declared scope; the `make check` output; each
named test and each mutation tried, with results; anything you could not
determine.

---

## Step 1, round 4 of 4: import fetches and checks, bytes per month, status

You are building step 1 of `docs/specs/2026-10-10-sync-everything-design.md`
(§6 "Import", §7 "Counting", §10). The round plans are in
`docs/plans/2026-10-10-sync-r2-bodies.md`; this is round 4, the last.
- Rounds 1–3 added `internal/blobstore`, `BlobRef` and `RefValue`.
- Rounds 1–3 also added worker protocol v2 with `GetBlob`, `ErrBlobMissing`,
  the `BlobMover` interface with `MemBlobMover`, and export and reconcile
  with refs.

## Goal

The importer turns every `$ref` back into the exact stored bytes before a
batch applies. It fetches them, checks the digest, holds the origin on a
transient failure, and latches when an object is missing. The daemon
accumulates exact bytes per month, and `db sync status` shows them against
quotas. `make check` green.

Out of scope: cleanup of unreferenced objects, marks, compaction and the
join guard (all step 2).

## Declared scope

- `internal/synclog/import.go` and NEW `internal/synclog/blobfetch.go`
- `internal/sync/steady.go`, NEW `internal/sync/bytes.go`, `settings.go`
- the `db sync status` rendering (find where it lives: `cmd/relevo/db_sync.go`
  calls into `internal/sync`; put the rule in `internal/sync`)
- `internal/sync/remoterefusal.go` (latch on a missing object)
- tests, including NEW `internal/synclog/blob_converge_test.go`

## Steps

1. **Fetch before apply.** In `blobfetch.go`:
   `resolveRefs(m BlobMover, stagingDir string, batch []Entry) error`
   replaces every `RefValue` in the batch's bodies with its bytes.
   - For each ref, it fetches `blobstore.Key(entry.Origin, ref.SHA256)` into
     `<stagingDir>/<sha256>`, at most 4 at a time.
   - It checks the file's size equals `ref.Bytes`, and that its sha256
     equals `ref.SHA256`.
   - It checks the row's `*_codec` column equals `ref.Codec`.
   - It reads the bytes and removes the staging file.
   - A digest mismatch is an error, never applied.
2. **Import.** In `import.go`, call `resolveRefs` for each batch before its
   transaction.
   - **A transient error** (network, a 5xx, a digest mismatch) returns
     before anything applies. The mark doesn't move and the origin is
     retried next attempt; the same "hold" behaviour as a gap today.
   - **`ErrBlobMissing`** becomes a latching refusal.
     - Its message names the table, the binding (resolve the record id to
       its name when the row has one), the round and the file name for
       `round_file`.
     - It names the owner id and seq for `transcript`, and the origin's
       label.
     - Latch through the existing path: make
       `internal/sync/remoterefusal.go` `IsPermanentRefusal` true for
       `synclog.ErrBlobMissing`. It then shows as `sync:err`, with the
       reason, until `relevo db sync retry`, the same as `ErrRemoteRefused`.
3. **Bytes.** NEW `internal/sync/bytes.go`:
   ```go
   type ByteCounters struct { TursoPush, TursoPull, R2Put, R2Get int64 } // json: turso_push, turso_pull, r2_put, r2_get
   func BytesKey(t time.Time) string // "sync.bytes." + t.UTC().Format("2006-01")
   ```
   - After each attempt, `steady.go` reads the worker's cumulative `Stats`
     counters and adds the delta since the last reading to the current
     month's kv row, in the machine-local file.
   - Use the same reset rule as round 2's `bytesDelta`: a counter lower
     than the last reading means the worker restarted.
   - `TursoSent` counts as push, `TursoReceived` as pull.
4. **Quotas and status.** Add to the `sync` settings `quota_turso_sync`,
   `quota_turso_storage` and `quota_r2_storage`, in bytes, with defaults of
   10 GB, 9 GB and 10 GB when zero.
   - `db sync status` prints this month's four counters and the Turso sync
     total as a share of its quota.
   - Write the formatting as a pure function in `internal/sync` and test it
     there. **No `cmd/relevo` test may run a subcommand that spawns a
     worker or reaches the network.**
5. **Convergence.** In `blob_converge_test.go`, two machines share
   `MemTransport` and one `MemBlobMover`. Machine A has a `round_file` with
   a 200 KiB body, a 3 KiB body, and a transcript with a large `rendered`.
   - After A exports and B imports, B's stored bytes and codec columns equal
     A's, byte for byte.
   - A second round of export and import moves nothing.
   - Then delete A's object from the store and re-import into a fresh
     machine C. This latches with the file named.

## Tests and mutations

| Test | Mutation that must make it fail |
|---|---|
| `TestImportChecksSha256OnFetch`: a tampered object is never applied, the mark doesn't move, and the error is transient | skip the digest comparison |
| `TestImportMissingObjectLatchesWithFile` | treat 404 as transient |
| `TestImportHoldsOriginOnFetchError` | apply the batch without the failed body |
| `TestBytesAccumulateAcrossWorkerRestart` | ignore the reset rule |
| `TestStatusShowsBytesAgainstQuota` | none needed |
| `TestBlobConvergeTwoMachines` | none needed |

## Rules

- `make check` green: gofmt, vet, golangci-lint, `go mod tidy`,
  `scripts/check-comments.sh`, `scripts/check-filesize.sh`, coverage within
  one point of the baseline.
- Functions ≤ 70 lines, files ≤ 600 (`import.go` is 532 lines: put new code
  in `blobfetch.go`); no new exclusions.
- CI has no network.
- Comments say why, never restate code. No issue numbers or history in code.

## Report

`git diff --stat` against the declared scope; the `make check` output; each
named test and each mutation tried, with results; the kv and status shapes as
built; what remains for the sandbox run (a real Turso database and R2 bucket);
anything you could not determine.
