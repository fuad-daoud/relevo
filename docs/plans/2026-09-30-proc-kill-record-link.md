# Plan: a planted kill-record link must not stop the kill

One change area, `internal/proc`. The guard is the *form* #685 introduced; the behaviour is a new policy on top of it. If the tree contradicts anything below, halt and report.

## 1. Seed vs tree (checked in this tree, HEAD `857a8fa2`)

Every seed anchor is live; nothing halts.

- `internal/proc/kill_record.go:24-30` — `recordKill`, the `os.WriteFile` at `:29`. It returns the write error to `Kill`. `killRecordPath` (`:17-19`) is the stream's sibling `<stream>.killed`.
- `internal/proc/proc.go:388-417` — `Kill`. The record call at `:396-398` returns its error *before* SIGTERM (`:399`), so a failed record refuses the kill. This is the bug.
- `internal/proc/proc.go:365-381` — `ExitCode`; `killRecorded(h, logPath)` at `:366` is the record's only reader (`killRecorded` appears nowhere else in non-test code). The record is a breadcrumb for `ExitCode` and nothing else, which is what makes the policy below safe.
- The form to port, not the refusal: `oNoFollow` at `proc.go:263` and `openAppend` at `proc.go:271-280` (Lstat is the refusal, the flag is the race backstop), added for the round log/stream by #685; its tests are `proc_test.go:273-345`.
- Repo rules apply as seeded: why-only comments and no `#NNN`/`§` (`scripts/check-comments.sh`); funlen 70 / gocognit 30, and `internal/proc` is **not** in `.golangci.yml`'s exclusions; `proc.go` is 522 lines, under the 600 ceiling and absent from `scripts/check-filesize.allow`; `testdata/coverage-baseline.txt:26` records `internal/proc 87.8` and must not be lowered.
- Testing holds: the whole fix is pure filesystem plus one `sleep` spawn in `internal/proc` — no harness binary, no network.

## 2. Behaviour and cases

Policy: **signal-and-warn.**

- `recordKill` keeps writing `"<pid> <unix-seconds>\n"` beside the stream and keeps replacing an existing record (truncate, as `os.WriteFile` does today), but refuses to open anything that is not a regular file, in the same shape as `openAppend`: an `Lstat` refuses a symlink, directory, fifo or device, and `O_NOFOLLOW` is the backstop for a link swapped in after the check, so nothing is ever written through a non-regular path.
- `Kill` no longer treats that refusal as a veto: it logs the failure (`slog.Warn`, the path and the error) and signals the process group exactly as before. The record still precedes the signal when it can be written, so the breadcrumb keeps its ordering.
- Unchanged: a live process with a regular or absent record path writes the record and is signalled as today; a dead handle is a no-op that writes nothing (`kill_record_test.go:71-89`); an empty `streamPath` records nothing (`headless.go:642` passes `""`); a signal that fails still returns `Kill`'s error; `ExitCode` is untouched.
- New: a symlink planted at `<stream>.killed` — dangling or pointing at an existing file — no longer blocks the signal, and the link's target is neither created nor modified. The link itself is left in place (removing it would be a second write into the runner-writable directory).

**The one consequence, stated plainly.** `ExitCode` reports ok=false through `killRecorded` (`proc.go:366`). With the record missing, `killRecorded` is false and `ExitCode` falls through to `lastLine`, so it can read the supervisor's trailer instead of reporting unknown. This is acceptable for a killed process, and **no in-memory fallback is warranted**:

- The supervisor traps TERM and `exit 143` before its trailer (`proc.go:70`, `:84`), so a killed supervisor normally leaves no trailer and ok=false still holds.
- Where a deferred trap races the trailer (the case `TestKilledSupervisorNeverWritesTheTrailer` pins), or where a predecessor's trailer survives at the same stream path, only a *label* leaks: `headless.go:749-751` renders it as `code %s` in a close payload/note, and `consult/reconcile.go:120-127` uses `ok` only to separate "silent" from "has findings". relevo killed the process deliberately, so the round's outcome is set by the kill path, not by the number — mislabelling an outcome relevo itself caused is strictly smaller than a planted link keeping a process alive.
- A fallback would need per-`Runner` state, a second source of truth inside a method that receives only the handle and path, and would not survive a daemon restart, which is the case where the breadcrumb matters most. Out of this round's scope.

