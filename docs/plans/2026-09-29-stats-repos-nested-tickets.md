# Plan for #667: nest each feature's tickets under that feature on the `:stats` repos tab

## One seed contradiction and one decision the builder follows

- **Visibility of `(no feature)` (the builder follows this; the MasterMind can override it).** Today `repoChildren` (`internal/ui/view_stats.go:2616-2635`) shows the `(no feature)` row only when the repo also has a labelled feature. The seed says the bucket shows "when shown" but does not define when. Nesting creates a new case. Take a repo with no labelled feature whose featureless rounds carry ticket labels. Today those tickets show in their own section. Under the old rule they would become unreachable. **The rule:** show `(no feature)` when `NoFeature.Rounds > 0` and the repo has a labelled feature or `NoFeature.Tickets` is non-empty. A repo with no labels at all still expands to nothing.
- **Ordering.** The seed says "tokens desc". `stats.groupRows` sorts by rounds, not tokens. Only the UI sorts by tokens (`reposByTokens`, `view_stats.go:2639-2643`). Keep it that way: stats order stays as it is, and the UI applies `reposByTokens` to both the features and each feature's tickets.
- The seed's other claims match the code. `internal/stats.Report` is consumed only by `internal/ui/view_stats.go`, so `relevo history --stats`/`--json` is not affected. The README has no repos-tab passage: `README.md` ~575 describes `:rounds` grouping, and ~1924 describes `history --stats`. The README therefore stays unchanged.

## Behaviour

When a repo is expanded, its children appear in this order:
1. Each labelled feature, tokens desc, with its name indented 2 cells.
   - Directly under each feature: that feature's labelled tickets, tokens desc, indented 4 cells.
2. Then the `(no feature)` row (indent 2), if the rule above shows it.
   - Under it: its labelled tickets (indent 4).

Other rules:
- No `(no ticket)` row exists anywhere. A feature's numbers already include its ticketless rounds. A round with neither label counts only in `(no feature)`'s totals.
- A ticket used by two features appears under each feature. Each copy counts only its own feature's rows in that repo.
- `enter` behaviour:
  - on a feature row: `repo:"R" feature:"F"`
  - on a ticket under feature F: `repo:"R" feature:"F" ticket:"T"`
  - on a ticket under `(no feature)`: `repo:"R" ticket:"T"`
  - on the `(no feature)` row: notice "rounds with no feature cannot be filtered"
  - on any child of the `(none)` repo: notice "rounds with no repo cannot be filtered"
  - Each query gets ` since:<w>` appended as today.
- These stay as they are: the detail block (`statsGroupDetail`, which tags a ticket row as "ticket"), the expand/collapse keys and their cursor rules, expansion keyed by repo so it survives a refresh, and cursor movement over visible rows only. There is no third expansion key.

## Seams

- `internal/stats/groups.go`
  - `RepoRow` (22-32): becomes `RepoRow{GroupRow; Features []FeatureRow; NoFeature FeatureRow}`.
  - New type `FeatureRow{GroupRow; Tickets []GroupRow}`.
  - `buildRepos` (137-163): split each repo's rows (`own`) by feature. Each labelled feature's `FeatureRow` gets `Tickets = groupRows(in, thatFeaturesRows, ticketKey)`. `NoFeature` is `noneRow(featureless rows)` plus the tickets of those rows, or zero when there are no featureless rows.
  - `noneRow` (165-172): keep it.
  - `noTicketKey` (202-207): delete it.
  - Doc comments at 22-25 and 137-140: update them, and fix the stray " ." typo at line 139.
- `internal/stats/stats.go:41`: the `Repos` comment becomes "each with its features and their tickets".
- `internal/ui/view_stats.go`
  - `repoTabRow` (2603-2609): add a `feature string` field, which is the owning feature key for a ticket row (`"(none)"` under the bucket) and `""` otherwise.
  - `repoChildren` (2611-2635): build the nested order described above.
  - `enter` (624-634) and `enterRepoChild` (642-653): make the ticket-row query feature-aware.
  - `statsRepoRow` (2699-2727): replace `indent bool` with a depth (0/1/2). The name is indented `2*depth` cells and `nameW` shrinks by the same amount through `statsMinWidth`.
  - `reposTabLines` (2752): pass the depth, computed from `parent`/`feature`/`kind`.
  - Rewrite the doc comments at 2611-2615, 2699-2704 and 2729-2733.
  - `reposSelected`, `toggle`/`expand`/`collapse*` and `repoRowIndex` need no change, because ticket rows carry `parent`.
- `internal/ui/golden_test.go:724-765`: update the `statsReposExpandedReport` comments. The rows stay as they are: they already contain a ticket that two repos share, featureless rounds and an unlabelled repo.
- `internal/ui/testdata/stats-repos-expanded-132.golden` gets regenerated. `stats-repos-132.golden` must come out byte-identical.
- `docs/specs/2026-09-24-cockpit-design.md`: rewrite the §5 table row "Repos & features" (line 292) and the sentence under the table (295-296).

