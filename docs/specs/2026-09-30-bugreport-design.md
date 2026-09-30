# `relevo bugreport`: a local, redacted diagnostic bundle

**Issue:** #709.
**Amended by the MasterMind, 2026-09-30**, after checking #709 against the plan:
the verb's shape gains the issue's `--round N` and `--out PATH`; the default file is
dated under `<state root>/bugreports/` rather than one overwritten
`<state root>/bugreport.md`; the title format and the section allow-lists stand as the
plan wrote them; the issue sets no JSON key list, so this plan's `--json` document shape
is the contract. Nothing else in the plan changed.
**Status:** round A of three; the plan is `docs/plans/2026-09-30-bugreport.md`.

This spec is transcribed from the issue text in the plan's appendix. The plan's author
could not read #709 directly -- `gh issue view 709` needed approval -- so it worked from
the seed's decisions plus the tree at `857a8fa2`; the MasterMind then checked the four
choices the plan had marked as its own. Two of them (`--round N`, `--out PATH`) were
missing from the plan's shape and are now part of it, matching the issue. The dated
default file is this spec's own choice, and the issue does not contradict it. The title
format and the section allow-lists match the issue. No difference between #709 and this
spec is left unresolved.

## 1. Problem

A relevo failure should cost one command to turn into a good issue. Today the reporter
hand-assembles `relevo version`, `relevo status --json`, the platform and logs, and
redacts by hand; an agent has no deterministic path at all. Everything needed is already
recorded locally -- rounds sealed in the database, the hook run log, config revisions,
the gate ledger, `relevo doctor` -- but nothing assembles it.

`relevo bugreport` assembles it: one reviewable diagnostic bundle written locally, plus
the exact `gh issue create` command. Nothing is sent automatically.

## 2. Decisions

1. **The verb is `relevo bugreport`**, mirroring `git bugreport` -- not
   `doctor --bugreport`, and not `report`, which collides with the round-report
   vocabulary. (Issue.)
2. **The default bundle is metadata only**; transcript, report and diff tails come only
   with `--logs`. (Issue.)
3. **An `internal` error records the failure and its `next:` hint becomes
   `relevo bugreport`**, so the bundle carries the failing command without clipboard
   work. (Issue.)
4. **Filing is never automatic:** the default writes the file and prints the
   `gh issue create ... --body-file <path>` line; `--gh` runs it, `--stdout`/`--json` are
   the agent paths. (Issue.)
5. **The default file is dated, under `<state root>/bugreports/`**, one file per run
   (`bugreport-<UTC timestamp>.md`, mode 0600). A report is never overwritten -- the
   repo's never-destroy-a-record rule -- and `--out PATH` writes exactly where it is told
   instead. (Seed and plan; the issue names no path.)
6. **The title is `relevo <version>: <code> in <verb>`** when a last error is recorded,
   else `relevo <version>: bug report`. (Seed; matches the issue.)
7. **Every section is an explicit allow-list projection**, never a dump of a whole
   document: the fields the sections list in §4 are all that leaves the machine.
   (Seed; matches the issue.)

## 3. Shape

```
relevo bugreport [--name <binding>] [--round N] [--logs] [--raw] [--out PATH]
                 [--stdout] [--json] [--gh]
```

- **Default.** Assemble the bundle, redact it, write it as markdown to
  `<state root>/bugreports/bugreport-<UTC timestamp>.md` (for example
  `bugreport-20260930T004312Z.md`), mode 0600, then print the path and this line on
  stdout, POSIX single-quoted:

  ```
  gh issue create --repo fuad-daoud/relevo --title '<title>' --body-file <path> --label bug
  ```

- **`--gh`.** Writes the same file, then runs exactly that argv with `gh`. A missing `gh`
  is `not_available`, and its message is the printed line.
- **`--out PATH`.** Writes the markdown to exactly PATH, creating parent directories; an
  unwritable PATH is `internal`, exit 1. It combines with `--gh` (the argv names PATH),
  `--logs` and `--raw`; with `--stdout` or `--json` it is `usage`, exit 2.
- **`--stdout`.** Prints the markdown and writes no file.
- **`--json`.** Prints the bundle document through `printDoc` and writes no file.
- **`--stdout`, `--json` and `--gh` are mutually exclusive.** Combining them is `usage`,
  exit 2. `--round N` requires `--name N`.
- **The verb is never called `report`**, which stays an unknown subcommand.

## 4. Sections

The default bundle is metadata only. Each section is filled by one read-only source; a
source that errors or panics becomes one visible line, `<section>: omitted: <reason>`,
and never fails the run. The verb fails only on a usage error, or on failing to write or
print its own output.

1. **Environment:** relevo version and distribution, Go version, GOOS/GOARCH, and the
   state root after redaction.
