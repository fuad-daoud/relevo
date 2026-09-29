# Plan: `relevo bugreport` (#709)

This adds a verb that builds a local, redacted bug-report bundle. It never sends anything on its own. The seed's seven decisions are final.

**The issue text was not read.** `gh issue view 709` and a `curl` of the issue both needed approval this round, so this plan comes from the seed's decisions plus the tree at `857a8fa2`. Four choices below are this plan's, not the issue's. Each is marked **(plan's choice)**. Before round A starts, the MasterMind should check them against #709. If one conflicts with the issue, the issue wins; send a corrected plan rather than letting a builder decide.

## Amendments (MasterMind, 2026-09-30, after checking #709)

- The issue's shape is `relevo bugreport [--name N] [--round N] [--logs] [--raw] [--out PATH] [--stdout|--json] [--gh]`. This plan now adds the missing `--round N` and `--out PATH`; their semantics are in Behaviour A, C.5 and D.
- The default file is dated under `<state root>/bugreports/`, not one overwritten `<state root>/bugreport.md`, so a report is never destroyed. `--out PATH` overrides the dated default.
- The other two plan's choices -- the title format and the section allow-lists -- match the issue and stand as written.
- Issue #709 sets no JSON key list; this plan's `--json` document shape is the contract.
- Nothing else in the plan changes; rounds A, B and C stand.

## Behaviour

