# Cockpit config views, round 3: the add and edit candidate forms (2026-09-25)

Branch `relevo/ck-config`. It already has one commit, holding rounds 1 and 2: the config-edit backend in
`internal/relevo/configedit.go`, the Actions `ConfigDoc`/`ApplyConfig`/`Probe`, and `:candidates` in
`internal/ui/view_candidates.go`. **Amend that commit**; do not add a second one.

## 1. System overview

This round adds the candidate form, one overlay used both to add and to edit, and binds `a` (add) and `enter` (edit)
on `:candidates`. The existing `formBox` (`internal/ui/form.go`) has only text fields, so this round adds a small set of
**shared form-row helpers** that round 4's actor form reuses, and a concrete `candidateForm` overlay.

The approved design, inside a 90-wide modal over the shaded `:candidates`, has these fields:
- **harness:** a chip per supported kind (`agy  claude  codex  opencode`), the selected one on the accent chip. `←`/`→`
  switches it while the row is focused.
- **provider:**
  - On a harness **with** a provider list (`harness.Harness.Providers`, agy and claude): a chip row of exactly those
    providers, none selected until one is picked (`←`/`→`). The provider cannot be typed.
  - On a harness **without** one (codex, opencode): a text field. Under it is a chip row of the providers the config
    already uses with that harness. `↑`/`↓` cycles through them, filling the field.
  - When the typed value is non-empty and matches none of those providers, the chip row is replaced by `new provider`
    in the accent colour.
- **model:** a text field. When it is empty its placeholder, in faint, is `the model id after <provider>/` on
  opencode with a provider set, and `the model id` otherwise.
- **name:** read-only. It is the name the model would give.
  - When adding: `follows the model` (muted) while the model is empty, else the derived name (text).
  - When editing: the derived name (text), and, if it differs from the current name, `   was <old> · still <slots>`
    (faint). `<slots>` is the SERVES text of the row being edited, e.g. `builder 3`; omit ` · still …` when empty.
