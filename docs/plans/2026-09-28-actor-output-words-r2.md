# Round 2 — issue #642: the last reader-reachable "report" strings

Round 1 is accepted except for the strings its report listed under note (e). Three are in scope; one stays.

## Fix

1. `internal/relevo/nudge.go` (~line 103, `nudgeResume`'s switch note): `(ended its turn without a report)` runs for both shapes. Render it with the existing `withoutArtifact(b.Shape)` helper: a writer keeps `without a report`, a reader reads `without an output`. Keep the rest of the format byte-identical. This is the issue's own `nudge.go` item.
2. `internal/relevo/headless.go:33` `ErrBuilderBusy`: `builder's previous process is still running; wait for its report, or relevo done` is returned for a reader too (`send.go:346, :476`). Neutralize the message to `the previous process is still running; wait for the round to close, or relevo done`. Keep the var name and `errors.Is` behaviour.
3. `internal/relevo/headless.go:40` `ErrReportPending`: `the round's report is on disk but not yet delivered; relevo wait delivers it, then send the next plan` is returned for a reader too (`send.go:350, :475`). Neutralize to `the round's output is on disk but not yet delivered; relevo wait delivers it, then send the next round`. Keep the var name and `errors.Is` behaviour. `README.md:291` restates this sentence; it is a docs edit and is handled with issue #645 — do not touch README here.
4. Leave, and say so in the report one line each: `reconcile.go:420`'s `scanForInjection(..., "report", ...)` is the classifier's internal source label, not user text; `reconcile.go`'s `paths: report N, diff M` diff note is writer-only (guarded by `Shape != reader`); `internal/relevo/text.go:91`'s dry-run report path label is round 1's §7 fence. Comments elsewhere may keep the word.

## Rules

- Only `internal/relevo/nudge.go`, `internal/relevo/headless.go`, the relevant `internal/relevo` tests, and the new doc. No other wording changes; writer bytes elsewhere stay byte-identical.
- Add a reader case for the nudge switch note (find the existing nudge tests) pinning `without an output`, and mutation-check it by forcing `report` back; name the failing test.
- Verify: `go test ./internal/relevo/ -count=1`, then `make check`.
- One commit on this branch: the two files, tests, and `docs/plans/2026-09-28-actor-output-words-r2.md` (this plan). No amend, no rebase.

## Report

`git diff --stat`, the mutation with the failing test name, `make check`'s tail, the four decisions, and anything the code contradicted.