## 3. Seams (verified)

| Seam | Anchor |
| --- | --- |
| the guarded write | `internal/proc/kill_record.go:21-30` (`os.WriteFile` at `:29`) |
| the refusal form to share | `internal/proc/proc.go:259-280` (`oNoFollow` `:263`, `openAppend` `:271-280`) |
| the caller that must warn, not refuse | `internal/proc/proc.go:383-417` (record call `:396-398`, SIGTERM `:399`) |
| the consumer to leave alone | `internal/proc/proc.go:362-381` (`killRecorded` check `:366`) |
| test seam / style to copy | `internal/proc/kill_record_test.go:71-139`; symlink style `internal/proc/proc_test.go:273-345`; helpers `helpers_test.go:15-42` |
| must not change | `.golangci.yml`; `scripts/check-filesize.allow`; `testdata/coverage-baseline.txt`; `internal/proc/proc_other.go` |

## 4. Steps (each with its done-when)

1. **Named test, red half.** Add `TestKillSignalsWhenTheRecordIsASymlink` to `internal/proc/kill_record_test.go`, beside `TestKillOnADeadProcessRecordsNothing` (`:71-89`), with the imports it needs (`errors`, `io/fs`, `path/filepath`). It has two planted shapes: a dangling link, and a link to a file holding planted bytes, with the target in a **separate** `t.TempDir()` from the stream's directory (the point is that the write must not escape the state dir). Each shape: `r := New(); r.KillGrace = 2 * time.Second`; `h, _, stream := start(t, r, "sleep", "60")`; `t.Cleanup` the `SIGKILL` of `-h.PID`; `os.Symlink(target, killRecordPath(stream))`; then `r.Kill(context.Background(), h, stream)` must return nil; `waitGone(t, r, h, 5*time.Second)` must see the process signalled; `os.Lstat(killRecordPath(stream))` must still report `os.ModeSymlink`; and the target must be untouched — `fs.ErrNotExist` for the dangling case, byte-identical to the planted body for the existing-file case.
   *Done when:* `go test ./internal/proc/ -run TestKillSignalsWhenTheRecordIsASymlink -count=1` fails, showing `Kill` returned nil, the process died, and the link's target was created (dangling case) or overwritten (existing-file case) — i.e. it pins the write-through.
2. **Extract the guard, then use it.** In `internal/proc/proc.go`, add one small helper beside `openAppend` that owns the discipline exactly once: `Lstat` the path (absent → allowed; not regular → refuse naming the path; any other error → return it), then `os.OpenFile(path, flags|os.O_CREATE|os.O_WRONLY|oNoFollow, 0o644)`. Move `openAppend`'s why-comment to that helper and make `openAppend(path)` call it with `os.O_APPEND`. In `internal/proc/kill_record.go`, `recordKill` opens via the helper with `os.O_TRUNC`, writes the body, and returns the first error, closing the file on every path; its doc comment changes to say why the refusal exists (nothing may be written through a path a runner can replace) and that the caller signals anyway.
   *Done when:* the step-1 test's failure **moves to the returned error** — the target is now untouched but `Kill` returns the refusal — while the #685 tests still pass: `go test ./internal/proc/ -run 'TestStartRefusesASymlinked' -count=1`.
3. **Signal through a refused record.** In `Kill` (`internal/proc/proc.go:388-417`), replace the `return err` at `:396-398` with a `slog.Warn` naming the stream path and the error, then fall through to the existing SIGTERM path unchanged. Add one sentence to `Kill`'s doc comment: a record that cannot be written is warned about, never a reason to leave a process alive. Nothing else in `Kill` moves; `Kill` stays far under 70 lines.
   *Done when:* step 1's test passes; `go test ./internal/proc/ -run 'TestKill|TestKilled|TestASecondProcess|TestABuilderKilled|TestExitCode|TestStartedProcess' -count=1` is green; `gofmt -l internal/proc` is empty.
