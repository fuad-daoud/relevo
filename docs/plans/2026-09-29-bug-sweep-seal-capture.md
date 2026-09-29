# Plan: #683 fix-up — the capture drift fixture follows the reserved-name policy

Round 2 on `relevo/bug-seal` after round 1 blocked at step 10. The branch carries two commits (`a6b75e7`, `c608e3a`), the tree is clean, and this round adds one commit on top. Do not amend, rebase, reset or redo any earlier step.

## Why round 1 blocked

`make check` fails in `internal/capture`: `TestReadDrift` (`drift_test.go:506`) is a ninth reserved-key fixture the plan's eight-fixture sweep (§6) does not name. It writes the drift patch to `s.DriftPath("webshop", 5)` with `os.WriteFile` and expects `ReadDrift` to read it back — exactly the disk-first behaviour the round deletes. The product behaviour is as planned (§4 row 2 classifies `ReadDrift` as reserved); only the fixture move was unplanned. Its case 4 (`:531`) also pins a wrapped read error from a **directory** at `006-drift.patch`, which the policy removes: a reserved name is never read from disk.

## Decisions (made; do not re-decide)

1. **Case 1 becomes a row.** Author the round-5 drift with `s.WithLock(func(tx *store.Tx) error { return tx.PutRoundFile("webshop", 5, patchPath, []byte(patchContent)) })` instead of `os.WriteFile`. The `data`/`ok` assertions stay; `patchPath` stays `s.DriftPath("webshop", 5)` and the path-name assertion on it can stay.
2. **Case 4 is re-pointed, not deleted.** A directory at a reserved drift path is no longer a read error: the disk is not consulted for a reserved name, so with no row the call is a miss. Replace the case with: `ReadDrift(s, "webshop", 6)` returns `(nil, false, nil)`, and keep the `filepath.Base(...) == "006-drift.patch"` assertion. The comment says why: a reserved key answers from the row or misses; whatever sits on disk at that name is never read. There is no wrapped-error path left for a reserved key — state that in the case comment.
3. **Nothing else in `internal/capture` changes.** `capture_test.go:115-118` and `:158-161` already hold under the policy (`os.Stat` still sees no file; `ReadFile` on the reserved path with no row errors). Confirm with the grep in step 1; do not touch them.

## Seams (on this branch)

| What | file:line |
|---|---|
| case 1 to convert | `internal/capture/drift_test.go:494-506` |
| case 4 to re-point | `internal/capture/drift_test.go:531-544` |
| `Tx.PutRoundFile` (row authoring) | `internal/store/roundfile_put.go:22` |
| the policy this follows | `internal/store/reserved.go`, `internal/store/seal.go` `ReadFile`/`StatFile` |
| capture expectations that already hold | `internal/capture/capture_test.go:115-118`, `:158-161`, `:200` |

## Steps

1. `grep -n "DriftPath\|DiffPath" internal/capture/*_test.go` — confirm `drift_test.go:494` is the only reserved-key disk write in the package; then edit `drift_test.go` per decisions 1 and 2.
   *Done when:* `go test ./internal/capture/ -run TestReadDrift -count=1` passes and `go test ./internal/capture/ -count=1` is green.
2. Mutation M5: remove `ReadFile`'s reserved branch in `internal/store/seal.go` (make it always disk-first). `go test ./internal/capture/ -run TestReadDrift -count=1` must fail — case 1 has no disk file and case 4 expects a miss, so both diverge. Restore the branch and confirm the command passes again.
   *Done when:* the named test fails under the mutation and passes after the restore, with `git diff --quiet` clean.
3. Copy this plan verbatim to `docs/plans/2026-09-29-bug-sweep-seal-capture.md`; `gofmt -w internal/capture/drift_test.go`; `git add` the plan and `internal/capture/drift_test.go` by name; commit `test(capture): author the drift fixture as a reserved row`, body ending `Fixes #683`. Do not push.
   *Done when:* `git log --oneline -3` shows the three commits, `git show --stat HEAD` lists only the plan and `internal/capture/drift_test.go`, and `git status --porcelain` is empty after the commit.
4. `make check` exactly once.
   *Done when:* `make check` is green, the tree is clean, and the diff adds no exclusion, allow-list entry or coverage-baseline change.

## Report must include

- The block cause and the two decisions (one sentence each).
- Commit 3: subject, hash, body ending `Fixes #683`; `git log --oneline -3`; nothing pushed; tree clean.
- The step 1 grep result, the focused commands, the M5 result (the exact failing line), and `make check`'s single end-of-round result.
- Anything halted on or left undone, instead of improvising.