## Steps (test first)

1. **stats tests** (`internal/stats/stats_test.go`, 352-530).
   - Rewrite `checkGroupRepoNoneBuckets` and `checkGroupUnlabelledRepo` against the nested shape, dropping every `Tickets`/`NoTicket` reference.
   - Add these cases to `repoScopedRowCases`:
     - a feature with tickets, where the ticketless rounds count only in the feature
     - a ticket under `(no feature)`
     - one ticket under two features, with numbers scoped to each feature
     - a ticket label under two repos, still split per repo
     - a repo with no labels: no features, and `NoFeature` holds every round with no tickets
   - Done when the new tests fail to compile or fail against the current code.
2. **stats implementation** (`groups.go`, `stats.go:41`). Done when `go test ./internal/stats/` passes.
3. **ui tests** (`internal/ui/view_stats_test.go`).
   - Port the fixture at 84-91 and every `NoFeature`/`Features` literal to `FeatureRow` (at about 2423-2465, 2610, 2848-2849, 2928-2929 and 3106-3110).
   - Rewrite `TestStatsRepoChildren` (2418-2470): nested order, ticket rows carry `feature`, the `(no feature)` rule including the tickets-only repo, and no children for an unlabelled repo.
   - Rewrite `TestStatsReposTicketRows` (2846-2918): tickets nested under the feature, the 4-cell indent against the feature's 2 cells in the rendered body, no `(no ticket)` anywhere, and the detail on a ticket row.
   - Extend `TestStatsReposChildEnter` (3085-3137) with three cases: ticket under a feature (3-term query), ticket under `(no feature)` (repo+ticket query), and the `(no feature)` notice.
   - Adjust `TestStatsReposNoFeatureRow` (2575-2624), whose last subtest's premise changes with the visibility rule.
   - Leave the expand, cursor and refresh tests (2924-3079) as they are. The fixture has no tickets, so their counts hold.
   - None of these tests spawns a harness or reaches the network. They drive `statsView` and `statsReposShell` in-package.
   - Done when the tests fail against the step-2 code.
4. **ui implementation** (`view_stats.go` seams above). Done when `go test ./internal/ui/ -run 'TestStats'` passes.
5. **Goldens.** Run `go test ./internal/ui/ -run TestGolden -update`, then check with `git diff`:
   - The expanded golden shows cockpit (2M), then `    #665` (1M) nested under it, then `(no feature)` 220k, and no `(no ticket)` row. The cursor is on cockpit and the detail is unchanged.
   - `stats-repos-132` has no diff.
6. **Spec.** Rewrite the §5 row and the sentence below it: features expand with their tickets nested beneath each; `(no feature)` is a feature row with its own tickets; there are no `(no ticket)` rows; a ticket under two features appears under each; `enter` scoping is as in the Behaviour section. No section or issue numbers go in code comments.
7. **Full gate.** Run `make check` once. Coverage baseline, `.golangci.yml`, `scripts/check-*.allow` and `testdata/coverage-baseline.txt` stay untouched. `view_stats.go` is already in `check-filesize.allow` and `check-comments.allow`, and no new exclusion is added. Every function stays at 70 lines or fewer.
8. **Plan copy.** Save this plan as `docs/plans/2026-09-29-stats-repos-nested-tickets.md` in the same PR (plans ship with their implementation).

Commands:
- focused: `go test ./internal/stats/ ./internal/ui/ -run 'TestGroupRows|TestStats|TestGolden' -count=1`
- full: `make check`

## Deleted behaviour (closed list)

1. The `RepoRow.Tickets` field.
2. The `RepoRow.NoTicket` field.
3. `noTicketKey` in `internal/stats/groups.go`.
4. The repo-level ticket section in `repoChildren`, meaning tickets listed after all the features.
5. The `(no ticket)` row, both at repo level and per feature, together with its "rounds with no ticket cannot be filtered" path from a repos-tab row. The guard in `enterRepoChild` may stay if it is harmless, but no row reaches it any more.
6. The single-cell-step `indent bool` parameter of `statsRepoRow`.

## Out of scope

- The overview's BUSIEST REPOS table (`overviewRepoRows`).
- The other tabs.
- `relevo history --stats`.
- The README.
- A new view, package, tab or expansion key.
- A new query term for "no feature" or "no repo".

## The report must include

- `git diff --stat`, which should contain only: `internal/stats/{groups.go,stats.go,stats_test.go}`, `internal/ui/{view_stats.go,view_stats_test.go,golden_test.go}`, `internal/ui/testdata/stats-repos-expanded-132.golden`, the spec, and the plan copy.
- The new expanded golden, pasted.
- A statement that `stats-repos-132` is unchanged.
- The `make check` result.
- One mutation test: make `enterRepoChild` drop the `feature:` term for tickets under a labelled feature, and name the test that fails.
- A confirmation that the coverage baseline and the allow-lists were not touched.
- Any departure from the `(no feature)` visibility rule, stated as a halt rather than improvised.
