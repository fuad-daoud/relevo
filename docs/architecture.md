# Architecture

relevo moves work between a MasterMind and builders: the MasterMind stages a plan file, a runner executes it in a worktree, and relevo carries the prompt, the report and the diff between them. The moving parts are the CLI (`cmd/relevo`), the daemon (`internal/serve`), the database owner (`internal/db/wire/owner`), the runners (`internal/harness` kinds started through `internal/spawn`), the remote server and client (`internal/serve`, `internal/remote/client`), and the cockpit (`internal/ui`). The command loop lives in `internal/mastermind/guide.md`; this guide maps where each part lives in the tree.

## Layers

Layer order comes from module-internal imports, computed with:

```
go list -f '{{.ImportPath}} {{join .Imports " "}}' ./...
```

A layer holds the packages whose deepest internal dependency sits one layer down. Layer membership traces to that listing; each ownership line traces to the package comment or files named beside it.

| Layer | Packages | Owns |
| --- | --- | --- |
| 0 | `internal/account`, `internal/blobstore`, `internal/board/theme`, `internal/db/wire`, `internal/diskfree`, `internal/e2e` (test-only), `internal/jsonshape`, `internal/patch`, `internal/pathscope`, `internal/refusal`, `internal/sanitize`, `internal/serviceunit`, `internal/shipped`, `internal/skill`, `internal/spawn`, `internal/text`, `internal/tmpdir`, `internal/trail` | `internal/blobstore` holds the bodies sync moves outside the log, one object per stored value (`internal/blobstore/blobstore.go`). `internal/text` holds the display-text helpers the CLI, the cockpit views, the daemon and the listings share (`internal/text/text.go:1-3`). |
| 1 | `internal/db/wire/client`, `internal/db/wire/owner`, `internal/harness`, `internal/proc`, `internal/reporttail`, `internal/syncworker`, `internal/transcript` | `internal/harness` owns the runner kinds and their agent definition files (`internal/harness/agentfiles.go`). |
| 2 | `internal/agentsrc`, `internal/candidate`, `internal/db`, `internal/isolate`, `internal/usage`, `internal/workflow` | `internal/db` is the system of record, the sqlite file behind this package alone (`internal/db`, package comment). |
| 3 | `internal/chatlabel`, `internal/db/dbtest`, `internal/installation`, `internal/policy`, `internal/release`, `internal/synclog` | — |
| 4 | `internal/classify`, `internal/consent`, `internal/hooks`, `internal/roles`, `internal/store`, `internal/sync` | `internal/store` owns the on-disk state: the bindings and the append-only round log (`internal/store/types.go:1-2`). `internal/sync` is the scaffolding cloud sync needs before a byte moves (`internal/sync/sync.go`). |
| 5 | `internal/availability`, `internal/git`, `internal/housekeep`, `internal/mastermind`, `internal/sync/verb`, `internal/syncpipe`, `internal/upgrade` | `internal/mastermind` owns the mastermind identity records and the rules that mint, validate and resolve them (`internal/mastermind/mastermind.go:1-3`). |
| 6 | `internal/board`, `internal/capture`, `internal/delivery`, `internal/ingest`, `internal/remote`, `internal/stats` | `internal/delivery` gets a mastermind its queued payloads over a live push claim (`internal/delivery/deps.go:1-3`). `internal/ingest` reads a binding and upserts every fact it holds into `internal/db` (`internal/ingest/source.go:1-3`). `internal/remote` owns the remote-builder wire protocol — auth, bundle transport, round state, server registry — with the client in `remote/client` (`internal/remote/auth.go`, `bundle.go`, `rounds.go`, `servers.go`, `client/`). |
| 7 | `internal/config`, `internal/consult`, `internal/histq`, `internal/remote/client` | — |
| 8 | `internal/resolve` | `internal/resolve` picks the serving candidate: the first ungated candidate in the actor's order (`internal/resolve/candidate.go:185`). |
| 9 | `internal/chain` | — |
| 10 | `internal/view` | `internal/view` renders a binding's state for a human and holds the plain types that carry it; the computing code stays in `relevo` (`internal/view/status.go:1-3`). |
| 11 | `internal/bugreport`, `internal/relevo` | `internal/relevo` implements the handoff policy — which text moves between a mastermind and a builder, when, and when to stop — and holds no judgement (`internal/relevo/runtime.go:1-3`). |
| 12 | `internal/doctor`, `internal/pick`, `internal/ui/dash`, `internal/ui/widget`, `internal/uplink` | — |
| 13 | `internal/mcp`, `internal/serve`, `internal/setup`, `internal/ui/stats` | `internal/serve` is the remote-builder server: the enrolled-client registry, the v1 wire routes over a git-bundle transport, the per-owner binding stores and the daemon that ticks them (`internal/serve/serve.go:1-3`). |
| 14 | `internal/ui` | `internal/ui` is the fleet cockpit: the shell model, its views, forms and overlays (`internal/ui/ui.go:1-2`). |
| 15 | `cmd/relevo` | `cmd/relevo` is the CLI surface: one file per verb family (`cmd/relevo/bind.go`, `chain.go`, `config.go`). |

