# Plan: shorten CI's critical path (PR wall time)

One builder round, one PR, new commits only (never amend or rebase a commit that
is already pushed). The builder cannot measure CI and must not run the full
`-race` suite locally. Checks it runs: `make check-static`, `make check-scripts`,
and the touched scripts' tests (`sh scripts/test-shard_test.sh`,
`sh scripts/test-shard-weights_test.sh`). CI is the full judge.

## Declared scope

- `.github/workflows/ci.yml` (edit)
- `scripts/test-shard.sh`, `scripts/test-shard_test.sh` (edit)
- `scripts/test-shard.weights` (new): the weights file
- `scripts/test-shard-weights.sh`, `scripts/test-shard-weights_test.sh` (new): generator and its test
- `docs/runbook.md` (one sentence, see step 7)
- `docs/plans/2026-10-17-ci-critical-path.md` (this plan, saved as the last step)

Out of scope: the coverage gate policy, `testdata/coverage-baseline.txt`,
`scripts/cover-merge.sh` and its test, the shard count (stays 3). No branch
protection or required-check rules exist on `main` (checked with the API), so
renaming the cross-compile jobs breaks no required check.

## What the code does today

- `scripts/test-shard.sh` assigns whole packages round-robin over `go list ./...`
  order (`k++ % total == idx`, lines 93-103) and splits only `SPLIT_PKGS`
  (default `./internal/relevo`) by test name, round-robin over the sorted names
  (lines 108-124). Each shard runs one `go test` for its whole packages
  (`whole.txt`) plus one per split package (`split-<base>.txt/.out`), with
  `<base>` = last path element (line 163). `cover-merge.sh` reads those file
  names back (`split-*.out`, `split-<base>.txt`).
- Measured per-package seconds on ubuntu/stable (`-race -cover`, run 39054768228):
  internal/db 177, internal/relevo 109+104+48 = 261 (three name-split runs),
  cmd/relevo 138, internal/sync 108, internal/serve 96, internal/synclog 78,
  internal/e2e 47, internal/mcp 43, internal/syncworker 33, internal/availability
  29, internal/ingest 23, internal/git 20, internal/hooks 20, then everything else
  at or under 15 (most 1-3). Full table in "Seed weights" below.
- Resulting shard sums: shard 0 about 630, shard 1 about 470 (internal/db 177 plus
  internal/synclog 78 plus its relevo slice), shard 2 about 250.

## Behaviour

### 1. Weighted assignment in `test-shard.sh`

- A committed weights file `scripts/test-shard.weights`: one `<package> <seconds>`
  per line, blank lines and `#` comments allowed, package keys relative to the
  module (`internal/db`, `cmd/relevo`), integer seconds. Path override by env
  `SHARD_WEIGHTS` (default: next to the script, resolved from `$0` like the test
  does with `here`). A missing or unreadable default file is an error (exit 2);
  an explicitly empty file is allowed (every package gets the default weight).
- Unknown packages (in `go list`, not in the file) get a default weight of 5.
  Weights naming packages that no longer exist are ignored by the script (the
  tests, step 3, flag them).
- Split packages (whole assignment is by test name, as today) contribute
  `weight / TOTAL` of load to every shard, since each shard gets an equal count of
  names. Whole packages are then placed greedily, longest first, ties broken by
  package path in `LC_ALL=C` order, each onto the currently least-loaded shard,
  ties to the lowest index. Every shard runs the same computation over the same
  inputs and keeps only its own index, which is what makes the shards disjoint,
  complete and deterministic.
- Do the arithmetic in integers: scale whole-package weights by TOTAL and give each
  split package a preload of its raw weight on every shard. That keeps ties exact
  and identical in BSD awk and gawk (no float comparisons).
- Pass the package/weight table to awk through a file under `$work`, never `-v`
  (the existing BSD-awk rule; `test-shard_test.sh` has a shim that rejects a
  newline in `-v`). Sort with `LC_ALL=C sort -k1,1nr -k2,2` on `weight path` lines
  (POSIX options only). No `sort -V`, no `readarray`, no GNU-only flags.
- `--dry-run` output format stays exactly `pkg <path>` / `test <pkg> <name>` so the
  disjoint/union tests and `cover-merge` keep working.
- `usage` text and the header comment name the weights file and `SHARD_WEIGHTS`.
  Comments say why, not history (CLAUDE.md).

### 2. Split the three heaviest packages

