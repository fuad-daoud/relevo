# Fix `TestGitShimRefusesEveryStashForm` ETXTBSY flake (issue #1382)

> Builder override (MasterMind, 2026-10-26): do NOT run `make check` or `make check-affected` in this round. Run only the targeted tests named in the plan. The MasterMind runs `make check-affected` after the round closes.

Scope: `internal/relevo/gitshim_test.go` (one retry helper, three exec
sites, one helper unit test) and `.github/workflows/flake-hunt.yml`
(one suite entry). Production code is NOT changed. Record copy of this
plan: `docs/plans/2026-10-26-gitshim-etxtbsy.md`.

## Behaviour and cases

1. Every test-side exec of a freshly written shim retries while the
   failure is `syscall.ETXTBSY` and stops retrying otherwise. Retryable
   is exactly `errors.Is(err, syscall.ETXTBSY)` on the error from
   `cmd.Run`, `cmd.Output`, or `cmd.CombinedOutput`; a
   `*exec.ExitError` (real refusal, exit 1/7/127) is never retried, and
   any other error fails the test at once.
2. Each retry attempt builds a fresh `*exec.Cmd` (a `Cmd` cannot be
   re-run); the helper takes a closure that constructs the command, so
   the rebuild happens inside the loop.
3. Bound: total wait of a few seconds with millisecond sleeps between
   attempts (5ms step, matching `pollUntil` in
   `internal/db/helpers_test.go:351-368`); exhaustion returns the last
   error and the calling test fails as today. The window being papered
   over is one fork holding a write descriptor across an exec, so a
   short bound suffices and a long one would mask real breakage.
4. All three exec sites in `gitshim_test.go` go through the one helper,
   so a future exec site inherits the retry: `shimRun`
   (`gitshim_test.go:43-58`, `t.Fatalf` at :55 is where the flake
   surfaces), the `git` closure's `CombinedOutput` at :168-174
   (surfaces at the `t.Fatalf` at :183-185), and the
   `exec.CommandContext` + `Output` at :229-233. One helper, not three
   copies.
5. Flake-hunt gains a suite entry running the shim tests by exact name
   in `internal/relevo`, following the existing matrix shape
   (`.github/workflows/flake-hunt.yml:35-59`, step at :67-68). The
   workflow's own rule applies: a failure there means fix the test, do
   not rerun it green.

## Seams (read these before touching)

- `internal/relevo/gitshim.go:82-115` — `ensureGitShim`: `CreateTemp`
  at :96, `WriteString` at :101, `Close` at :105, `Chmod` at :108,
  `Rename` at :111. The temp file is closed before the rename, so a
  concurrent exec never sees a half-written file; production is fine
  and stays untouched. The flake is test-side only: a parallel
  in-package fork landing between `CreateTemp` and `Close` holds the
  write descriptor until its exec, and the test's own immediate exec
  of the shim then fails ETXTBSY. Do not widen into production.
- `internal/relevo/gitshim_test.go:41-58` — `shimRun` (add the retry
  here, keep the `*exec.ExitError`-vs-rest split at :52-56).
- `internal/relevo/gitshim_test.go:168-174` — `git` closure in
  `TestGitShimRefusesANetworkPushButNotALocalOne` (rebuild the `Cmd`
  per attempt; env/dir setup at :162-171 stays outside the retried
  closure except the `exec.Command` itself).
- `internal/relevo/gitshim_test.go:222-240` —
  `TestGitShimNestedUnderAnotherShimReachesTheRealGit` (the
  `CommandContext` at :231 keeps its 10s timeout at :229-235; the
  ETXTBSY retry nests inside it, it does not replace it).
- `internal/relevo/gitshim_test.go:60-75` — `shimFixture`
  (the shared `ensureGitShim` caller; needs no change).
- `.github/workflows/flake-hunt.yml:35-59` — matrix shape to mirror;
  `:67-68` — the `go test -race -count` invocation the new entry runs
  under.
- `internal/db/helpers_test.go:351-368` — `pollUntil` (5ms step,
  deadline-aware); the only existing test retry-shape to stay
  consistent with. There is no existing ETXTBSY helper anywhere in the
  repo (no `ETXTBSY` or `syscall` match in `internal/`), so the helper
  is new and lives in `gitshim_test.go`, next to its only callers.

## Sibling search (evidence, not scope)

- `ensureGitShim` callers in `*_test.go`: only `gitshim_test.go`
  (:65, :159, :226, :245, :254, :265). No other test writes-then-execs
  the git shim.
- Other write-0755-then-exec tests exist in other packages (e.g.
  `internal/git/client_test.go:88`, `internal/git/sync_test.go:256`,
  `internal/hooks/executor_test.go:69,122,161`,
  `internal/proc/helpers_test.go:48`,
  `internal/doctor/coverage_test.go:93`) but they write unrelated
  stubs via `os.WriteFile`, not the git shim under parallel
  in-package forking, and the issue's scope decision covers only the
  shim tests. Name them in the report as checked; change none of them.

## Steps

1. Add one ETXTBSY-retry helper in `gitshim_test.go` (predicate +
   bounded loop, fresh `Cmd` per attempt via a constructor closure;
   ≤70 lines, no issue numbers in comments) — done when `gofmt -l`
   on the file is clean and the package builds.
2. Route `shimRun` (`gitshim_test.go:43-58`) through the helper —
   done when `TestGitShimRefusesEveryStashForm` and the other
   `shimRun` callers pass unchanged in a single focused run.
3. Route the `git` closure at :168-174 and the `CommandContext` site
   at :229-233 through the same helper — done when every exec of a
   freshly written shim in the file goes through it (grep shows no
   bare `Run`/`Output`/`CombinedOutput` on a shim path outside the
   helper).
4. Add one small unit test for the helper pinning: an operation that
   fails ETXTBSY once then succeeds is retried to success; an
   `*exec.ExitError` is never retried; exhaustion returns the last
   error (fake operation, no real ETXTBSY needed) — done when it
   passes and its name says what it pins.
5. Add the flake-hunt suite entry (`pkg: ./internal/relevo`,
   `-run` matching the shim tests by exact name, matrix shape as at
   `.github/workflows/flake-hunt.yml:35-59`) — done when the YAML
   parses and the entry mirrors the existing `name`/`pkg`/`run` keys
   (verify by diffing the stanza against a sibling entry).
6. Run `go test ./internal/relevo/ -run 'TestGitShim' -count=20`,
   and the `-race` variant per flake-hunt style — done when both
   pass.
7. Mutation check: neuter the retry (predicate matches nothing) and
   show the helper unit test fails while the suite still exercises
   the path; restore — done when the fail-then-pass pair is observed.
8. Save a copy of this plan at
   `docs/plans/2026-10-26-gitshim-etxtbsy.md` — done when the file
   exists verbatim.

Do NOT run `make check` or `make check-affected`; the MasterMind
gates after the round. Commit as new commits only, never amend or
rebase.

## Deleted

Nothing is deleted or removed by this work: no files, no tests, no
branches, no behaviour. The change is purely additive in tests plus
one workflow matrix entry.

## Report must include

- `git diff --stat` against this plan's declared scope (two files
  plus the plan record; production untouched).
- The focused `-count=20` runs (plain and `-race`) with pass output.
- The mutation check's fail-then-pass observation.
- The sibling-search result: `ensureGitShim` callers found only in
  `gitshim_test.go`; other write-then-exec tests named as checked
  and unchanged.
- Confirmation that no test or comment carries an issue/PR number or
  history, and every touched function stays within limits.
