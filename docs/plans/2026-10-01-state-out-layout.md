# S0 — the runner-writable `out/` state layout (#204)

One builder round on base `origin/main` (tree `7016cf2a`, tree-identical to
`origin/main` `95ab1151`). This document is the round's plan and its report.

## 1. The decided layout

A binding's runner-output files move under a new `out/` child of the binding
directory. `out/` is the only directory relevo passes to a harness as its
writable root. The row namespace does **not** gain an `out/` prefix: a read
strips one leading `out/` element, so `out/NNN-report.md` and
`<binding>/NNN-report.md` resolve to the same `round_file` name and old and new
paths both answer.

| What | Before S0 | After S0 |
| --- | --- | --- |
| writer report | `<b>/NNN-report.md` (`ReportPath`) | `<b>/out/NNN-report.md` |
| done marker | `<b>/NNN-done` (`DonePath`) | `<b>/out/NNN-done` |
| artifacts / reader output | `<b>/NNN-<actor>/…` (`ArtifactDir`, `OutputPath`) | `<b>/out/NNN-<actor>/…` |
| stream | `<b>/NNN-runner.jsonl` (`StreamPath`) | unchanged |
| prompt / staged plan | `<b>/NNN-prompt.md`, legacy `NNN-plan.md` (`PromptPath`) | unchanged |
| gate log, question, consult files, legacy builder log | flat under `<b>/` | unchanged |
| row-only keys (diff, plan-diff, chain-diff, drift, builder-segments, findings, ask) | DB rows | unchanged |
| binding record, logs, lock | `relevo.db`, `relevo.db-*`, `.lock` | unchanged |

### The stream does not move

The seed's list said "report, done marker, stream, artifacts", but the owner
decision on #654 names "report, done marker and artifacts" only, and the design
(`docs/specs/2026-10-01-tenant-isolation-design.md` §5.3) mounts/names only
those three. The stream is not a file the runner names: relevo's supervisor
opens `StreamPath` and hands the runner inherited fds
(`internal/proc/proc.go`), so it stays at `<binding>/NNN-runner.jsonl` (legacy
`NNN-builder.jsonl`) and is not moved by the migration.

## 2. Seams (`path:function`)

- path accessors — `internal/store/paths.go:Dir`, `:roundFile`, `:ArtifactDir`,
  `:OutputPath`, `:PromptPath`, `:ReportPath`, `:DonePath`, `:StreamPath`
- new layout helpers — `internal/store/out.go:OutDir`, `:EnsureOutDir`,
  `:MigrateOutLayout`, `:resolveRunnerOutput`, `:runnerOutputName`,
  `:runnerOutputBase`, `:migratableRunnerOutput`
- name resolution — `internal/store/paths.go:roundFileRel` (strips one leading
  `out/`), `:bindingRelOf`, `:ChainInputPath`
- reads / walks — `internal/store/read.go:ReadFile`, `:StatFile`,
  `:sealedRoundFile`, `:sealedLookup`, `:readRunnerOutput`, `:statRunnerOutput`;
  `internal/store/roundwalk.go:diskFiles` (two-root, out/ wins, flat names),
  `:walkRoundDir`, `:walkArtifactDir`, `:roundFilesOfDir`;
  `internal/store/seal.go:RoundFiles`, `:RoundsOnDisk`, `:SealRound`,
  `:removeEmptyRoundDirs`
- fork — `internal/store/fork.go:RoundFiles`, `:srcPath` (joins `OutDir` for a
  runner-output name)
- rows — `internal/store/roundfile_put.go:PutRoundFile`
- format — `internal/store/format.go:BindingFormat` (12), `:recordFormat`;
  `internal/store/testdata/binding-shape.golden`; `internal/store/link_test.go`
- round spawn — `internal/relevo/headless.go:startRound`, `:startProcess`
  (calls `EnsureOutDir`), `:resumeRound`
- close — `internal/relevo/reconcile.go:closeOnMarker`, `:queueReport`;
  `internal/relevo/summary.go:reportPathFor`, `:writeReaderSummary`
- daemon — `internal/relevo/daemon.go:tickOne` (calls `MigrateOutLayout` before
  `sealRounds`), `:sealRounds` (removes an empty `out/` before the binding dir)
- other path users — `internal/relevo/send.go`, `:bind.go`, `:switch.go`,
  `:queue.go`, `:nudge.go`, `:chain.go`, `:chain_send.go`, `:chain_seed.go`,
  `internal/relevo/artifacts.go`
- consults — `internal/consult/verify.go:verifyStart.start` (calls
  `EnsureOutDir`, passes `OutDir`)
- harness argv — `internal/harness/harness.go:WritableRootPlaceholder`,
  `:writableRootsArg`, `:PrintArgs`; `internal/harness/tier.go`;
  `internal/harness/resume.go:ResumeBuild`; `internal/spawn/launch.go:HeadlessLaunch`
- probe (unchanged) — `internal/availability/probe.go` passes a throwaway temp
  dir as both tree and state; no binding, no `out/`
