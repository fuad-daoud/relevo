# Plan: the verify consult's reviewer is a runner too (#662)

Fixes #662: `send --verify` (and every round-close verify) starts its reviewer with no `RELEVO_RUNNER`, so in the binding's repo the harness's consent hook asks the reviewer the repository consent question at its first turn, and on a `yes` repo registers it as a MasterMind and briefs it with the guide.

## Behaviour

**A. The marker (reviewer).** A verify consult's reviewer starts with `ProcSpec.Env` exactly `[RELEVO_RUNNER=<binding name>]` — the binding name is `v.b.Name` — and nothing else; no `GIT_*` author vars, because the reviewer only reads. The proc runner filters the daemon's own `RELEVO_MASTERMIND` / `RELEVO_PLANNER` / stale `RELEVO_RUNNER` from the parent and appends `spec.Env` *after* the filter, so the child environment carries exactly one marker and no identity it could otherwise inherit. Nothing in `internal/proc` changes.

**B. One builder for the entry.** `mastermind.RunnerEnvEntry(name string) string` is the only place the entry's spelling lives; `roundEnv` and the verify spawn both call it, so they cannot drift. `roundEnv`'s output stays byte-identical to today.

**C. The silence.** The consent commands already key on `IsRunner`; with the marker present the reviewer gets no ask-note (unset repo) and no registration/guide (yes repo). No hook change, no `IsRunner` change.

**D. One code path, local and served.** On `relevo serve` the verify consult spawns through the same `verifyStart.start`: `cmd/relevo/serve.go` `cmdServeRun` → `Server.ListenAndServe` (`internal/serve/listen.go:76`) → `Server.Run` (`internal/serve/daemon.go:60`) → `Server.Tick` (`:16`) → per-owner `relevo.Daemon.Tick` (`internal/relevo/daemon.go:142`) → `tickOne` (`:280`) → `reconcileWith` (`internal/relevo/reconcile.go:138`) → `reconcileHeadless` (`internal/relevo/headless.go:590`) → `markerClose` (`:1054`) → `consult.StartVerify` (`:1103`) → `verifyStart.launch` (`internal/consult/verify.go:220`) → `verifyStart.start` (`:275`) → `Runner.Start` (`:288`). `consult.StartVerify` has exactly one non-test call site; there is no second path to mark.

**E. Cases the tests pin.** The reviewer's recorded spec carries exactly one entry, deep-equal to `RELEVO_RUNNER=<binding name>` (so any extra entry fails); the helper's exact format; `roundEnv` unchanged (its existing tests run unmodified); mutation: deleting the `Env` from the verify spawn fails a named test.

## Seams

