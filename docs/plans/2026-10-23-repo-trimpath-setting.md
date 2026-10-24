# Plan C1: repo-trimpath-setting

Part of the disk lane (#1314 runner temp dirs, #1308 Go build cache).

Independent. It can start now.

**Behaviour.**
- A repository's `repos` config entry gains a `trimpath` key (values `on` or `off`; unset means on). `relevo config repo set trimpath off` opts a repository out, and `unset` restores the default.
- `relevo config repo` shows the effective value.
- The help text names the risk: with `-trimpath`, file paths in panics and `runtime.Caller` become module-relative, so tests that compare absolute source paths can break.
- A pure `GOFLAGS` helper is added. A caller supplies the tree and the effective setting, and the helper:
  - returns an env entry only when the tree has `go.mod` or `go.work` at its root and the setting is on;
  - appends ` -trimpath` to an existing `GOFLAGS` value;
  - does nothing when `-trimpath` is already in it, including `-trimpath=true`;
  - replaces nothing else.

**How `gate` is stored and read, and so how `trimpath` follows it.**
- `config.RepoEntry` (`internal/config/repos.go`): `Gate` is a string field, and `repoEntryFields` is the vocabulary. `parseRepoEntry` carries the "takes name, gate and board_theme" message.
- `cmd/relevo/config_repo.go`:
  - `repoEntryKeys` (~24).
  - `set` (~219) and `unset` (~295) switches.
  - `configRepoLines` (~376).
  - The list row builder (~368).
- `internal/relevo/repo.go`: `RepoSettings` and `RepoSettingsFor` (~127), which carry the entry to bind-time callers.

**Seams.**
- `internal/config/repos.go`: add `Trimpath string` to the entry, and add the key to `repoEntryFields`. `ParseRepos` refuses a value other than `on` or `off`, naming the section and entry. Update the "takes name, gate and board_theme" message.
- `cmd/relevo/config_repo.go`: the `set` and `unset` cases, `repoEntryKeys`, `configRepoLines` and the list row.
  - Show `trimpath on (default)` when unset, so the effective value is visible. Keep the file at or under 600 lines, or move the new cases to a new file.
- `internal/relevo/repo.go`: add `Trimpath` to `RepoSettings`, and a method `TrimpathOn()`.
- New `internal/relevo/goflags.go`: `goFlagsEntry(parent []string, tree string, on bool) []string`, where `parent` is the env the child inherits, and `isGoTree(dir)`.
- The CLI help for `config repo`, wherever the `gate` key is documented (grep `board_theme` in the help text and docs).

**Steps.**
1. Add the config field and parse validation. Deliverable: `go test ./internal/config/...` is green. Check `internal/config/never_syncs_test.go`, which encodes a repos body, in case the section's sync shape is asserted.
2. Add the CLI cases and help. Deliverable: `go test ./cmd/relevo -run ConfigRepo` is green. The test uses only in-process config verbs, with no harness and no network.
3. Add `RepoSettings.Trimpath` and `goflags.go` with its pure tests.
4. Run `make check-affected`.
5. Save the plan to `docs/plans/2026-10-23-repo-trimpath-setting.md` in the same commit.

**Tests.**
- Config:
  - `TestParseReposTrimpath`: accepts on and off, refuses `maybe`, and the entry round-trips through `EncodeRepos`.
  - An entry holding only `trimpath: on` is not dropped as empty. If it were stored as the default, it would be dropped by the empty rule, so decide whether to store `on` explicitly or treat it as unset, and state the choice.
- CLI: `config repo set trimpath off`, `unset`, and the shown effective value in both unset and off states.
- `goFlagsEntry` table:
  - No GOFLAGS → `GOFLAGS=-trimpath`.
  - Existing `-mod=vendor` → appended.
  - Already contains `-trimpath` → none.
  - `-trimpath=false` is treated as present (document this).
  - No `go.mod`/`go.work` → none.
  - Setting off → none.
  - `go.work` only → on.

**Mutation targets.**
- Drop the `isGoTree` guard: the non-Go-tree case must fail.
- Overwrite instead of append: the existing-GOFLAGS case must fail.
- Ignore the opt-out: the off case must fail.

**Deleted.** None.

**Report must include.**
- The stored form chosen for `on`.
- The finding that the serve runtime has no `Config`: `cmd`'s `newRuntime` sets it, `internal/serve/serve.go` `runtimeAt` does not. A served round therefore cannot read the opt-out and always trims. The MasterMind should record this limit; C3 states it again.

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
