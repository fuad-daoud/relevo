# Cockpit config views, round 4: `:actors` (2026-09-25)

Branch `relevo/ck-config`. It already has one commit, holding rounds 1-3:
- the backend in `internal/relevo/configedit.go`;
- Actions `ConfigDoc`/`ApplyConfig`/`Probe`;
- `:candidates` with its gate, ungate, probe and delete;
- the candidate form, plus the shared form rows in `internal/ui/form_rows.go`.

**Amend that commit**; do not add a second one.

## 1. System overview

`:actors` lists the actors. `enter` pushes `actors › <name>`, where the candidate list is edited: reorder, on/off,
add from a picker, remove. `e` there opens the actor form (agent, tier, check). `a`/`d` on the list add or delete an
actor. Every change writes at once through `relevo.SetActorEntries`, `EditActor`, `AddActor` or `DeleteActor`, then
`env.Actions.ApplyConfig`.

The approved boards, at 132×34:

**`:actors`**
```
   3 actors   1 writer   2 readers

   ACTOR                                                   AGENT          SHAPE   TIER   CANDIDATES  NEXT PICK
   builder                                                 plan-executor  writer  yolo            6  sonnet
   reviewer                                                reviewer       reader  yolo            2  sonnet
   researcher                                              researcher     reader  —               1  haiku


   builder   plan-executor · writer · tier yolo
    1  gemini-3.8-flash-high  gated 2h
    …
    5  sonnet                 ready       next pick
    6  glm-5.3-flash          ready
 ↑↓ move   enter open   a add   d delete   …
```
- The cursor row is on the band, name bold.
- NEXT PICK is green; `—` in faint when there is none.
- In the detail block the name is faint bold and the rest muted. The numbers are faint, the names text, and the status
  red/green/faint (`off`). `next pick` is green bold.

**`actors › builder`** (pushed; the breadcrumb reads `actors › builder`)
```
   plan-executor · writer   tier yolo   6 candidates   next pick sonnet

    #  CANDIDATE                                                   HARNESS   PROVIDER    STATUS
    1  gemini-3.8-flash-high                                       agy       google      gated 2h
    …
    5  sonnet                                                      claude    anthropic   ready  next pick
    6  glm-5.3-flash                                               opencode  openrouter  ready


   check after each round is on, but no check command is set; set one in :settings
 ↑↓ move   shift+↑↓ reorder   space on / off   a add   d remove   e edit   esc back
```

## 2. File structure

```
internal/ui/view_actors.go        NEW: actorsView (list + detail block, a/d)
internal/ui/view_actor.go         NEW: actorView (pushed detail: reorder, on/off, add picker, remove, e)
internal/ui/actor_form.go         NEW: actorForm overlay (agent, tier, check) and addActorForm (name + agent)
internal/ui/view_actors_test.go   NEW
internal/ui/cmdline.go            + {"actors", "", "who runs each job, and in what order", false}
internal/ui/view_rounds.go        execLine: + case "actors" (same nil-Actions notice as candidates)
internal/ui/golden_test.go        + actors-132, actor-builder-132, actor-edit-132, actor-pick-132
internal/ui/testdata/…            new goldens + regenerated cmdline-open / command-real-132
docs/plans/2026-09-25-cockpit-config-r4-actors.md
```

## 3. Data structures

- `actorsView{doc relevo.ConfigDoc; err error; loaded bool; cur, top int; actions bool}`.
- `actorView{name string; doc relevo.ConfigDoc; err error; loaded bool; cur, top int}`.
- `actorForm{ctx; actions; doc; actor string; agents []string; asel int; tiers []string; tsel int; check bool; builtin bool; err string}`.
  - The tier order is `read edit yolo harness`, and `tsel == -1` means unset.
- `addActorForm{ctx; actions; doc; nameIn textinput.Model; agents []string; asel int; focus int; tried bool}`.

Helpers in view_actors.go (pure, and unit-tested):
- `actorOrder(doc) []string`: `builder`, `reviewer`, `researcher` when present, then the rest by name.
- `agentShapeOf(doc, agent) string`: "writer"/"reader" from `actors.Shipped` or the custom entry. Source agents: parse
  with `agentsrc` the way `configedit.agentShape` does; reuse that function if it is exported, else export it as
  `relevo.AgentShape`.
- `nextPick(doc, actor, gated []ledger.Gate) string`: the first entry, in order, that is on and whose candidate token has
  no gate. "" when none.
