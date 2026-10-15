# Owner amendments (they override the plan below)

- Base is origin/main at d7ec498f, not the commit the plan was written against. Since then #1192 (check wording, empty default workflow gate, `bind --branch` remote pick, agent prompt wording) and #1193 (ticket forms; touched `internal/relevo/add.go`, `bind.go`, `history.go`, `internal/store/types.go`, `internal/mastermind/guide.md`) landed. Re-read every line the plan cites before editing it.
- The shipped default workflow's `gate` param is now `""` (#1192), not `make check`; `chainParamsFor` still fills it from policy for the default workflow, and the repo tier goes there as the plan says.
- The plan's one judgement call stands: a role whose check flag is off takes no gate even when the repo sets one.
- Only Plan A is in this round. Plan B (themes) is the next chain step; do not start it.
- No TUI work.

---

# Per-repo settings (`repos`) and board palettes (`themes`)

Two plans, in order. **Plan A lands first**; Plan B is written against the tree
Plan A leaves. Each plan is a separate PR and each is saved into the repo as its
own plan document:

- Plan A saves itself to `docs/plans/2026-10-15-repos-settings.md`.
- Plan B saves itself to `docs/plans/2026-10-15-board-themes.md`.

Seed: `001-prompt.md`. Decisions there are not reopened; where this plan adds a
decision the seed left open, it says so and gives the reason.

## Seed-vs-code check (findings, before either plan)

1. **The repo identity.** `bind --server` identifies a repository by
   `remote.RepoID(rootCommit)` (`internal/remote/proto.go:547-557`), the
   lower-case hex sha256 of the root commit SHA, computed from
   `git.Client.RootCommit` (`internal/git/refs.go:116-133`) at
   `internal/relevo/chain_remote.go:151-157`. That hash -- not the raw 40-hex
   root SHA -- is the identity that already travels on the wire
   (`remote.CreateBindingRequest.RepoID`, `internal/remote/proto.go:115`), so
   both plans key the new section on it and reuse the same two calls rather than
   deriving a second identity.
2. **"In relevo.db" is loose, and it matters.** Config sections live in
   `config_doc`, which after the local/shared split sits in the machine-local
   file (`internal/config/config.go:115-139`, `local_split_test.go`), and they
   reach other machines as *shared config doc* entries (`doc:config`,
   `internal/config/sharedoc.go:12-35`, `docs/plans/2026-10-12-sync-shared-config.md`).
   So `repos`/`themes` ride the existing machinery exactly as the seed asks --
   `config get/set/unset`, revision log, rollback, export/import, sync -- but
   "in relevo.db" means "in the config document", and the sync half is what
   carries a setting to a peer. No new table, no committed file.
3. **Both new sections must be per-item.** `perItemSections`
   (`internal/config/localkeys.go:18-24`) decides whether a section syncs as one
   key per item or as one whole-section key. A `repos` section that syncs whole
   would make two machines editing two different repos displace each other's
   entry; a `themes` section that syncs whole would do the same for two
   palettes. Both go in the map, which is also what makes
   `config get repos.<key>` address one entry.
4. **The sync spec names the per-item sections by hand.**
   `docs/specs/2026-10-10-sync-everything-design.md:157` and `:301` list
   `candidates, agents, actors, accounts, servers`. Both plans amend that list
   in the round that adds its section, so the spec does not drift.
5. **`scripts/check-board-theme.sh` is a `make check` gate**
   (`Makefile:50`), and its rule (a) is framed on the palette's one home being
   the Go table in `internal/board/theme.go` (`scripts/check-board-theme.sh:4`,
   `:46-51`). Plan B must reword that comment. The rule itself survives: the
   page still carries no hex literal, so the guard keeps working.
6. **`internal/board/theme.go` holds more than the table.** `ErrUsage`,
   `usageError` and `usagef` (`theme.go:12-25`) are the refusal vocabulary of the
   whole board package (`agent.go`, `comment.go`, `html.go`, `live.go`,
   `path.go`, `scene.go`). They stay in `internal/board` when the palette moves.
7. **`AppStateDefaults` has no production caller.** `(*Theme).AppStateDefaults`
   (`internal/board/theme.go:91-106`) is reached only by its own test; the page
   builds the same appState in JS (`board/src/index.jsx:30-42`). Plan B moves it
   with the palette and does not delete it -- deleting it is not this work.
8. **`relevo board` opens no database today.** `cmdBoard` and `cmdBoardComment`
   never call `newRuntime`; the board's git reads are raw `git` exec seams
   (`cmd/relevo/board.go:59-88`). Reading the `themes`/`repos` sections from the
   board verb therefore uses the existing read-only runtime
   (`newRuntimeReadOnly`, `cmd/relevo/bugreport_sources.go:43-109`), which
   creates, migrates and writes nothing, rather than `newRuntime`. `relevo board`
   must keep creating nothing.
