# Plan: close the last symlinked write sites — plan staging, the legacy drain, the binding directory

One builder round, two commits, never pushed. The plan is committed verbatim as `docs/plans/2026-09-30-symlink-plan-staging.md` in commit 1. If the tree contradicts anything below, or a done-when fails for a reason not stated here, halt and report — do not improvise.

## 1. Seed vs tree (all anchors re-read in this tree)

Every anchor is live and sits at the seed's own line; nothing halts.

| Write | Anchor, as found |
| --- | --- |
| plan of a send | `internal/relevo/send.go:528` `os.WriteFile(planPath, pf.body, 0o644)`, inside `Send`'s lock (`:449`); staged-plan removals `:546`, `:562` |
| plan of a remote send | `internal/relevo/remote_send.go:179` `os.WriteFile(planPath, planBody, 0o644)`; precedes `AppendLog` `:183` and `tx.Save` `:201` |
| plan of a `--regate` repair | `internal/relevo/repair.go:117` `os.WriteFile(planPath, []byte(text), 0o644)`; halt branch `:118`, caller `headless.go:1130` |
| legacy drain append | `internal/relevo/headless.go:527` inside `appendLines` (`:526-534`), called only from `drainFile` (`:502`); `legacyLog` (`:242`) |
| binding dir, save | `internal/store/lifecycle.go:210` `os.MkdirAll(s.Dir(b.Name), bindingDirMode)` in `prepareSave` (`:169`) |
| binding dir, append | `internal/store/log.go:310` `os.MkdirAll(s.Dir(name), bindingDirMode)` in `appendLog` (`:279`) |

Decisions taken here:

- One shared helper for the three plan sites: `stagePlan(path string, data []byte) error` in a new `internal/relevo/stage.go`. `oNoFollow` already exists in this package (`nofollow_unix.go:10`, `nofollow_other.go:7`), so no new platform file.
- The drain opens through `openAppend(path string) (*os.File, error)`, the direct port of `internal/proc/proc.go:265-280`; `internal/proc` and `internal/relevo` are siblings, neither imports the other. No name clash exists in package relevo.
- The store guard is `(s *Store) ensureBindingDir(name string) error` in `internal/store/lifecycle.go`, beside `prepareSave`, called from both store sites. It is `Lstat` + `MkdirAll` only — **no `O_NOFOLLOW`**: `MkdirAll` takes no flags, so the Lstat is the whole refusal there.
- Two test recipes depend on this: `Store.PromptPath` (`internal/store/paths.go:131`) and `legacyLog` resolve through `os.Stat`, which follows a link. A test that plants a **dangling** link sends the writer to a different path (`001-plan.md`, or no drain at all) and would pin nothing. Every planted link in this round must point at a file that exists, so "the target is byte-identical" is the assertion.
- Keep the diff off `remote_send.go`'s seed-cap check (#702) and `remote_catchup.go` (#705): `remote_send.go` changes exactly one line and nothing around it is reformatted.

## 2. Behaviour and the cases

### (a) Plan staging — one helper, three call sites

`stagePlan(path, data)`:

- `Lstat` the path. Absent → create (`O_WRONLY|O_CREATE|O_EXCL|O_NOFOLLOW`, 0o644) and write `data`. An existing regular file → `O_WRONLY|O_TRUNC|O_NOFOLLOW` and write `data`; without `O_CREATE` the mode is inert, so the file's mode and owner are untouched. Anything else — symlink (dangling or not), directory, fifo, socket, device — → an error naming the path, nothing created, the link's target untouched. Any other `Lstat` error is returned unchanged.
- The two flags are the race backstop behind the Lstat, the same discipline `writeReaderOutput` (`summary.go:79`) and `replaceReaderOutput` (`summary.go:106`) state. Bytes are written exactly as today: no trailing newline is added, so a plan's bytes do not change.
- Each site keeps its current error text (`stage plan at %s`, `write plan %s`, the repair halt message), so no existing substring expectation moves.

Cases:

- Absent path → created with the plan (unchanged, every fresh round).
- Existing regular file → replaced in place (a resend into a round that already holds a plan, a staged repair plan left behind). This is the case `O_EXCL` alone breaks, so it must keep working.
- Symlink at the path → refused; the target is byte-identical and the path stays a symlink.
- A local send refuses under the store lock before the spawn and before anything is recorded: no plan entry, no NEEDS YOU, no process. The `os.Remove(planPath)` paths are untouched (removal does not follow a link).
- A remote send refuses after the server accepted the round but before `AppendLog` and `tx.Save`: the local log and the stored binding are exactly as they were.
- A repair refuses through `haltBinding` (`repair.go:118`): round N+1 was never opened, the binding is NEEDS YOU with `could not stage its plan` in `Halt`, and the failed round's own report, diff and `gate=fail` stand.

