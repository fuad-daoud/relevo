# Cockpit `:settings`, round 1: the list, group forms, reset (2026-09-26)

## 1. System overview

The cockpit's C1 config views (`:candidates`, `:actors`, `:agents`, PR #510) edit the config through `Actions`.
This round adds `:settings`, which covers the **policy** section.

What the round delivers:
- **The list:** 17 rows in 6 groups.
- **Group forms:** one form per group of plain settings (rounds, check, timing, max_builders).
- **Reset:** `r` puts a setting back to its default.

Every save is one config revision with source `ui`, through the existing `ApplyConfig` write path.

Composite rows (`scope`, `serve.scope`, `scan_patterns`, `classify`, `notify.webhooks`) are listed and explained in
this round. Their editors come in round 2. On those rows, `enter` answers with a notice.

The approved design is the canvas boards `SettingsD2` and `SettingsCheckD2`. Their text is copied in §5.4.

## 2. File structure

```
internal/relevo/
  configedit.go         ConfigDoc gains PolicyRaw; LoadConfigDoc fills it
  configpolicy.go       NEW: Setting, Settings, PolicySet, EditPolicy, ResetSetting, SettingPaths, FormatDuration
  configpolicy_test.go  NEW
internal/ui/
  cmdline.go            + {"settings", "", "limits, checks and timings", false} after "agents"
  view_rounds.go        execLine: + case "settings" (same shape as "agents", lines ~279-284)
  view_settings.go      NEW: settingsView (list, cursor, detail block, keys, reset confirm)
  settings_form.go      NEW: settingsForm (chips and text fields in one modal)
  view_settings_test.go NEW
  golden_test.go        + 4 cases (§7 step 6)
  testdata/settings-*.golden  NEW (generated)
docs/plans/2026-09-26-cockpit-settings-r1.md   this plan (last step)
```

## 3. Data structures

### 3.1 `relevo.ConfigDoc` (`internal/relevo/configedit.go:21-26`)

Add the field:

```
PolicyRaw json.RawMessage // the stored policy body, verbatim; nil when the section is absent
```

`LoadConfigDoc` (lines 58-111) sets `d.PolicyRaw` to a copy of the body in the existing
`s.Body(config.Policy)` branch. Nothing else changes.

### 3.2 `relevo.Setting` (new, `configpolicy.go`)

| field | type | meaning |
|---|---|---|
| `Key` | string | the name the list shows, e.g. `max_switches`, `gate.timeout`, `serve.scope` |
| `Group` | string | `rounds`, `check`, `timing`, `processes`, `scan` or `notify` |
| `Value` | string | the effective value as display text (§3.4) |
| `Default` | string | the default as display text (§3.4) |
| `Set` | bool | the key is stored in the policy section (§3.3) |
| `Form` | string | which editor `enter` opens: `rounds`, `check`, `timing`, `max_builders`; or `scope`, `serve.scope`, `scan_patterns`, `classify`, `webhooks` (round 2) |

### 3.3 The 17 rows, in this order (closed list)

| # | Group | Key | JSON path(s) cleared by reset | Set when | Form |
|---|---|---|---|---|---|
| 1 | rounds | `max_switches` | `max_switches` | `MaxSwitches != nil` | rounds |
| 2 | rounds | `max_tier` | `max_tier` | `MaxTier != ""` | rounds |
| 3 | rounds | `verify.default` | `verify` | `Verify != nil` | rounds |
| 4 | check | `gate.default` | `gate.default` | `Gate != nil && Gate.Default != ""` | check |
| 5 | check | `gate.timeout` | `gate.timeout_ms` | `Gate != nil && Gate.TimeoutMS != nil` | check |
| 6 | check | `gate.regate` | `gate.regate` | `Gate != nil && Gate.Regate != nil` | check |
| 7 | timing | `limit_gate_default` | `limit_gate_default_ms` | pointer non-nil | timing |
| 8 | timing | `stall_after` | `stall_after_ms` | pointer non-nil | timing |
| 9 | timing | `progress_interval` | `progress_interval_ms` | pointer non-nil | timing |
| 10 | timing | `explore_after` | `explore_after_ms` | pointer non-nil | timing |
| 11 | timing | `stale_after` | `stale_after_ms` | pointer non-nil | timing |
| 12 | processes | `scope` | `scope` | `Scope != nil` | scope |
| 13 | processes | `serve.max_builders` | `serve.max_builders` | `Serve != nil && Serve.MaxBuilders != nil` | max_builders |
| 14 | processes | `serve.scope` | `serve.scope` | `Serve != nil && Serve.Scope != nil` | serve.scope |
| 15 | scan | `scan_patterns` | `scan_patterns` | `len(ScanPatterns) > 0` | scan_patterns |
| 16 | scan | `classify` | `classify` | `Classify != nil` | classify |
| 17 | notify | `notify.webhooks` | `notify` | `Notify != nil && len(Notify.Webhooks) > 0` | webhooks |

