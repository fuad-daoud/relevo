# Cleanup P2-1a -- extract `internal/spawn` from `internal/relevo/runner.go`

Phase 2 of the codebase cleanup (spec `docs/specs/2026-09-25-codebase-cleanup-design.md`,
§5 "exec/" -- named `spawn` here because a package called `exec` collides with
the standard library's `os/exec`, which proc and several tests import).
Base: `main` at `c8d3a51d`.

## 0. Rules

- **Run every command in the foreground and wait for it.** Never a background
  task, `&`, a scheduled wakeup, or "I'll wait for the notification": you are a
  headless process; when you end your turn the process exits and the round is
  lost. Do not end your turn until the report and done marker exist.
- **No behaviour change.** This round moves declarations and requalifies their
  uses. The only non-mechanical edits allowed are the ones §4 lists.
- **golangci-lint v2.14.0 must actually run.** If `golangci-lint version` is not
  2.14.0, `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0`
  and put `$(go env GOPATH)/bin` on PATH. Use `--allow-parallel-runners`.
- **Keep every `t.Parallel()` call.** Golden files and `*contract_test.go` are
  not edited.
- If a step is impossible as written, or the code contradicts this plan, stop
  and report. Never add a lint exclusion, `//nolint` or allow-list entry.

## 1. What moves (all of `internal/relevo/runner.go`, which imports only `internal/policy`)

`ProcSpec`, `ScopeSpec`, `GoMaxProcsFor`, `RusageTrailerPrefix`, `ProcRusage`,
`ProcHandle`, `Runner`, `ScopeProber`, `ErrRunnerUnavailable` -> new package
`internal/spawn` (file `internal/spawn/spawn.go`), names unchanged. Its test file
`internal/relevo/runner_test.go` moves to `internal/spawn/spawn_test.go`.
`runner.go` is deleted. Helpers that read `Runtime` or `store.Binding` stay in
relevo (`handleOf`, `scopeFor`, `scopeKind`, `scopeUnitName`, `ErrScopeActive`).

## 2. One home for the trailer formats

Add to `internal/spawn`: `const ExitTrailer = "relevo-exit:"` (next to the moved
`RusageTrailerPrefix = "relevo-rusage:"`). Then replace every duplicate with the
spawn constant and delete the duplicate:

- `internal/proc/proc.go:27` and `internal/proc/proc_other.go:15` (`ExitTrailer`),
  `internal/proc/scope.go:137` (`RusageTrailer`);
- `internal/store/seal.go:229` `ExitTrailer` and `:232` `rusageTrailer` (drop the
  comment at :224-228 that explains why they were duplicated);
- `internal/usage/source.go:73` `exitTrailer` (and `usage.ExitTrailerForTest`);
- `internal/transcript/trailers.go:13-14`.

Exported names that other packages use (`proc.ExitTrailer`, `store.ExitTrailer`)
are replaced at their call sites by `spawn.ExitTrailer`, not kept as aliases.
If making one of these packages import `spawn` creates an import cycle, keep that
package's duplicate, say so, and continue.

Pin tests that only asserted two duplicates were equal now compare a constant
with itself: delete them and list them in the report
(`internal/store/exit_trailer_ext_test.go` `TestExitTrailerMatchesProc`,
`internal/proc/proc_test.go` `TestExitTrailerMatchesUsage` and
`TestRusageTrailerPrefixMatchesProc`). **Keep** the pins of the supervisor shell
script literals (`proc.go:81,87`, tested at `proc_test.go:928,937`), rewritten
against `spawn.ExitTrailer` / `spawn.RusageTrailerPrefix`.

## 3. Requalify the uses (script it)

Outside relevo, every `relevo.<Moved>` becomes `spawn.<Moved>`; inside relevo,
every bare `<Moved>` becomes `spawn.<Moved>`. Use
`gofmt -w -r 'relevo.ProcSpec -> spawn.ProcSpec' <files>` (one rule per
identifier) outside relevo, `gofmt -w -r 'ProcSpec -> spawn.ProcSpec'
internal/relevo/*.go` inside, then `goimports -w` on every touched file
(`~/go/bin/goimports`). Where a file also imports `os/exec`, there is no clash
(the new package is `spawn`). The field `Runtime.Runner` keeps its name; only its
type becomes `spawn.Runner`.

Known uses (origin/main): relevo ask.go, bind.go, gate.go, headless.go,
runtime.go, send.go, verify.go and many tests (fake_test.go, ask_test, cpus_test,
gate_test, headless_test, send_test, verify_test, daemon_test, reconcile_test);
cmd/relevo/serve.go + serve_test.go; internal/serve serve.go, rounds.go and tests;
internal/proc proc.go, proc_other.go, scope.go, env.go and tests (proc can then
drop its relevo import entirely); internal/e2e fakes_test/headless_test/
remote_test/tier_test; internal/mcp/verbs_test.go; internal/remote/client/
helpers_test.go. `go build ./... && go vet ./...` finds any you miss.

## 4. The new package is born clean

`internal/spawn` gets **no** lint exclusion and no allow-list entry: its comments
follow CLAUDE.md "### Code style" (a 1-3 line package comment; *why* only; no
issue numbers, `§` or history; no restating names), functions at most 70 lines.
Rewriting the moved declarations' comments to that standard is the one
non-mechanical edit allowed. Delete `internal/relevo/runner.go`'s entries from
`scripts/check-comments.allow` / `check-filesize.allow` if present.

## 5. Coverage baseline

Code moved between packages, so regenerate: `make check` (it writes
`.coverage.txt`), then `sh scripts/check-coverage.sh --write`. Then restore every
line for a package this round did not touch to its old value (only relevo,
spawn, proc, store, usage, transcript, serve lines may change), and confirm no
touched package dropped more than 1.0 point without the move explaining it (code
leaving a package can raise or lower its percentage; report every change).

## 6. Steps

1. Create `internal/spawn`, move §1, add `ExitTrailer`. `go build ./internal/spawn/...`.
2. Requalify (§3). `go build ./... && go vet ./...`.
3. Trailers (§2). Build, vet.
4. Comments of the new package (§4). `golangci-lint run --allow-parallel-runners ./internal/spawn/...`
   0 issues; `sh scripts/check-comments.sh`, `sh scripts/check-filesize.sh` ok.
5. `go test ./internal/spawn/... ./internal/proc/... ./internal/store/... ./internal/usage/... ./internal/transcript/... ./internal/serve/... ./internal/relevo/... -count=1`.
6. Coverage baseline (§5); `make check` (foreground) passes; `golangci-lint run
   --allow-parallel-runners ./...` 0 issues.
7. `grep -rn --include='*.go' -E '"relevo-(exit|rusage):"' .` shows only
   `internal/spawn/spawn.go` and any duplicate §2 allowed to stay (report why).
8. Copy this plan to `docs/plans/2026-09-26-cleanup-p2-spawn.md`; commit once.

Report: files touched per package, duplicates removed (and any kept, why),
tests deleted with the reason, coverage lines changed (old -> new).