4. **Mutations M1a/M1b (§5), then restore.**
   *Done when:* each mutation makes the named test fail for the stated reason, and the restore makes it pass.
5. **Wrap up.** `gofmt -w` the changed files; commit by name (never `git add -A`) with subject `fix(proc): signal through a planted kill-record link` and a body ending `Fixes #707`; ride this plan verbatim as `docs/plans/2026-09-30-proc-kill-record-link.md` in that commit, which is the repo's own plan convention (`CLAUDE.md`, "Conventions") and what the symlink sweep round did. If this round's brief is explicitly code-only, this is the single step to drop, and the report says so. Do not push.
6. **Final verification.** `go test ./internal/proc/ -count=1`, then `make check` once.
   *Done when:* `make check` is green, the tree is clean, and the diff adds no exclusion, no allow-list entry and no coverage-baseline change.

Focused command: `go test ./internal/proc/ -run TestKillSignalsWhenTheRecordIsASymlink -count=1`. Full command: `make check`.

## 5. Mutation checks

| # | Mutation (revert the fix) | Test that must fail, and how |
| --- | --- | --- |
| M1a | in `recordKill`, call `os.WriteFile(killRecordPath(streamPath), []byte(body), 0o644)` again (drop the refusals and the flag), keeping the warn-and-continue | `TestKillSignalsWhenTheRecordIsASymlink`: the dangling link's target exists and the existing file's bytes changed — the write-through is back |
| M1b | in `Kill`, restore `if err := recordKill(h, streamPath); err != nil { return err }` (keep the guard) | `TestKillSignalsWhenTheRecordIsASymlink`: `Kill` returns `"<stream>.killed is not a regular file"` and `waitGone` reports the pid still alive — the refusal is a veto again |
| M1 | both halves together (the full revert) | both failures above, in order |

Unlike #685's flag caveat, both halves are independently pinned: M1a fails only on the target assertions, M1b fails only on the returned error and the liveness check.

## 6. Deleted behaviour (closed list)

1. A failure to write the `.killed` record no longer makes `Kill` refuse to signal a live process — it warns and signals.
2. `recordKill` no longer follows a symlink planted at `<stream>.killed`, and no longer writes through any non-regular path there; the link and its target are left as the runner left them.
3. `recordKill`'s doc comment no longer says the failure is returned "so the caller can refuse to signal".
4. Nothing else is deleted: no functions, no files, no tests, no lint exclusions, no filesize allow-list entries, no coverage-baseline values. A regular or absent record path is still written before the signal, a dead handle is still a no-op, and `ExitCode` is unchanged.

## 7. Declared scope

The builder's diff should touch exactly:

- `internal/proc/kill_record.go` — `recordKill`'s guarded open and its comment.
- `internal/proc/proc.go` — the shared open helper, `openAppend` refactored onto it, and `Kill`'s warn-and-continue plus its comment.
- `internal/proc/kill_record_test.go` — the named test and its imports.
- `docs/plans/2026-09-30-proc-kill-record-link.md` — this plan verbatim, per §4 step 5.

Explicitly untouched: `internal/proc/proc_other.go`, `ExitCode`, `killRecorded`, `.golangci.yml`, `scripts/check-filesize.allow`, `testdata/coverage-baseline.txt`, and every caller of `Kill`.

## 8. Report must include

1. The seed-vs-tree result of §1: every anchor live, and the two decisions taken — the refusal *form* is shared with `openAppend` rather than copied, and the policy on top is warn-and-signal.
2. The consequence of §2, with the evidence that the record is only `ExitCode`'s breadcrumb, and the explicit statement that no in-memory fallback was added and why.
3. The named test, its two planted shapes, and the exact output of the red half at step 1 and at step 2 (the failure moving from the target to the returned error).
4. Mutations M1a, M1b and M1: the test that failed and why, and the restored state.
5. The focused command's result and `make check`'s result, with confirmation that coverage did not drop and that no exclusion, allow-list entry or baseline value was added.
6. `git diff --stat` against the scope of §7, and the commit (or step 5's explicit drop, if the brief was code-only).
