# Cleanup P2-5a -- extract `internal/view`: the status read model and its rendering

Phase 2 of the codebase cleanup (spec `docs/specs/2026-09-25-codebase-cleanup-design.md`,
§5 "view/"). `view` holds what renders a binding's state and the plain types it
renders; the code that *computes* that state (process probes, live usage,
git numstat, planner routing) stays in relevo and fills the types. This round is
the status half; `show` and the transcript reader follow in P2-5b.
Base: `main` at `649b0551`.

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

## 1. Moves to `internal/view` (the package must not import `internal/relevo`)

From `internal/relevo/status.go`:
- types `LiveDiff`, `BindingStatus` (with `Key`, `ProcessWord`), `HeadlessInfo`,
  `LastEvent`, `CloseInfo`, `PendingInfo`, `Report`;
- pure helpers: `RenderStatus`, `writeGatedBlock`, `gateLabel`, `HideDone`,
  `ShortOwner`, `displayState`, `queueText`, `applyRemoteLive`,
  `plannerNameOrID`, `isPayloadKind`, `priorTokensOf`, `roundFacts`, `agentUnknown`.
From `internal/relevo/waiting.go`: the `Waiting` type, `WaitingOn`,
`WaitingLine` and the helpers they use (pure over store entries); if anything in
waiting.go reads `Runtime`, it stays and calls into view.
From `internal/relevo/unused_gates.go`: the `ProviderGate` type only
(`UnusedProviderGates`, which computes it, stays).
All of `statusline.go` except `PlannerStatus` (which reads `Runtime`: it stays in
relevo and returns view types).
All of `sort.go`, `roles_list.go`, `actors_list.go`.
From `candidates_list.go`: the formatters (`FormatCandidates`,
`FormatCandidatesLatency`, `FormatCandidatesLatencyFor`, and their private
helpers); `CandidateRoles(rt, token)` becomes `view.CandidateRoles(reg
*roles.Registry, set *candidate.Set, token string)` and callers pass
`rt.RoleRegistry(), rt.Candidates`.
From `text.go`: `HumanBytes`.

**Stays in relevo:** `Status`, `buildReport`, `statusRow`, `plannerRoute`,
`PlannerStatus`, `UnusedProviderGates`, policy_view.go, `brief`, and the verb
result texts (`DoneText`, `StopText`, `UnbindText`, `RenderDryRun`,
`RestoreText`) which take relevo verb result types. Move `Done` and
`DoneResult` from status.go into a new `internal/relevo/done.go` (they are a
mutating verb, not status).

Names in view drop nothing (they are already unprefixed), except where a name
would stutter: `view.View*` -- none expected; list any rename.

## 2. Requalify every user

About 540 references: internal/ui (most -- `BindingStatus`, `Report`,
`LastEvent`, `HeadlessInfo`, `CloseInfo`, `PendingInfo`, `SortRows`,
`CandidateRoles`, `Waiting`, `ProviderGate`), cmd/relevo (status.go,
candidates.go, config.go, doctor*.go, done.go, bind.go, send.go, unbind.go,
serve_admin.go, planner.go), internal/mcp (tools.go, verbs.go), internal/serve
(admin.go, bindings.go), internal/pick (list.go, model.go, verb.go),
internal/e2e, and relevo itself (served.go, headless.go `HeadlessInfo`,
livestat.go `LiveDiff`, progress.go and waiting.go `AgeText`, ...). Script it
per identifier (`gofmt -w -r 'relevo.BindingStatus -> view.BindingStatus'`
across the tree, and the bare form inside relevo), then `goimports -w`, then
fix what the build reports. **No type aliases** are left behind in relevo.
The ui golden files must not change (the rendering is moved, not changed):
`go test ./internal/ui/... -count=1` passes with no golden update.

## 3. Tests

Pure rendering tests move with the code (sort_test, roles_list_test,
actors_list_test, the RenderStatus / statusline render cases, candidates_list
formatter tests). Tests that build a Runtime and call `Status` / `statusRow`
stay in relevo and use `view.` names.

## 4. Steps

1. Create `internal/view`; move §1; `go build ./internal/view/...`
   (view imports neither relevo nor anything importing relevo).
2. §2; `go build ./... && go vet ./...`.
3. §3; `go test ./internal/view/... ./internal/relevo/... ./internal/ui/... ./internal/mcp/... ./internal/serve/... ./internal/pick/... ./cmd/relevo/... -count=1`
   and `go test ./internal/e2e/... -count=1`. No golden file changes (`git diff
   --stat -- '*.golden'` empty).
4. `golangci-lint run --allow-parallel-runners ./...` 0 issues;
   `sh scripts/check-comments.sh`, `sh scripts/check-filesize.sh` ok.
5. Coverage (§0); `make check` (foreground) passes.
6. Copy this plan to `docs/plans/2026-09-26-cleanup-p2-view-a.md`; commit once.

Report: the view API, what stayed in relevo and why, references rewritten per
package, the golden check, coverage lines changed.
