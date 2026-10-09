# S3 Push/Pull wiring — builder-round plan (spec §6 S3 only)

Base: `b3e861836` (spec tip; `git log` shows `b3e861836 docs: turso cloud-sync design spec (#473)` on top).
Worktree read: `/home/fuad/.local/state/relevo/.worktrees/.scratch/turso-plan-s3-001` (throwaway; changed nothing).

## 0. Base difference the seed must hear (not guessed around)

The seed assumes **S2 already landed** ("Assumes S2's `SyncClient` interface + fake and local `sync.*` kv; name the seam if the base differs"). **The base differs: S2 has not landed.** Verified by search over the tree:

- No `SyncClient` type, no `Push`/`Pull`/`Stats`/`Checkpoint` sync methods, no `sync:off|ok|behind|err` tokens, no `sync.*` kv keys anywhere in Go code. (`internal/db/stats.go:13` `DB.Stats()` is the unrelated SQLite file-size stat, not Turso `Stats(ctx)` with `CdcOperations`/`Revision`.)
- `internal/db/engine_turso.go:45-80` opens a plain `turso.NewConnector` pool; `NewTursoSyncDb` / `TursoSyncDbConfig` from spec §0 appear nowhere.
- `internal/db/migrations/SCOPES.md:50` mentions "per-machine `sync.*` state from §3" as a future resident of the local file; no key names or readers exist.
- `internal/relevo/statusline.go:20-55` (`MasterMindStatus`, `scopedStatus`) and `internal/view/statusline*.go` render with no sync token.

So Step 1 below **creates the S2-assumed seam with the exact names the seed mandates** (`SyncClient` with `Push`/`Pull`/`Stats`/`Checkpoint`, fake, `sync.*` kv helpers, four token mapping) as a clearly-marked prerequisite inside the S3 round — the smallest S2 surface S3 can consume — and every later step consumes only that seam. If the builder instead finds S2 landed on its branch, it uses S2's names and reports the substitution; it does not carry two seams.

## 1. Behaviour and cases

- **After-seal trigger:** whenever `sealRounds` seals ≥1 file for a binding, one bounded push-then-pull is scheduled; seal itself is unaffected (same return, same log lines, same removal semantics).
- **Idle-tick trigger:** at most one bounded push-then-pull per 5-minute window while the daemon ticks (default 2 s tick), skipped entirely when sync is disabled or no handle exists; a tick never waits on the network.
- **Order:** push-then-pull always (spec §3 rationale: fewer unpushed changes for rollback-and-replay). Pull-then-push is rejected and tested against.
- **Bounded context:** each `Push`/`Pull`/`Stats` call runs under a short timeout; expiry, blackholed network, and fake errors all surface as markers, never as a hung seal or tick.
- **Markers on every outcome** (local kv only): success with empty CDC backlog → `sync:ok`; success with backlog over threshold → `sync:behind`; failure/auth-refused → `sync:behind` or `sync:err` per S2's mapping; disabled → `sync:off`. Writers are the daemon sync paths only.
- **Stats surfacing:** CDC backlog (`CdcOperations`), bytes sent/received, `Revision`, last push/pull times are read from the client and persisted to local kv where `:sync` (S5) will later read them; S3 does not build the view.
- **Statusline reads local only:** token formatting calls kv, never the client; with no handle present all four tokens still render from kv (`TestStatuslineReadsLocalOnly`).
- **Retry:** a failed sync leaves the behind/err marker; the next trigger (seal or idle window) retries with no backoff state beyond the marker; no poisoned latch.

## 2. Seams (file:line in the read tree)

- Round-seal hook point: `internal/relevo/daemon.go:324-336` (`tickOne` calls `sealRounds` inside `WithLock`, then `reconcileWith`, `tx.Save`, `emitCommitted`); seal loop `internal/relevo/daemon.go:485-526` (`sealRounds`, per-round `tx.SealRound`, DONE-dir cleanup `516-525`). **Sync fires after seal commits, outside the lock** — never inside `sealRounds` or `Tx.SealRound`.
- Seal storage (untouched): `internal/store/seal.go:243-303` (`Tx.SealRound`), `Sealable` `internal/store/seal.go:104-123`, `RoundsOnDisk` `:72-91`, `StreamDrained` `:142-177`.
- Idle-tick slot: `internal/relevo/daemon.go:135-211` (`Daemon.Tick`, phases via `d.safely`), `Run` `:102-131`; interval flag `cmd/relevo/daemon.go:43` (`--interval`, 2 s default). New phase is a time-gated `d.safely("turso sync", …)` step, not a per-tick call.
- Runtime wiring: `internal/relevo/runtime.go:134-253` (`Runtime`; `Remote RemoteClient :221`, `DB *db.DB :172`, `Store *store.Store :140`) — new optional sync handle field lives beside `Remote`, nil means disabled.
- kv surface: `internal/db/kv.go:19-23` (`KV` interface `KVGet/KVPut/KVDelete`), `SCOPES.md` conventions `internal/db/migrations/SCOPES.md:46-90` (local-only list; `sync.*` is a new local namespace under the same rule).
- Statusline: `internal/relevo/statusline.go:20-55` (report builder; token mapping attaches here or in one new `sync_status.go`, formatting in `internal/view/statusline*.go`); tokens are spec-only today (`docs/specs/2026-10-03-turso-sync-design.md:103-109`).
- Spec authority: timing/order/rejections `docs/specs/2026-10-03-turso-sync-design.md:88-114` (§3), S3 slice `:195-199` (§6 S3), acceptance `:112-114` (§3) + `:212-227` (§7: `TestSyncNeverBlocksSeal`, `TestStatuslineReadsLocalOnly`).
- Untouched sites (named, not modified): `Tx.SealRound` semantics and removal; `Sealable`/`StreamDrained` predicates; `tickOne` lock/save/emit ordering; `Daemon.Run` ticker/cancel semantics; any `config_*`/`secret` rows (S2/S4); `:sync` view (S5); `SyncRemote`/`reconcileRemote` remote-binding paths in `internal/relevo/remote_sync.go` (different "sync", out of scope).

