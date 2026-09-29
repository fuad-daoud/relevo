# Cockpit `:settings` › notify.webhooks: list, add, edit, delete (2026-09-26)

## 1. Overview

`:settings` is on `main`: `internal/ui/view_settings.go`, `settings_form.go`, `settings_form_defs.go` and
`internal/relevo/configpolicy.go`. Today `enter` on its last two rows answers
`notice(row.Key + " opens its own editor soon")` (`view_settings.go`, `updateKey`, `case "enter"`, ~lines 424-434).

This round:
1. **`notify.webhooks` gets an editor:**
   - `enter` pushes a list view `settings › notify.webhooks`;
   - `a` adds and `e`/`enter` edits, both through one form;
   - `d` deletes behind a red confirm.
   - Each save is one config revision with source `ui`, through `ApplyConfig`, as the other settings forms do.
2. **`scan_patterns` gets no editor.** Its notice becomes
   `scan_patterns is edited with relevo config set policy`, so the cockpit stops promising an editor.

The approved design is the canvas boards `WebhooksD2` and `WebhookAddD2` (§5).

## 2. File structure

```
internal/policy/                  export the known webhook event names (see §3.1) -- new file webhooks.go if policy.go
                                  cannot grow (check scripts/check-filesize.sh / .allow first)
internal/relevo/configwebhooks.go NEW: SetWebhooks
internal/relevo/configwebhooks_test.go NEW
internal/ui/view_webhooks.go      NEW: webhooksView (the pushed list)
internal/ui/webhook_form.go       NEW: webhookForm (add/edit)
internal/ui/view_settings.go      enter on notify.webhooks pushes the view; scan_patterns notice reworded
internal/ui/view_webhooks_test.go NEW
internal/ui/golden_test.go, testdata/settings-webhooks-*.golden  NEW goldens
docs/plans/2026-09-26-cockpit-settings-webhooks.md
```

Every file must stay ≤ 600 lines. `check-filesize.sh` enforces this, and no allow-list entry may be added.

## 3. Contracts

### 3.1 `policy`

The known event names are a local map inside `Parse` today (`knownEvents`, near the `notify.webhooks[%d].events` error,
~line 742). Export them as an ordered list and have `Parse` use it, with no behaviour change:

```
var WebhookEvents = []string{"state_changed", "round_started", "fork_created", "builder_stalled", "binding_stale"}
```

`state_changed:<state>` stays valid exactly as `Parse` accepts it now.

### 3.2 `relevo.SetWebhooks` (new, `configwebhooks.go`)

```
// SetWebhooks replaces the stored webhook list with hooks, validated like any policy edit.
func SetWebhooks(d ConfigDoc, hooks []policy.Webhook, message string) (ConfigEdit, error)
```
- **An empty list** → `EditPolicy(d, []PolicySet{{Path: "notify", Value: nil}}, message)`. The `notify` block goes, and
  the row returns to its default.
- **Otherwise** → `EditPolicy(d, []PolicySet{{Path: "notify.webhooks", Value: <hooks as []any>}}, message)`.
  - Convert each hook with a `json.Marshal` / `Unmarshal` round trip into `map[string]any`, so the omitempty rules of
    `policy.Webhook` hold.
  - A `json` format is stored as `""` (omitted); `slack` and `discord` are stored.
  - An empty `Events` is omitted, meaning every event.
- **Errors:** `EditPolicy`'s `ErrNoChange`, `*FieldError`, or `dryRun`'s error.

### 3.3 `webhooksView` (new, pushed by `enter` on the `notify.webhooks` row)

Model it on `actorView` (`view_actor.go`) for a pushed list, and on `candidatesView` for loading.

- **Fields:** `doc relevo.ConfigDoc, err error, loaded bool, cur, top int, actions bool`.
- **Loading:** it reloads the doc through `env.Actions.ConfigDoc()` on init and on every `statusMsg`, with its own msg
  type.