- `entryStatus(doc, e actors.Entry, gated, now) (text string, style lipgloss.Style)`:
  - `off` in faint when `e.Off`;
  - else a gate gives `statsGateLeft` in red;
  - else `ready` in green.

## 4. Contracts

- **`:actors` list**
  - Crumbs `{"actors"}`.
  - Context left: `   <n> actors   <w> writer(s)   <r> reader(s)`. Use `writer` for 1 and `writers` otherwise; the same for
    readers.
  - Columns: ACTOR takes the rest of `[3, width-3)`; then AGENT 13, SHAPE 6, TIER 5, CANDIDATES 10 (right-aligned),
    NEXT PICK 21.
  - Narrow widths: drop SHAPE, then AGENT, while ACTOR would be under 12.
  - Detail block: two blanks, then `<name>   <agent> · <shape> · tier <tier|—>`, then one line per entry:
    `" %2d  %-23s%-10s"` with the `next pick` marker.
  - **Keys:**
    - `↑↓`/home/end/pgup/pgdn move, and the page follows.
    - `enter` pushes `actorView` for the cursor actor, `push(v, init)`.
    - `a` opens addActorForm.
    - **`d`:**
      1. `edit, err := relevo.DeleteActor(doc, name)`. An error (a builtin actor) gives `notice(err.Error())`.
      2. Otherwise open a danger `confirmBox`, `kind:"delete"`, title `Delete actor <name>?`.
      3. Lines: `agent` + agent, `candidates` + the count, and `kept` + `the candidates themselves`.
      4. `y` → `runAction(ctx, "delete actor", name, ApplyConfig(edit))`.
- **`actors › <name>` (actorView)**
  - Crumbs `{name}`.
  - Context left: `<agent>` text + ` · <shape>   ` muted + `tier ` faint + tier text (`—` faint when unset) + `   <n>`
    bold + ` candidates   ` muted + `next pick ` faint + the name in green, or `—`.
  - Columns: `#` 2 right-aligned; CANDIDATE takes the rest; HARNESS 8; PROVIDER 10; STATUS 20. Only the cursor row is
    banded. Off rows are faint throughout.
  - **Check line**, for writer agents only, two blanks after the table:
    - check on (nil or true) with `doc.Policy.GateDefault() == ""`: `check after each round is on, but no check command is set; set one in ` muted + `:settings` text;
    - check on with a command: `runs ` muted + the command text + ` after each round` muted;
    - check off: `no check after each round` muted.
  - **Keys** (while `env.Running["actor:"+name]` is non-empty, every change key is ignored):
    - `↑↓` etc. move.
    - `shift+up`/`shift+down` swap the cursor entry with its neighbour (no-op at the ends); the cursor follows.
    - `space` toggles `Off` on the cursor entry.
    - `d` removes the cursor entry. Refuse with `notice("<name> needs at least one candidate")` when it is the last one.
    - `a` opens a `listBox` (list_box.go):
      - `kind: "add to <name>"`, `submit: "add as <n+1>th"`. Use ordinal words 1st/2nd/3rd/…th: `add as 7th`.
      - Items are the doc's candidates **not** already in the list, in stored order. Each item's name is the candidate
        name, its status the `entryStatus` text and style, and its note `harness · provider`.
      - `onPick` appends the pick, **on**.
      - With nothing left to add: `notice("every candidate is already in <name>'s list")`.
    - `e` opens actorForm.
    - Every entry change runs:
      1. `edit, err := relevo.SetActorEntries(doc, name, next)`. An error gives a notice.
      2. **Update `v.doc` locally** so the next key builds on it: set `doc.Actors[name].Candidates = next` on a copied
         map.
      3. Return `runAction(ctx, "edit actor", "actor:"+name, ApplyConfig(edit))`.
  - `statusMsg` and the doc message reload the doc, as in candidates.
