# Cockpit: gates on unused providers, and greyed footer keys

## 1. System Overview

A rate-limit gate belongs to a **provider**. When a candidate's provider is renamed, or
the last candidate on a provider is deleted, that provider's live gate stays in the
ledger. `relevo.Gates` (internal/relevo/ledger.go:274) projects the ledger onto the
configured candidates only, so today **nothing shows such a gate**. The real case on
this machine: a `rate_limited` entry on `antigravity`, which was renamed to `agy-extra`.
It is still live and blocks nothing.

This round does three things:
1. **Data.** `relevo.Report` gains the live rate-limit gates on providers that no
   configured candidate uses.
2. **`:candidates`.** The view lists them in a second table under the candidates. The
   cursor can land on one; `u` clears it, and the detail block explains it.
3. **Footer.** A view can mark some of its keys as not applying to the current row, and
   the footer draws them greyed out. `:candidates` uses this: on an unused-provider row,
   `enter`, `d`, `g` and `p` are greyed out and do nothing.

The approved design is the board ":candidates D2 · a gate on a provider no candidate
uses", reproduced as text in §5.3. Match it exactly.

**If a step is impossible as written or contradicts the code, stop and report. Do not
improvise, and do not bend a test to fit.**

Out of scope:
- `spawn_failed` entries on unconfigured tokens;
- `relevo status` text and JSON output: the new field is `json:"-"`, so the pinned CLI
  goldens do not move;
- `serve ui`;
- every other view's footer.

## 2. File Structure

```
internal/relevo/
  unused_gates.go             NEW   ProviderGate, UnusedProviderGates
  unused_gates_test.go        NEW
  status.go                   EDIT  Report.Unused (json:"-"); buildReport fills it (line ~351); HideDone carries it (line ~781)
internal/ui/
  view.go                     EDIT  offKeyer interface next to helpKeyer (line 27)
  styles.go                   EDIT  offKbdStyle, offStyle
  frame.go                    EDIT  keysView renders a view's off keys greyed (lines 200-248)
  view_candidates_unused.go   NEW   the unused-provider table, its detail block, the cursor helpers
  view_candidates.go          EDIT  cursor spans both tables; keys, Context, bodyLines, follow, OffKeys
  view_candidates_test.go     EDIT  new tests (§8 step 4)
  golden_test.go              EDIT  one new golden case
  testdata/candidates-unused-132.golden   NEW (generated)
docs/plans/2026-09-26-cockpit-unused-provider-gates.md   NEW (last step): this plan, verbatim
```

`view_candidates.go` is already 796 lines and on the file-size allowlist. Put **all new
code** in `view_candidates_unused.go`, which must stay under 600 lines. In
`view_candidates.go`, change only the call sites listed in §4.

## 3. Data Structures & Type Definitions

### `relevo.ProviderGate` (internal/relevo/unused_gates.go)

| field | type | meaning |
|---|---|---|
| `Provider` | `string` | the ledger entry's Subject (a provider name) |
| `Since` | `time.Time` | the entry's At |
| `Until` | `time.Time` | the entry's Until; zero = until cleared |
| `Note` | `string` | the entry's Note (raw; the view extracts the reason) |
| `Source` | `string` | `relevo` or `planner` |
| `Binding` | `string` | the binding that recorded it; may be empty |

### `relevo.Report` (internal/relevo/status.go:308)

The new field is `Unused []ProviderGate \`json:"-"\``, with a comment that says why it
is kept out of JSON: the status document is a pinned contract.

### `ui.offKeyer` (internal/ui/view.go)

`type offKeyer interface{ OffKeys(env Env) []string }`. It returns the `Key` strings of
the view's `Keys()` that do not apply right now. It is optional; a view without it has
no off keys.

### Styles (internal/ui/styles.go)

- `offKbdStyle`: background `#15181e`, foreground `#3d4350`.
- `offStyle`: foreground `#3d4350`.

## 4. Interface Definitions & Component Contracts

### `relevo.UnusedProviderGates(rt Runtime) []ProviderGate`

- It returns nil when `rt.Gates` is nil or `rt.Candidates` is nil. A runtime with no
  candidates has nothing to compare against.
- It loads the ledger with `ledger.LoadKV(rt.Gates, ledgerLegacyPath(rt))`. A load error
  returns nil **silently**: `Gates` already reports that error once on stderr.
- It prunes with `rt.Now()` and keeps entries with `Kind == ledger.RateLimited` whose
  `Subject` is not in `rt.Candidates.Providers()`.
