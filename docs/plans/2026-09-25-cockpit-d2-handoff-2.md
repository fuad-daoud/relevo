# Cockpit D2 handoff 2 (2026-09-25, end of session 3)

Read this first. It continues `docs/plans/2026-09-25-cockpit-d2-handoff.md` (session 2), which in turn continues
`docs/plans/2026-09-24-cockpit-handoff.md` (waves 1-2).

Session 3 finished the `:stats` redesign, all five tabs. The next session designs the remaining views.

## 1. How this user works (keep doing this)

- **Design on the canvas first, then build, then let the user see it for real.** One screen at a time.
  - **The canvas** is https://claude.ai/artifact/7W1oBWL1BC5Zym4G3F2JaA. Open it with the Artifact tool's `read`, and follow
    the Design type's rules:
    - publish with `url` + `root` + `file_path` (the absolute path of one changed `project/*.dc.html`) + `files` for the
      others;
    - read the artifact (no path) right before a publish, or it is refused;
    - send `project/canvas.json` only when the layout changes.
  - **Boards** are 132×34 terminal text rendered as `.dc.html` spans. Two ways to make one:
    - **From a built screen** (exact, colours included): capture ANSI with the skill (`tui.sh ansi <session> > x.ansi`), then
      `python3 ~/.cache/relevo-verify/ansi2board.py <existing board .dc.html> <out .dc.html> < x.ansi`. The script turns SGR
      codes into inline `style` spans inside the board's `<div class="term">`, keeping the existing file's head and tail.
      Read the existing board first with the Artifact tool (`read` with `path`); it lands under the scratchpad's
      `artifact-files/`. **Do not import the skill's `ansi2html.py`:** it reads stdin when imported.
    - **For a design not built yet:** a small Python generator that writes `L(segments)` lines. It must refuse any line
      wider than 132.
  - **Use real data only.** Numbers on a board come from `relevo.db` (`sqlite3 -readonly ~/.local/state/relevo/relevo.db`)
    or `relevo history --stats`. **Never invent a figure.** Last session one invented breakdown was caught and replaced
    before it was published.
- **Before showing the user anything, capture it yourself** with the `capturing-tui-screens` skill (repo copy:
  `.claude/skills/capturing-tui-screens/tui.sh`).
  - Run it at 132, and at 100/110 or 200, and look at the PNGs.
  - Test the keys you added: scrolling, the cursor, `enter`.
  - Then send the PNGs with `SendUserFile`, and build `~/.cache/relevo-verify/relevo-try` for the user to run.
- **The user's design rules**, learned the hard way; apply them without being asked:
  - **No captions, subtitles or footnotes.** The header rows name the tables, and titles above tables go too. A column
    whose meaning needs a caption goes into a detail block as a plain sentence instead; MEASURED went that way.
  - **No dollars.** The views are tokens-first.
  - **A 3-cell margin on each side,** and nothing past `width - 3`. Tables fill `[3, width-3)`: the name column takes the
    rest, and the numbers sit against the right edge.
  - **Charts:** faint dotted grid (`┈` at y ticks, `┊` at x ticks), "nice" y ticks, weekly or daily date ticks.
    - The width is constant across windows; only the bars adapt.
    - The height depends on the **screen only**, never on the data or the window. The user caught the layout jumping on
      `w`.
  - **Every table lists everything,** and idle entries are faint rows, never hidden. Where a table does not add up to
    100%, an "other" row closes it: `(no repo)`, `(no feature)`.
  - **Bars are the number:** 10 cells = 100%.
  - **Short human text over raw errors:** `Individual quota reached`, not `RESOURCE_EXHAUSTED (code 429): …`.
  - **Every tab scrolls:**
    - pgup/pgdn/space/home/end;
    - ↑↓ move a cursor where there is a table, and scroll the page where there is not;
    - the page follows the cursor.
  - **The theme:** D2 tokens in `internal/ui/styles.go`. There is one accent, and the only boxes allowed are a card or a
    modal. `gridStyle` is `#2f3542`.
- **Merging:** "merge when ready", **only** once every CI check is green (`gh pr checks <n> --watch`). Squash-merge.

## 2. What landed in session 3

| PR | what |
|---|---|
| #490 | Control bytes in a round's text (VT/FF/NUL… from tool output) no longer scroll the whole screen: `sanitizeText` in `internal/ui/detail.go` `bodyOf`. |
| #500 | `:stats` redesigned, all five tabs; details below. Merged as `9471682`. |

**#500, per tab:**
- **overview:**
  - four tiles;
  - a tokens-per-day chart on a grid;
  - candidates and repos tables with a cursor, every configured candidate listed, idle ones faint.
- **candidates:**
  - every configured candidate: RNDS, DONE, HALTED (the **report** count), MED, TTFT, IN/RND, OUT/RND, CACHE, STATUS;
  - a detail block: roles (via `relevo.CandidateRoles`, from the role registry), bindings, token share, and
    "N of M rounds reported token usage".
- **tokens:**
  - `s` cycles total / candidate / provider / kind;
  - the splits are **stacked full charts** drawn by the overview's renderer (`statsTimelineVals`, `statsGeom`). An
    earlier strip design was rejected: the left column must be the token axis.
  - candidate and provider charts share one scale; each kind has its own.
