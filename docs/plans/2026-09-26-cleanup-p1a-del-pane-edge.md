# Cleanup P1a (round 2) -- delete the pane-mode leftovers and `relevo edge`

## Round 2: where this starts and what the planner decided

The tree is on branch `relevo/cl-del-pane-edge` at commit `0febaaf`, which
already holds §8 step 1 (D4.1, D4.2, D4.3, D4.5: edges.go, edge.go, their
tests, the headless call, the daemon fires, the dispatch and usage line).
Verify that with `git log -1` and `git status` (expect clean) before starting;
if the tree differs, stop and report. **Start at step 2.**

Two decisions from round 1's halt, which replace the plan text they name:

1. **D4.4 changes.** Do **not** remove `store.Binding.Edges` or `type Edge`:
   removing them changes the binding shape that `TestBindingShapeMatchesFormat`
   guards, and a `BindingFormat` bump is not wanted. Keep both as a record shim:
   replace the field's doc with one line, `// Edges is read from records written
   before relevo edge was removed; nothing writes it.`, and reduce `type Edge`'s
   doc (and its fields' comments) to one line saying the same. Remove the two
   comments that mention `evaluateEdges` (`types.go:~534`, `~711`). Do not touch
   `binding-shape.golden`. The "load a record with an `edges` key" test row is
   still required (it now proves the shim decodes).
2. **D1.7 changes.** Delete the pane-consult refusal branch in
   `internal/relevo/consult.go` (around line 76) **and** its test
   `TestReconcileAbandonsLegacyPaneConsult` (`internal/relevo/consult_test.go:~206`),
   citing D1.7. The owner decided this knowing a stored pre-headless consult
   could reach it: no machine has such a record running. Do not stop on the
   "if one can, stop and report" gate for D1.7.

Everything else in the plan below stands.


Round of the codebase cleanup (spec: `docs/specs/2026-09-25-codebase-cleanup-design.md`,
§2.2 deletion items 1 and 4).

## 0. Rules for this round

- **The deletion list below is closed.** Everything not on it survives. A test
  that asserts surviving behaviour through a deleted mechanism is **ported**
  (its assertion changed), not deleted. Every test or test row you remove must
  cite its item (D1.x / D4.x) in the report.
- If a step is impossible as written, or the code contradicts this plan, **stop
  and report** -- do not improvise. A halt that surfaces a wrong assumption is
  worth more than a green suite.
- Do not refactor, rename or re-comment surviving code beyond what a deletion
  forces. Comments you touch lose issue numbers and history.
- Parallel rounds are deleting `land`/`review`/`bind --from` and the `history` /
  `serve status` reports, and adding golden tests. Touch `cmd/relevo/main.go`
  only at the lines named here, so merges stay trivial.
- `cmd/relevo` tests must not spawn a harness or reach the network.

## 1. System overview

Pane mode (builders running in a terminal-multiplexer pane) was removed
earlier; what remains is code that retires or tolerates old pane-era state.
The owner has decided to delete it. Old stored rows whose builder mode is
`"pane"` (115 in the owner's database, none live) must still **load** and show
in history and archives. `relevo edge` (planner-declared handoffs fired when a
round closes) has never been used and is deleted whole; old `bind.json` records
that carry an `edges` key must still load (the key is ignored).

## 2. File structure

```
DELETE internal/relevo/edges.go, internal/relevo/edges_test.go
DELETE cmd/relevo/edge.go, cmd/relevo/edge_test.go
EDIT   internal/store/types.go        (ModePane; Binding.Edges; type Edge and its doc)
EDIT   internal/usage/source.go       (ModePane)
EDIT   internal/ingest/ingest.go      (the one ModePane use)
EDIT   internal/relevo/reconcile.go   (legacyPaneBinding, retireLegacyPane, their call)
EDIT   internal/relevo/channel.go     (SweepPaneKeyed)
EDIT   internal/relevo/candidate.go   (OrphanedPane)
EDIT   internal/relevo/consult.go     (the "pane consults were removed" refusal)
EDIT   internal/relevo/headless.go    (the evaluateEdges call)
EDIT   internal/relevo/daemon.go      (runFires/armedFires)
EDIT   cmd/relevo/mcp.go              (the SweepPaneKeyed call)
EDIT   cmd/relevo/main.go             (dispatch `case "edge"`, the edge usage line)
EDIT   tests in: internal/store/types_test.go, internal/relevo/{deliver,consult,bind,channel,reconcile,remote}_test.go,
       and any other test the build or grep turns up for the removed symbols
