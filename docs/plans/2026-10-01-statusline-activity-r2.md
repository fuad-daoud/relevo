# Plan: #822 round 2 — the fleet carries the live diff, and the sidebar shows the statusline's own row

Round 1 is on `relevo/status-activity` (three commits, rebased onto `origin/main` `f3697c24`; do not rebase or amend them). Round 2 folds in the two parity gaps: the cockpit fleet page hides the live diff, and the OpenCode sidebar composes its own two lines instead of printing the statusline's row. New commits only; end with `make check` green. Line references are as of the current HEAD; if a step contradicts the code, halt and report.

## Behaviour

1. **`StatusLineRow.Text`** (`json:"text,omitempty"`, at the struct's end): the row exactly as `relevo status --line` renders it, without SGR codes and without the trailing newline, laid out at the renderer's default width (pass `columns <= 0`; the renderer's 80). Filled only by the `--line --json` path in `cmd/relevo/status.go`; the text mode's bytes are unchanged. This is the OpenCode sidebar's row, so no plugin code composes a row.
2. **One renderer.** `internal/view` gains the plain path beside the coloured one, sharing the column widths, padding, `truncate` and the unpadded short-row fallback: `RenderStatusLine` and the new plain line builder must never disagree about the visible text. A test strips SGR from `RenderStatusLine`'s lines and compares them, line by line, with the plain output for every existing fixture (including a chain row and the 40-column case).
3. **Sidebar.** `internal/harness/opencodeplugin/tui.tsx` renders one row per binding: `row.text`, wrapped by the 37-column sidebar, coloured by the row's tone (`needs` → warning + bold, `report` → info, else base). Header, footer, stale line and the click-to-open handler stay. When `row.text` is absent (an older `relevo` on `PATH`), the row falls back to today's two-line composition, unchanged.
4. **Fleet.** The fleet page gains a `DIFF` column between `TIME` and `REASON`: `+A/-R in F`, ` (shared)` when the tree is shared, ellipsized and padded like its neighbours; `reasonPad` shrinks by the new column's width and the header grows the same word.
5. **Docs.** `docs/specs/2026-10-01-statusline-activity-design.md` §3 gains the `text` field, §4 says the sidebar prints it and the fleet shows the diff. README's statusline section gains one sentence for `text`.
6. **Golden.** `cmd/relevo/testdata/contract/statusline-json.golden` gains a `"text"` on every row — the only golden that changes. Regenerate it with `go test ./cmd/relevo/ -run TestContractStatusLine -count=1 -update`; `statusline.golden` (the text mode) must stay byte-identical.

## Seams

| File | Change |
|---|---|
| `internal/view/statusline.go` | `StatusLineRow.Text`; extract the plain row builder from `renderedStatusLineRow` so both paths share it; `RenderStatusLine`'s bytes unchanged |
| `internal/view/statusline_test.go` | plain == coloured-with-SGR-stripped, every fixture; a short-width case |
| `cmd/relevo/status.go` | the JSON path fills `Text` per row (pass `columns <= 0`) |
| `cmd/relevo/testdata/contract/statusline-json.golden` | regenerated: `text` on both rows |
| `internal/harness/opencodeplugin/tui.tsx` | sidebar prints `row.text` (with the old two-line fallback); fleet header + `DIFF` column |
| `docs/specs/2026-10-01-statusline-activity-design.md` | §3, §4 |
| `README.md` | the `text` field |
| `docs/plans/2026-10-01-statusline-activity-r2.md` | this plan, committed verbatim |

No other file. Nothing is deleted: the two-line composition survives as the fallback, `tone`/`status`/`reason`/`tokens`/`clock`/`live` stay in the document.

## Steps

1. Floor: `go test ./internal/view/ ./cmd/relevo/ -run 'Statusline|ContractStatus' -count=1`.
2. Share the renderer: add `Text` and the plain line builder; add the equality test. Check: `go test ./internal/view/ -run 'Statusline' -count=1`.
3. Fill `Text` in the JSON path; regenerate `statusline-json.golden`; check `git diff --name-only` names only that golden. Check: `go test ./cmd/relevo/ -run 'Statusline|Contract' -count=1`.
4. Plugin: sidebar `row.text` + fallback; fleet `DIFF` column. Verify both expressions with `node -e` (a `text` of 80 cells for the sidebar; a `live` object for the fleet), and confirm `git diff` touches no other plugin expression.
5. Commits: 1) view + cmd + golden + tests; 2) plugin; 3) spec + README + this plan.
6. `make check` once; no coverage baseline may be lowered.

## Report must include

- The regenerated `statusline-json.golden` rows verbatim (both `text` values).
- The sidebar row string and the fleet line string from the `node -e` checks.
- `git diff --stat`, commits, commands run.
- Confirmation that `statusline.golden` and every `internal/ui/testdata/*` golden are unchanged.
- Every deviation.

## Halt

- Any golden other than `statusline-json.golden` changes.
- The plain and the coloured renderer disagree on any fixture.
