# Plan: #801 documentor, slice 2 — the scope check when a round closes

## Behaviour

A **writer actor** can declare a `scope`. When a round of a scoped actor closes, relevo diffs the round's baseline tree against the closed tree and judges every changed path. If any path is out of scope, the round still closes (its report, diff and usage are recorded) but:
- its outcome is forced to `halted`;
- the binding goes to **NEEDS YOU** with the offending file named;
- the gate never runs.

A writer with no `scope` closes exactly as it does today. The `documentor` actor that `config init` seeds gets the docs set.

Cases, in judging order for each changed path (`git diff --raw --no-renames` between the baseline and closed trees):
1. The mode is a symlink (120000) or gitlink (160000) on either side, or the file mode changed → **refused**.
2. The path matches the scope's path patterns → **in scope**. This covers added, deleted and modified files.
3. The scope allows comments, the file is `.go`, the status is M, and the old and new contents differ only in non-directive comments → **in scope**.
4. Status M on a `.go` file, scope allows comments, but the contents differ in a non-comment token → **refused** ("code change").
5. Same as 4, but a directive comment was added, removed or changed → **refused** ("directive comment").
6. Status M on a `.go` file that does not scan cleanly on either side, or imports `"C"` (the cgo preamble is code) → **refused** ("cannot judge").
7. A `.go` file added or deleted → **refused** ("new/deleted source file").
8. Anything else (another extension, binary, config, `testdata/`) → **refused** ("outside scope; cannot judge").

The round itself also refuses when it cannot be judged: no baseline tree, `rt.Git` nil, or a git error. That halts NEEDS YOU with the reason. Refuse, never guess.

## Decisions settled (one each)

1. **How a round declares its scope:** only through an actor field in relevo.db, `actors.<name>.scope`. There is no bind or send flag: a per-send flag would let the MasterMind loosen a mechanical check. Shape: `{"paths": [...], "comments": bool}`.
   - A `paths` entry is a glob over the repo-relative path. `*` matches within one segment, `**` matches any number of segments, and a leading `!` excludes. The last match wins.
   - `@docs` expands to the built-in docs set. An unknown `@name` is a validation error.
   - `scope` on a reader actor is refused, like `check` is.
   - **Seed:** `config init` writes `"scope": {"paths": ["@docs"], "comments": true}` on `documentor` (`internal/setup/setup.go:129`). There is no migration: an existing DB adds the scope with `relevo config edit`, and the README says so.
2. **When the actor is resolved:** when the round closes, from `rt.RoleRegistry()` for `bindingRole(b)`. It is not copied onto the binding, so:
   - `store.Binding` does not change and there is no `BindingFormat` bump;
   - chain members and repair rounds are covered automatically;
   - a served binding is judged by the server's own actor config, the same rule #382 applies to shape and definitions.
   
   A role the registry no longer knows at close is treated as unscoped, with an `slog.Warn`.
3. **Where the check runs:** in `closeOnMarker` (`internal/relevo/reconcile.go:316`), once the marker is present.
   - **Scope fires before the gate.** When `b.GateRun == nil`, judge first. On refusal, skip `gateStep`: no gate record, no `KindGate` entry, no repair round, `Regate` untouched.
   - On a pass, the gate runs as today. On the tick a gate finishes (`rec != nil`), judge again, because the tree can move while the gate runs.
   - **A scope halt is not a gate failure:** `gate=` is absent from the note.
4. **How a refusal shows up:**
   - The report entry is queued as usual, with outcome `halted`, `HaltedAt` = `scope: <first path>`, and note `scope=refused`. A pass adds note `scope=ok`, and only for scoped actors.
   - The payload gains the line `Scope: refused -- <path>: <reason> (+N more)` followed by the `relevo show … --diff` hint.
   - After the round advances, `haltBinding` sets NEEDS YOU with `<name>: round N changed <path> outside actor <actor>'s scope (<reason>)[; N more]`. This goes in the same place `queueReport` does the reader artifact-cap halt (~L659).
   - The forced outcome is set before `chainEventFromClose` (`reconcile.go:447`), so a chain member's refusal halts its chain instead of advancing it.
