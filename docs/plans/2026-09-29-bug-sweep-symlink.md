# Plan: symlink-safe round files and reader-output strip (#685)

One builder round, two commits, never pushed. The plan itself is committed verbatim as `docs/plans/2026-09-29-bug-sweep-symlink.md` in commit 1. If the tree contradicts anything below, or a done-when fails for a reason not stated here, halt and report — do not improvise.

## 1. Seed vs tree (checked in this tree)

Every seed anchor is live; nothing halts.

- `internal/proc/proc.go:246,250` — the two `O_APPEND|O_CREATE|O_WRONLY` opens, both in `openSpawnFiles` (:244-256), called from `Start` (:194). For a new round `LogPath == StreamPath` (`headless.go:290-296`); a legacy `NNN-builder.log` exists only for pre-rename rounds. The same pattern at `proc/kill_record.go:29` is the `.killed` sibling (audited, §7).
- `internal/relevo/reconcile.go:558` — `os.WriteFile(path, stripped, 0o644)`, inside `queueReport` (:365-640), reached from `closeOnMarker` (:343, :349, :357) and `applyCatchUpReport` (`remote_catchup.go:177`); `path` is a reader's `reportPathFor` output.
- `internal/relevo/nofollow_unix.go` exists (`const oNoFollow = syscall.O_NOFOLLOW`, :10) with `nofollow_other.go` as its `!unix` twin; the discipline to copy is `writeReaderSummary` (`summary.go:38-73`) and `writeReaderOutput` (:75-89).
- Audit anchors verified: `remote_catchup.go:86,100,113`; `remote_sync.go:31,34,51,75,79`; `remote_artifacts.go:30,33,181`; `consult/verify.go:346`; `proc/kill_record.go:29`.
- Repo rules apply as seeded: why-only comments, no `#NNN`/`§` (`scripts/check-comments.sh`); funlen 70 / gocognit 30 (`.golangci.yml` excludes `^internal/relevo/` but **not** `internal/proc`); 600-line ceiling with `internal/relevo/reconcile.go` already in `scripts/check-filesize.allow` and **no** `internal/proc` entry, so `proc.go` stays under 600; no new exclusion, allow-list entry or baseline change.
- Decision 4 resolved here: the proc constant goes into `internal/proc/proc.go`, which is already `//go:build unix` (:1), so no new platform file is needed and the `!unix` build (`proc_other.go`) is untouched. A shared helper would need a new package for one constant, and the two packages are siblings under `spawn.Runner`, not importers of each other.
- Seed's testing note holds: both fixed sites are testable with pure filesystem work — no harness binary, no network.

## 2. Behaviour and cases

### (a) proc — the round log and stream open (`openSpawnFiles`)

Both opens (log first, then stream) go through one helper. Rule: `Lstat` the path; absent → create it; an existing regular file → open it; anything else (symlink, directory, fifo, socket, device) → refuse with an error naming the path and create nothing.

