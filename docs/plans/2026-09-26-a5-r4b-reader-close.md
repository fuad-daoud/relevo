# A5 R4b: a reader round closes with summary.md, and its scratch goes away

Spec: `docs/specs/2026-09-24-cockpit-design.md`, D5, D6, §3.4, §7 and §8 (the e2e
reader round).

These are on this branch:
- R1: artifact dirs (`store.ArtifactDir`, `SummaryPath`), which seal into `round_file`;
- R2: scratch (`CreateScratchFrom`, `RemoveScratch`, `SweepScratch`);
- R3: reader bindings (`Binding.Shape`);
- R4a: a reader round launches in `ScratchWorktreePath(name, round)` with the reader
  prompt, writes files into `ArtifactDir(name, round, actor)`, and ends with the
  `relevo` block in its final message plus the done marker.

**This round:** closing that round. After it, a reader round works end to end.

**If a step is impossible as written or contradicts the code, stop and report. Do not
improvise, and do not bend a test to fit.**

## 0. First: fix R4a's comments

`scripts/check-comments.sh` fails on R4a's commit, which is on this branch. On this server
the script can pass without checking anything, so the planner found this locally.
- `internal/relevo/actoroutput_test.go:9`, and `internal/relevo/reader_round_test.go` at
  lines 34, 81, 128, 193 and 222, have comments that cite "§" (spec sections).
- Reword each without the section reference, and keep the meaning.
- **For every file you touch in this round, also run**
  `grep -n '§\|#[0-9]' <files>` and make sure it prints nothing in comments. Do not
  rely on the script alone.

## 1. summary.md at close

