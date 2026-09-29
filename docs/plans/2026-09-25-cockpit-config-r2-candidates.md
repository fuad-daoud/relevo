# Cockpit config views, round 2: plumbing and the `:candidates` list (2026-09-25)

Branch `relevo/ck-config`. It already has round 1's commit, which adds `internal/relevo/configedit.go`
(`ConfigDoc`, `LoadConfigDoc`, `CandidateSlots`, `DeleteCandidate`, `WriteConfigEdit`, `ReloadConfig` and
`FieldError`) and `harness.Providers`/`AgentFiles`. **Amend that commit** (`git commit --amend --no-edit`); do not add a
second one.

## 1. System overview

This round wires the cockpit to config and ships the `:candidates` screen with its read-only table and detail block,
plus four actions: gate, ungate, probe and delete (with a confirmation). The add and edit forms are round 3, so `a` and
`enter` are **not** bound this round.

The design is the approved canvas board `:candidates D2` (132×34). Its text layout at 132 columns:

```
  ◆ relevo    candidates                                                       <version>    <clock>

   7 candidates   3 ready   4 gated                                                                        sort pick order

   CANDIDATE                         HARNESS   PROVIDER    MODEL                                SERVES                  STATUS
   gemini-3.8-flash-high             agy       google      gemini-3.8-flash-high                builder 1               gated 2h
   claude-sonnet-4-6                 agy       agy-extra   claude-sonnet-4-6                    builder 2               gated 20h
   deepseek-v4.1-flash               opencode  cline-pass  cline-pass/deepseek-v4.1-flash#high  builder 3               gated 1d
   gpt-5.6-terra                     codex     openai      gpt-5.6-terra:high                   builder 4 · reviewer 2  gated 24d
   sonnet                            claude    anthropic   sonnet                               builder 5 · reviewer 1  ready
   glm-5.3-flash                     opencode  openrouter  z-ai/glm-5.3-flash                   builder 6               ready
   haiku                             claude    anthropic   haiku                                researcher 1            ready


   gemini-3.8-flash-high   agy · google · gemini-3.8-flash-high
   gated since 20:41, until cleared   Individual quota reached
   the builder's first pick; while it is gated the builder takes sonnet
   …
 ↑↓ move   enter edit   a add   d delete   g gate   u ungate   p probe   : command   ? all keys   q quit
```

- The cursor row (the first row here) is on the selection band across the full row, with its name bold.
- In the detail block, the name is faint bold and the rest muted. `gated` is red, the reason is text-coloured, and the
  "takes sonnet" name is text-coloured.

## 2. File structure

```
internal/ui/
  actions.go          Actions interface: + ConfigDoc, ApplyConfig, Probe; plannerActions implements them
  ui.go               Options gains ProbeExec relevo.LineExec; Run passes it to plannerActions
  cmdline.go          commands table: + {"candidates", "", "candidates, gates and who picks them", false}
  view_rounds.go      execLine: + case "candidates"
  view_candidates.go  NEW: candidatesView (list, detail, keys, delete confirm, gate form, ungate, probe)
  view_candidates_test.go NEW
  actions_test.go     fakeActions implements the three new methods (records calls, scripted doc/results)
  golden_test.go      + cases candidates-132, candidates-100, candidates-delete-132
  testdata/*.golden   new goldens, plus the regenerated cmdline-open and command-real-132
cmd/relevo/main.go    ui.Run(…, ui.Options{…, ProbeExec: lineExec{}})   (line ~2554)
docs/plans/2026-09-25-cockpit-config-r2-candidates.md
```

## 3. Data structures

- The Actions interface (`internal/ui/actions.go:23`) gains:
  ```go
  ConfigDoc() (relevo.ConfigDoc, error)                        // the stored config, freshly read
  ApplyConfig(ctx context.Context, e relevo.ConfigEdit) Result // write one edit, then reload this adapter's runtime
  Probe(ctx context.Context, name string) Result               // probe one candidate (spawns its harness)
  ```
