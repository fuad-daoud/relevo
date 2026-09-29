# Cockpit `:settings`, round 2: the new checks, and four fixes from the real screen (2026-09-26)

## 1. System overview

Round 1 shipped `:settings` (commit "feat(cockpit): :settings …"). The planner has rebased it onto `origin/main`
`f06f2b4`, which adds #519's comment and file-size checks, and has run the real binary on real data.

This round fixes exactly five things. Nothing else changes.

1. **Comments.** `scripts/check-comments.sh` flags 44 lines that cite history (`#NNN` or `§`):
   - `internal/ui/settings_form.go`: 20;
   - `internal/ui/view_settings.go`: 18;
   - `internal/relevo/configpolicy.go`: 5;
   - `internal/ui/view_settings_test.go`: 1.

   Rewrite each so it says *why* in plain words, or delete it when it only pointed at a section. Do **not** add files
   to `scripts/check-comments.allow`.
2. **The blank line under the context row.** At 132×34, with the cursor on `gate.default`, the body is:
   - one leading blank line;
   - 24 table lines;
   - 2 blank lines;
   - a 3-line detail block.

   That is 30 lines against a body of 29. Round 1's extra `follow()` rule then scrolls `top` to 1 and hides the
   leading blank, so `SETTING` sits right under `17 settings · 3 set`. The board keeps that blank.
   - **Fix:** one blank line between the table and the detail block, not two.
   - Keep round 1's "advance `top` to show the detail when the cursor stays in view" rule. At 132×34 it must now leave
     `top` at 0.
3. **The cursor row's key.** On an unset row, the cursor row's key is drawn dim and bold. It must be the text colour
   and bold, as on every other cursor row (`candLine`). Value and default keep their colours.
4. **Reset is checked before the confirm.** Round 1 runs `ResetSetting` inside the confirm's `onYes`, so a reset that
   cannot pass is confirmed first and then fails. On this machine that happens to `max_tier`: the builder and reviewer
   actors run at `yolo`, so the default `edit` is refused.
   - **New rule:** at `r`, call `relevo.ResetSetting(doc, key)` at once.
     - `ErrNoChange` → `notice(key + " is already the default")`, as today.
     - Any other error → `notice("can't reset " + key + ": " + <error text>)`, and no confirm.
     - Success → open the same confirm, and `onYes` applies **that precomputed edit** through `runAction`/`ApplyConfig`.
   - This supersedes plan r1 §4.5's "y runs ResetSetting".
5. **The detail sentence colour.** The help sentence under the key line must be `mutedStyle`, dim, as r1 §5.2 says.
   The actor names in the check sentence stay in the text colour. Check what round 1 used and report it. If it
   already is `mutedStyle`, leave it and say so.

## 2. Files

```
internal/relevo/configpolicy.go        comments only
internal/ui/view_settings.go           comments; fixes 2, 3, 4, 5
internal/ui/settings_form.go           comments only
internal/ui/view_settings_test.go      comment; + tests of §3 step 3
internal/ui/testdata/settings-*.golden regenerated (settings-132 and settings-100 shift by the fixes; others as they fall)
docs/plans/2026-09-26-cockpit-settings-r2.md
```

## 3. Steps

### 0. Working efficiently

**How to work:**
- Run `sh scripts/check-comments.sh` once for the exact lines.
- Then, in one batch, read `internal/ui/view_settings.go`, `internal/ui/settings_form.go`,
  `internal/relevo/configpolicy.go`, `internal/ui/view_settings_test.go` and `internal/ui/view_candidates.go:220-300`
  (`candLine`, for the cursor style).
- Make each file's change in one edit.

**Commands:**
- First run `mkdir -p $HOME/.cache/relevo-verify/tmp`.
- Focused loop: `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/relevo/ ./internal/ui/ -run 'Setting|Policy|Golden' -count=1`
- Goldens: `... go test ./internal/ui/ -run TestGoldenViews -update -count=1`. Then read the regenerated
  `settings-132.golden`:
  - line 4 is blank;
  - line 5 is the `SETTING` header;
  - the detail block starts one blank line under `notify.webhooks`.
- Final checks:
  - `sh scripts/check-comments.sh && sh scripts/check-filesize.sh`
  - `golangci-lint run ./internal/ui/... ./internal/relevo/...`
  - `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/relevo/ ./internal/ui/ ./internal/policy/ -count=1`
  - `go vet ./internal/...`
  - `test -z "$(gofmt -l $(git ls-files '*.go'))"`
  - `sh scripts/check-name.sh`
- Skip `make check`: it timed out on this server last round.

### 1. Comments (§1.1)

### 2. Fixes 2, 3 and 5 (§1.2, §1.3, §1.5); regenerate the goldens

### 3. Fix 4 (§1.4), with tests in `view_settings_test.go`

- **a. `TestSettingsResetRefusedBeforeConfirm`:** use a fixture whose actor runs at `Tier: "yolo"` with `max_tier` set to
  `yolo`, as in `candFixtureDoc`. `r` on `max_tier` opens **no** overlay, shows a notice starting
  `can't reset max_tier:`, and records no config edit.
- **b.** Round 1's reset test (`r` then `y` records one edit, `reset max_tier`) must still pass. Give it a fixture where
  the reset is valid, e.g. one with no actor tier above `edit`. If you change its fixture, say so. Changing its
  assertions is not allowed.
- **c.** The golden `settings-reset-132` must still show the confirm. Give it the same valid fixture, or point it at a
  row whose reset is valid, and report which.

**Required mutation:**
- **M1:** make `r` skip the eager `ResetSetting` and always open the confirm → test 3a fails.

Report the failing line, then revert.

### 4. Checks, the plan, the commit

1. Run the final checks listed in step 0.
2. Copy this plan to `docs/plans/2026-09-26-cockpit-settings-r2.md`.
3. `git add -A && git commit --amend --no-edit`. One commit stays on the branch.

## 4. Deletions

- The second blank line between the table and the detail block.
- The `onYes`-time `ResetSetting` call, replaced by the precomputed edit.
- History citations in comments.

Nothing else is deleted.

## 5. Stop rather than improvise

Halt and report if any of these happens:
- a golden outside `testdata/settings-*` changes;
- `check-filesize.sh` fails after the edits;
- the reset fix needs a change to `confirmBox`.
