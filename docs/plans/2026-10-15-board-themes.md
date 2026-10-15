# Owner amendments (they override the plan below)

- Plan A (the `repos` section, `RepoKey`, the per-repo gate) landed in the previous chain step; build on it. Re-read every line the plan cites before editing it.
- **The palettes do not live in Go.** The owner's decision: `cockpit` and `blueprint` are seed data written into the `themes` config section, and relevo reads palettes only from there. Concretely:
  - The two palettes' colours live in one embedded data file (for example `internal/board/theme/seed.json`), not in a Go table. No `var themes = []*Theme{...}` or equivalent literal survives anywhere.
  - Seeding writes each seed palette into the `themes` section when that name is absent. It runs **once per database**: on the database's first open after this release (through the existing config/migration seeding path; find it -- `config init` already seeds sections), and in `relevo config init`. Record that seeding ran (a config marker or a migration), so a palette the user later unsets **stays unset**; an existing entry is never overwritten.
  - Every machine seeds identical values, so sync converges on the per-item `themes/<name>` keys.
  - After seeding, every lookup reads the section. `relevo config get themes.cockpit --json` prints cockpit's palette on a fresh install, and `relevo config set themes.cockpit '{...}'` changes what the board paints.
  - `relevo board` must keep creating nothing. When no database exists yet, or it has never been seeded, the board reads the embedded seed file directly. Reading the seed file is the only fallback.
  - The default theme name is still `cockpit`. If the resolved name is not in the section, the refusal lists the names the section holds and says how to add one (`relevo config set themes.<name> '{...}'`).
  - Replace "built-ins stay in Go" and "a stored entry overrides the built-in" throughout the plan with the above. `TestThemeBuiltinsAreCockpitAndBlueprint` becomes a test that the seed file carries both palettes byte for byte as today's Go table does (`#0f1115`... for cockpit), and that a seeded database returns them from `config get`.
  - New tests: `TestThemesSeedOnceAndADeletionSticks` (seed, unset cockpit, reopen: still absent; mutation: re-seed on every open); `TestThemesSeedNeverOverwrites` (a user-edited cockpit survives a reopen; mutation: overwrite on seed); `TestBoardWithoutADatabaseReadsTheSeedFile` (mutation: refuse instead).
  - `scripts/check-board-theme.sh`'s comment names the seed file and the `themes` section as the palette's home.
- The librarian prompt sentence from the plan stands: read the palette with `relevo config get themes.<name> --json`, conditional on the repository using relevo boards. #1192 already reworded other lines in those four files; re-read them, then `sh scripts/agents-shipped.sh --write`.
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

# Plan B -- board palettes in the `themes` config section

Lands after Plan A. Everything Plan A left in place stays: the `repos` section,
its `board_theme` field (now consumed), the gate work, and the identity helper.

## Behaviour and cases

- A new config section `themes`, an object keyed by theme name, each value the
  eleven palette colours: `bg`, `ink`, `muted`, `faint`, `line`, `panel`,
  `accent`, `good`, `warn`, `bad`, `comment`. It rides the config machinery
  exactly as `repos` does: `relevo config get themes.<name> --json`,
  `relevo config set themes.<name> '{...}'`, `config unset`, the revision log,
  rollback, export/import, and one sync key per theme.
- A palette entry is whole: every one of the eleven fields is required, and a
  missing or unknown field is a refusal naming it. Reason: the page and its
  export paint with these values directly, so a partial palette would render
  elements with empty colours; inheriting from a built-in by name would make the
  same config mean different things in two releases.
- Colours are validated on set: `#` followed by 3, 4, 6 or 8 hex digits,
  case-insensitive, and nothing else (no named colours, no `rgb()`, no
  `var(...)`). Reason: the values are injected into CSS and SVG attributes in
  the page, and the built-ins are all `#rrggbb`.
- `cockpit` and `blueprint` stay in Go as the built-in defaults. A stored entry
  under either name overrides the built-in; a theme name that is neither stored
  nor built-in is a refusal that lists every stored and built-in name.
- Theme choice for a board, highest first:
  1. `--theme <name>`;
  2. the repo's `board_theme` (Plan A's record, read through the repo identity);
  3. the repo-local git config `relevo.boardTheme` (kept as a fallback so
     existing setups keep working);
  4. `cockpit`.
- The board page and its SVG export read their colours from the resolved theme:
  the server sends the resolved palette on `GET /api/scene` and the page paints
  new elements, the Mermaid surface and the exported SVG from it
  (`board/src/index.jsx:30-42`, `:83-101`, `:184-194`). The page already reads
  `doc.theme`, so **no page change and no asset rebuild is planned**: the
  committed bundle under `internal/board/assets` is regenerated only by
  `make board-assets`, which needs node and network and never runs in CI. If a
  builder finds the export genuinely ignores the palette, it must halt and
  report rather than hand-edit the bundle.
