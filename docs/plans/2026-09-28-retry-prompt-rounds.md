# Fix #643 -- retry must see the round's `NNN-prompt.md`

## 0. Rules for this round

- Before anything else: `git status`. The tree must be clean and on the
  feature branch; anything else means stop and report.
- If a step is impossible as written, or the code contradicts this plan, **stop
  and report** -- do not improvise.
- **Run every check in the foreground and wait for it.** Never run `make check`
  or a test as a background task, and never end your turn to wait for one.
- Comments: *why* only, no issue numbers, no `§`, no history. No new
  `.golangci.yml` exclusion or allow-list entry. Functions stay <= 70 lines.
- `internal/ui/actions.go` sits at 590 lines against the 600 ceiling: keep the
  edit local, add no reflow.

## 1. The defect

`Retry` resends a round's plan on another candidate. When the binding has no
open round it asks `lastPlannedRound` (`internal/ui/actions.go`) which round to
resend: the highest round with a plan on disk.

`lastPlannedRound` scans `Store.RoundFiles` through `plannedRound`, and
`plannedRound` only stripped `-plan.md`. New rounds stage their plan as
`NNN-prompt.md` (`internal/store/paths.go`, `PromptPath`), so the scan finds
nothing and returns 0. `Retry` then falls back to `b.Round - 1`, which can
resend the wrong round or surface
`no plan recorded for <name> round N` from `internal/relevo/retry.go`.

The CLI path already reads the plan through `PromptPath`; this scan is the only
stale matcher. `git grep -- '-plan\.md' -- '*.go'` shows only
`internal/ui/actions.go` plus fixtures, and the `cmd/relevo/bind.go` hit is a
message.

## 2. Behaviour

- `plannedRound(base)` accepts a basename whose suffix is `-prompt.md` (new) or
  `-plan.md` (historical files stay readable), whose prefix parses as an integer
  `>= 1`; everything else is not a planned round. Single-digit prefixes stay
  accepted, exactly as today's `-plan.md` branch does. Matching stays
  suffix-over-the-whole-round-file-name; sealed rows are seen because
  `RoundFiles` unions disk and the live record.
- `lastPlannedRound` keeps returning the highest matching round, 0 when none.
- `Retry`, `lastPlannedRound`, `RetryPlan` and `PromptPath` do not change; only
  `plannedRound`'s acceptance widens.
- Acceptance cases:
  - match: `004-prompt.md` -> 4; `004-plan.md` -> 4; `1-prompt.md` -> 1;
  - must not match: `004-report.md`, `004-done`, `bind.json`, `004-plan.txt`,
    `000-prompt.md`, `no-round-prompt.md`, `004-planner/plan.md` (a reader
    artifact named `plan.md` under an artifact directory, as `RoundFiles`
    reports nested names -- this is why the hyphen belongs in the suffix);
  - `lastPlannedRound` over mixed files: flat `001-plan.md` + `003-prompt.md`
    with distractors `003-done`, `002-report.md` and nested
    `002-planner/plan.md` -> 3; `001-prompt.md` + `002-plan.md` -> 2;
    only `001-report.md` + `bind.json` -> 0.

## 3. File structure

```
internal/ui/actions.go     plannedRound accepts both suffixes (lines 423-434)
internal/ui/actions_test.go  TestPlannedRound + TestLastPlannedRound (after line 824)
docs/plans/2026-09-28-retry-prompt-rounds.md   this plan
```

No other file changes. `internal/relevo/retry.go`, `internal/store/paths.go`,
`cmd/relevo/bind.go`, README, docs/specs and both plugin trees are untouched.

## 4. The two accepted suffixes

```
suffix "-prompt.md"   the current round-file spelling (PromptPath's new name)
suffix "-plan.md"     the legacy spelling; historical rounds stay readable
prefix                Atoi(prefix) >= 1, single digit accepted
```

`004-planner/plan.md` must not match: the whole reported name ends with
`plan.md`, not with the hyphenated suffix.

## 5. Tests and the mutation check

`internal/ui/actions_test.go`:

- `TestPlannedRound`: a table over the match and must-not-match cases above.
- `TestLastPlannedRound`: the three store cases above, using
  `store.New(t.TempDir())` and files written under `st.Dir(name)`.

Mutation: remove the `-prompt.md` acceptance only, re-run the focused command,
and confirm `TestPlannedRound/004-prompt.md` and
`TestLastPlannedRound/prompt_beats_an_older_plan` fail; restore and re-run
green. A mutation that leaves the suite green shows the prompt branch is
unpinned -- do not bend the mutation to fit the code.

## 6. Ordered steps

1. Read in one batch: `actions.go` 351-430, `paths.go` 125-141, `retry.go`,
   `seal.go` 120-160; run `git grep -n -- 'plannedRound\|lastPlannedRound' -- '*.go'`
   and `git grep -n -- '-plan\.md' -- '*.go'`. Worked when only `actions.go`
   holds the matcher and its one call site; anything else halts per §7.
2. Edit `plannedRound` to accept both suffixes per §2, keeping the `n < 1`
   rejection; set the comment per §2. Worked when `go build ./...` is clean,
   `gofmt -l internal/ui/actions.go` is empty, and `git diff --stat` names only
   that file.
3. Add `TestPlannedRound` and `TestLastPlannedRound` to `actions_test.go` after
   line 824. Worked when
   `go test ./internal/ui -run 'PlannedRound|LastPlannedRound' -count=1` is green.
4. Mutation per §5; report the failing subtests and the restored diff.
5. Write this plan doc.
6. Run `make check` once, in the foreground. Worked when it is green and the
   `internal/ui` coverage line in `.coverage.txt` is not below 81.6 -- no
   `check-coverage.sh --write`.
7. Commit once: `actions.go`, `actions_test.go`, this doc; message in house
   shape, e.g. `fix(ui): retry finds a round's prompt, not only legacy plans (#643)`.
   Worked when `git show --stat HEAD` lists exactly those three files and
   `git status --porcelain` is empty.

Commands:

```
go test ./internal/ui -run 'PlannedRound|LastPlannedRound' -count=1
make check
```

## 7. Halt clauses

- Halt and report with file:line if any other caller carries the stale matcher,
  or if any test pins the old single-spelling `plannedRound` behaviour.
- Halt if a step is impossible as written, if `actions.go` would cross 600
  lines, if a third suffix seems needed, or if the prompt mutation leaves the
  focused suite green after the tests were made as precise as the cases above.
- Halt if `make check` needs a baseline regeneration or a new exclusion.

## 8. Deleted

1. `plannedRound`'s `-plan.md`-only acceptance -- replaced by the two-suffix
   acceptance.
2. Nothing else. No file, function, test, fixture, comment, allow-list entry,
   `.golangci.yml` exclusion or coverage baseline is deleted, lowered or added;
   `Retry`, `lastPlannedRound`, `RetryPlan` and `PromptPath` keep their code.

## 9. The report must include

- `git show --stat HEAD` for the one commit and the exact focused command with
  its result.
- The mutation: which acceptance was removed, the failing subtest names, and
  the restored diff.
- `make check`'s tail and the `internal/ui` coverage number.
- The `git grep -- '-plan\.md' -- '*.go'` result proving no stale matcher
  remains.
- Any halt or hazard found, and this doc's path.