- `plannerActions` gains a field `probe relevo.LineExec`.
- `candidatesView` (view_candidates.go):
  ```go
  type candidatesView struct {
      doc     relevo.ConfigDoc
      err     error   // the last ConfigDoc error; shown centred like stats' error state
      loaded  bool
      cur     int     // cursor over rows()
      top     int     // first body line shown (page follows the cursor)
      actions bool    // env.Actions != nil at construction
  }
  ```
- `candRow` is one table row, computed per render from `doc` and `env.Report.Gated`, never stored:
  ```go
  type candRow struct {
      c      candidate.Candidate
      slots  []relevo.ActorSlot // CandidateSlots(doc, c.Name), on entries only, for SERVES
      on     bool               // some actor has it on
      gate   *ledger.Gate       // the gate on its token from env.Report.Gated, nil when none
  }
  ```

## 4. Contracts

### Actions (actions.go)

- `plannerActions.ConfigDoc()` is `relevo.LoadConfigDoc(a.rt.Config)`. A nil `a.rt.Config` gives the error
  `"no config store"`.
- `plannerActions.ApplyConfig(ctx, e)`:
  1. `relevo.WriteConfigEdit(a.rt.Config, e)`. An error goes into `Result{Err}`.
  2. `rt, err := relevo.ReloadConfig(a.rt)`. On success, set `a.rt = rt`. On error, write the result anyway (the write
     happened) with `Text: "saved; reload failed: <err>"`.
  3. Return `Result{Text: e.Message, Refresh: true}`.
- `plannerActions.Probe(ctx, name)`:
  - With `a.probe == nil`, return `Result{Err: errors.New("probing needs relevo ui")}`.
  - Otherwise run `relevo.Probe(ctx, a.rt, a.probe, []string{name}, host, nil)`, with `host` computed exactly as the
    CLI does at `cmd/relevo/main.go` just before line 958.
  - Return `Result{Text: relevo.FormatProbe(r, relevo.ProbeNameWidth([]string{name}))}`.
  - A `ProbeResult` with an error field must come back as `Result{Err}`. Read `ProbeResult`/`latency.Sample` for how an
    error is carried.
- `fakeActions` (actions_test.go:35) records `configEdits []relevo.ConfigEdit` and `probes []string`, and returns a
  scripted `doc relevo.ConfigDoc` / `docErr`.

### `:candidates` (view_candidates.go)

- **`newCandidatesView(env Env) (View, tea.Cmd)`** returns the view and a command that loads the doc
  (`candDocMsg{doc, err}`) via `env.Actions.ConfigDoc()`.
  - When `env.Actions == nil`, execLine instead returns
    `notice("the config views need relevo ui on this machine")`, and no view is built.
  - `execLine` case `"candidates"`: `v, init := newCandidatesView(env); return rootThen(init, v)`.
- **Crumbs:** `{"candidates"}`. **Capturing:** always false.
- **Context left:** `   <n> candidates   <r> ready   <g> gated` plus `   <o> off` when o > 0. Numbers are
  textStyle-bold and words muted, as in `:rounds`' summary. **Context right:** `sort ` faint + `pick order` muted.
- **Row order is pick order:**
  1. Walk the actors in this order: `builder`, `reviewer`, `researcher`, then any other actor by name.
  2. Within each actor, walk its entries in order and emit each candidate the first time it is seen, including off
     entries.
  3. Then emit every remaining candidate in stored order.
- **Columns**, each followed by two spaces:
  - CANDIDATE takes the rest of `[3, width-3)`;
  - HARNESS 8, PROVIDER 10, MODEL 35, SERVES 22, STATUS 9, with STATUS last.
  - **Narrow widths:** while the name column would be under 16, drop columns in this order: MODEL, then SERVES, then
    HARNESS.
  - Clip over-long cells with `clip` (ellipsis).
- **SERVES** is `<actor> <position>` for each slot where the candidate is **on**, joined `" · "`, in actor-name order.
  Empty when none.
- **STATUS** and row style:
  - Not on for any actor: `off` in faint, and the whole row faint.
  - Otherwise, with a gate: `statsGateLeft(*gate, envNow(env))`, red (`gated 2h`).
  - Otherwise: `ready`, green.
  - Gate lookup: the first `env.Report.Gated` entry whose `Token` equals the candidate's `Ref().String()`.
