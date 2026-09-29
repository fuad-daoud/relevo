# The MasterMind rename

Owner decisions, 2026-09-27. This plan covers exactly one builder round: the
attached session is renamed from `planner` to `mastermind`.

Commit this plan as `docs/plans/2026-09-27-mastermind-rename.md` in this
round's commit (last step).

Halt and report instead of improvising if a step below is impossible as written
or contradicts the code. Never delete a test to get green; retarget it.

# Round 1 — the attached session is the MasterMind

## 1. System Overview

The session that runs relevo from inside a harness is currently called
`planner` everywhere: the `internal/planner` package, `relevo planner …`,
`--planner`, `RELEVO_PLANNER`, the `planner` db table, `binding.planner_id`,
the `planner/` kv prefix, the injected prompt text, doctor, the plugins, the
UI. It becomes **MasterMind**. The *actor* names `planner`, `lite-planner`
and the agent `architect` do **not** change: after this round `planner` means
the actor only.

Ten decisions this round executes. Do not re-open them:

- **D1 — the word rule.** Human- and model-facing text writes `MasterMind`
  (plural `MasterMinds`). Compile-time and wire identifiers are lowercase
  `mastermind`: Go identifiers (`MasterMindID` exported, `mastermindKeyPrefix`
  unexported), CLI verbs and flags, `RELEVO_MASTERMIND`, the package, the db
  table/column/index names, kv keys, JSON keys, paths.
- **D2 — stored spellings keep their bytes.** Every value already written
  keeps its old spelling and the new code writes that same old spelling, with
  the Go identifier renamed and a one-line *why* comment. The full list is §3.3.
  This is `internal/store/binding.go`'s `Builder`/`"runner"` precedent: field
  name and wire spelling may differ, and the wire spelling is the historical
  one.
- **D3 — the database is the one exception.** Migration `008` renames the
  table, the column, the indexes and the kv prefix, one transaction (§3.1).
- **D4 — ids.** `NewID` mints `mm_` + 12 base32 chars; `ValidID` accepts
  `mm_…`, the historical `pl_…`, and a legacy 26-char ULID. Existing records
  keep their ids.
