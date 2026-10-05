# Plan: TUI fleet first load (issue #1032)

Base: this tree at `6dab64a2`. This round delivers the plan only -- no code
change ships with it. Every phase below is written so a builder can execute it
in order with `make check` green after each.

The problem: `relevo ui`'s first paint is ~4.6s on this machine because the
fleet refresh builds a row for **every** binding in the store (487 here) and
computes three figures no fleet pixel ever reads. The fix has two halves:
**narrow before building** (Phase 1/1b), and **stop paying for figures the
fleet does not draw** (Phase 2), followed by three cheaper-same-pixels passes.

## Preamble

**Order.** Phase 0 -> Phase 1 -> Phase 1b -> Phase 2 -> Phase 3 -> Phase 4 ->
Phase 7 -> (Optional A, B, C, any order, each independent of the others and
of each other).

- Phase 1 and 1b are one commit: 1b is the same defect in the chains reader.
- Phase 2 is the largest single win on first paint and must not be merged into
  Phase 1, so a missed Phase 2 delta is attributable.
- Phase 3 and Phase 4 both cut per-row database work. They are separable and
  both are worth their own commit, but either order is safe.
- Phase 7 is pure render cost: no behaviour, same pixels. It goes last so a
  golden failure points at one thing.
- Optional A only has a measurable effect after Phase 2 (fleet rows no longer
  call `liveStat` at all).

**Out of scope.** Do not touch:
- `internal/serve` beyond making the server `Source` pass the Phase 2 flag
  (`internal/ui/source.go:80-82`). `serve.FlatStatus` (`internal/serve/admin.go:150`)
  aggregates `AdminStatus` per owner, each of which builds a full
  `Status`; it must keep producing identical rows.
- `internal/relevo/chain_server_view.go:34` (`chainServerStatus`), which builds
  all rows for a server chain. It is a fourth `buildReport` caller; Phase 1
  routes the other three and Phase 4 makes `buildReport` itself cheaper, so this
  one gets the win for free. Do not narrow it in this plan.
- The gate-roles file probes (`internal/availability/gates.go:344`).

**Seed-vs-tree contradictions.** Both are recorded, neither is guessed around.

1. The seed names a board file `docs/boards/tui-fleet-bottlenecks.excalidraw`.
   **This tree has no `docs/boards/` at all** (`ls docs/` ->
   `design.md plans runbook.md specs superpowers`). This plan uses the issue
   body and the spike reports as the diagram source and does not invent the
   file.
2. The seed's spike 06 reported merge markers in
   `internal/relevo/remote_unreachable.go:120` blocking `internal/ui`.
   **That file does not exist at `6dab64a2`** and no conflict is present. Every
   line range cited below was re-verified against this tree. A builder that
   finds the file reappear halts and says which phase.

**Commands, for every phase.**
- Focused: `go test ./internal/<pkg> -run '<TestNames>'`, fixing every reported
  error before the next run.
- Once at the end of the phase: `make check`.
- No lint, file-size or coverage exclusion is added. If a phase moves code
  between packages, regenerate with `sh scripts/check-coverage.sh --write` and
  say so in the report. No baseline is ever lowered.
- `cmd/relevo` tests stay harness-free and network-free: a rule is a pure
  function in `internal/relevo`. `TestMain`'s `HOME`/`XDG_*` temp root and its
  `CLAUDE*`/`RELEVO_*` unsets are untouched.
- Functions stay at most 70 lines, non-test files at most 600.
  `internal/ui/view_fleet.go` is 1028 lines and `internal/relevo/status.go` is
  376: Phase 4 grows `status.go`, Phase 7 shrinks `view_fleet.go`. Neither
  phase adds an exclusion.

**Every phase's report includes:**
- the commits (new, never amended or rebased);
- the `make check` result;
- the named mutation, run for real: `break X -> test Y fails`;
- before/after numbers for the three probes in the Perf appendix, measured on
  this machine against the same store;
- `git diff --stat` against that phase's declared scope.

**Halting.** A phase that misses its declared delta by more than run-to-run
noise halts and reports the numbers. Do not lower the expectation to make a
phase look like it landed.

---

## The three perf probes

Every phase records these three, before and after, on the same machine and the
same store. The command is the same each time:

```
relevo status --all-masterminds --json    # P1: full fleet wall
relevo status --mastermind <id> --json    # P2: narrow scope wall (1 row)
relevo status --chains                    # P3: chains wall
go test ./internal/ui -bench BenchmarkFrame -benchtime 300x   # P4: frame profile
```

The phase-0 numbers below are the baseline. P4's benchmark is **added by Phase
0** -- it does not exist in this tree today, so Phase 0 is not just a number
paste.

---

## PHASE 0 -- baseline and the concurrency precondition

### Why first

Every later phase's claim is a number, so the numbers need a floor before any
code moves. And Phases 3 and 4 both introduce a bulk load shared across rows,
which only pays off if concurrent reads on one `*db.DB` are actually safe. That
is an assumption the tree has never tested, so it gets pinned here, not
discovered mid-Phase 4.

### Seams

- `internal/db/db.go`:
  - `DB.sqlDB` is a field at `:40`, not a method. The pool is built by
    `openPool` (`internal/db/engine_modernc.go:37`, or `engine_turso.go:45` on
    the other engine) and stored at `:228`/`:212` through
    `finishDirectOpen` (`:218`).
  - `DB.Tx` (`:349`) delegates to `DB.tx` (`:357`), which takes a **dedicated
    `*sql.Conn`** via `d.sqlDB.Conn(ctx)` (`:357`) and issues `BEGIN IMMEDIATE`
    (`:376`) with a busy-retry loop (`:378-397`).
  - Read-only calls (`KVGet` `internal/db/kv.go:29`, `RecordGet`
    `internal/db/record.go:102`, `EventsOf` `:308`) go straight to
    `d.sqlDB` with `context.Background()` and hold **no** `*sql.Conn` and **no**
    `*Tx`. There is no shared mutable state on the read path.
- `internal/store/lifecycle.go`:
  - `List` at `:29-37` delegates to `list` (`:427`).
  - `read` (`:114-120`) builds `&Tx{s: s}` with **no** `conn` field set -- it
    is a store-level handle, not a database transaction. `readAll` (`:122-124`)
    is the same. So a row builder calling `rt.Store.ReadLog`/`ViewedAt`/
    `PendingForMasterMind` never takes a transaction and never contends for the
    write lock.
  - This is the fact Phase 4's concurrency rests on, and it is asserted only in
    a comment today.
- Existing concurrency tests in `internal/db/db_test.go` (the seed's
  `:180,276,393-524` are approximate; the tests are at):
  - `TestConcurrentOpenAppliesEachMigrationOnce` `:176`
  - `TestDirectHandlesOnOneFreshPathWriteTogether` `:243`
  - `TestTxRetriesABusyBegin` `:395`
  - `TestTxGivesUpOnABusyBeginAfterTheDeadline` `:444`
  - `TestOpenWithShortBusyFailsFast` `:497`
  None of them drives concurrent *reads* through one `*DB`.

### Steps

1. Add `internal/ui/frame_bench_test.go` with `BenchmarkFrame` covering
   `Body`, `rows()`, `fleetListLines` and `view.SortRows` at 30 and 64 rows, at
   160x50, with `b.ReportAllocs()`. Build its rows through the existing
   `allStatesRows`/`goldenModel` helpers in `internal/ui/golden_test.go` so the
   benchmark and the goldens read the same shapes.
   - Done when `go test ./internal/ui -bench BenchmarkFrame -benchtime 300x`
     prints a line per case and the tree is otherwise unchanged.
   - This file **stays** for the rest of the plan; Phase 7 re-runs it.
2. Add the concurrency test (see Tests below).
   - Done when `go test ./internal/db -run TestConcurrentReadsShareOneDB -race`
     passes under `-race`, and the mutation below fails.
3. Record the three wall probes and the frame profile. The numbers go in this
   document's Perf appendix, replacing the "to be measured" placeholders.
   - Done when every cell of the appendix has a number from this machine.