- **`Crumbs`:** `["notify.webhooks"]`. The settings view already contributes `settings`.
- **`Context`:** `"<n> webhooks"`, bold number, as the other views render counts (`1 webhook` for one).
- **Table:** a 3-cell margin on each side.
  - The columns are `URL` (the rest), `FORMAT` (8, `json` for `""`) and `EVENTS` (34: joined with `, `, or
    `every event` when empty, cut with `…`).
  - The header is faint bold, and the cursor row is a full-width `selBandStyle` band with the URL bold.
  - With no webhooks, one dim line: `none yet`.
- **Detail block**, one blank line under the table, for the cursor row:
  - the URL in faint bold;
  - then `format <f>` and `events <every event | the list>`, dim.
- **Keys:**
  - `a` add;
  - `e` and `enter` edit;
  - `d` delete;
  - `esc` back;
  - `? all keys`;
  - movement: `up`, `down`, `home`, `end`, `pgup`, `pgdown`.
  - Action keys are active only when `env.Actions` is set.
  - `e`, `enter` and `d` on an empty list do nothing.
- **Delete:** a `confirmBox` with `danger: true`, title `delete`, and the lines `Delete this webhook?` and `<url>`. `y`
  runs `SetWebhooks` without that index, message `delete webhook <url>`, through `runAction`/`ApplyConfig`.

### 3.4 `webhookForm` (new, an `overlay` + `modalOverlay` + `overlayKeyer`)

Model it on `settingsForm` (`settings_form.go`): chips, text inputs, live validation, and the enter chip that turns
faint while the form is invalid.

- **Title:** `add webhook` or `edit webhook`.
- **Fields:**

  | label | kind | parse |
  |---|---|---|
  | `url` | text, prefilled when editing | trimmed; must parse with `net/url`, scheme `http` or `https`, non-empty host, or `"an http or https URL"` |
  | `format` | chips `json slack discord` | starts on the hook's format, or `json` |
  | `events` | text, prefilled as the comma-joined list | split on commas and spaces, empties dropped; each name, or its part before `:`, must be in `policy.WebhookEvents`, and a `:<state>` suffix is allowed only on `state_changed`; else `"unknown event <name>"` |

  An empty `events` means every event, with the hint `every event when empty`.
- **Two dim reference rows** under the fields:
  - `events: ` followed by `policy.WebhookEvents` joined with two spaces;
  - `state_changed:needs_you narrows state_changed to one state`.
- **Submit:**
  - add appends a hook, and edit replaces the edited index;
  - it calls `SetWebhooks`, with message `add webhook <url>` or `edit webhook <url>`;
  - `ErrNoChange` → `notice("nothing changed")`, then close;
  - a `FieldError` stays open and shows `relevo.HumanPolicyError(err)` as the form-level error row.
- **Keys:** `enter save`, `tab next field`, `←→ choose`, `esc cancel`.

### 3.5 `view_settings.go` `enter`

- `row.Form == "webhooks"` → `push(newWebhooksView(env, v.doc))`, or the constructor shape the pushed view needs.
- `row.Form == "scan_patterns"` → `notice("scan_patterns is edited with relevo config set policy")`.

## 4. Steps

### 0. Working efficiently

**How to work:**
- In one batch, read:
  - `internal/ui/view_settings.go`, `internal/ui/settings_form.go`, `internal/ui/settings_form_defs.go`;
  - `internal/ui/view_actor.go`, `internal/ui/view_actors.go:400-440`, `internal/ui/confirm.go`,
    `internal/ui/form_rows.go`;
  - `internal/relevo/configpolicy.go`;
  - `internal/policy/policy.go:90-110` and the notify part of `Parse` (search `knownEvents`);
  - `internal/ui/golden_test.go` (search `settings-`), `internal/ui/view_settings_test.go`.
- Make each file's change in one edit.

**Commands:**
- First run `mkdir -p $HOME/.cache/go-tmp`, then `export TMPDIR=$HOME/.cache/go-tmp GOTMPDIR=$HOME/.cache/go-tmp`.
- Focused: `go test ./internal/policy/ ./internal/relevo/ ./internal/ui/ -run 'Webhook|Setting|Policy|Golden' -count=1`
- Goldens: `go test ./internal/ui/ -run TestGoldenViews -update -count=1`. Only the new `settings-webhooks-*` files may
  appear; no existing golden may change.