9. **The chain's gate is resolved twice, in two shapes.**
   `chainSettings` (`internal/relevo/chain_start.go:177-204`) fills the chain
   row's `Settings.Gate` for every workflow; `chainParamsFor`
   (`internal/relevo/chain_params.go:33-71`) fills the shipped default
   workflow's `gate` param and is reached only by `chain --dry-run`
   (`internal/relevo/chain_dryrun.go:67`). The repo tier goes in both.
10. **A chain's repository is `os.Getwd()`**, resolved *after* the params and
    settings are computed (`internal/relevo/chain_start_wf.go:57-73`,
    `internal/relevo/chain_server_start.go:86-96`). Plan A reorders those two
    blocks so the settings can be read first. A fork's child chain uses
    `parent.Repo` (`internal/relevo/chain_fork_start.go:76`).
11. **The gate-flag resume paths never fall back.** `chain_resume.go:109`,
    `chain_resume_wf.go:259` and `chain_server_view.go:208` resolve only when
    `--gate`/`--no-gate` was given, and the flag wins over every lower tier, so
    those three pass an empty repo record and their behaviour is unchanged.
12. **No contradiction found** between the seed and the code beyond the wording
    above. Nothing in either plan requires a rewrite of a commit already on a
    remote branch: both are ordinary new commits.

# Plan A -- the `repos` config section and the per-repo gate

## Behaviour and cases

- A new config section `repos`, an object keyed by the repo identity of
  finding 1, each value an object with three optional string fields:
  `{"name": "<label>", "gate": "<cmd>", "board_theme": "<theme>"}`. `name` is a
  display label only. An absent field is absent, not empty; a field set to `""`
  is stored as absent; an entry with no field left is not stored at all.
- The section is read and written through the existing config machinery:
  `relevo config get/set/unset`, `config log`, `config rollback`,
  `config export/import`, and the shared config doc. It is a per-item section,
  so it syncs as one key per repo.
- A thin CLI, CLI only (no TUI work):
  - `relevo config repo [--repo <path>] [--json]` -- the current repo's settings.
  - `relevo config repo set <key> <value> [--repo <path>] [--json]`.
  - `relevo config repo unset <key> [--repo <path>] [--json]`.
  - `relevo config repo list [--json]`.
  - `<key>` is `name`, `gate` or `board_theme`; anything else is a usage refusal
    naming the three.
- Gate precedence, applied to `bind`, `bind --worktree` (add), `bind --server`
  (remote add, resolved on the client and sent) and chains:
  1. `--gate <cmd>` / `--no-gate` (unchanged: `--no-gate` wins over everything);
  2. the repo's `gate`;
  3. `policy.gate.default`;
  4. no gate.
  The repo tier sits exactly where the policy tier sat, i.e. behind the
  existing role rule: a role whose check flag is off (`roleChecks` false, a
  reader round) still takes no gate, and `--gate` on a reader round is still
  refused. Reason: the seed's precedence lists the repo tier between the flag
  and the policy default, and the role rule is about whether any check runs at
  all, not about which tier supplies it. This is the one judgement call this
  plan adds; it is named here so a reviewer can overrule it.
- `bind`, `bind --worktree` and `chain` print which gate applied and its source,
  through the existing notice writer (stdout for a human, stderr under `--json`,
  so no document shape changes):
  - `gate: make check (--gate)`
  - `gate: make check (repo setting)`
  - `gate: make check (policy.gate.default)`
  - `gate: none`
  A `bind --resume` prints the stored command instead: `gate: make check
  (binding)`.
  `chain` keeps its existing `check:` line and gains the same parenthetical:
  `check: make check (repo setting)`.
- A repo with no commits has no settings: no error. A directory that is not a
  repository has no settings either on the gate path (a bind must not start
  failing because git cannot identify the tree); `relevo config repo` on a
  non-repository is a usage refusal.
- A machine whose `repos` section is empty reads no git for this at all, so a
  machine that never used the feature pays nothing and a git that cannot answer
  cannot break its binds.
- `board_theme` is stored as an opaque string in Plan A: Plan B owns the set of
  known theme names and refuses an unknown one at resolution time.

## Seams

- `internal/config/config.go:24-40` (`Section` constants, `Sections` order),
  `:82-98` (`Loaded`), `:363-413` (`Validate`), `:164-197` (`loadPolicy`, the
  shape a section loader takes).
