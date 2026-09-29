# Cleanup P0a -- pin the CLI and MCP contracts with golden tests

Round of the codebase cleanup (spec: `docs/specs/2026-09-25-codebase-cleanup-design.md`,
§3 contracts C1-C4, C6-C8, C10-C12). **Test-only round: no production code changes.**

## Why this is a second attempt

A first attempt at this round (on older code) edited production files --
`internal/relevo/status.go`, `internal/relevo/show.go`, `internal/mcp/tools.go`
-- to make its goldens pass. That inverts the purpose of a golden test and the
work was discarded. The rules below are absolute:

- **You may not modify, create or delete any non-test `.go` file.** Only
  `*_test.go` files and files under `testdata/` (plus the plan copy in
  `docs/plans/`). Before committing, run
  `git diff --name-only HEAD | grep -v -E '_test\.go$|/testdata/|^docs/plans/'`
  -- it must print nothing. If it prints anything, stop and report.
- **A golden records what the code does today.** Generate it with `-update`
  from the unmodified code. If an output looks wrong to you, it is still the
  contract: pin it as-is and list it in the report under "looks wrong" -- do not
  fix it.
- **Goldens must be machine-independent.** Anything derived from the host
  (`runtime.NumCPU`, hostnames, user names, absolute paths, the current time,
  the relevo version) is fixed by the fixture or normalised. Verify with
  `taskset -c 0 go test <pkg> -run Contract -count=1` (one CPU) in addition to
  a plain run; both must pass.