- The librarian prompt stops pointing at `internal/board/theme.go` and instead
  reads the palette with `relevo config get themes.<name> --json`, conditional
  on the repository using relevo boards, in all four shipped copies, then
  `sh scripts/agents-shipped.sh --write`.
- The README `relevo board` section documents reading, editing and adding
  themes.
- No TUI work.

## Seams

- New package `internal/board/theme`: `Theme` (moved verbatim from
  `internal/board/theme.go:34-51`), `FontFamily`/`Roughness` (`:27-32`), the two
  built-ins (`:53-69`), `Parse(name, body)`, `Lookup(name, stored)`,
  `Names(stored)`, `Encode(map[string]Theme)`, the colour validator, and
  `(*Theme).AppStateDefaults` (`:91-106`).
- `internal/board/theme.go` is deleted; `internal/board/palette.go` keeps
  `type Theme = theme.Theme` and the two constants so no call site changes;
  `internal/board/usage.go` takes `ErrUsage`, `usageError`, `usagef`
  (`theme.go:12-25`) unchanged.
- `internal/config`: `Themes Section = "themes"`, `Loaded.Themes
  map[string]theme.Theme`, `loadThemes` in `decodeDoc`
  (`internal/config/document.go:15-69`), the `Themes` case in `Validate`
  (`internal/config/config.go:363-413`), `Themes: true` in `perItemSections`
  (`internal/config/localkeys.go:18-24`), and the new `internal/config/themes.go`
  for parse/encode.
