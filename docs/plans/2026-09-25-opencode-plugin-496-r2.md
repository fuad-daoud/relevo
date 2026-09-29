# OpenCode plugin #496, round 2: a full-width table cell shifts its row by one column

Base: branch `relevo/oc-496` at `f252cac` (PR #499). Round 1 shipped
`docs/plans/2026-09-25-opencode-plugin-496.md`; read its §4.5 for the table
contract. If any step is impossible as written or contradicts the code you
find, **halt and report**. Do not improvise, and do not bend a test.

## 0. Working efficiently

- Read these once, in one parallel batch:
  - `internal/harness/opencodeplugin/tui.tsx` lines 1036-1112 (`renderTable`
    and its `renderCell`);
  - `scripts/opencode-plugin-smoke.sh` around assertions 32-37 and the final
    `PASSED` count.
- Test with `bash scripts/opencode-plugin-smoke.sh` (about 90 s, needs tmux
  and opencode, both present).
- Run `make check` once at the end. If a hook blocks it, run its steps
  directly and paste each result.
- Never dump the environment into a report or commit.

## 1. The fault

The planner's own smoke run captured this in `05f-ledger-after.txt`:

```
 Step │ Text             │ Status
 ─────┼──────────────────┼───────
 1    │ sleep 120        │ done
 2    │ write the ledger  │ done
```

The cell `**write** the ledger` is 16 visible columns, the column's full
width, so `renderCell` emits its pad node as `<text>` holding `" ".repeat(0)`,
an empty string. OpenTUI draws that empty text node one column wide, which
pushes the rest of the row right by one. Row 1's cell does not fill its
width, so it pads with 7 real spaces and lines up.

Assertion 32's regexes (`+`) accept any run of spaces, so the smoke did not
catch the shift.

## 2. Change

### 2.1 `renderCell` in `tui.tsx` (inside `renderTable`, ~1086-1097)

- When a cell fits its width, emit the pad `<text>` only if
  `width - vis.length > 0`. When nothing is left over, emit
  `renderInline(cell)` alone.
- Nothing else in `renderTable` changes.
- **Check every other place round 1 added for the same pattern:** a `<text>`
  whose content can be the empty string. List what you find in the report.
  - Fix only the ones inside `renderTable` and `relevoLine`.
  - Anything elsewhere, report it and leave it.

### 2.2 Smoke assertion 38 (`scripts/opencode-plugin-smoke.sh`)

Add it after assertion 37, in the same `check_assertion_N` style, and update
the final `PASSED` count to 38.

- In `05f-ledger-after.txt`, the table lines are the lines that contain `│`
  or `┼`: the header, the separator and the two body rows.
- Every one of those lines must have its first `│` or `┼` at the same column
  and its second at the same column.
- Compare character positions, not bytes: `│` and `┼` are multi-byte. Use
  `awk` with a UTF-8 locale, or `python3`, and pick one.
- The check must fail on the capture quoted in §1.
- Prove this: save that four-line block to a temp file, run the check's core
  against it, and paste the failure into the report.

Assertions 1-37 must pass unchanged.

## 3. Mutation

- **M9:** put back the unconditional pad node. Assertion 38 must fail;
  assertions 32-37 may still pass.
- Restore it and report the result.

## 4. Ship

1. Save this plan verbatim at
   `docs/plans/2026-09-25-opencode-plugin-496-r2.md`.
2. Make one new commit on the branch. Do not amend `f252cac`. Message:
   `fix(opencode): a table cell that fills its column no longer shifts the
   row (#496)`.
3. Push with `git push origin relevo/oc-496`.
4. Add a PR comment on #499 with the smoke result (38/38), M9's result and
   the `make check` result.

## 5. Scope

`git diff --stat f252cac` shows only `tui.tsx`,
`scripts/opencode-plugin-smoke.sh` and this plan. If the fix needs another
file, halt and report.