The deletion rounds have landed on `main` (this tree's base): `relevo land`,
`review`, `edge`, `bind --from`, `history --tab/--stats`, and the text forms of
`history` and `serve status` no longer exist. `relevo history` prints JSON
always. `serve status --json` and the wire protocol are pinned already
(`cmd/relevo/serve_contract_test.go`, which declares `wireNormalize`,
`wireAssertGolden` and the `-update-wire` flag -- do not redeclare those names).

## 0. Rules for this round

- If a step is impossible as written, or the code contradicts this plan, **stop and
  report** -- do not improvise, and never change production code to make a golden
  test possible. Pin at a lower level instead (see each step) and say so.
- Only files named in §2 may be created or changed.
- New comments follow the cleanup's comment rules: say *why* only where the code
  cannot; no issue numbers, no `§`, no history, no restating the code.
- `cmd/relevo` tests must not spawn a harness or reach the network. The package's
  TestMain already isolates HOME/XDG and unsets harness variables; a test that
  needs config writes it under a `t.TempDir()` it sets as `XDG_CONFIG_HOME`.
- `serve status --json` and the remote wire protocol are already pinned (C5, C9) -- skip them.

## 1. System overview

The cleanup will move and rewrite most of the code. Everything a machine reads
must stay byte-for-byte identical through it. This round snapshots those outputs
into golden files so any later round that changes one fails `make check`. Each
golden test regenerates its files with `go test ./<pkg> -run <Test> -update`.

## 2. File structure

```
cmd/relevo/contract_test.go            NEW  C1-C4, C6, C8 (wait exit codes), C11, C12 tests
cmd/relevo/testdata/contract/*.golden  NEW  one file per pinned output
internal/mcp/contract_test.go          NEW  C7
internal/mcp/testdata/contract/*.golden NEW
internal/relevo/contract_test.go       NEW  C8 (PushText payloads)
internal/relevo/testdata/contract/*.golden NEW
internal/db/contract_test.go           NEW  C10
internal/db/testdata/schema.golden     NEW
docs/plans/2026-09-26-cleanup-p0a-golden-cli.md  NEW  a copy of this plan (last step)
```

If a package already declares a package-level `update` flag in its tests, reuse
it instead of declaring a second one (declaring two panics at test start).
Existing: `internal/store/format_test.go`, `internal/planner/format_test.go`,
`internal/transcript/transcript_test.go` use `update`; `internal/ui`,
`internal/ui/dash`, `internal/agentsrc`, `internal/stats` use `updateGolden`.
None of the packages this round touches declares one today -- verify with grep.

## 3. Data structures

`goldenCase` (test-local, one per package that needs it):
- `name string` -- the golden file's base name, `[a-z0-9-]+`, unique in the package.
- `got []byte` -- the output under test, after normalisation.

`normalize(b []byte, roots ...string) []byte` (test-local helper, one per package):
replaces volatile substrings with fixed placeholders so goldens are deterministic:
- each path in `roots` (temp dirs) -> `<ROOT0>`, `<ROOT1>`, ... (longest first);
- RFC 3339 timestamps (with or without fractional seconds, `Z` or offset) -> `<TIME>`;
- 26-char Crockford ULIDs and relevo's `pl_` planner ids -> `<ID>` / `<PLANNER>`;
- relative ages such as `3m ago`, `12s` in human columns -> `<AGE>` (only where such
  a field exists in the output; do not blanket-replace numbers);
- the running binary's version string -> `<VERSION>`.
Anything else that differs between two runs is a bug in the fixture: fix the
fixture (fixed clock, fixed names), do not widen `normalize`.

`assertGolden(t, name string, got []byte)`: if `-update`, write
`testdata/contract/<name>.golden`; else compare and fail with a unified-looking
first-difference message naming the file and the `-update` command.

## 4. Contracts to pin (interface of this round)

| ID | Output | How to produce it | Golden files |
|---|---|---|---|
| C1 | `relevo status --json`, `--all --json`, `--name X --json` | `run([]string{...})` via `captureOutput` (cmd/relevo/main_test.go:15) over a seeded store: one active local binding at round 2 with a report pending, one DONE binding, one remote binding row if the store can hold one without a server | `status.golden`, `status-all.golden`, `status-name.golden` |
| C2 | `relevo status --line` and `--line --json` | same fixture, with `RELEVO_PLANNER` set via `t.Setenv` to the fixture planner | `statusline.golden`, `statusline-json.golden` |
| C3 | `relevo show <name> --json` for every section flag `show` accepts (read them from `cmd/relevo/show.go`'s FlagSet), and `show <name> --log --after 0 --json` | seed the files each section reads (plan, report, diff, drift, gate, findings, log, transcript). Reuse `seedShowDiffStore` (show_test.go:43), `seedLogStore` (log_test.go:18) and `seedReadVerbStore` (read_verbs_test.go:19) where they fit | `show-<section>.golden`, `show-log-after.golden` |
| C4 | `relevo history --json` with `--binding`, `--planner`, `--limit 1`, and `-q 'outcome:done'` | seed rounds the way `cmd/relevo/history_test.go` does | `history-binding.golden`, `history-planner.golden`, `history-limit.golden`, `history-q.golden` |
| C6 | every other surviving `--json` output: find them with `grep -n 'fs.Bool("json"' cmd/relevo/*.go` and drop the ones §0 excludes. At least `planner list --json`, `config log --json`, `config log --rev 1 --json` | `planner_test.go`'s `plannerRegistryAt` and `config_test.go`'s fixtures | `<verb>-<sub>.golden` |
| C8a | `relevo wait` exit codes 0, 2, 3, 4, 5, 124 | one table test: fixture state per outcome -> exit code and stdout. If `internal/mcp/waitcmd_test.go` or an existing cmd test already pins a code exactly, do not duplicate that row; list which existing test pins it in a comment above the table | `wait-<code>.golden` (stdout only) |
| C8b | `relevo.PushText` (internal/relevo/push.go:81) for each `store` log kind it expands and one it does not | direct call with a fake `read` func | `push-<kind>.golden` in internal/relevo |
| C7 | MCP: the `tools/list` result, the `initialize` result's instructions text, and the result text of `status`, `send` (with `dry_run: true`), `done` | drive the server the way `internal/mcp/server_test.go` does, over its fakes | `tools-list.golden`, `instructions.golden`, `tool-<name>.golden` |
| C10 | database schema | open a fresh db with `db.Open(tempPath)` (internal/db/db.go:66), dump `SELECT type, name, tbl_name, sql FROM sqlite_master ORDER BY type, name` and `SchemaVersions()` | `internal/db/testdata/schema.golden` |
| C11 | `relevo config export` | seed a config with every section non-empty using `config_test.go`'s helpers | `config-export.golden` |
| C12 | every `relevo <verb> ...` command line in `internal/harness/agents/*.md`, `claude-plugin/commands/*.md`, `internal/mcp/instructions.go` and `internal/relevo/push.go` | extract lines with a regexp, assert the verb is dispatched (reuse `commandVerbs`, cmd/relevo/*_test.go) and, where the verb's FlagSet is reachable from a test (for example `bindFlagSet`), that each `--flag` is defined | no golden; a failing case names the file and line |

Preconditions: every fixture lives under `t.TempDir()`; no test reads the real
`~/.local/state/relevo`. Postconditions: `go test ./cmd/relevo ./internal/mcp
./internal/relevo ./internal/db` passes twice in a row with no `-update`, and
`-count=2` passes (no order or time dependence).

## 5. Pseudocode

```
for each contract row:
    fixture := seed(t)                         // temp store, fixed clock where the API takes one
    out     := produce(fixture)                // run([...]) / direct call / MCP request
    assertGolden(t, name, normalize(out, fixture.roots...))
```

For C12:
```
for each file in the listed sources:
    for each line matching `relevo ([a-z-]+)((?: --?[a-z-]+)*)`:
        require verb in commandVerbs(t)
        if flagSetFor(verb) known: require every flag defined
```

## 6. Error handling

A fixture that cannot be built without a harness, the network, or a production
change: pin that output at the lowest pure function that produces it (for example
the renderer that `cmdStatus` calls) and list it in the report under "pinned
lower". If even that is impossible, list it under "not pinned" with the reason.
Do not skip silently.

## 7. Working efficiently

- Batch independent reads (the listed test helpers, show.go, history.go, mcp
  server_test.go, db.go) in one step. Everything this plan names is at the path
  and line given; do not re-search for it.
- Write each new test file in one edit.
- Iterate with focused commands, fixing every error before rerunning:
  `go test ./cmd/relevo -run 'Contract' -count=1`,
  `go test ./internal/mcp ./internal/relevo ./internal/db -run 'Contract' -count=1`;
  generate goldens with `-update`, then rerun without it.
- Run the full check once at the end: `make check`.

## 8. Ordered steps

1. **Helpers.** Add `normalize` and `assertGolden` at the top of
   `cmd/relevo/contract_test.go`. Done when the file compiles (`go vet ./cmd/relevo`).
2. **C1 + C2** (depends on 1). Seed the status fixture; add the five goldens.
   Done when `go test ./cmd/relevo -run Contract -count=2` passes without `-update`.
3. **C3** (1). All `show` sections. Same verification.
4. **C4 + C6** (1). History and the other JSON outputs. Same verification.
5. **C8a + C11 + C12** (1). Wait exit codes, config export, command-line references.
6. **C8b** in internal/relevo, **C7** in internal/mcp, **C10** in internal/db,
   each with its own small `normalize`/`assertGolden` (copy, do not export a
   shared helper -- phase 3 consolidates). Verify each package's Contract tests.
7. **Mutation check** (the one exception to the no-production-edit rule: each mutation is reverted with `git checkout -- <file>` before the next step, and the commit-time grep proves it). For three goldens (one each from C1, C3, C7), change one
   output byte in production code temporarily (for example a JSON field name),
   confirm the named test fails, and revert. Record the three in the report.
8. **Full check.** `make check` passes. Confirm `git diff --stat` shows only
   files from §2.
9. **Save the plan.** Copy this plan to
   `docs/plans/2026-09-26-cleanup-p0a-golden-cli.md` and commit it with the tests.

Report: the list of goldens written (file -> contract ID), anything "pinned
lower" or "not pinned" with reasons, and the three mutation checks.

## Round 3

# Cleanup P0a (round 3) -- pin a valid `history -q` query

Round 3 of `cl-golden-cli2`. Round 2 committed the CLI/MCP contract goldens
(`9ad02e2`). One of them pins nothing useful: `history-q.golden` is empty,
because the query the plan suggested (`-q 'outcome:done'`) is not a valid
outcome, so the command exits 2 with no stdout.

### Rules

- **Run every command in the foreground and wait for it**; never background a
  command or end your turn before the report and done marker exist (a headless
  builder's process exits when its turn ends).
- Only `cmd/relevo/contract_test.go`, `cmd/relevo/testdata/contract/history-q*.golden`
  and the plan copy may change. No production `.go` file.
- If a step is impossible as written, stop and report.

### Steps

1. In `TestContractHistory*` (cmd/relevo/contract_test.go), change the `-q`
   case to a valid query that matches at least one fixture round, for example
   `-q 'outcome:reported'` (valid outcomes: reported, halted, exited, switched,
   done_no_report, open -- pick one the fixture actually has). The golden must
   be non-empty JSON.
2. Keep the rejected-query behaviour pinned as a separate row
   `history-q-invalid`: `-q 'outcome:done'` -> exit code 2 and its stderr
   message (normalised), since agents do hit it.
3. `go test ./cmd/relevo -run Contract -update` for those rows only, then
   `go test ./cmd/relevo -run Contract -count=2` without `-update`, and
   `taskset -c 0 go test ./cmd/relevo -run Contract -count=1`.
4. `git diff --name-only HEAD | grep -v -E '_test\.go$|/testdata/|^docs/plans/'`
   prints nothing. `make check` passes (foreground).
5. Append a "Round 3" section with this plan to
   `docs/plans/2026-09-26-cleanup-p0a-golden-cli.md`; commit once.

Report: the query chosen and the two goldens' contents (first lines).
