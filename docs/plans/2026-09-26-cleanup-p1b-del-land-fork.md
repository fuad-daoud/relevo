# Cleanup P1b (round 2) -- delete `relevo land`, `relevo review` and `relevo bind --from`

## Round 2: where this starts

Round 1 was killed mid-work (the machine ran out of resources; nothing was
wrong with the plan). The tree on branch `relevo/cl-del-land-fork` is at
`0583bea` with **uncommitted partial work** from that run (about 22 changed
files). First run `git status` and `git diff --stat`, and review every change
against the deletion list below: keep what matches it, revert anything that
does not (and say what you reverted). Then continue from wherever that leaves
the steps in §8. The plan below is unchanged.


Round of the codebase cleanup (spec: `docs/specs/2026-09-25-codebase-cleanup-design.md`,
§2.2 deletion items 2, 3 and 5).

## 0. Rules for this round

- **The deletion list below is closed.** Everything not on it survives. A test
  that asserts surviving behaviour through a deleted mechanism is **ported**,
  not deleted. Every removed test or test row cites its item (D2.x / D3.x /
  D5.x) in the report.
- If a step is impossible as written, or the code contradicts this plan, **stop
  and report** -- do not improvise.
- Do not refactor, rename or re-comment surviving code beyond what a deletion
  forces. Comments you touch lose issue numbers and history.
- Parallel rounds delete pane/edge code and the history/serve-status reports,
  and add golden tests. In `cmd/relevo/main.go` touch only the functions and
  lines named here.
- `cmd/relevo` tests must not spawn a harness or reach the network.

## 1. System overview

Three verbs the owner never used go: `land` (rebase, gate, push, open a PR),
`review` (turn a comments file into a follow-up plan) and `bind --from`
(fork a new binding from an earlier round). Stored provenance stays readable:
`store.Binding.ForkedFrom` and the database's `forked_from_*` columns remain
(old archives carry them and `internal/ingest` reads them); nothing new writes them.

## 2. File structure

```
DELETE internal/relevo/land.go, land_test.go (and any other *_test.go that only tests Land)
DELETE internal/relevo/review.go, review_test.go
DELETE cmd/relevo/review.go (and cmd/relevo/review_test.go if present)
DELETE internal/relevo/fork.go, fork_test.go
EDIT   internal/relevo/text.go        (LandText and its doc, lines ~43-60)
EDIT   internal/relevo/{add,send,remote}.go  (fork references only)
EDIT   cmd/relevo/main.go             (land, review and fork code listed in §3)
EDIT   tests that reference the removed symbols (the build will name them)
EDIT   README.md                      (the land, review and --from references)
NEW    docs/plans/2026-09-26-cleanup-p1b-del-land-fork.md  (a copy of this plan, last step)
```

## 3. Deletion list (closed)

**D2 -- `relevo land`**
- D2.1 `cmdLand` (`cmd/relevo/main.go:~2698`) and `landFailure`, the
  `case "land":` dispatch (`main.go:370-371`), and the `land ...` line in the
  usage text at the top of `main.go`.
- D2.2 `internal/relevo/land.go` whole (LandOptions, LandResult, Land and helpers)
  and its tests; `LandText` in `internal/relevo/text.go` and its tests.
- D2.3 Anything that only `land` writes or reads, such as the `land-gate.log`
  artifact (grep `land-gate`). **Not** `stats.RoundsPerLandText`: "landed" there
  means a merged binding in the stats report and is not part of `relevo land`.
- D2.4 README: the `relevo land` bullet (~line 410), the land section (~lines
  985-1000), and the `land-gate.log` mention (~line 1116).

**D3 -- `relevo review`**
- D3.1 `cmd/relevo/review.go` (cmdReview), the `case "review":` dispatch
  (`main.go:354-355`), the `review ...` usage line.
- D3.2 `internal/relevo/review.go` whole (Comment, ParseComments, ReviewOptions,
  Review) and its tests.