- **`actorForm` (the `e` key)**
  - `kind: "edit <name>"`, `want` 90. Rows:
    1. blank;
    2. `name` + the name, muted, read-only;
    3. blank;
    4. `agent` + `formChips(agents, asel, false)`;
    5. under it, the selected agent's shape, muted;
    6. blank;
    7. `tier` + chips `read edit yolo harness` (none selected when unset);
    8. blank;
    9. `check` + chips `on off`, **disabled** (faint, not focusable) when the selected agent is a reader;
    10. blank;
    11. an error row (red) when `err`;
    12. blank;
    13. keys `enter save`, `tab next field`, `esc cancel`.
  - `agents`:
    - For a builtin actor name (`harness.RoleByName` ok), only agents whose shape equals the builtin's (writer for
      builder, reader for reviewer/researcher).
    - For a custom actor, every agent.
    - Order: shipped agents in `actors.Shipped` table order, then custom agents by name.
  - `left`/`right` move the focused row's selection. `tab` moves focus over agent, tier and check, skipping check when it
    is disabled.
  - `enter`:
    - If agent, tier and check all equal the stored actor, `notice("nothing changed")` and close.
    - Else `relevo.EditActor(doc, name, agent, tier, check)`:
      - an error sets `err` and the form stays open;
      - success closes and returns `runAction(ctx, "edit actor", "actor:"+name, ApplyConfig(edit))`.
- **`addActorForm` (the `a` key on the list)**
  - `kind: "add actor"`. Rows: `name` text input (placeholder `lowercase, e.g. security-review`), then `agent` chips (all
    agents, same order), then the error, then keys `enter add`, `tab next field`, `esc cancel`.
  - `enter` → `relevo.AddActor(doc, name, agent)`.
    - A FieldError shows red and the form stays open.
    - Success closes and runs `"add actor"`.

## 5. Pseudocode

```
actorView.Update(key):
  if env.Running["actor:"+name] != "": return v, nil          (for change keys only)
  next := copy(entries)
  shift+down: if cur < len-1 { swap(next[cur], next[cur+1]); cur++ }
  space:      next[cur].Off = !next[cur].Off
  d:          if len(next)==1 → notice; else next = remove(next, cur); cur = clamp
  edit, err := relevo.SetActorEntries(doc, name, next) → notice(err)
  v.doc = withEntries(doc, name, next)
  return v, runAction(ctx, "edit actor", "actor:"+name, ApplyConfig(edit))
```

## 6. Error handling

- Refusals are notices.
- ApplyConfig errors come back as a red notice plus a `:log` row (finishAction).
- The local doc copy is replaced by the next `statusMsg` reload. After a failed write that restores the truth.

## 7. Working efficiently

- **Read in one batch:**
  - `internal/ui/view_candidates.go`, `candidate_form.go`, `form_rows.go`, `list_box.go`, `confirm.go:40-130`
  - `internal/relevo/configedit.go:283-470, 600-640`
  - `internal/actors/shipped.go`, `internal/harness/tier.go`, `internal/policy` (GateDefault)
  - `internal/ui/golden_test.go` (the candidates cases)
- **Focused tests** (local; `make check` is refused on this machine by a hook):
  `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -run 'Actor|Golden|Cmdline' -count=1`
- **Goldens:** `-run TestGoldenViews -update`. Only the four new files, `cmdline-open` and `command-real-132` may change.
- **Final:** `go vet ./internal/...`, `test -z "$(gofmt -l $(git ls-files '*.go'))"` and `sh scripts/check-name.sh`.

## 8. Steps

1. `view_actors.go` with its helpers, and register the `actors` command.
2. `view_actor.go`, and the picker.
3. `actor_form.go`.
4. **Goldens** (132×34, round 2's fixture doc and gates):
   - `actors-132`;
   - `actor-builder-132` (enter on builder, cursor on sonnet);
   - `actor-edit-132` (`e` on builder);
   - `actor-pick-132` (`a` on builder: one item, `haiku`, `add as 7th`).
5. **Unit tests:**
   - actorOrder;
   - nextPick skips off and gated entries;
   - `shift+down` on gemini → `fakeActions.configEdits[0]` holds actors with builder
     `[claude-sonnet-4-6, gemini-3.8-flash-high, …]`, and the cursor moves to 2;
   - two fast `shift+down` presses while `env.Running` holds `"actor:builder"` → only one edit;
   - `space` → Off true on that entry;
   - `d` on researcher's only entry → the notice;
   - `a` → pick haiku → the builder list ends with haiku;
   - the actorForm agent chips for builder list only `plan-executor`;
   - check is disabled for reviewer;
   - `d` on builder in the list → a notice ("built in");
   - addActorForm with a bad name shows the FieldError.

   **Mutation (required):** remove the local doc update after a change, and the two-press test must fail (the second
   edit builds on the stale list) or another named test must. Restore it.
6. **Final checks.** Copy this plan, `git add -A`, `git commit --amend --no-edit`.

## 9. Stop rather than improvise

If a round 1-3 name this plan uses differs, use the real one and report it. If `policy.Policy` has no `GateDefault`, or
the listBox cannot carry a styled status, halt and report.