- **Info lines** (muted, names in text):
  - When the provider is gated (some `env.Report.Gated` entry whose token's provider segment equals it):
    `<provider> is gated until <statsGateUntil> · <statsGateReason>`, with `gated` in red.
  - When editing, if the provider is gated and unchanged, add `the gate is the provider's, so the new model stays gated`.
  - When adding, always add `no actor picks it until you add it to one in :actors` (`:actors` in text).
- **Errors:** a `*relevo.FieldError` shows red, on the row right under its field (harness, provider, model). A
  whole-form error (`Field == ""`) goes on the line above the keys. An error is shown only for a field the user has
  **touched**, or after enter was pressed once.
- **Keys row inside the box:** `enter save` (or `add`), `tab next field`, `esc cancel`. The `enter` chip and its label
  are **faint** while the input is invalid. The shell footer shows the same keys through `overlayKeyer`.

Validation is live: after every key, the form rebuilds the edit with `relevo.AddCandidate`/`relevo.EditCandidate` on
its `doc`. These are pure and fast.

**Changed from the canvas:** the board showed `p probe` inside the form. That key is dropped: `p` must type into the
text fields, and an unsaved candidate cannot be probed. Probe it from the list after saving.

## 2. File structure

```
internal/relevo/configedit.go      + PreviewCandidateName
internal/relevo/configedit_test.go + its test
internal/ui/form_rows.go           NEW: shared row renderers (label, chips, text field, error, keys)
internal/ui/candidate_form.go      NEW: candidateForm overlay
internal/ui/candidate_form_test.go NEW
internal/ui/view_candidates.go     bind `a` and `enter`
internal/ui/golden_test.go         + cases cand-add-132, cand-edit-132, cand-edit-invalid-132, cand-edit-agy-132
docs/plans/2026-09-25-cockpit-config-r3-candidate-form.md
```

## 3. Data structures

```go
// relevo
// PreviewCandidateName is the name in would get: for editing != "" the entry named editing takes in's
// harness/provider/model; for "" in is appended. Names are derived as AddCandidate/EditCandidate derive them.
// No validation; an empty model gives "".
func PreviewCandidateName(d ConfigDoc, editing string, in CandidateInput) string
```

```go
// ui: candidate_form.go
type candidateForm struct {
    ctx      context.Context
    actions  Actions
    doc      relevo.ConfigDoc
    gates    []ledger.Gate // env.Report.Gated at open
    now      time.Time
    editing  string        // "" = add; else the candidate's current name
    slots    string        // SERVES text of the edited row ("" when adding)
    kinds    []string      // harness kinds, harness.All() order
    hsel     int           // selected kind index
    psel     int           // selected provider index on a listed harness; -1 = none
    provIn   textinput.Model
    modelIn  textinput.Model
    focus    int           // 0 harness, 1 provider, 2 model
    touched  [3]bool
    tried    bool          // enter pressed at least once
    sugg     int           // position in the provider suggestion cycle; -1 = none
}
```

Rows in `form_rows.go` are pure functions returning one styled line each, with **no** trailing padding (the modal
pads):
- `formLabel(label string, focused bool) string`: padded to 11; accent when focused, faint otherwise. This matches
  `formBox`'s 10+1.
- `formChips(items []string, sel int, disabled bool) string`: each chip is `chip(...)`. The selected one uses
  `chipAccentStyle`; the others `kbdStyle` text muted, all joined by two spaces. When disabled, every chip is faint.
- `formInput(in textinput.Model, focused bool, placeholder string, width int) string`: the input's view on
  `selBandStyle`, padded to `width`. When the value is empty and not focused, the placeholder is faint.
- `formError(msg string) string`: indented to the value column (12 spaces), `redStyle`.
- `formKeys(keys []KeyHelp, disabled map[string]bool) string`: kbd chips, disabled ones faint, separated by six spaces.

## 4. Contracts

- **`newCandidateForm(env Env, doc relevo.ConfigDoc, editing string, slots string, harnessKind string) candidateForm`**
  - Editing: prefill the harness, provider and model from the entry. `focus = 2` (model), cursor at the end.
  - Adding: the harness is `harnessKind` (the cursor row's harness; the first kind if ""), provider empty, `psel = -1`,
    and `focus = 0`.
- **`input() relevo.CandidateInput`**: the selected kind, plus the provider (`Providers[psel]` or `provIn.Value()`) and
  `modelIn.Value()`.
- **`check() (relevo.ConfigEdit, error)`**: `relevo.AddCandidate(doc, in)` when adding, else
  `relevo.EditCandidate(doc, editing, in)`.
- **`update(k tea.KeyMsg) (overlay, tea.Cmd, bool)`**:
  - `esc` closes.
  - `tab`/`shift+tab` moves focus, wrapping over 3 fields.
  - **Harness focused:** `left`/`right` change `hsel`, wrapping.
    - On a change to a kind **with** Providers: set `psel` to the index of the current provider in that list, else -1,
      and mark provider touched.
    - On a change to a kind **without** Providers from one with them: if `psel >= 0`, copy that provider into `provIn`.
  - **Provider focused, listed kind:** `left`/`right` move `psel`, starting at 0 from -1. Mark touched.
  - **Provider focused, open kind:**
    - `up`/`down` cycle `sugg` through `suggestions()` and set `provIn` to it.
    - Every other key goes to `provIn`. Mark touched.
  - **Model focused:** keys go to `modelIn`. Mark touched.
  - **`enter`:** run `check()`.
    - On success: close, and return `runAction(ctx, "add candidate"|"edit candidate", name, ApplyConfig(edit))`, where
      name is `edit.Name`.
    - `ErrNoChange`: close and return `notice("nothing changed")`.
    - A `*FieldError`: set `tried`, and focus its field (harness 0, provider 1, model 2; "" keeps focus). Stay open.
- **`suggestions() []string`**: the distinct providers of `doc.Candidates` whose harness is the selected kind, in stored
  order.
- **`keys()`**: `enter save`/`add`, `tab next field`, `esc cancel`.
- **`modal(width)`**: `kind` is `add candidate` or `edit <name>`, `want` 90, and danger false. The rows follow §1's order:
  1. blank;
  2. harness row;
  3. its error (or nothing);
  4. blank;
  5. provider row;
  6. suggestion / `new provider` row (open kind) or error row;
  7. blank;
  8. model row;
  9. its error;
  10. blank;
  11. name row;
  12. blank;
  13. info lines;
  14. whole-form error;
  15. blank;
  16. keys row.

  On a listed kind, a provider error takes row 6.
- **`:candidates` keys** (when actions):
  - `a`: `openOverlay(newCandidateForm(env, doc, "", "", cursorRow.Harness))`.
  - `enter`: `openOverlay(newCandidateForm(env, doc, cursorName, servesText(cursorRow), ""))`.

## 5. Pseudocode

```
update(k):
  switch focus/k as §4
  (after any change) nothing else is cached: modal() recomputes check() and the preview each render
modal(width):
  in := input(); _, err := check(); fe := asFieldError(err)
  show fe under its field iff touched[field] || tried; show enter disabled iff err != nil && err != ErrNoChange
```

## 6. Error handling

- Validation errors stay in the form.
- An `ApplyConfig` failure comes back as a red notice and a `:log` row through `finishAction`, and the form is already
  closed. Re-open it to retry.

## 7. Working efficiently

- **Read in one batch:**
  - `internal/ui/form.go`, `confirm.go:40-130, 340-392`, `compose.go:70-160`, `styles.go`
  - `internal/ui/view_candidates.go` (round 2)
  - `internal/relevo/configedit.go:100-230, 460-560`
  - `internal/harness/harness.go:96-230`
  - `internal/ui/golden_test.go` (round 2's candidates cases)
- **Focused tests** (local only; `make check` is refused on this machine by a hook):
  `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -run 'Candidate|Golden' -count=1` and
  `… ./internal/relevo/ -run PreviewCandidateName -count=1`
- **Goldens:** `-run TestGoldenViews -update`. Only the four new files may appear.
- **Final:** `go vet ./internal/...`, `test -z "$(gofmt -l $(git ls-files '*.go'))"` and `sh scripts/check-name.sh`.

## 8. Steps

1. **`PreviewCandidateName`**, with a test:
   - deepseek's model → `cline-pass/deepseek-v4.2-flash#high` gives `deepseek-v4.2-flash`;
   - adding `opencode/openrouter/` with an empty model gives "".
2. **`form_rows.go`.**
3. **`candidate_form.go`**, and bind `a`/`enter` in view_candidates.go. Use the round 2 fixture doc and gates.
4. **Goldens** (132×34):
   - `cand-add-132`: `a` on the first row, then `tab` to provider, type `cline-pass`.
   - `cand-edit-132`: `enter` on deepseek, clear the model with ctrl+u, then type `cline-pass/deepseek-v4.2-flash#high`.
     The name row reads `deepseek-v4.2-flash   was deepseek-v4.1-flash · still builder 3`.
   - `cand-edit-invalid-132`: `enter` on glm-5.3-flash, `tab` back to provider, set `openrouter/z-ai`. The provider
     error shows and `enter` is faint.
   - `cand-edit-agy-132`: `enter` on glm-5.3-flash, focus harness, `left` until agy. The provider chips are
     `google  agy-extra` with none selected, the error `pick one of agy's providers` shows, and `enter` is faint.
5. **Unit tests** (candidate_form_test.go):
   - enter with a valid edit → `fakeActions.configEdits[0].Message ==`
     `"edit candidate deepseek-v4.1-flash → deepseek-v4.2-flash"`;
   - an unchanged edit + enter → a notice `nothing changed` and no edit;
   - enter on an invalid form stays open and sets `tried`;
   - `up`/`down` on the open-kind provider cycles `openrouter`/`cline-pass` for opencode;
   - switching to claude selects `anthropic` if it was the provider, else none;
   - a gated provider shows the gate line;
   - adding shows the `:actors` line.

   **Mutation (required):** drop the `touched || tried` condition so errors always show, and `cand-add-132` or a named
   test must fail. Restore it.
6. **Final checks.** Copy this plan into the worktree's `docs/plans/`, `git add -A`, `git commit --amend --no-edit`.

## 9. Stop rather than improvise

If round 2's names differ from what this plan uses (`candidatesView`, `env.Actions.ApplyConfig`, `servesText`, the
fixture helper), use the real names, and **report** each rename in the report; that is not a halt. If the form cannot
fit 90 wide at 132 columns, or `harness.Harness.Providers` is missing, **halt and report**.

## 10. Addendum: the `:candidates` footer

Round 2's footer at 132 columns drops `p probe`, because `keysView` runs out of room; see
`testdata/candidates-132.golden`. Fix it in `view_candidates.go`:
- `Keys()` no longer lists `↑↓ move`;
- add a `HelpKeys()` (the `helpKeyer` interface, view.go:27) that lists every key, `↑↓ move` included, so the `?`
  modal still shows it.

**Verify:** `candidates-132.golden` regenerates with `p  probe` in the footer. Only the footer line of the candidates
goldens changes from this addendum.