### Tests

- `internal/db`: `TestConcurrentReadsShareOneDB` -- N goroutines (8) each run
  `RecordGet`, `EventsOf` and `KVGet` against one `*db.DB` for 200 iterations,
  under `-race`. It asserts every read returns a value consistent with what the
  single-threaded setup wrote, and it asserts `*db.DB`'s read path takes no
  `*sql.Conn`: a `Tx` handed out by the read path would show up as a
  `sql.ErrConnDone`/leaked-connection failure.
- `internal/ui`: `BenchmarkFrame` (a benchmark, not a test -- it pins nothing
  and asserts nothing; it exists to produce numbers).

### Mutation

- **Break:** make `kvGet`/`RecordGet`/`EventsOf` on `*DB` route through
  `d.Tx(...)` (i.e. take a dedicated connection and `BEGIN IMMEDIATE`) instead
  of straight to `d.sqlDB`.
  **Fails:** `TestConcurrentReadsShareOneDB` -- 8 concurrent readers all
  serialise on the write lock, and under `-race` plus the busy-retry deadline
  the test exceeds it or returns `db: tx begin: ...` instead of reading.
  Run it before writing Phase 4; if this mutation does *not* fail, the
  precondition does not hold and Phase 4 halts.

### Done when

- The Perf appendix has all three walls and the frame profile at both row
  counts, cold and warm.
- `TestConcurrentReadsShareOneDB` is committed and its mutation was run.
- `make check` green.
- `git diff --stat` names only `internal/ui/frame_bench_test.go` and
  `internal/db/db_test.go`.

---

## PHASE 1 -- narrow the scope before building any row

### Why

`ScopeReport` (`internal/relevo/scope.go:26-40`) is a **pure post-filter**. It
throws away rows the machine already paid to build. On this machine
`relevo status --mastermind <id>` returns **1 row** and costs **4.6s** -- the
same 4.6s as the full fleet -- because `cmd/relevo/status.go:148` calls
`relevo.Status` (every binding) and only narrows at `:163`.

`scopedStatus` (`internal/relevo/statusline.go:35-55`) already does the right
thing -- `List` -> `scopeBinding` -> `buildReport` -> `applyChains(scopedChains)`
-- so the shape to reuse is already in the tree and is already tested. This
phase routes `cmd/relevo/status.go` and `runStatusChains` through it.

### Seams

- `internal/relevo/scope.go`:
  - `Scope` `:17-21`, `ScopeReport` `:26-40`, `scopeBinding` `:47-55`.
  - `ResolveScope` `:90-113` already produces the `Scope`; nothing here changes.
- `internal/relevo/statusline.go`:
  - `MasterMindStatus` `:20-29`, `scopedStatus` `:35-55`, `scopedChains`
    `:59-87`.
  - **`scopedStatus` must become the one entry point**, so it needs to accept a
    `Scope` that is not a single-mastermind scope: today it is only ever called
    with `Scope{MasterMindID: id}`. `Scope{All:true}` and `Scope{Named:true}`
    must pass through it unchanged.
- `cmd/relevo/status.go`:
  - `:139` `isChain := target != "" && statusChain(rt, target)`
  - `:140` `sc, _, err := statusScope(...)` -- the scope is already resolved
    **before** the report is built. Only the build call is wrong.
  - `:144-149` `rep, err = relever.Status(...)` / `relevo.ChainStatus(...)`
  - `:157-162` `if !isChain { rep, err = filterReport(rep, target) }`
  - `:163` `rep = relevo.ScopeReport(rep, sc)`
  - `:303-325` `runStatusChains` -> `relevo.ReadChainsScope(ctx, rt, sc)`
  - `:30-40` `filterReport`, kept -- a **named** view is `Scope.Named`, and a
    name takes no scope filter (`scope.go:15-16`, `:48-50`). `filterReport`
    stays as the post-check for the named case only.
- Flags (`cmd/relevo/status.go:79-89`): `--all`, `--all-masterminds`,
  `--mastermind`, `--name`, `--line`, `--chains`. No flag is added or removed.

### Steps

1. Export `ScopedStatus` (or make `MasterMindStatus` take a `Scope`) in
   `internal/relevo/statusline.go`, and widen its guard so `Scope.All` and
   `Scope.Named` reach `buildReport` with the un-narrowed binding set.
   - Done when `MasterMindStatus`'s existing callers
     (`cmd/relevo/status.go:273`, `:287`, and
     `internal/relevo/statusline_mastermind_test.go`) are unchanged and their
     tests pass without edits.
2. In `cmd/relevo/status.go:144-149`, build the non-chain report through the
   scoped path with the already-resolved `sc`, and keep `filterReport` for the
   named case and `ScopeReport` as the pure post-check that still runs at
   `:163`.
   - Done when `relevo status --mastermind X --json` returns a row set
     **byte-identical** to today's, and `relevo status --name N` still refuses
     an unknown name with `binding_not_found`.
3. Same for `runStatusChains` (`:303-325`) -- already scoped, see Phase 1b.
   - Done when `relevo status --chains --mastermind X` is unchanged.

### Behaviour that must not change

| Case | Rule |
|---|---|
| empty store | zero rows, no error, both before and after |
| all-DONE hidden | `Scope{All:false}` drops DONE via `scopeBinding` (`:54`); `--all` keeps it (`Scope{All:true}`) |
| named view (`--name`, `status <chain>`) | `Scope.Named` bypasses narrowing entirely (`scope.go:48-50`); `filterReport` still narrows to the one binding; `status <chain>` still skips `filterReport` (`:154-162`) |
| `DoneHidden` | still computed by `view.HideDone`, which now sees fewer rows to count -- the count must be identical |
| remote builder rows | untouched: `scopeBinding` reads `b.MasterMindID` off the stored binding, not off a local probe |
| refusal | `--mastermind` + `--all-masterminds` stay exclusive (`scope.go:93-98`); an unresolvable identity stays a `ScopeRefusal` naming the next step |

### Tests

- `internal/relevo/scope_test.go` (extends the existing fixture, which already
  holds `a1`, `a2`, `a-done` under one mastermind and `b1` under another, plus
  one chain each):
  - `TestScopedStatusReachesTheSameRowsAsScopeReport`: builds the full report,
  applies `ScopeReport`, and separately calls the scoped builder; the two row
  name sets and `DoneHidden` are equal, for `Scope{}`, `Scope{All:true}`,
  `Scope{Named:true}` and a single-mastermind scope.
  - `TestScopedStatusBuildsNoRowOutsideTheScope`: a `Runtime` whose store
    records a per-binding build (a counting `store.List` wrapper or a build
  counter on the row builder) shows **zero** builds for a foreign-mastermind
  binding under a narrow scope.
- `cmd/relevo/status_scope_test.go` (the fixture `seedTwoMastermindScope`
  already exists there):
  - `TestStatusNarrowScopeRowsAreUnchanged` -- `status --mastermind
    architect-a --json` names `a1 a2 cha`, unchanged.
  - `TestStatusAllMasterMindsKeepsEveryMastermind`.
  - `TestStatusNameBypassesNarrowing` -- `status --name a-done` still returns
    the DONE row.
- Unchanged and still green: `internal/relevo/scope_test.go`'s
  `TestScopeReportGivesEveryStatusSurfaceTheSameRows` and
  `TestScopeReportKeepsDoneOnlyWhenAsked`; `cmd/relevo/status_scope_test.go`'s
  `TestStatusSurfacesAgreeOnOneScope`,
  `TestStatusExplicitScopeFlagsOverrideResolution`,
  `TestStatusChainsFollowTheTheSameScope`, `TestStatusAllShowsTheScopedDoneRow`.

### Mutation