5. **The comment rule (Go):** scan the old and new blobs with `go/scanner` in `ScanComments` mode.
   - The non-comment token streams must be equal: compare (tok, lit), except `SEMICOLON`, which is compared by kind only.
   - The ordered lists of directive comments must be equal.
   - Whitespace and gofmt alignment changes therefore pass.
6. **The directive list:** a table compiled into the check package, one per language. It is not in config: an editable list would let an actor loosen its own check. The Go table:
   - `//go:`;
   - `// +build`;
   - `//line ` and `/*line `;
   - `//export ` and `//extern `;
   - `//nolint` and `// nolint`;
   - `//lint:`;
   - anything containing `#nosec`;
   - `^// Code generated .* DO NOT EDIT\.$`;
   - comments whose text starts `Output:` or `Unordered output:`.
   
   Other languages are not judgeable in this slice, so a change to them is refused.
7. **The docs set (`@docs`):**
   - Included anywhere in the tree: `**/*.md`, `**/*.markdown`, `**/*.mdx`, `**/*.mmd`, `**/*.svg`, `**/*.excalidraw`.
   - Excluded: `!**/testdata/**`, `!vendor/**`, `!**/node_modules/**`.
   - It does not cover all of `docs/**`, because `docs/specs/probes/` carries `.ts` and `.sh`.
   - Generated Go files are caught by the codegen marker in rule 6.
   - Known consequence in this repo: editing a shipped agent definition (`internal/harness/agents/*.md`) needs `shipped.sha256` regenerated. That file is out of scope, so such a round halts, which is intended.

## Seams

- **New package `internal/pathscope`:**
  - `scope.go`: `Scope{Paths; Comments}`, `Validate`, the matcher, `DocsSet`.
  - `golang.go`: the Go comment judge and directive table.
  - `judge.go`: `Change`, `Violation{Path, Reason}`, and a pure `Judge(scope, changes, readBlob)`.
- **`internal/git/scopediff.go`:**
  - `ChangedFiles(ctx, dir, from, to)` runs `git diff --raw -z --no-renames --abbrev=40`.
  - `ReadBlob(ctx, dir, oid)` runs `git cat-file blob`.
  - Add both to the `relevo.Git` interface (`internal/relevo/runtime.go:36-100`) and to `internal/relevo/fake_test.go`.
- **`internal/roles`:**
  - `Actor.Scope *pathscope.Scope` (`actors.go:48-60`), with validation in `validateActor`.
  - In `rowFor`, the reader refusal and the copy to `Row.Scope` (`actors_convert.go:39-78`).
  - `Row.Scope` (`file.go:47-83`), with the reader refusal at `file.go:184`.
  - `Role.Scope` (`registry.go:54-85`), copied in `buildFile` (~L207).
- **`internal/relevo`:**
  - New `scopecheck.go`: `judgeRoundScope(ctx, rt, b)`.
  - Wire it into `closeOnMarker` (`reconcile.go:316-371`).
  - `queueReport` (`reconcile.go:373`) takes the verdict. All other callers pass a zero verdict (`remote_catchup.go:269`, `headless.go` ~L850, and the rest found by grep). The builder may bundle the trailing optional parameters into a struct.
  - `EditActor` (`configedit.go:403-433`) clears `Scope` when the agent becomes a reader.
- **Surfaces that show an actor:**
  - `internal/view/actors_list.go` `formatActor` (~L72): `scope @docs + comments`.
  - `internal/view/roles_list.go` `formatRole` (~L44).
  - `internal/ui/view_actor.go`: a scope line under `actorCheckLine`.
  - Their goldens.
- **`README.md`** ~L1397-1420: replace "That contract is prose in the definition; nothing inspects the diff." with the scope contract (the field, `@docs`, comments, directive refusal, the NEEDS YOU halt, the server-config rule for remote rounds, the seed, and `config edit` for existing DBs).

