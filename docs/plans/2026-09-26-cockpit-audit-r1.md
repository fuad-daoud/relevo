# Cockpit `:audit`, round 1: config revisions, their changes, roll back (2026-09-26)

## 1. System overview

Every config write is a revision in `relevo.db` (`config_revision`: rev, at, source, message, version, changes JSON,
snapshot JSON). `relevo config log` and `relevo config rollback` read and restore them.

This round adds the cockpit view `:audit`:
- **The list:** revisions grouped by day, newest first.
- **The detail block:** the selected revision's changes, in human words. Candidates appear by name, not by
  `candidates[1]`.
- **`enter`:** opens a scrollable list of all its changes.
- **`R`:** rolls the config back to that revision, behind a red confirm that lists exactly what would change.

**The one rule the CLI lacks:** a rollback is checked the way a cockpit edit is checked, and refused with the reason
when the result would not pass.
- Today `Store.Rollback` validates each section alone.
- It never runs the cross-section check (`dryRun`) or the harness provider lock (`validateCandidateInput`,
  `configedit.go:496-510`).

Example: rolling this machine back to revision #3 would set `claude-sonnet-4-6`'s provider to `antigravity`, which agy
no longer takes. The cockpit must refuse it.

The approved design is the canvas boards `AuditD2` and `AuditRollbackD2`. Their text is in §5.3.

## 2. File structure

```
internal/config/
  revision.go            + (s *Store) RevisionDoc(rev) (Doc, error); + (s *Store) Current() (Doc, error)
internal/relevo/
  configedit.go          extract the provider-lock rule of validateCandidateInput into checkProvider (no behaviour change)
  configaudit.go         NEW: ChangeLine, DescribeChange, RevisionChanges, RollbackPreview, RollbackRefused, CheckDoc
  configaudit_test.go    NEW
internal/ui/
  actions.go             Actions + 4 methods; plannerActions implements them
  actions_test.go        fakeActions implements them (fields: revs, changes map, preview, previewErr, rollbacks []int64)
  cmdline.go             + {"audit", "", "config revisions, and roll back", false} after "agents"
  view_rounds.go         execLine: + case "audit" (same shape as "agents", ~279-284)
  view_audit.go          NEW: auditView (list + detail) and auditRevView (all changes of one revision)
  view_audit_test.go     NEW
  golden_test.go         + 4 cases
  testdata/audit-*.golden NEW
docs/plans/2026-09-26-cockpit-audit-r1.md   this plan (last step)
```

## 3. Data structures

### 3.1 `relevo.ChangeLine` (new)

| field | type | meaning |
|---|---|---|
| `Op` | string | `+` add, `-` remove, `~` change, `*` set (secrets) |
| `Subject` | string | what changed, in human words (§4.2) |
| `Field` | string | the rest of the path under the subject, `""` when none |
| `Before` | string | display text of the old value, `""` for `+` and `*` |
| `After` | string | display text of the new value, `""` for `-` and `*` |

### 3.2 `relevo.RollbackRefused` (new error type)

`struct{ Rev int64; Reason string }`, with `Error()` = `"can't roll back to #<rev>: <reason>"`.

### 3.3 Fake data for the UI (`fakeActions`, `actions_test.go`)

| field | type |
|---|---|
| `revs` | `[]db.RevisionRow` |
| `changes` | `map[int64][]relevo.ChangeLine` |
| `preview` | `[]relevo.ChangeLine` |
| `previewErr` | `error` |
| `rollbacks` | `[]int64` (recorder) |

## 4. Interfaces and contracts

### 4.1 `internal/config/revision.go`

```
func (s *Store) RevisionDoc(rev int64) (Doc, error)  // decodeSnapshot(Revision(rev).Snapshot); ErrNoRevision passes through
func (s *Store) Current() (Doc, error)               // exported wrapper of currentDoc (line 283)
```

### 4.2 `internal/relevo/configaudit.go`

```
func DescribeChange(c config.Change, before, after config.Doc) ChangeLine
```
- Pure.
- `before` is the doc the change was made to; `after` is the doc it produced.
- A name is looked up in `after` for `~` and `+`, and in `before` for `-`. Decode the section's JSON to find it; a
  failed decode or a missing index uses the fallback.

**Subject and Field rules (closed list, first match wins):**