- **Break:** in the scoped path, pass the un-narrowed `bindings` to
  `buildReport` (i.e. keep `relevo.Status`'s behaviour) while still running
  `ScopeReport` afterwards.
  **Fails:** `TestScopedStatusBuildsNoRowOutsideTheScope` -- the foreign
  mastermind's `b1` and the DONE `a-done` get built. Rows are still correct
  after the post-filter, which is exactly why a name-set assertion is not
  enough here and the build counter is the real pin.
  Second mutation, same phase: drop `sc.Named` from `scopedStatus`'s guard.
  **Fails:** `TestStatusNameBypassesNarrowing`.

### Perf

- P2 (`status --mastermind <id> --json`) drops from the P1 number to a small
  fraction of it -- the whole point is that 1 row no longer costs 487 builds.
  Expect the ~30x class the issue names.
- P1 (`status --all-masterminds --json`) is **unchanged**: `--all-masterminds`
  is `Scope{All:true}`, which narrows nothing.
- P3 unchanged (Phase 1b owns it).
- P4 unchanged: no render code is touched.

### Done when

- Both row sets byte-identical, `make check` green, mutation run and reported.
- `git diff --stat` names only `internal/relevo/statusline.go`,
  `internal/relevo/status.go` (if the option is threaded there),
  `cmd/relevo/status.go`, and the two test files.

---

## PHASE 1b -- the same shape fix in the chains reader

### Why

`ReadChainsScope` (`internal/relevo/chains_doc.go:64-96`) scopes its **chains**
at `:69` and then throws that scope away: `:74` lists **every** binding and
`:78` calls `buildReport(ctx, rt, bindings)` for all of them, ignoring the
error (`:78` is `rep, _ :=`). So `status --chains --mastermind X` builds the
whole fleet to answer for one chain's members -- the seed's ~10.3s.

This is the identical defect to Phase 1 in the same package, and it is one
commit with Phase 1.

### Seams

- `internal/relevo/chains_doc.go:64-96` (`ReadChainsScope`):
  - `:65` `rt.Store.Chains()`
  - `:69` `chains = scopedChains(chains, sc)`
  - `:70-72` empty-chain early return
  - `:74-77` `rt.Store.List()` -- every binding
  - `:78` `rep, _ := buildReport(ctx, rt, bindings)` -- **the dropped error**
  - `:79-86` `repByName` and `storeBindings` maps
  - `:88-93` one `chainDocEntry` per scoped chain
- `internal/relevo/chains_doc.go:107` (`chainDocEntry`) reads members out of
  `repByName` at `:144-149` and **silently drops a member whose row is absent**
  (`:147` `if bs, ok := repByName[mName]; ok`). Narrowing must not change that
  behaviour for a member the *scope* excluded -- but it must not accidentally
  start hiding a member the scope *kept* either. This is the whole risk of the
  phase and the mutation test below.
- `internal/relevo/scope.go:47` (`scopeBinding`), `:59-87` (`scopedChains`).

### Steps

1. Narrow `bindings` with `scopeBinding` before `buildReport`, using the same
   `Scope` the chains were narrowed with.
   - Done when `status --chains --mastermind X` output is byte-identical to
     today's and full-scope output is byte-identical to today's.
2. Stop discarding the `buildReport` error at `:78`. `buildReport` is
   abort-on-first-error (`internal/relevo/status.go:82-90`), so a store failure
   must surface here exactly as it does on the `status` path -- an
   `internal/relevo/unused_gates`-style silent nil is how today's line hides a
   real failure.
   - Done when a store that fails to read a log makes `status --chains` fail
     with the store's error, where today it prints chains with missing member
     rows.
3. If narrowing can drop a chain's **member**, keep the member's row. A chain
   that is in scope is shown with the members it has; a member the scope hides
   (another mastermind's) is the `ScopeReport` outcome today and stays it.
   - Done when `TestReadChainsScopeKeepsEveryScopedMemberRow` passes and the
     pre-change full-scope output is unchanged.

### Tests

- `internal/relevo`:
  - `TestReadChainsScopeNarrowsBeforeBuilding` -- a build counter shows the
    foreign mastermind's bindings are not built under a narrow scope.
  - `TestReadChainsScopeFailsOnAStoreError` -- a `Status` whose log read fails
    returns the error instead of a half-populated doc.
  - `TestReadChainsScopeKeepsEveryScopedMemberRow` -- under `Scope{}` and under
    `Scope{All:true}` every member row of every listed chain is present.
- Unchanged and still green: `cmd/relevo/status_scope_test.go`'s
  `TestStatusChainsFollowTheSameScope`;
  `internal/relevo/chain_status_test.go`.

### Mutation

- **Break:** delete the `scopeBinding` narrowing in `ReadChainsScope`, keeping
  the `scopedChains` call at `:69`.
  **Fails:** `TestReadChainsScopeNarrowsBeforeBuilding` (the counter shows every
  binding built) and `TestReadChainsScopeKeepsEveryScopedMemberRow` stays
  green -- which is the trap this phase exists to avoid, since a chain member
  excluded by scope is exactly the case the mutation must catch.
  Second mutation: drop the member row when it is absent from `repByName`
  (delete the `ok` check at `:147` and append a zero row).
  **Fails:** `TestReadChainsScopeKeepsEveryScopedMemberRow`.

### Perf

- P3 (`status --chains --mastermind X`) drops by the same order as P2. **On this
  machine the store holds 67 chain rows but no live chain output, so P3
  measures ~0.07s today and cannot show Phase 1b's win** -- the reader returns
  at `chains_doc.go:70-72` before it builds anything. Phase 1b therefore ships
  on its correctness and its mutation test, and the report says plainly that
  P3 was not a usable probe here.
- P1, P2, P4 unchanged.

### Done when

- Byte-identical output on both scope levels, `make check` green, both
  mutations run, `git diff --stat` inside `internal/relevo`.

---

## PHASE 2 -- the fleet/detail split

### Why

After Phase 1, a **narrow** scope is fast and a **full** fleet refresh is not --
and the full refresh is exactly what `relevo ui` does every 2s. Three per-row
computations on that path produce values no fleet pixel reads:

| Figure | Cost | Fleet reader |
|---|---|---|
| `Live` | `git rev-parse --git-dir` + `git diff --numstat` per open round (`internal/git/snapshot.go:189-194`) | none |
| `Tail` | whole-file `logTail` or a 64 KiB-window `streamTail` per row per tick (`internal/relevo/transcript.go:199-204`, called at `internal/relevo/headless.go:1459-1463`) | the detail pane only |
| `LiveUsage` | `Peek` + `Fold` per row (`internal/relevo/usage.go:26-46`) | the detail pane / round head only |

Every renderer already has the nil path: `internal/view/render.go:227-231`
(`LiveUsage` nil -> `LastUsage`), `internal/view/render.go:139-141` (`Live`
nil -> no diff line), `internal/ui/round_head.go:33-37`
(`LiveUsage`/`LastUsage` fallback). So the fleet row shape already tolerates
nil; what is missing is the gate.

### Seams

- `internal/relevo/status.go`:
  - `Status` `:65-80`, `buildReport` `:82-102`, `statusRow` `:118-376`.
  - `Live`: `:326-328` -- `if !b.Builder.Remote() { row.Live = liveStat(ctx, rt, b) }`
  - `LiveUsage`: `:317-319` -- `if !b.Builder.Remote() { row.LiveUsage = peekUsage(ctx, rt, b, now) }`
  - `Tail`: reached through `headlessStatus` at `:178-179`
    (`internal/relevo/headless.go:1453-1488`), whose tail block is `:1459-1463`.
  - `liveStat` itself: `internal/relevo/livestat.go:32-76`, cache key
    `b.Name + "@" + b.RoundBaselineTree` at `:47`, TTL `liveStatCacheTTL = 5s`
    at `:15`.
  - `peekUsage`: `internal/relevo/usage.go:26-46`, deadline `liveDeadline =
    500ms` at `:20`.
- `internal/ui` -- **two** fleet entry points, not one:
  - `mastermindSource.Status` `internal/ui/source.go:47-49` -> `relevo.Status`
  - `liveSource.Status` `internal/ui/live.go:89-92` -> `relevo.Status`
    (**this is the one `relevo ui` actually runs**: `internal/ui/ui.go:80`
    builds `liveSource{live}` and hands it to `RunSource`).
  - `serverSource.Status` `internal/ui/source.go:80-82` ->
    `serve.FlatStatus` (`internal/serve/admin.go:150`), which aggregates
    `AdminStatus` (`:66`) per owner, each of which builds a full `Status`. It
    **shares the builder**, so the flag has to reach it too or the server fleet
    keeps paying for `Live`/`Tail`/`LiveUsage` it does not draw.
- The detail pane's row source -- **the gap this phase has to close**:
  - `internal/ui/view_round.go:184` reads `row(mergeRows(env.Report, r.extra), r.pane.detail.name)`
  - `internal/ui/round_pane.go:416` and `:532` set `p.pane.report = mergeRows(env.Report, r.extra)`
  - `internal/ui/round_pane.go:404-406` uses `p.report` for the header
  - So today the detail pane reads **the fleet report's row**. There is no
    per-row fetch: grepping `DetailRow|SingleRow|StatusOne|statusOne` across
    the tree returns nothing. **Phase 2 must add one.**
  - The existing per-key fetches are the shape to copy: `fetchStatus`
    (`internal/ui/fetch.go:108-119`, all-or-nothing `statusMsg`), `fetchReport`
    (`:211`), `fetchFor` (`:873`).
  - `newRoundView` (`internal/ui/view_round.go:72`) sets `rv.extra` with a
    chain's member rows; that path must keep working for a chain member that
    the single-row fetch cannot resolve.
- `internal/ui/shell.go`: `Init` `:151-156` (`tea.Batch(fetchStatus, tick)`),
  single-flight tick `:176-178`, `updateStatus` `:252`. `defaultInterval = 2s`
  at `internal/ui/ui.go:56`, `minInterval = 500ms` at `:55`.

### Steps

1. Add one `Status` option, defaulting **off** (so `relevo status`,
   `relevo status --json`, `relevo statusline` and every existing caller keep
   the figures they have today). Name it for what it is: `Live`/`Tail`/
   `LiveUsage` are the *detail* figures. A `Detail` option that defaults to
   **true** and is set **false** by the fleet is the safer shape -- a new caller
   that forgets it gets today's behaviour, not a silent loss of figures.
   *(plan's choice: `Detail bool`, default true, fleet passes `false`.)*
   - Done when `internal/relevo` compiles and the existing `status` tests are
     unchanged and green.
2. Gate `Live` (`:326-328`), `LiveUsage` (`:317-319`) and the tail block
   (`headless.go:1459-1463`) behind it. `Tail` lives inside `headlessStatus`,
   so thread the flag through `statusRow` -> `headlessStatus` rather than
   gating the call site -- the tail must not be built and then thrown away.
   - Done when a `Status` with the option off returns rows with
     `Live == nil`, `LiveUsage == nil` and `Headless.Tail == nil`, and every
     other field byte-identical to the same store read with the option on.
3. `mastermindSource.Status` (`source.go:47-49`) and `liveSource.Status`
   (`live.go:89-92`) pass the option off. `serverSource.Status`
   (`source.go:80-82`) passes it off **if** `serve.FlatStatus` can carry it;
   if it cannot in this phase, the plan says so in the report and leaves the
   server path un-gated -- it must not be a silent regression.
   - Done when the fleet JSON carries no `live`, `live_usage` or `headless.tail`
     field on any row, and the non-live fields are byte-identical.
4. **Add the detail pane's own row fetch.** A new `fetchStatusRow`
   (`internal/ui/fetch.go`, beside `fetchStatus`) that resolves one key through
   `Source.Runtime` and builds one **full-detail** row, plus the matching
   `relevo` entry point (one binding, one row, `Detail` on). The round view
   keeps a `detailRow` it merges in **over** the fleet row when it arrives, so
   opening a row restores all three figures without a second paint of stale
   figures.
   - Done when: open a row whose fleet report has nil `Live`/`LiveUsage`/`Tail`
     and the round head shows all three, and the fleet JSON stays nil.
   - This is the largest single sub-step of Phase 2 and the one most likely to
     slip. If it slips, Phase 2 does **not** ship: dropping the figures without
     a detail-side fetch loses information a human can currently see.
5. A row with no detail fetch in flight keeps the fleet's nil figures; there is
   no placeholder figure and no half-populated row.

### Behaviour that must not change

| Case | Rule |
|---|---|
| empty store | `LiveUsage` nil and no `Peek` call; `Live` nil and no git call |
| all-DONE hidden vs `--all` | untouched; the DONE rule is Phase 1's |
| named view (`--name`, `status <chain>`) | keeps `Detail` on -- those surfaces draw the figures (`RenderStatus`, `render.go:227-231`) |
| remote builder rows | `Live` and `LiveUsage` are already skipped for `b.Builder.Remote()` (`:317`, `:326`); the tail comes from the remote view (`status.go:189-191`). Unchanged. |
| sealed / legacy-log rounds | `builderTail` (`transcript.go:199-204`) picks `logTail` when `LogPath == BuilderLogPath(name, round)` and `streamTail` otherwise; both are inside the gate, both are exercised by the detail fetch |
| missing usage reader / closed round | `peekUsage` returns nil when `rt.Usage == nil` or `RoundStartedAt.IsZero()` (`usage.go:27`); nil is what the fleet row now always carries |
| unread marker | `viewed_at` batching is Phase 4; Phase 2 does not touch `Unread` (`status.go:340-355`) |

### Tests

- `internal/relevo`:
  - `TestStatusWithoutDetailCarriesNoLiveFigures` -- a row with an open round,
    a wired Git and a wired usage reader comes back with `Live`, `LiveUsage`
    and `Headless.Tail` all nil, and every other field equal to the same read
    with detail on.
  - `TestStatusWithDetailIsUnchangedByTheOption` -- byte-equal reports.
  - `TestStatusDetailFalseCallsNoGitAndNoPeek` -- a counting `Git` and a
    counting usage `Reader` record **zero** calls.
- `internal/ui`:
  - `TestFleetReportCarriesNoDetailFigures` -- the fleet shape.
  - `TestDetailPaneRestoresAllThreeFigures` -- a nil-fleet row plus a detail
    fetch shows `Live`, usage and tail in `round_head`/`render`.
  - `TestDetailFetchFailureKeepsTheFleetRow` -- a failing single-row fetch
    leaves the fleet's nil figures; it never paints a partial or a stale-full
    row.
- Unchanged and still green: `internal/ui/pane_test.go` and
  `internal/ui/split_test.go` golden shapes; `internal/view/render.go`
  `LiveUsage`-nil and `Live`-nil output; `internal/ui/round_head.go:33-37`.

### Mutation

- **Break:** force the fleet option off for one row that does have live figures
  (i.e. ignore the flag in `statusRow`).
  **Fails:** `TestFleetReportCarriesNoDetailFigures` -- `Live`/`LiveUsage`/
  `Tail` are non-nil on a fleet row.
  Second mutation, the one that matters: make the detail fetch return a row
  built with the option **off** as well.
  **Fails:** `TestDetailPaneRestoresAllThreeFigures` -- the round head falls
  back to `LastUsage` and shows no diff.

### Perf

- P1 drops by the git and usage cost: the issue names ~0.6s of git fleet-wide.
  Expect the largest single drop of any phase.
- P2 also drops: the narrow scope now builds few rows, but each no longer pays
  git.
- P3 unchanged.
- P4 unchanged (no render code touched). Note that P4 measures `Body`, which
  reads no figure.

### Done when

- Fleet JSON has nil for all three figures with identical non-live rows;
  opening a row restores all three; `make check` green; both mutations run.
- `git diff --stat` names `internal/relevo/status.go`, `internal/relevo/livestat.go`
  (no), `internal/relevo/headless.go`, `internal/relevo/statusline.go` (no),
  `internal/ui/source.go`, `internal/ui/live.go`, `internal/ui/fetch.go`,
  `internal/ui/view_round.go`, `internal/ui/round_pane.go`, and `internal/serve`
  only if step 3 reached it.

---

## PHASE 3 -- one log read per binding

### Why

`statusRow` reads the binding's log at `internal/relevo/status.go:234` and then
throws the entries away for two facts:

- `ViewedAt` (`:350`) is a **separate** `RecordGet` per row
  (`internal/store/paths.go:337-347`), even though the `List` that produced the
  binding already read that record.
- `PendingForMasterMind` (`:357`) is a **second full log read**: the store's
  `pendingForMasterMind` (`internal/store/log.go:484-497`) calls
  `s.readLog(name)` at `:485`, which is `RecordGet` + `EventsOf` all over again
  (`internal/store/log.go:434-454`). The entries are already in hand at
  `status.go:234`.

So every row pays two full `RecordGet` + `EventsOf` pairs. This phase removes
the second.

### Seams

- `internal/relevo/status.go`:
  - `:234` `entries, err := rt.Store.ReadLog(b.Name)` -- the read that is kept
  - `:238-264` `PlanRound`, `WaitingOn`, `Last`, `LastPayload`, `LastClose`
  - `:265-270` the `KindDiff` scan
  - `:271-298` `RoundFacts`, `PriorTokensOf`, `LastUsage`, `Spend`
  - `:340-355` the `Unread` marker, which calls `rt.Store.ViewedAt(b.Name)` at `:350`
  - `:357-366` `PendingForMasterMind` and the `PendingInfo{Round, Kind, TS}` it fills
- `internal/store/log.go`:
  - `ReadLog` `:279-287`
  - `PendingForMasterMind` `:307-316` -> `Tx.PendingForMasterMind` ->
    `pendingForMasterMind` `:484-497`, whose `s.readLog` at `:485` is the
    second read. Its rule is **oldest-first unconfirmed to-mastermind**:
    `e.Direction == DirToMasterMind && !e.Confirmed` (`:491-492`).
  - `pendingForMasterMindThrough` `:500-514` does the same read; it is a
    **write-path** sibling (used by delivery) and is out of scope.
  - `readLog` `:434-454`, `readLogAfter` `:458-478`
- `internal/store/paths.go`: `ViewedAt` `:337-347`, `MarkViewed` `:322-335`

### Steps

1. Derive `Pending` from the `entries` already held at `:234`, using exactly
   `pendingForMasterMind`'s rule (`DirToMasterMind && !Confirmed`, oldest
   first). Do it in `internal/relevo`, not in the store: the store's function
   keeps its own read for every other caller.
   - Done when `Pending` is byte-identical on a store holding an unconfirmed
     payload, a confirmed one, and neither.
2. `viewed_at` arrives from the `List` record instead of a per-row `RecordGet`.
   This needs the store to surface `ViewedAt` on the binding it already loaded
   -- `internal/store/lifecycle.go:29-37` (`List`) -> `list` (`:427`). If that
   is not a pure addition, the alternative is a **bulk** `ViewedAt` for the
   whole report, which is Phase 4's mechanism anyway; prefer it and say which
   one shipped.
   - Done when the unread marker is byte-identical, including the
     no-stamp-at-all case.
3. Keep `PendingForMasterMind`'s store signature and behaviour untouched. It
   has non-`Status` callers.

### Behaviour that must not change

| Case | Rule |
|---|---|
| empty log | no `Pending`, no `Unread`, `LastSeq == 0` |
| oldest-first | two unconfirmed payloads -> the **older** one is `Pending`, as today |
| confirmed payload | never `Pending` |
| unread marker with no `viewed_at` stamp | `Unread = true` whenever a report entry exists (`:351`) |
| a report "consumed by chain " | breaks the scan before `ViewedAt` (`:347-349`); unchanged |
| delivery path | `pendingForMasterMindThrough` (`:500`) untouched |

### Tests

- `internal/relevo`:
  - `TestPendingIsDerivedFromTheHeldEntries` -- the fixture crafts a log whose
    **oldest** unconfirmed to-mastermind payload carries a different
    mastermind payload than the newest report; the row's `Pending` names the
    older one.
  - `TestUnreadIsUnchangedWhenViewedAtComesFromTheListRecord`.
  - `TestUnreadIsTrueWithNoViewedStamp`.
  - `TestStatusReadsEachLogOnce` -- a counting store wrapper over `ReadLog`
    shows exactly **one** `ReadLog` per binding per `Status`.
- `internal/store`: no change is required here, so no new store test. Existing
  `internal/store/log.go` tests must pass unedited.

### Mutation

- **Break:** derive `Pending` from the **newest** unconfirmed entry (iterate
  forward and keep overwriting) instead of the oldest.
  **Fails:** `TestPendingIsDerivedFromTheHeldEntries` -- with the fixture above
  the row names the newer payload.
  Second mutation: fall back to `rt.Store.PendingForMasterMind` instead of
  deriving.
  **Fails:** `TestStatusReadsEachLogOnce` -- the count doubles.

### Perf

- P1, P2, P3 all drop by one `RecordGet` + `EventsOf` pair per row.
- `internal/store/lifecycle.go:114-124` is untouched, so the Phase 0
  precondition is not exercised for the first time here.
- P4 unchanged.

### Done when

- One log decode per binding, unread/pending cells byte-identical,
  `make check` green, both mutations run.

---

## PHASE 4 -- bulk kv, one ledger load

### Why

Per row, on top of everything else, `Status` still pays:

| Read | Site | Cost |
|---|---|---|
| `MasterMinds.Get` | `status.go:162` -> `internal/mastermind/registry.go:103`, `KVGet` at `:111` | one kv row per row, and the records are shared |
| `Channels.Live` | `status.go:30` -> `internal/delivery/channel.go:78`, `liveFrom` `:91-115` | one kv `KVGet` per row |
| `Waits.Live` | `status.go:57` -> `internal/delivery/waitclaim.go:80` | one kv `KVGet` per row |
| `ViewedAt` | `status.go:350` -> `internal/store/paths.go:337` | one `RecordGet` per row (Phase 3 removes it) |
| ledger | `status.go:99` `availability.Gates` **and** `status.go:100` `UnusedProviderGates` | **two** full `LoadLedger` reads per report |

`KVKeys` (`internal/db/kv.go:78-103`) is the bulk primitive already present --
and note it is itself `SELECT key FROM kv ORDER BY key` for the **whole** kv
table, filtered by prefix in Go at `:96-101`. With 427 kv rows here that is one
query returning 427 keys to find 166 of them; the same shape in a bulk
`List` is acceptable, but the plan says so rather than pretending it is indexed.

### Seams

- `internal/db/kv.go`: `KV` interface `:19-23`, `KVGet` `:29`, `KVTx` `:145-150`,
  `DBTxKV` `:154-157`, `TxKV` `:161-175`, `kvKeys` `:84-103` (whole-table
  select + Go filter at `:96-101`)
- `internal/db/record.go`: `RecordGet` `:102`, `RecordList` `:154`,
  `RecordListArchived` `:171`, `RecordGetByID` `:187`, `EventsOf` `:308`
  -- **no bulk events variant exists**; `EventsOf(recordID, afterSeq)` is
  per-record
- `internal/store/lifecycle.go`: `List` `:29-37` -> `list` `:427`
- `internal/store/log.go`: `ReadLog` `:279-287` -> `readLog` `:434-454`
- `internal/store/paths.go`: `ViewedAt` `:337-347`
- `internal/mastermind/registry.go`: `Get` `:103` -> `getFrom` `:107`, `KVGet`
  `:111`; `List` `:179` -> `listFrom` `:183`, `KVKeys` `:184` then a `KVGet`
  per key at `:191`; `registryKey` `:17`, prefix `mastermindKeyPrefix`
- `internal/delivery/channel.go`: `Live` `:78-89`, `liveFrom` `:91-115`
- `internal/delivery/waitclaim.go`: `Live` `:80`
- `internal/availability/ledger.go`: `LoadLedger` `:103-111` -- **one kv row**
  (`ledgerKey`), so "bulk" here means *load once*, not many
- `internal/availability/gates.go`: `Gates` `:311-329` -> `LedgerGates` `:277`
  -> `LoadLedger` `:282`; plus `rolesMissingGates` `:344` (the per-report file
  probes, explicitly **out of scope**)
- `internal/relevo/unused_gates.go`: `UnusedProviderGates` `:15-52`,
  `LoadLedger` at `:20` -- the second read
- `internal/relevo/status.go`: `buildReport` `:82-102` sets `rep.Gated` at
  `:99` and `rep.Unused` at `:100`; `mastermindRoute` `:24-40`;
  `waitLive` `:49-59`
- `internal/relevo/runtime.go`: `AvailabilityDeps(rt)` `:346`, the `Git`
  interface seam `:49-53`

### Steps

1. **One ledger load per report.** Load the `Ledger` once in `buildReport` and
   project it through both `LedgerGates`-equivalent and
   `UnusedProviderGates`-equivalent paths. This needs either a
   `Ledger`-taking variant of each (keeping the `LoadLedger`-taking ones as
   thin wrappers for the other five callers: `gates.go:63`, `gates.go:204`,
   `candidate.go:254`, `headless.go:701`, `remote_gates.go:116`) or a
   per-report cached loader.
   - Done when the `kv` `ledger` key is read **once** per `Status` and both
     `Gated` and `Unused` are byte-identical, including the load-error case
     (stderr once, gates nil).
2. **Bulk mastermind records.** One `Registry.List` (or a
   `KVGet`-per-key loop **hoisted out of the row loop**) resolved into a
   `map[id]Record` before the loop. `MasterMindName` (`:162`) becomes a map
   lookup. A binding with no registry, a forgotten record, and a `Get` error
   all still leave the field empty, as the comment at `:158-160` requires.
   - Done when the kv read count for `mastermind.` keys is O(masterminds), not
     O(rows).
3. **Bulk claims and waits.** Hoist `Channels.Live` / `Waits.Live` out of the
   row loop into one bulk load each, resolved into maps keyed by
   `MasterMindID` / binding name. The failure directions must be preserved
   exactly: today every failure (no store, unreadable row, read error) reads as
   **not live** (`status.go:45-48`, `:57-58`). A bulk load that returns an error
   must mark *every* row not-live, not fail the report -- `buildReport` is
   abort-on-first-error and a claims read error must not take `status` down.
   - Done when `MasterMindRoute`, `MasterMindRouteLive` and `WaitLive` are
     byte-identical, including a dead-PID claim and a stale-TTL claim.
4. **Bulk `viewed_at`.** One `RecordList` (or a bulk `ViewedAt`) for the whole
   report, if Phase 3 did not already remove the per-row read.
   - Done when the unread marker is byte-identical and the record read count is
     O(1) rather than O(rows).
5. **Concurrency, optional and only if Phase 4's own measurement needs it.**
   `buildReport` may build rows concurrently **only if** it keeps
   abort-on-first-error with **no partial report**: cancel on first error,
   return `view.Report{}`. `store.read` (`internal/store/lifecycle.go:114-120`)
   builds `&Tx{s: s}` with no `conn`, so the read path holds no shared mutable
   state -- but Phase 0's `TestConcurrentReadsShareOneDB` is the gate on this
   sub-step, and it must have been run. **`internal/relevo/status.go` is 376
   lines today and every bulk map in step 2-4 grows it**: a row-build context
   struct (the maps) plus a per-row function fits under the 600-line file cap,
   but `statusRow` is already 258 lines against a 70-line **function** cap --
   `make check`'s exclusion list must be consulted, and a phase that pushes
   `statusRow` further may have to split it. No exclusion is added; if the split
   is needed, split it.

### Behaviour that must not change

| Case | Rule |
|---|---|
| empty store | no bulk read errors; zero rows; `Gated`/`Unused` as today |
| missing usage reader / closed round | untouched (Phase 2) |
| unread marker preserved when `viewed_at` is batched | `status.go:350-355` semantics, including the "consumed by chain " break at `:347` |
| a dead or stale claim | not live, exactly as `liveFrom` decides (`channel.go:104-107`), stale rows still deleted by the reader (`channel.go:113`) |
| ledger load error | stderr once, gates nil -- **not** an error returned by `Status` |
| a binding with no mastermind record | `MasterMindName == ""`, no error (`status.go:161-165`) |
| other tenants | `RecordList`/`KVKeys` are already owner-scoped at the db layer; the bulk call must keep that scope |

### Tests

- `internal/relevo`:
  - `TestStatusReadsTheLedgerOnce` -- a counting `db.KV` shows one read of the
    `ledger` key per `Status`, with `Gated` and `Unused` byte-identical to a
    baseline report.
  - `TestMasterMindNamesComeFromOneBulkLoad` -- remove one record from the bulk
    map and the row's `MasterMindName` goes empty (and only that row's).
  - `TestRouteAndWaitLiveSurviveAMissingBulkEntry` -- drop the claim for one
    mastermind: its rows read `pull`/not-live, every other row is unchanged.
  - `TestViewedAtComesFromTheBulkMap` -- drop one binding's stamp and only that
    row's `Unread` flips.
  - `TestBuildReportFailsFastWithNoPartialReport` -- a store that errors on the
    3rd binding returns `view.Report{}` and no rows.
- `internal/db`: no new test -- the bulk surface reuses `KVKeys`/`RecordList`.
  Existing `internal/mastermind/registry.go` and `internal/delivery` tests must
  pass unedited.

### Mutation

- **Break:** delete one key (a claim, a wait, a mastermind record, a
  `viewed_at`) from the bulk map before the row loop.
  **Fails:** the matching `Test...FromTheBulkMap` / `Test...FromOneBulkLoad` --
  one row loses exactly that one fact and the rest of the report is unchanged.
  Second mutation: load the ledger twice again (restore `UnusedProviderGates`'s
  own `LoadLedger`).
  **Fails:** `TestStatusReadsTheLedgerOnce`.

### Perf

- P1, P2, P3 all drop by the statement count. The seed names ~1.8k statements
  at the issue's fleet size; **on this store the size is 487 rows / 6133
  events / 427 kv rows, so the expected drop is larger.** Measure the statement
  count with a counting `db.KV`/`db.DB` wrapper in a test and quote it in the
  report; do not quote the seed's number.
- P4 unchanged.

### Done when

- Statement count down by the measured order, rows and gates byte-identical,
  `make check` green, both mutations run.
- The report says whether `statusRow` had to be split for the 70-line cap.

---

## PHASE 7 -- render the same pixels for less

### Why

`Body` is ~1.28ms at 64 rows on this machine, and it recomputes the same list
five times per cycle. `fleetView.rows(env)` (`internal/ui/view_fleet.go:161-194`)
calls `view.SortRows` (a full copy + sort), regroups, and re-filters -- and it
is called from:

| Caller | Line |
|---|---|
| `Update`, `statusMsg` | `view_fleet.go:377` |
| `Update`, `tea.KeyMsg` | `view_fleet.go:385` |
| `Update`, `esc` | `:402` |
| `Update`, `a` | `:420` |
| `repointed` | `:467` |
| `fleetListLines` | `:813` |
| `cardBlock` | `:882` |

and `fleetListLines` (`:812-877`) is itself called from `windowTop` (`:946-949`),
from `Body` (`:975`), and again inside `tableHeight` (`:899`) -> `cardBlock`.
`Body` (`:952-998`) calls `cardBlock` and `fleetListLines` and then paints only
`list[start:end]`, so **every line outside the window is rendered and thrown
away**.

Separately, `view.SortRows` (`internal/view/sort.go:34-64`) copies every
`BindingStatus` by value into a fresh slice (`:35-36`) and does a map lookup
per comparison (`:19-24`, `attentionRank` at `:10-17`). `view.BindingStatus`
is ~832 bytes.

### Seams

- `internal/ui/view_fleet.go`: `rows` `:161-194`, `Update` `:374-443`,
  `fleetListLines` `:812-877`, `cardBlock` `:881-890`, `tableHeight` `:899`,
  `windowTopLines` `:914-944`, `windowTop` `:946-949`, `Body` `:952-998`
- `internal/view/sort.go`: `attentionRank` `:10-17`, `rankOf` `:19-24`,
  `SortRows` `:26-64`
- `internal/ui/frame.go`: `bodyHeight` `:122-129`
- `internal/ui/fleet_group.go`: `groupOf` `:23-38`
- `internal/ui/ui.go`: `defaultInterval = 2s` `:56`, `minInterval = 500ms` `:55`
- `internal/ui/shell.go`: `View` `:430`, `updateStack` `:412`
- Phase 0's `BenchmarkFrame` -- the measuring instrument.

### Steps

1. Compute `rows()` **once per cycle** and share it. The natural place is the
   shell's `Env` (or a memo on `fleetView` keyed by the report the rows were
   computed from), so `Update`, `windowTop`, `Body`, `cardBlock` and
   `fleetListLines` all read the same slice.
   - The key must invalidate when the report changes (`StatusAt`), when the
     filter text changes, when `showDone` flips, and when `attention` flips --
     `repointed` (`:466-471`) already centralises the last two.
   - Done when `BenchmarkFrame/rows=64` shows **one** `SortRows` per iteration,
     measured by the allocation count dropping from 3492 to roughly a fifth.