- The open keeps `O_APPEND|O_CREATE|O_WRONLY`, 0o644, and adds `O_NOFOLLOW` as the race backstop behind the `Lstat`. The `Lstat` is also what keeps a planted fifo from blocking the open.
- Unchanged: a fresh round creates both files; an existing regular file is appended to (a resend into the same round, a mid-round switch, a gate's second attempt); `LogPath == StreamPath` still works because the second open sees the regular file the first created; `checkSpawnSpec` still runs before either open, so a refused spec leaves nothing; when the stream is refused the already-open log is closed (`proc.go:251-253`); the error still surfaces through `Start` (headless records a spawn failure `headless.go:318-320`, the gate records `Result: "error"` `gate.go:127-129`, a consult fails its spawn `verify.go:297-303`).
- New: a symlink at either path makes `Start` fail and nothing is created at the link's target.
- Trade to state: a human-made symlink at a round file is now refused too — the state dir is runner-writable, which is the #655 trade. A hard link is a regular file and is still followed (out of scope, same as #655).

### (b) relevo — the reader-output strip (`queueReport`)

The strip write goes through one replace-in-place helper: `Lstat` the path; a non-regular file → refuse (symlink, dir, fifo — the check also stops a fifo open from blocking); a regular file → open `O_WRONLY|O_TRUNC|O_NOFOLLOW`, write the stripped bytes, close. Without `O_CREATE` the mode argument is inert, so an existing file's mode and owner are untouched.

- The strip only runs when the close read a body whose `StripTail` differs, so the file just existed. A file that vanished in between, or is not regular, is refused and warned, never recreated. The write stays after `delivery.Queue` (:548), so a refusal never loses the outcome.
- Why truncate-in-place, not temp+rename: the strip edits a file the close just read under the same lock; in-place keeps the inode (mode, owner, hard links) and writes no new file into the runner-writable directory, while rename swaps the inode and leaves a temp to clean up for no concurrency gain. Rename would also be link-safe, but the file dance is the worse trade here.
- Unchanged: a regular runner-written output is stripped exactly as today; no block → no write; a writer never strips; the round still closes and advances with the refusal logged.
- Residual (report it): the close still *reads* the output path (:392) through a link; reading is not the fixed hazard (it feeds only the tail parse) and the write is now refused.

## 3. Seams (verified in this tree)

| Seam | Anchor |
| --- | --- |
| `openSpawnFiles` — the two opens to guard | `internal/proc/proc.go:244-256` (:246, :250) |
| `Start` — caller and error path | `internal/proc/proc.go:189-219` (:194) |
| proc's unix frame (where the constant can live) | `internal/proc/proc.go:1`; `internal/proc/proc_other.go:1` |
| real `Runner.Start` callers (round, gate, consult) | `internal/relevo/headless.go:310-317`; `internal/relevo/gate.go:118-129`; `internal/consult/verify.go:289-296` |
| the discipline to copy | `internal/relevo/summary.go:38-73`, `:75-89`; `internal/relevo/nofollow_unix.go:1-10` |
| strip write and its enclosing function | `internal/relevo/reconcile.go:556-562` (:558); `queueReport` :365-640; read :392; queue :548 |
| test seams | `internal/proc/proc_test.go:215-270`; `internal/relevo/summary_test.go:26-80`; `internal/relevo/reader_close_test.go:204-240`, `:27-67`; `internal/relevo/reader_round_test.go:20`; `internal/relevo/fixture_test.go:176` |
| audit rows | `remote_catchup.go:86,100,113`; `remote_sync.go:31,34,51,75,79`; `remote_artifacts.go:30,33,181`; `consult/verify.go:346`; `proc/kill_record.go:29`; sweep: `headless.go:518` |
| must not change | `scripts/check-filesize.allow` (reconcile.go listed, no proc entry); `.golangci.yml` (no proc rule); `testdata/coverage-baseline.txt` (proc 87.8, relevo 86.0) |

## 4. Steps (done-when each)

1. **proc tests, red half.** Add to `proc_test.go` beside `TestStartRefusesADirThatIsNotADirectory` (:256): `TestStartRefusesASymlinkedLog` and `TestStartRefusesASymlinkedStream`. Each plants a dangling symlink at its path (target in a fresh `t.TempDir()`), calls `r.Start` on `sh -c true` with both paths set and distinct, and wants a non-nil error plus `os.Lstat(target)` → `fs.ErrNotExist`. Add one case where the link points at an existing file and assert the file is byte-unchanged.
   *Done when:* `go test ./internal/proc/ -run 'TestStartRefusesASymlinked' -count=1` fails both, showing `Start` returned nil and the target was created.
2. **proc fix.** In `proc.go`: `const oNoFollow = syscall.O_NOFOLLOW`, and one small helper (why-only comments: the state dir is runner-writable, `Lstat` is the refusal, the flag the race backstop, a fifo would block the open) used by both opens of `openSpawnFiles`. Helper and `openSpawnFiles` under 70 lines; `proc.go` under 600; no new file.
   *Done when:* step 1's command passes; `gofmt -l internal/proc` is empty; `go vet ./internal/proc/` is clean.
3. **Mutation M1** (§5), then restore.
   *Done when:* the named test fails under the mutation and passes after the revert.
4. **relevo test, red half.** Add `TestReaderCloseRefusesToStripThroughASymlink` to `reader_close_test.go` beside the strip test (:207): `bindReader(t, readerRepo(t))`; write `readerCloseFinal` to a file in a fresh `t.TempDir()`; mkdir the artifact dir and `os.Symlink` the target at `OutputPath("reader-bind", 1, "reviewer", "findings")`; then `writeReaderStream`, `touch DonePath`, `exitReaderRunner`, `reconcile`. Want: no Reconcile error, round 2, target byte-identical to the planted body, and the output still a symlink.
   *Done when:* `go test ./internal/relevo/ -run TestReaderCloseRefusesToStripThroughASymlink -count=1` fails with the target holding the stripped body.
5. **relevo fix.** Add the replace-in-place helper of §2(b) beside `writeReaderOutput` (`summary.go:75-89`) and call it at `reconcile.go:558` in place of `os.WriteFile`; nothing else in `reconcile.go` moves.
   *Done when:* step 4's command passes and `go test ./internal/relevo/ -run 'TestReaderClose(Writes|Strips|Keeps)' -count=1` still passes.
6. **Mutation M2** (§5), then restore.
   *Done when:* the named test fails under the mutation and passes after the revert.
7. **Audit sweep.** Run `grep -rnE 'os\.(WriteFile|OpenFile|Create|CreateTemp|MkdirAll|Rename)' --include='*.go' internal cmd | grep -v '_test.go'`; check every hit that can land under `<state>/<name>/` against §7 and fill the report's table (file:line, what it writes, disposition, reason). New hits become deferred rows, not changes.
   *Done when:* the report has one row per hit; `git diff --name-only` lists only the plan, proc and relevo files.
8. **Commit 1.** Copy this plan verbatim to `docs/plans/2026-09-29-bug-sweep-symlink.md`; `gofmt -w` changed Go files; `git add` the plan and the proc files **by name** (the tree carries untracked `docs/plans/*` from other rounds — never `git add -A`); commit `fix(proc): refuse a symlinked round log and stream` with a body ending `Fixes #685`. Do not push.
   *Done when:* `git show --stat HEAD` lists the plan and only proc files, and `git log -1 --format=%s` is that subject.
9. **Commit 2.** `gofmt -w`; `git add` by name the relevo files (helper, strip call, test); commit `fix(relevo): refuse a symlink when stripping the reader output` with a body ending `Fixes #685` and one line saying the write audit is in the report. Do not push.
   *Done when:* `git log --oneline -2` shows exactly the two commits and `git status --porcelain` is empty.
10. **Final verification.** `go test ./internal/proc/ ./internal/relevo/ -count=1`, then `make check` once.
    *Done when:* `make check` is green, the tree is clean, and the diff adds no exclusion, allow-list entry or coverage-baseline change.

## 5. Mutation checks

| # | Mutation (revert the fix) | Test that must fail |
| --- | --- | --- |
| M1 | restore the two plain `os.OpenFile` calls in `openSpawnFiles` (drop the helper's `Lstat` and flag) | `TestStartRefusesASymlinkedLog` / `TestStartRefusesASymlinkedStream` (Start succeeds, `.../pwned` created) |
| M2 | restore `os.WriteFile(path, stripped, 0o644)` at `reconcile.go:558` | `TestReaderCloseRefusesToStripThroughASymlink` (target holds the stripped body) |

Caveat for the report: dropping only the flag fails no test (the `Lstat` refuses first), and dropping only the `Lstat` also passes (the kernel refuses the link with ELOOP). The tests pin "nothing outside is written"; only the full revert is the mutation. Do not claim the flags are independently pinned.

## 6. Deleted behaviour (closed list)

1. A round log or stream that is a symlink is no longer created or appended to: `Start` refuses it, and the link's target is never created or written.
2. The reader-output strip no longer rewrites a non-regular file: a symlink (or directory, fifo, …) at the output path leaves the path and its target byte-identical, logs the refusal, and the round still closes.
3. A reader output that vanishes between the close's read and the strip is no longer recreated by the strip.
4. Nothing else is deleted: no functions, files, tests, exclusions, allow-list entries or baseline values. A regular output is still stripped, fresh rounds still create their files, and existing regular files are still appended to.

## 7. Audit: fixed or deferred

Fixed this round:

- `internal/proc/proc.go:246,250` — the round's log and stream (one path for a new round), opened for append at every headless spawn, gate and consult — **fixed** (steps 1-3).
- `internal/relevo/reconcile.go:558` — the in-place strip of a reader's output — **fixed** (steps 4-6).

Deferred (the issue's audit list; nothing changed):

- `remote_catchup.go:86,100,113` — `os.Rename` of a fetched temp onto the report/log/stream. Rename replaces a destination symlink instead of traversing it, and a re-collection after a failed ack legitimately replaces these files (O_EXCL would strand the report). Residual: the temp is created in the binding directory; a symlinked directory component is the #655 leftover.
- `remote_sync.go:31,34,51,75,79` — `writeTempAndRename` and the legacy-log mirror. `:51`'s rename is link-safe; `:31/:34` share the temp-in-dir residual; `:79`'s `O_APPEND|O_CREATE` open does follow a planted link, but it is the pre-rename legacy round's display mirror and legitimately both appends to and replaces the file, so it needs the proc-style append form plus a behaviour decision — a follow-up.
- `remote_artifacts.go:30,33,181` — `downloadTemp` (`MkdirAll` + `CreateTemp`) and `applyCatchUpArtifacts` (`:181` rename). The rename is link-safe; `:30`'s `MkdirAll` does descend a symlinked artifact dir (`NNN-<actor>`, the directory `writeReaderSummary` now refuses) and `:33` writes the temp there. Closing it means deciding whether a whole catch-up aborts or skips the file — fetch semantics, not this round.
- `consult/verify.go:346` — `stageVerifyQuestion`'s `os.WriteFile` of an oversized question, at a name carrying a consult id minted immediately before (:238-239), so a runner cannot plant that name in advance; the write must still tolerate an existing file. Same replace-in-place form as the strip; `internal/consult` is outside this round's two commits.
- `proc/kill_record.go:29` — `recordKill`'s `os.WriteFile` of `.killed`. It must replace an existing record, and its failure makes `Kill` refuse to signal (`proc.go:372-375`), so a planted link becoming "the daemon will not stop this process" is a policy choice (refuse vs. signal-and-warn), not a mechanical port.

Sweep-found, outside the issue's list, reported not changed:

- `headless.go:518` — `appendLines` drains a legacy round's stream into its builder log (`drainStream` :427-435; `drainFile` :493). Display-only, legacy rounds only, append to an existing regular file; same class, follow-up.
- `send.go:513`, `remote_send.go:179`, `repair.go:117` — prompt staging; a resend legitimately replaces the file (create-with-EXCL when absent plus nofollow-truncate when present is the eventual form). Already reported deferred in `docs/plans/2026-09-29-bug-sweep-trust.md` §7.
- `daemon.go:498` — `os.Remove` of an empty binding dir; removal does not follow a link, no change.

## 8. Report must include

1. The seed-vs-tree result of §1: no anchor moved; the two decisions taken here (proc constant in `proc.go`, strip uses nofollow-truncate and why).
2. The two commits, subject + hash, the plan file riding commit 1 byte-identically, `git log --oneline -2`, nothing pushed, tree clean.
3. Mutations M1–M2: the exact test that failed, the restored state, and the flag caveat of §5.
4. The audit table of §7 with every row's disposition and reason, the sweep command's hits as evidence, including rows found outside the issue's list.
5. The focused commands (steps 1, 4, 5, 10) and `make check`'s result; coverage baseline untouched; no new exclusions or allow-list entries.
6. Every deferred item with its reason, plus the residual inside the fixed sites: the strip still reads through a link, a symlinked directory component is unaddressed outside `summary.go`'s artifact-dir check, and hard links are out of scope.