**A. The verb.** It is `relevo bugreport [--logs] [--name N] [--round N] [--raw] [--out PATH] [--stdout | --json | --gh]`. It is never called `report`, which stays an unknown subcommand.
- **Default:** assemble the bundle, redact it, and write it as markdown to `<state root>/bugreports/bugreport-<UTC timestamp>.md` (for example `bugreport-20260930T004312Z.md`), mode 0600. Each run writes a new file, so a report is never overwritten (the repo's never-destroy-a-record rule). Then print the path and this line on stdout:
  `gh issue create --repo fuad-daoud/relevo --title '<title>' --body-file <path> --label bug`
  The line uses POSIX single-quote quoting.
- **`--gh`:** writes the same file, then runs exactly that argv with `gh`. A missing `gh` is `not_available`; its message is the printed line.
- **`--out PATH`:** writes the markdown to exactly PATH instead of the dated default, creating parent directories; an unwritable PATH is `internal`, exit 1. It combines with `--gh` (the argv names PATH), `--logs` and `--raw`; with `--stdout` or `--json` it is `usage`, exit 2.
- **`--stdout`:** prints the markdown and writes no file.
- **`--json`:** prints the bundle document through `printDoc` and writes no file.
- `--stdout`, `--json` and `--gh` are mutually exclusive. Combining them is `usage`, exit 2.
- **Title:** `relevo <version>: <code> in <verb>` when a last error is recorded, else `relevo <version>: bug report` **(plan's choice)**.

**B. Sources.** All sources are read-only. Each one is independent. A source that errors or panics becomes one visible line, `<section>: omitted: <reason>`, and never fails the run. The verb fails only on a usage error or on failing to write or print its own output.

**C. Default sections (metadata only).** Each section is an explicit allow-list projection, never a dump of a whole document.
1. **Environment:** relevo version and distribution, Go version, GOOS/GOARCH, and the state root after redaction.
2. **Last error**, when recorded: time, verb, argv, code, message, next.
3. **Doctor:** the `DoctorDoc` that `relevo doctor --json` prints.
4. **Status:** per binding, only name, actor, candidate token, state, round and round count, shape, local or remote, and the pending kind. `view.BindingStatus.Tail` and every free-text field are excluded.
5. **Rounds:** the last 5 log entries per binding -- with `--name N` only that binding, and with `--round N` only that round (which requires `--name N`). Keeping only seq, ts, round, direction, kind, route, confirmed, late, tier, outcome, halted_at and usage totals. `Payload`, `Note`, `Path`, `ChangedPaths`, `CommandsRun` and `NotDone` are excluded.
6. **Hooks:** the last 20 runs from `hooks.KVLog.Runs`, keeping at, event, the basename of argv[0], exit_code, and error truncated to 200 runes. `Output` is excluded.
7. **Gates:** active gates plus the last 20 ledger entries, keeping kind, subject, at, until, source, binding, and note truncated to 200 runes.
8. **Daemon:** `Store.DaemonRunning` and `ReadDaemonInfo`.
9. **Journal** (Linux only): `journalctl --user -u relevo.service -n 100 --no-pager -o short-iso`, run through an injected exec. Anywhere else it is the line `journal: omitted: not available on <GOOS>`.

**D. `--logs`.** It adds three things for one round per selected binding: the newest round of each live binding, at most the 3 most recently active bindings -- or, with `--name N`, just that binding, and with `--round N` (which requires `--name N`) exactly that round.
- The report, capped at 16 KiB.
- The diff, capped at 64 KiB.
- The transcript tail: the last 200 lines, capped at 64 KiB, read through `relevo.RoundTranscript`.

It also adds hook `Output`. All of it passes through `sanitize.Text`, then redaction. Truncation is marked in the text.

**E. Redaction.** This pass runs over the whole bundle, markdown and JSON alike, as the last step before output. `--raw` skips it and the bundle says so in its header. The rules:
- `$HOME` becomes `~`, and any other `/home/<u>` or `/Users/<u>` prefix also becomes `~`.
- The current user name becomes `<user>` and `os.Hostname()` becomes `<host>`. Both match whole tokens only. Names shorter than 3 characters are not replaced, and the header says so.
- Git remotes and URLs (`git@h:o/r`, `ssh://`, `http(s)://`, including `user:tok@` credentials) become `<remote>`. The only exception is URLs under `github.com/fuad-daoud/relevo`.
- Secret-shaped strings become `<redacted>`:
  - `sk-ant-…`, `sk-…`, `ghp_`/`gho_`/`ghs_`/`github_pat_`, `AKIA…`, `xox[baprs]-`
  - JWTs (`eyJ…`) and PEM `PRIVATE KEY` blocks
  - `Bearer <tok>` and `(key|token|secret|password)=<value>`
  - base64/base62 runs of 40 or more characters that mix case and digits
  - Hex of 40 characters or fewer is kept, so commit SHAs survive.

The placeholder `<redacted>` has one spelling: a new exported constant in `internal/sanitize`. `internal/delivery/agy_creds.go:40` `redactedToken` switches to it. `bugreport` never reads the secret rows (`config secret`), `client.key` or typesafe keys.

**F. The error loop.**
- The `internal` catalog row gets `next: "relevo bugreport"`.
- `main()` records every coded `internal` failure into one slot, `<state root>/last-error.json`. The write is atomic (temp file then rename), mode 0600, and the state root is created with `store.StateRootMode`.
- The record holds time, version, verb, argv, code, message and next.
- The write runs after `run` returns and before `report` renders. It swallows every error and recovers from any panic, so the original error, its rendering and its exit code are unchanged.
- A run whose `args[0]` is `bugreport` never records. That way a bundle cannot record itself.
- The bundle's "Last error" section reads this slot.

**G. The read-only runtime.**
- `newRuntimeReadOnly()` combines `loadConfigReadOnly` with `buildRuntime(root, L, false)`.
- When `relevo.db` exists, it opens `db.OpenReadOnly` and sets `rt.Store = store.NewShared(root, "", d)`, `rt.DB = d`, `rt.Gates = rt.Latency = d`.
- Any write through it fails at the SQLite level, and that failure becomes an omitted line.
- `bugreport` never calls `newRuntime`, `SyncRemoteUnlessDaemon`, `MarkViewed`, or a non-peek `relevo.Show`.

**H. Cases the tests pin.**
- The default bundle contains no transcript, report, diff or hook-output text: the fixture's marker strings are absent.
- The default bundle contains no secret-shaped string: the fixture seeds a secret value of each kind into a note, a hook error and a gate note.
- The redaction rules are table-driven.
- A markdown golden with a fixed clock.
- The `--json` document shape.
- A failing or panicking source becomes an omitted line.
- The `gh` argv and printed line are asserted, never executed.
- `last-error.json` is written for `internal` only, and not for `bugreport`.
- A failing `last-error` write leaves `report`'s output and exit byte-identical.
- `internal` next is `relevo bugreport` in both renderings.

## Seams

| Seam | Where |
| --- | --- |
| Core package (new, fully linted, stdlib only) | `internal/bugreport/`: `bundle.go` (Bundle, Section, Omission, Source, `Collect` with per-source recover), `redact.go` (Redactor + rules), `render.go` (Markdown, JSON Doc), `lasterror.go` (LastError, `WriteLastError`/`ReadLastError`, slot name), `gh.go` (`IssueArgv(title, path) []string`, `ShellLine([]string) string`), `testdata/bundle.golden.md`. Package comment of 1–3 lines. Funcs ≤ 70 lines, files ≤ 600. |
| Projections | `internal/bugreport/project.go`: pure functions from `view.Report`, `[]store.LogEntry`, `[]hooks.HookRun`, `availability.Ledger`/`[]Gate` and `DoctorDoc`-shaped input to allow-listed rows. A `DoctorDoc` mirror type avoids importing `cmd`. |
| Placeholder | `internal/sanitize/text.go` (or a new `redact.go` beside it): an exported `Redacted = "<redacted>"`; widen the package comment by one clause. `internal/delivery/agy_creds.go:38-40` uses it. |
| Doctor reuse | `cmd/relevo/doctor.go:251-400`: extract everything between runtime build and render into `doctorReport(rt relevo.Runtime, L config.Loaded) (doctor.Report, error)`. `cmdDoctor` keeps flags, runtime, render and exit. This is a pure move with identical output. |
| Read-only runtime | new `cmd/relevo/bugreport_sources.go`: `newRuntimeReadOnly()` and the `[]bugreport.Source` wiring (doctor, status via `relevo.Status`, rounds via `Store.List`/`ReadLog`, hooks via `hooks.NewKVLog(db.TxKV{DB: d})`, gates via `availability.LoadLedger`/`Gates`, daemon, journal, and for `--logs` `relevo.RoundTranscript` with `Store.ReadFile`, `relevo.Show` with `Peek: true`). `wire.go` is at 440 lines, so the function does not go there. |
| Verb | new `cmd/relevo/bugreport.go`: `bugreportFlagSet`, `cmdBugreport`, and a package-level `bugreportExec` seam (journal and `gh`) that tests replace. |
| Dispatch | `cmd/relevo/main.go:191-234` (a `case "bugreport"`) and the `usage` text at `:43-83` (one line after `doctor`). |
| Registry | `cmd/relevo/registry_rows.go`: an entry after `doctor` (`:206-214`): Flags `--gh --json --logs --name --out --raw --round --stdout`, Output `json:BugreportDoc`, Exit `[0,1,2]`, Errors `usage`, `not_available`, `internal`. `cmd/relevo/registry.go:29-87`: `"bugreport": installer(bugreportFlagSet)`. |
| Catalog | `cmd/relevo/clierror.go:68`: `codeInternal: {exit: 1, next: "relevo bugreport"}`. |
| Recording | `cmd/relevo/main.go:107-115`: `main` calls a new `recordInternal(args, err)` between `run` and `report`. It lives in new `cmd/relevo/lasterror.go`, resolves the root via `store.DefaultRoot`, and delegates to `bugreport.WriteLastError`. |
| Tests that change with the catalog | `cmd/relevo/clierror_test.go:121-126` (TestReportHumanErrorLine now expects the next line), `:80-89` (add `codeInternal` to TestCatalogNextHints), `cmd/relevo/contract_docs_test.go:549-567` (the "no next line for internal" loop now expects `next: relevo bugreport`; JSON next likewise). |
| Docs | `README.md` "Command surface" (`:257`): one bullet. `internal/mastermind/guide.md` "Output and errors"/"When something is stuck": one bullet (an `internal` failure's next is `relevo bugreport`; run it and pass the printed `gh` line to the human). |
| Spec / plan | `docs/specs/2026-09-30-bugreport-design.md` (transcribed from #709), `docs/plans/2026-09-30-bugreport.md` (this plan). |

**Where the seed and the code differ.**
- **Transcript caps.** The seed speaks of the `show --transcript` reader "with its caps". `relevo.RoundTranscript` (`internal/relevo/transcript.go:270`) and `printShow` (`cmd/relevo/show.go:265`) have no cap. The caps in D are new and live in `internal/bugreport`.
- **Non-peek `Show` writes.** A non-peek `relevo.Show` claims a pending payload (`internal/relevo/show.go:108-120`), and `printShow` stamps viewed. So `--logs` must use `Peek: true` and call `relevo.Show` directly, never `printShow`.
- **Redaction precedent.** It is only `delivery`'s known-token replace (`deliver_agy.go:368-376`). There is no general redactor to reuse. `internal/sanitize` owns control-char neutralising only. So the rules live in `internal/bugreport`, and only the placeholder is shared.
- **Doctor and secrets.** Doctor's report loads config, which carries `ClientKey`/`Typesafe` in memory (`doctor.go:280-289`, `:391`). Reusing it means the doctor section reads what `relevo doctor` reads and renders no secret. `bugreport`'s own sources never open secret rows or key files. The secret-absence test is the guard. If #709 forbids even that in-memory load, halt at step B3.
- **Uncoded errors.** `report()` also prints uncoded errors as prose with exit 1 (`main.go:156`). Decision 3 names `internal`, so only coded `internal` failures are recorded.

## Steps

The work is three rounds, one commit each, on one branch and one PR.

- Focused command per round: `go test -count=1 ./internal/bugreport/ ./internal/sanitize/ ./internal/delivery/ ./cmd/relevo/ -run '<names>'`
- Full check, once per round at the end: `make check`

Tests come first in every step: write the failing test, then the code.

**Round A: core, redaction and goldens (`internal/bugreport`, `internal/sanitize`, `internal/delivery`)**

- **A1.** Write `docs/specs/2026-09-30-bugreport-design.md` from the issue text in the appendix below (do not run `gh`; the builder may not have network or approval). Deliverable: the spec, including the amendments above. Worked when it states decisions 1–7, the amendments, and this plan's remaining choices, or says where the issue differs; any difference halts the round.
- **A2.** `sanitize.Redacted`, plus `delivery` switched to it. Deliverable: one spelling. Worked when `go test ./internal/sanitize/ ./internal/delivery/` passes unmodified and `grep -rn '"<redacted>"' internal cmd` finds only the constant.
- **A3.** `redact.go` with a table test `TestRedactRules`: one row per rule in E, including the negative rows (a 40-hex SHA kept, a two-letter user not replaced, the relevo repo URL kept) and one `--raw` row. Worked when it passes.
- **A4.** `bundle.go`, `Collect`. `TestCollectOmitsFailingSource` and `TestCollectRecoversPanickingSource` show one omitted line each and the other sections intact. Worked when they pass.
- **A5.** `project.go`, the allow-list projections. `TestDefaultBundleCarriesNoContent`: fixtures carry `TRANSCRIPT-MARKER`, `PAYLOAD-MARKER`, `HOOK-OUTPUT-MARKER` and a `Tail` marker; none appears in the default render. `TestDefaultBundleCarriesNoSecrets`: the seeded secrets are absent after redaction. Worked when both pass.
- **A6.** `render.go` and the golden. `TestMarkdownGolden` uses a fixed clock and fake sources against `testdata/bundle.golden.md`, updated with the repo's existing `-update` flag convention (see `internal/agentsrc/render_test.go`). `TestJSONDocShape` pins top-level keys and a section's keys. Worked when both pass and the golden is committed.
- **A7.** `lasterror.go` and `gh.go`. `TestLastErrorRoundTrip` checks mode 0600 and that the slot is overwritten. `TestIssueArgv` pins the exact argv. `TestShellLineQuotes` covers a title with `'`, spaces and `$`. Worked when they pass.
- **A8.** Save this plan as `docs/plans/2026-09-30-bugreport.md`. Run `make check`. Make one commit with the spec, the plan and round A's code. Worked when `make check` is green and `git status` is clean.

**Round B: verb, wiring, registry and docs (`cmd/relevo`)**

Every test here follows the CLAUDE.md CI rule. No `cmd/relevo` test spawns a harness, runs `gh` or `journalctl`, or reaches the network. `bugreportExec` is replaced in each test, and the fixture configures no servers, as `TestContractDocsNoHarnessOrNetwork` requires.

- **B1.** Extract `doctorReport`. Worked when `TestContractDocsDoctorShape` and `go test ./cmd/relevo/ -run Doctor` pass unmodified.
- **B2.** `newRuntimeReadOnly`. `TestReadOnlyRuntimeWritesNothing`: an XDG temp root with a database and one binding shows the database's mtime and size unchanged after a full collect, and no new file under the root except the bundle. Worked when it passes. If `relevo.Status` cannot read through the read-only store, halt and report; do not fall back to `newRuntime`.
- **B3.** `bugreport_sources.go` and `bugreport.go`, dispatch, `usage` line, registry row and installer. Tests:
  - `TestBugreportDefaultWritesDatedFileAndPrintsGhLine`: `docsEnv` plus the fake exec; the file is under `<state>/bugreports/` with the dated name, mode 0600, and stdout's line equals `ShellLine(IssueArgv(...))`. `TestBugreportOutWritesExactPath` and `TestBugreportOutUnwritableIsInternal` cover `--out`; `TestBugreportRoundRequiresName` covers `--round` without `--name` (`usage`).
  - `TestBugreportStdoutAndJSONWriteNoFile`.
  - `TestBugreportGhRunsExactArgv`: the fake records the argv.
  - `TestBugreportGhMissingIsNotAvailable`.
  - `TestBugreportModesExclusive`: exit 2.
  - `TestBugreportJournalOmittedOffLinux`: a pure function over GOOS.
  - `TestBugreportIsNotReport`: `run([]string{"report"})` is `usage` with unknown subcommand, and no registry entry is named `report`.

  Worked when those pass, plus `TestRegistryMatchesDispatchersBothWays` and `TestRegistryFlagsMatchFlagSets`.
- **B4.** README bullet and guide bullet. Worked when `go test ./internal/mastermind/ ./internal/mcp/ ./cmd/relevo/ -run 'Guide|Mastermind|MasterMind'` stays green. Any guide pin that breaks is updated only for the added bullet, and the report says so.
- **B5.** Run `make check` and make one commit. Worked when it is green. Report the `cmd/relevo` coverage against its 54.6 baseline; do not regenerate the baseline, since no code moved between packages.

**Round C: the error loop**

- **C1.** Catalog `next` for `internal`, and update the three test sites in Seams to the new expectation. Worked when `go test ./cmd/relevo/ -run 'Catalog|ReportHuman|ContractDocs'` passes.
- **C2.** `recordInternal` in `lasterror.go` and its call in `main`. Tests:
  - `TestRecordInternalWritesSlot`
  - `TestRecordSkipsNonInternal`: `usage` and `binding_not_found`.
  - `TestRecordSkipsBugreport`
  - `TestRecordFailureLeavesReportUnchanged`: the state root is an unwritable path; `report`'s bytes and exit are equal with and without recording.

  Worked when they pass.
- **C3.** The bundle's Last error section reads the slot. `TestBundleIncludesLastError` also covers the redacted argv and message. Worked when it passes.
- **C4.** Mutation checks, each reverted after its test fails:
  - Drop the `bugreport` skip: `TestRecordSkipsBugreport` fails.
  - Drop the `Tail` exclusion: `TestDefaultBundleCarriesNoContent` fails.
  - Drop one secret rule: its `TestRedactRules` row fails.
  - Make the record error propagate: `TestRecordFailureLeavesReportUnchanged` fails.

  Worked when each named test fails under its mutation and the suite is green after reverting.
- **C5.** Run `make check` and make one commit. Do not push.

## Deleted behaviour

1. A coded `internal` failure no longer renders without a next line. Both the human and JSON forms now carry `next: relevo bugreport`.
2. The two test assertions that pinned "internal has no next" are replaced by the opposite assertion (`clierror_test.go:121-126`, `contract_docs_test.go:549-567`).
3. `delivery`'s private `<redacted>` literal is gone; the constant moves to `sanitize` with the same bytes.
4. Nothing else. No verb, flag, error code, golden or doctor row is removed, and `relevo doctor`'s output is byte-identical.

## Deliberately not done

- No automatic sending, no network in the default path, and no third-party dependency.
- No recording of uncoded prose errors (`main.go:156`).
- No new lint, comment, filesize or coverage exclusions, and no baseline regenerated. `internal/bugreport` is new and shows as "not in baseline".
- No history in code (no issue numbers, no `§`) and no MCP tool for `bugreport`.
- No mastermind or plugin changes beyond the guide bullet.

## The report must include

- Per round: the tests added by name, with the focused command's output.
- `git diff --stat` against the Seams table.
- `make check` output.
- `cmd/relevo` and `internal/bugreport` coverage figures, and the statement that the baselines and exclusions are untouched.
- The read-only proof from B2: the database's mtime and size before and after.
- Confirmation that no test executes `gh`, `journalctl` or a harness, and how `bugreportExec` is swapped.
- Each seed-vs-code note's outcome, and whether #709 matched this plan's four marked choices.
- The mutation results from C4, each reverted.
- The three commit hashes: one per round, not pushed.

---

## Appendix: issue #709 text (verbatim)

## Idea

A relevo failure should cost one command to turn into a good issue. Today the reporter
hand-assembles `relevo version`, `relevo status --json`, the platform and logs, and
redacts by hand; an agent has no deterministic path at all. Everything needed is already
recorded locally -- rounds sealed in the database, the hook run log, config revisions,
the gate ledger, `relevo doctor` -- but nothing assembles it.

Add `relevo bugreport`: one reviewable diagnostic bundle written locally, plus the exact
`gh issue create` command. Nothing is sent automatically.

## Decisions (owner, 2026-09-30)

1. The verb is `relevo bugreport`, mirroring `git bugreport` -- not `doctor --bugreport`,
   and not `report`, which collides with the round-report vocabulary.
2. The default bundle is metadata only; transcript, report and diff tails come only with
   `--logs`.
3. An `internal` error records the failure and its `next:` hint becomes
   `relevo bugreport`, so the bundle carries the failing command without clipboard work.
4. Filing is never automatic: the default writes the file and prints the
   `gh issue create ... --body-file <path>` line; `--gh` runs it, `--stdout`/`--json`
   are the agent paths.

## Shape

```
relevo bugreport [--name <binding>] [--round N] [--logs] [--raw] [--out PATH]
                 [--stdout] [--json] [--gh]
```

The bundle is template-shaped, so it can be the whole issue body: a header with a
review-before-posting manifest; what happened (the recorded failure: time, code, message,
argv); environment (version/provenance, OS/arch); doctor checks with their fixes; status
(the cwd binding, or all); round facts under `--name`/`--round`; hook and daemon-journal
facts when available; `--logs` adds transcript/report/diff tails, capped and marked.

Privacy: a redaction pass (`$HOME` to `~`, user, host, remotes, secret-shaped strings);
secret stores and key files are never read; `--raw` opts out for self-filing. Sources are
read-only; a dead daemon or unreadable database becomes a line in the bundle, not a
failed command.

Downstream, #453 is the transport; this is the payload.

## Out of scope

Auto-filing or commenting, telemetry or crash upload, database or full-log dumps by
default, UI surfaces.

## Acceptance

- `relevo bugreport` on a live machine writes the file and prints the command; `--json`
  is one document.
- Default bundles carry no transcript content and no secret-shaped strings; `--logs` and
  `--raw` are explicit.
- An internal error prints `next: relevo bugreport`; the next run of it includes that
  failure.
- Registry and `relevo help --json` list the verb; `make check` stays green with no new
  exclusions.