`policy.order` and `policy.tier` are **not** listed: `:actors` owns the order and the tiers now.

### 3.4 Display text

| row(s) | Value (effective) | Default |
|---|---|---|
| `max_switches`, `gate.regate`, `serve.max_builders` | decimal | `2`, `0`, `max(1, cpus-1)` |
| `max_tier` | tier name | `edit` |
| `verify.default` | `on` / `off` | `off` |
| `gate.default` | the command, or `none` | `none` |
| durations | `FormatDuration(accessor())` | `FormatDuration(the Default* constant)` |
| `scope`, `serve.scope` | see below | `on · no limits`, `as scope` |
| `scan_patterns` | `none`, or `N patterns` / `1 pattern` | `none` |
| `classify` | `off`, or the provider name | `off` |
| `notify.webhooks` | `none`, or `N webhooks` / `1 webhook` | `none` |

- **Duration defaults:** `limit_gate_default` 1h, `stall_after` 15m, `progress_interval` 30s, `explore_after` 20m,
  `stale_after` 4h, `gate.timeout` 10m.
- **The scope summary:**
  - nil gives the Default text;
  - otherwise `off` when `Enabled` is false;
  - otherwise the set fields as `key value` pairs joined by ` · `, in struct order (`slice relevo.slice · cpu_quota 200%`);
  - a block with no fields set reads `on · no limits`.
- **`cpus`** is a parameter, so tests are deterministic (§4.1).
- **`FormatDuration(d)`:** Go's `d.String()` with trailing zero units removed. `1h0m0s` → `1h`, `15m0s` → `15m`,
  `1h30m0s` → `1h30m`, `30s` → `30s`.

### 3.5 `relevo.PolicySet` (new)

```
type PolicySet struct {
    Path  string // dotted JSON path inside the policy section, e.g. "gate.timeout_ms", "serve.max_builders"
    Value any    // the JSON value to store; nil deletes the key
}
```

## 4. Interfaces and contracts

### 4.1 `internal/relevo/configpolicy.go`

```
func Settings(d ConfigDoc, cpus int) []Setting
```
- **Post:** exactly the 17 rows of §3.3, in order, filled from `d.Policy` (the parsed struct). Pure.

```
func SettingPaths(key string) []string
```
- The reset paths of §3.3 for a key, or nil for an unknown key. Pure.

```
func EditPolicy(d ConfigDoc, sets []PolicySet, message string) (ConfigEdit, error)
```
1. Decode `d.PolicyRaw` into `map[string]any` with `json.Decoder.UseNumber()`. nil or empty decodes as an empty map.
2. Apply each set in order:
   - A non-nil `Value` creates intermediate objects as needed and stores the value at the leaf.
   - A nil `Value` deletes the leaf, then removes every parent object that is left empty, walking upward.
   - A path whose intermediate is not an object returns `&FieldError{"", "<path>: not an object"}`.
3. Compare the result with the original by the compact encoding of both. Equal returns `ErrNoChange`.
4. Encode with `json.MarshalIndent(m, "", "  ")` plus `'\n'`, the same layout as `encodeCandidates` (line 586). An
   empty map encodes as `{}\n`.
