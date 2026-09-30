# Cockpit D2 handoff 3 (2026-09-25, end of session 4)

Read this first. It continues `docs/plans/2026-09-25-cockpit-d2-handoff-2.md` (session 3). That file's §1 (how
this user works, the design rules) and §4 (operations) still hold in full; read them too. This file only adds
what session 4 changed.

## 1. What landed in session 4

| PR | what |
|---|---|
| #504 | `:rounds` and `:log` redesigned in D2. Merged as `290b1f4` (squash; 5 builder rounds on one binding, `ck-d2-rounds`, now done and its worktree released). |

**`:rounds`** (`internal/ui/dash`, hosted by `internal/ui/view_rounds.go`):
- **Context row:** the summary (rounds, tokens, done, halted, blocked, exited, running, open, median). It drops
  items from the end when narrow.
- **Right of the context row:** `sort newest`, or an axis chip when grouped.
- **Day rules:** `today ┈┈┈ 103 rounds · 432M`, shown only when sorted by started. The cursor skips them.
- **Columns:** STARTED (HH:MM), BINDING, RND, CANDIDATE, REPO, OUTCOME, COMMITS, TREE, TOKENS, TOOK. The drop
  order when narrow is REPO, then TREE+COMMITS, then TOKENS.
- **OUTCOME** is a word: done, halted, blocked, exited, switched, `no outcome`, `no report`, `running` or `open`.
  - `running` means the live status shows that binding working on that round (`runningRounds`).
  - Anything else still open reads `open`: stale rounds.
- **Group table (`b`):** RNDS, BINDINGS, DONE, HALTED, COMMITS, TOKENS, `% TOKENS`, LAST. The none row
  (`(no repo)`/`(no feature)`/`(none)`) is always last. `enter` expands.
- **Tokens-first:** no cost anywhere, and the sort keys are started/tokens/duration/commits and
  tokens/rounds/halted/last.
- **`dash.ShortRepo`**, which stats' `shortRepo` now delegates to, strips `.git` and handles scp URLs.
- The cursor band spans the whole row: a per-cell background. Wrapping styled cells in one style does not work,
  because each cell's reset ends it.

**`:log`** (`internal/ui/view_log.go`):
- It is a persistent event log over the last 7 days, from four sources:
  - `db.RecentEvents`, for every binding's events;
  - `relevo.LoadHistory`, for gates;
  - `db.Revisions`, for config saves;
  - the session's cockpit actions (`Env.ActionLog`, rows `you`, session-only).
- **Folding:** plan+pick+drift become one `sent` row, and report+diff one `done`/`halted` row.
- Exits, switches, gates, questions and answers each get their own row.
- **Keys:** it refreshes every 5s and follows the newest entry; `down` pauses, `f` follows, `/` filters and
  `enter` opens the round.
- `statsGateReason` strips `Error 429:`-style prefixes. This also applies on the reliability tab.
- The user approved both screens ("the rounds looks good", "the logs good").

**Canvas:** row "Rounds and log in D2" (y=8680) holds `RoundsD2`, `RoundsByD2` and `LogD2`. They are the approved
DESIGN boards, generated from real data by `scratchpad/gen_rounds_log.py` (not kept). They are not converted
from the build: the build differs slightly, with `1.9B`/`153.6k` token format, a `6 open` item, and a right-aligned
`% TOKENS` header (round 5, after the user spotted the misalignment: the header was left-aligned while the
percentages sit at the cell's right end; the stats repos tables were fixed the same way). To make them exact, convert real captures with `~/.cache/relevo-verify/ansi2board.py` as
handoff-2 §1 describes.

## 2. Decisions the user made this session

- `:rounds`: a day-grouped list, not tiles. The day rules carry both the name and the sums.
- `:log`: persistent, with a row for every cockpit action you take.
- **Mouse-wheel scrolling: YES, as its own small PR, after this work.** The cockpit never enables the mouse
  today. Enabling it means Shift+drag to select text; say so in the PR.

## 3. Next

In this order:
1. **The mouse-wheel PR:** `tea.WithMouseCellMotion` or `WithMouseAllMotion` at program start. The wheel maps
   to ↑↓ (or a page) in every view that scrolls: fleet, rounds, log, stats, round detail. Small, one binding.
2. **C1, the config views** (actors, agents, candidates, audit, settings). The canvas rows 1-2 boards predate D2.
   Redesign them in D2 on the canvas first, with real data from `relevo.db` / `relevo config`, one screen at a
   time, and get approval before building.
3. **The rest of the roadmap** (2026-09-24 handoff §5):
   - A2 round 4 (install custom agents);
   - A3b (the draft engine);
   - A4 (the state rename);
   - A5 (reader rounds, the artifacts tab);
   - C3 (the site).

## 4. Operational notes added this session

- **Plans:**
  - Write the plan in the main checkout's `docs/plans/` (untracked there).
  - The builder copies it into the worktree's `docs/plans/` as its last step, and commits or amends.
  - One binding, one commit, amended every round: `ck-d2-rounds` had 5 rounds.
- **Polish rounds pay off:** capture the real screen after every round. Round 1's goldens passed, but the real
  screen showed:
  - a band on one cell only;
  - a missing blank row;
  - a summary overflowing at 100 columns;
  - italic on archived rows.
- **Builder:** relevo picked gemini-3.8-flash-high (agy/google) for every round. There were no halts, and each
  round was 6-18 minutes.
- **Gates at the end of the session:**
  - deepseek (cline-pass) until Sep 26 22:11;
  - claude-sonnet-4-6 (agy-extra) until Sep 26 18:03;
  - gpt-5.6-terra (openai) until Oct 19.
- **Verification for #504:**
  - `dev run sh -c 'go vet ./... && go test -race -count=1 ./...'` from the worktree: all packages ok.
  - Locally: gofmt, `go mod tidy -diff`, `check-plugin-version.sh`, `check-name.sh` and shellcheck, all ok.
- **Binaries:**
  - `~/.cache/relevo-verify/relevo-main` is `main` at `290b1f4`, and `relevo-try` is the same code.
  - I built it from a temp worktree because the main checkout's HEAD is still `9471682`: `git pull` there aborts
    on the untracked `docs/plans` files that main now tracks. Use worktrees, and do not force the pull.
- **Unfinished-looking things that are fine:**
  - The `:log` breadcrumb reads `fleet › log`, because `:log` pushes over fleet, as before.
  - A long `halted_at` text is clipped with `…`.
