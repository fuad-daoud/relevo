# Plan: fold remote-less scratch repos into one row in the stats Repos tab


## Behaviour

A **scratch repo** is a repo whose `repo.origin_url` is NULL, so `db.RoundRow.Repo`
(`internal/db/read.go:193`, `firstValid(origin, commonDir)`) carries its `common_dir`.
`git.Client.RepoFacts` (`internal/git/repo.go:11-43`) returns `commonDir` as an absolute,
symlink-resolved path, and `git.NormalizeOriginURL` (`internal/git/repo.go:75-131`) turns
every remote form into `https://host/owner/repo` (or keeps `http://`), returning anything
else trimmed and unchanged. **Predicate: a repo key is scratch when it starts with `/`.**
Not "has no `://`": the existing stats fixtures key repos `"A"`/`"B"`
(`internal/stats/stats_test.go:359-465`) and a scheme test would fold them, rewriting a
dozen green tests for nothing. Accepted edge, no special case: an origin that is itself a
local path (`origin = /srv/git/x.git`) normalises unchanged and counts as scratch — it has
no remote host. `"(none)"` (nil `Repo`) does not start with `/`, so it is excluded by the
predicate itself, not by a branch. A real key can never equal the reserved `(scratch)`:
both a URL and an absolute path differ from it.

`stats.Build` emits, in `Report.Repos`, **one** `RepoRow` keyed `(scratch)` whose
`GroupRow` aggregates every scratch round exactly as `groupRows` (`groups.go:112-141`)
already does — `Bindings` distinct over `BindingID`, `Landed` distinct over landed
bindings, tokens summed — plus a new `Scratch []RepoRow` holding the individual scratch
repos, each a full `RepoRow` over only its own rows, in `groupRows` order (rounds desc,
key asc). The fold row's own `Features`/`NoFeature` stay zero: a cross-repo feature
aggregate is not a thing anyone may render. The fold row is built only when at least one
scratch row exists (a `groupRows` group appears only when a row maps to it), and it sorts
among the other repos by the existing rules — no special casing anywhere.

Repos tab: the fold reads `scratch` (via `shortRepo`, like `(no repo)`), is collapsed by
default, expands on the same key as any repo to its repos at depth 1 ordered by tokens
desc. Those children are leaf rows. Selecting a child shows the normal repo detail block;
`enter` on a child pushes `:rounds` filtered to that child's own key. `enter` on the fold
row notices instead — `repo:"(scratch)"` matches no row. Overview BUSIEST REPOS shows the
fold row, since it reads the same `rep.Repos`; `enter` there notices the same way.

## Consumers of `Report.Repos` and of a repo key — each with the folded row

| Seam | Behaviour with the fold |
|---|---|
| `internal/stats/stats.go:122` | the producer; its field comment at `:45` gains one clause naming the fold |
| `internal/ui/view_stats.go:1432-1438` `overviewRepoRows` | unchanged: copy + tokens-desc sort; the fold participates with its aggregate tokens |
| `internal/ui/view_stats.go:1556-1597` `statsOverviewRepos` | unchanged: labels through `shortRepo`, so it reads `scratch` |
| `internal/ui/view_stats.go:2633-2645` `repoTabRows` | unchanged: emits the fold row, and its children when `expanded["(scratch)"]` |
| `internal/ui/view_stats.go:537-545` `repoHasChildren` | unchanged: finds the fold by key; true once `repoChildren` branches |
| `internal/ui/view_stats.go:462-477` `reposSelected`, `:479-487` `repoRowIndex`, `:547-558` `collapseRepo`, `:454-460` `panelRows`, `:560-575` `moveCursor` | unchanged: a child is recognised by `parent != ""`, so space on a child collapses the fold and lands the cursor on the fold row — the existing child convention, and why a child cannot expand further |
| `internal/ui/view_stats.go:2799-2812` `reposTabLines` depth | unchanged: `parent != ""` and `feature == ""` give depth 1, the two-cell indent |
| `internal/ui/view_stats.go:2752-2779` `statsRepoRow` | unchanged: a child is `kindRepo`, so its name is `shortRepo(path)` → `dash.ShortRepo` strips `/.git` and shows the last two segments |
| `internal/ui/view_stats.go:624-656` `enter`, `:677-702` `enterOverview` | **changes**: the fold key notices; a child falls to the default branch and filters on its own key |
| `internal/ui/view_stats.go:658-675` `enterRepoChild` | unchanged: only `kindFeature`/`kindTicket` reach it, never a scratch child |
| `internal/ui/view_stats.go:2818-2874` `statsGroupDetail` | **changes**: the key echo at `:2834-2841` skips the reserved fold key as it skips `(none)`; a child keeps its echo, since its full path is the useful half |
| `internal/ui/view_stats.go:524-535` `expandRepo` notice | unchanged and unreachable for the fold: the row exists only when it has children |
| `internal/db/read.go:67`, `:340` | unchanged: `(repo.origin_url = ? OR repo.common_dir = ?)` already matches a child's path key, so `repo:"/path/.git"` filters |
| `internal/relevo/statsinputs.go:17` | unchanged: assembles `Inputs`, never reads `Repos` |
| `internal/ui/dash`, `internal/relevo/history.go` | unchanged: both build their own repo axis from rows; neither reads `Report.Repos` |