5. Validate:
   - `policy.Parse(config.FileName(config.Policy), body)`. An error returns `&FieldError{"", msg}`, where `msg` is the
     error text with its leading `"<file>: "` removed and its trailing `": bad policy"`-style wrap removed. Report the
     exact trimming you did.
   - `dryRun(d.Candidates, d.Actors, d.Agents, parsed)` (line 538). Its error is returned as is.
6. **Post:** `ConfigEdit{Sections: {config.Policy: body}, Message: message}`. Unknown keys in `PolicyRaw` survive
   untouched.

```
func ResetSetting(d ConfigDoc, key string) (ConfigEdit, error)
```
- `ErrNoChange` when the row is not `Set`.
- Otherwise `EditPolicy` with one nil `PolicySet` per `SettingPaths(key)` and the message `"reset " + key`.
- An unknown key returns `&FieldError{"", "unknown setting " + key}`.

```
func FormatDuration(d time.Duration) string   // §3.4
```

### 4.2 `internal/ui/view_settings.go`: `settingsView`

Model it on `candidatesView` (`view_candidates.go:28-681`).

- **Fields:** `doc relevo.ConfigDoc, err error, loaded bool, cur, top int, actions bool`.
- **Constructor:** `newSettingsView(env Env) (settingsView, tea.Cmd)`. It loads through `env.Actions.ConfigDoc()` as
  `candDocCmd` does, with its own message type `settingsDocMsg`, and reloads on every `statusMsg`.
- **`cur` indexes the 17 settings**, never a group rule. The body line of setting `i` is
  `2 + i + (the number of group rules up to and including its group)`. `follow` uses that line.
- **Keys:**
  - `up`, `down`, `home`, `end`, `pgup`, `pgdown`, clamped;
  - `enter`;
  - `r`.
  - Keys other than movement are active only when `actions` is set, as in `candidatesView`.
- **`Crumbs`:** `["settings"]`.
- **`Context`:** left `"<17> settings   <n> set"`, with the numbers bold, as candidates renders its counts. The right side is
  empty in this round; round 2 adds the revision.
- **`Keys()`:** `enter edit`, `r reset`, `: command`, `? all keys`, `q quit`. `HelpKeys()` adds `↑↓ move`, as
  candidates does. There is **no `/` filter** this round.
- **`Body`:** the lines of §5.4. It is windowed by `top`, with the same error, loading and empty handling as
  `candidatesView.Body`.
- **`numCPU`:** a package var `var numCPU = runtime.NumCPU`. The view passes `numCPU()` to `relevo.Settings`. The golden
  test sets it to `func() int { return 22 }` and restores it.

### 4.3 `internal/ui/settings_form.go`: `settingsForm` (an `overlay` + `modalOverlay` + `overlayKeyer`)

Model it on `actorForm` (`actor_form.go:52-254`) for the chip rows and on `candidateForm` (`candidate_form.go`) for the
text inputs, live validation, `touched`/`tried`, and the enter chip that turns faint while the form is invalid.

- **Field:**
  ```
  type settingField struct {
      label  string            // left column, e.g. "command", "max_switches"
      path   string            // the PolicySet path this field writes
      chips  []string          // non-nil: a chip row; nil: a text input
      sel, origSel int         // chip rows
      input  textinput.Model   // text rows; prefilled with the stored value, "" when unset
      orig   string            // the prefill, to detect a change
      hint   string            // "default 10m": shown right of the input, faint
      parse  func(string) (any, error) // text rows: "" -> (nil, nil) means delete; else the JSON value or an error
      value  func(sel int) any         // chip rows: the JSON value for a selection
  }
  ```
- **Form:** `settingsForm{title string, fields []settingField, focus int, touched []bool, tried bool, doc relevo.ConfigDoc,
  env Env, note string /* one dim sentence row, may be "" */}`.
- **Constructor:** `newSettingsForm(env Env, doc relevo.ConfigDoc, form string, focusKey string)`. `focusKey` focuses the
  field of the row `enter` was pressed on.
