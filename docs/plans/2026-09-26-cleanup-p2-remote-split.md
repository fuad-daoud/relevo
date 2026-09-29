# Cleanup P2-6 -- split `relevo/remote.go` into focused files; move `pull.go` into `internal/delivery`

Phase 2 of the codebase cleanup. The spec (§5) planned to move the remote
client code into `internal/remote`; a survey showed that is not feasible without
~16 injected functions (it calls back into reconcile's halt/queue/deliver
paths, returns relevo's verb result types, and a package under
`internal/remote` would cycle through `internal/remote/client`). So this round
does the readable part instead: split the 1,500-line file by concern, inside
`internal/relevo`, and move the one genuinely separable piece, `pull.go`.
Base: `main` at `3cf2ad3f`.

## 0. Rules

- **Run every command in the foreground and wait for it.** Never a background
  task, `&`, a scheduled wakeup, or "I'll wait for the notification": you are a
  headless process; when you end your turn the process exits and the round is
  lost. Do not end your turn until the report and done marker exist.
- **No behaviour change.** Declarations move and are requalified; names change
  only where this plan says. Stored formats (KV keys, JSON, DB rows) and every
  golden / `*contract_test.go` stay byte-identical.
- **golangci-lint v2.14.0 must actually run** (install with
  `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0` if
  `golangci-lint version` differs); use `--allow-parallel-runners`.
- **Touch only the files the plan names.** CI configuration, `scripts/`
  (other than the lint allow-lists), `Makefile` and `.github/` are out of scope
  even if a move seems to call for them; if one does, stop and report.
- **Keep every `t.Parallel()` call.**
- **Do not rebase or fetch.** This tree may have no `origin`; the planner rebases.
- Script mechanical requalification (`gofmt -w -r 'old.X -> new.Y' <files>`, then
  `~/go/bin/goimports -w`), then fix what the build reports.
- New packages are born clean: no lint exclusion, no allow-list entry; their
  comments follow CLAUDE.md "### Code style" (1-3 line package comment; *why*
  only; no issue numbers, `§` or history; no restating names); functions at most
  70 lines. Rewriting moved comments to that standard is allowed.
- Coverage: code moves between packages, so after `make check` run
  `sh scripts/check-coverage.sh --write`, then restore every line for a package
  the round did not touch. Report each changed line.
- If a step is impossible as written, or the code contradicts this plan, stop
  and report. Never add a lint exclusion, `//nolint` or allow-list entry.

## 1. Split `internal/relevo/remote.go` (moves only, same package)

Move whole top-level declarations -- text unchanged, doc comments included --
into new files in `internal/relevo`, grouped by concern. Use a throwaway mover
script (go/parser byte ranges, as the earlier cmd/relevo split did), then
`goimports -w`:

| New file | Declarations (from remote.go) |
|---|---|
| `remote_add.go` | `addRemote`, `remotePickEntry` and the helpers only they use |
| `remote_send.go` | `sendRemote` and the helpers only it uses |
| `remote_sync.go` | `unreachableGrace`, `checkedOutWarned`, `writeTempAndRename`, `maxMirrorBytes`, `mirrorLog`, `mirrorDriftOnce`, `liveFactsOf`, `applyRemote`, `applyRemoteErr`, `applyRemoteView`, `observeRemote`, `reconcileRemote`, `SyncRemote`, `SyncRemoteUnlessDaemon`, and the catch-up group (`catchUp`, `applyCatchUpFiles`, `applyCatchUpAbsorb`, `applyCatchUpSettle`, `applyCatchUpReport`, `catchUpPayload`) |
| `remote_gates.go` | `ForwardUnavailable`, `ForwardAvailable`, `ServerInUse`, `matchCandidateView` |
| `servers.go` | `ServerProbe`, `ProbeServers`, `probeStatusText`, `ServerTierWarning`, `RenderServers` |
| `remote.go` (what remains) | `ErrServerPreTier`, `ErrNoGitIdentity`, `orText`, `is40Hex` and anything not listed |

Use the declaration names that exist in the file at the base commit; if one
listed here does not exist or another obviously belongs to a group, place it
by the table's intent and list it in the report. Every new file must be at most
600 lines; if `remote_sync.go` exceeds that, split the catch-up group into
`remote_catchup.go`. `remotefetch.go` stays as it is. Tests stay where they are
(`remote_test.go` is one test file for the whole cluster; do not split it).

Allow-lists: `remote.go` is in `scripts/check-comments.allow` (and
`check-filesize.allow` if listed); a new file that still carries a
`#NNN`/`§` comment goes into `check-comments.allow` as a rename of that entry
(the moved comments are rewritten in phase 3). Remove `remote.go`'s
`check-filesize.allow` entry once it is under 600 lines.

**Prove moves-only:** the sorted multiset of non-blank, non-import,
non-package lines of `remote.go` at the base equals that of the six files
after (`diff` empty). Report the command and result.

## 2. `pull.go` -> `internal/delivery`

`internal/relevo/pull.go` (`Pull`, `busyRetryDelays`, `retryBusy`,
`pullPending`, `pullPendingThrough`) only needs `delivery.PushText`,
`delivery.MaxPushBytes`, `rt.Store` and `db.ErrBusy`. Move it to
`internal/delivery/pull.go` taking `d Deps` (or `*store.Store` where only the
store is used) instead of `Runtime`. `pull_test.go` moves too (its helpers
`routeRuntime` / `seedPending` already have copies in delivery's tests).
Callers: `internal/relevo/wait.go` (`pullPendingThrough`, export it as
`PullPendingThrough`), `internal/ui/actions.go` (`Pull`), and any others the
build reports.

## 3. Steps

1. §1 with the mover; `go build ./... && go vet ./internal/relevo/...`; the
   moves-only proof.
2. §2; `go build ./... && go vet ./...`.
3. `go test ./internal/relevo/... ./internal/delivery/... ./internal/ui/... -count=1`.
4. `golangci-lint run --allow-parallel-runners ./...` 0 issues;
   `sh scripts/check-comments.sh`, `sh scripts/check-filesize.sh` ok.
5. Coverage (§0 of the rules): `make check`, `--write`, restore untouched
   packages' lines; `make check` (foreground) passes.
6. Copy this plan to `docs/plans/2026-09-26-cleanup-p2-remote-split.md`; commit once.

Report: final file list with line counts, the moves-only proof, allow-list
changes, pull.go's new API, coverage lines changed.