**JSON: no shape changes.** `stats.Report` and `RepoRow` carry no `json` tags and nothing
marshals them; the only `--json` surfaces are `board` and `bugreport`, which never touch
`Repos`. `relevo history --stats` renders text from the same `Inputs`. `Report.Repos` has
exactly two non-test readers, both in `internal/ui/view_stats.go` (`:539`, `:1435`).

## Ordered steps

1. **stats keys** — add `ScratchKey = "(scratch)"`, `isScratchRepoKey` (the `/` predicate,
   with a why-comment citing `RepoFacts`' absolute common_dir and `NormalizeOriginURL`'s
   scheme), `foldRepoKey` (repoKey with a scratch key mapped to `ScratchKey`), and the
   `Scratch []RepoRow` field on `RepoRow` (`groups.go:26-30`);
   `go test ./internal/stats -run TestGroupRows -count=1` still green — nothing folds yet.
2. **stats refactor** — extract `groups.go:160-185` into `repoRow(in, g, own) RepoRow` and
   call it from `buildRepos`; same command green, `git diff` shows no behaviour move.
3. **stats fold** — `buildRepos` (`groups.go:148-187`) groups by `foldRepoKey`, keeps
   `byRepo` keyed by the unfolded key, and on the `ScratchKey` group builds the fold row:
   the aggregate `GroupRow` plus `Scratch` children from `repoRow` over each path's own
   rows; update the `Report.Repos` comment (`stats.go:45`); `go build ./...` and the same
   test command green.