## Import rule

The bottom layer imports no relevo package. `internal/text` imports only `fmt`, `strconv` and `strings`, and stays that way: every higher layer renders through it, so a dependency from it to anything internal turns the layer map into a cycle. The same bar holds for the other bottom packages (`internal/sanitize`, `internal/trail`, `internal/tmpdir`, `internal/db/wire` and the rest of layer 0). Shared homes above the bottom name their boundary in their package comment, as `internal/view` does.

## New code goes here

| New code | Home |
| --- | --- |
| Wording helper shared by CLI, views and daemon | `internal/text` |
| Listing shape a human reads | `internal/view` (plain types; computation stays in `internal/relevo`) |
| CLI verb | `cmd/relevo`, calling into `internal/relevo` |
| Cockpit screen | `internal/ui`, with the dashboard in `internal/ui/dash`, stats in `internal/ui/stats`, shared pieces in `internal/ui/widget` |
| Database query | `internal/db`, behind its package alone |
| Remote route | `internal/serve` (v1 wire routes); protocol shapes in `internal/remote` |
| Engine operation (bind, send, round life, status) | `internal/relevo` |

The comment, size and test rules each change follows live in `docs/style.md`.

## Life of a round

1. Bind: `relevo bind --name <n> --feature <label>` (`cmd/relevo`) reaches `internal/relevo` (`internal/relevo/bind_create.go`), which refuses a taken name and creates the binding and its worktree.
2. Send: `relevo send --name <n> --file <path>` stages the prompt in `internal/relevo` (`internal/relevo/send.go`); `internal/store` copies it to the binding's `.NNN-prompt.md` (`internal/store/promptcopy.go`).
3. Run: the runner process comes from an `internal/harness` kind and starts through `internal/spawn`.
4. Report: the builder writes `NNN-report.md`, then creates empty `NNN-done` as its last action, both inside the binding's state directory (`internal/store`). A report with no marker closes as `unmarked` (`internal/relevo/headless_reconcile.go`).
5. Ingest: `internal/ingest` reads the closed binding and upserts its facts into `internal/db`.
6. Done: `relevo done` returns to `internal/relevo` (`internal/relevo/done.go`), which releases the binding.
7. Read: `relevo show <n> --diff` reads the round in `internal/relevo` (`internal/relevo/show.go`); the diff covers send to report, and drift between report and next send is mirrored separately (`internal/relevo/remotefetch.go`, store drift path) without notifying.

## Kept rationale

Each item below names the file that states it in code:

- File handoffs: the staged prompt copy (`internal/store/promptcopy.go`), the report-then-marker close and its `unmarked` path (`internal/relevo/headless_reconcile.go`), and the loop wording (`internal/mastermind/guide.md`).
- Judgement and mechanism split: relevo holds no judgement; every judgement stays with the mastermind agent (`internal/relevo/runtime.go:1-3`, `README.md:17-20`).
- One writer per tree: a bind on a taken name is refused (`internal/relevo/bind_create.go:52-59`).
- Round diff and unattributed drift: the diff covers send to report (`internal/relevo/show.go`); drift is fetched and stored apart from it (`internal/relevo/remotefetch.go:200-206`) and never changes binding state or notifies.
- Candidate order: the first ungated candidate in the actor's order serves (`internal/resolve/candidate.go:185`).

Pane delivery, held-state clobber guards, key-based answers and the watcher daemon have no non-test `.go` references and are not part of this architecture.