- Default `SPLIT_PKGS` becomes `./internal/relevo ./internal/db ./cmd/relevo`:
  unsplit, db (177 s) is the longest single unit and cmd/relevo (138 s) the
  second, against a fair share of about 430 package-seconds per shard; as whole
  packages they leave the shards unbalanced and set the floor on a shard's wall time.
- **Required fix, else the shards clobber each other:** the output file base is the
  last path element, and `cmd/relevo` and `internal/relevo` are both `relevo`, so
  both would write `split-relevo.txt/.out`. Change the base to the module-relative
  path with `/` replaced by `-` (`internal-relevo`, `internal-db`, `cmd-relevo`),
  in the three places that name it (line ~163 `base=`, the `cat` replay near the
  end which re-derives the name with `sed 's#.*/##'`, and the `.out`/`.txt` names).
  Keep the replay and the writer deriving the name from one variable/file
  (e.g. write `$work/sN.base` in the same loop that writes `sN.pkg`). `cover-merge.sh`
  needs no change: it takes `<base>` from the file name and the import path from
  the `.txt` content.
- `internal/db` and `cmd/relevo` both have a `TestMain` with child-process helper
  dispatch (`engineHelperRootEnv`, `lockHelperEnv`, `cmdTestHelperEnv`,
  `cmdTestDaemonHelperEnv`) that runs before `-run` matters, so name-splitting is
  safe for them. The builder must still confirm (read, not run) that no test in
  either package depends on state left by another named test; if one does, drop
  that package from the default split list and say so in the report.
- Names that exist only under a build tag are harmless in the `-run` regex.

### 3. Pin it in `test-shard_test.sh`

Keep all existing cases (they must still pass unmodified, including the two-package
SPLIT_PKGS case). Add, all through `--dry-run` so nothing compiles:

- Determinism: two dry runs of the same shard are byte-identical.
- Balance: with the shipped weights at TOTAL=3, compute each shard's load from its
  `pkg` lines (weights file, default 5) plus the split packages' `weight/3`, in the
  test with its own awk; assert max load minus min load is at most the largest
  single whole-package weight (the greedy bound). A round-robin assignment must
  fail this (the builder mutation-tests that: revert to `k++ % total` and see the
  named case fail).
- Longest-first: with a fixture weights file via `SHARD_WEIGHTS` giving the two
  heaviest real whole packages different shards.
- Unknown packages: `SHARD_WEIGHTS` pointing at an empty file puts every package
  at the default weight and the three shards' package counts differ by at most 1.
- Weights file hygiene: every non-comment line is `path integer`, keys unique, and
  every key resolves to a package in `go list ./...` (so a deleted package must
  leave the file in the same PR).
- The split base names are distinct for the default `SPLIT_PKGS` (run a stubbed
  `go` shim, as the coverage cases do, and assert the `-coverprofile=` file names
  of one shard's `go test` lines are pairwise distinct and include
  `split-cmd-relevo.out` and `split-internal-relevo.out`).
- Bad usage still exits 2; a nonexistent `SHARD_WEIGHTS` path exits 2.

### 4. Weights file and generator

- `scripts/test-shard-weights.sh [FILE...]` (stdin when no file or `-`): reads
  `go test` output and writes the weights file on stdout. Accepts
  (a) text output: lines `ok <tab> <import path> <tab> <N.NNNs> ...`, tolerating a
  leading CI timestamp field and ANSI escapes before the `ok` token (scan for the
  field `ok`, take the next field as the path and the one after as the time;
  skip `(cached)`; `FAIL` lines are ignored; `? <path> [no test files]` gives
  weight 0), and (b) `go test -json` output (first non-blank char `{`): package
  level events, i.e. `Action` pass/fail with no `Test` field, take `Package` and
  `Elapsed`; `Action` skip with no `Test` gives 0.
- Sum all lines of one package: a name-split package appears once per shard in a CI
  log, and the sum is its total cost. Document that the inputs are one leg's logs
  (the three ubuntu/stable shards, not also macOS), because mixing legs double
  counts.
- Output: header comment (what it is, how it was produced, what unit, the command
  to regenerate), then `path seconds` sorted by path in `LC_ALL=C` order, seconds
  rounded to the nearest integer with a minimum of 1 for any `ok`. Strip the module
  prefix with `go list -m`. Exit 1 when the input yields no package at all (never
  write an empty file silently), 2 on bad usage.