| Seam | Where |
| --- | --- |
| Helper | `internal/mastermind/ident.go`, next to `RunnerEnv` (line 69): `RunnerEnvEntry(name string) string` = `RunnerEnv + "=" + name`. Home is safe: `internal/consult`'s imports (candidate, delivery, harness, spawn, store, usage) and mastermind's (db, store) leave no path back to consult, and `internal/relevo` already imports mastermind (headless.go:22) |
| Reviewer spawn | `internal/consult/verify.go:288–294` (`verifyStart.start`'s `spawn.ProcSpec`): add `Env: []string{mastermind.RunnerEnvEntry(v.b.Name)}` and the `internal/mastermind` import |
| Round spawn (caller 1) | `internal/relevo/headless.go:179–181` (`roundEnv`): swap the literal for `mastermind.RunnerEnvEntry(b.Name)` |
| Helper test | `internal/mastermind/ident_test.go`, next to `TestIsRunner` (line 9) |
| Reviewer test + fake | `internal/relevo/verify_test.go` (new test) using `startVerifyRound` (`internal/relevo/fixture_test.go:145`); the fake runner's `Start` records every `spawn.ProcSpec` (`internal/relevo/fake_test.go:623`, `:690`) |
| Plan doc | `docs/plans/2026-09-29-verify-consult-runner.md` |

**Seed-vs-code note (decision 4).** The seed says to find the verify tests' fake runner in `internal/consult/*_test.go`. It is not there: `internal/consult`'s tests (`ask_test.go`, `prompt_test.go`, `verify_test.go`) are pure-function tables, contain no Runner/Git fake and never call `StartVerify` (`grep` over the package shows no `ProcSpec`, `Runner` or `Start(`). The existing verify tests are `internal/relevo/verify_test.go`, and the fake that records `ProcSpec.Env` is `fakeRunner` in `internal/relevo/fake_test.go`. The marker test therefore goes there; `internal/consult` gains no test file.

**Seed-vs-code note (scope).** Verified against every non-test `Runner.Start` call site: `internal/consult/verify.go:288` is the only harness spawn without the marker; `internal/relevo/gate.go:121` runs `sh`, the availability probe runs outside the runner, the other `HeadlessLaunch` calls spawn nothing. The rest of the seed matches the code.

## Steps

1. `internal/mastermind/ident.go`: add `RunnerEnvEntry` next to `RunnerEnv`, and widen `RunnerEnv`'s doc so it also covers the round-close reviewer (no issue numbers, no `§`). Deliverable: the one-statement pure helper with its why-comment. Worked when `go test -count=1 ./internal/mastermind/ -run 'RunnerEnvEntry|IsRunner'` passes.
2. `internal/mastermind/ident_test.go`: new `TestRunnerEnvEntry` pinning the exact returned string and that `IsRunner` reads it as a runner. Deliverable: the helper's unit test. Worked when step 1's command is green including it.
3. `internal/relevo/headless.go:179–181`: `roundEnv` appends `mastermind.RunnerEnvEntry(b.Name)`. Deliverable: one spelling of the marker. Worked when `go test -count=1 ./internal/relevo/ -run 'TestRoundEnvMarksTheRunner|TestStartProcessMarksTheRunner'` passes with no test edited.
4. `internal/consult/verify.go`: add the `internal/mastermind` import and the `Env` field to the ProcSpec at `:288–294`. Deliverable: the reviewer spawn carries the marker. Worked when `go build ./...` and `go vet ./internal/consult/` are clean.
5. `internal/relevo/verify_test.go`: new `TestVerifyConsultCarriesRunnerMarker` using `startVerifyRound`, asserting exactly one Start and `fr.specs[0].Env` deep-equals `[]string{"RELEVO_RUNNER=webshop"}`. Deliverable: the pinned reviewer spec. Worked when `go test -count=1 ./internal/relevo/ -run TestVerifyConsultCarriesRunnerMarker` passes.
6. Mutation check, each reverted after it fails, output kept for the report: delete the `Env` line → step 5's test fails; make `roundEnv` append a different name → `TestRoundEnvMarksTheRunner` fails. Worked when both named tests fail under their mutation and the focused set is green again after reverting.
7. Focused sweep `go test -count=1 ./internal/mastermind/ ./internal/relevo/ ./internal/consult/` green, then `make check` green once at the end (gofmt, vet, lint, comments, filesize, tidy, plugin-version, name, scripts, coverage). Coverage note: `internal/consult` gains one statement its own tests do not execute; measured with `go test -coverprofile` that is a ~0.06-point drop against the 1.0-point allowance, so the baseline is untouched and no exclusion is added.
8. `docs/plans/2026-09-29-verify-consult-runner.md`: this plan, transcribed; `git add` code, tests and the plan; one commit; do not push; `git status` clean afterwards. Deliverable: the commit hash in the report.

## Deleted behaviour

1. A verify reviewer's session waking the consent hooks: no ask-note in a repository whose answer is unset, and no MasterMind registration or guide brief in a `yes` repository.
2. A second, drifting spelling of the marker: the entry is built in one helper instead of being typed out at the verify spawn as well.
3. Nothing else: no verb, flag, file, test, golden or spawn site is removed, weakened or re-ordered, and `roundEnv`'s output is byte-identical.

## Deliberately not done

- No consent-command or hook change: they already read `IsRunner`; the spawn-level marker test is where this change lives.
- No README or spec edit: the README's marker paragraph describes round processes and stays true; the marker now also reaches the reviewer beside them, which the issue asks for without a doc edit.
- Everything the seed puts out of scope: other consults, the consent commands, the probe, the gate.

## The report must include

The helper's home and the import-cycle reasoning; the call-chain confirmation for `relevo serve` and that `StartVerify` has one call site; the test names added and the focused command output; for the mutation check, the named failing test and that it was reverted; that the existing `roundEnv` tests passed unmodified; `git diff --stat` against this seam list; `make check` output plus the consult coverage arithmetic and the statement that the baseline and exclusions are untouched; that no `cmd/relevo` test was added; the plan doc's commit hash, one commit, not pushed.
