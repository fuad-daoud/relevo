# Status line round 2: fleet REASON width, smoke #15, spec scope line (2026-09-26)

## 1. System Overview

Round 1 (commit `4c4a80ab` on `relevo/statusline-status`) shipped the single status column. It left three defects,
and this round fixes only those:

1. **Fleet REASON column is 6 columns wide.** In `internal/harness/opencodeplugin/tui.tsx` (~lines 954-958), the fleet
   row gives the reason `96 - ROW_PREFIX.length - fixed.length` runes. The `96` is round 1's plan error. The reason
   must take the page's real remaining width.
2. **Smoke assertion #15 still greps the old sidebar line B.** In `scripts/opencode-plugin-smoke.sh` (~lines 318-326),
   `check_assertion_15` greps `r3 · opencode · question in` and `! grep -q "r4 · opencode"`. Line B now reads
   `r3 · builder on opencode · …`.
3. **Stale spec scope line.** In `docs/specs/2026-09-24-statusline-redesign-design.md`, line 8 says `--json` is
   unchanged "except four additive fields". The status line document now also carries `actor`, `status`, `tone` and
   `reason`.

## 2. File Structure

```
internal/harness/opencodeplugin/tui.tsx   fleet row: reason width from the terminal
scripts/opencode-plugin-smoke.sh          check_assertion_15 greps the new line B
docs/specs/2026-09-24-statusline-redesign-design.md   line 8 scope sentence
docs/plans/2026-09-26-statusline-actor-dedupe-r2.md   this plan, copied in last
```

Nothing else changes. If any other file must change, stop and report.

## 3. Data Structures

None change.

## 4. Contracts

### Fleet row reason width (`tui.tsx`, fleet page rows, ~lines 930-960)

- **Page width.** Get it the way the binding page already does at ~lines 1029-1036: call `useTerminalDimensions()`,
  and read `.width` from either the accessor or the object. Subtract the fleet page's own horizontal padding. Read the
  fleet page's root `<box>` for its padding, and don't guess.
- **Reason width.** `pageWidth - ROW_PREFIX.length - fixed.length`, clamped at 0. When the renderer gives no width,
  fall back to 120 as the page width.
- **Header.** The header literal stays as round 1 left it.
- **Unchanged.** The fixed columns (NAME to TIME) and their widths stay the same.
- **Comment.** Replace the "fill the page's 96-wide grid" comment with one line on *why*: the reason takes whatever
  width the terminal has left after the fixed columns.

If `useTerminalDimensions` can't be called where the fleet rows are built, stop and report. For example, the fleet
isn't a component body, or hook rules forbid calling it there. Don't restructure the page.

### `check_assertion_15` (`scripts/opencode-plugin-smoke.sh`)

Keep its intent: the sidebar follows `needs_you` and `report_round` (r3), not the current round (r4).

- Replace the `r3 · opencode · question in` grep with `r3 · builder on opencode`.
- Replace the negative grep with `! grep -q "r4 · builder on opencode"`.
- Update its comment only if it names the old shape.

### Spec line 8

- **If** that sentence is about `relevo status --line --json`, say that the row document also gains `actor`,
  `status`, `tone` and `reason`.
- **If** it is about `status --json` only (the `BindingStatus` fields at line 91), leave it and add nothing.

## 5. Pseudocode

```
fleet rows:
  pageW   = terminal width - fleet page horizontal padding   (fallback 120)
  reasonW = max(0, pageW - len(ROW_PREFIX) - len(fixed))
  line    = fixed + ellipsize(row.reason or "", reasonW)
```

## 6. Error Handling

There are no new error paths. A missing terminal width falls back to 120.

## 7. Working Efficiently

- Read `tui.tsx` lines 880-1040, `scripts/opencode-plugin-smoke.sh` lines 300-330 and the spec's lines 1-20 in **one**
  step of parallel reads.
- Make one edit per file.
- **Verify** by running `OUT=/tmp/opencode/sl-smoke-out-r2 bash scripts/opencode-plugin-smoke.sh`. It needs 48/48.
  Then check `03-fleet.txt` in that directory: webshop's REASON is readable (not `quest…`) at the capture's width.
- **Full check**, once, at the end: `make check`, then `gofmt -l .`, which must print nothing.

If a step is impossible as written or contradicts the code, **stop and report**.

## 8. Ordered Implementation Steps

**Step 0 — sync.** Commit nothing yet. Run `git fetch origin && git rebase origin/main`. If the rebase conflicts,
stop and report without resolving.

**Step 1 — fleet reason width** (§4).
*Done when:* the smoke's `03-fleet.txt` shows webshop's reason in full, or ellipsized only at the real right edge.

**Step 2 — smoke #15** (§4).
*Done when:* the smoke passes 48/48.

**Step 3 — spec line 8** (§4).

**Step 4 — check and commit.** Run `make check` and `gofmt -l .`. Copy this plan to
`docs/plans/2026-09-26-statusline-actor-dedupe-r2.md`. Make one commit:
`fix(statusline): fleet reason takes the terminal width; smoke follows the new sidebar line`.
*Done when:* `make check` is green, and `git diff --stat HEAD~1` lists only the files in §2.

## Report

Include:

- the diff stat
- the smoke result (N/48)
- the fleet capture's four rows as they render
- whether the spec line changed
