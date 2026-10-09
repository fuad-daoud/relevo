# Turso cloud-sync design

Issue: #473 (part 3/3). Status: design spec for review, 2026-10-03. Docs only:
no Go file changes in this round.

This spec decides the open questions and breaks the build into slices. Facts
it rests on: `internal/db/migrations/SCOPES.md` (the scope classification),
the compression spike (`docs/specs/2026-09-25-compression-spike.md` §4),
`internal/db/engine_turso.go` (the linked Turso library), `internal/db/compress.go`
(per-row zstd, migration 013), migrations 014–015 and 020–021, and the four
follow-up comments on #473 (conflict rule, shape update, #472 prerequisites,
directory gate).

## 0. Starting position (fixed, not re-decided here)

- Order: #680 (daemon owns `relevo.db`) → #466 (Turso driver) → #472
  (origin/link columns) → this issue. #475, #476 and #672 are closed.
- Go cannot exclude tables from sync (`tables_ignore` is hard-coded empty in
  the Go sdk-kit). Machine-local rows and secrets go in a **second,
  local-only file** the single owner opens. `SCOPES.md` classifies; this spec
  places each scope in a file (§1).
- Conflict rule is **last-push-wins** with **rollback-and-replay on pull**,
  per Turso's documented strategy. Anything two machines can write
  concurrently is append-only with non-colliding keys (#472 per-origin
  counters, `binding_event.seq`) or single-writer per row. Client/server
  remote-binding copies join by `link_origin`/`link_id`.
- Sync is **opt-in only**. Settings live in a machine-local `sync` config
  section that never syncs; the token lives in `secret` (`turso.token`) and
  never syncs. Directory gate: the privacy page, `claude-plugin/README.md`
  and the portal answer say sync sends transcripts/round files to the user's
  own Turso database; the MasterMind transcript copy stays removed
  (migration 020, never reintroduced).
- Wire facts: push sends CDC `full` rows as Hrana JSON (BLOBs base64, +33%);
  pull receives raw 4KB pages; nothing is compressed on the wire, hence
  per-row zstd before write (landed, migration 013). Bootstrap of an existing
  local DB goes through `turso db import` / `--from-file` / upload API (WAL,
  4KB pages, `wal_checkpoint(TRUNCATE)`).
- The linked library already carries the sync client: `tursogo` v0.8.1
  exposes `NewTursoSyncDb` over `TursoSyncDbConfig{Path, RemoteUrl,
  Namespace, AuthToken, ClientName, BootstrapIfEmpty, ...}` with `Push(ctx)`,
  `Pull(ctx) (bool, error)`, `Stats(ctx)` (`CdcOperations`,
  `LastPullUnixTime`, `LastPushUnixTime`, `NetworkSentBytes/ReceivedBytes`,
  `Revision`) and `Checkpoint(ctx)`. The token travels as a Bearer header.

## 1. The two files

| File | Syncs | Holds |
|---|---|---|
| `relevo.db` | yes | Every `SCOPES.md` shared-history table and nothing else: `binding_record`, `chains`, `binding_event`, `chain_event`, `chain_member`, `chain_check`, `round_file`, `installation` (the directory projection), mirror `repo`/`mastermind`/`binding`/`round`/`event`/`artifact`/`transcript`. Its `kv`, `secret`, `config_*`, `session_consent`, `ingest_cursor` and `config_import` tables exist but stay empty by convention. |
| `relevo-local.db` | never | Everything `SCOPES.md` calls machine-local: the `kv` namespaces listed there (`daemon`, `serve.*`, `planner/*`, `mastermind/*`, `claim/*`, `hooks.log`, `ledger`, `availability`, `latency`, `ui`, `agents-manifest`, `release-check`, `planner.pruned_at`, `ingested.archive.*`, `mirror-dedupe.*`, `zstd-compress.v1`, `origin-backfill.v1`, plus per-machine `sync.*` state from §3), the whole `secret` table, `session_consent`, `ingest_cursor`, `config_import`, all `config_doc`/`config_revision`/`config_meta` rows (§2), and the installation file's authority (`installation.json` beside the databases, never synced). |

- **D0: both files run the same migrations, so the schema is identical and
  every `internal/db` call site keeps working; placement is by
  table/namespace routing in the owner, not by a schema fork.**
  Rejected: a slimmed-down local schema (a second migration track that every
  future migration must keep in step; one drift bricks the owner).
- Rationale: routing is a short allowlist the split slice (S1) encodes once;
  a forked schema duplicates every future migration review.
- Acceptance: `TestLocalFileHoldsNoSharedRows`-style coverage in S1 asserts a
  seeded database splits with zero shared-table rows in the local file and
  zero secret/config/kv-local rows in the shared file.

## 2. Q2 config: which sections sync

- **Decision: no config section syncs. All ten sections (`candidates`,
  `agents`, `actors`, `accounts`, `policy`, `roles`, `prices`, `servers`,
  `hooks`, `workflows`) and both secrets (`client.key`, `typesafe`) stay
  machine-local in the second file.**
