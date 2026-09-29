# `relevo unbind --done` clears only the calling planner's DONE bindings (#482)

## 1. System Overview

`relevo unbind --done` is the planner's cleanup verb. It archives (or with
`--delete`, removes) finished bindings, and deletes their worktrees and pushed
`relevo/<name>` branches and refs. Its help says "clear every binding the
planner marked DONE", but the implementation ignores the planner.
`relevo.GC` (`internal/relevo/gc.go:48`) walks every binding in the store and
filters only on `State == StateDone`. On 2026-09-25 a dry run by planner
architect-3 listed architect-4's bindings and branches for clearing. A real run
would have destroyed another live session's finished work.

The fix:
- `GC` takes an explicit scope: one planner id, or `AllPlanners`. It refuses
  to run with neither, so no caller can get "everything" by leaving a field
  empty.
- `unbind --done` resolves the calling planner with the same rule the other
  planner-scoped verbs use: `--planner`, then `$RELEVO_PLANNER`, then this
  session's host. It clears only that planner's DONE bindings.
- A new `--all-planners` flag keeps today's behaviour, for when a human
  really means it.
- If no planner resolves, and `--all-planners` is not given, the command
  refuses with exit 2. It never falls back to everything, unlike `relevo
  status`, because this verb destroys things.
- A binding with an empty `PlannerID` (written before #303) belongs to no
  planner, and is cleared only by `--all-planners`.
- Every output line (dry run, archived and deleted) names the binding's
  planner.

Out of scope:
- `relevo serve gc` (the server's own GC);
- `relevo unbind <name>` (single-binding unbind, which the caller names
  explicitly);
- `unbind --sweep`.

## 2. File Structure

```
internal/relevo/gc.go            MODIFIED  GCOptions.PlannerID/AllPlanners; GCResult.PlannerID; scope filter; ErrGCNoScope
internal/relevo/gc_test.go       MODIFIED  new scope tests; existing calls ported to AllPlanners: true (scripted)
internal/relevo/add_test.go      MODIFIED  one call ported (scripted)
internal/relevo/refclean_test.go MODIFIED  four calls ported (scripted)
internal/relevo/remote_test.go   MODIFIED  one call ported (scripted)
cmd/relevo/main.go               MODIFIED  cmdUnbind (~1663-1692): --planner, --all-planners; runGC (~1768-1830): scope + planner label
cmd/relevo/planner.go            MODIFIED  new pure func gcScope next to plannerLookup (~488)
cmd/relevo/main_test.go          MODIFIED  flag-conflict rows; TestGCScope (pure)
README.md                        MODIFIED  ~416-417 and ~1060: the flag and the scope
docs/plans/2026-09-25-unbind-done-planner.md  NEW  this plan, committed with the change
```

## 3. Data Structures & Type Definitions

### `relevo.GCOptions` (`gc.go:11`): add two fields

| field | type | meaning |
|---|---|---|
| `PlannerID` | `string` | clear only DONE bindings whose `Binding.PlannerID` equals this. It must be non-empty unless `AllPlanners` is set |
| `AllPlanners` | `bool` | clear every DONE binding regardless of planner, including bindings with an empty PlannerID. It is mutually exclusive with a non-empty `PlannerID` |

### `relevo.GCResult` (`gc.go:22`): add one field

| field | type | meaning |
|---|---|---|
| `PlannerID` | `string` | the binding's `PlannerID`, verbatim, which may be `""`. JSON tag `planner_id,omitempty` |

### New sentinel (`gc.go`)

`ErrGCNoScope = errors.New("gc needs a planner id or AllPlanners")`

## 4. Interface Definitions & Component Contracts

### `GC(ctx, rt, opts)`: contract change

- Precondition: exactly one of `opts.PlannerID != ""` and `opts.AllPlanners`
  holds.
  - Neither holds: return `nil, ErrGCNoScope` before taking the lock.
  - Both hold: return `nil, fmt.Errorf("%w: PlannerID and AllPlanners are exclusive", ErrGCNoScope)`.
- Inside the existing loop, right after the `b.State != store.StateDone`
  skip: `if !opts.AllPlanners && b.PlannerID != opts.PlannerID { continue }`.
  A binding with an empty PlannerID never equals a non-empty id, so it is
  skipped in planner mode.
- Set `res.PlannerID = b.PlannerID` where `res` is built (gc.go:62).
- Update the doc comment (gc.go:35-47) to state the scope rule and cite #482.
- Everything else is unchanged: the lock, the teardown, the refs, archive vs
  delete, and dry run.

### `func gcScope(plannerFlag string, all bool, resolve func(ref string) (planner.Record, error)) (relevo.GCOptions, error)` (new, `cmd/relevo/planner.go`, pure)

- Responsibility: turn the flags into a GC scope, with no fallback to
  "everything".
- The rules are ordered:
  1. `all && plannerFlag != ""`: return `exitCodeErr{code: 2}`. The caller has
     already printed
     `relevo: --all-planners and --planner are exclusive`. To keep this
     function pure, it returns a typed usage error that cmdUnbind turns into
     the message and exit 2. Pick one shape and use it consistently; the
     simplest is a sentinel `errGCUsage` that wraps the message text.
  2. `all`: return `GCOptions{AllPlanners: true}`.
  3. Otherwise, call `rec, err := resolve(plannerFlag)`:
     - an error returns a usage error with the message
       `relevo: unbind --done clears this planner's DONE bindings, and no planner resolved (<err>); pass --planner <name|id>, or --all-planners to clear every planner's`;
     - on success, return `GCOptions{PlannerID: rec.ID}`.
- `resolve` is injected so the test needs no registry. In production it is a
  closure over `rt` that calls `planner.Resolve(rt.Planners, planner.ResolveInput{Flag: ref, Env: os.Getenv, PPID: os.Getppid(), ProcStart: rt.ProcStart, Now: <rt.Now or zero>, CWD: <os.Getwd>, OpencodeSession: rt.OpencodeSession})`.
  That is the same input `plannerFilter` (planner.go:517) builds, plus
  `Flag`. When `rt.Planners == nil`, it returns an error
  (`no planner registry`). It must NOT call `planner.Init`: this verb never
  registers a planner.

### `cmdUnbind` (`main.go` ~1663-1692)

- Add the flags:
  - `plannerRef := fs.String("planner", "", "with --done: clear this planner's DONE bindings (id or name; default: $RELEVO_PLANNER, else this session's host)")`;
  - `allPlanners := fs.Bool("all-planners", false, "with --done: clear every planner's DONE bindings, including ones with no planner")`.
- Change the `--done` flag's help to
  `"clear the DONE bindings of the calling planner (--all-planners: of every planner)"`.
- In the `--sweep` guard (~1677), also reject `*plannerRef != "" || *allPlanners`.
- The existing `--done` guard (~1687) is unchanged. Change its message
  wording from "clears every DONE binding" to "clears DONE bindings".
- A new guard, placed before the `--done` branch: if
  `(*plannerRef != "" || *allPlanners) && !*done`, print
  `relevo: --planner and --all-planners go with --done` and exit 2.
- Change the call to `runGC(*delete, *dryRun, *plannerRef, *allPlanners)`.

### `runGC(delete, dryRun bool, plannerRef string, all bool)` (`main.go` ~1768)

- After `newRuntime()`, call `opts, err := gcScope(plannerRef, all, <resolver over rt>)`.
  On a usage error, print its message to stderr and return
  `exitCodeErr{code: 2}`.
- Set `opts.Delete = delete` and `opts.DryRun = dryRun`, and call `relevo.GC`.
- `"no finished bindings to clear"` becomes
  `"no finished bindings to clear for planner <label>"` in planner mode, and
  `"no finished bindings to clear"` in all mode.
- Planner label, for every result line: `plannerLabel(rt, id)`, a small helper
  in the same file. It returns:
  - `(none)` for `""`;
  - the record's `Name` when `rt.Planners != nil && rt.Planners.Get(id)`
    succeeds;
  - otherwise the id itself.

  Cache the labels in a map for the loop, as `planner.go:403-413` does.
- The line formats (existing text plus a planner field):
  - dry run: `would clear %-10s %-14s %s (%d rounds)%s` with args
    `name, "["+label+"]", cwd, rounds, wtMsg`;
  - archived: `archived    %-10s [%s]`;
  - deleted: find the existing deleted-case line at ~1812-1830 and add
    ` [%s]` after the name in the same way.

  The ref lines under each are unchanged.

## 5. High-Level Pseudocode

```
cmdUnbind(args):
    parse flags (adds --planner, --all-planners)
    if sweep: reject any of done/delete/archive/pick/name/args/planner/all-planners (exit 2); runSweep
    if (plannerRef != "" or allPlanners) and !done: usage exit 2
    if done:
        reject name/args/pick (exit 2, message reworded)
        return runGC(delete, dryRun, plannerRef, allPlanners)
    ... single-binding unbind unchanged ...

runGC(delete, dryRun, ref, all):
    rt := newRuntime()
    opts, err := gcScope(ref, all, resolverFor(rt))
    if usage error: stderr message; exit 2
    opts.Delete, opts.DryRun = delete, dryRun
    results := relevo.GC(ctx, rt, opts)
    if empty: print the no-bindings line for the scope
    for r in results: print as today, plus [label(r.PlannerID)]

GC(ctx, rt, opts):
    validate scope, or ErrGCNoScope
    lock; for b in bindings:
        skip if not DONE
        skip if !opts.AllPlanners and b.PlannerID != opts.PlannerID
        ... unchanged ...; res.PlannerID = b.PlannerID
```

## 6. Error Handling Strategy

- A scope error is a usage error: exit 2, with the message on stderr and
  nothing touched. A planner that fails to resolve is a scope error. This is
  the one place the fix deliberately differs from `relevo status`'s
  "no planner means show everything".
- `ErrGCNoScope` from `relevo.GC` is a programming error in a caller. The CLI
  cannot reach it, because gcScope always sets exactly one scope.

## 7. Working Efficiently

- Read in one step, as parallel reads:
  - `internal/relevo/gc.go`;
  - `internal/relevo/gc_test.go` lines 1-120;
  - `cmd/relevo/main.go` lines 1663-1835;
  - `cmd/relevo/planner.go` lines 395-420 and 485-540;
  - `cmd/relevo/main_test.go` lines 1428-1470;
  - `README.md` lines 410-420 and 1055-1065.
- Port the existing test calls with ONE scripted edit (step 2), not by hand.
- Focused tests:
  - `go test ./internal/relevo/ -run 'GC|Refclean|Clean|Remote' -count=1`
  - `go test ./cmd/relevo/ -run 'Unbind|GCScope' -count=1`
- Full check once at the end: `make check`.
- The CLI tests may only exercise flag validation that exits before
  `newRuntime()`, plus the pure `gcScope`. Nothing may spawn a harness or reach
  the network (CLAUDE.md, "Merging and CI"). The package TestMain already
  isolates HOME and XDG.

If any step is impossible as written or contradicts the code, stop and report.
Do not bend the plan, or a test, to fit.

## 8. Ordered Implementation Steps

**Step 0: sync and confirm the premises.** Run
`git fetch origin && git merge --ff-only origin/main`. Halt if any of these is
false:
- `GC`'s loop filters only on `b.State != store.StateDone`;
- `GCOptions` has exactly `Delete` and `DryRun`;
- `runGC(delete, dryRun bool)` is `unbind --done`'s only path to `relevo.GC`;
- `store.Binding` has `PlannerID string`;
- the `GC(` call sites in tests are exactly the 19 listed here:
  - `add_test.go:663`;
  - `gc_test.go:52, 73, 94, 132, 209, 264, 307, 344, 386, 423`;
  - `refclean_test.go:349, 391, 433, 476`;
  - `remote_test.go:2196`.

  Check with `git grep -n 'GC(' -- 'internal/relevo/*_test.go'`. Extra call
  sites are fine: include them in step 2's script.

**Step 1: GC scope tests first** (`gc_test.go`). Add a helper
`seedDoneFor(t, rt, name, cwd, plannerID)`, like `seedDone` but setting
`PlannerID`. Add these tests:
1. `TestGCClearsOnlyThisPlannersBindings`: seed `a1` (planner `pl_aaa`),
   `b1` (planner `pl_bbb`), and `legacy` (planner `""`), all DONE. Run
   `GC(…, GCOptions{PlannerID: "pl_aaa", Delete: true})`. The result is
   exactly `[a1]`, with `PlannerID == "pl_aaa"`, and `b1` and `legacy` are
   still in `rt.Store.List()`.
2. `TestGCAllPlannersClearsEveryDoneBinding`: the same seed with
   `GCOptions{AllPlanners: true, Delete: true}`. The result names all three,
   and each result's `PlannerID` matches its seed.
3. `TestGCRefusesWithoutScope`: `GCOptions{}` gives
   `errors.Is(err, ErrGCNoScope)`, and all three bindings survive.
   `GCOptions{PlannerID: "pl_aaa", AllPlanners: true}` also gives
   `ErrGCNoScope`.
4. `TestGCPlannerDryRunListsOnlyThisPlanner`: the same seed with
   `{PlannerID: "pl_bbb", DryRun: true}`. The result is exactly `[b1]`, and
   nothing is removed.

Run the focused internal test and watch it fail to compile. Record that.

**Step 2: implement GC's scope, then port the existing calls with one
script.** Make the `gc.go` changes (§4). Then run this, once, from the repo
root:

```
perl -0pi -e 's/GCOptions\{\}/GCOptions{AllPlanners: true}/g; s/GCOptions\{((?:Delete|DryRun): true)\}/GCOptions{$1, AllPlanners: true}/g' \
  internal/relevo/gc_test.go internal/relevo/add_test.go internal/relevo/refclean_test.go internal/relevo/remote_test.go
```

Then run `git grep -n 'GCOptions{' -- 'internal/relevo/*_test.go'`. Every
pre-existing call must now carry `AllPlanners: true`. Step 1's new tests set
their own scope and must not be altered. If the script touched a step 1 test,
fix that one line by hand. Every existing test keeps its assertions exactly.
They cleared "every DONE binding" before, and they do so now through
`AllPlanners`. No test is deleted. Verification: the internal focused test
passes.

**Step 3: prove the scope filter is pinned.** Temporarily delete the line
`if !opts.AllPlanners && b.PlannerID != opts.PlannerID { continue }`, and
confirm that tests 1 and 4 FAIL. Restore it. Temporarily make the no-scope
check a no-op, and confirm that test 3 FAILS. Restore it. Record the failure
lines, then confirm with `git diff` that only the intended changes remain. If
any named test passes under its mutation, halt and report.

**Step 4: the CLI, tests first** (`main_test.go`).
- Extend `TestUnbindDoneTakesNoBinding`, or add `TestUnbindPlannerFlags`, with
  rows that must exit 2 before a runtime is built:
  - `unbind --done --planner x --all-planners`: this one is checked inside
    runGC after newRuntime. If it cannot exit before newRuntime, test it
    through `gcScope` below instead, and leave it out of this table;
  - `unbind --planner x` (without `--done`);
  - `unbind --all-planners` (without `--done`);
  - `unbind --sweep --planner x`;
  - `unbind --sweep --all-planners`.
- Add `TestGCScope`, a table over `gcScope` with a fake `resolve`:
  - `("", false, ok→pl_aaa)` gives `{PlannerID: "pl_aaa"}`;
  - `("architect-2", false, …)` passes `"architect-2"` to resolve, which
    returns `pl_bbb`, giving `{PlannerID: "pl_bbb"}`;
  - `("", true, resolve must not be called)` gives `{AllPlanners: true}`.
    Make `resolve` call `t.Fatal`;
  - `("x", true, …)` gives a usage error;
  - `("", false, resolve returns an error)` gives a usage error whose message
    contains `--all-planners`, and a result whose PlannerID is empty and
    AllPlanners is false. This pins the no-fallback rule.

Run `go test ./cmd/relevo/ -run 'Unbind|GCScope' -count=1` and watch it fail.
Then implement §4's `gcScope`, the `cmdUnbind` flags and guards, `runGC`, and
`plannerLabel`. Verification: the CLI focused test passes.

**Step 5: prove the no-fallback rule is pinned.** Temporarily make
`gcScope`'s resolve-error branch return `GCOptions{AllPlanners: true}, nil`,
and confirm that `TestGCScope` FAILS. Restore it. Record the failure line.

**Step 6: README.** At ~416-417, the line becomes
`relevo unbind --done [--dry-run] [--delete] [--planner P | --all-planners]`.
It clears the DONE bindings of the calling planner (resolved like every
planner-scoped verb). `--all-planners` clears every planner's, including
bindings with no planner. If no planner resolves, it refuses rather than
clearing everything, and each line names the binding's planner. Keep the rest
of that bullet. At ~1060, `# archive every DONE binding` becomes
`# archive this planner's DONE bindings`.

**Step 7: full check and ship.** `make check` must pass. Copy this plan to
`docs/plans/2026-09-25-unbind-done-planner.md`. Commit as
`fix(unbind): --done clears only the calling planner's DONE bindings; --all-planners for every planner's (#482)`,
with `Fixes #482` in the body. Push, and open a PR against `main` with the
same title and `Fixes #482` in the body. Don't wait for CI and don't merge.

Do NOT run `relevo unbind --done` for real on this machine at any point. A dry
run through the built binary is allowed, but only as
`./relevo unbind --done --dry-run` from the worktree, with its output pasted
into the report. Do not run it without `--dry-run`.

The report states:
- the step 1 and step 4 initial failures;
- the step 3 and step 5 mutation failures;
- the `git grep` output after step 2's script;
- the `make check` result;
- the PR number;
- `git diff --stat origin/main`.