2. Window-only paint. `fleetListLines` renders every line; `Body` then slices
   `[start:end]`. Split the render so the section headers and the cursor's row
   are the only ones rendered when they fall inside the window.
   - This is the fiddly part: a section header immediately above the window
     must still paint, or the visible rows lose their group label. The plan
     does not prescribe the fix, only that the goldens decide it.
   - Done when the golden files are byte-identical.
3. `rankOf` as a `switch` instead of a map lookup (`:19-24`), and sort an index
   permutation (`[]int`) instead of copying `BindingStatus` values.
   - `SortRows` keeps its signature and its "never mutates its input" promise;
     it returns a fresh slice, but builds it by permuting indices and copying
     once at the end.
   - `attentionRank` is package-private and has **one** other user? Grep it. If
     `len(attentionRank)` is the fallback rank, replace it with a named constant.
   - Done when the sort order is byte-identical for a shuffled input and
     `BenchmarkFrame/SortRows` shows the 832B-per-element copies gone.

### Behaviour that must not change

| Case | Rule |
|---|---|
| empty store | `emptyPaneBlock` (`Body` `:957`), unchanged |
| all-DONE hidden vs `.` | `f.showDone` gating in `rows` (`:190-192`) and the fold line in `fleetListLines` (`:863-874`) |
| filter narrows live while typing | `activeFilter()` (`:198-203`) must invalidate the memo |
| sticky cursor across owners | `resolveSticky` (`:335-357`), `split_test.go`'s `TestStickyFollowsKeyAcrossOwners` |
| resize | `tea.WindowSizeMsg` -> `bodyHeight` (`frame.go:122-129`) must invalidate the window, not the rows |
| attention toggle | `a` re-sorts (`Update` `:415-427`); order byte-identical, goldens decide |

