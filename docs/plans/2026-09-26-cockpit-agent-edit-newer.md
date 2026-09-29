# Cockpit: an agent file you edited, when relevo ships a newer copy

## 1. System Overview

`:agents › <agent>` shows each definition file's STATE. Today a file you edited is
always `your edit` (`harness.FileEdited`, from the install outcome `OutcomeKeptDiffers`).
relevo cannot say whether the copy it ships **changed since you edited**, and when it
has, a reset brings a newer definition rather than just undoing your edit.

The role manifest records the sha of what relevo last wrote at each path. In the
`KeptDiffers` branch of `installBytes` (internal/harness/install.go:285-300), the cases
are:
- `manifest[homeRel] == docSHA(shipped)`: you edited the copy relevo still ships. The
  state stays `your edit`.
- `manifest[homeRel] != ""` and it differs from `docSHA(shipped)`: you edited an older
  copy, and relevo now ships a newer one. This is the new state, **`edit + newer`**.
- There is no manifest record: nothing is known, so the state stays `your edit`.

The approved design is the canvas board ":agents › researcher D2 · your edit, and relevo
ships a newer copy (illustration)", reproduced as text in §5.

**If a step is impossible as written or contradicts the code, stop and report. Do not
improvise, and do not bend a test to fit.**

Out of scope:
- `relevo config agents` output: the Outcome strings are unchanged;
- doctor;
- the `:agents` list cell, which stays `edited`, amber, for both edited states.

## 2. File Structure

```
internal/harness/install.go         EDIT  InstallResult.NewerShipped; set in installBytes' default branch (lines ~285-300)
internal/harness/agentfiles.go      EDIT  FileEditedNewer; fileState maps it (lines 7-14, 75-90)
internal/harness/agentfiles_test.go EDIT  two cases in TestAgentFilesStates (line ~24)
internal/ui/view_agent.go           EDIT  style, detail block, reset gate and note (lines 103-115, 141-153, 320-340)
internal/ui/view_agents.go          EDIT  agentStateCell: FileEditedNewer -> "edited" (line ~240)
internal/ui/golden_test.go          EDIT  one golden case next to agent-reset-132 (line ~1232)
internal/ui/testdata/agent-edited-newer-132.golden   NEW (generated)
docs/plans/2026-09-26-cockpit-agent-edit-newer.md    NEW (last step): this plan, verbatim
```

## 3. Data Structures

- `harness.InstallResult` (install.go:60) gains `NewerShipped bool`. It is true only
  when the Outcome is `OutcomeKeptDiffers` and the manifest holds a record for the path
  that differs from `docSHA(shipped)`. It is set in the default branch **before** the
  `!opts.Force` return. No other branch sets it.
- `harness.FileEditedNewer FileState = "edit + newer"`. It is exactly 12 characters,
  the STATE column's width (view_agent.go:81).

## 4. Contracts

- `fileState(res)` (agentfiles.go:75): `OutcomeKeptDiffers` gives `FileEditedNewer` when
  `res.NewerShipped` is true, else `FileEdited`. No other mapping changes.
- `agentStateStyle` (view_agent.go:105): `FileEditedNewer` uses `warnStyle`, as
  `FileEdited` does.
- `agentStateCell` (view_agents.go:~240): `FileEditedNewer` gives `"edited", warnStyle`.
- `agentView.bodyLines` (view_agent.go:141): when the cursor row's state is
  `FileEditedNewer`, append `"", ""` and then the three lines in §5, each through
  `fit(…, width)`. Any other state appends nothing, so every existing golden is
  unchanged.
- `resetCmd` (view_agent.go:323):
  - `FileEditedNewer` is resettable;
  - its confirm note is `"with the newer copy this relevo ships; your edit is lost"`;
  - the title and label are the same as for `FileEdited`.
- Keys are unchanged.

## 5. The approved board, as text (132 columns, cursor on claude)

```
   HARNESS   FILE                                        MODEL                           STATE
   agy       ~/.gemini/config/agents/researcher.md       inherit                         up to date
   claude    ~/.claude/agents/researcher.md              haiku                           edit + newer    <- cursor, amber
   codex     ...                                                                         up to date
   opencode  ...                                                                         up to date


   claude   ~/.claude/agents/researcher.md
   edit + newer  you edited this file, and this relevo ships a newer copy
   e shows your edit; r replaces it with the newer copy
```

