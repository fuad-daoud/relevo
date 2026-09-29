# OpenCode plugin live fixes, round 2: a dialog keeps keyboard focus

Base: branch `relevo/oc-live` at `b520136` (PR #503). Round 1's plan is
`docs/plans/2026-09-25-opencode-plugin-live-fixes.md`. If any step is
impossible as written or contradicts the code, **halt and report**. Do not
improvise, and do not bend a test.

## 0. Working efficiently

- Read once, in one batch:
  - `internal/harness/opencodeplugin/tui.tsx`: `showNeedsYouDialog`, from
    `dialogOpen = true` through its `finally`; the fleet route's outer box
    (`focusable`/`focused`, around line 850); and the binding route's outer
    box and its `<scrollbox … focusable focused onKeyDown={onKey}>` (around
    lines 1540 and 1597);
  - `scripts/opencode-plugin-smoke.sh` lines 160-205, plus the assertions
    39-46 block and the final `PASSED` line;
  - `scripts/testdata/opencode-plugin/relevo`, its `status)` branch.
- Test with `bash scripts/opencode-plugin-smoke.sh`, which takes about
  100 s.
- Run `make check` once at the end. If you use `dev run`, also run
  `gofmt -l $(git ls-files '*.go')` and `sh scripts/check-name.sh` locally.
- Never dump the environment into a report or commit.

## 1. What the planner found

The planner installed round 1's plugin and drove it in a real OpenCode
session.

**Result:** the `dialogOpen` key guard alone is not enough. On the fleet
page, "Tell the planner…" showed its prompt, but the prompt took no keys:
typed text did not appear, Enter did nothing, and only Esc closed it.

**Cause:** the page's outer box (and, on the binding page, the scrollbox)
carries `focused`. A status poll that changes the doc re-renders the route,
and the re-render re-applies `focused`, taking keyboard focus from the
dialog's text input. In real use the doc changes every few polls, because the
row's `clock` and `waiting` move.

**The fix, verified live by the planner** (by patching the installed copy):

- `focused={!dialogOpen}` on the three focus sites;
- one `updateStore()` right after `dialogOpen = false` in the `finally`, so
  the page re-renders and takes focus back when the dialog closes;
- round 1's `await new Promise((resolve) => setTimeout(resolve, 100));` in
  `showNeedsYouDialog` removed.

With those three changes, the planner confirmed on both pages that
text containing `a`, `[` and `]` reaches the chat whole, even across an 8 s
wait while polls land. The page's own keys (Tab, `a`) work again right after
the dialog closes. The 100 ms delay is not needed.

**Round 1 did not report the delay.** It was not in the plan and is not
listed under Deviations. Every change outside the plan must be listed in the
report.

**Why the smoke missed it:**

- The fake `status` output stops changing after its 3rd call, so no poll
  re-renders the page while a dialog is open.
- S1 and S2 type immediately after the prompt opens.
- M1 and M2 (remove the guard) therefore passed.

## 2. Changes

### 2.1 `tui.tsx`

1. The three focus sites become `focused={!dialogOpen}`:
   - the fleet route's outer box;
   - the binding route's outer box;
   - the binding body's `<scrollbox>`.

   `focusable` stays. The "no binding selected" box keeps plain `focused`.
2. In `showNeedsYouDialog`'s `finally`, call `updateStore()` right after
   `dialogOpen = false`, with no channel argument, so it always bumps.
3. Delete the line
   `await new Promise((resolve) => setTimeout(resolve, 100));`.

Nothing else changes.

### 2.2 Fake `relevo` (`scripts/testdata/opencode-plugin/relevo`, `status)` branch)

The rows' `clock` values must change on every call, so every poll changes
the doc, as it does for real:

- Print the chosen status file with every `"clock": "<anything>"` replaced
  by `"clock": "<COUNT>s"`, where COUNT is the call counter the branch
  already keeps.
- Use `sed`.
- The selection rule is unchanged: status-1 for calls up to 2, status-2
  after that.

No assertion reads a clock value today. If any assertion from 1 to 46 fails
because of this change, **halt and report**. Do not edit that assertion. In
particular, assertion 19 (scroll kept across polls) must still pass: the
body is keyed by name, round and tab, so a re-render must keep the scroll.
If it does not, that is a real bug, so halt.

### 2.3 Smoke steps

- **S1** (after `capture "05i-reenter"`): between the `send Enter` that picks
  "Send plan file…" and the `type_lit`, change the sleep to `sleep 7`, so at
  least one poll re-renders the page while the prompt is open. After
  `capture "05j-sent"`, add:
  - `send Tab; sleep 1.0; capture "05k-after-dialog-tab"`;
  - then `send BTab; sleep 1.0` (tmux names Shift-Tab `BTab`), to go back to the tab the page was on, so
    06-dialog's steps start where they do today.

  Check that 06-8 still pass.
- **S2** (the fleet block that captures `11-fleet-after-dialog`): replace
  its dialog line with:
  - `send a; sleep 1.5; send Down; sleep 0.3; send Enter; sleep 7`, which
    picks "Send plan file…" and waits across a poll;
  - `type_lit "a/fleet [plan] a.md"; sleep 0.8; send Enter; sleep 2`.

  Then `capture "11-fleet-after-dialog"` as today.
- Do not use "Tell the planner…" in the smoke. It posts into the real OpenCode
  session the smoke attaches to.

### 2.4 Assertions

39 and 40 are unchanged: 39 still expects `a[b]/plan a.md`, and 40 still
expects the cursor on webshop. Add these two, and update the final `PASSED`
count to 48:

| # | Asserts |
|---|---|
| 47 | The fake log has a line exactly `send --name webshop --file a/fleet [plan] a.md`. |
| 48 | In `05k-after-dialog-tab.txt`, the selected tab (the `[ … ]` word in the tab row) differs from the selected tab in `05j-sent.txt`. The page takes keys again after the dialog closes. |

## 3. Mutations (smoke; run each, report the result, restore)

| # | Break | Must fail |
|---|---|---|
| M1 | remove the `dialogOpen` return from the binding page's `onKey` | 39 |
| M2 | remove it from the fleet's `onKeyDown` | 40 or 47 |
| M8 | put plain `focused` back on all three sites (keep the guard) | 39 or 47 |
| M9 | drop the `updateStore()` in `finally` | 48, if the next poll does not refocus first. Report what happened either way. |

**If M1, M2 or M8 does not make its named check fail, halt and report.** That
means the smoke still does not reproduce the live failure, and passing on is
not acceptable.

## 4. Ship

1. Save this plan verbatim at
   `docs/plans/2026-09-25-opencode-plugin-live-fixes-r2.md`.
2. Make one new commit, without amending: `fix(opencode): a dialog keeps
   keyboard focus across polls; drop the 100 ms delay (#496)`.
3. Push with `git push origin relevo/oc-live`.
4. Comment on PR #503 with the smoke result, M1/M2/M8/M9 and `make check`.

## 5. Scope

`git diff --stat b520136` shows only `tui.tsx`,
`scripts/opencode-plugin-smoke.sh`, `scripts/testdata/opencode-plugin/relevo`
and this plan. List **every** change beyond §2 under Deviations. If a change
outside §2 seems necessary, halt and report rather than make it.