## 3. Ordered steps (each: deliverable + how to know it worked)

1. **S2-seam prerequisite (flagged base-difference): new `SyncClient` interface + recording fake + `sync.*` kv helpers + four-token mapper** — deliverable: one new small package or file (e.g. `internal/sync/sync.go`) exposing `SyncClient{Push/Pull/Stats/Checkpoint}`, `Fake` recording call order and injectable errors/stats, kv key constants and read/write helpers, `Token()` mapping kv→`sync:off|ok|behind|err`; worked: `go test` on the new package green, fake order test passes.
2. **Seal-hook scheduler: queue exactly one bounded sync after a seal that wrote ≥1 file, outside `WithLock`** — deliverable: hook call sited after the seal block in `tickOne` (or a `sealRounds` result returned out), firing async with bounded context, no seal-signature change; worked: seal log lines byte-identical, `TestSyncNeverBlocksSeal` passes.
3. **Idle-tick slot: 5-minute-gated `d.safely("turso sync")` phase in `Daemon.Tick`** — deliverable: elapsed-time gate (injectable clock), skip when disabled/no handle, failure contained by `safely`; worked: phase unit test pins at-most-once-per-window and skip paths.
4. **Bounded-context wrapper shared by both triggers** — deliverable: one function `syncOnce(ctx)` applying the timeout, calling push-then-pull, swallowing nothing (returns outcome); worked: blackhole/timeout test completes in bounded time with marker written.
5. **Marker writes on every outcome** — deliverable: success/backlog/failure/auth paths each write the specified kv marker (+ stats snapshot); worked: one test per outcome asserts kv contents (see §4).
6. **Stats surfacing to local kv** — deliverable: `CdcOperations`, bytes sent/received, `Revision`, last push/pull timestamps persisted where S5 will read; worked: fake-stats test asserts stored values match fake.
7. **Statusline local-only token** — deliverable: statusline path reads kv token only, no client reference; worked: `TestStatuslineReadsLocalOnly` passes with nil handle.
8. **Verification & report** — deliverable: focused suites green, then `make check` green; report quotes commands/outputs, commits+base SHA, mutations, out-of-scope list; worked: `make check` exits 0.

## 4. Tests (all against the fake; no network, no harness spawn, no `cmd/relevo` spawning subcommands per CLAUDE.md:93-95)

- `TestSyncPushThenPullOrder` — fake records call order; assert push precedes pull on both triggers. Mutation: swap the two calls → fails.
- `TestSyncNeverBlocksSeal` (spec §7) — seal with blackholed/blocking fake; assert seal returns, files land in `round_file`, tick/seal duration bounded. Mutation: make sync synchronous on the seal path → fails (timeout).
- `TestSyncRetryAfterFailure` — fail once, assert behind/err marker; succeed next trigger, assert `sync:ok` and stats updated. Mutation: keep first-failure latch → fails.
- `TestSyncMarkersOnEveryOutcome` (table: ok / behind-on-backlog / transport-failure / auth-refused / disabled) — assert exact kv marker per row. Mutation: drop any one write → its row fails.
- `TestStatuslineReadsLocalOnly` (spec §7) — nil client/handle, kv fixtures for all four tokens; assert rendered tokens. Mutation: add a client call on the statusline path → fails (nil deref / test hook).
- Each behaviour test gets exactly one mutation (break the pinned condition, confirm red), per §5.

## 5. Verification

- Focused: `go test -race -count=1 ./internal/sync/... ./internal/relevo/ -run 'TestSync|TestStatuslineReadsLocalOnly' -v` green.
- Full: `make check` green (covers `gofmt`, `go vet`, `golangci-lint`, comment/size scripts, `go mod tidy` check, `go test -race -count=1 -cover ./...` plus coverage gate per `Makefile:30-73` and CLAUDE.md:39-44).
- Report quotes each command and its output tail; a red focused suite is fixed before `make check`; `make e2e` is out of scope (CI-only, not part of `make check`).

## 6. What is deleted

1. Nothing. S3 deletes no behaviour, no flags, no kv keys, no UI.
2. If the S2-seam prerequisite duplicates names S2 later lands under, the follow-up renames there, not here; this round adds, it does not remove.

## 7. Commit discipline

New commits only on the round branch; never amend or rebase a commit already on the remote binding's branch (the client absorbs each closed round incrementally and a rewrite strands its base).

## 8. Report must include

- Commits + base SHA (`b3e861836`), files touched vs §2 seam list, and confirmation seal logic bytes are unchanged.
- Focused-suite and `make check` commands with quoted outputs.
- One mutation per behaviour test with before/after result.
- What was left out: S2 remainder if any, S4 enable/seed + turn-off flow, S5 `:sync` view, token rotation/encryption (spec §8).
- The Step 1 base-difference note: S2 absent at plan time; exact seam names created or adopted.