### (b) The legacy drain's append

`openAppend(path)`: `Lstat`; absent → create (`O_APPEND|O_CREATE|O_WRONLY|O_NOFOLLOW`, 0o644); regular → append; anything else → error naming the path, nothing opened or created. The Lstat also keeps a planted fifo from blocking the open.

`appendLines` keeps its single `WriteString` of the joined lines; only the open changes. `drainFile`'s contract is unchanged — a refusal is a `slog.Warn` with the offset left as it was, never a failed tick, so the lines re-render next tick rather than being dropped. That is deliberate: dropping them silently hides more.

### (c) The binding directory

`ensureBindingDir(name)`: `Lstat(s.Dir(name))`; exists and is a directory → nil; exists and is not (a symlink even one pointing at a real directory, a file, a fifo) → an error naming the path; absent → `MkdirAll(s.Dir(name), bindingDirMode)`, which still creates the state root when it is absent; any other `Lstat` error → returned.

- `prepareSave` refuses before `dbForWrite`, so a refused `Save`/`SaveWithLog` writes no record at all.
- `appendLog` refuses before `EventAppend`, so no event row is written.
- Unchanged: the first `Save` of a binding creates `<root>/<name>`; the root keeps being created by `New`/`WithLock`/serve (`store.go:164`, `serve.go:93`); `MkdirAll` still never chmods an existing directory; a plain file where the directory should be is refused by the same check.
- Out of scope, say so in the report: `<root>` and any component above it are not checked — that path is the user's own state root, not runner-writable.

## 3. Seams

| Seam | Anchor |
| --- | --- |
| new helper file | `internal/relevo/stage.go` (new): `stagePlan`, `openAppend` |
| `oNoFollow` (exists) | `internal/relevo/nofollow_unix.go:10`, `nofollow_other.go:7` |
| send plan write | `internal/relevo/send.go:528`; lock `:449`; removals `:546`, `:562` |
| remote plan write | `internal/relevo/remote_send.go:179`; `AppendLog` `:183`; `tx.Save` `:201` |
| repair plan write + halt | `internal/relevo/repair.go:117-118`; caller `headless.go:1130` |
| drain append + caller | `internal/relevo/headless.go:526-534`, `drainFile` `:473`, `legacyLog` `:242` |
| store guard | `internal/store/lifecycle.go:169,210`; `internal/store/log.go:310` |
| shape to copy | `internal/relevo/summary.go:38-73`, `:79-89`, `:91-115`; `internal/proc/proc.go:265-280` |
| test seams | `send_test.go:23` (`writePlan`), `fixture_test.go:23,37`; `headless_test.go:31,878,4199`; `reconcile_test.go:1703,1739`; `remote_test.go:52,1805`; `internal/store/helpers_test.go:79`, `log_test.go:153` |
| must not change | `scripts/check-filesize.allow`, `.golangci.yml`, `testdata/coverage-baseline.txt` |

## 4. Steps

1. **Red half, helper.** New `internal/relevo/stage_test.go`: `TestStagePlanOnlyTouchesARegularFile` (table: absent → created with the bytes; a regular file → replaced, mode kept; a symlink to a sentinel file → error, sentinel byte-identical, path still a symlink; a directory → error) and `TestOpenAppendAppendsOnlyARegularFile` (absent → created; regular → appended; a symlink to a sentinel → error, sentinel unchanged).
   *Done when:* `go test ./internal/relevo/ -run 'TestStagePlanOnlyTouchesARegularFile|TestOpenAppendAppendsOnlyARegularFile' -count=1` fails to compile.
2. **Helper.** New `internal/relevo/stage.go` with `stagePlan` and `openAppend` per §2(a)/(b), why-only comments.
   *Done when:* step 1's command passes and `gofmt -l internal/relevo/stage.go` is empty.
3. **Wire the three plan sites.** `send.go:528`, `remote_send.go:179`, `repair.go:117` each call `stagePlan`; `remote_send.go` gets that one line only.
   *Done when:* `go test ./internal/relevo/ -run 'Send|Remote|Repair|Regate' -count=1` passes.
4. **Wire the drain.** `appendLines` (`headless.go:526-534`) opens through `openAppend`, keeping its single write.
   *Done when:* `go test ./internal/relevo/ -run TestDrain -count=1` passes, old and new.