A reader round's report **is** its `summary.md`.
- Wherever a round closes (every path funnels into `queueReport`, internal/relevo/
  reconcile.go:~350, via `closeOnMarker` ~307, the headless marker close and unmarked
  close in headless.go, and stop.go's stopped close), for a reader:
  - if `SummaryPath(name, round, actor)` does not exist, write it from the runner's
    final message: `transcript.FinalText(kind, stream)`, where `stream` is the round's
    `BuilderStreamPath` and `kind` is the harness kind of the **last** segment that ran
    (a mid-round switch can change it). Mirror how `internal/consult/reconcile.go:~130`
    does it for consults;
  - an empty final message writes nothing, and the round closes as "noreport", exactly
    like a writer that wrote no report;
  - the report path handed to `queueReport` (and so the KindReport entry's `Path`, and
    what `relevo wait` prints) is the summary path. Tail parsing (`status:` etc.) reads
    it, so the reader's `relevo` block works like a writer's.
- Writers are unchanged: `NNN-report.md` stays flat (owner decision).

## 2. Skip the writer-only close steps for a reader

- No `capture.RoundDiff` (reconcile.go:~422). A reader round has no diff.
- No escape check (`escapeApplies`/`escapeCheck`, escape.go:~58-108). A reader never
  touches `b.CWD`, and a dirty writer tree next to it must not read as "escaped".
- Progress sampling (progress.go:~26 `sampleSignals`) samples `roundTree(rt, b)` (the
  scratch), not `b.CWD`.
- No check (gate), no repair round and no verify at close.

## 3. The scratch goes away

- `RemoveScratch` runs:
  - at close, after summary.md is written and before the round's files are sealable;
  - on `relevo stop` of a reader round;
  - when a reader binding goes DONE (`relevo done`) or is unbound.

  A removal error is logged and never fails the close.
- A daemon sweep phase calls `SweepScratch` with a `keep` that keeps exactly the scratch
  of each **open** reader round. Run it through `Daemon.safely` like the other phases
  (daemon.go:~453). Leftovers from crashes go away on the next tick.
- `store.Sealable` must not seal a reader round while its scratch is still being
  removed. Removal happens at close, so this normally holds. If you find an ordering
  where sealing can race the removal, report it.

## 4. The reader agents may write their artifact dir

The shipped reviewer, researcher and architect bodies (all four kinds each, in
`internal/harness/agents/`) forbid every write ("READ-ONLY, WITHOUT EXCEPTION", "spawned
by relevo ask").
- Change only those sentences to: never change the repository, its working tree or its
  git state; when your prompt names an artifact directory, write your files there and
  nowhere else.
- Remove "spawned by relevo ask" wording; `ask` is being removed.
- Keep everything else byte-identical.
- The architect's "Handing off" section must stay byte-identical with
  `internal/planner/handoff.md` (`handoff_test.go`). Do not touch that section.
- Run `sh scripts/agents-shipped.sh --write`, then `--check`.

## 5. Tests

1. `TestReaderCloseWritesSummaryFromTheFinalMessage`:
   - a reader round whose fake stream's final message ends in a `relevo` block closes
     with `SummaryPath` holding that message;
   - the KindReport entry's `Path` is the summary path;
   - the tail status parses;
   - no diff is captured.
2. `TestReaderCloseKeepsARunnerWrittenSummary`: if the runner wrote `summary.md`
   itself, it is not overwritten.
3. `TestReaderCloseRemovesTheScratch`, plus stop and done remove it.
4. `TestSweepScratchKeepsOpenReaderRounds`.
5. `TestReaderRoundIsNotAnEscape`: a dirty `b.CWD` during a reader round does not
   produce an escape annotation.
6. **The e2e reader round** (internal/e2e, run by `make e2e`; name it
   `TestHeadlessE2EReaderRound` so the existing `-run TestHeadlessE2E` picks it up):
   - config: the candidate serves builder and reviewer; add a reviewer order or actor as
     `writeCandidatesAndPolicy` needs (headless_test.go:~393);
   - a writer binding on a repo with a dirty file, and a **reviewer** binding on the
     same tree;
   - send the reviewer a plan;
   - the fake harness script (headless_test.go:~296-387), when its prompt has an
     artifact-directory line, writes `index.html` and `style.css` into that directory,
     **edits a file in its working tree** (the scratch), prints a final result whose
     text ends in a `relevo` block, and creates the marker;
   - assert:
     - the round closes;
     - `summary.md`, `index.html` and `style.css` exist, and after a seal they are
       `round_file` rows;
     - the binding's tree `git status --porcelain` and file contents are exactly as
       before;
     - the scratch directory is gone.
- **Required mutations.** Run each, confirm the named test fails, then restore. Report
  all three.
  - (a) Skip writing summary.md: test 1 fails.
  - (b) Don't remove the scratch at close: test 3, and the e2e, fail.
  - (c) The reader runs the escape check: test 5 fails.

## 6. Working efficiently

- Batch-read these:
  - internal/relevo/{reconcile,headless,stop,escape,progress,daemon,scratch,send,done,unbind}.go
    (use whichever names exist; grep for `func Done(` and `func Unbind(`);
  - internal/consult/reconcile.go 110-140;
  - internal/transcript/final.go;
  - internal/store/{seal,paths}.go;
  - internal/e2e/*;
  - internal/harness/agents/{reviewer,researcher,architect}.*;
  - scripts/agents-shipped.sh.
- Focused loop: `go build ./... && go test -count=1 ./internal/relevo/ -run 'Reader|Scratch|Close|Reconcile|Stop|Done|Escape|Progress|Sweep' && go test -count=1 ./internal/store/ ./internal/harness/ ./internal/planner/`
- e2e: `go test ./internal/e2e/ -run TestHeadlessE2E -count=1`
- Full, once at the end, on this server:
  `export TMPDIR=$HOME/.cache/go-tmp GOTMPDIR=$HOME/.cache/go-tmp; mkdir -p $TMPDIR; make check && make e2e`

## 7. Commit

Copy this plan verbatim to `docs/plans/2026-09-26-a5-r4b-reader-close.md`. Commit as
**one new commit**: `feat(a5): a reader round closes with summary.md, and its scratch goes away`.
Never amend, and never rebase.

## Report

The report covers:
- `git diff --stat`;
- every close path changed;
- the agent-body sentences, old and new;
- the mutations;
- the e2e result;
- `make check` and `make e2e`'s last lines;
- anything that did not match.
