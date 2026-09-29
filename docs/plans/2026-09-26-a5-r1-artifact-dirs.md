# A5 R1: round artifact directories in the store

Spec:
- `docs/specs/2026-09-24-cockpit-design.md` D5 and §3.4;
- A4 spec `docs/specs/2026-09-26-a4-state-rename-design.md` D2, which moved per-round
  artifact dirs to A5.

**Vocabulary:**
- an **actor** is config;
- a **runner** plays one actor;
- a **reader** round leaves its output in a per-round artifact directory.

This round adds **storage only**. Nothing writes an artifact directory yet; R4 does.
Behaviour for today's flat round files must not change at all.

**If a step is impossible as written or contradicts the code, stop and report. Do not
improvise, and do not bend a test to fit.**

## 1. The layout

- A round's artifact directory is `<state>/<binding>/NNN-<actor>/`, for example
  `004-reviewer/`. It may hold any files, in subdirectories too.
- `summary.md` in it is the runner's final message. R4 writes it; this round only
  names it.
- In `round_file` (PK `(record_id, name)`), a file in it is stored with
  `name = "NNN-<actor>/<relative path>"`. Always use forward slashes, whatever the OS.
  The flat files keep their names (`004-report.md`).
- Sealed rows go in `round_file`, **not** the `artifact` table. `round_file` is the
  record today, and this is a deliberate refinement of spec §3.4's wording. Say so in
  one comment where the name is built.

## 2. Path helpers (internal/store/paths.go)

- `ArtifactDir(name string, round int, actor string) string` gives
  `Dir(name)/NNN-<actor>`. `actor` must satisfy the actor name pattern
  (`^[a-z0-9][a-z0-9._-]{0,63}$`); an invalid actor panics, because it is a
  programming error. Mirror how the other helpers treat bad input; if they return
  errors instead, do the same and say so.
- `SummaryPath(name, round, actor)` gives `ArtifactDir(…)/summary.md`.
- `ArtifactRel(round int, actor, rel string) string` gives
  `"NNN-<actor>/" + rel`, with slashes. This is the `round_file` name of a nested file.

## 3. Seal and read, nested (internal/store/seal.go, roundfile_put.go, fork.go, archive.go)

Walk **only** directories whose name matches `^\d{3}-` (roundBaseRe), one level under the
binding dir, and everything below them.

- **Security:**
  - **never follow symlinks**: skip any symlink, file or dir, and log one warning per
    skipped path;
  - skip dot-files and dot-dirs;
  - refuse any relative path containing `..`.
- `roundFilesOfDir(dir, round)` also returns the files under `NNN-*/` dirs of that
  round.
- `SealRound`:
  - stores each file under its `round_file` name (the flat base, or `NNN-<actor>/rel`);
  - after commit, removes the sealed files and then the now-empty directories, bottom
    up, with `os.Remove` so a non-empty directory is never removed.
  - The same "removal error is logged, never returned" rule applies.
- `RoundFiles(name)` lists nested names too, sorted and de-duplicated with the sealed
  rows.
- `RoundsOnDisk(name)`: a `NNN-*` directory with at least one file counts as round NNN
  on disk.
- `sealedLookup(path)`, `ReadFile` and `StatFile`:
  - a path **inside** an `NNN-*` dir of a binding resolves to its nested `round_file`
    name;
  - flat paths behave exactly as today;
  - paths outside the binding dir are unchanged.
- `PutRoundFile`: allow a path inside an `NNN-*` dir of the binding. The first segment's
  NNN must equal `round`, and everything else is refused as today.
- Fork (`roundOfFile`, `forkRoundFiles`): nested files of rounds `<= throughRound` are
  copied like flat ones, under the same nested names.
- `SealAll` and the tarball import (archive.go): nested names round-trip. Read
  archive.go and follow whatever it does for flat names.
- The daemon removes a DONE binding's directory (`internal/relevo/daemon.go:~441`).
  Check that it still succeeds when every round is sealed. It should, since
  `SealRound` now removes nested dirs. Change nothing there unless a test shows it
  fails.

## 4. Tests (internal/store)

1. `TestSealRoundSealsAnArtifactDirAndRemovesIt`:
   - round 4 has `004-report.md` and `004-reviewer/summary.md` plus
     `004-reviewer/site/index.html`;
   - after `SealRound`, `round_file` has all three names, the files and both
     directories are gone, and `ReadFile` of each original path returns the bytes.
2. `TestSealRoundSkipsSymlinksAndDotfiles`: a symlink and `.hidden` inside the dir are
   not sealed and not followed.
3. `TestFlatRoundFilesUnchanged`: the existing flat behaviour, pinned by the current
   tests, passes untouched. **Do not edit any existing store test's assertions.** If
   one needs editing, stop and report.
4. `TestRoundsOnDiskCountsAnArtifactDir`: round 5 with only `005-reviewer/summary.md`
   is on disk.
5. `TestPutRoundFileNestedPath`: accepted inside `NNN-actor/`, and refused for a
   mismatched NNN or a path with `..`.
6. `TestForkCopiesNestedFiles`.
- **Required mutations.** Run each, confirm the named test fails, then restore. Report
  all three.
  - (a) The walk follows symlinks: test 2 fails.
  - (b) `SealRound` does not remove the empty dirs: test 1 fails.
  - (c) `sealedLookup` ignores nested paths: test 1's `ReadFile` fails.

## 5. Working efficiently

- Batch-read these:
  - internal/store/{paths,seal,roundfile_put,fork,archive}.go and their tests;
  - internal/relevo/daemon.go 400-450;
  - internal/db's RoundFilePut and RoundFileList.
- Focused loop: `go build ./... && go test -count=1 ./internal/store/ ./internal/db/ && go test -count=1 ./internal/relevo/ -run 'Seal|Fork|Archive|Daemon'`
- Full, once at the end, on this server:
  `export TMPDIR=$HOME/.cache/go-tmp GOTMPDIR=$HOME/.cache/go-tmp; mkdir -p $TMPDIR; make check`

## 6. Commit

Copy this plan verbatim to `docs/plans/2026-09-26-a5-r1-artifact-dirs.md`. Commit as
**one new commit**: `feat(a5): round artifact directories seal into round_file`.
Never amend, and never rebase.

## Report

The report covers:
- `git diff --stat`;
- the mutations;
- whether any existing test needed a change (it should not);
- `make check`'s last lines;
- anything that did not match.
