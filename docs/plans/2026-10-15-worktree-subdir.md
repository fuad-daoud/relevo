# Plan: worktree binds keep the subdirectory they were started from (slug: worktree-subdir)

Base: `1a991fefafd1bdc78f2d9fd7bce9bebefa436df0`. CLAUDE.md rules apply throughout: no
history in code or tests (no issue numbers, no "used to", test names say what they pin),
comments say why only, functions ≤70 lines, non-test files ≤600, no new lint exclusions,
the coverage baseline is not lowered (no code moves packages; `sh
scripts/check-coverage.sh --write` is not expected). The plan file itself is committed in
the same PR as the code.

## 1. Behaviour and the cases

Today one rule decides where a round's processes run: `roundTree(rt, b)`
(`internal/relevo/scratch.go:92-102`) returns `b.CWD` for a writer and the scratch
worktree for a reader. Both the builder spawn (`internal/relevo/headless.go:288,396`,
`internal/relevo/send.go:435`) and the gate (`internal/relevo/gate.go:36-43`,
`Dir: roundTree(rt, b)`) read it. A plain `relevo bind` stores `CWD = os.Getwd()`
(`cmd/relevo/bind.go:246`), so it already runs in the subdirectory. Every worktree path
stores `CWD = <worktree root>` (`internal/relevo/add.go:336,340` via `cutWorktree:486`;
`:317` for `--branch`), which is the defect.

The rule becomes: **the round's directory is the binding's tree plus the binding's
subdirectory.** `Binding.Subdir` (new, `json:"subdir,omitempty"`) holds a
slash-separated path relative to `CWD`, empty when the round runs at the tree root. `CWD`
keeps its meaning (the tree relevo cut, the repo every git call runs against) — only the
round's directory is joined, in the one place `roundTree` already is. Cases:

1. `bind --worktree` (and `--branch`) started below the root: `Subdir = git rev-parse
   --show-prefix`, so builder and gate run at `<worktree>/<subdir>`. `--cwd` never
   consults the prefix: the prepared directory *is* the round's directory, so `Subdir`
   stays "".
2. Started at the root: `Subdir` is "" and every behaviour is byte-identical to today.
3. Plain `relevo bind` (no placement flag): unchanged (`CWD = cwd`, `Subdir` "").
4. The subdirectory does not exist at the base commit: refuse, naming the subdirectory
   and the base, with the worktree and branch the cut made rolled back.
5. `--server`: the client resolves the prefix from its own checkout and sends it on the
   create (`subdir` on the wire). The server stores it on the served binding, so the
   server's builder (`Send` → `roundTree`), its gate and its client-requested check all
   run in the subdirectory of the worktree the server cuts. The server cannot check the
   subdirectory at create time (the base commit only reaches it when the round's bundle
   is absorbed), so the client's check is the only one — it runs before the create.
6. The client's *mirror* row of a remote binding keeps `Subdir` "": its `CWD` is the
   caller's own directory, which already is the subdirectory. Setting it there would
   double-join.
7. Chains: a local chain started below the root carries the prefix on every member
   (builder and readers share the cut worktree); a chain whose builder is placed on a
   server carries it on the create while its local readers keep `Subdir` "" (their `CWD`
   is already `plan.repo`); a fully served chain carries it on every member; members
   added by a resume, a served resume and a fork child inherit the subdirectory of the
   member they are built beside.
8. A pre-subdir server (no `subdir` feature) refuses a bind or a chain that would send a
   non-empty prefix, naming the server, exactly as `FeatureLabels` does.
9. Existing bindings: a record with no `subdir` key loads with `Subdir` "" and runs
   exactly where it ran. No migration.