- It sorts by Provider, then Since.
- It never writes.

### `buildReport` / `HideDone` (status.go)

- `rep.Unused = UnusedProviderGates(rt)` goes directly after `rep.Gated = Gates(rt)`.
- `HideDone` copies `Unused` the same way it copies `Gated`. It is machine-wide, not per
  binding.

### `keysView` (frame.go:200)

- The view's keys get off-aware rendering. When the top view implements `offKeyer`, a key
  whose `Key` is in `OffKeys(env)` renders as `chip(offKbdStyle, k) + " " + offStyle.Render(v)`.
  Every other key renders as today.
- The global tail and modal keys are never off.
- Layout, widths and the drop order are unchanged, because a greyed key takes the same
  cells.

### `candidatesView` changes (view_candidates.go)

Let `n = len(v.rows(env))` and `m = len(env.Report.Unused)`. The cursor `v.cur` now
ranges over `[0, n+m)`. Index `n+i` is unused gate `i`.

The empty-state message "no candidates; a adds one" is unchanged when `n == 0`: the
unused table is not shown without candidates.

| site | change |
|---|---|
| `Update` candDocMsg (line ~602) | clamp with `n+m` |
| `updateKey` (lines 621-682) | clamp with `n+m`; on an unused row (`v.cur >= n`), `enter`/`d`/`g`/`p` return `v, nil`; `a` opens the form with `""` harness; `u` runs `runAction(env.Ctx, "ungate", provider, … env.Actions.Ungate(ctx, provider))` where provider = the row's `Provider` |
| `OffKeys(env)` (new method) | nil unless `v.actions` and `v.cur >= n` with `m > 0`; then `["enter", "d", "g", "p"]` |
| `Context` (line 540) | when `m > 0`, append `"   " + item(m, "gate on an unused provider")`, or `"gates on unused providers"` when `m > 1` |
| `bodyLines` (line 446) | after the candidate rows, when `m > 0`: `""`, the unused header line, one line per unused gate; then the existing `"", ""` and the detail block, which is the candidate detail when `v.cur < n`, else the unused detail |
| `follow` (line 471) | the cursor's body line is `2 + cur` when `cur < n`, else `2 + n + 2 + (cur - n)` (blank, header) |

### view_candidates_unused.go (new)

- `unusedCols(width int, nameW int) (reasonW, setByW int)`
  - The table spans `cw = width-6` from column 3, with cells separated by two spaces.
  - PROVIDER is `nameW` wide, the same width as the CANDIDATE column from
    `candLayout(width)`.
  - STATUS is 9, SET BY is 33, and REASON takes the rest:
    `reasonW = cw - nameW - 2 - 33 - 2 - 9 - 2`.
  - When `reasonW < 16`, SET BY is dropped (`setByW = 0`) and REASON takes
    `cw - nameW - 2 - 9 - 2`.
  - At 132 columns this gives nameW 32, REASON 46, SET BY 33 and STATUS 9, so STATUS
    lines up with the candidates' STATUS column.
- `unusedHeaderLine(nameW, reasonW, setByW, cw) string`: `UNUSED PROVIDER`, `REASON`,
  `SET BY` and `STATUS` in `faintStyle.Bold(true)`, through `candLine`.
- `unusedDataLine(g relevo.ProviderGate, sel bool, nameW, reasonW, setByW, cw int, now time.Time) string`
  - The name is in `textStyle`, bold when selected.
  - The reason is `statsGateReasonText(g.Note)` in `mutedStyle`.
  - SET BY is `Source + " · " + Binding`, or `Source` alone when Binding is empty, in
    `textStyle`.
  - STATUS is `statsGateLeft(asGate(g), now)` in `redStyle`.
  - Every cell is padded with `pad` and drawn through `candLine(cells, sel, cw)`.
- `asGate(g relevo.ProviderGate) ledger.Gate`: `{Kind: ledger.RateLimited, Since, Until, Note, Source, Binding}`,
  so the existing `statsGateLeft`, `candSince` and `candUntilText` helpers are reused.
