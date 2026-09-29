# Codebase cleanup and refactor -- design

Date: 2026-09-25. Status: approved; phase 0 and phase 1 started 2026-09-26.
The cockpit's internal split (§5) is still open and is decided before phase 2.

## 1. Goal

Make relevo small enough, and plain enough, that its owner can read every line
of it and understand how it works. The review happens on the finished
codebase, not on the refactor's PRs, so each PR only has to leave `main` green
and closer to the end state.

Success means:

- Every non-test `.go` file passes the lint rules in §4 with no exclusions.
- Comments explain *why* and nothing else (§4.1). No history in the code.
- `internal/relevo` is the orchestration core only (§5), about 8-9k lines
  instead of 21.8k; `cmd/relevo` is flags and output only.
- Tests are no larger than the code they test (§6), without losing coverage.
- Every machine-read contract in §3 is byte-for-byte unchanged.

Starting point (2026-09-26, `main` at 0583bea, v0.14.0 plus #510): 88.9k
non-test lines, of which 15.9k are comment lines (about 2,200 cite issue
numbers or spec sections); 124.0k test lines; 41 functions over 70 lines, 86
over cognitive complexity 30; no golangci-lint config. (At 211117d, a day
earlier, it was 80.0k / 15.3k / 115.9k / 40 / 76: most of the growth is the
cockpit, `internal/ui` 16.3k lines plus `internal/ui/dash` 1.7k.)

## 2. Scope

Nothing is off limits: features, packages, tests and comments are all in
scope, subject to the keep list and the contracts.

Audiences (decided 2026-09-25): humans read relevo through the cockpit TUI
(and later a web GUI); the CLI is for AI agents and the plugins that drive
them. CLI output meant only for a person to read can therefore be dropped.
Redesigning the CLI for agents is #505 and comes after this refactor; this
refactor freezes the machine-read outputs (§3) instead of changing them.

### 2.1 Keep

- Every verb and flag not on the deletion list in §2.2, including codex
  (harness and candidates), `ask` (consults), remote builders (`serve`,
  enroll/keys, remote client), the cockpit (`relevo ui`, `relevo serve ui`),
  `doctor`, `config` (including its legacy roles view), `planner`, `gate`,
  `migrate`, `update`.
- The relay->relevo migration code (`relevo migrate`, `internal/migrate`,
  `internal/legacy`) and all old-state compatibility, until #411 removes them.
  The refactor reads the existing `relevo.db` and today's config documents; it
  adds no schema migration.
- Help text may be shortened; verb and flag names may not change.

### 2.2 Deletion list (closed)

Everything not on this list survives. A test that asserts surviving behaviour
through a deleted mechanism is ported, not deleted. Every removed test must
cite an item number here in its round's report.

1. **Pane-mode leftovers.** `store.ModePane` and `usage.ModePane` constants;
   `legacyPaneBinding` and `retireLegacyPane` (`internal/relevo/reconcile.go`);
   `ClaimStore.SweepPaneKeyed` / `KVClaims.SweepPaneKeyed`
   (`internal/relevo/channel.go`) and its call in `cmd/relevo/mcp.go`;
   `OrphanedPane` (`internal/relevo/candidate.go`); the "pane consults were
   removed" refusal in `internal/relevo/consult.go`. Stored rows whose
   `builder_mode` is `pane` (115 today, none live) still load: the string is
   read as an unknown, finished mode and shown as-is by history and archives.
   `internal/ingest` keeps writing the literal `"pane"` for archive imports.
2. **`relevo land`.** `cmdLand`, `landFailure` (`cmd/relevo/main.go`),
   `internal/relevo/land.go`, `LandText` (`internal/relevo/text.go`).
3. **`relevo review`.** `cmd/relevo/review.go`, `internal/relevo/review.go`.
4. **`relevo edge`.** `cmd/relevo/edge.go`, `internal/relevo/edges.go`,
   `evaluateEdges` and its call in `headless.go`, `armedFires` and its use in
   `daemon.go`, `store.Edge` and the binding's edge fields. Stored edge data in
   existing rows is ignored on load, not migrated.
5. **`relevo bind --from` (fork).** `runFork`, `splitFromSource`, the `--from`
   flag and its route in `bindRouteFor` (`cmd/relevo/main.go`),
   `internal/relevo/fork.go`, and fork references in `add.go`, `send.go`,
   `remote.go`. The `forked_from_*` columns stay in the schema, unwritten.
6. **`relevo history --tab`**, including the server's `--owner` / `--state`
   form: `historyTab`, `renderTabReport` (`cmd/relevo/history.go`), `serveTab`
   (`cmd/relevo/serve.go`), `serve.AdminTabEntries`, and `TabRows`,
   `RenderTab`, `TabEntries` (`internal/relevo/tab.go`). `ParseSince` survives
   (moved next to `histq.ParseSince`, its only real implementation).
7. **`relevo history --stats`**: `historyStats` and the text renderers in
   `internal/stats/render.go` (`Render` and every `render*`). `stats.Build`,
   `StatsInputs` and the formatters the cockpit uses survive.
8. **The text form of `relevo history`**: `FormatHistory`, `FormatGroups`,
   `HistoryLine` (`internal/relevo/history.go`). `relevo history` prints JSON
   only; `--json` stays accepted. The per-field filter flags that `-q`
   already expresses are removed; `--binding`, `--planner`, `--limit`, `-q`,
   `--here`, `--since` stay.
9. **The text form of `relevo serve status`**: `RenderAdminStatus` and the
   text branch of `cmdServeStatus`. `serve status` prints the JSON document;
   `--json` stays accepted.
10. **Dead code** reported by `golang.org/x/tools/cmd/deadcode ./cmd/relevo`
    after items 1-9 (about 60 functions today), except functions only tests
    or build-tagged code reach.
11. **Duplicates**, replaced by one implementation each: the cockpit's
    live-binding fetchers in `internal/ui/fetch.go` (`fetchPlan`,
    `fetchReport`, `fetchDiff`, `fetchLog`) give way to the shared `show`
    path; `dash.shortTokens` and `stats.ShortTokens` give way to
    `usage.ShortTokens`; `statsCountPairs` (`internal/ui/view_stats.go`) gives
    way to an exported `stats.CountPairs`; the `relevo-exit:` /
    `relevo-rusage:` literals duplicated in `store/seal.go` and
    `relevo/runner.go` move to `internal/exec`.

## 3. Contracts (must not change)

Phase 0 pins each of these with a golden or equivalence test. A change to a
golden file needs the owner's approval, recorded in the round's plan.

- C1 `relevo status --json` (and `--all`, `--name`).
- C2 `relevo status --line` and `--line --json` (`StatusLineDoc`): read by the
  Claude Code status line and the OpenCode plugin (`tui.tsx`).
- C3 `relevo show --json` for every artifact kind (plan, report, diff, drift,
  gate, findings, log, transcript), plus `--log --after N --json`.
- C4 `relevo history --json` with `--binding`, `--planner`, `--limit`, `-q`:
  read by the OpenCode plugin.
- C5 `relevo serve status --json`: parsed by
  `servers/contabo/burst/internal/observe/observe.go`.
- C6 `relevo planner list --json`, `relevo config log --json`, and every other
  `--json` output that survives.
- C7 The MCP tools (`status`, `send`, `done`) and their result text, and the
  MCP instructions' command lines.
- C8 The report payload text, including its `relevo show` line
  (`PushText`), and `relevo wait`'s exit codes (0, 2, 3, 4, 5, 124).
- C9 The remote wire protocol between a client and `relevo serve`: request
  and response bodies, headers, signatures. Client and server deploy
  separately (contabo), so both old-client/new-server and new-client/
  old-server must keep working.
- C10 The database: schema, `schema_version`, KV keys, and the meaning of
  every stored value. The refactor reads what exists and writes what it
  writes today, minus the deleted features' fields.
- C11 The config document format (`config export` / `import`), roles,
  candidates and policy documents.
- C12 The shipped agent definitions (`internal/harness/agents/*`) and plugin
  commands: every command line they name must still work.

## 4. Readability rules

These go into `CLAUDE.md` (Conventions) so every builder round follows them.

### 4.1 Comments

1. Every package has a 1-3 line package comment saying what it owns.
2. A comment says *why*, and only where the code cannot: a non-obvious
   constraint, where a number comes from, an ordering that matters, a hazard.
3. No history: no issue or PR numbers, no spec sections (`§`), no "round N",
   "used to", "pre-#NNN", "since 0.13". Git and the issues hold history.
4. No restating the code. A doc comment on an exported name is optional and
   is written only when it says something the name and signature do not.
5. Test files follow the same rules; a test's name says what it pins.

### 4.2 Code

The dexpace Go styleguide applies (`dexpace-styleguide:go-styleguide`), with
two stated exceptions: no "two assertions per function" rule (assert at
boundaries), and no mandatory doc comment on exported names (§4.1.4).

Concretely: functions at most 70 lines (aim 20-40); files around 500 lines at
most; one package per concept; dependencies passed explicitly; errors wrapped
once with `%w`; no `_ = err`.

### 4.3 Enforcement

`make check` gains:

- `golangci-lint run` with `.golangci.yml`: `funlen` (70 lines, comments
  excluded), `gocognit` (30), `errcheck`, `errorlint`, `unused`, `govet`,
  `staticcheck`, `ineffassign`; `revive`'s `exported` rule and `godot` off.
- `scripts/check-comments.sh`: fails on `#[0-9]+` or `§` inside a `//`
  comment in a tracked `.go` file.
- `scripts/check-filesize.sh`: fails on a non-test `.go` file over 600 lines
  (the 500 target plus slack for one long table).
- `scripts/check-coverage.sh` (§6.2).

During the refactor, packages not yet finished are listed in
`.golangci.yml` exclusions and in the scripts' allow-lists. Phase 3 removes
one package's entries per round. At the end there are no exclusions.

`golangci-lint` must be installed in CI (the `check` jobs); pin its version
in the workflow. CI has no network at test time, so the linter is installed
in a setup step, not fetched by `make check`.

## 5. Target package layout

The principle: one package owns one concept, and dependencies point one way.
The clusters inside `internal/relevo` call back into each other (switching
reaches into send and the headless supervisor; delivery reaches back into
status and bind), so the split pulls out the leaves and leaves a smaller
orchestration core rather than making ten peers.

```
cmd/relevo/
  main.go          dispatch and usage only (~200 lines)
  wire.go          the one place dependencies are composed
  <verb>.go        one file per verb: flag parsing, calling internal/, output

internal/
  exec/            NEW. Runner, ProcSpec, ScopeSpec, the exit/rusage trailer
                   format. Below proc and relevo; ends the duplicated literals.
  capture/         from relevo: capture.go, drift.go, scratch.go
  availability/    ledger + history + latency packages, and from relevo:
                   ledger.go, limit.go, probe.go, available.go, tier.go
  delivery/        from relevo: deliver*.go, deliverer.go, channel*.go,
                   drain.go, giveuplog.go, agy_creds.go, opencode_session.go
  view/            from relevo: status.go, statusline.go, show.go, text.go,
                   sort.go, policy_view.go, candidates_list.go,
                   actors_list.go, roles_list.go; printDiff/printLog from cmd
  consult/         from relevo: ask.go, consult.go, verify.go
  relevo/          what remains: bindings (bind, add, gc, refclean), the round
                   lifecycle (send, queue, wait, stop, reconcile, report tail,
                   injection, escape), headless supervision, candidate
                   resolution and switching, the check-command gate (renamed
                   check.go / check_repair.go), the daemon
  roles/           roles + actors (actors is an input format for roles)
  config/          gains relevo/configedit.go (the cockpit's structured add,
                   edit and delete of candidates, actors and agents: a pure
                   transform over the config document); ReloadConfig, which
                   takes the runtime, stays in relevo
  harness/         gains relevo/installenv.go (the InstallEnv agent
                   definitions are read and written through)
  remote/          client side; relevo/remote.go and pull.go, authgrace.go
                   move here, split by verb (add, send, catch-up, observe)
  serve/           server side; relevo/served.go moves here
  ui/, ui/dash/     the cockpit, now the second-largest package; its internal
                   split (views, forms, fetching, shell) is decided when this
                   spec is revisited, after the cockpit work in flight lands
  (unchanged, trimmed) store db git proc hooks pick mcp
                   doctor config candidate policy planner ingest transcript
                   classify histq stats usage chatlabel patch release setup
                   upgrade migrate legacy agentsrc jsonshape
```

Rules that come with the layout:

- `Runtime` is dissolved. Each extracted package declares a small dependency
  struct with only the fields it uses (for example `capture.Deps{Git, Now}`);
  `relevo` keeps a trimmed `Deps` for the core. `cmd/relevo/wire.go` builds
  all of them once. Functions take a pointer to their package's deps struct,
  not a 40-field value.
- Logic in `cmd/relevo` moves to `internal/`: daemon startup (lock, DB open
  and newer-schema check, dedupe, daemon.json, upgrade watcher, reaper
  wiring), `bindRouteFor`, gate resolution and forwarding, GC planner
  scoping, printDiff's round selection, doctor's input assembly.
- `config` stops importing `remote/client`.
- "gate" means a provider gate only; the check-command gate is "check".
- Only `migrate`, `legacy` and the old-state loaders import `legacy`.
- Not merged: `store` and `db` (a clean facade over the record), `ingest`,
  `transcript` and `classify` (different concerns), `histq`, `stats`,
  `usage`.

## 6. Tests

### 6.1 Rules

1. Test through the package's API. When code moves package, its tests move
   with it and keep their assertions.
2. One table per behaviour: near-identical tests collapse into a
   table-driven test whose row names say what each row pins.
3. One `helpers_test.go` per package holds its fakes and fixture builders
   (fake runner, fake remote server, store with N bindings); tests do not
   build their world by hand.
4. Deleted features take their tests, and only theirs (§2.2).
5. Comment rules as §4.1.
6. `cmd/relevo` tests never spawn a harness or reach the network, and the
   package's TestMain isolation stays (CLAUDE.md).

### 6.2 Coverage guard

The round that closes phase 1 writes `testdata/coverage-baseline.txt`:
per-package statement coverage from `go test -coverprofile` on `main` after
the deletions (a baseline taken before them would only be rewritten). `scripts/check-coverage.sh`
fails when a surviving package's coverage drops more than 1 point below its
baseline. When code moves package, the baseline entry moves with it (the
round that moves it updates the file and says so). Deleted packages leave
the file.

Coverage cannot see a consolidation that keeps lines covered but stops
asserting on them, so each consolidation round also mutation-tests: break a
condition the consolidated table covers and confirm a named row fails.

Target: test lines at or below non-test lines, about 50-60k. The number is a
result, not a quota; no test is cut to reach it.

`make e2e` runs at the end of every phase.

## 7. Phases

Each phase is one or more builder rounds; each round is one PR, green on
`make check` and `make e2e`, and saves its plan to `docs/plans/` (plans ship
with their implementation). Nothing else is in flight on relevo while a phase
runs: the layout moves touch most files, so parallel work would conflict.

- **Phase 0 -- pin.** Golden tests for C1-C12; `.golangci.yml` with every
  package excluded; `check-comments.sh` and `check-filesize.sh` with every
  file allow-listed; golangci-lint in CI; §4 rules into `CLAUDE.md`. No
  production code changes. Three parallel rounds: CLI/MCP goldens, wire
  goldens, tooling.
- **Phase 1 -- delete.** Items 1-9 of §2.2 in three parallel rounds (pane +
  edge; land + review + fork; the history and serve-status reports), run
  alongside phase 0. Then one closing round: item 10 (dead code), the
  coverage baseline and `check-coverage.sh`.
- **Phase 2 -- restructure.** 2a extract `exec`, then the leaves (`capture`,
  `availability`, `delivery`, `view`, `consult`), merge `actors` into
  `roles`, move remote and served code, dissolve `Runtime`; 2b split
  `cmd/relevo` into per-verb files and `wire.go`, moving logic down; 2c the
  duplicates (§2.2 item 11). Moves and renames are scripted (gopls rename /
  `gofmt -r` / a move tool plus manifest), then one pass fixes the build.
  Phase 2 does not rewrite function bodies or comments beyond what a move
  needs.
- **Phase 3 -- finish, one package per round, leaves first.** For each
  package: comments to §4.1, functions to §4.2, tests to §6.1, then remove
  its exclusions so lint and the scripts enforce it. Order follows the import
  graph from leaves up (for example `exec`, `jsonshape`, `legacy`, `db`,
  `store`, ..., `relevo`, `cmd/relevo`); large packages (`relevo`, `ui`,
  `serve`, `store`) take several rounds.
- **Phase 4 -- close.** No exclusions remain; `CLAUDE.md` and the README
  reflect the new layout; final measurements against §1.

Rough size: 15-25 rounds.

## 8. Risks

- **Remote compatibility (C9).** contabo runs a separately installed binary.
  Phase 0's wire goldens and a mixed-version check before each deploy
  (`deploying-to-contabo`) guard it.
- **Behaviour drift in moves.** Phase 2 is mechanical by rule; any logic
  change found necessary during a move halts the round.
- **Coverage-neutral test loss.** Covered by §6.2 mutation checks.
- **Long-lived conflicts.** No other relevo work runs during a phase.
- **Lint noise.** A rule that fights readable code (for example `gocognit` on
  a flat switch) is tuned in `.golangci.yml` with a one-line reason, not
  suppressed per line.

## 9. Out of scope

- Removing the relay->relevo migration and old-state compatibility (#411).
- Behaviour changes, new features and bug fixes, except where a bug blocks a
  move; such a bug is reported, not fixed silently.
- Schema changes.