10. Display: `relevo status`'s row names the round's directory, and `relevo bind` prints
    it (`--json`'s `dir`, plus one line on the add route) when it is not the tree root.

## 2. Seams

- `internal/relevo/subdir.go` (new, the whole rule): `NormalizeSubdir(raw string)
  (string, error)` (exported, pure: trim the trailing `/`, refuse an absolute path, a
  `..` segment or a `.` result), `resolveSubdir(ctx, rt, repo)` (the one `ShowPrefix`
  call site; a non-repo repo resolves to ""), `RoundDir(b store.Binding) string`
  (exported pure join of `CWD` and `Subdir`, for the CLI and the status row).
- `internal/relevo/scratch.go`: `roundTree` joins — reader:
  `filepath.Join(scratch, b.Subdir)`, writer: `RoundDir(b)`. Callers unchanged.
- `internal/relevo/add.go`: `Add` — resolve the prefix once, before the branch/cut split
  (skipped when `opts.CWD != ""`, and skipped when `rt.Git == nil`); check
  `DirAt(opts.Repo, base|tip, subdir)` after the cut / the `--branch` checkout, before
  the binding is written, so a refusal rolls back; `Subdir: subdir` in the binding
  literal (next to `CWD`).
- `internal/relevo/runtime.go`: the `Git` interface gains `ShowPrefix(ctx, dir) (string,
  error)` and `DirAt(ctx, dir, ref, path) (bool, error)`.
- `internal/git/refs.go`: `ShowPrefix` (`rev-parse --show-prefix`); `DirAt`, a
  `ls-tree -z <ref> -- <path>` parse, mode `040000` + type `tree` (a blob at that path is
  false).
- `internal/relevo/remote_add.go`: `addRemote` — resolve the prefix after the
  `--cwd`/`rt.Git` guards; refuse a server without `remote.FeatureSubdir` next to the
  labels refusal; `Subdir: subdir` in `createReq`; the mirror literal stays `Subdir` "".
- `internal/relevo/chain_start.go`: `chainBase` gains `subdir`;
  `chainMemberBinding` sets `b.Subdir = base.subdir`.
- `internal/relevo/chain_start_wf.go`: `chainBuildStartMembers` — resolve the prefix for
  the local chain only, into `rowBase`; `readersBase` (the remote chain's local readers)
  keeps "".
- `internal/relevo/chain_remote.go`: `chainRemotePlan` gains `subdir`;
  `chainRemotePreflight` resolves it and refuses a server without the feature;
  `chainBuildRemoteBuilder` sends it.
- `internal/relevo/chain_server_start.go`: `chainServerPlan` gains `subdir`;
  `chainServerResolve` resolves it from `plan.repo` and refuses a pre-subdir server
  beside `missingChainFeatures`; `chainServerRequest` sends it; `chainMirrorMember`
  (mirror) stays "".
- `internal/relevo/chain_served.go`: `ServedChainRequest` and `servedFacts` gain
  `Subdir`/`subdir`; `ServedChainCreate` copies it into the facts;
  `servedChainMember` sets it on every member.
- `internal/relevo/chain_served_wf.go`: `servedChainAddMembers` inherits
  `builder.Subdir` into its facts.
- `internal/relevo/chain_resume_wf.go`: the resumed local chain's `base` inherits
  `builder.Subdir`.
- `internal/relevo/chain_fork_start.go`: the child plan's base inherits the parent
  builder member's `Subdir` (the fork cuts from `parent.Repo`, the root checkout).
- `internal/relevo/status.go`: `CWD: RoundDir(b)`; `internal/view/status.go` doc comment
  says the round's directory.
- `internal/relevo/served_check.go`: the served check's `Dir: roundTree(rt, b)`.
- `internal/store/binding.go`: `Subdir string \`json:"subdir,omitempty"\`` with a
  why-comment; `internal/store/format.go`: `BindingFormat` 16 → 17 (an older relevo that
  drops `subdir` would run the round at the tree root: the wrong directory, silently)
  plus the const comment's next sentence; golden regenerated.
- `internal/remote/proto.go`: `CreateBindingRequest` gains `Subdir`;
  `FeatureSubdir = "subdir"`. `internal/remote/chain.go`: `CreateChainRequest` gains
  `Subdir`.
- `internal/remote/contract_test.go`: the filled create requests gain a subdir; both
  goldens regenerated.
- `internal/serve/routes.go`: `remote.FeatureSubdir` in the WhoAmI feature list;
  `internal/serve/serve_test.go` (`TestWhoAmI`) and `internal/serve/configreload_test.go`
  (`TestReloadIsWireAdditive`) gain it.
- `internal/serve/bindings.go`: `parseCreateRequest` validates the subdir;
  `buildServedBinding` sets `Subdir: req.Subdir`.
- `internal/serve/chains.go`: `parseChainCreateShared` validates it;
  `internal/serve/chains_view.go` copies it, and `chainRequestMatches` compares it (an
  identical retry must not swallow a different prefix) by loading the chain's builder
  member binding.
- `cmd/relevo/bind.go`: `runAdd` — the tree/worktree switch becomes a pure
  `addedTreeLines(res relevo.AddResult) []string` (same file), which prints the round
  directory line when `res.Binding.Subdir != ""`; the `--json` `dir` becomes
  `relevo.RoundDir(res.Binding)` (identical to today when `Subdir` is "").
- Test fakes: `internal/relevo/fake_test.go` `fakeGit` gains `showPrefixResult`,
  `dirAtResult` and their call counters.

## 3. Scope (builder may touch only these)

- `internal/relevo/subdir.go` (new), `add.go`, `remote_add.go`, `scratch.go`,
  `runtime.go`, `gate.go` (only if the gate spec needs the join to move),
  `served_check.go`, `status.go`, `chain_start.go`, `chain_start_wf.go`,
  `chain_remote.go`, `chain_server_start.go`, `chain_served.go`, `chain_served_wf.go`,
  `chain_resume_wf.go`, `chain_fork_start.go`, plus `internal/relevo/fake_test.go` and
  the new/updated tests.
- `internal/git/refs.go`, `internal/git/refs_test.go`.
- `internal/store/binding.go`, `format.go`, `testdata/binding-shape.golden`,
  `internal/store/*_test.go` (one decode test only).
- `internal/remote/proto.go`, `chain.go`, `contract_test.go`,
  `testdata/contract/proto-CreateBindingRequest.golden`,
  `testdata/contract/proto-CreateChainRequest.golden`.
- `internal/serve/bindings.go`, `chains.go`, `chains_view.go`, `routes.go`,
  `serve_test.go`, `configreload_test.go`, `chains_test.go`.
- `internal/view/status.go` (comment only).
- `cmd/relevo/bind.go`, `cmd/relevo/bind_lines_test.go` (new).
- `docs/plans/2026-10-15-worktree-subdir.md` (this plan).
- Must not touch: `internal/relevo/chain_pull.go` and `chainMirrorMember` (a mirror's
  `CWD` is already the caller's directory), `internal/relevo/bind.go` (resume keeps the
  stored binding), `internal/ui/*` (the TUI's shell opens in the worktree root on
  purpose), `internal/view/render.go` (it prints `BindingStatus.CWD`, which the status
  row now fills with the round's directory), `internal/db/*`, `internal/e2e/*`. Any file
  outside this list is a halt with evidence, not a widening.

## 4. Ordered steps (deliverable + how to know it worked)

1. `internal/git/refs.go`: `ShowPrefix` and `DirAt` with their tests — green, and the
   `DirAt` mutation (accept a `100644` blob) red.
2. `internal/store/binding.go` + `format.go`: the `Subdir` field and `BindingFormat =
   17` — the shape test fails, then `--update` rewrites the golden (`format 17` plus
   `subdir`) and the test is green; the golden diff is those two lines only.
3. `internal/relevo/subdir.go` + `runtime.go`: `NormalizeSubdir`, `resolveSubdir`,
   `RoundDir` and the two interface methods (fakeGit stubs default to no prefix, so
   nothing existing moves) — green; mutations (accept an absolute or `..` prefix; drop
   the join) red.
4. `internal/relevo/scratch.go`: `roundTree` joins through `RoundDir` — green plus
   `TestGateRunsInTheRoundDirectory` and `TestReaderRoundRunsInTheScratchSubdirectory`;
   mutation (return `b.CWD`) red.
5. `internal/relevo/add.go`: resolve the prefix, check `DirAt` at the base commit, store
   `Subdir` — green, including the add/branch/root/cwd/refusal tests; mutations (skip
   `DirAt`; resolve the prefix on the `--cwd` path) red.
6. `internal/relevo/remote_add.go`: send the prefix, refuse a server without
   `FeatureSubdir`, keep the mirror at "" — green; mutations (put the prefix on the
   mirror; drop the refusal) red.
7. `internal/relevo/status.go` + `internal/view/status.go`: the row's `cwd` is
   `RoundDir(b)` — green; mutation (`row.CWD = b.CWD`) red.
8. `internal/relevo/served_check.go`: the served check's `Dir` is `roundTree(rt, b)` —
   the existing assertion is updated to the joined directory.
9. Chains: `chainBase`/`chainMemberBinding`, `chainBuildStartMembers`,
   `chainRemotePlan`/`chainBuildRemoteBuilder`,
   `chainServerPlan`/`chainServerRequest`, `ServedChainRequest`/`servedFacts`/
   `servedChainMember`, `servedChainAddMembers`, the resumed local base, the fork child
   — green with the five inheritance tests; mutation (leave the members at the root) red.
10. `internal/remote` + `internal/serve`: the wire field, the feature token, the
    validation, the stored value, the retry comparison — the two goldens regenerate (one
    added line each), then green with `TestCreateBindingStoresSubdir`, the `subdir` rows
    in `TestCreateInvalid`, `TestChainRequestMatchesComparesTheSubdirectory`.
11. `internal/store` decode test: a record with no `subdir` key loads with `Subdir` "" —
    the "no migration" pin.
12. `cmd/relevo/bind.go`: extract `addedTreeLines` and use `relevo.RoundDir` for the
    `--json` `dir` — green; the new test lives in `cmd/relevo` because it renders no
    process, spawns no harness and reaches no network.
13. Full verification `make check`.

## 5. Tests that pin the rule, and the mutation each must catch

| Test (package) | Pins | Mutation it must kill |
| --- | --- | --- |
| `TestNormalizeSubdir` (relevo) | the stored form of a prefix | accept `/abs` or `a/../b` |
| `TestRoundDir` (relevo) | `CWD` + `Subdir` | return `b.CWD` → the subdirectory is dropped |
| `TestShowPrefix…` / `TestDirAt…` (git) | the two git reads | `DirAt` accepts a blob |
| `TestAddFromASubdirectoryRunsTheRoundThere` (relevo) | `CWD` = tree root, `Subdir` set, `roundTree` = tree+subdir | drop the join in `roundTree` |
| `TestAddRefusesASubdirectoryTheBaseCommitDoesNotHave` (relevo) | the refusal names it, and the rollback | skip `DirAt` |
| `TestAddFromTheRepositoryRootKeepsTheRoundAtTheRoot` (relevo) | no prefix, no join | join an empty prefix into `./` |
| `TestAddWithCWDNeverConsultsThePrefix` (relevo) | `--cwd` overrides | resolve the prefix on the `--cwd` path |
| `TestAddBranchAdoptsTheSubdirectoryToo` (relevo) | the `--branch` path | resolve the prefix only on the cut path |
| `TestGateRunsInTheRoundDirectory` (relevo) | the gate's `Dir` | `gate.go`'s `Dir: b.CWD` |
| `TestReaderRoundRunsInTheScratchSubdirectory` (relevo) | the reader join | join only the writer branch |
| `TestRemoteAddSendsTheSubdirectory` (relevo) | the wire value, and the mirror at "" | set it on the mirror too |
| `TestRemoteAddRefusesAServerWithoutTheSubdirectoryFeature` (relevo) | the refusal, nothing sent | send it anyway |
| `TestCreateBindingStoresSubdir` + `TestCreateInvalid` rows (serve) | the server stores it; a hostile value is 400 | accept `../up` |
| `TestChainRequestMatchesComparesTheSubdirectory` (serve) | the retry rule | treat a differing prefix as an identical retry |
| `TestChainMembersCarryTheSubdirectory` and its four siblings (relevo) | members inherit it | leave members at the tree root |
| `TestStatusRowNamesTheRoundDirectory` (relevo) | the row's `cwd` | `row.CWD = b.CWD` |
| `TestSubdirIsEmptyForARecordThatHasNone` (store) | old records keep their tree | default a missing key to anything but "" |
| `TestAddedTreeLinesNamesTheRoundDirectory` (cmd/relevo) | the bind output line | drop the line when `Subdir != ""` |

## 6. What is deleted (closed list)

1. The worktree root as the directory a `--worktree` or `--branch` bind's builder and
   gate run in, when the bind was started below the root.
2. The server worktree root as the served round's directory for such a bind.
3. The tree root as a chain member's round directory for a chain started, resumed,
   forked or served-added below the root.
4. The tree root as the served check's directory.
5. The status row's `cwd`, the bind document's `dir` and the added line's silence, all
   naming the tree root when the round runs in a subdirectory.
