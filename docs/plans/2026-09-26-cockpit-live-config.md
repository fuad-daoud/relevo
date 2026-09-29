# Cockpit: every view sees a config change without a restart

## 1. System Overview

`relevo ui` builds one `relevo.Runtime` at startup. It hands one **value copy** to
`plannerSource` (the fleet refresh, `Base()`, and the per-row `Runtime(key)`), and a
second copy to `plannerActions` (every write). After a config edit,
`plannerActions.ApplyConfig` and `plannerActions.Rollback` reload **their own** copy.
The source keeps the startup copy, so these keep the old candidates, actors and policy
until restart: `:stats` names, `:rounds` names, the fleet's gates, `:log` names, and
anything read through `Base()`. A change made outside the cockpit (`relevo config set`,
`relevo gate`'s config side, another cockpit) is never seen by either copy.

This round replaces both copies with **one shared holder**, `liveRuntime`. The fleet
refresh (every 2 s) re-reads the config when the store's version counter moved, and
config writes from the cockpit refresh the same holder. No screen changes; the views
simply stop being stale.

Out of scope: `serverSource` (`relevo serve ui`) is unchanged. So are the daemon's
`relevo.ConfigWatcher` and every `internal/relevo` function.

**If a step is impossible as written or contradicts the code, stop and report. Do not
improvise, and do not bend a test to fit.**

## 2. File Structure

```
internal/ui/
  live.go          NEW   liveRuntime: the one shared runtime, refreshed on config version change
                         liveSource: the Source `relevo ui` runs on, reading through liveRuntime
  live_test.go     NEW   tests for both
  actions.go       EDIT  plannerActions reads the shared holder (field rt -> live)
  actions_audit.go EDIT  Rollback refreshes the shared holder
  ui.go            EDIT  Run builds one liveRuntime, gives it to the source and the actions
  view_rounds.go   EDIT  the dashboard's Names reads the live set on every call
docs/plans/2026-09-26-cockpit-live-config.md   NEW (last step): this plan, verbatim
```

`plannerSource` in `internal/ui/source.go` **stays as it is**. 93 test literals build
`plannerSource{rt}` for a fixed runtime, and that stays correct for them.

## 3. Data Structures & Type Definitions

### `liveRuntime` (internal/ui/live.go)

The single runtime every part of a planner-side cockpit reads.

| field | type | purpose |
|---|---|---|
| `mu` | `sync.Mutex` | guards `rt` and `version` only; **never held across I/O** |
| `rt` | `relevo.Runtime` | the current snapshot |
| `version` | `int64` | `rt.Config.Version()` at the time `rt`'s config sections were loaded; `-1` = unknown |

Invariants:
- `version` only ever increases. A reload that read an older version than the one
  already stored never replaces `rt`, because two refreshes can race (the 2 s tick and
  an `ApplyConfig`).
- Only the config sections change: `Candidates`, `Policy`, `Registry` and
  `ConfigWarnings`, exactly what `relevo.ReloadConfig` (internal/relevo/configedit.go:467)
  replaces. `Store`, `DB`, `Config`, `Gates`, `Now` and the rest keep their startup
  values.

### `liveSource` (internal/ui/live.go)

`type liveSource struct { live *liveRuntime }` implements `Source`
(internal/ui/source.go:18-38).

## 4. Interface Definitions & Component Contracts

### liveRuntime

- `newLiveRuntime(rt relevo.Runtime) *liveRuntime`
  - Sets `version` from `rt.Config.Version()`. When `rt.Config` is nil or `Version`
    errors, it sets `-1`.
  - Post: `Get()` returns `rt` unchanged.
- `(*liveRuntime) Get() relevo.Runtime`
  - Returns the snapshot under `mu`. It never does I/O, because renders call it through
    `Base()`.
- `(*liveRuntime) Refresh() error`
  - Pre: none. With a nil `Config` it is a no-op that returns nil.
  - It reads `Version()` without holding `mu`. When that equals the stored `version`, it
    returns nil without loading.
  - Otherwise it calls `relevo.ReloadConfig(snapshot)` without holding `mu`. Then, under
    `mu`, it swaps in the result and the new version only when the version read is
    greater than the stored one.
  - Errors: a `Version` or `ReloadConfig` error is returned and the snapshot is kept.

### liveSource (implements Source)

| method | contract |
|---|---|
| `Status(ctx)` | `live.Refresh()`, ignoring its error so a failed reload keeps the last good copy and never fails the fleet refresh, then `relevo.Status(ctx, live.Get())` |
| `Runtime(key)` | `live.Get(), key, true` |
| `Base()` | `live.Get()` |
| `MarkViewed(key)` | `_ = live.Get().Store.MarkViewed(key, time.Now())`, as plannerSource does |

### plannerActions (internal/ui/actions.go:80-92)

- The field `rt relevo.Runtime` becomes `live *liveRuntime`.
- A new method `func (a *plannerActions) runtime() relevo.Runtime { return a.live.Get() }`
  replaces every read of `a.rt`.
- `ApplyConfig` (actions.go:455-470) and `Rollback` (actions_audit.go:44-61):
  - after a successful write, call `a.live.Refresh()`;
  - a refresh error gives `Result{Text: "saved; reload failed: " + err.Error(), Refresh: true}`,
    the same text as today;
  - otherwise the success Result is unchanged.
  - The `rt, err := relevo.ReloadConfig(a.rt)` … `a.rt = rt` lines go.

## 5. High-Level Pseudocode

```
Run(ctx, rt, opts):                                   # ui.go:68-79
    live = newLiveRuntime(rt)
    if opts.Actions == nil:
        opts.Actions = &plannerActions{live: live, repo: repoRoot(ctx, rt), probe: opts.ProbeExec}
    return RunSource(ctx, liveSource{live}, opts)

every refresh tick:  liveSource.Status -> live.Refresh (cheap version read) -> Status on the snapshot
cockpit edit:        ApplyConfig -> WriteConfigEdit -> live.Refresh -> next render's Base() sees it
external edit:       version bumps -> next tick's Refresh loads it -> every view sees it

Refresh():
    snap, cur = lock{ rt, version }
    if snap.Config == nil: return nil
    v, err = snap.Config.Version();          if err: return err
    if v == cur: return nil
    next, err = relevo.ReloadConfig(snap);   if err: return err
    lock{ if v > version: rt.Candidates, rt.Policy, rt.Registry, rt.ConfigWarnings = next's; version = v }
    return nil
```

When swapping, copy only the four config fields from `next` into the **current** `rt`,
not the whole `next`. A later startup-field change must never be undone by a slow reload.

`view_rounds.go:69`: `d.Names = env.Src.Base().Candidates.NameOf` binds the startup set
for the life of the view. Replace it with a closure that calls
`env.Src.Base().Candidates.NameOf(token)` on each call (`NameOf` is nil-safe,
internal/candidate/candidate.go:126).

## 6. Error Handling Strategy

- A reload failure is recoverable. The source keeps the last good snapshot and says
  nothing: the config views already surface load errors through `ConfigDoc()`.
- The write paths report a reload failure as today's "saved; reload failed: …" text.
- No new error types, no logging.

## 7. Working Efficiently

- Batch the reads in one step: internal/ui/source.go, ui.go, actions.go (lines 77-95 and
  440-470), actions_audit.go (lines 40-62), view_rounds.go (lines 60-72) and
  internal/relevo/configedit.go (lines 460-480). You need no other file.
- The `a.rt` → `a.runtime()` rewrite is mechanical. First make the ApplyConfig and
  Rollback edits by hand (they delete the only `a.rt = rt` assignments), then run once:
  `sed -i -E 's/\ba\.rt\b/a.runtime()/g' internal/ui/actions.go internal/ui/actions_audit.go`
  and fix whatever no longer compiles in one pass.
- Focused loop: `go build ./... && go test -race -count=1 ./internal/ui/`
- Full check, once at the end: `make check`. On this server `/tmp` is a small tmpfs.
  Run it as
  `export TMPDIR=$HOME/.cache/go-tmp GOTMPDIR=$HOME/.cache/go-tmp; mkdir -p $TMPDIR; make check`.

## 8. Ordered Implementation Steps

**Step 1: liveRuntime and liveSource.** Write `internal/ui/live.go` per §3-§5.
- Package-level style: no doc comment that restates a name. `Refresh`'s comment says
  why `mu` is not held across I/O, and why the version must increase.
- Verify: `go build ./internal/ui/`.

**Step 2: wire it in.** Depends on step 1.
- Edit actions.go and actions_audit.go per §4, including the scripted rewrite in §7.
- Edit ui.go:75-78 per §5, and view_rounds.go:69.
- Verify: `go vet ./internal/ui/` and `go test -race -count=1 ./internal/ui/` pass with no
  edits to existing tests. If an existing test fails, stop and report: none should
  depend on the stale copy.

**Step 3: tests, in `internal/ui/live_test.go`.** Depends on step 2.
- Seed a real store: `db.Open(filepath.Join(t.TempDir(), "relevo.db"))`, then
  `config.Open(d)`, `relevo.LoadConfigDoc(st)`,
  `relevo.AddCandidate(doc, relevo.CandidateInput{Harness: "opencode", Provider: "cline-pass", Model: "cline-pass/deepseek-v4.1-flash#high"})`
  and `relevo.WriteConfigEdit(st, e)`. This is verified to work on an empty store:
  version 0 becomes 1, and the candidate is named `deepseek-v4.1-flash`.
- Build `rt` with `relevo.ReloadConfig(relevo.Runtime{Store: store.New(t.TempDir()), Config: st, DB: d})`.
- The tests:
  1. `TestLiveSourceStatusPicksUpAnExternalConfigWrite`
     - Build `liveSource{newLiveRuntime(rt)}`.
     - Write a second candidate straight to the store, as `relevo config` from another
       terminal would: opencode / openrouter / `z-ai/glm-5.3-flash`.
     - Assert `Base().Candidates` does not hold it yet.
     - Call `Status(ctx)`, then assert `Base().Candidates.NameOf(<its token>)` is its
       name, and that `Runtime("x")` returns the same set.
  2. `TestApplyConfigIsSeenThroughTheSource`
     - Build one `live`, a `plannerActions{live: live}` and a `liveSource{live}`.
     - Call `ApplyConfig` with an `AddCandidate` edit.
     - Assert the source's `Base()` holds the new candidate **without** calling
       `Status`. This is the bug being fixed.
  3. `TestRollbackIsSeenThroughTheSource`: the same as test 2, through `Rollback` to
     revision 1.
  4. `TestRefreshNeverGoesBackwards`
     - Call `Refresh` once, then set `live.version` to a value above the store's
       version, then write a new candidate.
     - A further `Refresh` must not swap in the new candidate, because the version it
       reads is not greater.
     - This pins the `v > version` guard.
  5. `TestRefreshWithoutAConfigStoreIsANoOp`: `Config` is nil, `Refresh` returns nil,
     and `Get` returns the runtime unchanged.
- Each test name says what it pins. No history in names or comments.
- **Required mutations.** Run each, confirm the named test fails, then restore. List
  all three in the report.
  - (a) Delete the `live.Refresh()` call from `liveSource.Status`: test 1 fails.
  - (b) Make `plannerActions.ApplyConfig` skip `a.live.Refresh()`: test 2 fails.
  - (c) Change `v > version` to `true`: test 4 fails.
- Tests must not spawn a harness or reach the network.
- Verify: `go test -race -count=1 ./internal/ui/`.

**Step 4: full check.** Depends on step 3.
- Run `make check`. It must pass as is.
- `internal/ui` coverage must not drop: the baseline is 82.0 in
  testdata/coverage-baseline.txt. Never lower a baseline, and add no exclusion to
  `.golangci.yml`, `scripts/check-comments.allow` or `scripts/check-filesize.allow`.
- `live.go` is new, so it must pass `scripts/check-comments.sh` with no allowlist
  entry.

**Step 5: ship the plan.** Copy this plan verbatim to
`docs/plans/2026-09-26-cockpit-live-config.md`. Then commit everything in **one** commit:
`fix(cockpit): every view sees a config change without a restart`. Never amend, and never
rebase.

## Report

The report covers:
- the files changed, and `git diff --stat`;
- the three mutation results, each with the test that failed;
- `make check`'s last lines;
- anything in this plan that did not match the code.