- **D5 — CLI and env.** `relevo mastermind init|list|rename|forget`;
  `--mastermind`, `--all-masterminds`; `RELEVO_MASTERMIND` is written and
  read. `relevo planner …` exits 2 through `removedVerbs`; `--planner` and
  `--all-planners` become unknown flags (`flag provided but not defined`,
  the code every removed flag spelling gets since A4's clean break).
  `Resolve` still reads
  `RELEVO_PLANNER` after `RELEVO_MASTERMIND` (§4.2): a session's
  `$CLAUDE_ENV_FILE` line is state already written.
- **D6 — live JSON keys rename.** `view.BindingStatus` keys become
  `mastermind_id|name|kind|route|route_live|chat_label|chat_link`, the
  statusline document key becomes `"mastermind"`. This is *not* bind.json:
  those keys are D2.
- **D7 — actors stay.** `setup.PlannerDefaults`, the actor literals
  `"planner"`/`"lite-planner"`, `actors.planner`, the README examples and the
  `architect` agent name keep the word. `setup.go`/`setup_test.go` are
  *skipped* by the sweep.
- **D8 — doctor matches the new hook.** `hookRunsPlannerInit` becomes
  `hookRunsMasterMindInit` and matches `relevo mastermind init` only. A plugin
  installed by an older relevo FAILs with the existing "reinstall the relevo
  plugin" fix — the old command exits 2, so a green row would be a lie.
- **D9 — history is not rewritten.** `docs/plans/**`, `docs/specs/**`,
  `docs/superpowers/**`, `internal/legacy/**`, `scripts/rename-relevo.sh`,
  `scripts/check-name*.sh`, and the `olderArchitectDoc` literal in
  `internal/harness/install_test.go` (a byte-exact record of a shipped
  definition) are exempt. Live docs (`README.md`, `CLAUDE.md`,
  `docs/design.md`, `CONTRIBUTING.md`, `dist/relevo.service`, `.github/`) are
  not.
- **D10 — the tool ships in the tree.** `scripts/rename-mastermind.sh`, in
  the shape of `scripts/rename-relevo.sh`: refuses a dirty tree, refuses when
  already run, moves paths, applies the token sweep with the protect/restore
  manifest, prints the residue. Kept as the record of the rename.

Behaviour is unchanged: routing, state machine, storage formats and protocols
are untouched except where D3 and D6 rename a name.

## 2. File Structure

Moves (`git mv`, in the script):

```
internal/planner/                  -> internal/mastermind/            (package, all files)
internal/planner/planner.go        -> internal/mastermind/mastermind.go
internal/planner/planner_test.go   -> internal/mastermind/mastermind_test.go
cmd/relevo/planner.go              -> cmd/relevo/mastermind.go
cmd/relevo/planner_test.go         -> cmd/relevo/mastermind_test.go
internal/doctor/planner.go         -> internal/doctor/mastermind.go
internal/doctor/planner_test.go    -> internal/doctor/mastermind_test.go
internal/db/planner_test.go        -> internal/db/mastermind_test.go
internal/relevo/statusline_planner_test.go -> internal/relevo/statusline_mastermind_test.go
cmd/relevo/testdata/contract/planner-list.golden  -> mastermind-list.golden
cmd/relevo/testdata/contract/history-planner.golden -> history-mastermind.golden
internal/classify/testdata/injection/negative/06-question-to-planner.md -> 06-question-to-mastermind.md
```

New files:

```
scripts/rename-mastermind.sh                  the one-shot mechanical rename (§4.4, D10)
internal/db/migrations/008_mastermind.sql     table, column, indexes, kv prefix (§3.1)
internal/db/mastermind_test.go                the migration case (moved test + new test)
internal/mastermind/*_test.go                 new: ids, env fallback, hook output
cmd/relevo/mastermind_test.go                 new: the removed verb, the unknown flags
```

Edited by area (the sweep does most of it; the hand edits are named per step):

```
internal/store/binding.go        Go fields renamed, JSON tags kept (D2, §3.3)
internal/store/log.go            DirToMasterMind, value "to_planner" kept
internal/store/paths.go          MasterMindsDir, path "planners" kept
internal/relevo/*.go             Runtime.MasterMinds, resolveVerbMasterMind, GCOptions.MasterMindID/AllMasterMinds,
                                 mastermindRoute, MasterMindStatus, delivery.MasterMindDeliverer, origin texts
internal/delivery/origin.go      "from the MasterMind", "to MasterMind"
internal/mcp/{instructions,tools,verbs,server}.go  text and the RelevoVerbs field
internal/db/{lookup,write,read,stats,types}.go     MasterMind struct, mastermind_id, mastermind table
internal/doctor/*.go             MasterMindCheckInput, texts, row names, hook match (D8)
internal/view/{status,statusline,render,sort,waiting}.go  D6 keys and labels
internal/ui/*.go                 MasterMind labels, mastermindActions/mastermindSource/mastermindCell
cmd/relevo/{main,mastermind,bind,unbind,history,mcp,status,doctor,doctor_checks,wire}.go
internal/setup/{setup,setup_test}.go   skip: actor seeds (D7)
internal/harness/{harness.go,agents/*.md,agents/*.toml}  session prose; handoff.md stays byte-equal to
                                 the architect "Handing off" section (handoff_test)
claude-plugin/{.claude-plugin/plugin.json,commands/status.md,hooks/hooks.json}
.claude-plugin/marketplace.json, dist/relevo.service, .github/ISSUE_TEMPLATE/feature_request.yml,
CONTRIBUTING.md, README.md, CLAUDE.md, docs/design.md
internal/harness/opencodeplugin/tui.tsx, scripts/opencode-plugin-smoke.sh,
scripts/testdata/opencode-plugin/{relevo,status-1.json,status-2.json}
scripts/check-comments.allow     planner.go entries -> mastermind.go
testdata/coverage-baseline.txt   regenerated, not hand-edited
```

## 3. Data Structures & Type Definitions

### 3.1 Database (migration 008, the one rewrite)

`internal/db/migrations/008_mastermind.sql` (same Turso-safe dialect as 001–007;
008 is the second `ALTER TABLE` and relies on the version guard, like 007):

- `ALTER TABLE planner RENAME TO mastermind;`
- `ALTER TABLE binding RENAME COLUMN planner_id TO mastermind_id;`
- `DROP INDEX IF EXISTS planner_harness_session_uidx;` then
  `CREATE UNIQUE INDEX IF NOT EXISTS mastermind_harness_session_uidx ON mastermind(harness_kind, session_id);`
- `DROP INDEX IF EXISTS binding_planner_id_idx;` then
  `CREATE INDEX IF NOT EXISTS binding_mastermind_id_idx ON binding(mastermind_id);`
- `UPDATE kv SET key = 'mastermind/' || substr(key, 9) WHERE key LIKE 'planner/%';`
  (`'planner/'` is 8 chars; the registry key is `planner/<id>` — `internal/mastermind/registry.go`.)

Nothing else in the db moves: `owner_kind = 'planner'`, the ingest cursor key
`planner::<locator>`, the event directions and the mirror tables keep their
bytes (D2). `statsTables` (`internal/db/stats.go:9`) lists `"mastermind"` where
it names the table. `internal/db/testdata/schema.golden` is regenerated with
`go test ./internal/db -run Contract -update`; every changed line must be the
table, the column or one of the two indexes.

### 3.2 `store.Binding` (D2 — the precedent)

```
MasterMind      Endpoint `json:"planner"`              // why: bind.json keys are state already written
MasterMindID    string   `json:"planner_id,omitempty"` // why: ditto
MasterMindScreen   string   `json:"planner_screen,omitempty"`
MasterMindScreenAt time.Time `json:"planner_screen_at,omitempty"`
```

`internal/store/testdata/binding-shape.golden` must stay **byte-identical**:
it is the pin that these keys did not move. If it changes, the tags moved.

### 3.3 Identifiers renamed, values kept (the D2 table)

| where | Go identifier after | value after (kept) |
|---|---|---|
| `store/log.go:26` | `DirToMasterMind` | `"to_planner"` |
| `availability/available.go:18` | `ClearedByMasterMind` | `"planner"` |
| `availability/gates.go:168`, `ledger.go:86` | — | `"planner"` |
| `db/types.go:239` | `OwnerMasterMind` | `"planner"` |
| `delivery/channel.go:29` | `MasterMind string` | `json:"planner"` |
| `relevo/daemon.go:36` | `mastermindPrunedAtKey` | `"planner.pruned_at"` |
| `ingest/ingest_tx.go:296` | — | `"planner::"` |
| `store/paths.go:288-293` | `MasterMindsDir`, `AgyCredsDir` | `<root>/planners`, `<root>/planners/.agy` |
| `mastermind/registry.go:19` | `mastermindKeyPrefix` | `"mastermind/"` (migrated, D3) |
| `mastermind/planner.go` `NewID` | — | mints `"mm_" + 12 chars` |
| `mastermind/planner.go` `ValidID` | — | accepts `mm_…`, `pl_…`, ULID |

Each row gets a one-line why comment naming "state already written" (or the
one-time migration for the kv prefix).

### 3.4 Live JSON keys renamed (D6)

- `internal/view/status.go:54-74`: `MasterMindID json:"mastermind_id,omitempty"`,
  `MasterMindName json:"mastermind_name,omitempty"`,
  `MasterMindChatLabel json:"mastermind_chat_label,omitempty"`,
  `MasterMindChatLink json:"mastermind_chat_link,omitempty"`,
  `MasterMindKind json:"mastermind_kind"`,
  `MasterMindRoute json:"mastermind_route"`,
  `MasterMindRouteLive json:"mastermind_route_live"`.
- `internal/view/statusline.go:324-368`: `StatusLineMasterMind`, doc key
  `json:"mastermind"`.

`internal/view/status.go:26-74` doc comments come with them.

## 4. Interface Definitions & Component Contracts

### 4.1 CLI

- `relevo mastermind init [--name N] [--kind K --session S] [--hook claude]`,
  `list [--json]`, `rename <id|name> <new-name>`, `forget <id|name>`,
  `prune` (still removed, text says "the daemon prunes dead MasterMinds hourly").
- `init` prints `MasterMind <name> (<id>) <result>` and
  `export RELEVO_MASTERMIND=<id>` (§4.2 is what the opencode plugin parses).
- `relevo planner …` → `removedVerbs["planner"] = "relevo mastermind"` at
  `cmd/relevo/main.go:223-245`; the `case "planner"` at 200-201 goes. Exit 2,
  the map's message byte-for-byte.
- `--mastermind` replaces `--planner` at `cmd/relevo/bind.go:107`,
  `cmd/relevo/unbind.go:25`, `cmd/relevo/mcp.go:38`, `cmd/relevo/history.go:67`;
  `--all-masterminds` replaces `--all-planners` at `cmd/relevo/unbind.go:26`.
  The old flags are unknown flags: `parseFlags` returns the flag package's
  `flag provided but not defined: -planner`, exactly as `--tab` does today
  (`cmd/relevo/main_test.go:1026-1035`). A test pins it.
- Usage text (`cmd/relevo/main.go:43-73`) and every error/help string follow D1.

### 4.2 Env and the hook

- `mastermind.EnvLine(id)` → `export RELEVO_MASTERMIND=<id>\n`.
- `hookContext` (`internal/mastermind/hook.go:69-73`) →
  `You are relevo MasterMind %s (%s). RELEVO_MASTERMIND is set in your shell; pass --mastermind %s only to act as another MasterMind.`
- `noEnvNote` (`hook.go:75`) → same sentence with `RELEVO_MASTERMIND`.
- `Resolve` (`internal/mastermind/resolve.go:72`): reads
  `RELEVO_MASTERMIND`, then `RELEVO_PLANNER` (why comment: the export line in
  a live session's `$CLAUDE_ENV_FILE` is state already written), then the host
  and session steps unchanged.
- `cmd/relevo/main_test.go:120-123`: unset both `RELEVO_MASTERMIND` and
  `RELEVO_PLANNER`.

### 4.3 Text surfaces

- Delivery origins (`internal/delivery/origin.go:14-19`):
  `from the MasterMind (not the human)`, `to MasterMind · about runner %q`.
- MCP instructions (`internal/mcp/instructions.go:24,29,71,77`) and tool text
  (`internal/mcp/tools.go:95,98`): "this MasterMind". `RelevoVerbs.Planner`
  (`internal/mcp/verbs.go:23`) → `MasterMind`.
- Doctor (`internal/doctor/mastermind.go`): row names `"MasterMind"` /
  `"MasterMinds"`, details per D1, `pluginHookInitCommand = "mastermind init"`,
  `hookRunsMasterMindInit` matches a SessionStart command running
  `relevo mastermind init` (D8).
- View/UI labels: `view.RenderMasterMindLine` renders `MasterMind <name>`
  (`statusline.go:143-152`); `writeMasterMindLine` renders
  `  MasterMind  <name>  <kind>  route <route>` (`render.go:117-124`); the
  cockpit's cell and confirm lines say `MasterMind …`
  (`internal/ui/view_fleet.go:539,687,689`, `view_round.go:235,266`,
  `confirm.go:250-283`).
- `statuslineJSON` consumer: `internal/harness/opencodeplugin/tui.tsx` reads
  `currentDoc.mastermind`, invokes `["mastermind","init",…]`, matches
  `export RELEVO_MASTERMIND=…` and
  `/MasterMind\s+([^\s(]+)\s+\(((?:mm|pl)_[a-z0-9]+)\)/`, passes
  `--mastermind`, and says `Tell the MasterMind…` / `sent to the MasterMind` /
  `relevo: not a MasterMind (see relevo doctor)`.

### 4.4 `scripts/rename-mastermind.sh` (D10)

Usage: `sh scripts/rename-mastermind.sh`, from the repo root, on a clean tree.
Interface: refuses with exit 1 when `internal/planner` is gone or the tree is
dirty; performs §2's moves; applies §5's sweep; prints the residue (every
remaining `planner`/`Planner` line, path:line:text) to stdout; exits 0 when the
residue equals the protect manifest of §3.3/§4.3 and 1 otherwise. `set -eu`,
shellcheck-clean (`make check-scripts`).

## 5. High-Level Pseudocode

```
rename-mastermind.sh:
    guard: internal/planner exists?  tree clean?
    for each move in §2: git mv
    sweep: every tracked text file except
        docs/plans/** docs/specs/** docs/superpowers/** go.sum internal/legacy/**
        scripts/rename-relevo.sh scripts/check-name*.sh scripts/rename-mastermind.sh
        internal/harness/install_test.go internal/harness/agents/shipped.sha256
        **/*.golden
      per file, in order:
        s/RELEVO_PLANNER/RELEVO_MASTERMIND/g
        s/--all-planners/--all-masterminds/g
        s/--planner/--mastermind/g
        s{internal/planner}{internal/mastermind}g
        s/Planner/MasterMind/g          # no word boundary: catches testPlannerName too
        s/planner/mastermind/g          # no word boundary: to_planner and lite-planner are
                                        # put back by the restore pass below; pl_ has no
                                        # "planner" in it and survives
    restore manifest (the stored spellings, and the actor name):
        global:  to_mastermind->to_planner, lite-mastermind->lite-planner,
                 mastermind.pruned_at->planner.pruned_at, mastermind::->planner::
        files:   internal/availability/*.go      "mastermind" -> "planner"
                 internal/db/types.go            OwnerMasterMind value
                 internal/delivery/channel.go    json:"mastermind" -> "planner"
                 internal/store/binding.go       json:"mastermind*" -> "planner*"
                 internal/store/paths.go         "masterminds" -> "planners"
                 internal/store/store_test.go    "mastermind":{ -> "planner":{ in fixtures
                 internal/relevo/policy_view_test.go, internal/relevo/unused_gates_test.go,
                 internal/ui/view_candidates_test.go    Source "mastermind" -> "planner"
    print residue: git grep -nE '(planner|Planner)' outside the exempt paths

Resolve(reg, in):                         # unchanged order, new spelling + fallback
    flag -> env RELEVO_MASTERMIND -> env RELEVO_PLANNER -> host -> session

initialize DB:
    open -> applyMigrations -> 008 in one transaction:
        planner->mastermind, planner_id->mastermind_id, two indexes, kv key rewrite
```

## 6. Error Handling Strategy

- No new error types. `ErrNoPlanner` becomes `ErrNoMasterMind` with the text
  `no relevo MasterMind for this session: is the relevo plugin enabled (relevo doctor)? Or run relevo mastermind init`;
  `ErrUnknownPlanner` → `ErrUnknownMasterMind`; `ErrNoPlannerSession` →
  `ErrNoMasterMindSession` (`internal/relevo/bind.go:27-30`), text updated.
- The removed verb exits 2 with the map's message; a removed flag is the flag
  package's `flag provided but not defined`, exit 1, exactly as every other
  removed flag spelling. Nothing else changes.
- The migration is one transaction (008's guard): a failure rolls the whole
  schema move back and `open` fails; the second open is a no-op.
- Halt and report, do not improvise, if: a test pins an old spelling outside
  the §3.3 manifest; `internal/store/testdata/binding-shape.golden` changes
  (the bind.json keys moved); or a writer payload, a delivery origin or a
  wire direction byte changes that the plan did not list.

## 7. Working Efficiently

- The whole mechanical rename is **one step**: write the script, run it, read
  its residue report once. Do not hand-edit 300 files.
- Read once, from these locations, in one parallel batch before editing:
  `internal/store/binding.go` 94-120, 210-225; `internal/db/write.go` 100-230;
  `internal/db/lookup.go`; `internal/db/read.go` 70-80, 190-225;
  `internal/mastermind/planner.go` (after the move); `internal/relevo/bind.go`
  27-90; `cmd/relevo/main.go` 30-80, 195-245; `cmd/relevo/doctor_checks.go`
  236-300; `internal/doctor/mastermind.go` 15-30, 100-130, 285-315;
  `internal/harness/opencodeplugin/tui.tsx` 130-200, 400-460, 550-700;
  `scripts/testdata/opencode-plugin/*`.
- One edit call per file; the residue manifest is the work list.
- Focused loop, fix every error before the next run:
  `go build ./... && go test ./internal/mastermind/ ./internal/db/ ./internal/store/ ./cmd/relevo/ -count=1`.
- Goldens: after each surface's code lands,
  `go test ./cmd/relevo -run Contract -update`,
  `go test ./internal/mcp -run Contract -update`,
  `go test ./internal/db -run Contract -update`,
  `go test ./internal/serve -run Contract -update`;
  then read `git diff --stat` + `git diff` on the goldens and confirm every
  changed line is the rename. `internal/store/testdata/binding-shape.golden`
  and `internal/store/testdata/*` must not appear in the diff.
- Full check once, at the end: `make check`, then `gofmt -l .` (prints
  nothing), then `make e2e`.
  `make check` fails on the coverage baseline first: after all code changes,
  run `go test -race -count=1 -cover ./... > .coverage.txt &&
  sh scripts/check-coverage.sh --write && make check`. **Say in the report
  that the baseline was regenerated** because `internal/planner` became
  `internal/mastermind`.
- CI has no harness and no network: no test may spawn one. The e2e tests use
  the fake harness (`internal/e2e/fakes_test.go`).

## 8. Ordered Implementation Steps

1. **`scripts/rename-mastermind.sh` (§4.4) and run it.** Deliverable: §2's
   moves + the sweep + the restore manifest + the residue report.
   Verify: `git status --porcelain` shows only renames and edits; the residue
   equals §3.3/§4.3's protected sites. Nothing else in the tree matches
   `planner` except those, `docs/plans/**`, `docs/specs/**`,
   `internal/legacy/**` and `*.golden`.
2. **Compile and fix the residue the sweep got wrong.** `go build ./...`;
   `go vet ./...`; `go test ./internal/mastermind/ ./internal/db/ ./cmd/relevo/ -count=1`
   and fix every reported error per D1. Verify: the focused command is green
   up to the tests the later steps retarget.
3. **Identity and env (§4.2, D4, D5).** `internal/mastermind/planner.go`:
   `idRe`/`legacyPlRe`/`NewID`/`ValidID`/error texts; resolve.go's fallback;
   `hook.go`'s `hookContext`, `noEnvNote`, `EnvLine`; `cmd/relevo/mastermind.go`'s
   init prints. New tests in `internal/mastermind/mastermind_test.go`:
   `TestNewIDMintsMm`, `TestValidIDAcceptsLegacyPlAndULID`,
   `TestResolveFallsBackToLegacyEnvVar`, `TestEnvLineNamesMasterMind`.
   Verify: `go test ./internal/mastermind/ -count=1`.
   Mutation-check the fallback: delete it, confirm the fallback test fails,
   restore.
4. **Database (D3, §3.1).** Add `008_mastermind.sql`; rename `db.Planner` →
   `db.MasterMind` and `Binding.PlannerID` → `MasterMindID` with their SQL;
   `statsTables`. Extend the migration test in `internal/db/mastermind_test.go`
   (pattern: `internal/db/oldschema_test.go`) with a schema-7 DB holding one
   `planner` row, one `binding` with `planner_id`, and one kv row
   `planner/<id>`: after open, the same data is under `mastermind`,
   `mastermind_id` and `mastermind/<id>`; a second open is a no-op. Regenerate
   `internal/db/testdata/schema.golden`.
   Verify: `go test ./internal/db/ -count=1`; the golden diff is exactly the
   table, the column and the two indexes. Mutation-check: drop the kv UPDATE,
   confirm the test fails, restore.
5. **Store-level compat (D2, §3.2/§3.3).** Confirm the sweep+restore left:
   the four `Binding` JSON tags, `DirToMasterMind`'s value, `OwnerMasterMind`'s
   value, the claim tag, `mastermindPrunedAtKey`'s value, the ingest cursor
   prefix, `MasterMindsDir`'s path, `ClearedByMasterMind`'s value. Add the why
   comments. Verify: `go test ./internal/store/ ./internal/delivery/ ./internal/ingest/ ./internal/availability/ -count=1`
   and `git diff internal/store/testdata/` is empty.
6. **CLI surface (D5, §4.1).** `cmd/relevo/main.go` (case, `removedVerbs`,
   usage), `mastermind.go`, the four flags, all help/error strings. Tests in
   `cmd/relevo/mastermind_test.go`: `TestPlannerVerbIsRemoved` (exit 2, stderr
   has `"planner" was removed; use relevo mastermind`),
   `TestRemovedPlannerFlagsAreUnknown` (bind `--planner`, unbind
   `--all-planners` → `flag provided but not defined`, like
   `main_test.go:1096`). Verify:
   `go test ./cmd/relevo/ -run 'MasterMind|Removed|Unknown|Init|List|Rename|Forget' -count=1`
   and `go run ./cmd/relevo planner list; echo $?` → 2.
   Mutation-check the removed verb: re-add `case "planner"`, confirm the test
   fails, restore.
7. **Text surfaces (D1, §4.3), goldens.** MCP, doctor, view, ui, delivery
   origins; regenerate the `cmd/relevo`, `internal/mcp`, `internal/serve`
   goldens and read the diffs. Verify:
   `go test ./internal/mcp/ ./internal/doctor/ ./internal/view/ ./internal/ui/ ./internal/serve/ -count=1`
   and `git diff cmd/relevo/testdata/contract/` contains only the rename.
8. **Plugins (D8, §4.3).** `claude-plugin/hooks/hooks.json` →
   `relevo mastermind init --hook claude`; plugin.json/marketplace
   description; `claude-plugin/commands/status.md`; `tui.tsx` as §4.3;
   `scripts/opencode-plugin-smoke.sh` and its fixtures emit/expect
   `mastermind`, `mm_smoke000001`, `RELEVO_MASTERMIND`, `"mastermind"`.
   Verify: `sh scripts/opencode-plugin-smoke.sh` (if it runs locally without
   network), `sh scripts/check-plugin-version.sh`, and
   `go test ./internal/doctor/ -count=1`.
9. **Shipped definitions (§2, D9).** Update `architect.{agy,claude,opencode}.md`,
   `architect.codex.toml`, `reviewer.*`, `researcher.*` prose and
   `internal/mastermind/handoff.md` (which stays byte-equal to the architect
   "Handing off" section — `handoff_test.go` pins it), and
   `internal/harness/harness_test.go:199`'s `RELEVO_PLANNER` expectation.
   Run `sh scripts/agents-shipped.sh --write` and confirm `--check` passes.
   Verify: `go test ./internal/harness/ ./internal/mastermind/ -count=1`.
10. **Docs and lists (§2).** README (the actor examples at lines ~159-171,
    377, 1828-1834 keep `planner`/`lite-planner`), CLAUDE.md, docs/design.md,
    CONTRIBUTING.md, dist/relevo.service, `.github`, and
    `scripts/check-comments.allow`'s two entries.
    Verify: `sh scripts/check-comments.sh`; `git grep -n planner README.md` →
    only actor lines.
11. **Full check, baseline, e2e, commit.** `go test -race -count=1 -cover ./... > .coverage.txt`,
    `sh scripts/check-coverage.sh --write`, `make check`, `gofmt -l .`,
    `make e2e`. Copy this plan to
    `docs/plans/2026-09-27-mastermind-rename.md`, one
    commit with the code, tests, goldens, baseline and plan.
    Verify: `make check` green; `git diff --stat HEAD~1` lists only §2's files
    plus the plan; the baseline diff is the `internal/planner` →
    `internal/mastermind` line.

### Closed list of what is deleted (everything not on this list survives)

1. `cmd/relevo/main.go:200-201` — `case "planner"` / `cmdPlanner` dispatch,
   replaced by `removedVerbs["planner"]`.
2. The four `--planner`/`--all-planners` flag registrations
   (`bind.go:107`, `unbind.go:25-26`, `mcp.go:38`, `history.go:67`),
   replaced by the mastermind spellings.
3. `RELEVO_PLANNER` as the *written* env name (`EnvLine`, `hookContext`,
   `noEnvNote`, plugin env), replaced by `RELEVO_MASTERMIND`. The *read* stays
   (§4.2).
4. `hookRunsPlannerInit`'s match on `relevo planner init`, replaced by the new
   spelling (D8).

No test is deleted. Every test that asserted one of these is retargeted to the
new behaviour and keeps every other assertion it had.

### Report must include

- `git diff --stat` and the residue report from step 1.
- The mutation checks of steps 3, 4 and 6, with the failing test name.
- The goldens whose bytes changed, one line each.
- `make check` and `make e2e` results; the coverage-baseline regeneration said
  explicitly (`internal/planner` → `internal/mastermind`).
- Any §2 file that did not need a change, and anything left.

