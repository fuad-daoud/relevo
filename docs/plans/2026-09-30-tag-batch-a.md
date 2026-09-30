# Plan: tag batch A — three tag-blocking defects in one builder round

**Round shape:** one builder round on branch `relevo/tag-a-build`, three focused commits (one per issue, each naming it) plus the plan doc, one `make check` green at the end. No push, no PR. CI has no harness binary and no network at test time, so every new test here is pure or fixture-based.

**Input:** the worktree at HEAD `29b14ae1`, tracked tree clean (only untracked `docs/plans/*.md` from other rounds). Read the three files first. None of the three issues is fixed there — verified by reading:
- `scripts/check-filesize.sh:34-37` still lists tracked files only.
- `cmd/relevo/bind.go:337-340` prints the local `bound` line for every fresh bind.
- `scripts/test-shard.sh:143,155` always pass `-cover`, and the `test-macos` job (`.github/workflows/ci.yml:189-243`) sets no toggle.

## 1. What changes

**1. `scripts/check-filesize.sh` (#758) — the scan also sees untracked, non-ignored files.**
- Lines 34-42: keep `git ls-files -- '*.go' ':!:*_test.go' > "$work/all"` (line 37) and append `git ls-files --others --exclude-standard -- '*.go' ':!:*_test.go' >> "$work/all"`. The two lists cannot overlap (index vs untracked), so no sort or dedupe; `--exclude-standard` drops ignored paths, the `:!` pathspec drops test files. Update the comment above to say "every tracked non-test Go file plus every untracked, non-ignored one". Allow-list semantics (lines 38-42) untouched. Verified in a scratch repo: an untracked `fixtures/big.go` is listed, `fixtures/big_test.go` is not, a `.gitignore`d file is not.
- `scripts/check-filesize_test.sh`: add a `put_untracked` helper next to `put` (lines 38-43) that copies a fixture into `$work/repo/fixtures` and commits nothing, then four cases before the final verdict (line 117):
  - untracked over-limit fails: `stage; put_untracked big.go; run` → exit 1 and `fixtures/big.go: 601 lines (max 600)`;
  - untracked under-limit passes: `stage; put_untracked small.go; run` → exit 0 and `check-filesize: ok`;
  - untracked ignored file is not scanned: write `.gitignore` holding `fixtures/big.go`, `put_untracked big.go`, `commit` (stages only `.gitignore`), `run` → exit 0;
  - untracked allow-listed file is not scanned: `put_untracked big.go`, write `scripts/check-filesize.allow` holding `fixtures/big.go`, `run` → exit 0.
  POSIX sh only. Acceptance: `sh scripts/check-filesize_test.sh` prints `check-filesize: ok`; `make check-scripts` green.

**2. `cmd/relevo/bind.go` + `internal/relevo/text.go` (#756) — a remote fresh bind names its server.**
- `internal/relevo/text.go`, after `RestoreText` (line 63): add the exported clause helper `PlacementText(p PlacementResolution) string` — `""` unless `p.How == placementHowActor`, otherwise `placementClause(p)` (`internal/relevo/candidate.go:426`), the exact bytes the pick note already appends. `placementClause` stays unexported and unedited, so the pick note's text is byte-identical.
- `cmd/relevo/bind.go`: replace the unconditional `fmt.Printf` at lines 337-340 with a call to a new pure helper `boundLine(b store.Binding, mastermind, candidate string, placement relevo.PlacementResolution) string`, placed next to `runBind`. It branches on `b.Builder.Remote()`:
  - remote: `bound <name>: mastermind <mm> -> builder <candidate> on <server>, round N` (mirrors `runAdd`'s `added %s: builder %s on %s`, line 413), with `relevo.PlacementText(placement)` appended — present only when the actor's placement list chose the server, absent for an explicit `--server`;
  - otherwise: today's line byte-identical (`builder <builderWhere> (<candidate>), round N`).
  `--json` untouched (`bindDocOf`, `cmd/relevo/writejson.go:36`); the resume/rebind branch untouched.
- Tests, both pure — no subcommand, no runtime, no config/state, no network (CLAUDE.md's cmd/relevo rule):
  - `cmd/relevo/bind_test.go` (new), `TestBoundLineNamesTheRemoteServer`: remote + actor placement ends `on zen, round 1; placement zen (actor); skipped backup (unreachable)`; remote + explicit/zero placement ends `on zen, round 1` with no clause; local keeps `builder headless (flash), round 1`.
  - `internal/relevo/placement_test.go`, near `TestPickNotesNameThePlacementOnlyWhenItExists` (line 483): `TestPlacementTextNamesOnlyTheActorsOwnChoice` — actor → the clause, explicit → `""`, zero → `""`.
  Stated choice: the wording is pinned through the formatting helper, not through `runBind`, because no live server exists in tests.
- Acceptance: `go test -count=1 -run 'TestBoundLine|TestPlacementText' ./cmd/relevo ./internal/relevo` passes; gofmt clean; the human line names the server and, when the placement chose it, the clause.

**3. `scripts/test-shard.sh` + `.github/workflows/ci.yml` (#757) — no coverage instrumentation on the macOS legs.**
- `scripts/test-shard.sh`: add the `SHARD_COVER` toggle (unset or any value but `0` = today; `0` = off), documented in the header `Env:` block (lines 9-12). Both coverage flags are gated by it: `-cover` on the whole-package job (line 143) and `-coverprofile="$outdir/split-$base.out"` on the split jobs (lines 155-156) — `-coverprofile` implies coverage, so it must disappear with the toggle. `-race -count=1` stays on every job. Optional args rely on word splitting, so keep/extend the `# shellcheck disable=SC2086` comment where the edit needs it. `--dry-run` output stays byte-identical.
- `.github/workflows/ci.yml`, `test-macos` job (lines 189-243): add `SHARD_COVER: '0'` as step env on the `test shard` step (lines 227-228) with a one-line comment (darwin's coverage report intermittently fails after a passing run; the `coverage` job reads only the ubuntu/stable shards' artifacts, `if:` at line 177). The ubuntu `test` job and the `coverage` job get no edit.
- `scripts/test-shard_test.sh`: no existing case asserts the command line (every assignment case is `--dry-run`, lines 24-27), so add one, in the file's existing shim style (lines 95-128): a `go` stand-in in `$work/bin` that appends each `go test` invocation's argv as one line to `$SHIM_LOG` and exits 0, and otherwise `exec`s the real path from `command -v go`. Run `test-shard.sh 0 3 "$work/shard"` from `$repo` with `PATH="$work/bin:$PATH"` — default: at least one line carries `-coverprofile=`, every line carries `-cover -race -count=1`; with `SHARD_COVER=0`: no line carries `-cover` or `-coverprofile`, every line still carries `-race -count=1`. Both runs exit 0; outdirs live under `$work`, never in the repo.
- Acceptance: `sh scripts/test-shard_test.sh` prints `test-shard: ok`; `sh scripts/test-shard.sh --dry-run 0 3` unchanged; `make check-scripts` green.

## 2. Ordered steps (done-when)

1. #758 script edit plus the four test cases → `sh scripts/check-filesize_test.sh` prints `check-filesize: ok`; commit 1 `fix(scripts): check-filesize also scans untracked non-test Go files (#758)`.
2. #756 `PlacementText` and `boundLine` plus both tests → the focused `go test` passes; commit 2 `fix(relevo): a placement-chosen remote bind names its server (#756)`.
3. #757 toggle, ci.yml step env, shim case → `sh scripts/test-shard_test.sh` prints `test-shard: ok`; commit 3 `fix(ci): the macOS shards run without coverage instrumentation (#757)`.
4. Save this plan verbatim as `docs/plans/2026-09-30-tag-batch-a.md`; commit 4 `docs(plans): the tag batch A plan (#756, #757, #758)`.
5. `make check` once, at the end → green; `git status` clean.

## 3. Mutation pins

- Delete the `git ls-files --others` line → the two untracked cases in `check-filesize_test.sh` fail (ignored/allow-listed keep passing).
- Drop `PlacementText`'s actor filter (return the clause for every How) → `TestPlacementTextNamesOnlyTheActorsOwnChoice` and the explicit case of `TestBoundLineNamesTheRemoteServer` fail.
- Restore the single Printf (no `Remote()` branch) → `TestBoundLineNamesTheRemoteServer` fails on the server-missing case.
- Always pass `-cover` (ignore `SHARD_COVER`) → the shim case's `SHARD_COVER=0` run records `-cover`/`-coverprofile` and fails.
Each mutation is reverted after it fails.

## 4. Files this round touches (closed)

1. `scripts/check-filesize.sh`
2. `scripts/check-filesize_test.sh`
3. `cmd/relevo/bind.go`
4. `cmd/relevo/bind_test.go` (new)
5. `internal/relevo/text.go`
6. `internal/relevo/placement_test.go`
7. `scripts/test-shard.sh`
8. `scripts/test-shard_test.sh`
9. `.github/workflows/ci.yml`
10. `docs/plans/2026-09-30-tag-batch-a.md` (new, this plan)

Halt on anything else: do not touch `runAdd`, the resume/rebind line, the cockpit's `Bind` action (`internal/ui/actions.go:335`), the ubuntu `test` matrix, the `coverage` job, `Makefile`, or `scripts/check-filesize.allow` (no new exclusion).

## 5. Deleting behaviour (closed list)

Nothing is deleted: (1) no file, function, test, fixture or allow-list entry is removed; (2) the only dropped behaviour is coverage instrumentation on the macOS legs, set in `ci.yml` env and not replaced there. Nothing else.

## 6. Rules

- `make check` once, at the end; it is the gate and it runs `check-scripts` (shellcheck is installed here).
- POSIX sh for all four scripts, no bashisms. Comments say why; no issue numbers, no "round N" in code.
- No coverage baseline moves; no new `check-filesize`/`check-comments` exclusion; no file crosses 600 lines.
- The cmd/relevo test calls the pure `boundLine` only: no subcommand, no harness, no network, no real config or state.
- Commit each issue before starting the next so each diff is exactly one issue.

## 7. What the report must include

- The four commit hashes and subjects, and `git diff --stat` for the round.
- #758: the new cases, the mutation result, `make check-scripts` output.
- #756: the exact new `bound` lines for remote (placement-chosen and `--server`) and the unchanged local line; the statement that the wording is pinned through the pure helper because no live server exists in tests; confirmation `--json` and `runAdd` are untouched.
- #757: the `ci.yml` step diff; the recorded `go test` argvs in both toggle states (coverage only by default, `-race` in both); confirmation `--dry-run` is unchanged; a note that the macOS flake itself is only confirmable in CI.
- `make check` result and coverage numbers; `git status` clean; nothing outside §4 touched.
- Not done (deliberate): the actor-chosen *local* bind line gets no placement clause (the issue scopes the clause to the remote branch); the resume/rebind line and the cockpit's add line keep `builderWhere`; no doc or spec edits beyond this plan.
