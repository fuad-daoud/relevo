# Remove the feat-drift check from make check

Date: 2026-09-25. Base: origin/main 58f8e603. Line numbers are exact at that commit. One round.

**Stop rather than improvise.** If a file differs from what is quoted here, halt and report.

## 1. Why

`scripts/check-plugin-version.sh` fails `make check` when main has more than 10 first-parent
`feat` commits since the release tag (#164, #176). Because CI runs on the PR merged into main, the
count is main's, not the PR's. So once it trips, **every** open PR goes red until someone cuts a
release, whatever the PR contains. The user has decided to remove the drift rule. The other two checks
in the script stay:
- the two manifests must exist and agree;
- a tag argument must match their version (the release workflow relies on this).

## 2. Deletions (closed list)

**scripts/check-plugin-version.sh**
- **D1** Lines 2 and 7 of the header comment. Line 2 becomes `# Verifies plugin manifest versions:`.
  Line 7 (`# 3. First-parent feat commits since the manifest tag must not exceed max_feat_drift.`)
  is deleted. Line 8 (`# The release commit itself passes …`) is deleted too: it only explained the
  drift rule.
- **D2** Lines 11-14: the `max_feat_drift` comment, the variable, and the blank line after it.
- **D3** Lines 67-78: the blank line, the `tag="v$v1"` line, the tag-exists block (with its
  "skipping drift check" message), and the `n=$(git log …)` count and its `if` block. The script
  now ends after the tag-argument check (line 66 `fi`). Add nothing in their place.

**scripts/check-plugin-version_test.sh**
- **D4** Lines 25-50: the `stage_repo` helper and its comment (the blank line 51 goes too, so one
  blank line separates `stage` from `fail=0`).
- **D5** Lines 77-99: the four drift cases ("drift at the limit", "drift past the limit",
  "tag absent, drift would be past the limit", "feats behind a merge are not counted"), keeping one
  blank line before line 101.

**.github/workflows/ci.yml**
- **D6** Lines 32-34, the comment that says the drift check needs the release tags. Keep
  `fetch-depth: 0` itself: other steps may rely on full history, and removing it is not part of
  this change.

Everything else survives, including the manifest checks, the tag-argument check, the `stage` helper,
the six remaining test cases, and the release workflow's call with a tag.

## 3. Verify

- `sh scripts/check-plugin-version_test.sh` prints `check-plugin-version: ok` and exits 0.
- `sh scripts/check-plugin-version.sh` exits 0 on this repo (main is past the old limit).
- `sh scripts/check-plugin-version.sh v0.13.0` exits 0. `sh scripts/check-plugin-version.sh v9.9.9`
  exits 1.
- `shellcheck scripts/check-plugin-version.sh scripts/check-plugin-version_test.sh`, if shellcheck is installed.
- `git grep -n "max_feat_drift\|drift check"` finds nothing outside `docs/`.
- `make check`. It should now pass end to end. If a hook refuses it, run the three commands above
  plus `go vet ./...` and say so.

## 4. Report

List:
- the files changed, with each deletion's D-number;
- the verification outputs (exit codes).
