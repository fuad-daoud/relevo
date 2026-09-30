# Cockpit D2 handoff (2026-09-25, end of session 2)

Read this first. It picks up the cockpit redesign where the previous planner session ran out of context. Earlier
context is in `docs/plans/2026-09-24-cockpit-handoff.md` (waves 1-2) and `docs/plans/2026-09-24-cockpit-review.md`
(the review).

## 1. How this user works (keep doing this)

- **Design on the canvas first, one screen at a time.** Canvas: https://claude.ai/artifact/7W1oBWL1BC5Zym4G3F2JaA. Open
  it with the Artifact tool's read action. The Design type's rules: re-read `project/canvas.json` right before any publish that
  changes it, and publish only changed files.
  - The board generators are in the previous session's scratchpad (`gen_*.py`), which is gone. Rebuild what you need;
    boards are 132×34 terminal text rendered as `.dc.html` spans.
- **Then build it, and let the user see it for real before moving on.** The user reviews real screenshots and runs the
  binary (`~/.cache/relevo-verify/relevo-<x>`).
- **Stats are done tab by tab:** build a tab, the user reviews it, then the next tab.
- **Before showing the user anything, capture it yourself** with the `capturing-tui-screens` skill
  (`.claude/skills/capturing-tui-screens/`, shipped in #460). Run it at 132, 100 and 200 columns and look at the PNGs. It
  catches most defects before the user does.
- **Merging:** "merge when ready", but only when every check is green (CLAUDE.md). Squash-merge.
- **Theme D2 tokens** are in `internal/ui/styles.go`. The only box allowed is a card or a modal, and there is one accent.
  The user dislikes clutter and wants breathing room.

## 2. What landed this session (all on main)

| PR | what |
|---|---|
| #449 | D2 theme; `:fleet` grouped by state (needs you / working / idle / on hold / done folded with `.`); card on top; version in the header, no gates in the header |
| #450 | the feat-drift rule removed from `check-plugin-version.sh` (user's call) |
| #460 | round detail: calm two-line header, pill tabs, markdown plans and reports; a blank row under the title on every view; fixed the "loading…" forever race (`push`/`rootThen` run a view's init after the stack change); the skill |
| #468 | every overlay is a boxed modal over the dimmed screen (`compose.go`): confirms, `:` (sections, live-first), `?` (3 columns), the gate form, the send picker, the retry list, the bind prompts |

Issues filed: #454 (racy SnapshotTree, since fixed by someone in #461/#462).

## 3. In flight: stats, tab 1 (overview), round 3 due

- **Binding** `ck-d2-stats`. Worktree `~/.local/state/relevo/.worktrees/ck-d2-stats`, branch `relevo/ck-d2-stats`, **one commit
  `5a8e522d`** on origin/main e2218b3f. Not pushed, no PR.
- **Built:**
  - `internal/stats` gained `TokenCounts` (In, Cache, Write, Out, Measured) on Totals, ScoreRow, DayCost and GroupRow.
    The CLI text output is unchanged.
  - `:stats` has a tab bar (`overview candidates tokens reliability repos`) on the context row, with window chips on the right.
  - The overview has four full-width tiles, a tokens/day chart, and candidates and busiest-repos tables with aligned header rows.
  - The other four tabs temporarily show the old panels.
- **Plans:** `docs/plans/2026-09-25-cockpit-d2-stats-overview.md` and `-r2.md` (in the main checkout, untracked; copy them into
  the branch before the PR).

**The user's feedback on overview round 2, still to do as round 3:**
1. **Side padding.** Leave 1-3 cells of room on each side instead of running to the edge: right-align the tables at
   `width - 3`, and apply the same to the tiles and the chart. "Much cleaner and readable."
2. **A timeline with numbers.**
   - The x axis shows only the first and last date. Add intermediate date ticks, about every 5-7 days, or weekly.
   - The y axis shows only the max and `0`. Add 2-3 intermediate value ticks.
3. **A grid, like the relevo-site style.** The user wants the chart drawn "as a grid, just like a grid style in
   `~/projects/relevo-site`". **Look at that repo's chart/grid CSS first** (not done yet) and mimic it with faint dotted
   gridlines at the ticks.
4. **Candidates: list all, scrollable.** Drop "top by rounds, 5+ rounds". List every candidate, with fewer-than-5 rows dimmed,
   and make the table scrollable (↑↓). The same probably applies to repos.
5. **The SHARE bar touches the TOKENS column** (`1.5B▇▇▇▇ 89%`). Add a 2-cell gap, left-align the bar, and right-align the pct.
6. **Answered:** does stats count the planner's own tokens (this chat)? **No.** A round's tokens come from the usage on the
   builder's report entry (`internal/ingest/facts.go:58-70`): the builder harness's session only. Planner sessions are never
   measured by relevo. The user may want planner usage later; that would need a new data source, so ask before planning it.

**After overview is approved:** the candidates tab, then tokens, then reliability, then repos. Each tab has a design board in the
canvas row "Stats in D2" (tokens-first versions). Then open the stats PR. Rebase onto main first, and squash against the real
base (see §5).

## 4. The remaining roadmap

- `:rounds` (the dashboard grid) and `:log`, which are still in the old style.
- Then the original roadmap from the 2026-09-24 handoff §5:
  - A2 round 4 (install custom agents);
  - A3b (the draft engine);
  - C1 (config views: actors, agents, candidates, audit, settings; boards exist in canvas rows 1-2 but predate D2, so redesign
    them in D2 first);
  - A4 (the state rename);
  - A5 (reader rounds, the artifacts tab);
  - C3 (the site).

## 5. Operational notes and lessons

- **Verify** with focused tests, `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ ./internal/stats/ ./cmd/relevo/
  -count=1`, plus gofmt and vet. `make check` is hook-blocked on this laptop.
- **Mutation-test** each subtle fix: break the condition and name the test that fails.
- **Squashing:** `git reset --soft <merge-base>`, never `origin/main` after a fetch. Main moves, and a soft reset onto a newer
  main silently reverts what landed in between. It happened once this session and was caught by `git diff --stat
  origin/main HEAD`.
- **`relevo wait` exit 2** is the report-tail parser (#438). The report itself is fine, so read `NNN-report.md`.
- **Gemini** gets rate-limited. The daemon switches builders to deepseek on its own. Don't hand-pick builders.
- **A new binding** cuts from the main checkout's HEAD, which is old. Fast-forward the worktree to origin/main (or to the branch
  you stack on) before sending.
- **cmd/relevo `TestAddBranchDerivesName`** failed once only in a builder's environment. CLAUDE.md now says TestMain unsets the
  harness variables (#463).
- **Personal copy of the skill:** `~/.claude/skills/capturing-tui-screens` duplicates the repo copy. Remove it once the main
  checkout is on a commit that has `.claude/skills/` (after `git pull`).