- **Cursor row:** per-cell `selBandStyle` background across the whole row. Copy how `view_log.go` builds its selected
  row. The name cell is bold.
- **Detail block:** two blank lines after the table, then the lines for the cursor row, each starting `"   "`:
  1. The name (faint, bold), three spaces, then `harness · provider · model` (muted).
  2. Only when gated: `gated` (red), then ` since HH:MM` (muted, the gate's `Since` in local time; include the date as
     `Jan 2 15:04` when it is not today), then `, until cleared` or `, until <statsGateUntil>` (muted), three spaces,
     then `statsGateReason(gate.Note)` (text).
  3. The pick sentence, muted with actor and candidate names in text:
     - **Not on anywhere:** `off: no actor has it on`.
     - **Otherwise:** a list of slots, `the builder's first pick`, joined with `" and "`. The possessive is `the <actor>'s`
       and the ordinal is first … tenth, then `#11`.
     - **When gated,** append `; while it is gated the <first slot's actor> takes <next>`. `<next>` is the first entry in
       that actor's list after or before this one that is on and not gated. When none: `; the <actor> has no other ready
       candidate`.
- **Keys** (listed in `Keys()` only when `actions`):
  - `↑↓` move and the page follows (copy view_stats' `follow`).
  - `home`/`end` and `pgup`/`pgdown` move the cursor.
  - `enter` edit and `a` add are **shown but inert** this round (return nil). Round 3 binds them.
  - **`d` delete:**
    1. `edit, err := relevo.DeleteCandidate(doc, name)`. On error, return `notice(err.Error())`.
    2. Otherwise open a **danger** `confirmBox`, `kind:"delete"`, `yes:"delete"`, with the title
       `Delete <name>?` (name accent-bold).
    3. Its lines, each a label column padded to 11 (faint) then text:
       - `picked by` + `builder 4 · reviewer 2`, then `   both drop it` (or `   it drops it` for one slot); omit the line
         when there are no slots;
       - for each actor whose on-list shrinks to exactly one remaining entry: a line under it (label blank):
         `the <actor> is left with <name>`;
       - `kept` + `<provider>'s gate, until <statsGateUntil>` when gated;
       - blank label + `its past rounds in :rounds and :stats`.
    4. `onYes` is `runAction(env.Ctx, "delete", name, func(ctx) Result { return env.Actions.ApplyConfig(ctx, edit) })`.
  - **`g` gate:** a `formBox` built like `gateCmd` (confirm.go:348), with the same "for"/"reason" fields, validator,
    hint and note. The header is `Gate <name>  (provider <provider>)`. The subject passed to `env.Actions.Gate` is the
    candidate **name**.
  - **`u` ungate:** `runAction(env.Ctx, "ungate", name, … env.Actions.Ungate(ctx, name))`, with no confirm, as
    `:ungate` does.
  - **`p` probe:** `runAction(env.Ctx, "probe", name, … env.Actions.Probe(ctx, name))`.
- **Update:**
  - On `candDocMsg`, store the doc or the error and clamp the cursor.
  - On `statusMsg`, return the doc-load command again. Config changes from anywhere show up on the next refresh, and
    this refresh is what reloads the table after `ApplyConfig` (its `Refresh: true`).
  - `tea.WindowSizeMsg` needs nothing.
- **Body:**
  - Before the first doc: the centred `loading…` (copy stats).
  - On an error: centred `emptyStyle` text.
  - Zero candidates: centred `no candidates; a adds one`.

### actKeys (frame.go:458)

Leave unchanged. The help modal lists the new view keys under MOVE & VIEW, which is acceptable.

## 5. Pseudocode

```
Update(msg):
  candDocMsg → v.doc, v.err, v.loaded = …; v.cur = clamp(v.cur, 0, len(rows)-1)
  statusMsg  → return v, loadDoc(env)
  KeyMsg (when actions):
    up/down/home/end/pgup/pgdown → move cursor; follow
    d → DeleteCandidate → notice(err) | openOverlay(confirmBox{danger…})
    g → openOverlay(gateForm(name, provider))
    u → runAction(ungate)
    p → runAction(probe)
```

