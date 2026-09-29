# Cleanup P1c -- delete the human-only history and serve-status reports

Round of the codebase cleanup (spec: `docs/specs/2026-09-25-codebase-cleanup-design.md`,
§2.2 deletion items 6-9). Humans read these figures in the cockpit (`relevo ui`)
now; the CLI is for agents and keeps only its JSON outputs.

## 0. Rules for this round

- **The deletion list below is closed.** Everything not on it survives. A test
  that asserts surviving behaviour through a deleted mechanism is **ported**,
  not deleted. Every removed test or test row cites its item (D6.x-D9.x).
- If a step is impossible as written, or the code contradicts this plan, **stop
  and report** -- do not improvise.
- Do not refactor, rename or re-comment surviving code beyond what a deletion
  forces. Comments you touch lose issue numbers and history.
- Parallel rounds delete pane/edge and land/review/fork code and add golden
  tests (one of them pins `history --json` with `--binding`, `--planner`,
  `--limit`, `-q`, and `serve status --json`). Those must keep working exactly.
  In `cmd/relevo/main.go` touch only the usage lines named here.
- `cmd/relevo` tests must not spawn a harness or reach the network.

## 1. System overview

`relevo history` keeps its JSON output and its query language; its text
renderers, `--tab` (token/cost table, including the server's `--owner` form)
and `--stats` (text scorecard) go. `relevo serve status` keeps its JSON
document and loses its text rendering. The cockpit's stats view shares
`stats.Build` and the formatters, so those stay.

## 2. File structure

```
EDIT   cmd/relevo/history.go          (items D6-D8)
EDIT   cmd/relevo/serve.go            (serveTab; cmdServeStatus text branch)
EDIT   cmd/relevo/main.go             (history usage lines only, ~lines 80-82)
DELETE cmd/relevo/history_tab_test.go (D6/D7 -- check each test first, port any that pins surviving behaviour)
EDIT   cmd/relevo/history_test.go, serve_test.go (remove/port per item)
EDIT   internal/relevo/tab.go         (delete; move ParseSince, see D6.3)
EDIT   internal/relevo/history.go     (FormatHistory, FormatGroups, HistoryLine)
EDIT   internal/serve/admin.go        (AdminTabEntries, RenderAdminStatus)
EDIT   internal/stats/render.go       (Render and render*; countPairs)
DELETE internal/stats/testdata/report.golden (D7, if only Render's test uses it)
EDIT   tests of the above in internal/relevo, internal/serve, internal/stats
EDIT   README.md
NEW    docs/plans/2026-09-26-cleanup-p1c-del-history.md  (a copy of this plan, last step)
```

## 3. Deletion list (closed)

**D6 -- `history --tab`** (local and server `--owner` forms)
- D6.1 In `cmd/relevo/history.go`: `historyTab` (~358), `renderTabReport` (~433),
  the `--tab`, `--owner`, `--state` flags (~190-193) and their validation
  (`validateHistoryTabStats` ~97, the `--owner`/`--state` checks ~205-250),
  and the routing to `serveTab`.
- D6.2 `serveTab` in `cmd/relevo/serve.go` (~850-~890) and
  `serve.AdminTabEntries` (`internal/serve/admin.go:414-~470`) with their tests.
- D6.3 `internal/relevo/tab.go`: `TabEntry`, `TabEntries`, `TabRow`, `TabRows`,
  `TabReport`, `RenderTab`, and `tab_test.go`'s tests of them. **`ParseSince`
  survives** (the cockpit uses it): move it, unchanged, into
  `internal/relevo/history.go` with its tests, then delete `tab.go`. First grep
  for `TabEntries`/`TabEntry` outside cmd/relevo and internal/serve; if the
  cockpit (`internal/ui`) uses them, stop and report.

**D7 -- `history --stats`**
- D7.1 `historyStats` in `cmd/relevo/history.go` (~383) and the `--stats` flag.
- D7.2 In `internal/stats/render.go`: `Render`, `renderScorecard`, `renderSpend`,
  `renderReliability`, `renderGroups`, `renderOutcomes`, `countPairs`, and their
  tests; `testdata/report.golden` if nothing else reads it. **Survive**: every
  other exported function in render.go that has a caller outside
  `internal/stats` after the deletion (the cockpit uses FitKey, PctText,
  TTFTText, Money, ShortTokens, Duration, MonthDay, and others -- check each
  with grep). An exported formatter left with no caller outside the package and
  no caller inside it is dead: delete it too, and list it.

**D8 -- the text form of `relevo history`**
- D8.1 `FormatHistory`, `FormatGroups`, `HistoryLine`
  (`internal/relevo/history.go:272-~360`) and their tests. `Bindings`,
  `HistoryOptions`, `LoadHistory` survive.
- D8.2 `cmdHistory` always prints JSON (the current `--json` path, byte for
  byte). `--json` stays accepted and does nothing extra.
- D8.3 Remove the per-field filter flags that `-q` already expresses:
  `--repo`, `--feature`, `--harness`, `--provider`, `--model`, `--candidate`,
  `--outcome`, `--until`, `--archived`, `--live`, `--by`, `--rows`. **For each,
  first confirm the query language (`internal/histq`) has an equivalent key or
  operator; if one has none, keep that flag and list it in the report.**
  Keep: `--here`, `--binding`, `--planner`, `--since`, `--limit`, `-q`, `--json`.
  Drop helpers only the removed flags used (for example `validateHistoryFlags`,
  `validateHistoryBy`, `historyAxisNames`, `historyAxis`, `groupJSON` -- but if
  `-q ... by:X` still produces grouped JSON through one of them, keep it).

**D9 -- the text form of `relevo serve status`**
- D9.1 `cmdServeStatus` (`cmd/relevo/serve.go:~720`) always encodes
  `serve.StatusDocument` exactly as its `--json` branch does today; `--json`
  stays accepted.
- D9.2 `serve.RenderAdminStatus` (`internal/serve/admin.go:147-~196`) and its
  tests. `StatusDocument`, `AdminStatus`, `FlatStatus`, `RenderClients`,
  `RenderGates` survive.

**Usage and docs**
- D10.1 `cmd/relevo/main.go` usage lines for `history` (~lines 80-82): one
  line for `history` listing the kept flags; no `--tab` / `--stats` lines.
  The `history` help text inside `cmd/relevo/history.go` (~line 47) likewise.
- D10.2 README: `history --tab`/`--stats` bullets (~332-334), the `--owner`
  mention (~444), the `relevo tab`/`relevo stats` rows of the rename table
  (~493-494), the `serve status` text description (~784: say it prints JSON)
  and the server `history --tab --owner` bullet (~787).

## 4. Interfaces after the round

- `relevo history [--here] [--binding B] [--planner P] [--since D] [--limit N] [-q QUERY] [--json]`
  -> JSON array of the same `historyJSONRow` values as today (or grouped JSON
  when the query groups, exactly as `--json --by` did).
- `relevo serve status [--state DIR] [--json]` -> the `StatusDocument` JSON,
  indented with two spaces, as today's `--json`.

## 5. Pseudocode

```
cmdHistory(args):
    parse kept flags
    rows := load + filter (unchanged)
    print JSON (the existing --json code path)
cmdServeStatus(args):
    owners, builders := AdminStatus(...)
    encode StatusDocument(owners, builders)   // the existing --json path
```

## 6. Error handling

No new errors. A removed flag now fails with the flag package's "flag provided
but not defined" (exit 2), like any unknown flag.

## 7. Working efficiently

- Read `cmd/relevo/history.go` whole, `cmd/relevo/serve.go:700-900`,
  `internal/relevo/{tab,history}.go`, `internal/serve/admin.go:140-200,410-475`,
  `internal/stats/render.go`, and `internal/histq`'s query key list in one batch.
- One grep to confirm callers before each deletion group:
  `grep -rn --include='*.go' -E 'TabEntr|TabRow|TabReport|RenderTab|AdminTabEntries|serveTab|historyTab|historyStats|renderTabReport|stats\.Render\b|FormatHistory|FormatGroups|HistoryLine|RenderAdminStatus|ParseSince' .`
- Each file's edits in one edit call; deletions with one `git rm`.
- Focused loop: `go build ./... && go vet ./cmd/relevo ./internal/relevo ./internal/serve ./internal/stats`,
  then `go test ./cmd/relevo ./internal/relevo ./internal/serve ./internal/stats ./internal/ui -count=1`.
- Full check once at the end: `make check`, then `make e2e`.

## 8. Ordered steps

1. D6 (tab, both forms), with ParseSince moved first. Build passes.
2. D7 (stats text). Build passes; `go test ./internal/ui` passes (the cockpit's
   stats view must be untouched).
3. D8 (history text and flags), checking each flag against histq first.
4. D9 (serve status text).
5. Tests: fix every compile failure; delete (cite item) or port each affected
   test. Before deleting `cmd/relevo/history_tab_test.go`, read each test in it
   and port any that pins behaviour outside D6/D7.
6. D10 usage and README.
7. The §7 grep shows only the survivors (ParseSince). `make check` and
   `make e2e` pass. `git diff --stat` shows only §2 files.
8. **Save the plan.** Copy this plan to
   `docs/plans/2026-09-26-cleanup-p1c-del-history.md` and commit it.

Report: per item, what was removed; each D8.3 flag removed or kept (with the
histq equivalent); every removed or ported test with its item; formatters
deleted as dead under D7.2; non-test and test lines removed.
