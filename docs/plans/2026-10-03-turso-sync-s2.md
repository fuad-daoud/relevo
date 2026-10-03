# Builder-round plan: Turso sync S2 — config/token/status scaffolding (spec §6 S2 only)

Base: `b3e861836` (spec tip; S2 builds on it). Scope boundary: config + token + status + a fake client, zero packets — no real network in any test, no S3 wiring, no S4 enable, no S5 view. All work lands as new commits on top of the base; no amend, no rebase.

## Behaviour and cases

1. Local `sync` config section: validate + store, never synced. Accepts a well-formed sync settings body, rejects malformed bodies and unknown fields with nothing written; delete removes it. (Spec §2, `docs/specs/2026-10-03-turso-sync-design.md:63-86`.)
2. `turso.token` set/delete in the local-only `secret`: set stores, delete removes, absent delete is a no-op; the value never appears in logs, error strings, or any payload the client sends. (Spec §4, `docs/specs/2026-10-03-turso-sync-design.md:116-135`.)
3. Statusline mapping over local kv only: exactly four tokens — `sync:off` (disabled), `sync:ok` (last tick succeeded, no CDC backlog), `sync:behind` (last tick failed or unpushed CDC ops over threshold), `sync:err` (enabled but handle reports an error needing attention) — computed with no network handle present. (Spec §3, `docs/specs/2026-10-03-turso-sync-design.md:103-114`.)
4. `SyncClient` interface (`Push`/`Pull`/`Stats`/`Checkpoint`) plus a recording fake: the fake records call order and serves scripted stats/errors; the real Turso type is wrapped, never called in tests. (Spec §6 S2, `docs/specs/2026-10-03-turso-sync-design.md:189-194`.)
5. Negative cases: unknown config section rejected; malformed `sync` body writes nothing; token value in no CDC payload and no shared-file bytes (`TestTokenNeverLeavesMachine`); every section unchanged on B after A mutates all sections (`TestConfigNeverSyncs`).

## Seams (file:line — hand these to the builder, no searching needed)

- Spec: §0 wire facts `docs/specs/2026-10-03-turso-sync-design.md:38-43`; §1 two files `:46-61`; §2 `:63-86`; §3 `:88-114`; §4 `:116-135`; §6 S2 `:189-194`; §7 `:212-227`.
- Config read/write path: `internal/db/config.go:25-46` (`ConfigGet`), `:50-56` (`ConfigPut`), `:100-121` (`SecretGet`), `:126-139` (`SecretPut`/`SecretDelete`), `:141-157` (`SecretNames`).
- Section registry + validation to extend: `internal/config/config.go:23-40` (`Section`, ten constants, `Sections`), `:363-403` (`Validate` switch), `:407-437` (`Put`), `:549-594` (`PutSecret`/`SecretDelete`/`SecretNames`).
- Local kv for `sync.*` markers: `internal/db/kv.go:29-45` (`KVGet`), `:50-63` (`KVPut`), `:105-141` (`PrefixKV` namespacing pattern).
- kv namespace conventions: `internal/db/migrations/SCOPES.md:52-85` (machine-local list; new `sync.*` keys land here by the same rule), shared tables `:15-44`, per-user config `:92-103`.
- Statusline renderer: `cmd/relevo/status.go:239-298` (`runStatusline`, the `--line` body incl. local-only failure silence), `internal/view/statusline.go:52-68` (`RenderStatusLine`), `:483-498` (`StatusLineRows`), `:510-585` (`statusLineRowOf`); composition point `internal/relevo/statusline.go:20-29` (`MasterMindStatus`) — untouched by S2, named so the token row hooks in without forking it.
- Turso client surface (external module, not this repo): `tursogo@v0.8.1/driver_sync.go:37-94` (`TursoSyncDbConfig`), `:96-114` (`TursoSyncDbStats`), `:132-215` (`NewTursoSyncDb`), `:273-311` (`Pull`), `:315-329` (`Push`), `:332-362` (`Stats`), `:365-379` (`Checkpoint`), `:481-483` (Bearer header). This repo's `internal/db/engine_turso.go:45-80` opens local pools only and carries no sync calls.
- Single-file open S2 must not disturb: `internal/db/db.go:99-107` (`Open`/`OpenWith`), `DB` struct `:39-77`.
- Round rules: `CLAUDE.md:39-44` (`make check` gate), `:93-95` (no `cmd/relevo` test reaches the network — test the rule as a pure function in the new package), `:99-102` (`cmd/relevo` `TestMain` isolation), `:71-84` (≤70-line functions, ≤600-line files, no new lint exclusions).