- D3.3 README: the `relevo review` bullet (~line 307) and its section (~line 523).

**D5 -- `relevo bind --from`**
- D5.1 In `cmd/relevo/main.go`: `bindFlags.from` and `bindFlags.round`
  (~lines 1199-1200) and every read of them; `routeFork` (~1210) and the
  `case f.from != ""` branch of `bindRouteFor` (~1219-1227); the `--from` and
  `--round` flag definitions in `bindFlagSet` (~1296-1297) and their copies into
  `bindFlags` (~1318); the `case routeFork:` dispatch (~1331-1332);
  `splitFromSource` and `runFork` (~1476-end of runFork); the `"fork"` entry in
  the verb-hint map (~line 419); the `--from SRC[@ROUND]` usage lines (~74-75).
  Change the `--feature` flag's help from "(fork inherits it)" to just its
  label description. If `--round` is read by any route other than fork, keep it
  and report.
- D5.2 `internal/relevo/fork.go` whole (ForkOptions, ForkResult, Fork, helpers)
  and `fork_test.go`.
- D5.3 Fork-only code in `internal/relevo/add.go`, `send.go`, `remote.go`
  (grep `fork|Fork` in each): delete what only a fork reaches; keep anything a
  normal bind/add/send still reaches, and list what you kept and why.
- D5.4 `noteConsultRolesTooLong`'s mention of fork in its doc comment (main.go ~845):
  reword to bind/add only.
- D5.5 README: `--from` bullets and caveats (~lines 382-383, 846, 907-960),
  and the `relevo fork` row of the verb-rename table (~line 484).

**Survives (not on the list)**: `store.Binding.ForkedFrom`, `ForkedAtRound` or
similar stored fields, `db.BindingRow.ForkedFromBindingID/ForkedFromRound`,
their columns and their reads in `internal/ingest` and `internal/db`.

## 4. Interfaces after the round

`relevo land`, `relevo review` become unknown verbs. `relevo bind --from` and
`bind --round` become unknown flags (the flag package's normal error). No new
interfaces.

## 5. Pseudocode

```
bindRouteFor(f):          // before: from != "" -> routeFork
    resume / add / bind routes exactly as today
```

## 6. Error handling

No new errors. If deleting D5.3 code would change a normal (non-fork) bind,
add or send, stop and report.

## 7. Working efficiently

- Read the named ranges of `cmd/relevo/main.go`, `internal/relevo/{text,add,send,remote}.go`
  and the files to delete in one batch. Confirm nothing else references a
  deleted symbol with one grep:
  `grep -rn --include='*.go' -E '\bLand\(|LandOptions|LandResult|LandText|landFailure|cmdLand|cmdReview|ReviewOptions|ParseComments|\bFork\(|ForkOptions|ForkResult|runFork|splitFromSource|routeFork|land-gate' .`
- Delete files with one `git rm`; each remaining file's edits in one edit call.
- Focused loop: `go build ./... && go vet ./cmd/relevo ./internal/relevo`, then
  `go test ./cmd/relevo ./internal/relevo -count=1`.
- Full check once at the end: `make check`, then `make e2e`.

## 8. Ordered steps

1. D2.1-D2.3. Build passes.
2. D3.1-D3.2. Build passes.
3. D5.1-D5.4. Build passes.
4. Tests: fix every compile failure; delete (cite item) or port (say what
   changed) each affected test. Focused tests pass.
5. D2.4, D3.3, D5.5 README.
6. The §7 grep returns nothing. `make check` and `make e2e` pass.
   `git diff --stat` shows only §2 files.
7. **Save the plan.** Copy this plan to
   `docs/plans/2026-09-26-cleanup-p1b-del-land-fork.md` and commit it.

Report: per item, what was removed; every removed or ported test with its item;
anything kept under D5.1/D5.3 and why; non-test and test lines removed.
