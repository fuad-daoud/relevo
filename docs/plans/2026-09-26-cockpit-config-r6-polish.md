# Cockpit config views, round 6: polish (2026-09-26)

Branch `relevo/ck-config`. It has one commit holding rounds 1-5. **Amend it**. This round also **replaces its
message** (step 4).

## 1. Overview

These are small fixes found on the real screen and in the goldens after round 5. **No new features.**

## 2. Changes, closed list

1. **The candidate form's provider sub-row starts at the box edge.** In `internal/ui/candidate_form.go`, the row under
   the provider field is one of:
   - the suggestion chips (open harness);
   - `new provider`;
   - the provider error.

   It currently starts at column 0 of the box body. Indent every one of them to the **value column**, the same column
   the provider's text value starts in: `formLabel` width + the input's leading space. The model's error row and the
   harness's error row get the same indent. `formError` in `form_rows.go` may already indent to 12 spaces; make all
   three sub-rows use one helper so they line up with the value.
   - Verify in `cand-edit-132.golden`: the `openrouter    cline-pass` chips start in the same column as `cline-pass` on
     the provider line above.
2. **An empty gate reason renders as `· ·`.** Wherever a gate line is built as `… · <statsGateReason(note)>`, omit the
   ` · <reason>` part when the reason is empty or `·`. That is the candidate form's info line and the `:candidates`
   detail block. Grep `statsGateReason(` in `candidate_form.go`, `view_candidates.go`, `view_actors.go` and
   `view_actor.go` and fix each call site the same way.
   - Verify: `cand-edit-132.golden` no longer contains `· ·`.
3. **`r` on a missing agent file says "up to date".** In `internal/ui/view_agent.go`, `r` is enabled when the state is
   `your edit`, `stale` **or `missing`**.
   - For missing, the confirmation title is `Write <kind>'s <name>?`, the label `writes` (instead of `overwrites`), and
     the second line `the copy this relevo ships`. `ResetAgentFile` (Force install) already writes an absent file.
   - The notice for `up to date` is unchanged.
   - Add a unit test: a missing file, then `r`, then `y`, gives `resets == [[kind, name]]`.
4. **The commit message.** Replace it with:

   ```
   feat(cockpit): :candidates, :actors and :agents -- add, edit and delete candidates, order each actor's candidates, per-harness agent files

   - :candidates lists every candidate in pick order with who picks it (SERVES) and its gate; enter edits,
     a adds (harness from the supported list, provider locked on agy and claude, the name follows the model),
     d deletes behind a confirm, g/u gate and ungate, p probes.
   - :actors lists the actors and their next pick; enter opens one to reorder (shift+up/down), switch
     entries on/off (space), add from a picker, remove, and e edits its agent, tier and check.
   - :agents lists the shipped and custom agents with each harness's definition file state; enter opens
     one: e edits a file in $EDITOR, r resets an edited or stale file behind a confirm, d deletes a custom agent.
   - internal/relevo/configedit.go: the edit operations, validated against the whole config (a dry run of
     actors.ToRolesFile + roles.Build) before one config revision is written with source "ui".
   - harness.Providers (agy, claude), harness.PinnedModel (moved from doctor), harness.AgentFiles/ResetAgentFile.
   ```

   Use `git commit --amend -F <file>` with that text, and end the message with these two lines:

   ```
   Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
   Claude-Session: https://claude.ai/code/session_014qTDXQ7YHf1v8oDZx2cqex
   ```

Nothing else changes. A test that fails because of one of these four items is updated to the new text (report it). Any
other failure means halt.

## 3. Working efficiently

- Read `internal/ui/candidate_form.go`, `form_rows.go`, `view_agent.go`, and grep `statsGateReason(` in one batch.
- **Focused:** `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -count=1`. Regenerate goldens with
  `-run TestGoldenViews -update`. Only `cand-*` goldens may change; any other golden changing means halt.
- **Final:** `go vet ./internal/... ./cmd/...`, `test -z "$(gofmt -l $(git ls-files '*.go'))"`, `go mod tidy -diff`
  and `sh scripts/check-name.sh`. `make check` is refused on this machine by a hook, so do not run it.

## 4. Steps

1. Items 1-3, with the goldens regenerated and the new unit test.
2. The checks.
3. Copy this plan into the worktree's `docs/plans/`. `git add -A`, then amend with the item 4 message.

## 5. Stop rather than improvise

If an item cannot be done as written, halt and report.
