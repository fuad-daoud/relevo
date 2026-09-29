# Cleanup P2b -- split `cmd/relevo` into one file per verb (moves only)

Round of the codebase cleanup (spec: `docs/specs/2026-09-25-codebase-cleanup-design.md`,
§5 and §7 phase 2b). Base: `main` at `fa0e60e`.

## 0. Rules

- **Run every command in the foreground and wait for it.** Never a background
  task, `&`, a scheduled wakeup, or "I'll wait for the notification": you are a
  headless process, and when you end your turn the process exits and the round
  is lost. Do not end your turn until the report and the done marker exist.
- **Moves only.** Every top-level declaration keeps its exact text, doc comment
  included -- no body, signature, name or comment changes. The only edits
  besides moving are `package`/`import` blocks. `git diff --stat -M` should show
  pure line moves; the report proves it (§6 step 5).
- **Do not touch `cmdUI`, `uiStart`, `runPick`, `pickNamesNothing`** or any
  file under `internal/`: another planner is working on the cockpit.
- If a step is impossible as written, or the code contradicts this plan, stop
  and report -- do not improvise.
- `cmd/relevo` tests must not spawn a harness or reach the network.

## 1. Why

`cmd/relevo/main.go` is 3,055 lines holding every verb; `config.go` (1,117),
`serve.go` (1,074) and `doctor.go` (931) are also over the 600-line file limit.
One file per verb is the layout the spec asks for, and it makes phase 3's
per-function cleanup reviewable.

## 2. The manifest for `main.go` (names as of `fa0e60e`)

Each declaration moves with its doc comment to the file named. Anything not
listed stays in `main.go`.

| New file | Declarations |
|---|---|
| `version.go` | `buildVersion`, `releaseInputs`, `statusNotice` |
| `wire.go` | `resolveHooksConfig`, `hooksRunLog`, `newHooksDispatcher`, `opencodeServiceFiles`, `opencodeDBPath`, `captureAgyEnv`, `newDeliverers`, `newRuntime`, `openDB`, `newRuntimePeek`, `configFilesPresent`, `fileExists`, `buildRuntime`, `procStartUnix`, `newRemoteClient` |
| `candidates.go` | `noteConsultRolesTooLong`, `notePick`, `roleOrBuilder`, `builderWhere`, `cmdCandidates`, `formatCandidates`, `legacyGatesPath`, `loadHistory`, `formatPolicy` |
| `gate.go` | `parseFor`, `cmdGate`, `gateList`, `gateUnavailable`, `gateClear`, `gateServe` |
| `bind.go` | `bindFlags`, `bindRoute`, the `const` block of `bindRoute` values that follows it, `bindRouteFor`, `bindFlagValues`, `bindFlagSet`, `cmdBind`, `runBind`, `runAdd` |
| `unbind.go` | `cmdUnbind`, `runSweep`, `runGC`, `gcResolver`, `plannerLabel` |
| `send.go` | `cmdSend` |
| `ask.go` | `askFlagValues`, `askFlagSet`, `cmdAsk` |
| `status.go` | `scopeReport`, `filterReport`, `filterReportPlanner`, `cmdStatus`, `runStatusline` |
| `show.go` (exists) | append `printDiff`, `printLog` |
| `wait.go` | `cmdWait` |
| `done.go` | `cmdDone`, `cmdStop` |
| `daemon.go` | `cmdDaemon`, `refreshRoles`, `sameFileID` |
| `args.go` | `explicitBinding`, `bindingHint`, `firstLine`, `withEnv`, `bindingArg`, `resolveBinding`, `warnWaitingOnYou`, `parseFlags`, `regateFlag`, `noteRegateNoGate`, `isTerminal`, `bareArgs` |

`main.go` keeps: `version`, `distribution`, `usage`, `exitCodeErr` and its
method, the `var` block after it, `main`, `run`, `removedVerbs`,
`userConfigRoot`, and the four cockpit-entry functions in §0.

If a file named above already exists (other than `show.go`), stop and report.

## 3. The other oversized files

Split `config.go`, `serve.go` and `doctor.go` the same way (whole declarations,
text unchanged) into files of at most 600 lines each, grouped by subcommand:
for example `config.go` -> `config.go` (dispatch, show) + `config_edit.go`
(edit/get/set/unset) + `config_io.go` (export/import/init/agents) +
`config_server.go` (server/secret); `serve.go` -> `serve.go` (run) +
`serve_admin.go` (init/enroll/clients/revoke/fingerprint/status/gc/unbind/ui)
+ `serve_show.go` (owner reads); `doctor.go` -> `doctor.go` + `doctor_checks.go`.
Choose the grouping by reading each file's dispatch function; write the chosen
manifest into the report. A single declaration longer than 600 lines cannot be
split by moving: if one exists, leave it and list it.

## 4. Tooling (a throwaway script, not committed)

Write the mover as a small Go program under `/tmp` (not in the repo) using
`go/parser` + `go/ast` with comments: for each manifest entry, find the top-level
declaration by name (for `const`/`var` blocks, by the rule in §2), take its byte
range **including its doc comment**, append it to the target file (creating it
with `package main`), and cut it from the source. Then run
`goimports -w cmd/relevo/*.go` (installed at `~/go/bin/goimports`) to fix import
blocks, and `gofmt -l cmd/relevo`.

## 5. Allow-lists

- `scripts/check-filesize.allow`: remove every `cmd/relevo/...` entry for a file
  now at or under 600 lines. A new file must be at or under 600 lines and is
  never added.
- `scripts/check-comments.allow`: the moved declarations still carry their old
  comments (phase 3 rewrites them). For each new file that `check-comments.sh`
  now flags, add its path **only if** the file it came from was already on the
  list -- that is a rename of an existing exemption, not a new one. Remove any
  entry for a file that no longer violates.
- `.golangci.yml`: unchanged (`cmd/relevo` is excluded as a package).

## 6. Steps

1. Write and run the mover for `main.go` (§2). `go build ./... && go vet ./cmd/relevo`.
2. Same for `config.go`, `serve.go`, `doctor.go` (§3). Build and vet.
3. Allow-lists (§5). `sh scripts/check-comments.sh`, `sh scripts/check-filesize.sh` pass.
4. `go test ./cmd/relevo -count=1` passes, including `cmd/relevo/contract_test.go`
   (the CLI contract goldens) unchanged.
5. **Prove moves-only:** `git diff -M --stat` and a check that the multiset of
   non-blank, non-import lines across `cmd/relevo/*.go` (excluding tests) is
   identical before and after (for example: `git show HEAD:<each old file>` vs
   the new files, concatenated, `grep -v` package/import lines, `sort`, `diff`).
   Report the command and that the diff is empty.
6. `make check` (foreground) passes.
7. Copy this plan to `docs/plans/2026-09-26-cleanup-p2b-cmd-split.md`; commit
   everything in one commit.

Report: the final file list with line counts, the §3 manifests, the §5 list
changes, and the step-5 proof.
