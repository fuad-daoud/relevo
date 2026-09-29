# Cockpit D2 overlays, round 5: the last two rendering leftovers

Date: 2026-09-25. Worktree: ck-d2-overlay, which holds rounds 3-4 **uncommitted**. Build on it; do not reset or commit. A tiny round.

**Stop rather than improvise.** If the code differs from what is quoted, halt and report.

The real-terminal capture after round 4 shows two leftovers. Round 4 fixed both of them elsewhere, but not here:

1. **The gate form still shows `> `** before `for` and `reason`. internal/ui/form.go:145 builds its input with `textinput.New()` and keeps
   the default prompt. Set `in.Prompt = ""` there, or build the input with `newPromptInput()` (confirm.go:146), which already clears it.
2. **The retry list's selection band stops before the row ends.** internal/ui/list_box.go:113 wraps an already-styled row in
   `selBandStyle`, so the inner resets end the band. Build the selected row from plain text (name, status and note, padded to the
   row width `innerW`) and render it once with `selBandStyle.Foreground(<text token>).Bold(true)`, exactly as round 4 did for
   `modalRow`. Or render the listBox rows through `modalRow` itself if the columns fit.

## Tests

- `TestGateFormNoPrompt`: the stripped gate form contains no `> `.
- `TestListBoxBandCoversRow`: set the true-colour profile. The selected listBox row contains exactly one background SGR, and its plain
  width equals innerW.
- Regenerate the goldens. Only `prompt-gate` and `retry-list-132` may change.

## Commands

- Loop: `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -count=1`, plus `-run Golden -update`.
- Final: `gofmt -l internal/ui/*.go` and `go vet ./internal/ui/`, then the same test command. Not `make check`.

## Report

List the files changed, the golden diffs and the check results.