1. `candidates[i].<rest>`: Subject = the `name` of candidate `i`; Field = `<rest>`. The fallback Subject is
   `candidates[i]`.
2. `candidates[i]`: Subject = `candidate <name>`; Field = `""`.
3. `actors.<a>.<rest>`: Subject = `actor <a>`; Field = `<rest>`.
4. `actors.<a>`: Subject = `actor <a>`.
5. `agents.<a>[.<rest>]`: Subject = `agent <a>`, and Field as in 3 and 4.
6. `policy.<rest>`: Subject = `<rest>`, with a trailing `_ms` stripped from its last segment; Field = `""`.
7. anything else, including a bare section name such as `roles`, `actors`, `servers`, `hooks` or `prices`:
   Subject = the path; Field = `""`.

**Value text:**
- A JSON string is unquoted.
- A number under a rule-6 path that ended in `_ms` is rendered as `FormatDuration(n ms)` if that function exists on
  this branch (it arrives with the `:settings` round). Otherwise render the number with its `ms` unit, e.g.
  `900000ms`. Say which you did.
- `true` / `false` read `on` / `off`.
- An object is its top-level scalar members as `key value` pairs joined by `, `, in key order. A nested object or
  array member reads `key …`.
- An array is its scalar items joined by `, `.
- The whole value is cut to 60 runes with `…`, as `config.cutValue` does.

**Examples** (they are also tests):

| change | ChangeLine |
|---|---|
| `{path candidates[1].provider, change, "antigravity" → "agy-extra"}` | `{~, claude-sonnet-4-6, provider, antigravity, agy-extra}` |
| `{path actors.planner, add, {"agent":"architect"}}` | `{+, actor planner, "", "", agent architect}` |
| `{path roles, remove, {...}}` | `{-, roles, "", <cut object text>, ""}` |

```
func RevisionChanges(s *config.Store, rev int64) ([]ChangeLine, error)
```
1. `row := s.Revision(rev)`, then decode `row.Changes` into `[]config.Change`.
2. `after := s.RevisionDoc(rev)`.
3. `before := s.RevisionDoc(rev-1)` when `rev > 1`; otherwise an empty `config.Doc`.
4. Map each change through `DescribeChange`, in stored order.

```
func CheckDoc(doc config.Doc) error
```
1. Decode each section of `doc` exactly as `LoadConfigDoc` decodes a stored body (`configedit.go:58-111`). The result
   is candidates, actors, agents and policy; a missing section is empty.
2. For every candidate, run `checkProvider(c.Harness, c.Provider)`, the provider-lock rule extracted from
   `validateCandidateInput` (`configedit.go:~496-510`). `validateCandidateInput` now calls it, so its behaviour and
   messages do not change.
3. Run `dryRun(cands, acts, agents, pol)`.
4. Return the first error, or nil.

```
func RollbackPreview(s *config.Store, rev int64) ([]ChangeLine, error)
```
1. `changes := s.RollbackPlan(rev)`. `config.ErrNoChange` and `config.ErrNoRevision` pass through unwrapped.
2. `snap := s.RevisionDoc(rev)`; `cur := s.Current()`.
3. If `CheckDoc(snap)` returns an error `e`, return `&RollbackRefused{Rev: rev, Reason: <e's text, first line>}`.
4. Otherwise return `DescribeChange(c, cur, snap)` for each change.

### 4.3 `ui.Actions` additions (`actions.go:24-54`, after `ApplyConfig`)

```
ConfigLog() ([]db.RevisionRow, error)                 // every revision, newest first (rt.Config.Log(0)); no snapshots
ConfigChanges(rev int64) ([]relevo.ChangeLine, error) // relevo.RevisionChanges(rt.Config, rev)
RollbackPreview(rev int64) ([]relevo.ChangeLine, error)
Rollback(ctx context.Context, rev int64) Result       // rt.Config.As("rollback", "").Rollback(rev), then relevo.ReloadConfig
```

- **`plannerActions`:** each method returns `errors.New("no config store")` when `a.rt.Config == nil`, as `ConfigDoc`
  does.
- **`Rollback`'s Result:**
  - success: `Text: "rolled back to #<rev> as #<new>"`, `Refresh: true`;
  - `ErrNoChange`: `Text: "config already equals #<rev>"`;
  - a failed reload after a successful write: the same wording `ApplyConfig` uses.