### Tests

- `internal/ui`:
  - `TestRowsAreComputedOncePerCycle` -- a counting hook on the sort shows one
    sort per `Body` call, not five.
  - `TestWindowPaintMatchesTheFullList` -- for every window height from 1 to
    the full list, `Body`'s output equals the corresponding slice of the
    full-list render.
  - `TestSortRowsOrderIsStableUnderShuffle` -- shuffle the input 50 times; the
    output order is identical every time and equals the pre-change order.
  - `TestSortRowsDoesNotMutateItsInput`.
- Unchanged and still green: every `internal/ui` golden
  (`golden_test.go`, `pane_test.go`, `split_test.go`, `card_test.go`,
  `view_fleet_test.go`, `rail_test.go`). Run them with `-run` on the golden
  names **without** `-update`; if a golden needs regenerating, that is a pixel
  change and the phase halts.

### Mutation

- **Break:** reverse the sort's tiebreak so name breaks ties descending.
  **Fails:** `TestSortRowsOrderIsStableUnderShuffle` and the goldens.
  Second mutation: clip one line too few in the window-only paint.
  **Fails:** `TestWindowPaintMatchesTheFullList` at that exact height.
  Third mutation: drop the memo's invalidation on the filter text.
  **Fails:** `TestRowsAreComputedOncePerCycle`'s sibling case -- stale rows
  while typing.

