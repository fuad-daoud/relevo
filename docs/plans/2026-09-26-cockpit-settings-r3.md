# Cockpit `:settings`, round 3: the scope, serve.scope and classify forms; human refusals (2026-09-26)

## 1. System overview

`:settings` rounds 1 and 2 are one commit on this branch. `enter` already opens a group form for the rounds, check,
timing and max_builders rows. On the five composite rows, `enter` answers
`notice(key + " opens its own editor in the next round")`.

This round:
1. **Three composite forms.** `scope`, `serve.scope` and `classify` get forms built on the existing `settingsForm`.
   The approved design for the scope forms is the canvas board `SettingsScopeD2` (§5).
2. **Human refusals.** The raw validation text reaches the user today, e.g.
   `can't reset max_tier: roles.json: builder.tier: yolo exceeds max_tier edit: bad roles`. It becomes plain words.

`scan_patterns` and `notify.webhooks` keep their notice this round. Their list editors come after a design board.

**Commit rule for this round (important):** make a **new commit** on top of the branch. **Do not amend.** This round
runs on a remote builder, and relevo can only take a fast-forward back from it.

## 2. File structure

```
internal/ui/settings_form.go        newSettingsForm dispatches to the new builders (keep this file ≤ 600 lines)
internal/ui/settings_form_defs.go   NEW: the scope/serve.scope/classify field lists and their parsers
internal/ui/view_settings.go        enter on scope, serve.scope and classify opens the form; the notice stays for the other two
internal/relevo/configpolicy.go     + HumanPolicyError(err error) string
internal/relevo/configpolicy_test.go  + its table test
internal/ui/view_settings_test.go   + form tests
internal/ui/golden_test.go, testdata/settings-scope-form-132.golden, settings-classify-form-132.golden  NEW goldens
docs/plans/2026-09-26-cockpit-settings-r3.md
```

## 3. Data structures

### 3.1 `settingField` gains one field

```
disabled func(f settingsForm) bool // nil = never; true = drawn faint, skipped by focus, validation and sets()
```

### 3.2 The scope form (`form == "scope"` → prefix `scope`; `form == "serve.scope"` → prefix `serve.scope`)

| label | path | kind | parse (empty text deletes the key) | hint |
|---|---|---|---|---|
| `enabled` | `<p>.enabled` | chips `on off` | a bool | — |
| `slice` | `<p>.slice` | text | must end in `.slice`, or `"a systemd slice name ending in .slice"` | `default: systemd's` |
| `cpu_weight` | `<p>.cpu_weight` | text | an int 1..10000, or `"a whole number from 1 to 10000"` | `default 100` |
| `memory_max` | `<p>.memory_max` | text | `^[0-9]+[KMGT]?$`, or `"a size like 8G or 512M"` | `default none · e.g. 8G` |
| `cpu_quota` | `<p>.cpu_quota` | text | `^[0-9]+%$` and ≥ 1%, or `"a percentage like 200%"` | `default none · e.g. 200%` |
| `gate_cpu_quota` | `<p>.gate_cpu_quota` | text | as `cpu_quota` | `default: cpu_quota` |
| `allowed_cpus` | `<p>.allowed_cpus` | text | trimmed text; `policy.Parse` judges it | `default: no pinning · e.g. 0-3` |
| `tasks_max` | `<p>.tasks_max` | text | an int ≥ 1, or `"a whole number, 1 or more"` | `default none` |

- **Prefill:** from the stored block, `d.Policy.Scope` or `d.Policy.Serve.Scope`. An empty value stays empty.
- **`enabled`** starts on `on` unless the block's `Enabled` is `false`.
- **The note row:**
  - `serve.scope`: `rounds relevo serve runs use this in place of scope`;
  - `scope`: `every round this machine starts runs in this scope`.
- **Title:** `scope` or `serve.scope`.
- **Prune an empty block:** when every field of the block ends empty or unset and `enabled` is `on`, the edit deletes
  the whole block (`PolicySet{Path: <p>, Value: nil}`), so the row returns to its default. `EditPolicy`'s
  empty-parent pruning already removes an emptied object. Check that it does, and add the explicit block delete only
  if it does not. Report which.