## Seed-vs-code notes (said, not guessed around)

1. The seed's "`internal/db/engine_turso.go` (`NewTursoSyncDb`, …)" misplaces the symbols: verified they live only in the external `tursogo@v0.8.1/driver_sync.go` (lines above); `engine_turso.go` has no sync client. The plan wraps the external type behind the new interface instead.
2. "Assumes S1's two-file routing exists": on this base S1 has not landed — no `relevo-local.db`, no routing table, no `SyncClient` anywhere in `*.go`. Per the seed's fallback, S2 does not invent S1 internals; it names the seam S2 needs from S1 (step 1: a local-file `*db.DB` handle that config/token/`sync.*` kv reads bind to — today `db.Open`'s single handle, `internal/db/db.go:99-107`).
3. The seed cites "§7 (`TestConfigNeverSyncs`, `TestTokenNeverLeavesMachine`)": the names are real but live in §2 (`:84-86`), §4 (`:132-135`) and §6 S2 (`:192-194`); §7 (`:212-227`) names different tests (`TestSecretsNeverUploaded`, `TestTokenNeverUploaded`, …). Step 5 pins the S2 names where the spec actually defines them. None of this halts the round.

## Ordered steps (one line each: deliverable + done-criterion)

1. New `internal/sync` package skeleton holding the S1 seam type (local-file handle alias + `sync.*` kv key constants under `SCOPES.md:52-85`) — done when `go build ./internal/sync/` passes and no other package imports it yet.
2. `sync` section schema + `Validate`/`Put`/`Delete` extension in `internal/config/config.go:23-40,363-437` (one edit call to that file; no other section's validation touched) — done when `TestSyncSectionValidation` passes and unknown-section/malformed-body cases write nothing.
3. `turso.token` set/delete with redaction (store verbs over `internal/db/config.go:100-139` + never-logged error paths) — done when `TestTokenNeverLeavesMachine` passes and a grep for the fixture token over test logs finds nothing.
4. Statusline token mapping as a pure function in `internal/sync` over local kv reads (`internal/db/kv.go:29-63`), leaving `cmd/relevo/status.go:239-298` and `internal/view/statusline.go:52-68` unmodified — done when the statusline-mapping test passes with no network handle present.
5. `SyncClient` interface + recording fake in `internal/sync` mirroring `Push`/`Pull`/`Stats`/`Checkpoint` semantics from `driver_sync.go:273-379` — done when the fake call-order test passes and no test constructs a real `TursoSyncDb`.
6. `TestConfigNeverSyncs` pinning §2 acceptance (`design.md:84-86`: all sections mutated on A, B byte-identical) against the routing seam from step 1 — done when it fails if any section body is routed to the shared file.
7. Mutation pass, one mutation per behaviour test (break sync validation; echo the token into an error; route one section shared; make the mapper dial; reorder fake calls) — done when each named test fails on its mutation and passes restored.
8. Focused suites `go test ./internal/sync/... ./internal/config/... ./internal/db/... ./internal/view/...` green, then full `make check` green — done when both outputs are quoted verbatim in the report.

## Batching and file discipline

- Batch the read-only survey (spec + seams above) into one step before any edit.
- One edit call per file (`internal/config/config.go` once; new `internal/sync` files once each); no mechanical sweep needed; `cmd/relevo`, `internal/view`, `internal/db` stay read-only — untouched sites are named in Seams, not edited.
- Iterate the focused test command per step, fixing every reported error before the next run; run `make check` once at the end.

## Deleted behaviour (closed list)

1. Nothing is deleted: S2 is pure addition (new package + new section + new secret name + new kv keys + new interface/fake); no existing flag, verb, section, or rendering changes.

## Report must include

- Commits (new, on top of base `b3e861836`) + base SHA; focused-suite outputs then `make check` output, quoted with commands.
- Each behaviour test named with its mutation and the failing-vs-passing result.
- Explicitly left out: S3 push/pull wiring, S4 enable/disable with seed, S5 `:sync` view, any cloud MVCC/encryption work (spec §8).
- The three seed-vs-code notes above, restated with the file:line evidence.