- `unusedDetailLines(g relevo.ProviderGate, now time.Time, width int) []string` gives
  three lines, each through `fit(…, width)`:
  1. `"   " + faintStyle.Bold(true).Render(p) + "   " + mutedStyle.Render("a provider no candidate uses")`
  2. the candidate detail's gate line with the same styles:
     `"   " + redStyle.Render("gated") + mutedStyle.Render(" since "+candSince(…)) + mutedStyle.Render(", "+candUntilText(…))`,
     plus `"   " + textStyle.Render(reason)` when the reason is non-empty
  3. `"   " + mutedStyle.Render("no candidate uses "+p+"; ") + textStyle.Render("u") + mutedStyle.Render(" clears it")`

## 5. High-Level Pseudocode

### 5.1 Data

```
buildReport: rep.Gated = Gates(rt); rep.Unused = UnusedProviderGates(rt)
UnusedProviderGates: nil-guards -> LoadKV (err -> nil) -> Prune(now)
    -> filter RateLimited && Subject not in Candidates.Providers() -> sort -> []ProviderGate
```

### 5.2 Keys on an unused row

```
u      -> Ungate(provider)    # relevo.Available already accepts a bare gated provider
                              # (internal/relevo/available.go:74-76)
a      -> add-candidate form, empty harness
enter, d, g, p -> nothing (greyed in the footer)
↑↓ home end pgup pgdown -> move across both tables
```

### 5.3 The approved board, as text (132 columns, cursor on the unused row)

```
   7 candidates   2 ready   5 gated   1 gate on an unused provider                          sort pick order

   CANDIDATE                         HARNESS   PROVIDER    MODEL                                SERVES                  STATUS
   ... seven candidate rows ...

   UNUSED PROVIDER                   REASON                                          SET BY                             STATUS
   antigravity                       Individual quota reached                        relevo · oc-tui-a                  gated 3h     <- cursor band


   antigravity   a provider no candidate uses
   gated since Sep 24 22:37, until 18:04   Individual quota reached
   no candidate uses antigravity; u clears it
footer:  enter edit (grey)   a add   d delete (grey)   g gate (grey)   u ungate   p probe (grey)   : command   ? all keys   q quit
```

## 6. Error Handling Strategy

- A ledger load failure gives an empty `Unused` and no output. `Gates` already warns.
- A failed ungate goes through `runAction` as today: `Result.Err` becomes a red notice.
- No new error types.

## 7. Working Efficiently

- Read these in one batch, and nothing else:
  - internal/relevo/status.go 300-360 and 770-790;
  - internal/relevo/ledger.go 250-300;
  - internal/relevo/bind_test.go 41-66 (`newRuntime`: tests write entries with
    `ledger.SaveKV(rt.Gates, ledger.Ledger{Entries: …})`);
  - internal/ui/view.go 1-40;
  - internal/ui/styles.go 1-60;
  - internal/ui/frame.go 190-290;
  - internal/ui/view_candidates.go (all);
  - internal/ui/view_candidates_test.go 1-120 and 290-320;
  - internal/ui/golden_test.go 1100-1135 and 1330-1360;
  - internal/ui/detail_test.go 118-132 (how a test turns colour on and restores it).
- Make each file's changes in one edit.
- Focused loop: `go build ./... && go test -count=1 ./internal/relevo/ -run 'Unused|HideDone' && go test -count=1 ./internal/ui/ -run 'Candidates|Footer|Golden'`
- Full check, once at the end: `make check`. On this server `/tmp` is a small tmpfs.
  Run it as
  `export TMPDIR=$HOME/.cache/go-tmp GOTMPDIR=$HOME/.cache/go-tmp; mkdir -p $TMPDIR; make check`.

## 8. Ordered Implementation Steps

**Step 1: data.** Write `internal/relevo/unused_gates.go` and edit status.go, both per §4.
- Write `internal/relevo/unused_gates_test.go` using `newRuntime(t)`, whose candidates
  come from `testCandidatesJSON`. Read which providers those use and pick a provider
  outside them.
  - `TestUnusedProviderGatesListsOnlyProvidersNoCandidateUses`
    - The ledger holds: a live rate limit on the unused provider; a live rate limit on
      a configured provider; an **expired** rate limit on a second unused provider; and
      a `spawn_failed` on an unconfigured token.
    - Only the first comes back, with all six fields.
  - `TestUnusedProviderGatesWithoutCandidatesIsNil`
  - `TestHideDoneKeepsUnusedProviderGates`
- Verify: `go test -count=1 ./internal/relevo/ -run 'Unused|HideDone'`.