- Rationale: sections name local binaries and harnesses. `candidates` rows
  point at executables and providers installed on this machine; `servers`
  names endpoints this machine reaches; `accounts` holds this machine's quota
  pool. A synced row would route another machine's rounds at binaries that do
  not exist there. Worse, `config_doc` rows are single rows two machines
  would overwrite under last-push-wins with silent loss — the exact shape §0
  forbids. `SCOPES.md` already leans local; this spec decides it.
- Rejected: an allowlist sync (`prices`, `hooks`, `workflows` — "these look
  portable"). Small value, unsolved merge semantics, and a second sync flavor
  to maintain; a later spec can reopen one section at a time once sync itself
  works.
- The machine-local `sync` settings section and the `turso.token` secret are
  new residents of the same rule: the section lives in the local
  `config_doc`, the token in the local `secret`, and neither ever enters the
  shared file.
- Acceptance: S2 carries `TestConfigNeverSyncs` — change every section on
  machine A, sync, and assert machine B's sections are byte-identical to
  before.

## 3. Q4 timing: when push/pull run, and what the statusline shows

- **Decision: push-then-pull, in the background, at two moments: right after
  a round seals, and on an idle daemon tick (every 5 minutes). A sync never
  blocks user-visible work: bounded context, failures recorded to local
  `sync.*` kv, retried next tick.**
- Rationale: round seal is when the bulk bytes land (`builder.jsonl`,
  transcripts), so pushing there keeps machines close without polling the
  network every 2 s tick. The idle tick covers edits that are not round
  output (config-adjacent rows, installation touch, confirms). Push goes
  first so a pull has fewer unpushed local changes to roll back and replay.
  Bounding is load-bearing: a network stall must never stall a round.
- Rejected: sync on every write (chatty; every write pays network latency),
  blocking push at round close (a plane without Wi-Fi would hang sealing),
  pull-then-push (maximises rollback-and-replay churn).
- Statusline (cheap by construction — it runs about every second, so it reads
  local kv only, never the network): exactly four tokens —
  `sync:off` (disabled), `sync:ok` (last tick succeeded and no CDC backlog),
  `sync:behind` (last tick failed, or unpushed CDC ops exceed the S3
  threshold), `sync:err` (sync is enabled but the handle reports an error
  needing attention, e.g. authorisation refused). The daemon writes the
  underlying markers; the statusline formats them.
- Rejected: richer statusline states (counts, ages — a 1 s renderer shows
  state, detail lives in `:sync`); statusline network checks.
- Acceptance: S3 carries `TestSyncNeverBlocksSeal` (seal completes with the
  network blackholed) and `TestStatuslineReadsLocalOnly` (statusline tokens
  with no network handle present).

## 4. Q6 security: token, encryption, leak blast radius

- **Decision: one database-scoped bearer token per installation, minted with
  `turso db tokens create`, stored as `turso.token` in the local-only
  `secret`. Revocation is per installation: invalidating machine A's token
  cuts A off and touches no other machine. Encryption posture is TLS on the
  wire plus Turso Cloud at-rest; relevo adds no client-side encryption in v1.**
- Rationale: per-installation tokens make the common incident (lost laptop)
  a single revocation, not a fleet re-seed. A leaked token exposes the whole
  synced file read-write — builder transcripts, round files, bindings — which
  is precisely why §1 keeps secrets, tokens and config out of it: the blast
  radius is bounded by file placement, and placement is testable (§7).
- Rejected: one shared token for all of a user's machines (one leak forces
  every machine to re-seed); relevo-side row encryption in v1 (key
  distribution across installations unsolved, and ciphertext would blind
  rollback-and-replay and any future server-side reader).
- Acceptance: S2/S4 carry `TestTokenNeverLeavesMachine` (the token value
  appears in no CDC payload the fake client receives and in no shared-file
  bytes) and `TestRevokeOneKeepsOthers` (revoking A's token in the fake
  leaves B syncing).

## 5. Q7 leaving, identity, backfill, label, and the origin gate

- **Q7 turn-off flow (`relevo sync off`), exact:** (1) one final push attempt,
  best-effort and bounded — a failure does not block the rest; (2) mark
  disabled in the local `sync` section; (3) delete `turso.token` from the
  local `secret`; (4) close the sync handle. The local files keep full
  history and the daemon keeps serving them unchanged: a machine that leaves
  sync is a machine that never had it. The cloud copy is left alone — the
  user deletes it with Turso tooling; relevo never deletes cloud data.
  Re-enabling is the fresh-enable path (§6/S4), never a resume.
  Rejected: wipe-local-on-leave (destroys the user's record to punish
  leaving); auto-deleting the cloud copy (irreversible from a typo).
- **`repo` identity across installations:** one row per `(origin,
  origin_url)` / `(origin, common_dir)`, never merged across origins. The
  local machine reads its own origin's row for paths and identity; other
  origins' rows are read-only attribution context. Paths stay absolute and
  paired with their row's origin per `SCOPES.md` (shown with the writer's
  label, never opened). Rejected: a canonical merged repo row (destroys
  attribution; `common_dir` differs per machine anyway).