- `internal/config/never_syncs_test.go:16-60` bodies.
- `cmd/relevo/board.go:37-44` (`boardFlagSet`'s `--theme` help),
  `:253-260` (resolution before anything else), `:324-339`
  (`boardResolveTheme`), `cmd/relevo/board_comment.go:133`;
  `cmd/relevo/bugreport_sources.go:43-109` (`newRuntimeReadOnly`, the read-only
  runtime the board verb now uses).
- `internal/board/server.go:45`, `:54`, `:153` (the theme the server sends);
  `internal/board/server_test.go` (the scene document's theme);
  `internal/board/theme_test.go` (moves with the palette).
- `scripts/check-board-theme.sh:4`, `:46-51` (the comment that names the Go
  table); `internal/harness/agents/librarian.agy.md:41`,
  `librarian.claude.md:26`, `librarian.codex.toml:24`,
  `librarian.opencode.md:26`; `scripts/agents-shipped.sh --write` and
  `internal/harness/agents/shipped.sha256`.
- `README.md:1255-1340` (the `relevo board` section).
- `docs/specs/2026-10-10-sync-everything-design.md:157`, `:301`;
  `testdata/coverage-baseline.txt`.

## Ordered steps

Focused iteration is `make check-quick PKGS='./internal/board/... ./internal/config/... ./cmd/relevo/'`;
the gate is `make check`, run once at the end.

1. **`internal/board/theme`** delivering the palette type, the two built-ins,
   `Parse`, `Lookup`, `Names`, `Encode`, `AppStateDefaults` and the colour
   validator -- known good when the palette test still pins both built-ins byte
   for byte and each refusal has its own failing-without-it test.
2. **The board package shim** delivering `internal/board/palette.go` (the type
   alias and the two constants), `internal/board/usage.go` (the moved refusal
   vocabulary) and the deletion of `internal/board/theme.go` -- known good when
   `go build ./...` and the board tests pass with no call-site churn.
3. **The `themes` section** delivering the section constant and order,
   `Loaded.Themes`, `loadThemes`, the `Validate` case, `internal/config/themes.go`
   parsing each entry through `theme.Parse`, and the per-item sync key -- known
   good when `relevo config set themes.mine '{...}'` lands, a bad colour is
   refused naming the field, and `config get themes.mine --json` prints it.
4. **The board theme resolution** delivering the four-tier precedence, the
   stored set read through `newRuntimeReadOnly` plus the repo identity from
   Plan A, and the refusal listing stored and built-in names -- known good when
   the precedence table test passes, `relevo board --theme mine` serves the
   stored palette, and `relevo board` still creates no database.
5. **The page and its export** delivering the check that the resolved palette is
   what `GET /api/scene` carries and what the export paints from, with no change
   to `board/src` and no rebuild of `internal/board/assets` -- known good when
   the server test pins the stored palette in the scene document and the
   committed bundle is byte-identical.
6. **The librarian prompt** delivering the new sentence in all four copies
   (read the palette with `relevo config get themes.<name> --json`, conditional
   on the repository using relevo boards; no copy names
   `internal/board/theme.go`) and the regenerated
   `internal/harness/agents/shipped.sha256` -- known good when
   `sh scripts/agents-shipped.sh --check` passes and a grep finds no copy
   pointing at Go source.
7. **The README** delivering the `relevo board` section's paragraph on reading,
   editing and adding themes, with the commands as written actually working --
   known good when each command in the paragraph is run once by hand and does
   what the text says.
8. **The static guard and the spec** delivering the reworded
   `scripts/check-board-theme.sh` comment (the page still carries no colour; the
   palette's home is now the `themes` section, with the built-ins as defaults)
   and `themes` added to the sync spec's per-item list -- known good when
   `sh scripts/check-board-theme.sh` prints ok and the comment names no stale
   file.
9. **Coverage** delivering the baseline decision: the palette leaves
   `internal/board` for a new package, so run
   `RELEVO_REQUIRE_COVERAGE=1` and regenerate with
   `sh scripts/check-coverage.sh --write` if the move took `internal/board`
   more than a point down; a new package is reported as new and never fails.
   Say in the report whether it was regenerated. No baseline is lowered to get
   green.
10. **The plan** delivering this plan verbatim as
    `docs/plans/2026-10-15-board-themes.md` in the same PR.

## Named tests and the mutation each must catch

`internal/board/theme`:

- `TestThemeBuiltinsAreCockpitAndBlueprint` (the moved palette test) --
  mutation: change one colour of a built-in.
- `TestThemeParseRefusesABadColour` (table: `red`, `rgb(0,0,0)`, `#12345`,
  `0f1115`, `#`) -- mutation: loosen the validator to "starts with #".
- `TestThemeParseRequiresEveryField` -- mutation: default a missing field from
  the built-in instead of refusing.
- `TestThemeParseRefusesAnUnknownField` -- mutation: ignore unknown keys.
- `TestStoredThemeOverridesTheBuiltin` -- mutation: look the built-ins up first.
- `TestThemeLookupNamesEveryStoredAndBuiltinTheme` -- mutation: list only the
  built-ins in the refusal.
- `TestThemeAppStateDefaults` (moved) -- mutation: drop `viewBackgroundColor`
  for a new scene.

`internal/config`:

- `TestThemesSectionRefusesABadPalette` (through `Put`) -- mutation: remove the
  `Themes` case from `Validate`.
- `TestThemesSyncsOneKeyPerTheme` -- mutation: remove `Themes` from
  `perItemSections`.
- `TestThemesWriteIsARevisionAndRollsBack` -- mutation: remove `Themes` from
  `Sections`.
- `TestConfigNeverSyncs` (extended bodies) -- mutation: identical `themes`
  bodies on both machines.

`cmd/relevo` (CLI tests; the precedence rule is a pure function, and no test
here spawns a harness or reaches the network -- say so in the step that adds it):

- `TestBoardThemePrecedence` (extended: flag, repo `board_theme`, git config,
  cockpit) -- mutation: swap the repo and git tiers.
- `TestBoardRefusesAnUnknownThemeNamingStoredThemes` -- mutation: list only the
  built-ins.
- `TestConfigSetThemesStoresAPalette` / `TestConfigGetThemesPrintsIt` --
  mutation: parse the entry without validating a colour.
- `TestBoardServesTheStoredPalette` (the scene document's theme) -- mutation:
  send the built-in instead of the resolved palette.

Librarian prompt (no Go test; the guard is the shipped hash):
- `sh scripts/agents-shipped.sh --check`, run by `make check` through
  `scripts/agents-shipped_test.sh` -- mutation: edit a librarian copy without
  `--write`; `make check` fails naming the file.

## What is deleted (closed list)

1. The palette table's status as the only source of themes:
   `internal/board/theme.go`'s `themes` variable, `Names()` and
   `Lookup(name)` are deleted with the file; `cockpit` and `blueprint` survive
   as built-in defaults inside `internal/board/theme`.
2. The librarian instruction to read `internal/board/theme.go`: replaced in all
   four copies by the `relevo config get themes.<name> --json` sentence.
3. `scripts/check-board-theme.sh`'s claim that the palette's one home is the Go
   table: the comment is reworded; the rule it guards is unchanged.
4. Nothing else: the `relevo.boardTheme` git-config fallback is **kept** (a
   lower tier, not deleted), no verb, flag, colour or export behaviour is
   removed, no page asset is touched, and no TUI work is done.

## What the report must include

- `git diff --stat` against the declared scope, with every deviation named,
  including a positive statement that `board/src` and `internal/board/assets`
  are unchanged and why.
- `make check` output; the focused command used while iterating.
- For each named test: what it pins and the mutation that was run to watch it
  fail; for the librarian prompt, the `--check` failure from the mutation.
- Every refusal added, with the input that earns it (colour forms, missing
  field, unknown theme name).
- Whether `testdata/coverage-baseline.txt` was regenerated, the package the code
  moved out of, and the before/after figures.
- Anything deliberately left: the TUI, a page-side palette change (with the
  reason it would need node and network), and any name a theme may still not
  use.