## 6. Error handling

- A ConfigDoc error renders in the body and does not crash. An action's error reaches the red notice and `:log` through
  the existing `finishAction`.
- A refused delete (FieldError) is a notice, not an overlay.

## 7. Working efficiently

- **Read in one parallel batch:**
  - `internal/ui/actions.go`, `ui.go`, `cmdline.go`, `view_rounds.go:200-284`, `confirm.go:40-130, 300-395`
  - `internal/ui/view_log.go:640-760` (band rows), `view_stats.go:120-175, 580-720, 1600-1760, 2200-2260`
  - `internal/ui/golden_test.go:1-120, 540-600, 650-700`, `actions_test.go:30-160`
  - `internal/relevo/configedit.go`, `probe.go:30-60, 200-320`
  - `cmd/relevo/main.go:930-962, 2515-2560`
- **Focused tests** (local; `make check` is refused on this machine by a hook, so do not run it):
  `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -run 'Candidates|Golden|Cmdline|Actions' -count=1`
- **Goldens:** `… go test ./internal/ui/ -run TestGoldenViews -update -count=1`.
  - Expected changes: the three new files, plus `cmdline-open.golden` and `command-real-132.golden` (one new
    `candidates` line in the command list).
  - Any other golden changing means stop and report.
- **Final:** `go vet ./internal/... ./cmd/...`, `test -z "$(gofmt -l $(git ls-files '*.go'))"`, `go mod tidy -diff` and
  `sh scripts/check-name.sh`.

## 8. Steps

1. **Actions:** the three methods on the interface, `plannerActions`, `fakeActions`, `Options.ProbeExec`, `Run`
   passing it, and `main.go` setting `ProbeExec: lineExec{}`.
   - Verify: `go build ./...`.
   - **CI rule:** no test may call `plannerActions.Probe` with a real exec. Test the view against `fakeActions` only.
2. **`view_candidates.go`**, and register the command in cmdline and execLine.
3. **Goldens.** Use a fixture doc of this machine's seven candidates and three actors, the same data as round 1's
   tests; reuse its helper if it is exported to tests, else copy the JSON. Gate fixtures on `env.Report.Gated`:
   - gemini: Until zero, note `RESOURCE_EXHAUSTED (code 429): Individual quota reached`, since `railNow` − 45m;
   - claude-sonnet-4-6: until `railNow` + 20h;
   - deepseek: until + 1d;
   - gpt: until + 24d.

   Cases (use `goldenActionModel` with `fakeActions{doc: fixture}`, then send `:candidates` via `execLine` or
   `Options.Start: "candidates"` and drain):
   - `candidates-132`: 132×34, cursor on row 1;
   - `candidates-100`: 100×30;
   - `candidates-delete-132`: `d` pressed on `gpt-5.6-terra`.
4. **Unit tests** (view_candidates_test.go):
   - pick order;
   - SERVES text;
   - off row → STATUS `off` + faint;
   - the gated pick sentence names `sonnet`;
   - `d` on `haiku` → a notice containing "researcher has no other candidate" and no overlay;
   - `y` on the delete confirm → `fakeActions.configEdits` holds one edit with message `delete candidate gpt-5.6-terra`;
   - `u` → `ungates == ["gemini-3.8-flash-high"]`;
   - `p` → `probes == [name]`;
   - `enter`/`a` do nothing this round;
   - a nil Actions `:candidates` gives the notice.

   **Mutation (required):** make SERVES include off entries, and a named test must fail. Restore it.
5. **Final checks** (§7). Copy this plan to the worktree's `docs/plans/`, `git add -A`, `git commit --amend --no-edit`.

## 9. Stop rather than improvise

If `relevo.Probe`'s result shape, `statsGateLeft`'s signature, or the gate fixture fields differ from what §4 needs,
halt and report. The same applies if a golden other than the listed ones changes. Do not bend a test to fit.