### 4.4 `internal/ui/view_audit.go`: `auditView`

Model it on `candidatesView` for loading, cursor, follow, body windowing and keys, and on the `:rounds` day rules for
grouping.

- **Fields:** `revs []db.RevisionRow, err error, loaded bool, cur, top int, actions bool,
  changes map[int64][]relevo.ChangeLine, changesErr map[int64]error`.
- **Loading:** `ConfigLog()` on init and on every `statusMsg`, through its own `auditLogMsg`.
- **Change loading:** after a load, and whenever `cur` moves to a revision not in `changes`, issue
  `ConfigChanges(rev)` as an `auditChangesMsg{rev, lines, err}`, then cache it.
- **Rows:**
  - `cur` indexes revisions, never a rule.
  - Group by local day with `dash.DayLabel(row.At, env.now(), time.Local)`. Use the same `now` the rounds view uses,
    so goldens pin it.
  - Each group gets a day rule: label dim, `┈` fill in the grid style, ` <n> revision(s) ` faint. Reuse or mirror
    `dash`'s `dayRuleLine` (`internal/ui/dash/render.go:309-325`), and say which.
- **Columns:**
  - `WHEN` (5, local `15:04`, dim);
  - `REV` (4, `#N`, right-aligned, dim, bold on the cursor);
  - `SOURCE` (9, dim);
  - `MESSAGE` (the rest, text colour);
  - `CHANGES` (7, right-aligned: the length of the `Changes` JSON array, faint when 0).
  - Two spaces sit between columns, with a 3-cell margin on each side, and nothing passes `width-3`.
  - The cursor is a full-width `selBandStyle` band, as in `candLine`.
- **`Crumbs`:** `["audit"]`.
- **`Context`:** left `"<n> revisions   <m> changes"` (bold numbers; `m` is the sum of CHANGES); right
  `"config version <newest.Version>"` (faint).
- **Detail block** (two blank lines under the table, then):
  - line 1: `#N` (faint bold), three spaces, `<source> · <Jan 2 15:04 local> · <message>` (dim);
  - then one line per ChangeLine (§5.2), up to the lines left above the footer. When more remain, the last line is
    `+ <k> more · enter shows all` (faint).
  - While changes load: `loading…`. On an error, the error text in the error style.
- **Keys:**
  - `up`, `down`, `home`, `end`, `pgup`, `pgdown`;
  - `enter`: push `auditRevView` for the cursor revision;
  - `R` (actions only): §4.6.
  - `Keys()`: `enter all changes`, `R roll back to here`, `: command`, `? all keys`, `q quit`. `HelpKeys()` adds
    `↑↓ move`.

### 4.5 `auditRevView` (pushed by `enter`)

- **`Crumbs`:** `["audit", "#N"]`.
- **`Context`:** `<source> · <Jan 2 15:04> · <message>`.
- **Body:** every ChangeLine of the revision (§5.2), scrollable with `up`, `down`, `pgup`, `pgdown`, `home`, `end`.
- **Keys:** `esc back`, plus `R roll back to here` when actions are available. Implement `R` in both views with the
  same helper.
- **Revision 1 (baseline, no changes):** the body is one dim line, `the config as it was when revisions began`.

### 4.6 `R`: roll back to the cursor revision

```
lines, err := env.Actions.RollbackPreview(rev)   (run as a tea.Cmd; the reply opens the overlay)
switch:
  errors.Is(err, config.ErrNoChange) -> notice("config already equals #<rev>")
  errors.As(err, *relevo.RollbackRefused) -> notice(err.Error())
  err != nil -> notice(err.Error())
  else -> openOverlay(confirmBox{danger: true, title: "roll back", lines: <below>, onYes: runAction(Rollback(rev))})
```

**The confirm lines** (see `AuditRollbackD2`):
- `Roll back to #<rev>?`, bold with `#<rev>` in the accent colour;
- a blank line;
- the first change line labelled `undoes     `, and every further one indented under it, rendered per §5.2;
- a blank line;
- `saves as   #<newest+1> · rollback`;
- `kept       #<newest> and every revision before it`.

The danger confirm keys come from `confirmBox` itself: `y roll back`, `n cancel`, "any other key cancels".

## 5. Pseudocode and layout

### 5.1 Update

