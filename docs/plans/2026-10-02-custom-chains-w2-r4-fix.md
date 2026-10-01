I have the reviewer's finding, the code at `c660cd3f`, and the plan text. The finding is a single, traceable bug: the check's seal recomputes a time-sensitive round and can corrupt its own round-file key. Here is the correction plan.

---

# Correction plan — custom chains W2, plan 4 of 9, Round 4, correction 0

Base: `c660cd3f` (round 4's commit) on `relevo/cc-w2`. Line numbers are at that commit, in the throwaway tree.

**Scope.** The reviewer's only finding is the seal's recomputation below; every other seam of plan 4 is confirmed as written. Nothing else in plan 4 changes.

**Defect.** `chainCheckSealLog` recomputes the log target instead of reusing the one the row was keyed with. `chainStartCheck` mints `row.Log` from the round that is newest at *start* (`internal/relevo/chain_check.go:42-55`, via `chainCheckLogTarget` → `memberNewestClosedRound`, `internal/relevo/chain_resume.go:509-524`). At the seal, `internal/relevo/chain_check.go:210` calls `chainCheckLogTarget(tx, c)` a second time and hands the freshly recomputed round to `tx.PutRoundFile(member, round, row.Log, body)` (`:214`). A writer that closes a new round while the run is in flight makes the recomputed round disagree with the round encoded in `row.Log`'s name; `PutRoundFile` refuses it (`internal/store/roundfile_put.go:27-29`), and the error leaves `chainAdvanceCheck` before `tx.ChainCheckPut` (`:96-99`), so `row.Result` is never persisted: the check never settles and every later tick re-reads the same row and hits the same refusal. The round's tests miss it because `chainCheckFixture` closes only round 1 (`internal/relevo/chain_check_test.go:19-46`).

**Fix, chosen semantics.** The seal takes the member and round from `row.Log` itself — the exact values the path was built with — and never re-reads the writer's log.

**Seams**

- `internal/store/paths.go`: beside `CheckLogPath` (`:203-209`) add
  `func (s *Store) CheckLogTarget(path string) (member string, round int, ok bool)`,
  resolving the path with the store's own round-file rules (`bindingRelOf`, `paths.go:94-111`; `roundOfFile`, `internal/store/fork.go:19-34`). Not ok for a path outside the state root or a name that carries no round; a check log path built by `CheckLogPath` always resolves.
- `internal/relevo/chain_check.go:201-221` (`chainCheckSealLog`): call `rt.Store.CheckLogTarget(row.Log)` once and pass its `(member, round)` to `tx.PutRoundFile`; an unresolvable path is an error naming the path. The on-disk removal and the empty-body seal stay as they are.
- Unchanged: `chainStartCheck`, `chainCheckLogTarget` and its round-1 clamp, `PutRoundFile`'s refuse-on-mismatch contract, the rest of the check runner, the gate core, and every existing test.

**The case that pins it.** `TestChainCheckSealsUnderTheRoundItStartedWith` in `internal/relevo/chain_check_test.go`:

- fixture as today (writer `shop`, round 1 closed); start the check; capture `row.Log` and write a body to it with `os.WriteFile`, as `TestChainCheckLogIsARoundFile` does;
- while the run is live, append a round-2 report to the writer's log (`tx.AppendLog("shop", store.LogEntry{TS: baseTime.Add(time.Minute), Round: 2, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: "/x/002-report.md"})`), exactly as the fixture does for round 1 — the writer closing a round mid-check;
- end the process; advance once. Want: green, no error, `row.Result == "pass"`, the returned key equals the captured `row.Log`, `Store.ReadFile(row.Log)` returns the body, and the on-disk copy is gone.
- Against the unfixed seal, `chainAdvanceCheckForTest` fatals with `chainAdvanceCheck: put round file …: name "001-check-001.log" is round 1, not 2`. Run it there first and keep that output.

**Steps**

1. Add `TestChainCheckSealsUnderTheRoundItStartedWith` using the existing fixture and helpers; run it against `c660cd3f` and capture the round-mismatch failure. Known by: `go test ./internal/relevo/ -run TestChainCheckSealsUnderTheRoundItStartedWith -count=1` fails with `is round 1, not 2`.
2. Add `Store.CheckLogTarget` and switch `chainCheckSealLog` to it. Known by: `go test ./internal/relevo/ ./internal/store/ -run 'Gate|ChainCheck|CheckLog' -count=1` green, with `gate_test.go` and `gatekv_test.go` untouched.
3. Mutation: put the second `chainCheckLogTarget` read back into the seal and pass its round. The new test must fail with the mismatch; restore. Known by: the failing run, then a green re-run.
4. `make check`, then `make e2e`; one new commit on `relevo/cc-w2` carrying the fix with this plan saved as `docs/plans/2026-10-02-custom-chains-w2-r4-fix.md`. Never amend, rebase or force-push `c660cd3f` or any commit already on the branch.

**Deleted**

1. `chainCheckSealLog`'s second `chainCheckLogTarget(tx, c)` read (`internal/relevo/chain_check.go:210`) and its freshly recomputed round — the seal now derives member and round from `row.Log`.
2. Nothing else: `chainCheckLogTarget` stays for `chainStartCheck`, the round-1 clamp stays, and no store caller, gate test or existing check test changes.

**Commands**

- Focused: `go test ./internal/relevo/ ./internal/store/ -run 'Gate|ChainCheck|CheckLog' -count=1`
- Full: `make check`, then `make e2e` (no new e2e; the Makefile `-run` filter is unchanged).

**Mutation for the MasterMind:** restore the live recomputation in `chainCheckSealLog` (call `chainCheckLogTarget` again and hand its round to `PutRoundFile`). `TestChainCheckSealsUnderTheRoundItStartedWith` must fail with the round mismatch; the restore passes.

**Halt if**

- `row.Log` cannot be resolved back to its member and round with the store's own path rules without changing `PutRoundFile` or another store caller;
- any existing gate, check or store test has to change an assertion to go green.

**Report includes** the focused and full command outputs; `git diff --stat` against these seams; the new test by name with its red run against the unfixed seal and its green run after; the mutation and its failing/passing evidence; any coverage-baseline line changed (expect none — no package move, no `--write`); and confirmation that no commit was amended, rebased or force-pushed and that no lint, comment or filesize exclusion was added.