- Final:
  - `go test ./internal/policy/ ./internal/relevo/ ./internal/ui/ -count=1 -race`
  - `go vet ./internal/...`
  - `test -z "$(gofmt -l $(git ls-files '*.go'))"`
  - `sh scripts/check-comments.sh`
  - `sh scripts/check-filesize.sh`
- If `git ls-files` fails in this worktree, check the touched files by hand, and say so.
- Skip `make check`. No test in `cmd/relevo`.
- Comments say *why*, with no issue numbers, `§` or plan references. Test names say what they pin.

### 1. `policy.WebhookEvents` (§3.1). The existing policy tests must pass unchanged.

### 2. `SetWebhooks` (§3.2) and its tests

- **a.** On the real policy `{"max_switches":2,"max_tier":"yolo","serve":{"scope":{"slice":"relevo.slice"}}}`, adding
  one hook with a url and the slack format gives a body with
  `notify.webhooks[0] = {"url": …, "format": "slack"}` and keeps the other keys.
- **b.** A `json` format and empty events store only `url`.
- **c.** Setting the list back to empty removes the `notify` key.
- **d.** An `ftp://` url returns a `*FieldError`.

### 3. `webhooksView`, `webhookForm`, and the settings `enter` change (§3.3-§3.5)

### 4. UI tests and goldens

**Tests** (with `fakeActions.configEdits`):
- **a.** `enter` on `notify.webhooks`, `a`, type a url, press enter. One edit, and its body has the hook.
- **b.** Edit with two fixture hooks: change the second's format to `discord`. The body's second hook has
  `"format": "discord"`, and the first is unchanged.
- **c.** `d`, then `y`, on the only hook. The body has no `notify` key.
- **d.** Type `nope` into `events`. The form shows `unknown event nope`, and enter records no edit.
- **e.** `state_changed:needs_you, builder_stalled` is accepted and stored as two events.
- **f.** `enter` on `scan_patterns` shows the new notice.

**Goldens:**
- `settings-webhooks-empty-132`: the pushed view with no hooks;
- `settings-webhooks-132`: two fixture hooks, the cursor on the second;
- `settings-webhook-add-132`: `a` on the empty list.

**Required mutation:** make `SetWebhooks` store an empty array instead of deleting `notify`. Tests 2c and 4c must fail.
Report the failing lines, then revert.

### 5. Checks, the plan, the commit

1. Run the final commands listed in step 0.
2. Copy this plan to `docs/plans/2026-09-26-cockpit-settings-webhooks.md`.
3. `git add -A && git commit -m "feat(cockpit): :settings › notify.webhooks -- list, add, edit and delete webhooks"`.
   A new commit.

## 5. The approved boards, as text

`WebhooksD2`:
```
  ◆ relevo   settings ›  notify.webhooks                                                   v0.14.0    04:20

   0 webhooks

   URL                                                                               FORMAT    EVENTS
   none yet
                                                                      (footer)  a add   esc back   ? all keys   q quit
```
`WebhookAddD2` (a modal over the shaded list):
```
╭─ add webhook ─────────────────────────────────────────────────────────╮
│   url          https://example.com/relevo▏                            │
│   format       json    slack    discord                               │
│   events                                     every event when empty   │
│   events: state_changed  round_started  fork_created  builder_stalled  binding_stale │
│           state_changed:needs_you narrows state_changed to one state  │
│    enter  save       tab  next field       ←→  choose       esc  cancel │
╰───────────────────────────────────────────────────────────────────────╯
```
The build draws its field rows in the settings forms' spacing (a blank row between fields), not this board's tighter
one.

## 6. Deletions

- The `"opens its own editor soon"` notice. It is replaced for both rows (§3.5).

Nothing else is deleted, and no existing test assertion changes.

## 7. Stop rather than improvise

Halt and report if any of these happens:
- an existing golden changes;
- `policy.go` cannot take the exported list without breaking the size check, and a new file cannot hold it either;
- `EditPolicy` refuses an array value at `notify.webhooks`.