- **Keys:**
  - `tab` / `shift+tab` move the focus;
  - `left` / `right` move a chip selection;
  - typing edits a text field;
  - `enter` submits;
  - `esc` cancels.
- **Live validation:** every render parses each text field. A parse error shows under its field (`formError`) once the
  field is touched or `tried`. Then `EditPolicy` runs on the collected sets, and its `FieldError` shows as one form-level
  error row above the key row. The enter chip is faint while any error stands.
- **`sets()`:** one `PolicySet` per changed field. A text field is changed when `input.Value() != orig`, and a chip row
  when `sel != origSel`.
- **Submit:**
  - no sets → `notice("nothing changed")`, then close;
  - `EditPolicy` returning `ErrNoChange` → the same;
  - a `FieldError` → set `tried` and stay open;
  - otherwise `runAction(... ApplyConfig)` exactly as `candidateForm.submit` does (lines 344-373), then close.
- **The message** is `"set " + ", ".join(<key> <new display value>)`, e.g. `set gate.default make check, gate.timeout 20m`.
  A cleared text field reads `<key> default`.
- **`modal()` returns the title `form`** (`rounds`, `check`, `timing` or `serve.max_builders`). Rows:
  - a blank row;
  - per field: label (17 cells), then the input or chips, then the faint hint;
  - a blank row between chip groups;
  - the `note` row;
  - the key row (`enter save`, `tab next field`, `esc cancel`, plus `←→ choose` when the form has a chip row).
  - Draw it the way `SettingsCheckD2` (§5.4) shows.

**The four forms (closed list):**

| form | fields: label → path, kind, parse |
|---|---|
| `rounds` | `max_switches` → `max_switches`, text, a non-negative int. `max_tier` → `max_tier`, chips `read edit yolo`, the value is the string. `verify.default` → `verify.default`, chips `on off`, the value is a bool |
| `check` | `command` → `gate.default`, text, trimmed; "" deletes. `timeout` → `gate.timeout_ms`, text, a duration (§4.4). `repair rounds` → `gate.regate`, text, a non-negative int. note: the check sentence of §5.3 |
| `timing` | `limit_gate_default` → `limit_gate_default_ms`, `stall_after` → `stall_after_ms`, `progress_interval` → `progress_interval_ms`, `explore_after` → `explore_after_ms`, `stale_after` → `stale_after_ms`; all text durations |
| `max_builders` | `max_builders` → `serve.max_builders`, text, an int ≥ 1 |

- **Chip rows start** on the effective value: `max_tier` on `MaxTierOrDefault()`, `verify.default` on `VerifyDefault()`.
- **Every text hint** is `"default " + Setting.Default`.

### 4.4 Parsers (in `settings_form.go`)

- **int:** `strconv.Atoi` on the trimmed text.
  - The rounds form refuses a negative value with `"a whole number, 0 or more"`.
  - `max_builders` refuses a value below 1 with `"a whole number, 1 or more"`.
- **duration:**
  - `time.ParseDuration` on the trimmed text. It must be > 0, or the field says `"a duration like 15m or 1h30m"`.
  - The stored value is `d.Milliseconds()` as an int.
  - The prefill of a stored `*_ms` value is `relevo.FormatDuration(ms * time.Millisecond)`.
- **An empty text field** parses to `(nil, nil)`, which deletes the key, so the setting returns to its default.

### 4.5 Reset (`r` in `settingsView`)

- **On a row that is not `Set`:** `notice(key + " is already the default")`.
- **Otherwise:**
  - Open `confirmBox` (`confirm.go:51-117`) with **danger false**, title `reset`, and the lines
    `Reset <key> to its default?` and `<value> → <default>`.
  - `y` runs `ResetSetting`, then `ApplyConfig` through `runAction`.
  - An `ErrNoChange` from `ResetSetting` becomes the same notice.

### 4.6 `enter` on a composite row (rows 12, 14-17)

`notice(key + " opens its own editor in the next round")`. Round 2 replaces this.

## 5. Pseudocode and layout