## Steps (each is one or more new commits; never amend or rebase a commit already pushed)

1. **`pathscope` Scope, matcher, `@docs` and Validate.** Done when `go test ./internal/pathscope/` passes and covers: md in, `testdata/x.md` out, `docs/specs/probes/x.ts` out, `!` last-wins, unknown `@set` refused.
2. **Go comment judge, directive table and `Judge`.** Done when one named pure test per case 1–8 passes.
3. **`git.ChangedFiles` / `ReadBlob`**, tested against a real temp repo in `internal/git`. Done when add, modify, delete, mode change and rename (seen as delete+add) all parse.
4. **roles field, validation and carry-through.** Done when `go test ./internal/roles/` passes, including the round-trip, the reader refusal in both paths, and `Role.Scope` populated.
5. **relevo wiring.** Tests live in `internal/relevo`, use the fake Git, and spawn no harness. Done when these pass:
   - `TestScopeDocsOnlyClosesClean`
   - `TestScopeStatementChangeHaltsNamingFile` (NEEDS YOU, the Halt text names the path, Outcome `halted`, note `scope=refused`, no `KindGate`)
   - `TestScopeDocCommentOnlyClosesClean`
   - `TestScopeDirectiveChangeRefused`
   - `TestUnscopedWriterUnaffected`
   - `TestScopeNoBaselineRefuses`
   - `TestScopeRejudgedAfterGate`
   - `TestScopeRefusalHaltsChainMember`
6. **Seed in `setup.go`**, plus a `setup_test.go` assertion. Refresh any `cmd/relevo` contract golden (e.g. `config-log.golden`). No CLI test may run a subcommand that spawns a harness or reaches the network.
7. **Views and goldens.** Done when the documentor shows its scope and other writers show nothing new.
8. **README.** Done when `grep -n "nothing inspects the diff" README.md` is empty.
9. **Save this plan** as `docs/plans/2026-10-01-documentor-s2-scope-check.md`.
10. **Full check.**
    - Focused: `go test ./internal/pathscope/ ./internal/git/ ./internal/roles/ ./internal/relevo/ ./internal/setup/ ./internal/view/ ./internal/ui/ -run 'Scope|Documentor|Actor'`.
    - Final: `make check`.
    - Run `sh scripts/check-coverage.sh --write` only if the new package needs an entry, and report it.
    - No new exclusions, and no lowered baselines.

## Not done in this slice

- Remote client re-judging, and a server feature flag.
- Comment judgement for languages other than Go.
- Slice 3: the chain step and cockpit rows.
- Migrating existing DBs' documentor actor.

## Mutation list (each must fail a named test)

1. `Judge` returns nil → `TestScopeStatementChangeHaltsNamingFile` fails.
2. Remove the `judgeRoundScope` call → the same test fails.
3. Drop the `//go:` directive entry → `TestScopeDirectiveChangeRefused` fails.
4. Include comments in the token comparison → `TestScopeDocCommentOnlyClosesClean` fails.
5. Move the check after `gateStep` → the no-`KindGate` assertion fails.
6. Skip the forced outcome → `TestScopeRefusalHaltsChainMember` fails.
7. Drop the scope from the seed → the setup test fails.
8. Drop the `testdata` exclusion → the pathscope test fails.

## The report must include

- Per-step status and the new commits.
- `git diff --stat` against these seams.
- The `make check` result, and whether the coverage baseline was regenerated.
- Each mutation, run, with the test that failed.
- The goldens refreshed.
- Any `queueReport` refactor.
- The remote/server limitation and the README change.
- A halt instead of improvising if a seam contradicts the code.

## Reviewer note (MasterMind, before the build)

One acceptance detail to pin in step 1's tests: the `@docs` globs must match root-level files too — `**/*.md` matches `README.md`, and `**/*.svg` would match a root-level `.svg`. A README edit is in scope by slice 1's contract, so `**` must match zero directories.

