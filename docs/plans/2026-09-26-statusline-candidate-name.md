# Statusline rows name the candidate, not the harness

## Goal

A statusline row's middle reads `r1 · lite-planner on opencode`. It should
name the candidate the actor runs on -- `r1 · lite-planner on
deepseek-v4.1-flash-max` -- the same short name `relevo status` prints on its
runner line. Both surfaces change: the Go statusline (`relevo status --line`)
and the OpenCode TUI plugin, which renders the same row from
`relevo status --line --json`.

## Facts (verified against main 0c8da537)

- `internal/view/statusline.go` `renderedStatusLineRow` builds the middle:

      mid := "r" + strconv.Itoa(round) + " · " + row.Actor
      if row.Candidate != "" {
          mid += " on " + row.Harness
      }

  `row.Harness` comes from `statusLineRowOf`: `harnessSegment(b.BuilderCandidate)`
  (the token up to the first `/`), plus `"@" + b.Server` for a remote runner.
- `b.BuilderCandidate` is the full token, e.g.
  `opencode/cline-pass/cline-pass/deepseek-v4.1-flash#max`.
- `view.BindingStatus.BuilderName` (`internal/view/status.go`) is that token's
  short name, e.g. `deepseek-v4.1-flash-max`, set in
  `internal/relevo/status.go` `statusRow` only when the candidate set holds the
  token; a retired token leaves it empty. `relevo status` (`writeBuilderLine`
  in `internal/view/render.go`) prints `BuilderName`, falling back to the token.
- `relevo status --line` and `--line --json` get their Report from
  `relevo.PlannerStatus` (`internal/relevo/statusline.go`). Confirm it builds
  its rows through `statusRow` so `BuilderName` is populated; if it does not,
  halt and report -- do not duplicate the name lookup.
- `internal/harness/opencodeplugin/tui.tsx` derives the same wording itself:
  - line ~550, the needs-you dialog hint:
    `const who = rowActor(row) + " on " + (row.harness || "opencode");`
  - line ~721, the sidebar row's line B:
    `` `  r${displayRound} · ${rowActor(row)} on ${row.harness || "opencode"}` ``
  - the fleet table's ON column (~961) and the binding page header (~1549)
    use `parseModel(row.candidate)`, which strips the token to
    `deepseek-v4.1-flash` -- not the candidate name either.

## Steps

1. **`StatusLineRow` gains `On`.** In `internal/view/statusline.go` add

       // On is what the row's actor runs on, as "actor on X" names it: the
       // candidate's short name, else its harness when the set no longer
       // holds the token, with "@server" for a remote runner.
       On string `json:"on"`

   `statusLineRowOf` sets it: `b.BuilderName` when non-empty, else
   `harnessSegment(b.BuilderCandidate)`; then `"@" + b.Server` when
   `b.Server != ""`; empty when `b.BuilderCandidate` is empty. Keep `Harness`
   and `Candidate` in the JSON unchanged -- older plugins read them.
2. **The Go middle uses it.** `renderedStatusLineRow`: `mid += " on " + row.On`
   when `row.On != ""` (replacing the `row.Candidate != ""` / `row.Harness`
   pair). Update `RenderStatusLine`'s doc comment only if it mentions the
   harness.
3. **The plugin uses it**, falling back to today's behaviour when an older
   binary sends no `on`:
   - dialog hint (~550) and sidebar line B (~721):
     `row.on || row.harness || "opencode"`;
   - fleet ON column (~961) and binding page header (~1549):
     `row.on || parseModel(row.candidate)`.
   Change nothing else in `tui.tsx`. If `scripts/opencode-plugin-smoke.sh`
   or any test asserts the old wording, update it to the new one.
4. **Tests** (`internal/view/statusline_test.go`):
   - Update the existing assertions that expect `builder on opencode`
     (around lines 264, 774, 928, 935): set `BuilderName` on those fixtures
     and expect the name, e.g. `builder on glm-5.3-flash@contabo` for the
     remote case and `builder on glm-5.3-flash` for the local one.
   - Add a test that pins the fallback: `BuilderName` empty (retired token)
     renders `on opencode` (and `on opencode@contabo` with a server).
   - Add a JSON test (or extend an existing `StatusLineRows` test) that pins
     `row.On` for: named local, named remote, unnamed, and no candidate
     (`On == ""`, and the middle has no ` on `).
   - If a `cmd/relevo` golden or test covers `status --line --json`, update
     it. Such tests must not spawn a harness or reach the network.
5. **Mutation-test** before finishing: (a) make `On` ignore `BuilderName`
   and confirm a named test fails; (b) drop the `@server` suffix and confirm
   a named test fails; (c) drop the harness fallback and confirm a named test
   fails. Restore each. Name the failing test for each mutation in your report.
6. `make check` must pass. Run `gofmt -l .` and `sh scripts/check-comments.sh`
   yourself too.
7. Last step: save this plan verbatim as
   `docs/plans/2026-09-26-statusline-candidate-name.md` and commit it with the
   change.

## Scope

Expected files: `internal/view/statusline.go`, `internal/view/statusline_test.go`,
`internal/harness/opencodeplugin/tui.tsx`, the plan file, and at most one
`cmd/relevo` test/golden and `scripts/opencode-plugin-smoke.sh` if they
assert the old wording. Anything else: halt and report why first.

## Rules

- Follow CLAUDE.md's code style: comments say why, no issue numbers or
  history in code or tests; a test's name says what it pins.
- If a step is impossible as written or contradicts the code, stop and report
  -- do not improvise and do not bend a test to fit.
- Commit on the binding's branch with a message like
  `fix(statusline): a row names its candidate, not the harness`.