### 3.3 The classify form (`form == "classify"`, title `classify`)

| label | path | kind | parse | hint |
|---|---|---|---|---|
| `provider` | `classify.provider` | chips `off jev` | — | — |
| `model` | `classify.model` | text | trimmed; empty deletes | `default jev-latest` |
| `threshold` | `classify.injection_threshold` | text | a float in (0, 1], or `"a number above 0, up to 1"` | `default 0.7` |
| `timeout` | `classify.timeout_ms` | text | a duration > 0, stored as ms (the existing `parseDuration`) | `default 4s` |

- **`provider` starts** on `jev` when `d.Policy.Classify != nil`, otherwise on `off`.
- **`disabled`** holds for `model`, `threshold` and `timeout` while the provider chip is `off`.
- **`sets()` when `provider` is `off`:** exactly `[{classify, nil}]` if `classify` was stored, else nothing (so
  "nothing changed"). Other fields are ignored.
- **Turning it on:** `provider` from `off` to `jev` sets `classify.provider = "jev"`, plus the changed text fields.
- **The note row:** `scores each builder line for prompt injection, beside the pattern scan`.
- **`TYPESAFE_API_KEY`:** the classifier needs it. Do not mention or check it.

### 3.4 `relevo.HumanPolicyError(err error) string` (new, `configpolicy.go`)

Returns plain words for a validation error from `EditPolicy`/`ResetSetting`/`dryRun`.

**Rules, first match wins:**
1. The text matches `(?:^|: )(\w[\w-]*)\.tier: (\w+) exceeds max_tier (\w+)`. Return
   `<actor> runs at <tier>, above <max>; lower its tier in :actors first`.
2. Otherwise strip:
   - a leading `<name>.json: ` (any of `roles.json`, `policy.json`, `actors.json`, `candidates.json`, `agents.json`);
   - a trailing `: bad <word>`.

   Return the rest.

**Use it:**
- in `view_settings.go`'s reset refusal (`can't reset <key>: ` + HumanPolicyError);
- in `settingsForm`'s form-level error row.

A per-field parse error stays as it is.

## 4. Pseudocode

```
view_settings enter:
    row.Form in {rounds, check, timing, max_builders, scope, serve.scope, classify} -> openOverlay(newSettingsForm(...))
    row.Form in {scan_patterns, webhooks} -> notice(key + " opens its own editor soon")
settingsForm.update: tab/shift+tab skip disabled fields; left/right on a chip row re-evaluates every field's disabled state
settingsForm.modal: a disabled field draws its label, input and hint in faintStyle
```

## 5. The approved board (`SettingsScopeD2`), as text

```
╭─ serve.scope ────────────────────────────────────────────────────────╮
│                                                                      │
│   enabled           on    off                                        │
│                                                                      │
│   slice            relevo.slice▏                  default: systemd's │
│   cpu_weight                                      default 100        │
│   memory_max                                      default none · e.g. 8G │
│   cpu_quota                                       default none · e.g. 200% │
│   gate_cpu_quota                                  default: cpu_quota │
│   allowed_cpus                                    default: no pinning · e.g. 0-3 │
│   tasks_max                                       default none       │
│                                                                      │
│   rounds relevo serve runs use this in place of scope                │
│                                                                      │
│    enter  save      tab  next field      space  on / off      esc  cancel │
╰──────────────────────────────────────────────────────────────────────╯
```
- The board's `space on / off` key is **not** built. Chips move with `←→`, as in the rounds form, and the key row says
  `←→ choose`.

## 6. Ordered steps

### 0. Working efficiently

**How to work:**
- In one batch, read:
  - `internal/ui/settings_form.go`, `internal/ui/view_settings.go`, `internal/ui/view_settings_test.go`;
  - `internal/relevo/configpolicy.go`, `internal/relevo/configpolicy_test.go`;
  - `internal/policy/policy.go:120-200` (`ScopePolicy`, `Classify`) and `:400-450` (`validateScope`);
  - `internal/ui/golden_test.go` (search `settings-`).
- Make each file's change in one edit.

**Commands:**
- First run `mkdir -p $HOME/.cache/relevo-verify/tmp`.
- Focused loop: `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/relevo/ ./internal/ui/ -run 'Setting|Policy|Golden' -count=1`
- Goldens: `... go test ./internal/ui/ -run TestGoldenViews -update -count=1`. Only the two new goldens may appear; no
  existing golden may change.
- Final checks:
  - `sh scripts/check-comments.sh && sh scripts/check-filesize.sh`
  - `test -z "$(gofmt -l $(git ls-files '*.go'))"`
  - `go vet ./internal/...`
  - `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/relevo/ ./internal/ui/ ./internal/policy/ -count=1`
  - `sh scripts/check-name.sh`
  - `golangci-lint` is not installed on this server; skip it and say so.
- Comments say *why*, with no `§`, `#NNN` or plan references. `check-comments.sh` enforces this.