### Perf

- P4 improves on the frame profile with **pixel-identical goldens**.
  Baseline at 6dab64a2 on this machine: `Body` ~1.28ms/op, ~675 KB, ~3492
  allocs at 64 rows; `rows()` ~0.10ms, ~227 KB, 12 allocs; `fleetListLines`
  ~0.82ms, ~426 KB, ~3272 allocs; `SortRows` ~0.034ms, ~58 KB, 4 allocs.
  The pprof top is `lipgloss.Style.Render` at ~42% cumulative with
  `fleetRowLine` at ~56% and `applyBorder` at ~24% -- so window-only paint is
  where the win is, and the sort is the smaller half of this phase.
- P1, P2, P3 unchanged.

### Done when

- Goldens byte-identical, `make check` green, all three mutations run,
  `git diff --stat` names `internal/ui/view_fleet.go`, `internal/ui/frame.go`
  and `internal/view/sort.go` (and `internal/ui/view_round.go` if the memo
  lands on `Env`).

---

## OPTIONAL A (issue fix 5) -- halve the git cost

Only meaningful **after Phase 2**, since the fleet path no longer calls
`liveStat` at all. It is then a detail-pane win.

### Seams

- `internal/git/snapshot.go:189-194` (`DiffWorktreeStat`): the
  `rev-parse --git-dir` probe at `:190` is **discarded** -- only its error is
  used, and `numstat` at `:196-202` fails the same way. One of the two forks per
  open round is pure waste.
