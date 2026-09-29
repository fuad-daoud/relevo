# OpenCode plugin live fixes, round 3: the binding page updates in place

Base: branch `relevo/oc-live` at `b520136` **plus round 2's uncommitted
changes in the worktree**. Keep them: they are the `focused={!dialogOpen}`
sites, the `updateStore()` in `finally`, the deleted 100 ms delay, the fake's
varying `clock`, the S1/S2 changes and assertions 47-48. Round 2 halted
correctly at assertion 19. This round fixes the bug that halt exposed.

If a step is impossible as written or contradicts the code, **halt and
report**. Do not improvise, and do not bend a test.

## 0. Working efficiently

- Read `internal/harness/opencodeplugin/tui.tsx` in full once. The binding
  route is the `api.ui.router.register({ name: "relevo.binding", … })` block
  near the end. Also read the smoke's assertion 19 and its 05b/05c steps.
- Test with `bash scripts/opencode-plugin-smoke.sh` (about 100 s).
- Run `make check` once at the end. If you use `dev run`, also run
  `gofmt -l $(git ls-files '*.go')` and `sh scripts/check-name.sh` locally.
- Temporary diagnostics, such as appending to a file under `/tmp` from the
  plugin, are allowed while you work. Remove every one before committing.
- Never dump the environment into a report or commit.

## 1. The bug

The binding route's `render(props)` begins with `void store.rev;`. The host
calls `render` inside a tracking scope, so every store bump runs the whole
function again. Each run builds a new tree, including a new `<scrollbox>`
whose scroll offset starts at 0, and the `focused` props are re-applied.

This causes three visible faults:

- a scrolled report jumps back to the top whenever a poll brings a changed
  status doc;
- a live transcript, which is refetched every poll while the round is ACTIVE,
  cannot be scrolled at all;
- a dialog's text input loses focus.

Round 2's varying `clock` makes the smoke reproduce the first fault
(assertion 19). Round #477 hid it by not bumping the store for unchanged
data, but in real use the data changes every poll.

## 2. Design: a component that runs once, with reactive parts

The plugin already imports `Show` from `solid-js`, and its JSX is compiled for
Solid's fine-grained updates. A component invoked through JSX runs **once**
(Solid untracks component bodies). JSX expressions that call accessors
re-evaluate on their own when the store they read changes.

### 2.1 Structure

- The route becomes `render: (props) => <BindingPage routeProps={props} />`.
  `render` itself reads nothing from the store.
- `BindingPage` is a function component defined inside `setup`, next to the
  route, so it closes over `api`, `store`, `setStore`, `stateWord` and the
  other helpers. Its body runs once per navigation.
- Everything the current render computes at its top level moves into one
  accessor, `view()`. It reads `store.rev` first, then computes exactly what
  the render computes today:
  - params, name, row, round (with the F2 `liveID` rule and the picked-round
    rule), rounds, `currentTab`, `tabContent`;
  - the state word and the colours;
  - the side effects that ran on each render, in the same order:
    `currentRoute = "relevo.binding"`, `currentRouteParams`, `fetchHistory`
    and `fetchShow`.

  It returns those values as one object. Wrap it in `createMemo` from
  `solid-js`, so the parts below share one computation per bump.
- The JSX skeleton is static: the outer box, the header row container, the
  tab row container, the round row container and the body container.
- Only these parts are reactive expressions that call `view()`:
  - the header texts;
  - the muted sub-line;
  - the tab labels;
  - the round labels, with their `onMouseDown`;
  - the body key;
  - the body's lines (the `renderMarkdownLines`, transcript, log and diff
    branches, unchanged).
- The body keeps its keyed `Show`, with the key from `view()`:
  `` `${name}:${round}:${tab}` ``. The scrollbox is created inside the Show,
  so a new key (a tab or round change) still starts at the top. A bump with
  the same key keeps the same scrollbox, and its lines update inside it.
- **No binding** (`!view().name`) renders today's one-line "no binding
  selected" box, through `<Show when=… fallback=…>`.
- **Keys:** `onKey` reads `view()` when a key arrives, not a value captured
  at mount. Its behaviour is unchanged, including the `dialogOpen` guard.
- **Focus:** `dialogOpen` becomes reactive. Add `dialogOpen: false` to the
  store's initial value.
  - `showNeedsYouDialog` sets it with `setStore` on entry and clears it in
    `finally`. Keep the module variable too, set alongside, because the key
    guards read it synchronously.
  - The three sites read `focused={!store.dialogOpen}`.
  - The `updateStore()` in `finally` may stay.

The fleet route and the sidebar are **not** restructured in this round.

### 2.2 What must not change

- Every rendered line, colour, word and key binding stays as it is today.
- Assertions 1-48 cover them, and all 48 must pass.

## 3. Steps

1. Restructure the binding route as in §2.1. Run the smoke, and iterate until
   1-48 pass, **assertion 19 included, with round 2's varying clock left in
   place**.
2. **If 19 still fails after the restructure, halt and report.** First
   measure how many times `BindingPage`'s body runs and how many times
   `view()` runs during the 05b→05c window, with a temporary counter written
   to `/tmp`. Report both numbers. Do not try to save and restore scroll
   offsets. Do not reduce the store bumps to make 19 pass.
3. Run the mutations (§4), restoring after each one.
4. Run the full check (§0).
5. Save and ship:
   - Save round 2's plan verbatim at
     `docs/plans/2026-09-25-opencode-plugin-live-fixes-r2.md`, copied from
     `~/.local/state/relevo/oc-live/002-plan.md`.
   - Save this plan verbatim at
     `docs/plans/2026-09-25-opencode-plugin-live-fixes-r3.md`.
   - Make one new commit, without amending: `fix(opencode): the binding page
     updates in place -- scroll and dialog focus survive polls; drop the
     100 ms delay (#496)`.
   - Push with `git push origin relevo/oc-live`.
   - Comment on PR #503 with the smoke result, the §4 results, the step-2
     counters if you measured them, and `make check`.

## 4. Mutations (smoke; run each one, report the result, restore)

| # | Break | Must fail |
|---|---|---|
| M10 | `render: (props) => { void store.rev; return <BindingPage routeProps={props} />; }` (full re-render again) | 19 |
| M1 | remove the `dialogOpen` return from the binding page's `onKey` | 39 |
| M2 | remove it from the fleet's `onKeyDown` | 40 or 47 |
| M8 | plain `focused` on all three sites | 39, 47 or 48 |

If M10, M1 or M2 does not make its named check fail, **halt and report**.
Report M8's result either way.

## 5. Scope

`git diff --stat b520136` shows only:

- `tui.tsx`;
- `scripts/opencode-plugin-smoke.sh`;
- `scripts/testdata/opencode-plugin/relevo`;
- the two plan files.

List every change beyond §2 under Deviations, including any helper you add.
The smoke assertions must not change in this round, beyond round 2's 47-48.