- `scripts/test-shard-weights_test.sh`, with inline heredoc inputs only: text with
  timestamp prefix and a split package listed three times (summed), a cached line
  skipped, a FAIL line ignored, a no-test-files line giving 0, the JSON form giving
  the same table, empty input exits 1, bad usage exits 2. It must run on macOS
  userland (no GNU flags) and be shellcheck clean (SC2002: no `cat file | cmd`).
- Seed `scripts/test-shard.weights` from real data. First choice: regenerate it
  with the generator from the three ubuntu/stable shard jobs of run 39054768228
  (job ids 117461205862, 117461205869, 117461205808), fetched with
  `gh api --allow-escape-sequences repos/fuad-daoud/relevo/actions/jobs/<id>/logs`
  if `gh` and the network work in the builder's sandbox. Fallback: hand-write the
  file from the table below. Either way the header comment says the unit and the
  regenerate command, not a run id.

### 5. `ci.yml`

1. **modernc leg** (`modernc` job, last step): the test command becomes
   `go test -count=1 -tags modernc ./internal/db/... ./internal/store/...` with
   `-race` added only when `github.event_name == 'push'` (an expression inside the
   `run:` line is enough). Keep `go vet -tags modernc ./internal/db/...` unchanged
   on both. Update the job comment: on pull requests the leg only keeps
   `-tags modernc` compiling and passing; main adds the race detector.
2. **Go 1.25 ubuntu leg on push only:** add to the `test` job's matrix the same
   `exclude` trick `test-macos` uses
   (`- go: ${{ github.event_name == 'pull_request' && '1.25' || 'none' }}`), and
   correct the comment that says PRs skip only macOS. The `coverage` job `needs:
   [changes, test]` still holds with excluded entries; the `shard-*` artifacts
   come only from ubuntu/stable and are unaffected.
3. **Cross-compile:** replace the sequential loop with a matrix over the five
   targets (`linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64`),
   `fail-fast: false`, job name `cross-compile (${{ matrix.target }})`, each job
   running the two builds (`go build ./...`, `go build -tags modernc ./...`) with
   `CGO_ENABLED=0` for its one `GOOS/GOARCH`. Keep the CGO_ENABLED/purego comment.
   Expected about 60-90 s per job against ~230 s serial, so cross-compile stops
   being the path after the test legs shrink.
4. Test step command unchanged (`sh scripts/test-shard.sh <shard> 3 .shard`); the
   job comment describing "a third of the packages and a third of internal/relevo's
   tests" is rewritten to describe weighted packages plus split db, cmd/relevo and
   internal/relevo. Do the same for the `test-macos` comment.

### 6. Expected effect (the MasterMind verifies on the PR's CI runs)

Balanced load is about 410 package-seconds per shard (split packages contribute
192 each, the whole packages about 660 / 3 = 220), against 630/470/250 now. Wall
time is not linear in that sum, because each split package becomes its own process
and the longest unit is now about 90-110 s, not db at 177 s.

| Job | Now | Expected on a PR |
|---|---|---|
| test ubuntu stable, each shard | 130-280 s | 180-230 s (incl. ~35 s setup) |
| test macOS stable, each shard | 150-440 s | 260-330 s (shard 0 also installs shellcheck) |
| modernc | 300-570 s | 130-200 s (no race) |
| cross-compile, each of 5 | ~230 s one job | 60-90 s |
| owner-mode / lint | 190 / 150 s | unchanged |
| Go 1.25 ubuntu legs | 3 jobs | not run on PRs |