### 5.1 `settingsView.Update`

```
settingsDocMsg -> store doc/err, loaded = true, clamp cur
statusMsg      -> return settingsDocCmd(env)            (reload, as candidates does)
KeyMsg         -> movement: clamp, follow
                  enter (actions): row := Settings(doc, numCPU())[cur]
                        if row.Form in {rounds, check, timing, max_builders}: openOverlay(newSettingsForm(env, doc, row.Form, row.Key))
                        else notice (§4.6)
                  r (actions): §4.5
```

### 5.2 Detail block (the two blank lines below the table, then these lines)

- **Line 1:** the key in faint bold, three spaces, then the value, dim.
- **Then** the row's sentence(s) from `settingHelp[key]`, wrapped to the body width minus 6 and dim. Sentences are
  plain prose with no captions. Use exactly these texts:

| key | sentence |
|---|---|
| `max_switches` | How many times relevo may move one round to the next candidate after a rate limit or a failed start before it asks you. 0 means it asks at once. |
| `max_tier` | The highest permission tier any actor may run at without --allow-yolo. |
| `verify.default` | When on, a plain relevo send marks the round for a read-only reviewer when it closes. |
| `gate.default` | The command relevo runs in a binding's tree when a writer round reports done; a failure opens a repair round while gate.regate allows one. |
| `gate.timeout` | How long that command may run before it counts as failed. |
| `gate.regate` | How many repair rounds relevo may open after a failed check before it asks you. |
| `limit_gate_default` | How long a rate limit closes a provider when its message names no reset time. |
| `stall_after` | A running builder whose stream is quiet this long is shown as stalled. |
| `progress_interval` | How often relevo samples a running round's tree and stream. |
| `explore_after` | A builder whose output moves while its tree does not, for this long, is shown as exploring. |
| `stale_after` | A binding left in needs-you or held this long is shown as stale. |
| `scope` | The systemd scope relevo starts each round's builder in on this machine: CPU, memory and task limits. |
| `serve.max_builders` | How many builders relevo serve runs at once; more rounds wait in its queue. |
| `serve.scope` | The scope for rounds relevo serve runs; when set it replaces scope for them. |
| `scan_patterns` | Extra patterns that mark a builder's line as instruction-shaped, beside the built-in list. |
| `classify` | An optional classifier that scores a builder's lines for prompt injection, beside the pattern scan. |
| `notify.webhooks` | Where relevo posts round and binding events over HTTP. |

### 5.3 The check sentence

It follows `gate.default`'s sentence in the detail block, and it is the check form's note. Compute it from the doc. A
writer actor has check on when `actorCheckOn(a.Check)` holds and its agent is a writer (`agentShapeOf`, as in
`actorCheckLine`, `view_actor.go:105-117`). `<names>` is those actors, sorted and joined with `, `.

| case | sentence |
|---|---|
| no actor has check on | `no actor has check on, so nothing runs it` |
| on, and `gate.default` is empty | `<names> has check on, so with none set its rounds run nothing.` (`have` and `their` for several) |
| on, and `gate.default` is set | `<names> runs it after each round.` (`run` for several) |

### 5.4 The approved boards, as text (132 wide; faint, dim and bold as in the C1 views)

`SettingsD2`, with the cursor on `gate.default`:

```
  ◆ relevo    settings                                                                                            v0.14.0    02:10
                                                                                        
   17 settings   3 set

   SETTING                                                                         VALUE                   DEFAULT
   rounds ┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈ 3 settings · 2 set
   max_switches                                                                    2                       2
   max_tier                                                                        yolo                    edit
   verify.default                                                                  off                     off
   check ┈┈┈┈┈┈ … ┈┈┈ 3 settings
 ▌ gate.default                                                                    none                    none       <- cursor band
   gate.timeout                                                                    10m                     10m
   …  (timing, processes, scan, notify groups the same way; a group with one row says "1 setting")


   gate.default   none
   The command relevo runs in a binding's tree when a writer round reports done; a failure opens a repair round
   while gate.regate allows one. builder has check on, so with none set its rounds run nothing.
```