5. **Red half, the six sites** (every planted link points at a file that exists, §1):
   - `send_test.go`: `TestSendRefusesASymlinkedPlanFile` — `seedBound(t)`, sentinel in a `t.TempDir()`, `os.Symlink(sentinel, rt.Store.PromptPath("webshop", 1))`, `Send(..., SendOptions{Defer: true})`; want an error containing `stage plan`, the sentinel byte-identical, the path still a symlink, exactly one log entry (Bind's pick), the stored binding still active with PID 0. And `TestSendReplacesAnExistingPlanFile` — write the old body as a regular file at `rt.Store.PromptPath("webshop", 1)`, `Send(Defer)`; want that path holding the new body, still regular.
   - `remote_test.go`: `TestSendRemoteRefusesASymlinkedPlanFile` — the `TestSendRemoteTierPassedToStartRound` runtime recipe (`fakeGit` with `refs/heads/relevo/api`, `fakeTransport` snapshot, `fakeRemote` answering `RoundRunning`), plant the link at `st.PromptPath("api", 1)`, `Send`; want an error containing `write plan`, the sentinel byte-identical, an empty log, and the stored binding unchanged.
   - `reconcile_test.go`, beside `TestRegateFailOpensRepairRound`: `TestRepairRoundRefusesASymlinkedPlanFile` — `sentBinding`, `Gate`, `Regate 2`, plant the link at `rt.Store.PromptPath("webshop", 2)`, `failRoundWithGate`; want no reconcile error, `State == NEEDS YOU` with `could not stage its plan` in `Halt`, the path still a symlink and the sentinel byte-identical.
   - `headless_test.go`, beside `TestDrainKeepsAppendingALegacyLog`: `TestDrainRefusesASymlinkedLegacyLog` — `sentHeadless`, the legacy log path replaced by a symlink to a sentinel file, `streamWrite`, `drainStream`; want the sentinel byte-identical, the path still a symlink, `StreamOffset` unchanged.
   *Done when:* each of the five names fails alone on the current code with `-run`, showing the sentinel gained the plan/rendered bytes.
6. **Mutations M1–M3** (§5), then restore. *Done when:* each named test fails under its mutation and passes after the revert.
7. **Mutation M4**, then restore. *Done when:* `TestDrainRefusesASymlinkedLegacyLog` fails under it.
8. **Red half, store.** `store_test.go`: `TestSaveRefusesASymlinkedBindingDir` — `New(t.TempDir())`, target dir in a temp dir, `os.Symlink(target, s.Dir("webshop"))`, `s.Save(newBinding("webshop", "/repo"))`; want an error, the target still empty, the path still a symlink. `log_test.go`, beside `TestAppendLogTakesLockOnlyOnce`: `TestAppendLogRefusesASymlinkedBindingDir` — `seedBinding(t)`, `os.Remove(s.Dir(name))`, `os.Symlink(target, s.Dir(name))`, `s.WithLock(tx.AppendLog(...))`; want an error, the target still empty, `ReadLog` still empty.
   *Done when:* `go test ./internal/store/ -run 'RefusesASymlinkedBindingDir' -count=1` fails both.
9. **Store fix.** `ensureBindingDir` in `lifecycle.go` beside `prepareSave`, called from `lifecycle.go:210` and `log.go:310`.
   *Done when:* step 8's command passes and `go test ./internal/store/ -count=1` is green.
10. **Mutation M5**, then restore. *Done when:* both step-8 tests fail under the plain `MkdirAll`.
11. **Commit 1.** Copy this plan verbatim to `docs/plans/2026-09-30-symlink-plan-staging.md`; `gofmt -w` every changed/new `.go` file; `git add` **by name** the plan, `internal/relevo/stage.go`, `stage_test.go`, `send.go`, `remote_send.go`, `repair.go`, `headless.go` (never `git add -A`: the tree carries untracked `docs/plans/*` from other rounds); commit `fix(relevo): refuse a symlinked plan path and legacy-drain log` with a body ending `Fixes #706`. Do not push.
    *Done when:* `git show --stat HEAD` lists exactly those files and `git log -1 --format=%s` is that subject.
12. **Commit 2.** `git add` by name `internal/store/lifecycle.go`, `internal/store/log.go`, `internal/store/store_test.go`, `internal/store/log_test.go`; commit `fix(store): refuse a symlinked binding directory`. Do not push.
    *Done when:* `git log --oneline -2` shows exactly the two commits and `git status --porcelain` is empty.
13. **Final verification.** `go test ./internal/relevo/ ./internal/store/ -count=1`, then `make check` once, then `git diff --stat` against §7.
    *Done when:* `make check` is green, the tree is clean, and no exclusion, allow-list entry or baseline value was added or lowered. Every new file must be `git add`ed before `make check` — `gofmt`, `check-comments` and `check-filesize` walk `git ls-files` and skip an untracked file.

## 5. Mutation checks

| # | Mutation (revert the fix) | Test that must fail |
| --- | --- | --- |
| M1 | `send.go:528` back to `os.WriteFile(planPath, pf.body, 0o644)` | `TestSendRefusesASymlinkedPlanFile` |
| M2 | `remote_send.go:179` back to `os.WriteFile` | `TestSendRemoteRefusesASymlinkedPlanFile` |
| M3 | `repair.go:117` back to `os.WriteFile` | `TestRepairRoundRefusesASymlinkedPlanFile` |
| M4 | `appendLines` back to the plain `os.OpenFile` (drop the Lstat and the flag) | `TestDrainRefusesASymlinkedLegacyLog` |
| M5 | both store sites back to `os.MkdirAll(s.Dir(...))` | `TestSaveRefusesASymlinkedBindingDir`, `TestAppendLogRefusesASymlinkedBindingDir` |
| M6 | `stagePlan` always `O_EXCL` (drop the existing-regular branch) | `TestSendReplacesAnExistingPlanFile`, `TestStagePlanOnlyTouchesARegularFile` |

Caveat for the report: dropping only `oNoFollow` passes (the Lstat refuses first) and dropping only the Lstat still fails shut in the kernel (EELOOP on the truncate, EEXIST on the `O_EXCL` create), so the flags are a backstop, not the pin. The pin is "nothing outside the path is written"; only the full revert is a mutation. Do not claim the flags are independently pinned.

## 6. Deleted behaviour (closed list)

1. A plan staged by `relevo send`, by a remote send or by a `--regate` repair no longer follows a symlink at the plan path: the write is refused, the target is untouched and the path stays a symlink. A local send then records nothing and spawns nothing; a remote send records nothing after the server accepted the round; a repair halts the binding NEEDS YOU.
2. A legacy round's builder log is no longer created or appended to through a symlink: the drain warns, its cursor does not advance, and nothing outside the binding directory is written.
3. `Save`/`SaveWithLog` no longer create or write into a binding directory reached through a symlink: the save fails and no record is written.
4. `appendLog`/`Tx.AppendLog` no longer create the binding directory through a symlink: the append fails and no event row is written.
5. Nothing else is deleted: no functions, no files, no tests, no `.golangci.yml` rule, no `scripts/check-filesize.allow` entry, no coverage-baseline value. A missing plan path is still created, an existing regular plan file is still replaced, a real binding directory and a real legacy log still work, and the store's own root creation is untouched.

## 7. Declared scope (the files the builder's diff touches)

- Production: `internal/relevo/stage.go` (new), `internal/relevo/send.go` (one line), `internal/relevo/remote_send.go` (one line), `internal/relevo/repair.go` (one line), `internal/relevo/headless.go` (`appendLines`), `internal/store/lifecycle.go` (helper + one call), `internal/store/log.go` (one call).
- Tests: `internal/relevo/stage_test.go` (new), `send_test.go`, `remote_test.go`, `reconcile_test.go`, `headless_test.go`, `internal/store/store_test.go`, `internal/store/log_test.go`.
- Docs: `docs/plans/2026-09-30-symlink-plan-staging.md`.
- Nothing else: no `internal/relevo/remote_catchup.go` (in flight), no reformatting around `remote_send.go:179`, no `scripts/`, no `testdata/`, no `go.mod`/`go.sum`.

## 8. The report must include

1. Seed vs tree: each §1 anchor with the line actually found, and that none moved.
2. The two commits (subject + hash), the plan riding commit 1 byte-identically, `git log --oneline -2`, `git status --porcelain` empty, nothing pushed.
3. Every named test with its mutation row, plus the §5 caveat about the flags.
4. `git diff --stat` beside §7, with any extra file named and justified.
5. The focused commands of steps 1, 5, 8 and 13, and `make check`'s result; per-package coverage before and after; no new exclusion, allow entry or baseline change.
6. Residuals, stated plainly: the store guard checks `<name>` only, since `<root>` is the user's own state root; a hard link at any of these paths is a regular file and is still followed (out of scope, as in the earlier rounds); the drain's refusal re-warns each tick until the link is removed rather than silently dropping lines; the send's staged-plan `os.Remove` paths are unchanged; a fifo at the binding dir is refused by the same check as a symlink but is not separately tested.
