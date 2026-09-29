# NEEDS YOU dialog: the hint line says who and why (2026-09-26)

## 1. System Overview

The OpenCode plugin opens a NEEDS YOU picker (`showNeedsYouDialog`, `internal/harness/opencodeplugin/tui.tsx`,
~lines 536-560). Its title and its filter-box placeholder repeat the binding name and round. The placeholder still
carries the old `waiting` phase, so the status appears twice:

```
webshop · r3 · NEEDS YOU          ← title  (stateWord → row.status)
webshop · r3 · question in        ← placeholder (row.waiting)
```

PR #549 moved every other surface to one status plus an identity line (`<actor> on <harness>`) with an optional
reason. This round moves the placeholder to match:

```
webshop · r3 · NEEDS YOU
builder on opencode · question in
```

**Unchanged:**
- The title.
- The "Tell the planner…" chat label, `[relevo · ${name} r${round} · ${waiting}]` (~line 570). The planner reads it
  with no status column beside it.
- The notifications.
- Every other `row.waiting` use.

## 2. File Structure

```
internal/harness/opencodeplugin/tui.tsx   showNeedsYouDialog: the placeholder line only
scripts/opencode-plugin-smoke.sh          + assertion 49 (dialog hint); final count 48 → 49
docs/plans/2026-09-26-needs-you-dialog-hint.md   this plan, copied in last
```

Nothing else changes. If any other file must change, stop and report.

## 3. Data Structures

None change. The dialog reads existing row fields: `actor`, `harness`, `status` and `reason` (from
`relevo status --line --json`), with `rowActor(row)` for the actor.

## 4. Contracts

### Placeholder (`showNeedsYouDialog`, the `const placeholder = …` line, ~line 549)

- **Identity:** `rowActor(row) + " on " + (row.harness || "opencode")`.
- **Reason:**
  - When `typeof row.status === "string"`, the document is new, so use `row.reason`. It may be `""`. In that case
    add nothing, even if `waiting` is set.
  - Otherwise the binary is older and has no `reason`, so use `row.waiting`.
- **Placeholder:** the identity, plus `" · " + reason` when the reason is non-empty.
- **Leave as they are:**
  - The `waiting` local (~line 546), which the "Tell the planner…" label still uses.
  - The title line.
  - The options.

Add a one-line comment on *why*: the title already carries the name, the round and the status, so the hint says who
is working and why it needs you.

### Smoke assertion 49 (`scripts/opencode-plugin-smoke.sh`)

Add it after assertion 48 (~line 661), in the same style as `check_assertion_44` (~lines 620-626).

- **Check that `$OUT/06-dialog.txt`:**
  - contains `builder on opencode · question in`, and
  - does **not** contain `webshop · r3 · question in`.

  The fixture webshop row in `status-1.json` has `actor: "builder"`, `harness: "opencode"` and
  `reason: "question in"`.
- **Title:** `"06-dialog hint names the actor and reason, not the name and round again"`.
- **Final message:** change `(all 48 assertions passed)` (~line 673) to 49.

Run the smoke test. If `06-dialog.txt` shows that the placeholder is not rendered at all (for example, the host hides
it once the list has focus), stop and report with the capture. Don't weaken the assertion.

## 5. Pseudocode

```
showNeedsYouDialog(row):
  ... existing name, round, waiting, candidate, sw, title ...
  who    = rowActor(row) + " on " + (row.harness or "opencode")
  reason = row.reason if typeof row.status == "string" else row.waiting
  placeholder = who + (reason ? " · " + reason : "")
```

## 6. Error Handling

There are no new paths. Missing fields fall back as described in §4.

## 7. Working Efficiently

- Read `tui.tsx` lines 330-380 (for `rowActor` and `stateWord`) and 530-580, and the smoke script's lines 170-190 and
  600-680. Do it in **one** step of parallel reads.
- Make one edit per file.
- **Verify:** run `OUT=/tmp/opencode/nyd-smoke-out bash scripts/opencode-plugin-smoke.sh`. It must end with 49/49.
  Quote `06-dialog.txt`'s title and hint lines in the report.
- **Full check**, once, at the end: `make check`.

If a step is impossible as written or contradicts the code, **stop and report**.

## 8. Ordered Implementation Steps

**Step 0 — sync.** This binding's branch `relevo/statusline-status` was already squash-merged into main as PR #549.
1. Run `git status` and confirm the tree is clean. If it isn't, stop and report.
2. Run `git fetch origin && git reset --hard origin/main`.
3. Confirm that `grep -n "rowActor" internal/harness/opencodeplugin/tui.tsx` finds the helper. If it doesn't, stop
   and report.

**Step 1 — placeholder** (§4).

**Step 2 — smoke assertion 49** (§4).
*Done when:* the smoke passes 49/49.

**Step 3 — check and commit.** Run `make check`. Copy this plan to `docs/plans/2026-09-26-needs-you-dialog-hint.md`.
Make one commit: `fix(opencode): NEEDS YOU dialog hint names the actor and reason instead of repeating the status`.
*Done when:* `make check` is green, and `git diff --stat origin/main` lists only the files in §2.

## Report

Include:
- the diff stat
- the smoke result (N/49)
- the title and hint lines from `06-dialog.txt`, verbatim