- **reliability:** tiles; a gates table with short reasons (`statsGateReason`); limits by hour. It fills the width.
- **repos:**
  - repos and features tables with the same columns, `% TOKENS` bars, and `(no repo)` and `(no feature)` rows;
  - a detail block with the top candidates;
  - `enter` opens `repo:` or `feature:` rounds.

**The engine** (`internal/stats`) gained:
- per-day `Kinds`, `ByCandidate` and `TokensByProvider`;
- per-candidate `Bindings`, `ReportHalted` and `Switches`;
- per-group `Bindings`, `Done`, `ReportHalted`, `Commits` and `ByCandidate`;
- `Report.NoFeature`.

The `relevo history --stats` CLI output is unchanged (the user: "no need to touch the cli").

**Canvas:** row "Stats in D2" holds the five tab boards: overview (`StatsD2B`), `StatsTabCand`, `StatsTabSpend`,
`StatsTabRel` and `StatsTabRepos`. All five are converted from the merged build (canvas version 29), so they are exactly what
`main` draws. A binary of `main` at `9471682` is `~/.cache/relevo-verify/relevo-main`.

## 3. Next: design the remaining views

In the order the user left them:
1. **`:rounds`** (the dashboard grid) and **`:log`**. Both are still in the old style.
2. **C1, the config views:** actors, agents, candidates, audit, settings. The canvas rows 1-2 boards ("Actors, agents,
   candidates", "Config: draft, save, audit, settings") predate D2. Redesign them in D2 first, with real data.
3. Then the rest of the original roadmap (the 2026-09-24 handoff §5):
   - A2 round 4 (install custom agents);
   - A3b (the draft engine);
   - A4 (the state rename);
   - A5 (reader rounds, the artifacts tab);
   - C3 (the site).

**Open question for the user:** mouse-wheel scrolling in the cockpit. The cockpit never enables the mouse today. Enabling
it means Shift+drag to select text. If they want it, it is its own small PR.

## 4. Operational notes and lessons

- **Verification (USER RULE): never run `make check` on this laptop.** A PreToolUse hook
  (`~/.claude/hooks/laptop-no-local-servers.py`) refuses it; heavy commands go to contabo. Split `make check` the same way
  it is composed:
  - **On the server:** `dev run sh -c 'go vet ./... && go test -race -count=1 ./...'` from the worktree.
  - **Locally, the git-based steps** (`dev run` copies the tree without `.git`, so these would pass vacuously there):
    - `test -z "$(gofmt -l $(git ls-files '*.go'))"`
    - `go mod tidy -diff`
    - `sh scripts/check-plugin-version.sh`
    - `sh scripts/check-name.sh`
    - `shellcheck scripts/*.sh`
  - **`check-name.sh` bans the old name `relay`** in tracked files, test fixtures included. PR #500 failed CI on it once.
  - For quick iteration only: `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -run '<focus>' -count=1`
    locally.
- **Plans ship with the code:** every round's plan is copied into `docs/plans/` and committed in the same PR (USER RULE).
- **A long feature is one binding and one commit:** every round amends it (`git commit --amend --no-edit`), and the planner
  rebases onto `origin/main` before the PR.
  - **Squash against the merge base, never `origin/main` after a fetch.**
  - Check the scope against the real fork point (`git diff --stat $(git merge-base origin/main HEAD) HEAD`): a stale
    `origin/main` makes other people's commits look like yours.
- **A bug in `main`: read `main`, not your feature worktree.** Last session a plan for "a live round's transcript shows
  raw JSON" was written from the stale stats worktree. `main` had already fixed it (#478), and the builder halted
  correctly. **The user's test binary was built from the feature branch, so its bugs may already be fixed on `main`.**
  Rebase the feature branch often.
- **A new binding** cuts from the main checkout's HEAD, which is old. Fast-forward its worktree to `origin/main` before the
  first send. The main checkout's `git pull` aborts on untracked `docs/plans` files that main now tracks. Use worktrees;
  do not force it.
- **Builders:** do not hand-pick; relevo picks by config order and skips gated providers.
  - At the end of session 3, deepseek (cline-pass) was gated until Sep 26 22:11, gemini (google) and claude-sonnet-4-6
    (agy-extra) were gated, and gpt-5.6-terra was gated until Oct 19. New bindings will likely get **sonnet** or
    **glm-5.3-flash**.
  - Gates are per machine: contabo keeps its own.
- **Builders halt correctly on a wrong premise** ("stop rather than improvise"). Three halts last session were all planner
  errors:
  - a golden-height assumption;
  - a test comparing two already-capped heights;
  - the stale-worktree bug.

  Read the report's arithmetic, fix the plan, and resend a small round.
- **Mutation-test every subtle rule** (CLAUDE.md): break the condition, and a named test must fail. Mutations that do not
  compile prove nothing, so check the build line.
- **Transcripts since #478** are the `NNN-builder.jsonl` stream, rendered by readers. `relevo show <name> --round N --transcript`
  shows one rendered.
- **Personal skill copy:** `~/.claude/skills/capturing-tui-screens` duplicates the repo copy. Delete it once the main
  checkout is on a commit that ships `.claude/skills/`.