```
auditLogMsg     -> store revs/err, loaded, clamp cur, return loadChanges(cur rev) if not cached
auditChangesMsg -> cache lines/err for rev
statusMsg       -> reload the log
KeyMsg          -> movement (clamp, follow, loadChanges if needed); enter; R (§4.6)
```

### 5.2 Rendering one ChangeLine

Everything is inside the 3-cell margins, and the text is cut with `…` to the width.

| Op | line |
|---|---|
| `~` | `~ ` (accent), Subject (text), ` Field` (dim, when not empty), 3 spaces, Before (dim), ` → ` (faint), After (text) |
| `+` | `+ ` (green), Subject, ` Field`, 3 spaces, After (dim) |
| `-` | `- ` (red), Subject, ` Field`, 3 spaces, Before (dim) |
| `*` | `* ` (dim), Subject |

### 5.3 The approved boards, as text (132 wide)

`AuditD2`, with the cursor on #4. The day labels come from `dash.DayLabel`, so the build prints `thu 24 sep` where the
board says `Sep 24`:

```
  ◆ relevo    audit                                                                                               v0.14.0    02:40

   5 revisions   11 changes                                                                                    config version 16

   WHEN    REV  SOURCE     MESSAGE                                                                                        CHANGES
   today ┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈ 1 revision
   01:36    #5  ui         add actor planner                                                                                    1
   yesterday ┈┈┈┈ … ┈ 1 revision
 ▌ 09:59    #4  cli        config set candidates                                                                                1   <- band
   Sep 24 ┈┈┈┈ … ┈ 3 revisions
   23:18    #3  migration  roles → actors                                                                                       2
   21:52    #2  migration  candidate names derived                                                                              7
   21:52    #1  baseline   config before revisions                                                                              0


   #4   cli · Sep 25 09:59 · config set candidates
   ~ claude-sonnet-4-6 provider   antigravity → agy-extra
```

`AuditRollbackD2`, `R` on #4, a red modal over the shaded list:

```
╭─ roll back ──────────────────────────────────────────────╮
│                                                          │
│   Roll back to #4?                                       │
│                                                          │
│   undoes     - actor planner   agent architect           │
│                                                          │
│   saves as   #6 · rollback                               │
│   kept       #5 and every revision before it             │
│                                                          │
│    y  roll back       n  cancel      any other key cancels │
╰──────────────────────────────────────────────────────────╯
```

## 6. Error handling

- **`ConfigLog` error:** the view shows it centred, as candidates does.
- **A pre-v6 database:** it returns nil rows, and the view's empty state says `no config revisions yet`.
- **`ConfigChanges` error:** shown in the detail block (§4.4); nothing else fails.
- **`RollbackRefused`, `ErrNoChange`, `ErrNoRevision`:** notices (§4.6).
- **`Rollback` write error:** comes back as `Result.Err`.

## 7. Ordered steps

### 0. Working efficiently (read before step 1)

**How to work:**
- In **one** parallel batch, read:
  - `internal/config/revision.go` and `internal/config/diff.go` (whole);
  - `internal/relevo/configedit.go:1-120,480-600`;
  - `internal/ui/view_candidates.go` (whole), `internal/ui/confirm.go`;
  - `internal/ui/actions.go:1-60,425-470`, `internal/ui/actions_test.go:30-190`;
  - `internal/ui/cmdline.go:20-35`, `internal/ui/view_rounds.go:200-300`;
  - `internal/ui/dash/render.go:300-330,830-870`;
  - `internal/ui/golden_test.go:480-520,640-760,990-1120`;
  - `internal/config/revision_test.go:1-60,270-370`.
- Do not search for them again.
- Make every change to a file in one edit.

**Commands:**
- First run `mkdir -p $HOME/.cache/relevo-verify/tmp`.
- Focused loop: `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/config/ ./internal/relevo/ ./internal/ui/ -run 'Audit|Rollback|DescribeChange|CheckDoc|Revision|Golden' -count=1`
- Goldens: `... go test ./internal/ui/ -run TestGoldenViews -update -count=1`, then read each new
  `testdata/audit-*.golden` against §5.3.
- Final checks, once:
  - `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/config/ ./internal/relevo/ ./internal/ui/ -count=1`
  - `go vet ./internal/...`
  - `test -z "$(gofmt -l $(git ls-files '*.go'))"`
  - `sh scripts/check-name.sh`