Expected PR wall time: 5-5.5 min, macOS being the longest job; the target is
<= 5 min and may need the lever below. After the PR's first CI run, the MasterMind
(or a follow-up commit on the PR, never an amend) regenerates
`scripts/test-shard.weights` from that run's ubuntu/stable shard logs, because the
split packages now report real per-shard times, then reads job times. If macOS is
still over 5 min, the next lever is a fourth macOS shard on PRs
(`shard: [0,1,2,3]`, TOTAL 4; 4 macOS jobs stays under the free plan's cap of 5);
it is deliberately not in this PR because main would then queue 8 macOS jobs and
the owner has not approved it.

### 7. Docs

`docs/runbook.md` ("Verifying a branch") says the modernc leg runs with `-race`;
add one sentence that CI runs that leg without `-race` on pull requests and the
local command above is the stricter one. The runbook does not describe the shards,
so nothing else changes there.

## Ordered steps

1. Read `scripts/test-shard.sh`, `scripts/test-shard_test.sh`, `scripts/cover-merge.sh`
   and `.github/workflows/ci.yml`; confirm every line number and premise above. A
   contradiction (for example a `cover-merge` dependency on the base name beyond
   what is described) halts the round and is reported.
2. Write `scripts/test-shard-weights.sh` and its test. Done when
   `sh scripts/test-shard-weights_test.sh` prints ok and `shellcheck` is clean.
3. Generate (or hand-write) `scripts/test-shard.weights`. Done when it parses with
   the hygiene check from step 5 and lists every package from the table.
4. Rewrite the assignment in `scripts/test-shard.sh` (weights, greedy, preload,
   base names, default `SPLIT_PKGS`, usage and comments). Done when
   `sh scripts/test-shard.sh --dry-run 0 3` runs and shards 0-2 are disjoint.
5. Extend `scripts/test-shard_test.sh` with the cases above. Done when it passes,
   and when reverting the greedy to `k++ % total` makes the balance case fail
   (do the mutation once, then restore). Run both scripts' tests until green.
6. Edit `.github/workflows/ci.yml` (modernc, ubuntu 1.25 exclude, cross-compile
   matrix, comments). Done when the file is valid YAML (`python3 -I -c` with
   `yaml` or `go run` of an existing YAML reader if available; otherwise read it
   twice) and `make check-static` passes.
7. Edit `docs/runbook.md`, then `make check-scripts` and `make check-static`. Done
   when both are green. Do not run `make check-test`, `go test -race ./...` or
   `make e2e`.
8. Save this plan to `docs/plans/2026-10-17-ci-critical-path.md` as the last step,
   commit as new commits, push the branch. Do not touch the coverage baseline.

## Seed weights (seconds, ubuntu/stable, -race -cover)

cmd/relevo 138; internal/account 1; agentsrc 1; availability 29; blobstore 1; board 2;
board/theme 1; bugreport 1; candidate 1; capture 2; chain 1; chatlabel 1; classify 1;
config 13; consent 2; consult 2; db 177; db/dbtest 2; db/wire 3; db/wire/client 11;
db/wire/owner 15; delivery 13; doctor 9; e2e 47; git 20; harness 2; histq 1; hooks 20;
ingest 23; installation 1; isolate 1; jsonshape 1; mastermind 6; mcp 43; patch 1;
pathscope 1; pick 7; policy 1; proc 11; relevo 261; release 3; remote 2; remote/client
11; reporttail 1; roles 1; sanitize 1; serve 96; serviceunit 1; setup 1; spawn 1;
stats 1; store 4; sync 108; synclog 78; syncpipe 10; syncworker 33; transcript 1;
ui 11; ui/dash 1; upgrade 2; usage 2; view 1; workflow 2. Packages from the module
not listed here take the default weight 5; any `ok` line in a newer log wins over
this table.

## Halt conditions

- `cover-merge.sh` depends on the split base name in a way that needs more than the
  path-derived base described in section 2.
- A test in `internal/db` or `cmd/relevo` cannot be run with `-run` on a name subset.
- BSD awk, POSIX sh or shellcheck 0.9 cannot express the greedy as described.

## The report must include

- The files changed against the declared scope (`git diff --stat`), and any
  deviation, with the reason.
- The weights file's provenance (generated from run logs, or hand-written from the
  table) and the per-shard loads `--dry-run` produces for TOTAL=3.
- Output of `sh scripts/test-shard_test.sh`, `sh scripts/test-shard-weights_test.sh`,
  `make check-scripts` and `make check-static`, and the result of the one
  mutation (round-robin restored, named case failing).
- Whether db and cmd/relevo were kept in the default split list, with the
  reading that justified it.
- The expected-time table restated with any change, and a note that no
  `testdata/coverage-baseline.txt` or `cover-merge.sh` edit was made.
- The commit sha(s) pushed.

## Outcome on the PR's CI

Splitting internal/db and cmd/relevo by test name (section 2) was measured and
dropped. With it, the ubuntu/stable shards summed 963 s (longest 347 s) against
709 s (longest 284 s) without it, and macOS shard 0 rose from 442 s to 506 s:
each split is another race test binary built and run on the same 3-4 cores, and
a third of internal/db took 170 s on macOS where the whole package had taken
108 s. The default split list stays internal/relevo; the weighted placement, the
path-derived split file names, modernc without -race on PRs, the main-only 1.25
leg and the cross-compile matrix shipped as planned.