4. **stats tests** — six cases in `repoScopedRowCases()` (`stats_test.go:353+`) with their
   `check*` funcs beside the existing ones, using `stStr`/`stI64`/`stInt` and absolute-path
   keys: `scratch repos fold into one row` (one `(scratch)` row, the paths gone from
   `Repos`, `Scratch` holding them), `scratch fold totals equal the sum of its repos`
   (rounds, tokens, halted, commits, done, and `Bindings` distinct across two children
   sharing a binding), `a remote repo keeps its own row beside the fold` (its key,
   features, tickets and `(none)` numbers unchanged), `no scratch repo builds no fold row`
   (no `(scratch)` key, every `Scratch` nil), `a round with no repo is not scratch`
   (`(none)` keeps only the repo-less round), `scratch children keep their own feature
   buckets` (each child counts only its own rows; the fold's `Features`/`NoFeature` zero);
   `go test ./internal/stats -run TestGroupRows -count=1` green, and each new case fails
   with step 3 reverted.
5. **ui names** — `shortRepo` (`view_stats.go:1651-1658`) maps `(scratch)` to `scratch`,
   and `statsGroupDetail`'s key echo (`:2834-2841`) skips it;
   `go test ./internal/ui -run 'TestStatsReposTabDetail|TestStatsOverview' -count=1` green.
6. **ui children** — new `scratchRepoChildren(r) []repoTabRow` (one `kindRepo` row per
   `reposByTokens(r.Scratch, …)`, `parent` = the fold key) and a first-line branch in
   `repoChildren` (`:2658-2680`) that returns it before the feature logic;
   `go test ./internal/ui -run 'TestStatsRepos' -count=1` green.
7. **ui enter** — the fold key notices `scratch repos cannot be filtered as one` in `enter`
   (`:651-655`) and `enterOverview` (`:697-701`); same command green.
8. **ui tests** — a `statsScratchReport()` helper in `view_stats_test.go` built through
   `stats.Build` over rows with absolute-path `Repo` keys (so the fold is exercised end to
   end, and a stats regression fails these too), plus `TestStatsScratchRowCollapsed` (one
   `scratch` row, no child path in the body, the fold's numbers),
   `TestStatsScratchRowExpandsToItsRepos` (children at depth 1, two-cell indent, tokens
   desc, each `parent` the fold key), `TestStatsScratchChildIsALeaf` (space on a child
   clears `expanded[ScratchKey]`, lands the cursor on the fold row, and the child names
   leave the body), `TestStatsScratchChildDetail` (the child's short name and full path on
   detail line 1, its own totals), `TestStatsScratchRowEnterNotices` (fold notices on both
   the repos tab and the overview; a child pushes a `roundsView` whose
   `rv.dash.QueryText()` is `repo:"<child path>" since:30d`, the pattern at
   `view_stats_test.go:256-291`), `TestStatsScratchOverviewRow` (the overview's BUSIEST
   REPOS shows `scratch` at its tokens rank);
   `go test ./internal/ui -run 'TestStatsScratch' -count=1` green.
9. **goldens** — two cases in `TestGoldenViews`' list (`golden_test.go:1324`, beside
   `stats-repos-expanded-132` at `:1368`): `stats-repos-scratch-132` (132×34,
   `goldenStatsModel(…, statsScratchReport())` then `statsKey('5')`) and
   `stats-repos-scratch-expanded-132` (same, then space on the fold row and down onto a
   child); write them with the repo's mechanism `go test ./internal/ui -run
   TestGoldenViews -update` (the `-update` flag, `golden_test.go:29`) and confirm
   `git status --short internal/ui/testdata` lists **exactly** those two new files — every
   existing stats fixture keys its repos with URLs or `(none)`, so no committed golden may
   change.
10. **plan file** — save this plan verbatim at `docs/plans/2026-10-09-scratch-repos-fold.md`.
11. **mutation** — run the mutation below and record the failing test names.
12. **gate** — `make check` once, green; land the work as **new commits** on the round's
    branch (no amend, no rebase of anything already pushed), the plan file in the same
    round's commits.

Focused commands while iterating: `go test ./internal/stats -run TestGroupRows -count=1`
and `go test ./internal/ui -run 'TestStatsScratch' -count=1`. Full gate once at the end:
`make check`.

## Deleted behaviour (closed list)

1. The individual per-scratch-repo rows in `stats.Report.Repos` — they move into the fold
   row's `Scratch`.
2. The Repos tab's one visible row per scratch repo — now one `scratch` row, its repos on
   demand.
3. The overview BUSIEST REPOS table's individual scratch entries — now one `scratch` entry.
4. `enter` from a top-level scratch repo row — reachable only as a child of the fold, where
   the filter behaviour is preserved; `enter` on the fold row itself notices.
5. Nothing else: no DB or query change, no golden deleted or rewritten, `internal/ui/dash`
   untouched, no `check-filesize.allow` or `check-comments.allow` entry added or removed.

## Mutation for the MasterMind

- Primary: in `internal/stats/groups.go`, make `foldRepoKey` return the row's own key for a
  scratch repo (drop the mapping to `ScratchKey`).
  `TestGroupRows/scratch_repos_fold_into_one_row` and
  `TestGroupRows/scratch_fold_totals_equal_the_sum_of_its_repos` must fail, and with them
  `TestStatsScratchRowCollapsed` — the UI fixture is built through `stats.Build`.
- Predicate guard: make `isScratchRepoKey` true for every non-empty key.
  `TestGroupRows/a_round_with_no_repo_is_not_scratch` and
  `TestGroupRows/no_scratch_repo_builds_no_fold_row` must fail.
  A test that passes under both mutations pins nothing.

## Repo rules this plan is written against

Comments say why and cite no history: `internal/stats/*` is **not** in
`scripts/check-comments.allow`, so no `#NNN` and no `§` may appear in its new comments or
test names (`internal/ui/view_stats.go:282`, `view_stats_test.go:283` and
`golden_test.go:247` are listed there, but CLAUDE.md's rule still governs new comments).
Every new function ≤ 70 lines — hence the `repoRow` extraction in step 2 and the
`scratchRepoChildren` helper in step 6. `internal/stats/groups.go` is 243 lines and gains
roughly 60, under the 600 cap and off the allow-list. `internal/ui/view_stats.go` is 2907
lines and already listed at `scripts/check-filesize.allow:16`, so the small in-place edits
need no new file and add no exclusion. Coverage: `internal/stats` 98.1 and `internal/ui`
81.2 in `testdata/coverage-baseline.txt` (lines 47, 50); no code moves between packages, so
the baseline must not be regenerated — if a leg drops, add tests, never lower a number.

## Report must include

`git diff --stat` against this plan's declared scope; the output of `make check`; the two
new golden paths and an explicit statement that no committed golden changed; the mutation
actually run with the failing test names; confirmation that the coverage baseline was not
touched (or, if it was, why and the `sh scripts/check-coverage.sh --write` run); and the
committed path `docs/plans/2026-10-09-scratch-repos-fold.md`.