- New `internal/config/repos.go`: `RepoEntry`, `ParseRepos`, `EncodeRepos`, the
  per-entry validation, and the "empty entry is dropped" rule.
- `internal/config/document.go:15-69` (`decodeDoc`), `:101-104`
  (`validateProspective`); `internal/config/sectionwrite.go:19-72` (`Put`),
  `:127-192` (`PutDoc`), `:257-277` (`checkDocSections`, `known`).
- `internal/config/localkeys.go:18-24` (`perItemSections`),
  `:76-119` (`splits`, `splitNamedObject`), `:125-147` (`joins`).
- `internal/config/sharedoc.go:95-117` (`itemsOf`), `:151-155` (`syncs`),
  `:321-327` (`docKeyOf`); `internal/config/diff.go:24-39`, `:57-92`;
  `internal/config/revision.go:60-140` (`record`).
- `internal/config/never_syncs_test.go:16-60` (`syncBaseBodies`,
  `changedBodies`), `:96-159` (`TestConfigNeverSyncs`).
- New `internal/relevo/repo.go`: `RepoKey(ctx, g Git, dir)`, `RepoSettings`,
  `RepoSettingsFor(ctx, rt, dir)`, `GateResolution`, `GateSource`, and the
  rewritten `resolveGateFor`. `resolveGateFor` currently lives at
  `internal/relevo/bind.go:608-623`.
- `internal/relevo/runtime.go:37-134` (`Git` interface), `:204` (`Config`);
  `internal/relevo/chain_remote.go:130-166` (`chainRemoteFacts`, which must call
  the new `RepoKey` instead of its own two lines).
- Gate call sites: `internal/relevo/bind.go:740` (create),
  `internal/relevo/add.go:414` (local add), `internal/relevo/remote_add.go:226`
  (remote add), `internal/relevo/chain_start.go:184` (`chainSettings`),
  `internal/relevo/chain_params.go:44` (`chainParamsFor`), and the three
  resume-only sites `chain_resume.go:109`, `chain_resume_wf.go:259`,
  `chain_server_view.go:208` (empty record, see finding 11).
- `internal/relevo/candidate.go:67-96` (`Resolution`, which carries the gate
  resolution back to the CLI); `internal/relevo/add.go:94-102` (`AddResult`);
  `internal/relevo/chain_start.go:84-90` (`ChainResult.Check`).
- `internal/relevo/chain_start_wf.go:52-73`, `chain_server_start.go:86-96`,
  `chain_fork_start.go:66-80` (repo resolution order), `chain_dryrun.go:61-74`.
- `cmd/relevo/bind.go:310-360` (`runBind` notices), `:430-460` (`runAdd`
  notices); `cmd/relevo/args.go:233-247` (`noteRegateNoGate`, `noticeWriter`);
  `cmd/relevo/chain.go:390-397` (`chainDocOf`), `:440-470` (`chainStartedText`,
  `chainCheckText`).
- New `cmd/relevo/config_repo.go`: the four verbs, their flag sets, the two
  documents, and the repo-key lookup. `cmd/relevo/config.go:16-37`
  (`configUsage`), `:43-83` (`cmdConfig` dispatch);
  `cmd/relevo/config_edit.go:488-496` (`rawJSONorString`);
  `cmd/relevo/writejson_outcomes.go:75-92` (`ConfigWriteDoc`,
  `configWriteDocOf`).
- `cmd/relevo/registry.go:29-118` (`verbFlagSets`),
  `cmd/relevo/registry_rows.go:99-285` (the `config` rows).
- `internal/git/refs.go:116-133` (`RootCommit`, `ErrRefMissing`);
  `internal/remote/proto.go:547-557` (`RepoID`).
- `README.md:412` (the `relevo config` verb list), `README.md:2002-2023` (the
  `## Gate` section).
- `docs/specs/2026-10-10-sync-everything-design.md:157`, `:301`;
  `testdata/coverage-baseline.txt`.

## Ordered steps

Each step names its deliverable and how it is known good. Focused iteration is
`make check-quick PKGS='./internal/config/... ./internal/relevo/... ./cmd/relevo/'`;
the gate is `make check` (gofmt, vet, lint, tidy, the shell checks and
`go test -race -count=1 -cover ./...`), run once at the end. `go test ./internal/config/ ./internal/relevo/ ./cmd/relevo/ -count=1` is the narrow run when a
step touches only those packages.

1. **`internal/config/repos.go`** delivering `RepoEntry{Name, Gate, BoardTheme}`,
   `ParseRepos`, `EncodeRepos` and the per-entry rules (an object; the three
   known string fields; an unknown field, a non-string value and a non-object
   entry each refused by name; empty fields dropped; an entry left empty
   dropped) -- known good when the parse table test round-trips a full entry and
   refuses every malformed shape.