### 1. `HumanPolicyError` and its table test

**Cases:**
- `roles.json: builder.tier: yolo exceeds max_tier edit: bad roles` →
  `builder runs at yolo, above edit; lower its tier in :actors first`
- `policy.json: serve.scope.slice: must end in ".slice", got "x": bad policy` →
  `serve.scope.slice: must end in ".slice", got "x"`
- `something else` → `something else`

Then wire it into the reset refusal and the form error row.

### 2. `disabled` on `settingField` (§3.1)

### 3. The scope forms (§3.2) and the classify form (§3.3), in `settings_form_defs.go`; route `enter` (§4)

### 4. Tests (`view_settings_test.go`) and goldens

**Tests:**
- **a.** `serve.scope` form: prefilled `slice` = `relevo.slice`. Type `200%` into `cpu_quota`, press enter. One edit;
  its policy body has `serve.scope.cpu_quota = "200%"` and still `serve.scope.slice`.
- **b.** `serve.scope` form: clear `slice`, press enter. The body has no `serve` key at all.
- **c.** `scope` form: type `8` into `cpu_weight` and `abc` into `memory_max`. The form shows `a size like 8G or 512M`,
  and enter records nothing.
- **d.** classify form on a policy without `classify`: move `provider` to `jev`, press enter. The body has
  `classify.provider = "jev"` and nothing else under `classify`.
- **e.** classify form on a policy with `classify.provider = "jev"`: move `provider` to `off`, press enter. The body has
  no `classify`.
- **f.** classify form, provider `off`: `tab` never focuses `model`.
- **g.** `r` on `max_tier` with actors at yolo: the notice reads
  `can't reset max_tier: builder runs at yolo, above edit; lower its tier in :actors first`.

**Goldens:**
- `settings-scope-form-132`: `enter` on `serve.scope`, with the real-policy fixture;
- `settings-classify-form-132`: `enter` on `classify` (provider off, fields faint).

**Required mutations** (report the failing lines, then revert):
- **M1:** `disabled` fields still counted by `sets()` → e fails.
- **M2:** rule 1 of `HumanPolicyError` removed → step 1's first case and test g fail.

### 5. Checks, the plan, the commit

1. Run the final checks listed in step 0.
2. Copy this plan to `docs/plans/2026-09-26-cockpit-settings-r3.md`.
3. `git add -A && git commit -m "feat(cockpit): :settings round 3 -- scope, serve.scope and classify forms; plain-word refusals"`.
   A **new** commit; do not amend.

## 7. Deletions

The notice text for `scope`, `serve.scope` and `classify`. The other two rows keep a notice, reworded to
`… opens its own editor soon`. Nothing else is deleted, and no existing test assertion changes.

## 8. Stop rather than improvise

Halt and report if any of these happens:
- an existing golden changes;
- `EditPolicy` cannot express a block delete;
- a file passes 600 lines after the split.