**Layout rules:**
- **Margins:** a 3-cell margin on each side. The table fills `[3, width-3)`.
- **Columns:** VALUE and DEFAULT are 22 cells each, and SETTING takes the rest.
- **Header:** the header row is faint bold.
- **Group rules:** each is `   <group> ` (dim), then `┈` in `gridStyle` to fill, then
  ` <n> settings[ · <m> set] ` (faint). Look at how `:rounds` draws its day rules (`internal/ui/dash`, the day-rule
  line) and reuse that helper or its styles if one exists. Say which.
- **Rows:**
  - a set row's key and value are in the text colour;
  - an unset row's key and value are dim;
  - the DEFAULT column is always faint.
- **Cursor:** a full-width `selBandStyle` band, with the key bold, exactly like `candLine`.
- **At width 100:** SETTING shrinks. Nothing may pass `width-3`.

`SettingsCheckD2`, after `enter` on a check row (the modal over the shaded list):

```
╭─ check ───────────────────────────────────────╮
│                                               │
│   command         make check▏                 │
│                                               │
│   timeout         10m          default 10m    │
│                                               │
│   repair rounds   0            default 0      │
│                                               │
│   runs in the binding's tree after each round of an actor with check on: builder │
│                                               │
│    enter  save       tab  next field       esc  cancel │
╰─────────────────────────────────────────────╯
```
- In the build, the check form's note is the §5.3 sentence, not the board's wording.
- The text fields show the stored value, or stay empty with the default hint. The board shows typed values.

## 6. Error handling

- **Config load error:** the view shows it centred, as candidates does.
- **Validation:** from `EditPolicy`, shown live in the form, which never closes on an invalid edit.
- **Write errors:** come back as `Result.Err` through `runAction` and are shown the way the C1 views show them.
- **No new error types.**

## 7. Ordered steps

### 0. Working efficiently (read before step 1)

**How to work:**
- In **one** parallel batch, read:
  - `internal/relevo/configedit.go` (whole);
  - `internal/policy/policy.go:40-260` (the Policy type, the defaults and the accessors);
  - `internal/ui/view_candidates.go` (whole);
  - `internal/ui/actor_form.go`, `internal/ui/candidate_form.go`, `internal/ui/form_rows.go`, `internal/ui/confirm.go`;
  - `internal/ui/view_actor.go:95-125`, `internal/ui/cmdline.go:20-35`, `internal/ui/view_rounds.go:262-290`;
  - `internal/ui/golden_test.go:640-760` and `:990-1120`;
  - `internal/ui/view_candidates_test.go:1-110`, `internal/ui/actions_test.go:30-190`.
- Do not search for them again.
- Make every change to a file in one edit.

**Commands:**
- First run `mkdir -p $HOME/.cache/relevo-verify/tmp`. It is only a temp dir; on a machine where `/tmp` is roomy you
  may drop the `env TMPDIR=...` prefix.
- Focused loop: `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/relevo/ ./internal/ui/ -run 'Setting|Policy|Golden' -count=1`
- Goldens: `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -run TestGoldenViews -update -count=1`,
  then read each new `testdata/settings-*.golden` and compare it with §5.4.
- Final checks, once:
  - `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/relevo/ ./internal/ui/ ./internal/policy/ -count=1`
  - `go vet ./internal/...`
  - `test -z "$(gofmt -l $(git ls-files '*.go'))"`
  - `sh scripts/check-name.sh`
- `make check` is refused on the planner's machine and may be on yours. If it runs, run it too; otherwise do not.
- No test goes in `cmd/relevo`: CI runners have no harness binary.

### 1. `ConfigDoc.PolicyRaw` (§3.1)

- **Verify:** the existing `internal/relevo` tests pass, and `LoadConfigDoc` on a store with a policy body fills
  `PolicyRaw`. Test it in step 3.

### 2. `configpolicy.go` (§3.2-§3.5, §4.1)

### 3. `configpolicy_test.go`