EDIT   README.md                      (the edge reference and the edge section)
NEW    docs/plans/2026-09-26-cleanup-p1a-del-pane-edge.md  (a copy of this plan, last step)
```

## 3. Deletion list (closed)

**D1 -- pane leftovers**
- D1.1 `store.ModePane` constant, `internal/store/types.go:34`. Stored modes are
  plain strings, so a row holding `"pane"` still loads as `store.Mode("pane")`.
  Before deleting, grep for any validation of `Mode` values (a switch or a
  `valid` func in store, db or ingest); if one rejects unknown modes, keep
  `"pane"` accepted there and say so in the report.
- D1.2 `usage.ModePane`, `internal/usage/source.go:21-24` and every use of it.
  Where a use *reads* old rows (history, usage figures), replace the constant
  with the literal `"pane"` and keep the behaviour; where it serves only live
  pane rounds, delete the branch.
- D1.3 `internal/ingest/ingest.go:281` keeps writing the literal `"pane"` for
  archive imports: replace `string(store.ModePane)` with `"pane"` and a
  one-line comment saying archives from before headless mode record it.
- D1.4 `legacyPaneBinding` and `retireLegacyPane` (`internal/relevo/reconcile.go:104-~140`)
  and their call at `reconcile.go:204-205`.
- D1.5 `SweepPaneKeyed` -- the `ClaimStore` interface method
  (`internal/relevo/channel.go:60-64`), the `KVClaims` implementation
  (`channel.go:249-~280`), and the call `rt.Channels.SweepPaneKeyed()` at
  `cmd/relevo/mcp.go:190`. **Keep** the claim reader's rule that ignores
  pane-keyed claim rows (`channel.go:52`, `140`): old rows still exist and must
  not break delivery. Reword those two comments so they no longer mention
  `SweepPaneKeyed` (say the old rows are ignored and never rewritten).
- D1.6 `OrphanedPane` field (`internal/relevo/candidate.go:87-90`) and every
  write or read of it.
- D1.7 (REPLACED by round-2 decision 2 above) The pane-consult refusal in `internal/relevo/consult.go` (around line 76).
  First read how a consult could reach it: if no surviving flag or stored value
  can select a pane consult any more, delete the branch; if one can, stop and report.

**D4 -- `relevo edge`**
- D4.1 `internal/relevo/edges.go` whole (AddEdge, ListEdges, RemoveEdge,
  firePending, evaluateEdges, runFires, edgeArtifactPath) and `edges_test.go`.
- D4.2 The call `next, _, err = evaluateEdges(ctx, rt, tx, next, closedRound)`
  at `internal/relevo/headless.go:971` and anything that exists only to feed or
  consume it. `next` must end up exactly as it was before the call for every
  binding with no edges (i.e. the call's effect disappears, nothing else).
- D4.3 In `internal/relevo/daemon.go`: the fires block at lines ~175-186
  (`d.safely("fires", func() { runFires(ctx, d.rt, armedFires(fresh)) })` and
  its comment) and `armedFires` at ~271-285. If `fresh` becomes unused, remove
  what only produced it.
- D4.4 (REPLACED by round-2 decision 1 above) `store.Binding.Edges` (`internal/store/types.go:532-536`) and `type Edge`
  with its doc and constants (`types.go:~669-720`). A stored `bind.json` with an
  `edges` key must still load: confirm the decoder does not use
  `DisallowUnknownFields` (it does not today) and add one test row to an
  existing store load test that loads such a record.
- D4.5 `cmd/relevo/edge.go`, `edge_test.go`; the `case "edge":` dispatch at
  `cmd/relevo/main.go:372-373`; the `edge ...` line(s) in the usage text near the
  top of `main.go`; any `verbHint`-style map entry for edge.
- D4.6 README: the `relevo edge` bullet (around line 426-427) and the edge section
  (around lines 915-950). Leave neighbouring sections intact.

If `internal/store/testdata/binding-shape.golden` (pinned by
`internal/store/format_test.go`) contains an `edges` key, regenerate it with
`go test ./internal/store -run <that test> -update`, cite D4.4, and do **not**
bump `BindingFormat` unless that test says a shape change requires it -- if it
does, stop and report instead.

## 4. Interfaces after the round

No new interfaces. `ClaimStore` loses one method (D1.5); every implementation
and fake of it loses the method too. `relevo edge` becomes an unknown verb
(the dispatcher's normal unknown-verb error).

## 5. Pseudocode (the one behavioural seam)

```
reconcile(b):                       // before: if b not done and legacyPane(b): retire(b)
    ... existing flow, unchanged, for every binding ...
headless round close:               // before: next = evaluateEdges(next)
    next = <value before the call>
daemon tick:                        // before: runFires(armedFires(fresh))
    (nothing)
```

## 6. Error handling

No new errors. If deleting D1.4 would change how a *currently possible*
binding is reconciled (for example a local binding with an empty mode that
new code can still create), stop and report which path creates it.

## 7. Working efficiently

- Read every file in §2 at the named lines in one batch; the symbol list in §3
  is complete as of `main` 0583bea, so use grep only to confirm nothing else
  references a deleted symbol: `grep -rn --include='*.go' -E 'ModePane|legacyPaneBinding|retireLegacyPane|SweepPaneKeyed|OrphanedPane|evaluateEdges|armedFires|runFires|firePending|store\.Edge\b|\.Edges\b|AddEdge|ListEdges|RemoveEdge|cmdEdge' .`
- Delete files with one `git rm`. Make each file's edits in one edit call.
- Focused loop: `go build ./... && go vet ./internal/relevo ./internal/store ./internal/usage ./internal/ingest ./cmd/relevo`
  then `go test ./internal/relevo ./internal/store ./internal/usage ./internal/ingest ./cmd/relevo -count=1`.
- Full check once at the end: `make check`, then `make e2e`.

## 8. Ordered steps

1. D4.1, D4.2, D4.3, D4.5 (edge code and callers). Build passes.
2. D4.4 (store type) and its load test row. `go test ./internal/store` passes.
3. D1.1-D1.3 (constants). Build passes.
4. D1.4-D1.7. Build passes.
5. Tests: fix every compile failure; for each failing or removed test decide
   delete (cite item) vs port (say what assertion changed). Focused tests pass.
6. D4.6 README.
7. Final grep (§7) returns nothing outside comments you intentionally kept
   (list them). `make check` and `make e2e` pass. `git diff --stat` shows only
   §2 files.
8. **Save the plan.** Copy this plan to
   `docs/plans/2026-09-26-cleanup-p1a-del-pane-edge.md` and commit it.

Report: per deletion item, the lines removed; every removed or ported test with
its item; the D1.1 validation finding; lines of non-test and test code removed
(`git diff --shortstat` split by `_test.go`).
