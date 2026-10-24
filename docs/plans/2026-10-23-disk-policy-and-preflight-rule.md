# Plan C2: disk-policy-and-preflight-rule

Part of the disk lane (#1314 runner temp dirs, #1308 Go build cache).

Independent. It can start now.

**Behaviour.**
- The policy gains a key `disk_min_free_gb`: an integer, default 5, `0` means the preflight is off, and it is validated like the other policy keys. It is shown in `relevo config` and editable in the settings form.
- A pure rule decides the preflight. Given the paths to check, the minimum and a free-bytes function, it returns nil or an `*EnvironmentError`.
- The error's text is exactly `environment: disk full: <path> has N GB free (< M GB)`. The first path under the minimum is named, and N is rendered to one decimal place.
- Free space comes from a small shared package rather than a second copy of the `statfs` code.

**Seams.**
- `internal/policy/policy.go`:
  - Add `DiskMinFreeGB *int` with the JSON name `disk_min_free_gb`, beside `ArtifactMaxMB`.
  - Add `DefaultDiskMinFreeGB = 5`.
  - Add the accessor `DiskMinFreeBytes()`.
- `internal/policy/validate.go`, `validateThresholds` (~312): reject a negative value, in the same shape as the `artifact_max_mb` line.
- `internal/relevo/configpolicy.go` (~122-128 and ~327): the key listing, default text and set/unset mapping, as `artifact_max_mb` has them.
- `internal/ui/settings_form.go` (~187-197): the settings field. Update the golden files under `internal/ui/testdata/` if the form renders it, and say so.
- New package `internal/diskfree`: `Free(path string) (uint64, error)`, with `_unix` and `_other` files taken from `internal/db/diskfree_unix.go` and `diskfree_other.go`.
  - Point `internal/db`'s `freeBytes` var (`cdc_drop.go` ~26) at it and delete the two `db` files.
  - This moves code between packages, so regenerate `testdata/coverage-baseline.txt` and say so.
- New `internal/relevo/environment.go`:
  - `EnvironmentError` carries a reason string. `errors.As` finds it, and `Error()` returns the text above.
  - `CheckDiskFree(paths []string, minBytes uint64, free func(string) (uint64, error)) error`. A path whose free space cannot be read is skipped with a log line and never fails the round.

**Steps.**
1. Add the policy key, validation, config listing and settings field. Deliverable: `go test ./internal/policy/... ./internal/relevo -run ConfigPolicy ./internal/ui/...` is green.
2. Add `internal/diskfree` and move `db`'s use. Deliverable: `go test ./internal/db/...` is green.
3. Add `environment.go` with its pure tests.
4. Regenerate the coverage baseline with `sh scripts/check-coverage.sh --write` and say so.
5. Run `make check-affected`.
6. Save the plan to `docs/plans/2026-10-23-disk-policy-and-preflight-rule.md` in the same commit.

**Tests.**
- Policy: default 5, negative refused, 0 accepted.
- Config listing: the row shows the default and the set/unset mapping.
- `TestCheckDiskFreeNamesFirstShortPath` with an exact-text assertion.
- `TestCheckDiskFreeAtThresholdPasses`.
- `TestCheckDiskFreeZeroMinimumDisabled`.
- `TestCheckDiskFreeUnreadablePathSkipped`.
- `TestEnvironmentErrorFoundByErrorsAs`, including through `fmt.Errorf("%w")` wrapping.

**Mutation targets.**
- Change `<` to `<=`: the boundary test must fail.
- Fail on an unreadable path: the skip test must fail.
- Drop the 0-disables branch: that test must fail.

**Deleted.**
1. `internal/db/diskfree_unix.go`
2. `internal/db/diskfree_other.go`

Both are replaced by `internal/diskfree`.

**Report must include.**
- That the coverage baseline was regenerated, and why.
- Any golden files that changed.

## Standing constraints (every plan carries them)

- CLAUDE.md applies.
  - Comments say *why* and never restate code.
  - No issue or PR numbers, and no "used to", in code or tests.
  - Functions are at most 70 lines; non-test files at most 600.
  - `headless.go` is over the limit and on the allow-list: do not grow it. New code goes in a new file, and a call-site addition there is paid for by moving the same number of lines out.
  - Add no lint or allow-list exclusion.
- Tests in `cmd/relevo` must not spawn a harness or reach the network, because CI has neither. Test rules as pure functions in `internal/relevo` or the owning package.
- Run only the focused tests of the packages you touched, fixing every error before the next run. Then run `make check-affected` once at the end. The chain runs the full gate (`make check`) after the last plan.
- A step that moves code between packages regenerates `testdata/coverage-baseline.txt` with `sh scripts/check-coverage.sh --write` and says so. No step lowers a baseline.
- Every plan's last step is to save the plan to `docs/plans/2026-10-23-<slug>.md` in the same commit.
- Never amend or rebase a pushed commit. Fix forward with new commits.
- If a premise below contradicts the code, halt and report. Do not improvise.