- `internal/relevo/livestat.go`: cache `:27-30`, key `:47`, TTL `:15`.

### Steps

1. Drop the discarded probe at `:190`. `DiffWorktreeStat` becomes a single
   `git diff --numstat`.
   - Done when an open round's `statusRow` git cost halves with identical `Live`
     values, and a binding whose `dir` is not a git repository still yields nil
     from `liveStat` (`livestat.go:56-59`) rather than an error.
2. Give `liveStatCacheTTL` a real deadline on the fleet-excluded path -- with
   Phase 2, a fleet refresh never populates the cache, so the TTL no longer
   protects anything on the fleet path and the detail pane's first open pays the
   full diff every time. Either the detail pane accepts that or the cache is
   keyed to the detail fetch too.
   - Done when the report states which, with the number.

### Mutation

- **Break:** keep the probe.
  **Fails:** a fork-count test. Count the `git` subprocesses the detail fetch
  makes for one open round: exactly one after, two before.

---

## OPTIONAL B (issue fix 6) -- one liveness probe per refresh, tail after the checks

### Seams

- `internal/proc/proc.go:336-355` (`Alive`) forks `ps` through `psInfo` per
  call, and `headlessStatus` calls it once per headless row per tick
  (`internal/relevo/headless.go:1470`).
- `internal/relevo/headless.go:1446` (`statusTailLines = 3`), `:1459-1463` (the
  tail block), `:1464-1473` (the `PID == 0` / `rt.Runner == nil` early returns),
  `:1482` (`ExitCode`).
- `internal/relevo/transcript.go:199-204` (`builderTail`), `:212-221`
  (`currentBuilderTail`) -- the bounded-read sibling.

### Steps

1. One liveness probe per refresh, or a TTL cache of it, instead of one per row
   per tick. Same rule as `liveStatCacheTTL`: a `PID` + `StartedAt` handle is
   its own cache key, and a changed handle is a new key.
2. Compute `Tail` only **after** the `PID == 0` and `rt.Runner == nil` checks
   (`headless.go:1464`, `:1467`), and bound the legacy `logTail` read.
   - Ordering matters: an idle row has no tail to show, so the read is pure
     waste today.
3. Note that Phase 2 already removed the fleet path's tail. This is then a
   detail-pane and statusline win, and it is worth saying so in the report.

