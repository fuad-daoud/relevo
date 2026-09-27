# relevo

[![ci](https://github.com/fuad-daoud/relevo/actions/workflows/ci.yml/badge.svg)](https://github.com/fuad-daoud/relevo/actions/workflows/ci.yml)
[![license: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Site: [relay-site.fuad-daoud.com](https://relay-site.fuad-daoud.com) (source in [fuad-daoud/relevo-site](https://github.com/fuad-daoud/relevo-site), together with the `DESIGN.md` and `PRODUCT.md` that govern the page).

`relevo` automates the plan/report handoff between two AI coding agents. A
human talks to a **planner** agent; the planner hands work to a **builder**
agent; relevo moves the files between them so the human never copy-pastes a
plan or a report by hand. Builders are headless or remote processes, and
relevo no longer integrates with herdr.

<!-- name-guard: off -->
relevo is Spanish for relay (the changeover in a relay race); it was called relay until v0.12.0.
<!-- name-guard: on -->

Relevo makes no judgements. It moves files, starts builders, and reports what
each round did — whether a report is good, whether a question needs a human,
whether the work is done, is a decision that stays with the planner (or the
human) at every step.

## Requirements

- **Two agent harnesses** — one for the planner, one for the builder. relevo
  knows how to start `opencode`, `claude`, `agy` and `codex`; you tell it
  which models in [Candidates](#candidates).
- **`git` on `PATH` (optional).** Required for automatic round diff capture; without it, relevo works normally but rounds produce no diffs.
- **Linux or macOS.** See [Platform support](#platform-support).
- **Go 1.22+**, to build from source. Not needed if you install a release
  binary.

## Install

Prebuilt binaries for Linux and macOS (amd64 and arm64) are attached to every
[release](https://github.com/fuad-daoud/relevo/releases); unpack one and put
`relevo` on your `PATH`.

With a Go toolchain:

```
go install github.com/fuad-daoud/relevo/cmd/relevo@latest
```

That drops `relevo` in `$(go env GOPATH)/bin` — make sure it is on your `PATH`.

A release binary and a `go install` both know how they were installed. A
release binary updates itself with `relevo update`: it downloads the archive,
verifies its SHA-256 against `checksums.txt`, preflights the new binary and
renames it over the running one. `relevo update --check` prints what it would
do and changes nothing, and `--to vX.Y.Z` installs an exact tag (downgrades
included). A `go install` gets its `go install` command printed instead. A
local build is left alone unless `relevo update --release` converts it to a
release binary. `relevo doctor` warns when a newer release exists and prints
the update step for that install. `relevo status` shows one line when a newer
release exists.

Or from a clone, which also stamps the binary with the current tag so
`relevo version` is meaningful:

```
git clone https://github.com/fuad-daoud/relevo
cd relevo
make install        # builds and installs ~/.local/bin/relevo
```

To install the built binary by hand, use the same two commands `make install`
does. The rename is atomic within the directory, so a running daemon never sees
a half-written file:

```
install -m755 relevo ~/.local/bin/relevo.new
mv -f ~/.local/bin/relevo.new ~/.local/bin/relevo
```

`make check` runs the full gate — `gofmt -l .`, `go vet ./...`, and
`go test -count=1 ./...` — and `make install` runs it first.

To run the reconciler as a background service:

```
make service        # systemd user unit on Linux, LaunchAgent on macOS
```

### Upgrading

`relevo update` lands a new binary the same atomic way `make install` does -- a
rename within the directory, so a running daemon never sees a half-written
file. A running daemon moves onto a newly installed binary by itself within a
few seconds, and a round in flight is not interrupted: builders, gates and consults
run in their own systemd scopes and survive the restart. A daemon started
before this release needs one manual restart to start following upgrades —
`make service`, or `systemctl --user restart relevo.service`. `relevo doctor`
shows what the daemon is running. The daemon also refreshes the agent
definitions relevo wrote for each harness on every start, and leaves a file you
edited alone; a planner session's `relevo mcp` notices the upgrade too -- it
appends a line to every tool result saying to reconnect it (`/mcp`), so the
session loads the new server without a restart.

<!-- name-guard: off -->

### Upgrading from relay

relay was renamed relevo in v0.12.0. A machine that ran relay keeps its state,
its old `relay.service` (or LaunchAgent) and the old `relay` binary until
`relevo migrate` moves them. The order:

1. Finish or pause every binding (`relevo status`).
2. Install `relevo`.
3. Run `relevo migrate --dry-run`, then `relevo migrate`.
4. Reinstall the Claude Code plugin: remove `relay@relay`, then add the
   marketplace and install the plugin again:
   ```
   /plugin uninstall relay@relay
   /plugin marketplace add fuad-daoud/relevo
   /plugin install relevo@relevo
   ```
5. Run `relevo config agents`.
6. Restart planner sessions.

<!-- name-guard: on -->

### The Claude Code plugin

A Claude Code planner installs relevo as a plugin. The plugin provides the
`relevo mcp` MCP server and a `SessionStart` hook that runs
`relevo planner init`, so relevo knows which planner session is calling:

    /plugin marketplace add fuad-daoud/relevo
    /plugin install relevo@relevo

The plugin ships with relevo's releases. Claude Code caches an installed plugin
by version, so after upgrading relevo, update the plugin to match (`relevo
doctor` warns when they differ):

    claude plugin marketplace update relevo && claude plugin update relevo@relevo

A change under `claude-plugin/` that is not released yet never reaches an
installed plugin this way -- `update` sees the same version and skips it. To
try one, reinstall: `claude plugin uninstall relevo@relevo && claude plugin
install relevo@relevo`.

See [Claude Code plugin](#claude-code-plugin) below for how a report reaches
the planner.

## First run on a clean machine

On a clean machine, set up prerequisites and preflight with `relevo config init` and
`relevo doctor`:

1. Install relevo (see [Install](#install)).
2. Run `relevo config init` to write a starter configuration from the harnesses on
   `PATH`:
   ```
   relevo config init
   ```
   It finds the harness binaries on `PATH`, writes one builder candidate per
   harness to the candidates section except claude, which only plans (it gets the
   `planner` actor), writes the policy section and the builder
   actor, plus a `planner` and a `lite-planner` reader actor for claude and
   opencode, and installs the agent definitions into each of those harnesses. The
   configuration lives in relevo.db under the state root, not in a file; a
   file you drop into `~/.config/relevo` is imported on the next command and
   removed. It refuses to overwrite the candidates, policy or actors section
   without `--force`, and `--no-agents` skips the definitions. With claude as the
   only harness on `PATH`, the builder is written with no candidates, and init
   prints the command to add one. It says what it wrote and
   the command to run next, e.g.:
   ```
   wrote candidates (3: glm-5.3-flash, opus, deepseek-v4.1-flash)
   wrote actors (builder: glm-5.3-flash; planner: opus; lite-planner: deepseek-v4.1-flash)
   wrote  ~/.claude/agents/plan-executor.md
   wrote  ~/.config/opencode/agents/plan-executor.md
   next: edit the model names, then run: relevo doctor
   ```

   **Edit the model names it wrote** to the models your accounts may run (see
   [Candidates](#candidates)); `relevo config` prints what you configured.
   When more than one candidate serves `builder`, `relevo config` shows the
   order relevo would pick them in and says `would refuse` until the actor's
   candidate list suits you (see [Actors and agents](#actors-and-agents)).
3. Run `relevo doctor` to check your environment:
   ```
   relevo doctor
   ```
   Doctor inspects the background daemon, each harness binary on `PATH`, and the relevo plugin and its hook in Claude Code, and the builder's agent files.
4. Run the literal fix commands `relevo doctor` prints for any missing items.
5. Re-run `relevo doctor` to confirm `0 failures`.
6. Start the daemon (e.g. `relevo daemon &` or `make service`).
7. Bind your first agent from the planner session:
   ```
   relevo bind --candidate claude/anthropic/sonnet
   ```
   (or, with one candidate, `relevo bind`).

To install the agent definitions into each harness on `PATH` by hand instead —
the daemon refreshes unmodified definitions on every start and upgrade, a file
you edited is kept, and `relevo config agents --force` replaces it:
```
relevo config agents
```
One line per file says `wrote`, `updated (unchanged since relevo wrote it)`,
`kept (identical)` or `kept (differs; --force to overwrite)`. Pass `--kind` to
name a harness that is not on `PATH` yet, `--agent` for one definition,
`--dry-run` to look first. This writes `plan-executor`, `researcher`, `reviewer`
and `architect` for every kind; `relevo config agents --dry-run` shows what
would be written.

`researcher` is the read-only agent the builder's own sub-agents run as. It
exists because exactly one agent may write to a working tree: research can fan
out safely, implementation cannot. The claude and opencode definitions pin a
`model:` in their front matter as a worked example, chosen so neither needs a
provider the rest of relevo does not already assume; that line is the first
thing to change for your own setup, and a plain `relevo config agents`
keeps your edit. The agy definitions pin `model: inherit`
and that is not an example: on agy the key is a tier (`inherit`, `flash`,
`pro`) that would override the `--model` relevo passes at launch. `relevo
doctor` reports the pin each installed definition carries, warns when an
agy copy pins a tier or differs from what relevo ships, and names the
`relevo config agents ... --force` that restores it.

codex agent definitions are TOML profiles at `~/.codex/<name>.config.toml` selected
with `-p`; the researcher profile pins `gpt-5.6-luna` at `medium` for
every codex builder's research sub-agents and `relevo doctor` warns when
that pin drifts.

Then set the candidates section (see [Candidates](#candidates)) and check it
with `relevo config`. If more than one candidate serves `builder`, order them
in the builder actor's `candidates` list (see [Actors and agents](#actors-and-agents)); `relevo config` shows what
relevo would pick and says `would refuse` until you do. `relevo doctor` warns
about this too and prints a starter policy built from your candidates.

## Quick start

On a clean machine, seed your configuration first (see
[First run on a clean machine](#first-run-on-a-clean-machine)):

```
relevo config init                 # seed candidates, policy and actors, install the agent definitions
```

From the planner session, in the repository you want worked on:

```
relevo bind --candidate claude/anthropic/sonnet     # start a builder on this tree
relevo send --file plan.md         # hand it the plan; the builder starts working
relevo status                      # watch the round
relevo wait                        # block until the round closes; prints the report
relevo done <name>                 # stop relaying when you are satisfied
```

The builder writes `NNN-report.md` when it has finished and then creates an
empty `NNN-done` as its last action; relevo closes the round on that marker.
A builder that exits without the marker still closes the round, and relevo
delivers its report flagged `unmarked`.

`relevo bind` identifies the calling planner through `RELEVO_PLANNER`, which
the relevo plugin's `SessionStart` hook exports, or through the harness
process the `relevo mcp` server shares with the session. Run
`relevo planner list` to see the planners relevo knows.

Its `chat` column names each planner as a person sees it: a Claude Code chat's
title, or its last prompt, plus the claude.ai link when the session is bridged;
an opencode session's title; and `-` when nothing can be read. The label is read
from the harness's own files when the command runs and is never stored. The same
label follows the planner's name in `relevo status` and `relevo doctor`.
`relevo planner rename <id|name> <new-name>` gives a planner a name of your own.

## Command surface

- `relevo bind [--name N] [--candidate CANDIDATE] [--actor R] [--tier T [--allow-yolo]] [--gate CMD|--no-gate] [--regate N] [--resume [--rebind]] [--timeout D] [--feature L] [--planner P]`
  — start a binding between the calling planner and a builder. `--candidate` is
  a candidate token; `--actor R` is the writer actor the binding runs (default
  `builder`). A name that already exists is refused rather than reused:
  only the binding's record would be rewritten, so a fresh round 1 would
  collide with the previous session's round log. `--resume --name N` re-points that
  existing binding's planner side at the calling planner without touching the
  builder; `relevo unbind N` is the other way out.
- `relevo send [NAME|--name N] --file PATH [--dry-run] [--tier T [--allow-yolo]] [--candidate CANDIDATE] [--verify|--no-verify] [--regate N]` — stage the file as the current round's
  plan and hand it to the builder as the prompt of a fresh process started in
  the binding's tree.
  A headless binding whose previous round's process is still running refuses
  the send; wait for its report or `relevo done` it.
  `--dry-run` checks every precondition a send would and prints what it would
  do, writing nothing: no plan staged, no log entry, no prompt, no process
  started. A precondition that fails is the same error `relevo send` gives, exit
  1, with nothing written. A local binding names the exact command line it
  would run, and a remote one the server and branch without contacting it:
  ```
  would send round 5 to api-auth
    builder   headless agy/google/gemini-3.8-flash-high
    where     /usr/bin/agy -p
    tier      yolo
    plan      /home/me/.local/state/relevo/api-auth/005-plan.md  (staged from ./plan.md, 4.1 KiB)
    report    /home/me/.local/state/relevo/api-auth/005-report.md
    marker    /home/me/.local/state/relevo/api-auth/005-done
    prompt    relevo: round 5 · to builder "api-auth" · from the planner (not the human)
              Your working tree is: /home/me/.worktrees/api-auth
  ```
- `relevo show NAME --diff [--round R] [--stat] [--drift] [--anchors]` — print a round's
  captured patch to stdout, or its diffstat summary with `--stat`. Pass `--drift`
  to inspect between-rounds drift instead of the round's diff; `--drift` composes
  with `--stat` and `--round`, and defaults to the currently open round where plain
  `relevo show --diff` defaults to the newest completed one. Pass `--anchors` to prefix
  each hunk and each `' '`/`'+'` line with its `path:line`, ready to quote into a
  review comment (see "Reviewing a round" below).
- `relevo status [NAME|--name N] [--json] [--all] [--line]` — one row per binding: round, display state, the builder's own status, the last relayed event and anything pending. `--line` is the one-row-per-binding form Claude Code's status line runs (see [Status line](#status-line)). Rows are attention-first -- NEEDS YOU, ACTIVE, PAUSED, DONE, stale first within a group, newest last-event first -- the same order `relevo ui` has always used, so the two never disagree. Naming a binding shows only that one. Bindings marked DONE are hidden by default and the footer names how many are hidden.
  While a round is open a row also shows the round's live diff against its baseline (`+120/-30 in 6`, `(shared tree)` for a `--cwd` binding sharing the planner's own working tree), an ACTIVE row's `quiet <age>` since its last progress sample, and `●new` when the binding's newest report is unread (its record's `viewed_at` stamp is older than the newest report).
  `--json` also carries fields the prose above does not spell out:
  ```
  branch      the binding's worktree branch; absent for a --cwd binding
  waiting     set when the binding is stalled on a human: cause, line, since, hint
  last_seq    the Seq of the binding's newest log entry; 0 when the log is empty
  live        the round's live diff stat against its baseline tree (files/added/removed,
              shared true for a --cwd binding); absent when no round is open
  quiet_for   an ACTIVE row's age since its last progress sample; absent otherwise
  unread      true when the binding's newest report is newer than its .viewed stamp
              (or there is no stamp and a report exists)
  ```
- `relevo show NAME [--round N] --log [--after N] [--follow] [--json]` — the binding's append-only round log. Every entry carries a 1-based `seq`, monotonic within the binding and never rewritten; `--round N` shows one round, `--after N` shows only entries with a greater `seq`, `--json` prints one compact JSON object per line (NDJSON, `seq` included), and `--follow` keeps printing new entries until the binding is DONE or gone. A file written before `seq` existed reads back with `seq` equal to the line number, so nothing is rewritten.
  A hook or script that has already seen up to a known `seq` asks only for the rest:
  ```bash
  last_seq=$(relevo status --json | jq -r '.bindings[] | select(.name == "NAME") | .last_seq')
  relevo show NAME --log --after $(last_seq) --json
  ```
- `relevo history [--here] [--binding B] [--planner P] [--since D] [--limit N] [-q "<query>"] [--json] [--rows]` —
  round history as a JSON array across every binding relevo has ever recorded, live or archived, newest first. `-q` filters with the query language. See "The database" below.
- `relevo show <name> [--round N] [--plan|--report|--diff [--stat|--anchors]|--drift|--log [--follow --after N]|--transcript|--gate|--findings <id>] [--json]` —
  one round's plan, report, diff, drift, log, transcript, gate log or a consult's findings: from a live binding's open round files, or, for anything sealed or archived, from the database. See "The database" below.
- `relevo wait [NAME|--name N] [--any N1 N2 ...] [--round R] [--timeout D] [--peek]` — block
  until the round closes or the binding needs you, reading relevo's own state only.
  Exit 0: closed on the marker, stdout is the report path. 2: closed
  without it (`unmarked`, `noreport` — verify before trusting), report
  path or `-`. 3: needs you, stdout is one line saying what it is waiting on. 4:
  the binding is DONE or was unbound. 5: closed, but the builder's report says
  halted or blocked -- read it before sending again; stdout is the report path.
  6: the round has no plan entry -- it was never sent, so nothing is in flight;
  stdout says so. 124: `--timeout` (default 10m) elapsed. On every exit but 4 and
  124 the wait then prints a blank line and the oldest pending report's text
  (the report's pointer line, a blank line, then the report itself, capped at
  64 KiB), marked delivered, unless `--peek` asks for the outcome line only.
  `--any` waits on several and prints the
  winner's name first. A Claude Code planner runs `relevo wait N` as a
  background command and ends its turn: Claude Code wakes the session when the
  command exits, with the report in its output (see
  [Claude Code plugin](#claude-code-plugin)).
- `relevo ui [--interval D] [:view [args]]` — the cockpit: `:fleet`, `:rounds [query]`,
  `:round <binding> [N]`. `:fleet` is the root table of bindings; `enter` opens its round
  detail, `esc` goes back, `:` the command line, `?` the key list. `relevo ui :rounds`
  opens the rounds grid directly (`:rounds` reaches it from the fleet).
- `relevo bind --worktree --name N [--candidate CANDIDATE] [--actor R] [--cwd DIR] [--feature LABEL]` — attach an
  additional builder to this planner on its own git worktree, starting at
  round 1. This is how one planner drives several builders at once.
  `--actor R` is the writer actor the binding runs (default `builder`).
  `relevo bind --name N --server S [--base REF]` runs that builder on a
  configured remote server instead (see "Remote builders: the client" below);
  `--cwd` cannot be combined with `--server`.
  `relevo bind --branch B` checks an existing branch out into relevo's own
  worktree instead of cutting `relevo/<name>`: a local `B` is used first, and
  `origin/B` is made a local tracking branch only when no local `B` exists
  (a branch on neither is refused). A branch already checked out in another
  worktree is refused; free it first. `--name` is optional with `--branch`
  and defaults to the branch's last path segment, lowercased and reduced to
  the characters a binding name accepts. The binding records
  `existing_branch: true`, and relevo never deletes a branch it did not
  create. Works with `--server`; not with `--cwd`.
  For a `--server` binding the server's own `refs/heads/relevo/<name>` ref is
  kept in the repository beside the adopted branch: every closed round
  absorbs it and fast-forwards `B` to it; relevo never deletes the adopted
  branch; the server's `relevo/<name>` ref is deleted by `unbind --done` once it is on a
  remote-tracking ref.
- `relevo config` — show the actors, the current pick and the configured
  candidates, in three blocks; `relevo config --probe` runs each candidate
  once and records its time to first output.
- `relevo config init` — seed the candidates, policy and actors sections from
  the harnesses on `PATH` (one builder candidate per harness except claude,
  which only plans, plus a `planner` and a `lite-planner` reader actor for
  claude and opencode) and install the agent definitions (`--force`,
  `--no-agents`).
- `relevo config agents` — install the per-kind agent definitions
  (`--kind`, `--agent`, `--force`, `--dry-run`).
- `relevo config export|import|get|set|unset|edit` — read and change the
  configuration document section by section.
- `relevo config secret set|rm|list` — store, forget or list the `typesafe`
  and `client.key` secrets (the value is read from stdin).
- `relevo gate` — list this machine's active gates.
- `relevo gate <harness/provider/model> [--for D] [--reason S]` — record
  that a candidate's provider is rate-limited; gates every candidate on that
  provider until `--for` elapses, or until `relevo gate --clear` lifts it.
- `relevo gate --clear <provider|harness/provider/model>` — clear a recorded rate
  limit on a provider.
- `relevo done NAME|--name N|--pick` — mark a binding done; relaying stops.
  `--pick` chooses from a list in the terminal.
- `relevo stop NAME|--name N` — kill the builder process and close its open
  round without a report. See [Stopping a round](#stopping-a-round).
- `relevo unbind NAME|--name N|--pick [--archive]` — forget a binding, deleting its directory or
  archiving its record first. `--pick` chooses from a list in the terminal.

- `relevo unbind --done [--dry-run] [--delete] [--planner P | --all-planners]` —
  clear the DONE bindings of the calling planner (resolved like every
  planner-scoped verb), in one pass; also deletes each cleared binding's
  `relevo/<name>` branch and `refs/relevo/<name>/*` refs once each is on a
  remote-tracking ref. `--all-planners` clears every planner's, including
  bindings with no planner. If no planner resolves, it refuses rather than
  clearing everything, and each line names the binding's planner. Archives by
  default; pass `--delete` to remove each binding's directory instead
  (`relevo unbind --done --archive` is accepted as a no-op).
- `relevo unbind --sweep [--dry-run]` — delete `relevo/<name>` branches and `refs/relevo/<name>/*` refs of bindings that no longer exist, once each is on a remote-tracking ref.
- `relevo daemon [--interval D] [--check]` — the long-running reconciler; this is what the
  service unit runs. It reconciles builders, queues reports for the planner
  and syncs remote bindings. `--check` exits 0 when a daemon is running and 1 when not, printing nothing.
- `relevo mcp [--mode channel|tools|auto] [--planner P] [--interval D]` — run the
  MCP server over stdio for a Claude Code planner pane. See
  [Claude Code plugin](#claude-code-plugin).
- `relevo planner init [--name N] [--kind K --session S] [--hook claude]`;
  `relevo planner list [--json]`; `relevo planner rename <id|name> <new-name>`;
  `relevo planner forget <id|name>` — register this planner, or list, rename
  or forget its records.
- `relevo doctor` — preflight check: plugin, daemon, harness binaries, roles,
  the database and hooks. See [First run on a clean machine](#first-run-on-a-clean-machine).
- `relevo migrate [--dry-run] [--keep-old-binary] [--state-from DIR] [--state-to DIR]` —
  move a pre-relevo installation's state, switch the client unit and remove the old binary.
- `relevo serve [--listen :7777] [--state <dir>] [--interval 2s] [--insecure-http] [--max-bundle-bytes N] [--max-builders N]` — run the remote-builder server (listener + daemon).
- `relevo serve init|enroll|clients|revoke|fingerprint|status|ui|gc|unbind` — server administration, on the server host. `relevo show --owner` reads one owner's round on that host, and `serve ui` is the server's own reader.
- `relevo gate --serve [--state DIR]` — list the gates on the server's own ledger.
- `relevo gate --serve --clear <provider|token> [--state DIR]` — clear a recorded rate limit on the server's ledger.
- `relevo gate --serve <token> [--for D] [--reason S] [--state DIR]` — record a provider rate limit on the server's ledger.
- `relevo config server key [--enroll-line]` — generate this machine's remote-builder identity (an
  ed25519 keypair); prints the enrollment line a server admin runs
  `relevo serve enroll --key "<line>"` with. With `--enroll-line`, it prints
  only the `ed25519 ...` line, for scripts.
- `relevo config server add NAME URL (--fingerprint sha256:HEX | --ca system | --insecure)` —
  record a remote server; with `--fingerprint`, checks enrollment once.
  Generates and prints this machine's key when it has none yet.
- `relevo config server rm NAME` — forget a configured server; refused while
  any binding still names it.
- `relevo config server list` — one row per configured server: name, url, and this
  client's enrollment on it.
- `relevo help` — the command list. `relevo <command> -h` prints that command's
  flags.
- `relevo version` — the build's version.

Every binding-scoped command takes its binding either positionally or as
`--name`; naming it both ways at once is refused. `send` falls
back to whichever binding owns the current working directory, and a bare
`relevo status` lists them all. Naming one is **required** for `done`,
`stop` and `unbind`: those act on a specific loop — `stop` kills a
process, the others end one — and they refuse to guess (see below).

### Renamed commands

Every verb the housekeeping removed, and the form that replaces it. An old
name exits 2 and names its replacement:

| removed | use instead |
| --- | --- |
| `relevo init` | `relevo config init` |
| `relevo candidates` | `relevo config` |
| `relevo policy` | `relevo config` |
| `relevo roles` | `relevo config` |
| `relevo agent` | `relevo config agents` |
| `relevo client` | `relevo config server` |
| `relevo servers` | `relevo config server list` |
| `relevo add` | `relevo bind --worktree` |
| `relevo diff` | `relevo show --diff` |
| `relevo log` | `relevo show --log` |
| `relevo gc` | `relevo unbind --done` |
| `relevo pause` | `relevo done`, then `relevo bind --resume` |
| `relevo statusline` | `relevo status --line` |
| `relevo pull` | `relevo wait (it prints the report)` |
| `relevo unavailable` | `relevo gate <token>` |
| `relevo available` | `relevo gate --clear <provider>` |
| `relevo db` | `relevo doctor (the database row)` |

### Reviewing a round

`relevo show --diff --anchors` prints a round's patch with a `path:line` gutter on
every hunk header and every `' '`/`'+'` line, so a comment can quote a line
straight off the printed diff instead of counting by hand:

```
$ relevo show api-auth --diff --anchors
diff --git a/auth.go b/auth.go
--- a/auth.go
+++ b/auth.go
auth.go:41  @@ -38,6 +38,7 @@ func Login(ctx context.Context, u string) error {
auth.go:41  	if u == "" {
auth.go:42 +		return errEmptyUser
auth.go:43  	}
```

### The report block

The builder ends its report with a fenced `relevo` block:

```relevo
status: done            # done | halted | blocked | deferred
halted_at: ""           # which step, when halted or blocked
changed_paths: []       # repo-relative files you changed
commands_run: []        # commands you ran, e.g. ["make check"]
not_done: []            # adjacent work you deliberately left
```

The four valid statuses are `done`, `halted`, `blocked`, and `deferred`. A missing or malformed block closes the round as `unstructured` without error or refusal. When `changed_paths` does not match git's count of changed files, relevo notes the discrepancy as `paths: report N, diff M` on the diff entry.

### The cockpit: relevo ui

`relevo ui [:view [args]]` is the cockpit: a full-screen terminal reader for
live bindings. `relevo status` remains the tool for shell pipes and scripts
(`watch -n2 relevo status` for a ticker); `relevo ui` is the interactive
sibling that lets you inspect substance instead of just state.

It is **read-only** until B2 of the cockpit plan
(`docs/specs/2026-09-24-cockpit-design.md`): it never mutates state and never
appends to round logs. It holds the state lock only for the duration of a
read, exactly as `relevo status` does.

`:fleet` is the root: every binding as a table row (name, actor, candidate,
round, state, age, spend, planner, repo). `s` toggles attention and name
order and `/` filters the rows; `enter` opens the selected binding's round
detail, `esc` goes back to it, `:` opens the command line (`:fleet`,
`:rounds [query]`, `:round <binding> [N]`), `?` lists every key, and `q`
quits at the root. The pane's five tabs:
- **plan** — the round's own plan file, first in the order (#183).
- **report** — that round's planner-bound report or question payload.
- **terminal** — recent live terminal output from the builder's log.
- **diff** — the captured git patch from the round.
- **log** — the formatted append-only round log, scoped to the round.

`[` and `]` step the round a tab reads, from round 1 through the binding's
current round -- every tab refetches for the new round. Stepping onto a
live binding's own open (not yet closed) round reads as prose, not an
error: the report and diff tabs say so ("round N is open; report arrives
when it closes", "diff is captured when round N closes") rather than
showing stale content.


### `relevo ui`'s `:rounds` view: every round, filtered and regrouped

`:rounds` opens a second view: every round relevo's database has recorded, live
or archived, as a grid. It is the same data `relevo history` reads, with the
query language as a filter line and the sums of `--by` above the rows. `esc`
returns to the fleet with the query intact; `relevo ui :rounds` opens here
directly, and `relevo ui :rounds harness:agy` opens it with a query.

The first line is the applied query and the regroup axis; under it a tiles
line — `rounds 57   cost $14.20 (3 unknown)   tokens 41.2M   halted 4 · exited
2   median 23m   bindings 12 · builders 3` — then the grid:

```
started           binding       rnd  builder                               outcome        commits  tree    gate   tokens   cost     duration
2026-09-20 22:01  persist       r5   claude/anthropic/sonnet               reported       +1       clean   pass   1.2M     $0.42    27m
```

`/` opens the query line, prefilled with the current query; `enter` applies it,
`esc` cancels, and a parse error is shown under the input with the previous
query kept. The grammar is exactly `relevo history -q`'s:

```
/ harness:agy outcome:halted since:30d
/ auth cost>1            (then b to regroup by builder)
/ by:day since:14d
```

`b` regroups by the next axis (binding, repo, feature, builder, harness,
provider, model, day, outcome); groups carry rounds, reported, halted, commits,
tokens, cost and the last round's date, and `enter` on a group expands its
rounds beneath it. `s` cycles the sort column for the level under the cursor
(rounds: started, cost, tokens, duration, commits; groups: cost, rounds,
halted, last) and `S` flips the direction; `r` re-queries now, and the fleet's
tick re-queries at most every 10 seconds while the screen is up. `enter` on a
round row opens that binding's round detail, live or archived. The query text
and the sort column are remembered in the machine database.

Below 140 columns the grid drops `gate`, below 120 `tree` and `commits`, below
100 `tokens`; `cost` always stays. A round's cost reads `unknown` as `?`, a
round with no usage at all as `-`; archived rounds are dim; the same filter
grammar is documented in full under `relevo history`.

### `relevo ui`'s archived bindings: every binding, not just today's

The fleet lists live bindings only. Every binding the database has ever
recorded stays reachable through `:rounds`: its grid carries archived rounds
too, and `enter` on one opens the same five tabs, reading the database instead
of files -- `terminal` renders the round's stored transcript rows and does not
follow a tail, since nothing about an archived round is still moving. A
database relevo cannot open shows `no database: <err>` as a sticky notice and
refuses `:rounds`; `relevo ui` never exits over it.

### Headless builders

A builder is a process relevo runs, one fresh process per round; each
`relevo send` starts the harness's non-interactive form -- `agy -p …`,
`claude -p …`, `opencode run …` -- in the binding's tree with the round's
prompt, writes the harness's output -- stdout and stderr both -- to
`~/.local/state/relevo/<name>/NNN-runner.jsonl` beside the round's plan and
report, and returns.
Those are the round's **open-round files**, in the binding's directory under
the state root. When the round closes, relevo seals them into the database in
the same transaction that closes the round -- the report text rides in the
planner's payload -- and removes them, so the directory afterwards holds only
the files of a round still open.
The daemon renders the stream as it grows -- one line per
tool call (`● Bash go test ./...`), its result with the first line of what it printed (`  ⎿ ok: ok  github.com/… 0.4s`, `  ⎿ error: …`),
the builder's text, any denied permission, and the final answer -- so
`relevo ui`'s terminal tab and `relevo status` show
the round live, about two seconds behind. Between rounds the tab keeps
the last round's rendered output. The `.jsonl` is the raw record;
relevo never reads it for meaning, and `relevo show <name> --round N
--transcript` renders it for a human. A round from an older relevo version
keeps its `NNN-builder.log`, and the readers show that instead. The process
exits when it has written the report, or when
it fails; between rounds a headless binding has no process and is idle, not
broken. The completion marker is the contract: a process that wrote its report and
created `NNN-done`, then exited non-zero, has done its job. A process that
exits with a report but no marker closes the round too, flagged `unmarked`;
one that exits with neither is the "exited without a report" case below.

What this means in practice:

- **No dialogs.** The process runs with stdin closed. If a harness needs
  permission prompts answered, put its `--dangerously-skip-permissions`/`--auto`
  extra arg in the candidates section.
- **No memory across rounds.** Every round is a fresh process. relevo plans
  already carry their own context (worktree table, conventions, "stop rather
  than improvise"); headless makes that a hard requirement.
- **`relevo status`** shows `builder  headless  <kind>  <idle|working|exited N>
  pid P since HH:MM` and the log's last three lines as `log` rows.
  `relevo ui`'s terminal tab shows the round's rendered output.
- **Exit without a report** is logged as an `exit` entry (exit code and the
  log's last 20 lines) and the daemon switches builders, up to `max_switches`
  (a switch caused by a rate-limit gate is not counted); then
  `NEEDS YOU`. The one exception is a builder whose
  supervisor died with the daemon itself (a systemd restart, `kill -9` of
  the process tree): relevo tells that apart from a real builder death and
  relaunches the same candidate on the same round instead, uncounted.
- **`done` and `unbind` stop the process** if a round is running. A stop that
  fails is reported, and the binding is still done or unbound. The round budget
  never kills anything: it flags `NEEDS YOU` and leaves the process alone.
- **`relevo gate`** on the provider mid-round kills the running process
  and starts the next candidate on the same round.
- **opencode 2.x** headless builders launch `run` with `--standalone` (#256):
  each headless round gets its own private server instead of the one
  `opencode serve --service` shared by every `opencode run` on that machine,
  so a kill, `relevo done`/`unbind`, a `relevo stop`, or a mid-round switch
  stops the agent for real -- before the fix, the client process died but the
  agent session kept running inside the shared service, still editing the
  worktree relevo had already switched away from. `relevo doctor` notes the shared
  service (and, when readable, its session count from opencode.db) whenever
  `~/.config/opencode/service.json` exists. They also pass `--thinking`, so
  the model's reasoning reaches the transcript.

**Scopes.** A local headless round runs in its own transient systemd scope
named `relevo-round-local-<binding>-<round>` (an owned remote binding uses its
owner's id where `local` sits). It is a *sibling* of `relevo.service`, not a
child: `systemctl --user restart relevo` -- what `make service` does -- leaves a
running round alone instead of killing it, so a restart no longer looks to
relevo like a builder that "exited without a report". With no `scope` block
configured this is the whole change: a local headless builder moves out of
`relevo.service`'s cgroup into its own scope, with no quota and no memory cap --
only its location, and with it restart survival. The gate, a consult and the
verify reviewer each get a scope from the same template too, with a unit name
that says what it is: `relevo-gate-local-<binding>-<round>`,
`relevo-consult-local-<binding>-<round>-<consult-id>` and
`relevo-verify-local-<binding>-<round>-<consult-id>` (an owned remote binding
uses its owner's id where `local` sits, exactly as for a round). The policy section's
top-level `scope` block configures the scope (`enabled`, `slice`, `cpu_weight`,
`cpu_quota`, `gate_cpu_quota`, `memory_max`, `tasks_max`); `gate_cpu_quota` is
the gate's own CPU ceiling, and defaults to `cpu_quota`. `serve.scope` replaces
that block entirely for served rounds, and `scope: {"enabled": false}` opts
out. `allowed_cpus` is a pool of cores (`"0-2"`), and each round is pinned to
one core from it: the gate runs on its round's core, while a consult and the
verify reviewer run on the whole pool. When every core is taken, a round runs
on the whole pool instead. Pinning needs `cpuset` delegated to your user
manager through a root drop-in on `user@.service`, and `relevo doctor` checks
this; if systemd refuses it, relevo logs one warning and runs unpinned. #314
measured a CPU-bound job pinned to one core using 10–18% less CPU time than the
same job left to float. Every scoped builder, gate, consult and verify reviewer
also gets `GOMAXPROCS` set to the CPUs its scope allows -- 1 for a pinned round,
`ceil(quota)` otherwise -- replacing any `GOMAXPROCS` the daemon inherited, such
as a shell-wide export; this affects Go processes only, and Go's `-p` and
`-parallel` follow it. On a host without a
usable systemd user manager relevo logs one warning and runs builders unscoped,
in `relevo.service`'s cgroup, exactly as before.

### Progress labels

relevo keeps a progress clock on every local binding with an open round and
labels what it sees. The labels are observations, never actions: relevo never
kills, nudges, switches or halts on them, and the round budget stays the only
automatic halt. Killing a stalled builder stays the human's decision -- `relevo
done`, `relevo unbind`, or `relevo stop`.

The signals are the working tree (its fingerprint is `HEAD` plus `git status
--porcelain`, hashed -- no diff, no snapshot) and the builder's output: the
builder's stream file (`NNN-runner.jsonl`) mtime. Each signal keeps the time it last changed, and the daemon samples at
most once every `progress_interval_ms` (default thirty seconds).

- **`stalled <age>`** -- no signal has moved for `stall_after_ms` (default
  fifteen minutes) while the round is open and the builder is not blocked. The
  label replaces `working` in `relevo status` and `relevo ui`; a stalled binding
  is still `ACTIVE` with `relevo wait` still waiting. The label clears when a
  signal moves again or the process exits, and the daemon fires one
  `builder_stalled` hook event per episode and none when it clears.
- **`exploring <age>`** -- the output or screen is changing but the tree has
  not for `explore_after_ms` (default twenty minutes). A label only: some plans
  are read-heavy, so it fires no hook event and no notification.
- **`stale <age>`** -- a `NEEDS YOU` binding has sat unacted for
  `stale_after_ms` (default four hours). The age is measured from the halt, or
  from the newest log entry when the binding has none. The word follows the
  state word in `relevo status`, joins the row in `relevo ui`, and puts the row
  first inside its attention group; the daemon fires one `binding_stale` hook
  event per episode.

A binding with no readable signal at all -- no git and no stream -- is never
labelled.

When a round stops -- exit without the marker or the round budget -- relevo
scans the builder's last output for the harness's
rate-limit text and records a `rate_limited` gate (`source relevo`, the
matched line) on a match, parsing the line's own reset time when it names
one (`Resets in 2h48m52s`, `resets 7pm`) or using `limit_gate_default_ms`
otherwise, then switches uncounted toward `max_switches`. `relevo
gate` still overrides; `relevo gate --clear` undoes a false positive.

### Remote builders: the server

`relevo serve` runs a remote-builder server: an HTTPS listener over enrolled clients and an autonomous daemon loop that runs headless builders on the server host without a local planner or GUI. It opens the one machine database: enrolled clients, the server's TLS key and certificate, its gates and every served binding are records in relevo.db. `--state` only says where the server keeps its bare repos, its temp files and its worktrees.

On a fresh server host, the first run looks like:
1. `relevo serve init --host <hostname>` generates a server private key and self-signed certificate, printing the SHA-256 fingerprint that clients pin.
2. Copy the fingerprint to share with clients.
3. Enrol each client's public key: `relevo serve enroll --label <client-label> --key "<public key line>"`.
4. Copy the service unit to `~/.config/systemd/user/relevo-serve.service` and enable it:
   ```
   cp dist/relevo-serve.service ~/.config/systemd/user/
   systemctl --user daemon-reload
   systemctl --user enable --now relevo-serve
   ```

On the server machine, the admin runs these on the server host. No `--state` is needed: an admin verb reads the running daemon's root from the database's `serve.daemon` record (an explicit `--state` still wins, and a stale record falls back to the default root with a note):

- `relevo serve status [--json]` prints active bindings across all owners as JSON (top-level keys `builders`, `last_contact` and `owners`), sorted by owner label.
- `relevo show <name> --owner <label|id>` prints one round's plan, report, diff, drift, log or transcript, with `relevo show`'s flags. Read-only, and it reads live bindings only: a non-live binding reads as "binding not found".
- `relevo show <name> --owner <label|id> --log` prints that owner's binding log, with `relevo show --log`'s `--round`, `--after`, `--json` and `--follow`. Read-only: `--owner` is an exact label or an exact client id, and nothing is stamped or created.
- `relevo serve clients` lists enrolled clients and their revocation status.
- `relevo serve gc --abandoned <duration>` prunes abandoned bindings whose last activity is older than the threshold by archiving them (running rounds are never touched).
- A **DONE** served binding is collected once its last round has been acked by the client, or after seven days without an ack: the daemon removes its worktree, deletes its branch and every `refs/relevo/<name>/*` ref in the owner's bare repo, and archives its record in the database (this cleanup lands in the server's next round). Every server unbind releases the binding's branch and refs as well. A bare repo is deleted once no live binding uses it, and the next bind of that repository recreates it.

What `relevo serve` does not do: it runs no planner and provides no administrative verbs over the network (administration happens on the server host). Tenants are protected from each other over the wire and from a passive network, but not from the server admin or from each other at the OS level where all builders run under the same unix user. Tenant isolation by unix user or container is tracked in #204.

### Remote builders: the client

A remote binding is an ordinary binding whose builder runs on someone else's
machine, over a signed, pinned HTTPS connection instead of a local process.
It has no worktree of its own: `relevo send` ships a bundle of your branch's
history alongside the plan, and the daemon polls the server for the round's
state the same way it polls a local builder.

Set up once per machine:
1. `relevo config server key` generates this client's ed25519 keypair (stored
   in relevo.db) and prints two lines: the client's id, and an enrollment line
   (`ed25519 <base64> <user>@<host>`) to hand the server admin.
2. The admin runs `relevo serve enroll --label <you> --key "<enrollment
   line>"` on the server, and shares back that server's certificate
   fingerprint (printed by `relevo serve init` there).
3. `relevo config server add <name> <url> --fingerprint sha256:<hex>` records
   the server and, having a fingerprint to pin the connection with, checks
   enrollment immediately: "enrolled as
   `<label>`", or "not enrolled on `<name>`: give the admin: `<enrollment
   line>`" if step 2 has not happened yet. `--ca system` trusts the system CA
   pool instead of pinning a fingerprint; `--insecure` allows plain HTTP, for
   a server reachable only over an already-trusted tunnel.
4. `relevo config server list` lists every configured server and this client's
   enrollment on each: `enrolled as <label>`, `not enrolled`, `unreachable`,
   or `cert changed` (the pinned fingerprint no longer matches -- a hard
   refusal the client never overrides silently).

Then, from any repository:
```
relevo bind --name api --server zen        # creates the server binding and
                                           # the local branch relevo/api, no worktree
relevo send --name api --file plan.md      # ships plan.md and a bundle of relevo/api
relevo status                              # round state comes from the server, polled
```

What comes back as `relevo/<name>`: the result of a closed round is fetched
into your repository's own `refs/heads/relevo/<name>` branch -- fast-forward
only, exactly like a local builder's worktree branch. If the round closed with
uncommitted changes on the server, they land on a side ref,
`refs/relevo/<name>/round-<N>`, whose parent is that round's commit on
`relevo/<name>`; the report names it. If that fast-forward collides with a
branch you have checked out locally, relevo retries quietly next tick --
check out something else, then `relevo wait`.

`relevo send` also ships your repository's tags as data beside the bundle, and
the server sets each one whose commit it already has, so a tagged server
worktree can `git describe --tags`. Catch-up fetches the builder's own stream
file (`NNN-runner.jsonl`) alongside the report, diff and log, so a remote
round's failure carries its detail to the client.

What is refused: `--cwd` cannot be combined with `--server` (a remote binding
is always created fresh, never bound to an existing directory); and
`relevo bind --resume --rebind` (or
`--candidate`) against a remote binding ("cannot change a remote
builder; unbind and re-create" -- a binding's mode is fixed at creation, the same
rule a headless binding follows). `relevo done` and `relevo unbind` tell the
server first, and only change anything locally once it agrees (a 404 from
the server is treated as already gone, and proceeds).

What `status` and `doctor` show: `relevo status` and `relevo ui` name a
remote binding's builder by its server (`zen`), with the
last round state the daemon observed there (`running`, `idle`, `closed`,
`needs_you`, `unreachable`, `cert`) in the status column -- read from the
store, never over the network, so it costs nothing extra. `relevo status`
and each `relevo wait` poll additionally sync every remote
binding first, so a round the server closed while your daemon was not
running (or was never started) is collected without it -- a laptop closed
overnight still shows the finished round on the next `relevo status`.
`relevo doctor` adds one row per configured server: reachable and enrolled
(`enrolled as <label>`), not yet enrolled (with the line to give the
admin), unreachable, or a certificate that no longer matches the pinned
fingerprint.

### Round budget

Each round carries a budget; past it, relevo flags the binding `NEEDS YOU` and
notifies once. It never kills anything — a builder working a real stage of a
plan runs for hours, so the budget is a runaway guard, not a progress estimate.
The default is 24 hours; `relevo bind --timeout 2h` sets it per binding.

A headless builder that exits without writing its report file still closes the
round, and relevo delivers its report flagged `unmarked`. Nothing is scraped and
nothing is guessed.

### Running several builders at once

`relevo bind` gives the planner one builder over the current tree. `relevo bind --worktree`
attaches more, each on its own git worktree, so they never contend for files:

```
relevo bind --candidate claude/anthropic/sonnet --name api
relevo bind --worktree --name frontend --candidate claude/anthropic/sonnet
relevo bind --worktree --name backend  --candidate opencode/openrouter/z-ai/glm-5.3-flash

relevo send --name frontend --file ui_plan.md
relevo send --name backend  --file api_plan.md
```

Each peer is an ordinary binding: its own round counter, round log, captured
diffs and budget. `relevo status` lists them all, and every verb that acts on a
binding takes `--name`.

Every mutating verb (`bind`, `send`, `done`, `unbind`) ends by
listing, on stderr, every *other* binding that is waiting on a human — a halt,
a dead builder, a lost planner — with how long and the verb that resolves it,
e.g. `waiting on you: api round 4 halted 23m -- relevo status --name api`. The
exit code is unchanged; it is a reminder, not a refusal.

Relevo does not sequence them and does not merge their trees. The planner
decides how many builders it needs, which run in parallel and which wait, and
integrates the results — relevo only carries plans out and reports back.

Headless builders are the cheap way to run several: no terminal per builder,
no idle harness holding memory. `relevo bind --worktree --name api` gives a peer its own
worktree and a fresh process per round.


### Stopping a round

`relevo stop` ends an open round on purpose. The builder runs with no stdin, so
there is nothing to ask it: `relevo stop` kills the process and closes the round
without a report (`noreport stopped`).

```
relevo stop webshop               # end the round now
```

- **A stop is not a failure**, so it never charges a switch and never excludes
  a candidate, unlike an exit-without-report.
- **`stop` then `done`** is the sequence for parking a binding between
  rounds: `stop` ends the round, and `done` then releases the worktree.
- **Send and the round close clear the request**, so a stop never outlives
  the round it was made for. `stop` refuses a binding that is already `DONE`
  or `PAUSED`.

A remote binding's round is stopped on its server, and the binding stays: on
both sides only the round ends. A round still queued on the server is dropped
from the queue instead, since there is no process to kill. A server that
predates this refuses the request with a pointer to `relevo unbind webshop`,
which stops the round and drops the binding.

### Cleaning up finished bindings

A binding's live state is a record in the database, not a directory of files.
While a round is open that round's files sit in `$XDG_STATE_HOME/relevo/<name>/`
(defaulting to `~/.local/state/relevo/<name>/`) -- its plan, report,
patch (`NNN-diff.patch`), captured dialog and builder stream -- and when the
round closes relevo seals them into the database and removes them.

`unread`/`●new` comes from the binding record's `viewed_at` stamp (#143):
`relevo show` (`--plan`, `--diff`, `--log`) each stamps it after a successful
print of a live binding, and `status` compares the binding's newest report
against it. `relevo ui` never writes it directly -- it stamps through the same
call `show` uses, keeping `ui` itself read-only. `relevo done` stops relaying and, when the binding's worktree is clean and
no round is open, removes the worktree so its branch can be checked out
in the main repo (`removed worktree ... (branch relevo/x is free to check
out)`); a dirty tree or an open round is kept and `relevo unbind --done` retries
when it is clean. The binding directory itself is never removed by `done`
— the log is the record of what the planner actually told the builder.
`relevo bind --resume <name>` puts a removed worktree back on the same
branch at the same path; a DONE binding may then be rebound with
`--rebind`, since the old builder cannot work in the recreated directory.

relevo deletes a branch in two places:
- `unbind --done` and `unbind --sweep`, for a `relevo/<name>` branch relevo created, only when its commit is on a remote-tracking ref;
- the existing `bind --server` rollback exception, which stays (a `relevo/<name>` branch created seconds earlier and undone because the server refused the binding).

A branch adopted with `--branch` is never deleted; `unbind`, `done` and a kept (dirty) worktree never delete anything.

```
relevo unbind ai              # delete the binding and its whole directory
relevo unbind ai --archive    # keep the binding's record, log and rounds in the database
relevo unbind --done          # archive this planner's DONE bindings
relevo unbind --done --delete # remove them instead
```

Archiving is the default because every other destruction decision in relevo keeps
by default. An archived binding keeps every row it had -- its rounds, its events,
its artifacts and its transcripts -- so `relevo history`, `relevo show` and the
ui's `all` scope still read it months later, and the name frees for a fresh
binding. Nothing is written to `.archive/` any more: a tarball an older relevo
left there is imported once, on the next read, and removed.

`unbind --done` only touches bindings the planner marked `DONE`. A `PAUSED` or a `BROKEN`
one is left alone: it still needs a
human, and clearing it would throw away the state that explains why it stopped,
while a paused binding is released but alive — `relevo bind --resume` brings it
back, and `relevo unbind` is how to clear it.

Snapshot tree objects created during round diff capture are written directly to
git's object database unreferenced. They never alter repository refs, branches,
or the working index, and they are reclaimed automatically by the repository's
own `git gc`.

**What a binding records.** Beyond its round history and live state, a fresh
`bind` fills in four more facts about the binding: which
repository it works in (the origin URL, normalised, and the git common
directory — best-effort, so a directory git can't read leaves this blank
rather than failing the command), the `--feature` label grouping it with
other bindings, which
binding and round it was forked from, and the planner's own harness
transcript file path, when relevo can locate one at bind time. None of this
changes what you see day to day; it exists for `relevo history`, the ui's
dashboard and the database below.

### The database

relevo keeps a pure-Go sqlite database at `$XDG_STATE_HOME/relevo/relevo.db`
(defaulting to `~/.local/state/relevo/relevo.db`), mode 0600, and it is the
record: the configuration sections and secrets, every binding with its round
log and rounds, gates, planner records, and every file a closed round produced
(artifact and transcript rows). Nothing else is a source of truth, and no verb
needs it closed.

The state root holds only what cannot be a row:

- `relevo.db`, with its `-wal` and `-shm`;
- `.daemon.lock` and the per-root `.lock`, the two flocks the daemon and every
  command take;
- `.worktrees/`, the git worktrees relevo creates;
- one directory per binding, holding the files of a round that is still open;
- on a server, `serve/repos/` (the bare repos), `serve/tmp/` (in-flight request
  bodies) and `serve/bindings/<owner>/` (one directory per owner).

Nothing else is used. A file dropped into `~/.config/relevo` is imported on the
next command and removed, and a `.archive/*.tar.gz` an older relevo left behind
is imported once and removed; after that neither path exists. `relevo doctor`
prints the database's `database` row: its path, its size, the schema version,
and the live and archived binding counts.

`relevo history` and `relevo show` read the database for anything that is not a
live binding's open round, so a round from months ago renders the same way a
live one does. See "relevo history" and "relevo show" below.

### relevo history

`relevo history` reads the database, not the filesystem: one line per round
across every binding relevo has ever recorded -- live or archived -- newest
first.

```
relevo history [--here|--repo <url|dir>]   filter to a repo: --here resolves the current directory's
                                           origin url (or its git common dir with no remote);
                                           --repo takes either form directly
              [--feature LABEL]           filter to a --feature label
              [--binding NAME]            filter to one binding name
              [--planner SESSION]         filter to one planner session id
              [--harness K] [--provider P] [--model M]
                                           filter to the round's builder columns
              [--candidate TOKEN]         filter to one harness/provider/model token
              [--outcome O]               filter to one round outcome: reported, halted, exited,
                                           switched, done_no_report, open
              [--since D] [--until D]     only rounds started in this window: 24h, 7d, or YYYY-MM-DD
              [--archived|--live]         archived bindings only, or live bindings only (default: both)
              [--limit N]                 max rows to print; 0 = all (default 200)
              [--json]                    a JSON array of RoundRow, `[]` when empty
              [-q "<query>"]              filter with the query language below
              [--by <axis>]               regroup the result: none, binding, repo, feature, builder,
                                           harness, provider, model, day, outcome
              [--rows]                    with --json --by, include each group's Rows
```

Two switches turn the same query into an aggregate instead of a
row-per-round list:

- `--tab` — tokens and cost across bindings, archived ones included (see
  "Round usage" below).
- `--stats` — rounds, outcomes, switches, gate results and consults across
  bindings, archived ones included, with the last 30 days of provider blocks
  (see "Usage stats" below).

Every flag is one field of the shared `Filter` the ui's coming `all` scope
and dashboard query too. `--archived` and `--live` together, and an
`--outcome` outside the enum above, are usage errors. A plain line looks
like:

```
2026-09-15 14:02  api-auth      r3  agy/antigravity/opus                      reported        +2 commits  clean  $0.42  (archived)
```

started time in the local zone; the binding name, truncated with `…` past
12 characters; the round; the builder candidate; the outcome; commits
(`-` when unknown); the worktree's tree state at close; cost (`$0.42`
measured, `~$0.42` estimated, `unknown`, or `-` when the round recorded no
usage at all); `(archived)` for a round from a binding `unbind --done` has archived.

`-q` takes the query language the dashboard's filter line shares
(`docs/specs/2026-09-21-dashboard-design.md` §3):

```
query  := token*                              whitespace separated
token  := key ":" value                       equality; "quoted" for spaces
        | numkey op number                    op in > < >= <= = 
        | word                                case-insensitive substring of binding, repo or feature
key    := binding repo feature planner harness provider model candidate outcome
          report state gate basis server mode since until archived by
numkey := cost tokens commits duration round
values : outcome reported|halted|exited|switched|done_no_report|open
         report  done|halted|blocked|deferred|unstructured
         gate    pass|fail|timeout|error
         basis   measured|estimated|unknown
         mode    pane|headless|remote   (pane: history only)
         archived true|false
         since/until 24h|7d|YYYY-MM-DD
         by      none|binding|repo|feature|builder|harness|provider|model|day|outcome
         duration minutes; tokens = in+cache+write+out; cost in USD
```

`by:` (or `--by`) regroups the result instead of printing one line per
round: one row per axis value with rounds, reported, halted, commits,
tokens, cost and the last round's date. A flag overrides the same key in
`-q` and prints `note: --<flag> overrides <key>:<value> from -q` on stderr.
An unknown key, a bad enum value or a bad number is a usage error (exit 2).

```
$ relevo history -q "harness:agy outcome:halted since:30d"
$ relevo history -q "auth cost>1" --by builder
$ relevo history --by day --since 14d
```

### relevo show

`relevo show` prints one round's plan, report, diff, drift, log or
transcript. A live binding is read straight from its files, exactly as
today; anything not live -- an archived binding, or one this machine's
database otherwise knows about -- is read from the database instead, so a
round from months ago renders the same way a live one does.

```
relevo show <name> [--round N]                      the round to read; default: the newest completed one
                   [--plan|--report|--diff|--drift|--log|--transcript]
                                                     which section; default: --plan; only one may be given
                   [--json]                         the ShowResult as JSON (Events included for --log)
```

A header line goes to stderr -- `<name> round <N> of <Rounds> · <section>`,
with `· archived <date>` appended for a non-live binding -- so stdout is
always just the section itself and safe to pipe. A round with no such
section (an open round with no diff yet, say) prints `no <section> for
round N` and exits 0 rather than erroring. `--log` prints the round's
events; `--transcript` prints the builder's
rendered stream. Reading `--round N` outside the binding's round count, or
naming a binding neither live nor in the database, exits 1:

```
$ relevo show api-auth --report
api-auth round 3 of 4 · report
report text here
```

### done and unbind are the destructive verbs

`relevo done` and `relevo unbind` both require a binding name (`relevo done ai`, or
`--name ai`) or `--pick`, which lists the bindings and runs the verb on the one
you choose. Neither resolves the current directory for you: a bare `relevo done` once ended a live
loop by accident, and the recovery is `relevo bind --resume --name <name>`.
`--pick` is explicit for the same reason -- a bare verb never opens a picker,
so a planner agent can never fall into one.
And because a popup takes focus the instant it opens, `Enter` on a binding
that is not `DONE` asks first -- `mark webshop done? it is ACTIVE in round 5`
-- and only `y` proceeds; any other key returns to the list.

## Status line

`relevo status --line` shows this planner's live bindings, one row each, under
the Claude Code prompt; it shows nothing on error and never probes a builder.
The first line names the planner (`planner architect-14`), so each terminal
shows which planner it is; `relevo planner list` maps that name to its chat.
Each row shows the round's harness (`harness@server` for a remote builder),
what it is waiting on, this round's tokens, and the round's length: ticking while
it runs, frozen once the report is in.

Add this to `~/.claude/settings.json`:

```json
"statusLine": { "type": "command", "command": "relevo status --line", "refreshInterval": 1 }
```

One precondition: `relevo` must be on the `PATH` of the Claude Code process.
The planner session is identified by `RELEVO_PLANNER`, which the relevo plugin's
hook exports.

Claude Code renders a few cells less than `COLUMNS`; relevo subtracts 4 by
default (measured on the fullscreen TUI). If the right-hand round clock (with
a state word only when it is not ACTIVE) is clipped or sits short of the edge,
measure yours and set `RELEVO_STATUSLINE_MARGIN` in the environment Claude Code
starts from. To measure, put this in `statusLine.command` for one refresh and
count the cells before Claude Code's `…`:

    sh -c 'printf "%s" "$(seq -s . 1 $COLUMNS | cut -c1-$COLUMNS)"'

Each row is `○ name  rN · builder · what relevo is waiting on  …  age · STATE`,
where `builder` is the harness segment of the candidate token, and `age` is
time since the last plan, report or question crossed.

## Candidates

A candidate is one way to run an actor, named by the token `harness/provider/model`. `harness` and `provider` are single segments; `model` is the rest, so `opencode/openrouter/z-ai/glm-5.3-flash` is one token. **relevo ships no candidates**: which model you are entitled to run is a fact about your accounts, not about relevo.

Candidates are the `candidates` section of relevo.db, a JSON array you read
and write with `relevo config`: show it with `relevo config get candidates`, set
the whole array with `relevo config set candidates '[…]'`, or edit the document
with `relevo config edit`. The array is:

```json
[
  {
    "harness":  "claude",
    "provider": "anthropic",
    "model":    "sonnet"
  },
  {
    "harness":  "opencode",
    "provider": "openrouter",
    "model":    "z-ai/glm-5.3-flash",
    "extra_args": ["--auto"]
  },
  {
    "harness":    "agy",
    "provider":   "google",
    "model":      "gemini-3.8-flash-high",
    "extra_args": ["--dangerously-skip-permissions"]
  }
]
```

- `harness` — a kind relevo knows: `agy`, `claude`, `opencode`, `codex`. Required.
- `provider` — who enforces the quota; free text. Required.
- `model` — passed to the harness as-is. Required.
- `tree` — `binding` (the default) or `none`.
- `extra_args` — appended verbatim after what relevo renders.
- `tier` — default permission tier for this candidate: `harness`, `read`, `edit`, `yolo`. Optional; defaults to the actor's tier, else `harness`.
- `denial_patterns` — regexes that replace the harness default denial patterns for this candidate when detecting permission-blocked exits. Optional.
- `limit_patterns` — extra regexes, appended to the harness defaults, for the text this candidate's provider prints when it closes a session on quota. Extend-only; a default that misfires is a bug to report.
- `dialog_patterns` — extra regexes appended to the harness defaults, matched against a runner's output to detect a blocking dialog; a match refuses `send` as blocked; extend-only.

A section that does not validate is refused with a message naming the entry; an absent section is zero candidates.

| actor | shape | agent |
| --- | --- | --- |
| `builder` | writer | `plan-executor` |
| `reviewer` | reader | `reviewer` |
| `researcher` | reader | `researcher` |

An actor is relevo's name for a job; its agent is the harness definition `relevo config agents` installs.

### How relevo launches one

| kind | args |
| --- | --- |
| `agy` | `--model <model> --agent <agent>` |
| `claude` | `--model <model> --agent <agent>` |
| `opencode` | `--agent <agent> -m <provider>/<model>` |
| `codex` | `-p <agent> -m <id> -c model_provider=<provider> [-c model_reasoning_effort=<effort>]` |

For `codex` the candidate's `model` is `<id>[:<effort>]`: `gpt-5.6-terra:high` runs `-m gpt-5.6-terra -c model_reasoning_effort=high`, and the suffix stays in the token so two efforts are two candidates.

For `claude` the `model` may end in `:low|medium|high|xhigh|max`: `opus:medium` runs `--model opus --effort medium`, and any other suffix stays part of the model id, so a Bedrock id such as `...-v1:0` passes through whole. For `opencode` the effort is a variant, `model#<variant>`, passed as is to `opencode run -m`: variants are per model, and opencode refuses an unknown one.

Under `workspace-write`, codex also cannot write to Go's default build cache (`~/.cache/go-build`), so a Go plan fails at `go build` unless the plan sets `GOCACHE` inside the worktree or `/tmp`, or your `~/.codex/config.toml` lists it under `sandbox_workspace_write.writable_roots`. relevo adds only its own state directory.

Any `extra_args` are appended verbatim after what relevo renders. Because relevo renders the argv, the token in `relevo status` is exactly what was started.

> **Note on `--dangerously-skip-permissions`:** it lets the builder act without approval prompts, which makes an unattended relevo loop work, but it is a real grant of trust. It is an `extra_args` entry you add once you have watched a few rounds and trust the loop with that tree; relevo never adds it.

### Choosing a candidate

Pass the token to `relevo bind --candidate claude/anthropic/sonnet` and relevo
starts exactly that, gated or not (with a `note:` on stderr if it is).

With `--candidate` omitted, relevo decides, by one rule:

- exactly one configured candidate serves the actor → that one, unless it
  is gated;
- several serve it and the actor's `candidates` list orders them (see
  [Actors and agents](#actors-and-agents)) → the first in that list that is not
  gated or `off`, then any serving candidate the list does not name, in token
  order;
- several serve it and the actor lists none → relevo refuses and says so.
  Name one, or add it to the actor.

When every candidate serving the actor is gated or `off`, relevo refuses and
says why each one is; an explicit `--candidate` still bypasses that. The same
rule applies to `relevo bind --worktree`.

Every choice is written down. `bind` and `send` print one
line saying what was picked and why, and the same line lands in the
binding's log as a `pick` entry, so `relevo show --log` shows it later:

```
picked claude/anthropic/sonnet for builder: order #2; skipped agy/google/gemini-3.8-flash-high (rate-limited until 20:28)
```

`relevo config` prints the actors, the pick per actor and the candidates.

### Policy

The `policy` section of relevo.db holds the tuning knobs shared by every
binding and the permission ceiling. Read it with `relevo config get policy`
and change it with `relevo config set policy.<key> <json>` or `relevo config
edit`:

```json
{
  "max_tier": "edit",
  "max_switches": 2,
  "limit_gate_default_ms": 3600000,
  "stall_after_ms": 900000,
  "progress_interval_ms": 30000,
  "explore_after_ms": 1200000,
  "stale_after_ms": 14400000,
  "scan_patterns": ["(?i)<instruction-tag"],
  "classify": { "provider": "jev", "model": "jev-latest", "injection_threshold": 0.7, "timeout_ms": 4000 },
  "gate": { "default": "make check", "timeout_ms": 600000, "regate": 0 }
}
```

Who runs what is not here: an actor's `candidates` list is its order and its
`tier` is its permission tier (see [Actors and agents](#actors-and-agents)).
`max_tier` bounds permission autonomy across all commands (`read`, `edit`, `yolo`),
defaulting to `edit`; commands requesting a tier above `max_tier` require
`--allow-yolo` or raising `max_tier`. `max_switches` bounds
how many times the daemon may replace a builder mid-round before the
binding goes `NEEDS YOU`; absent defaults to 2, `0` turns switching off.
`limit_gate_default_ms` is how long a rate limit relevo detects itself
gates the provider when the matched line names no reset time; absent
defaults to one hour. `stall_after_ms` is how long a live headless
builder's stream may go without an event before `relevo status` and
`relevo ui` label it `stalled`; absent defaults to fifteen minutes, and
must be `> 0` when present. `progress_interval_ms` is how often the
daemon samples a binding's progress signals while its round is open;
absent defaults to thirty seconds, and must be `> 0` when present.
`explore_after_ms` is how long a builder's output or screen may keep
moving while its tree has not before relevo labels it `exploring`; absent
defaults to twenty minutes, and must be `> 0` when present.
`stale_after_ms` is how long a `NEEDS YOU` binding may sit
unacted before relevo labels it `stale`; absent defaults to four hours,
and must be `> 0` when present. `scan_patterns` is an optional list of extra
regular expressions appended to relevo's built-in instruction-shaped scan list;
each pattern must compile. `gate` configures the default acceptance command
(see [Gate](#gate) below): `default` is the command a binding gets when it
does not set `--gate` or `--no-gate` itself, absent or `""` meaning no gate;
`timeout_ms` bounds one gate run, absent defaulting to ten minutes, and must
be `> 0` when present. `regate` is how many automatic repair rounds a new
binding may open after a failing gate (see [Repair
rounds](#repair-rounds) below), absent or `0` meaning none, and must be
`>= 0` when present.

`classify` configures an optional classifier (TypeSafe's Jev model) to run
beside the regex scan. When absent, relevo scans with regexes only. The block
requires `"provider": "jev"`; `model` defaults to `"jev-latest"`,
`injection_threshold` defaults to `0.7`, and `timeout_ms` defaults to `4000`.
The API key is read from the `TYPESAFE_API_KEY` environment variable or from
the `typesafe` secret (`relevo config secret set typesafe` reads the value from
stdin). The key is stripped from every builder's environment so that agents
running arbitrary plans never inherit relevo's own secrets. The secret exists
because the daemon runs as a systemd user unit that inherits no login
environment (`systemctl --user set-environment TYPESAFE_API_KEY=...` also
works). `relevo doctor` reports which key source was found or warns if neither
is set. In `relevo log`, an entry like
`flagged=3 by=both p=0.94` records the de-duplicated union of regex-hit lines
and classifier paragraphs at or above the threshold, which judge flagged the
content (`regex`, `jev`, or `both`), and the maximum probability seen across
all paragraphs. The 0.7 threshold is provisional pending `make jev`. Model
output is never altered and delivery is never held: a high probability flags the
entry for the planner to see, but never halts delivery.

`relevo config` shows what relevo would do right now:

```
builder  (config actors)
  1  gemini-3.8-flash-high    order     rate-limited until 20:28
  2  sonnet                   order     <- would pick
  3  glm-5.3-flash            order     limited 2x around 14:00 (30d)
reviewer  (config actors)
  1  opus                     sole      <- would pick
```

The marker is computed by the same code `bind` runs, so it cannot
disagree with what `bind` does next. Change an actor's order by editing its
`candidates` list (`relevo config set actors.builder.candidates '["…"]'` or
`relevo config edit`). The rest of #61 --
scoring for unordered actors, peak windows, mid-round switching -- will add keys
to the policy section as it lands.

### Actors and agents

An **agent** is what does the work: a named agent definition. An **actor** is
who runs it -- the agent plus an ordered list of candidates, a tier, and, for a
writer, whether its round closes on a gate.

- A **shipped** agent is one relevo renders and installs: `plan-executor`,
  `reviewer`, `researcher` and `architect`. It needs no `agents` entry -- an
  actor names it directly.
- A **custom** agent is one you write, carried in the `agents` section as its
  `source` text (an `agentsrc` definition; its `name` must equal its key).
- A **native** agent points at a harness definition you already have: it names
  the `agent` and `requires` per kind, plus its `shape`. Nothing is rendered or
  installed for it.

| shipped agent | shape | output | requires |
| --- | --- | --- | --- |
| `plan-executor` | writer | `report` | `researcher` |
| `reviewer` | reader | `findings` | -- |
| `researcher` | reader | `notes` | -- |
| `architect` | reader | `plan` | -- |

Agents and actors are the `agents` and `actors` sections of relevo.db; read
them with `relevo config get agents` and `relevo config get actors`, and change
them with `relevo config edit`. Both blocks below are copied from a real
`relevo config export`:

```json
"agents": {
  "my-exec": {
    "native": {
      "claude": {
        "agent": "my-exec",
        "requires": [
          "my-scout"
        ]
      }
    },
    "shape": "writer"
  }
}
```

```json
"actors": {
  "builder": {
    "agent": "plan-executor",
    "candidates": [
      "sonnet",
      {
        "candidate": "glm-5.3-flash",
        "off": true
      }
    ],
    "tier": "yolo"
  }
}
```

Agent entries:

- `source` -- the agent definition's text. relevo renders and installs it.
  Exactly one of `source` and `native` is set.
- `native` -- kind to `{"agent": "...", "requires": ["..."]}`, for a
  definition the harness already has. The agent and requires names follow the
  same shape as an agent key (`^[a-z0-9][a-z0-9._-]{0,63}$`).
- `shape` -- `writer` or `reader`. Required with `native`; forbidden with
  `source`, which carries its own.

Actor entries:

- `agent` -- required. A shipped agent's name, a key in `agents`, or a native
  harness definition's name.
- `candidates` -- the actor's candidate tokens, most preferred first. A bare
  string is **on**; `{"candidate": "...", "off": true}` keeps the entry in its
  place but the pick skips it unless you name it explicitly. relevo prints
  `off` in the pick block and a note if it runs an off candidate you named.
- `tier` -- the actor's permission tier.
- `check` -- writers only: whether a round closes on a gate; defaults to true.
  An actor whose agent is a reader must not set it.

An actor named `builder`, `reviewer` or `researcher` keeps that builtin's
shape: `builder` must run a writer agent, `reviewer` and `researcher` a reader.
An unknown agent, a reader with `check`, or a builtin actor with the wrong
shape is refused when the config loads.

- A new **reader** actor runs as a binding's actor too: `relevo bind
  --worktree --actor <name>` or `relevo bind --actor <name>`, then `relevo
  send` hands it a plan. Its round writes `NNN-<actor>/summary.md` and any
  files it produced. A new **writer** actor runs a round the same way. Every
  round of that binding runs it.

With `relevo bind --worktree --server S --actor <r>`, the server resolves `<r>`
against **its own** actors section, and your local sections do not travel. A
server too old to run custom actors refuses the add.

**Custom definitions.** A definition is *custom* when its name -- or a name it
requires -- is not one relevo ships. relevo never installs or refreshes a custom
definition; each harness looks for it in its own place:

- claude: `~/.claude/agents/<n>.md`
- opencode: `~/.config/opencode/agents/<n>.md`
- agy: `~/.gemini/config/agents/<n>.md`
- codex: `~/.codex/<n>.config.toml`

A missing custom definition gates its candidates for that actor only, and
`relevo doctor` lists it.

**Bring your own agent.** Everything relevo's round protocol needs travels in
the prompt relevo sends: the working tree and its `git status` check, the plan
path, the report path, the done marker and the closing `relevo` block. A custom
definition only shapes behaviour; `requires` names the definitions your agent
dispatches to (the shipped builder requires `researcher`).

**Migrating.** A config with no actors section still reads its legacy `roles`
section, or the candidates' `roles`/`tier` and the policy's `order`/`tier`, and
keeps working. The first load after the update migrates it in one pass: it
writes `agents` (when an actor needs a custom or native definition) and `actors`,
drops the legacy fields, and records one revision whose source is `migration`.
`relevo config log` lists that revision and `relevo config rollback <rev>`
restores the pre-migration document. Once actors exist the legacy keys are
ignored, and relevo warns about any that remain.

**Seeing it.** `relevo config` shows the actors block, then the pick per actor
labelled `(config actors)`, then the candidates; `relevo status --json` has
`agent_definition` for a custom runner. `relevo status` shows `actor <r>` on
the runner line, and `status --json` has `actor`.

**Remote builders.** A server resolves actors from its *own* config, not the
client's.

### Availability

relevo keeps a ledger of when a candidate could not be used: spawn failures it
observed itself, rate limits you report. It shows the ledger, and an omitted
`--candidate` skips what the ledger gates (see [Choosing a
candidate](#choosing-a-candidate)). It is a table in the database, not a file.

Report a limit with:

```
relevo gate claude/anthropic/sonnet --reason "5-hour window"
relevo gate claude/anthropic/sonnet --for 2h
relevo gate --clear anthropic
```

A limit gates the **provider** (every candidate with `provider: anthropic`),
because that is who enforces the quota, not the model. Without `--for` it
stays gated until you run `relevo gate --clear`, because relevo does not know
your provider's reset schedule.

`relevo gate --clear` also clears the gate on every server your bindings name
and prints each server's answer; on a box running `relevo serve`, use `relevo
gate --serve --clear`.

Spawn failures need no command: relevo records one itself when starting an
agent fails, gating that one candidate for ten minutes, and it expires on
its own.

Where it shows: `relevo status` gains a `candidates` block only while
something is gated; `relevo config` marks gated rows `unavailable:`;
`relevo doctor` warns per gated candidate with the command that clears it.
`bind`/`send` with an explicit token print a `note:` on
stderr when the candidate is gated and **proceed** -- you named it. With
the token omitted they skip gated candidates and refuse when nothing
ungated serves the actor.

A candidate whose harness agent files are missing on disk is gated the same
way (`roles missing` in `relevo config` and `relevo
doctor`), fixed with `relevo config agents --kind <kind>` -- except an
explicit `--candidate` pick of it is **refused**, not allowed to proceed,
because it cannot succeed. `relevo serve` logs each configured harness
kind's agent coverage once at startup.

#### Mid-round switching

A builder relevo spawned can be replaced by the daemon while a round is
open, in two cases:

- its process exits without writing a report;
- you gate its provider with `relevo gate` -- which is how you
  tell relevo a running builder hit its limit. The command names the
  bindings the daemon will switch.

The daemon resolves `builder` again through the builder actor's candidate list
and the ledger (an omitted token, so the actor's order applies even to a builder
you named), starts the pick in the **same** tree, and hands it the **same**
round's plan. The round number does not change; the round clock
restarts. The new builder inherits whatever the old one left in the
tree. A `switch` entry in the log says what was tried and why:

```
switched builder (rate-limited: 5h window): picked opencode/openrouter/z-ai/glm-5.3-flash for builder: order #3; skipped claude/anthropic/sonnet (rate-limited until cleared)
```

`relevo status` shows `switched 1x` on the builder line. After
`max_switches` replacements in one round (default 2; set it in
the policy section, `0` turns switching off), or when nothing ungated
serves `builder`, the binding goes `NEEDS YOU` with the reason, and
recovers on its own once `relevo gate --clear` clears a provider. A
failed replacement spawn counts as a switch and the daemon walks to
the next candidate. The halt always states its reason, even on a round
that has already notified once; a `relevo send` re-send resets the
round's switch budget, since the human asked for another attempt.

A builder gone between rounds is `BROKEN` as before: `relevo bind --resume`
starts a fresh one. relevo selects an agent for every builder it starts, with
`--agent` on the launch line.

`aliases.json` from earlier versions is no longer read, and neither is anything else an older relevo kept in `~/.config/relevo`.

#### History

Every gate relevo records -- a limit you report, a spawn failure it hit
-- is also kept for 30 days in the database's gate history, by
provider and local hour. `relevo config` shows it twice: a `limited 3x
around 21:00 (30d)` note on a candidate whose provider was limited
within an hour of now, and a `history` block with a 24-hour row per
provider. It changes nothing about which candidate is picked; it is the
cue to write a different order, or to `relevo gate <provider>` a provider
before it bites. Older installs are migrated on first read.

## Permission tiers

relevo defines four permission tiers that control how autonomously agents may use tools and make changes:

- `harness` — relevo passes no permission flags to the harness; the harness CLI defaults or configuration files decide (relevo default). Outside the tier order.
- `read` — read-only inspection and planning; write operations are denied.
- `edit` — file editing and standard workspace modification commands permitted.
- `yolo` — full autonomy; interactive permission prompts and confirmation dialogs bypassed.

The flags rendered for each harness kind (verified 2026-09-19 on claude 2.1.278, agy 1.2.7, opencode 2.0.8, codex 0.155.1):

| kind | harness | read | edit | yolo |
|---|---|---|---|---|
| claude | (none) | `--permission-mode plan` | `--permission-mode acceptEdits` | `--dangerously-skip-permissions` |
| agy | (none) | `--mode plan` | `--mode accept-edits` | `--dangerously-skip-permissions` |
| opencode | (none) | refuse | refuse | `--auto` |
| codex | (none) | refuse | `-s workspace-write -c sandbox_workspace_write.writable_roots=["<binding state dir>"]` | `--dangerously-bypass-approvals-and-sandbox` |

opencode does not support `read` or `edit` tiers because it has no read-only or edit-only CLI flag. Choosing `read` or `edit` for an opencode candidate is refused immediately with an error directing you to use `--tier harness` (where `opencode.jsonc` decides) or `--tier yolo` (`--auto`).

codex does not support the `read` tier: `-s read-only` cannot write the report, marker, question and findings files relevo stages under `~/.local/state/relevo/<binding>/`, and codex ignores `writable_roots` under read-only. Choosing `read` for a codex candidate is refused with an error directing you to `--tier edit` or `--tier harness`. At `edit` relevo adds the binding's state directory as a writable root; that is the only path outside the worktree the sandbox lets the builder write.

### Ceiling semantics and ordering

Tiers are ordered as `read < edit < yolo`. `harness` is outside this hierarchy and is never compared as above or below other tiers.

`max_tier` in the policy section defines the permission ceiling across all commands, defaulting to `edit`. Any command requesting a tier above `max_tier` (such as `yolo` under default policy) is refused unless:
- The command includes `--allow-yolo` on the command line (e.g. `relevo bind --tier yolo --allow-yolo` or `relevo send --tier yolo --allow-yolo`), or
- `max_tier` is explicitly raised to `"yolo"` in the policy section.

`max_tier` cannot be set to `"harness"` because `"harness"` is outside the rank order and does not represent a ceiling.

### Resolution chain

When starting an agent, relevo resolves the permission tier through a precedence chain:
1. Explicit CLI flag: `--tier <tier>` passed to `bind` or `send`.
2. Candidate configuration: `"tier"` set on the candidate in the candidates section.
3. Actor configuration: `"tier"` set on the actor in the actors section.
4. Fallback default: `harness`.

The result is then capped by the policy's `max_tier`.

For the verify reviewer, tier resolves from the policy section's `tier.reviewer` when set, else the candidate's `tier`, else `harness` -- and then capped by `max_tier`. The reviewer accepts no `--tier` flag, and a reviewer requesting `yolo` requires `max_tier: "yolo"` in the policy section since `send --verify` has no `--allow-yolo` flag.

### Per-round tiers

Every builder runs a new process for each round, so a round may temporarily override the tier with `relevo send --tier <tier> [--allow-yolo]`. The override applies to that round only, and resets to the binding's default tier when the round completes.

`relevo send --candidate <token>` moves the binding to another configured candidate from this round on. It is refused while a round is open (stop it first with `relevo stop`). An explicit pick of a gated candidate is recorded and proceeds, as with `relevo bind --worktree --candidate`. The binding's tier is re-derived for the new candidate. On a remote binding the server must advertise the `builder` feature.

### Permission-blocked exits

When a headless builder exits without producing a report file, relevo inspects the tail of its stdout/stderr log against the harness's default denial patterns (or the candidate's `denial_patterns` if configured).

If a permission denial pattern matches (for example, if a tool was refused because the agent attempted an edit while in `read` mode, or executed a command without required approvals):
- The binding halts and transitions to `NEEDS YOU`.
- The halt message quotes the matching denial line and instructs the planner to re-send with a higher tier or adjust harness allow lists.
- The ledger records an exit entry with note suffix `; permission-blocked: <line>`.
- relevo does **not** switch to another candidate (which would waste quota on a configuration error) and does **not** record a rate-limit gate.

To recover from a permission-blocked halt, adjust the tier (e.g. `relevo send --tier edit` or `relevo send --tier yolo --allow-yolo`) or update permissions, then re-send the plan.

### Migration from extra_args

Previously, permission bypass flags were often passed via `extra_args` in the candidates section (such as `"--dangerously-skip-permissions"` or `"--auto"`).

When any explicit tier (`read`, `edit`, `yolo`) is active, relevo validates that `extra_args` does not contain conflicting permission flags (e.g. `--permission-mode`, `--dangerously-skip-permissions`, `--mode`, `--auto`). If detected, relevo refuses to launch.

To migrate:
- Move `--dangerously-skip-permissions` (or `--auto`) from `extra_args` to `"tier": "yolo"` on the candidate in the candidates section, and set `"max_tier": "yolo"` in the policy section (or use `--allow-yolo` on CLI commands).
- Alternatively, leave `tier` unset (or set to `"harness"`), and relevo will leave `extra_args` untouched.

## Gate

A binding can carry a **gate command**: a shell line relevo runs in the
worktree the instant the builder's completion marker appears, against the
tree exactly as the builder left it, the same moment the round's diff is
captured. The gate never decides anything -- it annotates. The round still
closes on the marker, the report is still delivered, and the human still
judges the diff; the gate only adds a `gate=<result>` note and a `Gate:`
line to the payload, plus a structured record on the report's log entry.

While the gate runs, the round is held: nothing else acts on the
builder -- no nudge, no "exited without a report" handling, no round-timeout
halt -- until the gate finishes or times out.

Configure it with `--gate '<cmd>'` on `relevo bind` or `relevo bind
--worktree`; `--no-gate` opts a binding out of the policy section's `gate.default`
(see above) even when one is configured machine-wide. Omitting both flags
on `bind` falls back to `gate.default`, `""` meaning no gate at all --
bindings written before this feature have no gate and are unaffected.

The gate's full output -- and the supervisor's exit trailer -- lives at
`NNN-gate.log` next to the round's plan and report. Its result is one of
`pass`, `fail`, `timeout`, or `error` (the last for a gate that could not
start, or whose exit code could not be read):

```
Gate: make check -- PASS (exit 0, 1m40s). Output: /path/to/003-gate.log
```

A failing gate's payload line also carries the last few non-empty lines of
the log, so the planner sees why without opening the file:

```
Gate: make check -- FAIL (exit 2, 1m40s). Output: /path/to/003-gate.log
  ok  	github.com/example/pkg	0.01s
  FAIL	github.com/example/pkg2	0.02s
  ...
```

`relevo status` shows `gating <age>` in place of the builder's own status
while the gate is running, and `relevo show --log` appends ` gate=<result>` to the
round's report entry.

A round's report entry also carries its **builder session**, `builder_session`
in the JSON that `relevo show --log --json` and `relevo show --json` print: the harness
session that built the closed round, so a report read two rounds later can
still name the session that wrote it. It is the session the round's stream
announced in its first event. It is absent when the harness named none -- relevo
never guesses. The one-line form appends ` session=<kind>:<id8>`.

### Verify

`relevo send --verify` -- or the policy section's `"verify": {"default": true}` -- marks
the round: when it closes, **after the gate** so the reviewer sees the gate's
own output, relevo runs a read-only **reviewer** over the finished round and
records its verdict. `--no-verify` overrides the policy default for one send;
the two flags are exclusive.

The reviewer is a headless, one-shot consult started at round close. Its
findings are its final message rather than a file, and it runs in a **throwaway
worktree**: relevo creates a detached
worktree at the builder's HEAD under
`~/.local/state/relevo/.worktrees/.verify/<name>-<NNN>`, launches the reviewer
there, and removes the tree once the consult reaches any terminal state. That
isolation is what lets the reviewer run tests without touching the builder's
tree or the planner's checkout, and it is why the reviewer consult's tier is
the policy section's `tier.reviewer` when set, else the candidate's, else **yolo** --
for this consult only, in this tree only. The reviewer's agent definition still
tells it not to edit; relevo cannot observe writes.

The question names the round's plan, report, diff and gate log, and asks the
reviewer to end its findings with exactly this block:

```
verdict: accepted | rejected
reasons: ["..."]
```

relevo parses the last ` ```relevo ` block for those two lines. Anything else --
no block, an unreadable one, a verdict that is neither word -- is recorded as
`unstructured` and delivered as prose for the planner to read. `verdict` and
`reasons` ride on the findings entry, and the newest verdict shows in
`relevo status` as `verdict: rejected (2 reasons)` for as long as it judges the
round just closed.

**A verdict decides nothing.** `rejected` does not reopen the round, stop the
binding, or summon a human: the report is delivered exactly as it always was,
and the human still judges. `relevo wait --verdict` is a follow-up.

A round whose reviewer could not be started -- no reviewer candidate, every one
of them gated, no runner, a worktree that could not be created -- closes
normally with one `verify skipped: <why>` note in the log. A crashed relevo can
leave a tree under `.worktrees/.verify/`; remove it with
`git worktree remove <path>`.

### Repair rounds

A failing gate does nothing on its own: the round closes, the report goes to
the planner, and a human judges the diff. A binding can opt into a **repair
round** instead, with `--regate N` on `relevo bind`
or `relevo send`, or with `"regate": N` under `gate` in the policy section (the
default for new bindings). `N` is
how many repair rounds relevo may open after failing gates; `0` -- the default
-- turns the loop off, and `--regate` on a binding with no gate is accepted and
inert, since a binding with no gate never fails one.

When a round closes with `gate=fail` and the budget is not yet spent, relevo
stages round N+1 in the same tick, after the report has been queued. Its plan
file is written for the builder rather than by the planner: it names the failed
round's acceptance check and the original plan, and carries the last 200
non-empty lines of `NNN-gate.log`, instructing the builder to fix ONLY what the
check reports and to halt and report if no code change can fix it. The hand-off
is exactly a send's -- a fresh builder process -- and the new round's plan
entry is logged with `repair k/M`.

Two bounds end the loop with `NEEDS YOU` instead of another repair round:

- the budget is spent: `gate failed after M repair round(s) (regate N)`;
- the new failure's normalised output equals the previous failure's -- the
  builder changed nothing that mattered -- reported as `gate output unchanged
  after repair`. Timestamps, durations, large integers, hex digests and `/tmp`
  paths are stripped before the two are compared.

The failed round stays closed either way: its own report, diff and `gate=fail`
stand, and relevo never writes or removes `NNN-done`. A passing gate or a human
`relevo send` resets the count, so the next failure gets a fresh budget.
Headless bindings are the intended case -- the server runs the same reconcile,
so a remote headless binding gets repair rounds too.

## Reader actors

A **reader** actor is a binding, exactly as a writer is, but its round reads
and reports: relevo runs one headless process, it never edits the shared tree,
and its output lands in the round's artifact directory. That output is
`NNN-<actor>/summary.md` -- the reader's final message -- plus any files it
produced.

The planner runs, from its own session:

```
relevo bind --actor reviewer --name webshop
relevo send --name webshop --file q.md
```

relevo stages `q.md` as the round's plan and runs the reader headless; its
**final message** becomes the round's report at `NNN-<actor>/summary.md` and is
queued to the planner like any report. `relevo show <name> --summary` prints
the summary, `relevo show <name> --artifacts` lists the round's other files,
and the cockpit's artifacts tab shows both.

A bound reader round is the only way to get a reader's answer: the old
one-shot `relevo ask` consult is gone. `relevo show <name> --findings <id>`
still reads the findings of a consult recorded before that removal, and it is
how a verify reviewer's findings are read.

The process is the only thing relevo can observe: relevo kills one that
outlives its deadline, and a process that exits without a final message is
reported with its exit code and the stream to read. The resolved tier still
gates the pick: at `read`, claude and agy can run (claude `--permission-mode
plan`, agy `--mode plan`), while opencode and codex cannot honour `read` and
are refused.

While a verify reviewer is running, `relevo status` appends ` +Nc` to the
binding's row — only when non-zero, so a healthy binding looks no different. A
finished reviewer's record is dropped once its findings have been queued.
Terminal consults are not work in flight, so the count does not include them.

`relevo config agents` writes the reviewer definition with the other
agents; to install just this one:

```
relevo config agents --agent reviewer
```

`relevo doctor` reports whether the definition landed, on every kind.

Read-only is a property of the agent definition — the definition pins a
read-only tool set and the candidate's `tree` decides where it runs — not
something relevo enforces. On agy the definition's `tools:` allowlist makes it
a property the harness enforces: a write tool that is not listed is not
offered. The list is also load-bearing the other way: a definition with no
`tools:` gets no write or shell tool at all, and one naming a tool agy does
not have does not start -- so every agy definition relevo ships carries an
explicit, verified list, and `relevo doctor` warns when the installed agy
copy differs from it (on claude and opencode the copy is yours to edit, and
doctor leaves it alone). relevo cannot observe writes; it reports what is in a tree
and no more. Note also that `reviewer` is deliberately not the `researcher`
agent: `researcher` is dispatched by a builder's own plan-executor and returns
findings in-band to it, while a reviewer runs as its own relevo consult and
hands back a file path.

### Planner actors

A planner actor is a reader binding too: bind it, send it the task, and read
its plan back from the round's summary.

```
relevo bind --actor lite-planner --name plan-x
relevo send --name plan-x --file task.md
relevo show plan-x --summary                                  # or the cockpit's artifacts tab
relevo send --name <builder> --file <the summary path>
```

The plan is the reader round's summary, `NNN-lite-planner/summary.md`. Review
it with `relevo show plan-x --summary` (or the cockpit's artifacts tab), then
hand that summary path to a builder with `relevo send --name <builder> --file
<the summary path>`. Reviewing is a human (or planner-session) step: relevo
sends nothing automatically.

### Round usage

At every round close relevo records what the round consumed on the
round's `report` event (and a consult's on its `findings`
entry): harness, provider, model, duration, tokens (`in`, `cache_read`,
`cache_write`, `out` -- `out` includes thinking), and a cost with its
provenance:

| `cost.basis` | meaning |
|---|---|
| `measured` | the harness reported dollars itself (claude's `total_cost_usd`, opencode's `cost`) |
| `estimated` | relevo multiplied the harness's token counts by the prices section |
| `unknown` | no record, no price row, or no way to read; `note` says which |

`unknown` is an answer, not a failure. Where each figure comes from:

| harness | headless |
|---|---|
| claude | the round's stream (`measured`) |
| agy | the round's stream (`estimated`) |
| opencode | the round's stream (`measured`) |

A binding on `--cwd` shares the planner's directory, so its rounds
are `unknown` (`shared cwd`) rather than counting the planner's spend.

The `prices` section is `{"as_of": "YYYY-MM-DD", "source": "...", "models":
{"<provider>/<model>": {"in": …, "cache_read": …, "cache_write": …,
"out": …}}}` in USD per million tokens, overlaid on the table relevo ships;
a model with no row is `unknown`, never `$0`. Mark a subscription lane
with `"plan": true` on its candidate: its rounds record `cost.plan` and
are shown as a quota draw, never as free.

Where you see it: `relevo show --log` prints the round line under each report
(`⎿ opencode/cline-pass/glm-5.3-flash  14m  in 2k  cache 166k (91%)  write 14k  out 12k  $0.41`);
`relevo status` adds a `usage` row (newest round) and a `spend` row
(the binding's total: `4 rounds +2c · $1.23 · ~$0.40 · 2 unknown · 2.1M tok`), both
on `--json` as `last_usage` and `spend`; `relevo ui` shows the total in
the fleet's SPEND column and in the round pane's `spend` row.

A remote builder's round is measured on the server, from the builder's
own stream there, and shipped with the round: the client keeps the
server's figure verbatim instead of reading a record it does not have. A
server built before this ships no figure, and its rounds print
`unknown · remote: server sent no usage`.

While a round is running, `relevo status` and `relevo ui` show a `live`
figure read from the harness's record on each refresh, and
`relevo status --line` appends `live $0.02 · 41k tok` to the row. On
`status --json` it is carried as `live_usage`. The live figure is
estimated (`~$`) unless the harness reports dollars per step (opencode).
It is never recorded and never added to `spend`. Across bindings:

```
relevo history --tab [--since 7d|24h|2026-09-01] [--by binding|model|provider|owner] [--json]
```

sums every round relevo has recorded, including bindings `unbind --done` has
archived, one row per group and a total, with the four token columns
`in`, `cache`, `write`, `out` (a sum across models has no meaningful
ratio, so `cache` carries no percentage). Measured and estimated dollars
never share a column; `plan` and `unknown` are counts of rounds. `--tab`
is the second exception to the #114 verb freeze, taken because its
sums exist regardless (they are on `status --json`) and a cross-binding
view has no other home.

### Usage stats

```
relevo history --stats [--since 7d] [--json]
```

`relevo history --stats` answers the questions `--tab`'s money sums do not. It reads
the same records as `relevo history --tab`, live and archived, locally only:
nothing leaves the machine. `--since` cuts on a round's start, so a round that
started before the cut is not counted.

Each counted round has one builder -- `harness/provider/model`, taken from the
report's usage record when it has one, else from the round's pick, else
`unknown` -- and one outcome:

| outcome | meaning |
|---|---|
| `done` | the report's relevo block said `done` |
| `halted` | the report's block said `halted` |
| `blocked` | the report's block said `blocked` |
| `deferred` | the report's block said `deferred` |
| `unstructured` | a report arrived without a usable block |
| `noreport` | the report was noted `noreport` |
| `stopped` | no report; the round was stopped |
| `exited` | no report; the builder exited |
| `open` | no report, no stop and no exit: the round is still open |

`needs-you` and `stalled` are not outcomes because they are live state, not a
property of a finished round: relevo records neither in the round log, so there
is nothing to count after the fact. They stay where they are visible, on
`relevo status` and in `relevo ui`.

`switches` counts the rounds that changed builder mid-round, attributes each to
the provider it left with a reason (`rate-limited`, `exited`, `gated`, `remote`
or `other`), and says how many of the rounds that was. `gate` counts pass, fail,
timeout and error over the rounds that ran a gate. `consults` counts consults
per actor with the findings' models beside them, plus the verifies that were
skipped.

`blocked` comes from the gate history in the database, which keeps 30 days: rate-limit and
spawn-failure events in that window, and the gates a human cleared with
`relevo gate --clear`. Only a gate cleared by hand has a recorded length -- an
expired gate keeps no expiry time, so it counts as an event with no duration.

### Reviewer candidates

The verify reviewer resolves the reviewer actor and picks from its `candidates`
list, by the same rule as `--candidate`.

In the actors section:

```json
"reviewer": { "agent": "reviewer", "candidates": ["opus"] }
```

Until the reviewer actor lists a candidate, the verify consult fails with
`no configured candidate serves actor "reviewer"`.

## The planner: architect

relevo ships one more definition it never launches: `architect`, the planner's
persona. The planner is the session you drive -- the one you run `relevo bind`
and `relevo send` from -- and relevo does not pick its harness or start it. What
relevo provides is the definition, so the same architect runs on any kind:

```
relevo config agents --agent architect
```

(`relevo config agents` with no flags writes it too.)

Then start the planner with the harness's own `--agent` flag, for example:

```
claude   --agent architect --model opus
opencode --agent architect -m openrouter/deepseek/deepseek-v4-pro
```

The `relevo planner init` SessionStart hook also injects the relevo handoff
rules, so a planner session running an agent other than `architect` still
receives them.

**Wait for the report after every send.** In Claude Code the planner starts
`relevo wait <name> --timeout <budget>` as a **background**
command and ends its turn: Claude Code wakes the session when the command exits,
with the report already in the command's output, and the `relevo mcp` send result
prints the exact command for that binding. Other
harnesses run `relevo wait <name> --timeout 9m` in a loop while it exits 124; the
wait prints the report, and they end their turn only when no binding has a round in
flight.

The architect designs and never implements: it produces a system overview,
file structure, data structures, interface contracts, pseudocode, an error
handling strategy and ordered implementation steps -- the plan a builder's
plan-executor takes as written. Its agy copy pins `model: inherit` and a
read-plus-`write_to_file` tool set, enough to read the tree and write the plan
and nothing more; the claude and opencode copies leave `model:` unset so the
launch line's flag decides.

`architect` is not a relevo actor: it is absent from the actors section, so
`relevo bind --actor architect` is refused, and
`relevo doctor` does not check for it -- doctor reports only the definitions
some candidate would load, and no candidate loads the planner.

## Display states

`relevo status` collapses the binding's internal state into four:

- **ACTIVE** — someone is working (planner or builder), nothing needs a human
  yet.
- **NEEDS YOU** — relevo has stopped and a person must act. Covers a dead
  builder process, a round that ran past its timeout, and a binding that hit
  its round cap.
- **PAUSED** — the binding's worktree was released between rounds, keeping the
  branch and the round log; nothing sets this state today, but a binding a
  pre-housekeeping relevo left paused still reads as PAUSED, and
  `relevo bind --resume` restores it. Nothing needs a human, and
  `relevo unbind --done` leaves it alone.
- **DONE** — the planner declared the work verified via `relevo done`, and
  relaying has stopped deliberately, not because anything went wrong: unlike
  NEEDS YOU, nothing needs a human here. `Reconcile` returns immediately for
  a done binding — no reports are queued and no timeouts are flagged. The binding and its round log stay in the database (`relevo show <name> --log`
  still works as an audit trail) until `relevo unbind` or `relevo unbind --done` archives or removes them;
  a clean worktree is released at `done` so the branch is free to review.

## Running the daemon

`relevo daemon` is the reconciler: it watches builders, queues reports back to
the planner, and flags stalled rounds. Nothing else needs it running — the CLI
works on its own — but without it, reports are only delivered when you run
`relevo wait` by hand.

You can just run `relevo daemon` in any spare terminal. To have it start with
your session:

```
make service     # systemd user unit on Linux, LaunchAgent on macOS
make uninstall   # stop it and remove both the binary and the unit
```

On Linux that installs `dist/relevo.service` to
`~/.config/systemd/user/relevo.service` and enables it. On macOS it renders
`dist/com.github.fuad-daoud.relevo.plist.in` into `~/Library/LaunchAgents/` and
loads it, logging to `~/Library/Logs/relevo.log`.

Only one daemon runs at a time. `relevo daemon` takes an exclusive lock on
`$XDG_STATE_HOME/relevo/.daemon.lock` and refuses to start if another one holds
it, so starting a second by hand next to the service is an error rather than
two reconcilers racing. `relevo daemon --check` exits 0 if a daemon is running
and 1 if not, printing nothing.

## Lifecycle hooks

relevo supports user-defined hook commands dispatched during binding lifecycle
events. When state changes or a new round begins, `relevo daemon` runs the argv
lists in the `hooks` section, keyed by event type. Set one with:

```
relevo config set hooks.state_changed '[["/path/to/script"]]'
```

A directory of executable scripts dropped into
`$XDG_CONFIG_HOME/relevo/hooks/<event_type>.d/` (default
`~/.config/relevo/hooks/<event_type>.d/`) is imported into the `hooks` section
on the next command and removed; the scripts themselves are never removed.

### Supported events

- `state_changed` — fires whenever a binding transitions between states (`active`, `needs_you`, `broken`, `done`, `paused`).
- `round_started` — fires whenever a new round starts.
- `builder_stalled` — fires once when a live local builder's tree and stream or screen have been quiet for `stall_after_ms` (#252, generalised by #135). Clearing the stall fires nothing.
- `binding_stale` — fires once when a `NEEDS YOU` binding has sat unacted for `stale_after_ms` (#135). Clearing the stamp fires nothing.

### Hook execution & environment

Each hook command is executed asynchronously in a detached process with a 10-second timeout. relevo injects the following environment variables:

- `RELEVO_EVENT`: The event type name (`state_changed`, `round_started`, `builder_stalled`, `binding_stale`).
- `RELEVO_BINDING`: The name of the binding.
- `RELEVO_STATE`: The current state of the binding.
- `RELEVO_OLD_STATE`: The previous state of the binding.
- `RELEVO_ROUND`: The current round number.

Hook stdout, stderr, and execution failures are recorded in the database's hook run log. `relevo doctor` prints a `hooks` row -- `hooks: N runs, M failed in the last 24h (last: <event>: <err>)`. A legacy `<root>/hooks.log` is imported once and removed.

Each hook script must have its executable bit set (`chmod +x`). An event with no argv list in the `hooks` section is a silent no-op.

### Webhooks

Beside hook scripts, the policy section's `notify.webhooks` posts lifecycle events straight to a URL -- a Slack incoming webhook, a Discord webhook, or any endpoint that accepts a JSON POST -- with no script required:

```json
"notify": { "webhooks": [
  { "url": "https://hooks.slack.com/services/…", "format": "slack", "events": ["state_changed:needs_you", "binding_stale", "builder_stalled"] }
] }
```

- `url` (required) -- where the event is POSTed; must be `http://` or `https://`.
- `events` -- which events reach this webhook; omit (or leave empty) to receive every event. `state_changed:<state>` matches only a `state_changed` event whose new state is `<state>` -- `state_changed:needs_you` is the one most people want.
- `format` -- `json` (default: the event and its rendered text as a JSON object), `slack` (`{"text": ...}`), or `discord` (`{"content": ...}`).

One sentence per event name, for a webhook filter:

- `state_changed` -- a binding transitioned between states; filter to one target state with `state_changed:<state>`.
- `round_started` -- a new round began.
- `fork_created` -- a new binding was branched from an earlier round of an existing binding.
- `builder_stalled` -- a live headless builder's stream went quiet for `stall_after_ms`.
- `binding_stale` -- a NEEDS YOU binding sat unacted for `stale_after_ms`.

Each matching webhook POSTs in its own goroutine with a 5-second timeout and never blocks a daemon tick. A failure -- a non-2xx response or a transport error -- is logged to the hook run log with the URL's host only, never the full URL, since a webhook URL is a secret. There are no retries and no queue: a webhook is best-effort, exactly like a hook script.

## Setting up your agent harnesses

A harness needs nothing installed beyond its own binary on `PATH` and the agent
definitions relevo installs (`relevo config agents`). relevo starts each builder
as a non-interactive process and reads the round from the harness's own stream,
so there is no lifecycle hook to install for it.

### opencode permission allowlist

relevo stages plans and reports under `~/.local/state/relevo/<binding>/`, outside
the repo the builder is working in, so a fresh opencode builder blocks on an
"Access external directory" dialog on its first round. To skip it entirely, add
this to `~/.config/opencode/opencode.jsonc`:

```jsonc
"permission": {
  "external_directory": {
    "/home/you/.local/state/relevo/*": "allow",
    "/home/you/.local/state/relevo/**": "allow"
  }
}
```

Substitute your real home directory: opencode does not expand `~` or `$HOME`
in these patterns. Claude builders (`claude/anthropic/sonnet`) have their own permission model
and are not covered by that entry.

relevo doctor warns when this entry is missing (row external_directory under opencode).

## Recovering a broken binding

If the builder's process dies without a report, relevo switches to the next
candidate or, when none serves the binding, goes `NEEDS YOU` — see "Headless
builders". A binding whose builder is gone between rounds is `BROKEN` and
relaying stops until you point it at a new builder:

```bash
relevo bind --resume --name N --rebind                                       # start a fresh builder, picked by the actor's order
relevo bind --resume --name N --candidate agy/google/gemini-3.8-flash-high     # start a fresh builder, naming it
```

The binding keeps its name, round number, round log, working directory, and diff
baseline. The replacement builder is started with its agent on the launch line,
like any builder relevo spawns. With `--rebind` the candidate is resolved through
the builder actor's candidate list and the ledger, and the pick is logged, exactly as a fresh
bind with `--candidate` omitted. Relevo does not automatically re-send the current
plan: it prints the `relevo send` command pointing at the staged plan so you can
hand over the round when ready.

If only the planner moved or restarted, `relevo bind --resume --name N` re-points
the planner without touching the builder. If you want to start over from scratch,
use `relevo unbind N` and bind fresh.

A headless binding is never `BROKEN` for lack of a process: between rounds
there is none. If its process died mid-round the daemon already switched or
halted it (see "Headless builders"). To move a binding to a fresh process by
hand, `relevo send` the staged plan again once `relevo status` shows the builder
`exited`.

## Platform support

**Linux and macOS.** Both are exercised in CI, on the Go 1.22 floor and on
current stable.

Windows is not supported. State locking is behind a build tag
(`internal/store/lock_unix.go`) and could be implemented there, but the
harnesses and process supervision relevo relies on are unix-shaped. The tree
still cross-compiles for `windows/amd64` (CI checks it), and relevo will refuse
at runtime with a clear error rather than running without a state lock.

## Claude Code plugin

The relevo plugin gives a Claude Code planner two things: the `relevo mcp` MCP
server (`relevo` from `PATH`), which exposes `status`, `send` and `done` as
tools, and a `SessionStart` hook that runs
`relevo planner init`. The hook exports `RELEVO_PLANNER` and tells the model its
planner name. Install it once per machine:

    /plugin marketplace add fuad-daoud/relevo
    /plugin install relevo@relevo

The plugin also carries two slash commands over relevo's read verbs:

- `/relevo:status [--name <binding>] [--all]` -- the bindings, round and state.
- `/relevo:show [<binding>] [--round N] [--diff|--drift|--log|--report|--plan|--transcript]` -- one round's plan, report, diff, drift, log or transcript, already fetched.

Then launch Claude Code normally:

    claude --agent architect --model opus

**The background wait is the default.** After each `relevo send`, the planner
runs

    relevo wait --name <n> --timeout <budget>

as a background Bash command and ends its turn. Claude Code wakes the session
when the command exits, and its output is the report (or the reason the round
stopped). The `relevo mcp` send result prints that exact command for the
binding, so the model does not have to remember it. Act on the wait's output
after every exit except `WaitTimeout`; on a timeout, run `relevo status --name
<n>` and start the wait again if the round is still running.

**The channel is an opt-in upgrade.** With the channel enabled, reports,
consult answers and edge artifacts arrive as `<channel source="relevo">` events
the moment the daemon has them, instead of being fetched by the wait. Turn it
on by launching with the development flag, which asks for confirmation at
every start:

    claude --agent architect --model opus --dangerously-load-development-channels plugin:relevo@relevo

During the research preview `--channels` only registers plugins on an
Anthropic-curated allowlist, and relevo is not on it. A Team or Enterprise admin
can instead add `{"marketplace": "relevo", "plugin": "relevo"}` under
`allowedChannelPlugins` (with `channelsEnabled: true`) in managed settings,
which replaces Anthropic's list for that org and makes plain
`--channels plugin:relevo@relevo` work. Without either, `relevo mcp` runs in
tools mode: the tools work and nothing is pushed, which is the background wait
above.

**How reports arrive.** relevo identifies the planner session itself -- the
plugin's hook registers it, and `relevo planner list` shows the records -- so no
verb has to guess who is calling. A report then reaches the planner by exactly
one of four routes: the background wait (the Claude Code default), the channel
(opt-in, above), a deliverer for a harness that has one (opencode, agy), or
`relevo wait` by hand. Nothing is ever typed into a terminal.

An agy planner runs `relevo planner init` once inside agy, with no flags: relevo
detects the session from agy's own environment, so nothing has to be exported by
hand. Every relevo command that planner runs refreshes the session's local
agentapi credentials, which relevo keeps 0600 under its state directory and never
prints. A report relevo pushes through those credentials wakes the idle agy
session, so an agy planner is woken by a report rather than polling for it --
and that wake-up costs one turn of the agy session.

## Design

[`docs/design.md`](docs/design.md) is the architecture document written before
relevo was built. It explains why the CLI and the daemon are split and what was
deliberately left out. It is a historical record, not maintained against the
code.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). In short: open an issue first, keep it
stdlib-only, write the test, and make sure `make check` passes.

`make e2e` runs one headless relevo round end to end -- planner init, bind,
send, delivery -- with a fake harness binary on `PATH`. It runs in CI and is not
part of `make check`.

## License

[MIT](LICENSE) © Fuad Daoud
