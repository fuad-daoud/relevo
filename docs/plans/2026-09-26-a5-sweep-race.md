# A5 fix: the scratch sweep never removes the current round's scratch

**The bug**, found by the second real reader smoke round (binding `a5-smoke`, round 2, on
2026-09-26):
- `relevo send` created `.worktrees/.scratch/a5-smoke-002`;
- one second later, the daemon log shows `removed scratch worktree path=…/a5-smoke-002`;
- the runner then failed: "Failed to change directory to …/a5-smoke-002".

**The cause** is in `sweepReaderScratch` (internal/relevo/daemon.go:~421):
- It keeps a reader's scratch only when `roundOpenIn(entries, b.Round)`.
- After round 1 closed, the stored `b.Round` is 2, but round 2 is not "open" in the log
  until its plan entry is written.
- `send` creates the scratch (send.go:~516) **before** that entry exists. A daemon tick
  in that window sees the round as not open, and deletes the scratch it just made.

**The fix:** a scratch whose round is the binding's current round **or later** is never
swept. The sweep removes only:
- scratches for rounds **before** `b.Round`;
- scratches of bindings that are gone, DONE, or not readers.

A scratch left for the current round by a crash is removed at that round's close, or
when the binding is done or unbound, both of which already call `removeReaderScratch`.

**If a step is impossible as written or contradicts the code, stop and report. Do not
improvise, and do not bend a test to fit.**

## 1. Change

In `sweepReaderScratch`, replace the `roundOpenIn` keep rule. For each reader binding
that is not DONE, keep entries of that binding whose round is `>= b.Round`. Drop the
`ReadLog` call if nothing else needs it. Update the function's comment to say why the
current round is kept even before it is open: `send` makes the scratch before it logs
the plan.

## 2. Test (internal/relevo)

`TestSweepKeepsTheCurrentRoundsScratchBeforeItOpens`:
- a reader binding with `Round == 2`, a closed round 1 in its log, and **no** round-2
  plan entry;
- scratch dirs for both rounds, `<name>-001` and `<name>-002` (use the real-git helper
  the scratch tests use, or plain dirs if SweepScratch accepts them);
- after `sweepReaderScratch`, 002 exists and 001 is gone.

Also keep or port the existing sweep tests, and say which.
- **Required mutation.** Put back the `roundOpenIn` rule; the new test must fail. Then
  restore.

## 3. Checks (laptop: no make check, no `go test ./...`, no -race)

- `gofmt -l $(git ls-files '*.go')`, which must print nothing;
- `go vet ./internal/relevo/`;
- `sh scripts/check-comments.sh`, plus `grep -n '§\|#[0-9]' internal/relevo/daemon.go <test file>`,
  which must print nothing in comments;
- `sh scripts/check-filesize.sh`;
- `golangci-lint run ./internal/relevo/...`;
- `go test -count=1 ./internal/relevo/ ./internal/e2e/`.

## 4. Commit

Copy this plan verbatim to `docs/plans/2026-09-26-a5-sweep-race.md`. Commit as **one new
commit**: `fix(a5): the scratch sweep never removes the current round's scratch`.
Never amend, and never rebase.

## Report

The report covers:
- the diff;
- the mutation result;
- the check outputs.