### Mutation

- **Break:** move the tail block back above the `PID == 0` check.
  **Fails:** a fork-count / read-count test on an idle headless row -- it reads
  the log it must not read.

---

## OPTIONAL C (issue fix 8) -- bound the first fetch

### Seams

- `internal/ui/shell.go`: `Init` `:151-156` (`tea.Batch(fetchStatus, tick)`),
  the single-flight tick `:176-178`, `updateStatus` `:252`.
- `internal/ui/fetch.go:108-119` (`fetchStatus`) -- already all-or-nothing: it
  returns `statusMsg{err}` without a partial report.
- `internal/ui/view_fleet.go:952-956` (`Body`) already paints
  `loading…` while `!env.Loaded`.

### Steps

1. Time-box the first `fetchStatus` with a watchdog, and **re-fire on landing**
   rather than waiting for the next tick boundary. No partial-report merge: the
   watchdog paints the bounded loading state and nothing else.
2. A watchdog must not make the slow path slower: the re-fire is on landing, and
   the single-flight guard at `shell.go:176-178` is what prevents a double fetch.

### Mutation

- **Break:** merge the watchdog's partial report into `env.Report`.
  **Fails:** a test that a slow `Source.Status` never produces a partial
  `statusMsg`.

---

## Perf appendix (Phase 0 baseline, this machine, 2026-10-05)

Machine: Intel Core Ultra 7 155H, Linux, Go 1.27.1. Binary built from `6dab64a2`.
Store: `~/.local/state/relevo/relevo.db`, schema 21.

| Table | Rows |
|---|---|
| `binding` | 1132 |
| `binding_record` | 1041 |
| `binding_event` | 5727 |
| `event` | 6133 |
| `kv` | 427 |
| `mastermind` | 166 |
| `chains` / `chain_member` / `chain_event` | 67 / 211 / 91 |
| `round_file` | 7546 |

Fleet shape: **487 bindings built**, 23 shown after the DONE rule (464 DONE,
20 ACTIVE, 3 NEEDS YOU), 6 distinct masterminds, largest `pl_orsgv3tzeswf` at 38.

### Wall probes (seconds, 3 runs each)

| Probe | Cold | Warm | Note |
|---|---|---|---|
| P1 `status --all-masterminds --json` | 4.667 / 4.646 / 4.520 | 4.727 / 4.697 / 4.598 | matches the issue's ~4.6s |
| P2 `status --mastermind <id> --json` (1 row) | 4.567 / 4.618 / 4.589 | - | **identical to P1** -- the defect: 1 row costs 487 builds |
| P3 `status --chains` | 0.072 / 0.070 / 0.083 | - | not a usable probe here: no live chain output, so `chains_doc.go:70-72` returns before building |
| `status --line --json` (statusline) | 0.070 / 0.065 / 0.069 | - | `MasterMindStatus` already narrows (`statusline.go:20-29`) -- this is why it is fast, and it is the shape Phase 1 copies |

### Frame profile (160x50, `-benchtime 200x`)

| Benchmark | rows=30 | rows=64 |
|---|---|---|
| `Body` | 0.704 ms, 339 KB, 1808 allocs | **1.285 ms, 675 KB, 3492 allocs** |
| `rows()` | 0.048 ms, 109 KB, 11 allocs | 0.102 ms, 227 KB, 12 allocs |
| `fleetListLines` | 0.396 ms, 205 KB, 1569 allocs | 0.819 ms, 426 KB, 3272 allocs |
| `view.SortRows` | 0.018 ms, 28 KB, 4 allocs | 0.034 ms, 58 KB, 4 allocs |

pprof of `Body` at 64 rows: `lipgloss.Style.Render` **42% cumulative**
(`applyBorder` 24%, `applyMargins` 6%), `fleetRowLine` **56% cumulative**,
`fleetView.rows` 12%, `ansi.stringWidth` 10%. The issue's "~39% `Style.Render`"
is confirmed; the profile says the win is in **rendering lines nobody sees**,
not in the sort.

### Statement count

**Not measured at `6dab64a2`.** Phase 4 must measure it with a counting
`db.DB`/`db.KV` wrapper and quote its own number. The seed's "~1.8k at the
issue's fleet size" is **not** transferable to this store (487 rows, 427 kv
rows) and must not be quoted as an expected value.

### Expected deltas, per phase

| Phase | P1 | P2 | P3 | P4 |
|---|---|---|---|---|
| 0 | baseline | baseline | baseline (unusable) | baseline |
| 1 | unchanged | large drop (the 30x class) | unchanged | unchanged |
| 1b | unchanged | unchanged | ship on correctness | unchanged |
| 2 | large drop (git, the ~0.6s) | drop | unchanged | unchanged |
| 3 | drop (one log decode per row) | drop | drop | unchanged |
| 4 | drop (measured statement count) | drop | drop | unchanged |
| 7 | unchanged | unchanged | unchanged | improves, goldens fixed |
| A/B/C | unchanged | unchanged | unchanged | unchanged |

---

## Deletes

A closed list. Nothing else is removed, and each item is grepped for other
callers before it goes.

1. Fleet-path computation of `Live` -- the per-open-round
   `git rev-parse --git-dir` + `git diff --numstat`
   (`internal/relevo/status.go:326-328`). Replaced by the detail-pane fetch;
   nil on fleet rows. `liveStat` itself stays -- the detail path uses it.
2. Fleet-path computation of `Tail` -- the whole-file `logTail` /
   64 KiB-window `streamTail` per row per tick
   (`internal/relevo/headless.go:1459-1463`, `internal/relevo/transcript.go:199-204`).
   Replaced by the detail-pane fetch; nil on fleet rows.
3. Fleet-path computation of `LiveUsage` -- the per-row `Peek` + `Fold`
   (`internal/relevo/status.go:317-319`). Replaced by the detail-pane fetch;
   nil on fleet rows. `peekUsage` itself stays.
4. The second full per-binding log read and decode inside
   `PendingForMasterMind` on the `statusRow` path
   (`internal/store/log.go:485`). Replaced by derivation from the entries held
   at `status.go:234`. The store function stays for its other callers.
5. Per-binding single-key `KVGet`/`RecordGet` round trips on the `Status` path
   -- `claim`, `wait`, `mastermind`, `viewed_at` -- replaced by per-`Status`
   bulk loads. No lint, file-size or coverage exclusion is added to get this
   green.
6. *(Optional A only)* The discarded `rev-parse --git-dir` probe preceding
   every `diff --numstat` (`internal/git/snapshot.go:190`).
7. The second full ledger kv read and decode feeding `UnusedProviderGates`
   (`internal/relevo/unused_gates.go:20`) -- replaced by sharing one load per
   `Status`.
8. Duplicate full-list `SortRows` + group + copy passes per render cycle
   (currently up to 5x) -- replaced by one computed `rows()` shared across
   `Update`/`Body`/windowing. Same pixels.
9. The `attentionRank` map lookup and the 832B-per-element sort copies on the
   hot path (`internal/view/sort.go:10-24`, `:35-36`) -- replaced by a switch
   and an index-keyed sort. Same order.

---

## Out-of-scope ledger, for the next plan

- `rolesMissingGates` (`internal/availability/gates.go:344`) does per-report
  file probes. It already caches by distinct (kind, definition list), so it is
  not the N+1 the other sites are; a plan that touches it is a different plan.
- `KVKeys` (`internal/db/kv.go:84-103`) selects the **whole** kv table and
  filters by prefix in Go. With 427 rows that is fine; with 100k it is not. A
  `WHERE key LIKE ?` or an index is its own round.
- `internal/relevo/chain_server_view.go:34` (`chainServerStatus`) builds all
  rows for a server chain -- the fourth `buildReport` caller Phase 1 leaves
  alone. Phase 4 makes it cheaper for free.
- `ReadChainsScope`'s silent `rep, _ :=` at `chains_doc.go:78` is fixed in
  Phase 1b, but the same `rep, _` shape may exist elsewhere; grep before
  assuming this was the only one.