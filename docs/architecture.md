# Architecture

relevo hands work between a MasterMind agent and builder agents. The
MasterMind writes a plan, relevo starts a builder on it in a git worktree, and
relevo carries the prompt, the report and the diff between the two. relevo
holds no judgement: whether a round is done, and what comes next, is always
the MasterMind's call.

## Moving parts

| Part | What it does | Where |
| --- | --- | --- |
| CLI | every `relevo` verb; parses flags and maps errors to exit codes | `cmd/relevo` |
| Engine | bind, send, round life, status, chains: the handoff policy | `internal/relevo` |
| Daemon | ticks every binding: reconciles finished rounds, delivers reports, ingests history, upgrades itself | `internal/relevo` (the tick), started by `cmd/relevo` |
| Database owner | the one process that opens `relevo.db`; every other process reaches it over a unix socket | `internal/db`, `internal/db/wire/owner`, `internal/db/wire/client` |
| Runners | the harness processes (Claude Code, OpenCode, Muse and others) a round starts | `internal/harness`, `internal/spawn`, `internal/proc` |
| Remote server and client | run builders on another machine over HTTPS and git bundles | `internal/serve`, `internal/remote`, `internal/remote/client`, `internal/uplink` |
| Cockpit | the `relevo ui` terminal interface | `internal/ui` and its subpackages |

## Layers

A package imports only packages below it. Print each package's layer (0
imports no relevo package; n imports nothing above n − 1) with:

```sh
go list -f '{{.ImportPath}} {{join .Imports " "}}' ./... | grep '^github.com/fuad-daoud/relevo'
```

The packages fall into five bands, bottom to top:

| Band | Packages | What they own |
| --- | --- | --- |
| Foundations | `text`, `sanitize`, `tmpdir`, `trail`, `refusal`, `jsonshape`, `patch`, `pathscope`, `spawn`, `skill`, `shipped`, `serviceunit`, `diskfree`, `account`, `blobstore`, `board/theme`, `db/wire` | Pure helpers and small primitives with no relevo imports: display text, untrusted-text cleaning, the state-root layout, the refusal errors the CLI maps to exit codes, the process model, the database wire protocol. |
| Records | `db`, `db/wire/client`, `db/wire/owner`, `store`, `synclog`, `sync`, `installation`, `usage`, `transcript`, `reporttail`, `harness`, `proc`, `agentsrc`, `candidate`, `policy`, `roles`, `workflow` | The system of record and the documents in it: the database and its owner, bindings and the round log, the sync log, what a harness is and how to start one, candidates, policy, actors and workflows. |
| Services | `config`, `availability`, `resolve`, `capture`, `ingest`, `delivery`, `consult`, `chain`, `git`, `housekeep`, `mastermind`, `remote`, `remote/client`, `stats`, `histq`, `hooks`, `classify`, `consent`, `upgrade`, `syncpipe`, `syncworker`, `sync/verb`, `board`, `chatlabel`, `release`, `isolate` | One job each, used by the engine: configuration and secrets, which candidate serves an actor, what a round changed, reading history into the database, getting reports to a MasterMind, consults, git, remote protocol, stats, hooks. |
| Engine | `view`, `relevo`, `bugreport` | `relevo` runs the handoff. `view` holds the plain types a status or listing is drawn from, and renders them; `relevo` computes and fills them. |
| Surfaces | `doctor`, `setup`, `pick`, `uplink`, `serve`, `mcp`, `ui`, `ui/dash`, `ui/stats`, `ui/widget`, `cmd/relevo` | What a human, a harness or another machine talks to: the health checks, the setup wizard, the remote server, the MCP server, the cockpit and the CLI. |

All package paths except `cmd/relevo` are under `internal/`. Two packages
serve tests only: `db/dbtest` (a migrated template database per test binary)
and `e2e` (end-to-end rounds against a fake harness).

## The import rule

`scripts/check-layers.sh` (part of `make check-static`) reads the band table
above and fails a package that is in no band, imports a higher band, or is a
Foundations package importing any relevo package. Test files are not held.
A new package goes into the table in the same change.

- A package imports only packages in a lower layer. A cycle does not compile;
  an upward import that happens not to cycle is still wrong, because it pulls
  the upper package's dependencies into everything below.
- The foundations stay free of relevo imports. `internal/text` imports only
  `fmt`, `strconv` and `strings`: everything renders through it, so one
  internal import there reaches the whole tree.
- When two packages need the same helper, it lives in the lower one, or moves
  down to a package both can import. It is never copied.
- Adding an import that deepens a package's dependencies is a design change:
  check `go list -deps <package>` before and after.

## New code goes here

| New code | Home |
| --- | --- |
| A wording helper (plurals, first lines, fallbacks) | `internal/text` |
| A shape a human reads (status, a listing) | `internal/view`; the engine computes the values |
| A CLI verb | `cmd/relevo` for flags and exit codes, `internal/relevo` for the work |
| A rule a CLI test needs to pin | a pure function in `internal/relevo` (CI has no harness or network) |
| A cockpit screen | `internal/ui`; shared layout and palette in `internal/ui/widget` |
| A database table or query | `internal/db`; only it opens the database |
| A binding or round-log field | `internal/store` |
| A remote route | `internal/serve`; shared request and reply shapes in `internal/remote` |
| An engine operation (bind, send, round life) | `internal/relevo` |
| A configuration key | `internal/config`, and `internal/policy` for policy knobs |

## Life of a round

1. **Bind.** `relevo bind` reaches `internal/relevo`, which creates the
   binding and its worktree. `internal/store` refuses a second writer on a
   working tree that a binding not yet done still drives.
2. **Send.** `relevo send` copies the plan into the binding's state directory
   as the round's prompt (`internal/store`) and records the tree the round
   starts from (`internal/capture`).
3. **Run.** `internal/resolve` picks the candidate: the first one in the
   actor's order that no gate holds. The runner starts as that harness kind
   (`internal/harness`) through the process model in `internal/spawn`, or on a
   server through `internal/uplink`.
4. **Report.** The builder writes `NNN-report.md`, then an empty `NNN-done`
   as its last action. The daemon's tick sees the process exit; a report
   without the marker closes the round as `unmarked`.
5. **Diff.** `internal/capture` records the diff from send to report. Changes
   made between a report and the next send are drift, recorded apart from the
   round and never notified.
6. **Deliver.** `internal/delivery` gets the report to the MasterMind over the
   route its harness supports.
7. **Done.** The MasterMind verifies the round and runs `relevo done`.
   The daemon's ingest (`internal/ingest`) folds each binding's rounds and
   events into the database that history and stats read.

## Related

- [Code style](style.md): comments, size, helpers, tests and what
  `make check` enforces.
- `internal/mastermind/guide.md`: the dispatch protocol relevo injects into a
  MasterMind's session.