The detail lines, with their styles:
1. `"   " + faintStyle.Bold(true).Render(kind) + "   " + mutedStyle.Render(tildePath(path))`
2. `"   " + warnStyle.Render("edit + newer") + mutedStyle.Render("  you edited this file, and this relevo ships a newer copy")`
3. `"   " + textStyle.Render("e") + mutedStyle.Render(" shows your edit; ") + textStyle.Render("r") + mutedStyle.Render(" replaces it with the newer copy")`

## 6. Working Efficiently

- Read these in one batch:
  - internal/harness/install.go 55-70 and 250-310;
  - internal/harness/agentfiles.go (all);
  - internal/harness/agentfiles_test.go 1-60;
  - internal/ui/view_agent.go (all);
  - internal/ui/view_agents.go 225-245;
  - internal/ui/golden_test.go 720-760 and 1225-1245.
- Make each file's changes in one edit.
- **This round runs on a laptop that crashes under full race suites. Do NOT run
  `make check`, `go test ./...` or any `-race` run.** CI runs them. Run only:
  - loop: `go build ./... && go test -count=1 ./internal/harness/ -run AgentFiles && go test -count=1 ./internal/ui/ -run 'Agent|Golden'`
  - end, once:
    - `gofmt -l $(git ls-files '*.go')` must print nothing;
    - `go vet ./internal/harness/ ./internal/ui/`;
    - `sh scripts/check-comments.sh`;
    - `sh scripts/check-filesize.sh`;
    - `golangci-lint run ./internal/harness/... ./internal/ui/...`, when it is installed;
    - `go test -count=1 ./internal/harness/ ./internal/ui/`.

## 7. Ordered Implementation Steps

**Step 1: harness.** Edit install.go and agentfiles.go per §3-§4.
- Add two cases to `TestAgentFilesStates` (agentfiles_test.go:24):
  - `"your edit over an older shipped copy gives edit + newer"`: file `edited`, manifest
    `{".gemini/config/agents/reviewer.md": docSHA(old)}`, want `FileEditedNewer`, model
    `"haiku"`.
  - `"your edit over the current shipped copy stays your edit"`: file `edited`, manifest
    `{…: docSHA(shipped)}`, want `FileEdited`, model `"haiku"`.
- The existing nil-manifest `FileEdited` case must still pass unchanged.
- Verify: `go test -count=1 ./internal/harness/`.

**Step 2: ui.** Edit view_agent.go and view_agents.go per §4-§5. Depends on step 1.
- Verify: `go test -count=1 ./internal/ui/`. **Every existing golden passes
  unchanged.** If one moves, stop and report.

**Step 3: golden and tests.** Depends on step 2.
- The golden case `agent-edited-newer-132` goes next to `agent-reset-132`
  (golden_test.go:1232):
  - use the same fixture with claude's researcher state set to `harness.FileEditedNewer`;
  - `candDown(…, 1)` puts the cursor on claude, with no key press after;
  - generate it with `go test ./internal/ui/ -run Golden -update`;
  - read it, and check it line by line against §5;
  - report the golden's full text.
- A unit test in view_agent's test file, or golden_test.go if agent view tests live
  there, named `TestAgentResetOnEditNewerSaysNewerCopy`:
  - `r` on an `edit + newer` row opens a confirm whose lines contain
    `"with the newer copy this relevo ships; your edit is lost"`.
- **Required mutations.** Run each, confirm the named test fails, then restore. List all
  three in the report.
  - (a) Never set `NewerShipped`: the "edit + newer" harness case fails.
  - (b) Set `NewerShipped` also when there is no manifest record: the existing
    nil-manifest `FileEdited` case fails.
  - (c) Drop the detail block from `bodyLines`: `agent-edited-newer-132` fails.
- Comments say why, never restate code, and carry no history, "§" or issue numbers.
  `check-comments.sh` must pass.

**Step 4: ship.** Copy this plan verbatim to
`docs/plans/2026-09-26-cockpit-agent-edit-newer.md`. Commit everything in one commit:
`feat(cockpit): an agent file you edited shows edit + newer when relevo ships a newer copy`.

## Report

The report covers:
- `git diff --stat`;
- the three mutations, each with its failing test;
- the golden's text;
- the output of the step-6 end checks;
- anything that did not match the code.