- `make check` is refused by a hook on this machine, so do not run it.
- No test goes in `cmd/relevo`: CI runners have no harness binary.

### 1. `config`: `RevisionDoc` and `Current` (§4.1)

- **Test** in `revision_test.go`: `RevisionDoc` of a known revision equals its written doc, and a missing revision
  returns `ErrNoRevision`.

### 2. `relevo`: `checkProvider` extraction (§4.2 CheckDoc step 2)

- **Verify:** the existing candidate-form tests pass unchanged.

### 3. `relevo/configaudit.go` (§3.1, §3.2, §4.2)

### 4. `relevo/configaudit_test.go`

- **a. `DescribeChange`:** the three examples of §4.2, plus rules 5, 6 and 7. The rule 6 case is
  `policy.stall_after_ms` changing from 900000 to 1200000.
- **b. `RevisionChanges`** on a real store (built as in `configedit_test.go:453-493`):
  - write candidates (revision a);
  - change candidate 1's provider (revision b);
  - `RevisionChanges(b)` gives `{~, <candidate 1's name>, provider, <old>, <new>}`.
- **c. `RollbackPreview` refused:**
  - revision a stores an agy candidate with provider `antigravity`, written raw through the store, bypassing the form;
  - revision b fixes it to `agy-extra`;
  - `RollbackPreview(a)` returns `*RollbackRefused`, and its reason names agy's providers.
- **d. `RollbackPreview` on the newest revision** returns `config.ErrNoChange`.
- **e. `CheckDoc`** passes on a valid doc and fails on an actor naming an unknown candidate.

### 5. `ui.Actions` + `plannerActions` + `fakeActions` (§4.3, §3.3)

### 6. `auditView`, `auditRevView`, `R` (§4.4-§4.6, §5)

### 7. Register `:audit` (`cmdline.go`, `view_rounds.go`)

### 8. UI tests and goldens

**Goldens** (the fixture copies this machine's five revisions: the rows of §5.3, with `At` times and changes JSON
counts 1, 1, 2, 7, 0; `changes[4]` = the claude-sonnet-4-6 provider line; `changes[5]` = `+ actor planner`; the `now`
the rounds goldens use, so "today" and "yesterday" land as in §5.3 or report how they land):
- `audit-132`, with the cursor on #4;
- `audit-100`;
- `audit-rollback-132`: `R` on #4, with `preview = [{-, actor planner, "", agent architect, ""}]`;
- `audit-rev-132`: `enter` on #2, with seven `+ <name> name` lines.

**Tests:**
- **a.** `R` then `y` records `rollbacks == [4]`.
- **b.** `R` with `previewErr = &relevo.RollbackRefused{…}` shows a notice, opens no overlay and records no rollback.
- **c.** `R` with `previewErr = config.ErrNoChange` shows the notice `config already equals #4`.
- **d.** `down` from #5 lands on #4, never on a rule.
- **e.** Moving the cursor requests `ConfigChanges` once per revision, and the cache holds it.

**Required mutations.** Run each, confirm the named test fails, revert, and report the build line.

- **M1:** `CheckDoc` skips `checkProvider` → 4c fails.
- **M2:** `DescribeChange` rule 1 uses the fallback → 4a fails.
- **M3:** `RollbackPreview` skips `CheckDoc` → 4c fails.
- **M4:** `R` skips the confirm and rolls back at once → 8a or the rollback golden fails.

### 9. Checks, the plan, the commit

1. Run the final checks listed in step 0.
2. Copy this plan to `docs/plans/2026-09-26-cockpit-audit-r1.md`.
3. `git add -A`, then make one commit:

   ```
   feat(cockpit): :audit -- config revisions by day, their changes by name, a checked roll back
   ```

## 8. Deletions

None. `validateCandidateInput` keeps its behaviour: step 2 only extracts a helper. No existing test is changed or
deleted, with one exception: a golden that lists every `:` command gains the `audit` line. Update it and report it.

## 9. Stop rather than improvise

Halt and report if any of these happens:
- a named location is not where this plan says;
- the provider lock is not a separable rule in `validateCandidateInput`;
- `confirmBox` cannot carry the rendered change lines without a change to `confirm.go` or `renderModal`;
- an existing golden other than the command list changes.

A halt that finds a planning error is worth more than a green suite bent to fit.