**Step 2: greyed footer keys.** Edit view.go, styles.go and frame.go per §3-§4. Depends
on nothing.
- Test `TestFooterGreysAViewsOffKeys` in view_candidates_test.go, or wherever footer
  tests live:
  - turn on TrueColor as detail_test.go:124-126 does, restoring the profile afterwards;
  - build a model whose top view implements `offKeyer` (a `candidatesView` with an
    unused row selected works);
  - assert `m.keysView(env)` contains `chip(offKbdStyle, "d") + " " + offStyle.Render("delete")`
    and contains `chip(kbdStyle, "u") + " " + mutedStyle.Render("ungate")`.
- Verify: `go test -count=1 ./internal/ui/ -run Footer`.

**Step 3: `:candidates`.** Write view_candidates_unused.go and edit view_candidates.go
per §4. Depends on steps 1 and 2.
- Verify: `go build ./... && go test -count=1 ./internal/ui/`. **Every existing golden
  must pass unchanged.** Their fixtures have no unused gates, so nothing they draw may
  move. If one changes, stop and report.

**Step 4: tests and the golden.** Depends on step 3.
- The fixture is `candUnusedReport()`: `candGatedReport()` plus
  `Unused: []relevo.ProviderGate{{Provider: "antigravity", Since: railNow.Add(-40*time.Hour), Until: railNow.Add(3*time.Hour), Note: "RESOURCE_EXHAUSTED (code 429): Individual quota reached", Source: "relevo", Binding: "oc-tui-a"}}`.
- Unit tests in view_candidates_test.go:
  1. `TestCandidatesCursorReachesTheUnusedRow`: `end` puts the cursor on index n, and
     `OffKeys` is `[enter d g p]`. On a candidate row `OffKeys` is empty.
  2. `TestCandidatesUngateOnAnUnusedRowClearsItsProvider`: `u` on the unused row calls
     `fakeActions.Ungate` with `"antigravity"`. Mirror `TestCandidatesUngateKey` at
     line 297.
  3. `TestCandidatesCandidateKeysDoNothingOnAnUnusedRow`:
     - `enter`, `d`, `g` and `p` there open no overlay and run no action;
     - assert that no fakeActions call was recorded and the returned cmd is nil.
  4. `TestCandidatesContextCountsUnusedProviderGates`:
     - one gate gives "1 gate on an unused provider";
     - two give "2 gates on unused providers";
     - none gives today's text unchanged.
- The golden case `candidates-unused-132`, 132×34:
  - `goldenCandidatesModel(t, 132, 34, &fakeActions{doc: candFixtureDoc(t)}, candUnusedReport())`,
    then `end`;
  - generate it with `go test ./internal/ui/ -run Golden -update`;
  - then **read the golden file** and check it line by line against §5.3: the counts
    line, header words, column alignment (STATUS under STATUS), the three detail lines
    and the footer's key order.
  - Put any difference in the report. Do not hand-edit the golden.
- **Required mutations.** Run each, confirm the named test fails, then restore. List all
  four in the report.
  - (a) `UnusedProviderGates` drops the `Providers()` filter: the step 1 test fails.
  - (b) `keysView` ignores `offKeyer`: `TestFooterGreysAViewsOffKeys` fails.
  - (c) `u` on an unused row passes the candidate path (`rows[...]`), or nothing: test 2
    fails.
  - (d) `HideDone` does not copy `Unused`: `TestHideDoneKeepsUnusedProviderGates` fails.
- No test spawns a harness or reaches the network. Keep `t.Parallel()` wherever the
  surrounding tests use it. A test that sets the global colour profile must **not** be
  parallel.

**Step 5: full check.** Depends on step 4.
- Run `make check`, exported as in §7. It must pass.
- The coverage baselines are `internal/ui` 82.0 and `internal/relevo` as listed in
  testdata/coverage-baseline.txt. Never lower one.
- Add no entry to `.golangci.yml`, `scripts/check-comments.allow` or
  `scripts/check-filesize.allow`. The two new files must pass `check-comments.sh`
  unlisted.
- Comments say why, never restate code, and carry no history, issue numbers or
  "round N".

**Step 6: ship the plan.** Copy this plan verbatim to
`docs/plans/2026-09-26-cockpit-unused-provider-gates.md`. Commit everything as **one new
commit**: `feat(cockpit): gates on unused providers in :candidates; greyed footer keys`.
Never amend, and never rebase.

## Report

The report covers:
- `git diff --stat`;
- each mutation, with its failing test;
- the golden file's full text;
- `make check`'s last lines;
- anything in this plan that did not match the code.