2. **Last error**, when recorded: time, verb, argv, code, message, next.
3. **Doctor:** the `DoctorDoc` that `relevo doctor --json` prints.
4. **Status:** per binding, only name, actor, candidate token, state, round and round
   count, shape, local or remote, and the pending kind. The status row's tail and every
   free-text field are excluded.
5. **Rounds:** the last 5 log entries per binding -- with `--name N` only that binding,
   and with `--round N` only that round (which requires `--name N`). Only seq, ts, round,
   direction, kind, route, confirmed, late, tier, outcome, halted_at and usage totals are
   kept; `Payload`, `Note`, `Path`, `ChangedPaths`, `CommandsRun` and `NotDone` are
   excluded.
6. **Hooks:** the last 20 runs from `hooks.KVLog.Runs`, keeping at, event, the basename of
   argv[0], exit_code, and error truncated to 200 runes. `Output` is excluded.
7. **Gates:** active gates plus the last 20 ledger entries, keeping kind, subject, at,
   until, source, binding, and note truncated to 200 runes.
8. **Daemon:** `Store.DaemonRunning` and `ReadDaemonInfo`.
9. **Journal** (Linux only): `journalctl --user -u relevo.service -n 100 --no-pager
   -o short-iso`, run through an injected exec. Anywhere else it is the line
   `journal: omitted: not available on <GOOS>`.

**`--logs`** adds three things for one round per selected binding: the newest round of
each live binding, at most the 3 most recently active bindings -- or, with `--name N`,
just that binding, and with `--round N` exactly that round. It adds the report (capped at
16 KiB), the diff (capped at 64 KiB) and the transcript tail (the last 200 lines, capped
at 64 KiB, read through `relevo.RoundTranscript`), and it adds hook `Output`. All of it
passes through `sanitize.Text` and then redaction, and every truncation is marked in the
text.

## 5. Privacy

The redaction pass runs over the whole bundle, markdown and JSON alike, as the last step
before output. `--raw` skips it, and the bundle says so in its header.

- `$HOME` becomes `~`, and any other `/home/<u>` or `/Users/<u>` prefix also becomes `~`.
- The current user name becomes `<user>` and `os.Hostname()` becomes `<host>`. Both match
  whole tokens only. Names shorter than 3 characters are not replaced, and the header
  says so.
- Git remotes and URLs (`git@h:o/r`, `ssh://`, `http(s)://`, including `user:tok@`
  credentials) become `<remote>`. The only exception is URLs under
  `github.com/fuad-daoud/relevo`.
- Secret-shaped strings become `<redacted>`: `sk-ant-…`, `sk-…`,
  `ghp_`/`gho_`/`ghs_`/`github_pat_`, `AKIA…`, `xox[baprs]-`, JWTs (`eyJ…`), PEM
  `PRIVATE KEY` blocks, `Bearer <tok>`, `(key|token|secret|password)=<value>`, and
  base64/base62 runs of 40 or more characters that mix case and digits. Hex of 40
  characters or fewer is kept, so commit SHAs survive.

`<redacted>` has one spelling: an exported constant in `internal/sanitize`. The bundle never
*includes* a secret value, and never opens the secret rows (`config secret`), `client.key` or
the typesafe keys for itself. The config load it shares with `relevo doctor` does read the
stored secrets into memory -- as `relevo doctor` does on every run -- and renders none of
them; `TestDefaultBundleCarriesNoSecrets` is the guard that holds the bundle to that: the
allow-list projections carry no secret, the redaction pass rewrites any secret-shaped string,
and no rendering contains a seeded secret.

Sources are read-only: a dead daemon or an unreadable database becomes a line in the
bundle, not a failed command. A write attempted through the read-only runtime fails at
the SQLite level and becomes an omitted line.

## 6. The error loop

The `internal` catalog row gets `next: "relevo bugreport"`. `main()` records every coded
`internal` failure into one slot, `<state root>/last-error.json`: atomic (temp file then
rename), mode 0600, written after `run` returns and before `report` renders, holding
time, version, verb, argv, code, message and next. The write swallows every error and
recovers from any panic, so the original error, its rendering and its exit code are
unchanged. A run whose `args[0]` is `bugreport` never records, so a bundle cannot record
itself. The bundle's "Last error" section reads this slot.

## 7. Out of scope

Auto-filing or commenting, telemetry or crash upload, database or full-log dumps by
default, UI surfaces.

## 8. Acceptance

- `relevo bugreport` on a live machine writes the file and prints the command; `--json`
  is one document.
- Default bundles carry no transcript content and no secret-shaped strings; `--logs` and
  `--raw` are explicit.
- An internal error prints `next: relevo bugreport`; the next run of it includes that
  failure.
- Registry and `relevo help --json` list the verb; `make check` stays green with no new
  exclusions.

Downstream, #453 is the transport; this is the payload.