- **`client.key` moves before first sync:** the split slice (S1) migrates the
  whole `secret` table — `client.key`, `typesafe`, `serve.tls.*` — into the
  local file. Enable preflight refuses while any secret row remains in the
  shared file. Rejected: moving only `turso.token` and leaving the builder
  identity shared (one forgotten row uploads the remote-builder key).
- **Link backfill for pre-#693 remote bindings:** none, as migration 015
  records — an existing remote binding stays unlinked until it is bound
  again. A name-heuristic backfill is unsafe across origins (two machines can
  hold the same name for different bindings) and silent under
  last-push-wins. The `:sync` view (S5) lists unlinked remote bindings so the
  user can re-bind them.
- **Display label:** editable, by its owner only. Each installation writes
  only its own `installation` row (`InstallationTouch`), so the row is
  single-writer and last-push-wins is harmless. Rejected: immutable labels
  (a renamed machine would lie forever); centrally assigned labels (needs a
  writer election sync does not have).
- **Origin gate:** enable refuses while any `origin = ''` row remains in any
  origin-carrying table (`binding_record`, `binding`, `repo`, `mastermind`,
  `chains`). The refusal names per-table counts and points at the backfill
  (`BackfillOriginOnce`) / upgrade path. Rejected: auto-backfilling inside
  enable (a sync decision must not mutate history as a side effect).

## 6. Slice breakdown

Each slice is docs-plus-code later; stated here so the build order and the
tests are agreed. **No CI test in any slice reaches the network: the Turso
client sits behind an interface and every test uses a fake.**

- **S1 — second-file split.** The owner opens `relevo.db` + `relevo-local.db`
  (same migrations, §1 routing), migrates secret/config/kv-local rows once
  with a backup first, like the zstd and origin passes. Tests: routing
  (shared rows only in shared, local rows only in local), idempotent
  re-run, backup-then-migrate, rollback on failure.
- **S2 — config/token/status scaffolding behind a fake Turso client.** The
  local `sync` section (validate + store), `turso.token` set/delete with the
  value never logged, the four statusline tokens over local kv, and the
  `SyncClient` interface (`Push`/`Pull`/`Stats`/`Checkpoint`) with a fake
  recording calls. Tests: section validation, `TestTokenNeverLeavesMachine`,
  `TestConfigNeverSyncs`, statusline mapping, fake call-order.
- **S3 — Push/Pull wiring.** Push-then-pull after seal and on the 5-minute
  idle tick, bounded contexts, failure → `sync:behind`/`sync:err` markers,
  `Stats` surfaced (CDC backlog, bytes, `Revision`). Tests (all fake):
  order is push-then-pull, `TestSyncNeverBlocksSeal` with blackholed
  network, retry-after-failure, markers written on every outcome.
- **S4 — enable/disable with seed.** Enable preflight (origin gate §5, empty
  shared `secret`, compress pass done), seed decision (empty cloud → first
  push is the seed; existing local DB → documented Turso upload path, then
  enable; new machine → `BootstrapIfEmpty` pull), and the §5 turn-off flow.
  Tests: each refusal named (`TestEnableRefusesEmptyOrigin`,
  `TestEnableRefusesSharedSecrets`), seed matrix against the fake,
  disable-keeps-local-usable.
- **S5 — cockpit `:sync` view.** Status, last push/pull times, bytes sent and
  received, server revision, per-installation rows with labels, unlinked
  remote bindings, enable/disable affordances. Tests: renders from fake
  stats, unlinked list matches fixture, no network handle needed.

## 7. Acceptance

- Two machines share one record: A writes a binding, syncs; B syncs and
  reads it with A's origin and label attached
  (`TestTwoMachinesShareOneRecord`, fake client both ends).
- `origin = ''` refusal: a database with one empty-origin row in each of the
  five tables refuses enable naming all five counts
  (`TestEnableRefusesEmptyOrigin`).
- Secrets never uploaded — one named test per claim:
  `TestSecretsNeverUploaded` (fake receives no `secret`-table CDC),
  `TestTokenNeverUploaded` (`turso.token` value in no payload),
  `TestClientKeyNeverUploaded` (post-split shared file carries no
  `client.key`).
- Late enable costs only a longer first upload: a 400 MB-class existing DB
  enables through the upload path with no rework and no second conversion
  (`TestLateEnableSeedsOnce`).

## 8. Deliberately out of scope

The zstd dictionary for planner transcripts (spike recommendation 3);
client-side encryption; multi-user/team sharing semantics (one user, N
machines is the model); the cloud MVCC concurrent-writes toggle and `BEGIN
CONCURRENT`; partial sync; automatic token rotation; conflict UI beyond the
statusline token and `:sync`; syncing any config section (needs its own
spec); deleting cloud data.

## 9. Release gate (directory, from #835)

Before the release that enables sync: the privacy page, the
`claude-plugin/README.md` disclosures and the portal's data-handling answer
each say sync sends transcripts and round files to the user's own Turso
database. The MasterMind transcript copy stays removed.