2. **Section wiring** delivering `Repos Section = "repos"` in the constants and
   in `Sections`, `Loaded.Repos map[string]RepoEntry`, a `loadRepos` called from
   `decodeDoc`, and the `Repos` case in `Validate` -- known good when
   `relevo config get repos.<key> --json` prints a stored entry, `config
   export`/`config import` carry it, and the existing config tests pass.
3. **Per-item sync** delivering `Repos: true` in `perItemSections` so
   `Items()` emits `repos/<key>` and `Apply` writes one key -- known good when
   the items-shape test lists exactly the stored keys and an apply test lands
   one entry leaving the others untouched.
4. **The revision machinery proof** delivering a test that a `repos` write is a
   revision and a rollback removes it, and that an export/import round trip
   keeps the section -- known good when both pass with no code beyond step 2
   (the machinery is generic; this step proves the new section is inside it).
5. **`internal/relevo/repo.go`: the identity and the settings** delivering
   `RepoKey(ctx, g Git, dir) (string, bool, error)` (reusing
   `rt.Git.RootCommit` + `remote.RepoID`), `RepoSettings{Key, Name, Gate,
   BoardTheme}` and `RepoSettingsFor(ctx, rt, dir)` (reads `rt.Config`; no
   config store, an empty section, `git.ErrRefMissing`, `git.ErrNotRepo` and
   `git.ErrGitUnavailable` each mean "no settings, no error"; any other error
   propagates), and rewiring `chainRemoteFacts` onto `RepoKey` -- known good when
   the existing chain-remote tests pass unchanged and the new identity tests
   pass.
6. **The gate rule** delivering `GateResolution{Command, Source}`,
   `GateSource` words (`--gate`, `repo setting`, `policy.gate.default`), and
   `resolveGateFor(gate string, noGate bool, pol policy.Policy, roleChecks bool,
   repo RepoSettings) GateResolution` with the four tiers of the seed -- known
   good when the precedence table test passes for every tier including the
   role-check guard.
7. **The bind and add paths** delivering the repo settings read and the
   resolution stored: `internal/relevo/bind.go` `create` (settings for
   `opts.CWD`), `internal/relevo/add.go` (settings for `opts.Repo`),
   `internal/relevo/remote_add.go` (settings for `opts.Repo`, on the client, in
   the create request), and the resolution carried out on `Resolution.Gate` --
   known good when a bind, a local add and a remote add each store the repo's
   gate and report the source.
8. **The chain paths** delivering the repo settings threaded into
   `chainSettings` and `chainParamsFor`, with the repository resolved
   (`os.Getwd()` / `parent.Repo`) *before* the params in
   `chainResolveWorkflowStart`, `chainServerResolve`, `chain_fork_start` and
   `ChainDryRun`, and the three resume-only sites passing an empty record --
   known good when a chain start stores the repo gate in `Settings.Gate` and
   `chain --dry-run` shows it in `params`.
9. **The printed line** delivering one renderer for `gate: <cmd> (<source>)` /
   `gate: none` used by `runBind` and `runAdd` through `noticeWriter`, the
   `(binding)` form on a resume, and `chainStartedText`'s `check: <cmd>
   (<source>)` -- known good when a table test covers all five renderings and a
   `--json` bind keeps stdout a single document with the line on stderr.
10. **`cmd/relevo/config_repo.go`** delivering the four verbs, `--repo` and
    `--json`, the two documents (`ConfigRepoDoc` for show, a list document for
    list), reuse of `ConfigWriteDoc` for `set`/`unset`, and the usage text in
    `configUsage` plus the four dispatch cases -- known good when the four verbs
    round-trip on a temporary repository and the registry parity tests pass.
11. **The registry rows** delivering `config repo`, `config repo set`,
    `config repo unset`, `config repo list` in `registry_rows.go` and their four
    installers in `verbFlagSets` (flags sorted, matching the installers exactly)
    -- known good when `TestRegistryFlagsMatchFlagSets` and
    `TestRegistryMatchesDispatchersBothWays` pass with no other change.
12. **Docs and the plan** delivering the `## Gate` precedence paragraph and the
    `relevo config` verb list in `README.md`, the per-item list in
    `docs/specs/2026-10-10-sync-everything-design.md`, and this plan saved
    verbatim as `docs/plans/2026-10-15-repos-settings.md` -- known good when the
    documented precedence matches the code and the verb list names `config repo`.