Table tests:
- **a. `Settings`:** the real policy `{"max_switches":2,"max_tier":"yolo","serve":{"scope":{"slice":"relevo.slice"}}}`
  with `cpus = 22`.
  - Rows 1, 2 and 14 are `Set`.
  - `serve.scope` reads `slice relevo.slice`.
  - `serve.max_builders` reads `21` / `21`.
  - `max_tier` reads `yolo` / `edit`.
- **b. `Settings` on an empty policy:** no row is `Set`.
- **c. `EditPolicy`:**
  - it sets `gate.timeout_ms` and creates `gate`;
  - deleting `serve.scope.slice` from the real policy removes `serve` entirely (empty parents are pruned);
  - an unknown key `"future_knob": 1` survives an unrelated set;
  - it returns `ErrNoChange` when the value is unchanged;
  - `max_switches: -1` returns a `*FieldError`.
- **d. `ResetSetting`:** `max_tier` removes it, and `gate.default` when unset returns `ErrNoChange`.
- **e. `FormatDuration`:** `1h`, `15m`, `30s`, `1h30m`, `4h`.
- **f. `LoadConfigDoc`:** it fills `PolicyRaw`, using a real store the way `TestConfigEditWriteAndLoadRoundTrip` does
  (`configedit_test.go:453`).

### 4. `settingsView` and `settingsForm` (§4.2-§4.6, §5)

### 5. Register `:settings` (`cmdline.go`, `view_rounds.go`)

### 6. UI tests and goldens (`view_settings_test.go`, `golden_test.go`)

**Goldens** (fixture: `candFixtureDoc(t)` with `Policy` = the real policy of 3a, parsed, and `PolicyRaw` = its bytes;
`numCPU` = 22):
- `settings-132`: 132×34, with the cursor moved to `gate.default`;
- `settings-100`: 100×34;
- `settings-check-form-132`: `enter` on `gate.default`;
- `settings-reset-132`: `r` on `max_tier`.

**Tests**, using `fakeActions.configEdits`:
- **a.** The rounds form: move `max_tier` from `yolo` to `edit`, press enter. One edit is recorded, its policy body has
  `"max_tier": "edit"`, and its message is `set max_tier edit`.
- **b.** The check form: type `make check` into `command`, press enter. The body has `gate.default`.
- **c.** The timing form: type `15` into `stall_after`. The form shows `a duration like 15m or 1h30m`, and enter records
  no edit.
- **d.** `r` on `max_tier`, then `y`. One edit with the message `reset max_tier`, and the body no longer has `max_tier`.
- **e.** `r` on `gate.default` (unset). A notice, and no confirm.
- **f.** `down` from `verify.default` lands on `gate.default`, never on a rule. Assert the cursor's key.

**Required mutations.** Run each, confirm the named test fails, revert, and report the build line.

- **M1:** skip pruning empty parents → 3c fails.
- **M2:** `EditPolicy` never returns `ErrNoChange` → 3c fails.
- **M3:** `cur` counts rule lines (follow off by the rules) → 6f or a golden fails.
- **M4:** `settingsForm.sets()` includes unchanged chip rows → 6a fails, because its message gains `verify.default`.

### 7. Checks, the plan, the commit

1. Run the final checks listed in step 0.
2. Copy this plan to `docs/plans/2026-09-26-cockpit-settings-r1.md`.
3. `git add -A`, then make one commit:

   ```
   feat(cockpit): :settings -- every policy setting with its default, group forms, reset
   ```

## 8. Deletions

None. No existing test is changed or deleted. If an existing golden changes, halt and report it, with one exception: a
golden that lists every `:` command (the palette or help) gains the `settings` line. Update that one and report it.

## 9. Stop rather than improvise

Halt and report if any of these happens:
- a named location is not where this plan says;
- `policy.Parse` accepts the section in a form `EditPolicy`'s map round-trip cannot produce;
- a golden other than the palette changes;
- the form machinery needs a change to `formBox`, `confirmBox` or `renderModal` to fit this design.

A halt that finds a planning error is worth more than a green suite bent to fit.