- docs — `README.md` codex writable-root notes

## 3. Migration rule

`Store.MigrateOutLayout(name string) (int, error)`:

1. ensure `<b>/out/` exists (refuse a symlink or any non-directory);
2. rename `<b>/NNN-report.md`, `<b>/NNN-done` and every top-level `<b>/NNN-*`
   directory into `out/`;
3. a destination that already exists is never clobbered — the old file is left
   in place, untouched, and warned about once per process (`out/` wins for every
   read);
4. a failed rename leaves the file for the next tick;
5. no copy fallback: `out/` is a child of the binding directory, so the rename is
   same-filesystem by construction.

It is idempotent (a second run moves 0). It runs from `tickOne`, under the
already-held state lock, before `sealRounds`, so the seal and every read below
see one home. The migration trigger is structural (files in the old home), not
format-gated: a plain `send` on the new binary stamps format 12 before the first
tick.

## 4. Reads and refusals

`ReadFile`/`StatFile` keep their contract for every other name. For a
runner-output name (flat `NNN-report.md` / `NNN-done`, or any nested
`NNN-<actor>/rel`) they resolve the name's two homes out/-first, touch disk only
through `Lstat` + `O_NOFOLLOW` (`internal/store/nofollow_unix.go` /
`nofollow_other.go`) and refuse anything that is not a regular file, then fall
back to the sealed row. A regular disk file still wins over a row; a non-regular
file is refused instead of read (#655). The done marker is read with the same
`StatFile`, so a sealed marker answers.

## 5. Behaviour and cases

1. **Fresh round.** `startProcess` ensures `<b>/out/` before `Runner.Start`; the
   composed prompt names the out/ report, done and output paths; codex edit gets
   `sandbox_workspace_write.writable_roots=["<b>/out"]`; the close reads from
   there; the seal writes the row and removes the file.
2. **Fresh reader.** The artifact dir is `out/NNN-<actor>/`; relevo's fallback
   output write (`O_EXCL|O_NOFOLLOW`) targets it.
3. **Old binding, daemon tick.** The tick moves old-layout runner files into
   `out/` by rename before sealing; a second tick finds nothing.
4. **Both homes exist.** `out/` wins for every read; the old file is left in
   place and the daemon says so once per process; nothing is deleted.
5. **Round in flight across the upgrade.** Its report/done may be written at the
   old path; the accessors' fallback and the name-level read fallback still find
   it, the close works, and the next tick moves it. A rename of a file the
   runner still has open is safe (the fd follows the inode).
6. **Plant.** A symlink/dir/fifo at a report, done or artifact path is refused
   by the read/stat, never followed; the round closes without a report rather
   than following the target.
7. **Walks and seal.** `RoundFiles`, `RoundsOnDisk`, `SealRound`,
   `removeEmptyRoundDirs` see `out/` files (and old-location leftovers during
   the transition), with flat names and out/ winning on a name present in both;
   a DONE binding's empty `out/` is removed before the empty binding dir.
8. **Serve/wire.** `internal/serve/roundfiles.go` serves the report through
   `Store.ReportPath` → `ReadFile`, and artifacts through
   `internal/serve/artifacts.go` → `relevo.RoundArtifacts`/`ReadArtifact` →
   `StatFile`/`ReadFile`. No new routing: both inherit the out/ paths and the
   strict read. Nothing else on the wire reads done markers.
9. **Compatibility.** `store.BindingFormat` 11 → 12: a format-12 record is
   refused by an older relevo (`ErrNewerFormat`); the new binary loads ≤11 and
   migrates.

## 6. What is deleted

Nothing. No accessor, exported name, row name, file format, verb or test
subject is removed. The only rewrites of the old layout are the path table in
`internal/store/store_test.go`, `TestOutputPathIsTheLabelUnderTheArtifactDir`,
`internal/store/link_test.go`'s historic `BindingFormat == 11` pin, and the
goldens (`internal/store/testdata/binding-shape.golden`'s format line and the
`cmd/relevo/testdata/contract/wait-json*.golden` report paths).

## 7. Report

- **Commands run and results**: `go build ./...` (ok); `go test ./internal/store`
  (ok); `go test ./internal/harness ./internal/spawn` (ok);
  `go test ./internal/relevo` (ok); `go test ./internal/consult` (ok);
  `go test ./internal/serve ./internal/e2e ./cmd/relevo` (ok);
  `go test ./...` (ok); `gofmt -l internal/store` (empty); `make check` (see the
  builder's report).
- **Coverage / baseline**: no baseline regeneration expected; `make check`
  reports the coverage delta against `testdata/coverage-baseline.txt`.
- **#685 refusals still passing**: `internal/proc/proc_test.go:275,297,319`;
  `internal/proc/kill_record_test.go:100`; `internal/relevo/summary_test.go:94,120`;
  `internal/relevo/reader_close_test.go:256`; `internal/relevo/headless_test.go:4220`.
- **Left out**: isolation modes, serve mounts, DB migration — none needed. The
  layout moves files on disk; the `round_file` names are unchanged.