13. **Coverage** delivering the baseline decision: run
    `RELEVO_REQUIRE_COVERAGE=1` against a fresh `.coverage.txt`; regenerate with
    `sh scripts/check-coverage.sh --write` only if a package the round moved code
    between dropped more than a point, and say so in the report. No baseline is
    lowered to get green.

## Named tests and the mutation each must catch

`internal/config`:

- `TestReposEntryRoundTripsEveryField` -- mutation: drop `BoardTheme` from the
  struct or its json tag; the round trip loses the field.
- `TestReposSectionRefusesABadShape` (table: not an object, unknown field,
  non-string value, `null` entry) -- mutation: remove the unknown-field check.
- `TestReposEmptyEntryIsNotStored` -- mutation: keep an entry whose fields are
  all empty.
- `TestReposSyncsOneKeyPerRepo` -- mutation: remove `Repos` from
  `perItemSections`; the document key collapses to `repos`.
- `TestReposWriteIsARevisionAndRollsBack` -- mutation: remove `Repos` from
  `Sections`; the write leaves no revision for it.
- `TestReposSectionSurvivesExportImport` -- mutation: same removal.
- `TestConfigNeverSyncs` (extended bodies) -- mutation: give `repos` the same
  body on both machines; the test's own guard fails on it.

`internal/relevo`:

- `TestResolveGatePrefersRepoOverPolicy` (table: flag, repo, policy, none) --
  mutation: swap the repo and policy tiers.
- `TestResolveGateTakesNoGateForARoleThatDoesNotCheck` -- mutation: drop the
  `roleChecks` guard.
- `TestRepoKeyIsTheRootCommitID` -- mutation: key on the raw root SHA instead of
  `remote.RepoID(root)`.
- `TestRepoKeyWithoutCommitsIsNoSettings` (`fakeGit.rootCommitErr =
  git.ErrRefMissing`) -- mutation: return the error instead of `ok=false`.
- `TestRepoSettingsCostsNoGitWhenTheSectionIsEmpty` -- mutation: move the
  `RepoKey` call above the empty-section check.
- `TestBindStoresTheRepoGate` -- mutation: pass the zero record at
  `bind.go:740`.
- `TestAddStoresTheRepoGate` / `TestRemoteAddSendsTheRepoGate` -- mutation: the
  same at `add.go:414` / `remote_add.go:226`.
- `TestChainStartTakesTheRepoGate` -- mutation: drop the tier from
  `chainSettings`.
- `TestChainDryRunFillsGateFromTheRepo` -- mutation: keep
  `pol.GateDefault()` in `chainParamsFor`.
- `TestGateLineNamesTheSource` (all five renderings) -- mutation: print the
  command with no source, or always `policy.gate.default`.

`cmd/relevo` (CLI tests; each rule is a pure function in `internal/relevo` or
`internal/config`, and no test here spawns a harness or reaches the network --
say so in the step that adds it):

- `TestConfigRepoSetShowUnsetRoundTrip` (a `git init` temp repo, one commit) --
  mutation: write the entry under a different section or key.
- `TestConfigRepoUnsetDropsAnEmptyEntry` -- mutation: keep the empty record.
- `TestConfigRepoJSONAndListJSON` -- mutation: omit `board_theme` from the
  document.
- `TestConfigRepoRefusesAnUnknownKey` -- mutation: accept any key.
- `TestConfigRepoRefusesOutsideARepository` -- mutation: treat `ErrNotRepo` as
  "no settings" and exit 0.
- `TestRegistryFlagsMatchFlagSets` / `TestRegistryMatchesDispatchersBothWays`
  (existing) -- mutation: add a registry row with no installer, or an installer
  with no row.

## What is deleted (closed list)

1. The old four-argument `resolveGateFor` (and the four callers' old shape): it
   is replaced by the five-argument form returning a resolution. No behaviour is
   lost; the policy tier keeps its place, one tier lower.
2. The machine-wide reach of `policy.gate.default`: it still applies, but no
   longer in a repository that sets its own `gate`. That is the point of the
   round, and the only behaviour this round narrows.
3. Nothing else: no verb, no flag, no stored binding field, no wire field, no
   config section and no refusal is removed, and no TUI work is done.

## What the report must include

- `git diff --stat` against the declared scope, with every deviation named.
- `make check` output; the focused command used while iterating.
- For each named test: what it pins and the mutation that was run to watch it
  fail.
- Every refusal added, with the input that earns it.
- Whether the coverage baseline was regenerated, and why (or that it was not).
- Anything deliberately left: the TUI, `board_theme` resolution (Plan B), and
  any seed decision this plan narrowed, named as in "Behaviour and cases".

