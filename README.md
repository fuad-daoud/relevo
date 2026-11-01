# relevo

[![ci](https://github.com/fuad-daoud/relevo/actions/workflows/ci.yml/badge.svg)](https://github.com/fuad-daoud/relevo/actions/workflows/ci.yml)
[![license: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Site: [relevo.sh](https://relevo.sh) (source in [fuad-daoud/relevo-site](https://github.com/fuad-daoud/relevo-site), together with the `DESIGN.md` and `PRODUCT.md` that govern the page).

`relevo` automates the plan/report handoff between two AI coding agents. A
human talks to a **MasterMind** agent; the MasterMind hands work to a **builder**
agent; relevo moves the files between them so the human never copy-pastes a
plan or a report by hand. Builders are headless or remote processes.

<!-- name-guard: off -->
relevo is Spanish for relay (the changeover in a relay race); it was called relay until v0.12.0.
<!-- name-guard: on -->

Relevo makes no judgements. It moves files, starts builders, and reports what
each round did — whether a report is good, whether a question needs a human,
whether the work is done, is a decision that stays with the MasterMind (or the
human) at every step.

## Requirements

- **Two agent harnesses** — one for the MasterMind, one for the builder. relevo
  knows how to start `opencode`, `claude`, `agy` and `codex`; you tell it
  which models in [Candidates](docs/candidates.md#candidates).
- **`git` on `PATH` (optional).** Required for automatic round diff capture; without it, relevo works normally but rounds produce no diffs.
- **Linux or macOS.** See [Platform support](#platform-support).
- **Go 1.25+**, to build from source. Not needed if you install a release
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
`go test -count=1 ./...` — and `make install` runs it first. While iterating,
`make check-affected` runs the same static checks over only the packages your
change reaches.

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
`systemctl --user restart relevo.service`. `relevo doctor` shows what the
daemon is running. The daemon also refreshes the agent definitions relevo wrote
for each harness on every start, and leaves a file you edited alone; it does the
same for its own service unit, which ships inside the binary, so `relevo update`
alone carries a release forward -- no checkout and no `make service`. A
refreshed unit takes effect at the next daemon restart, and `relevo doctor`
says when one is due. The first install of a release has no unit yet:
`relevo service install` writes it and starts the daemon (`relevo service
uninstall` removes it). A MasterMind session's `relevo mcp` notices the upgrade too -- it
appends a line to every tool result saying to reconnect it (`/mcp`), so the
session loads the new server without a restart.

### The Claude Code plugin

A Claude Code MasterMind installs relevo as a plugin. The plugin's MCP server,
its `SessionStart` and `UserPromptSubmit` hooks and its slash commands all run
through tiny scripts under `${CLAUDE_PLUGIN_ROOT}/scripts/` that exec `relevo`
from `PATH`, so relevo knows which MasterMind session is calling. Without
`relevo` on `PATH` the hooks stay silent, and the MCP server and the commands
print one install hint:

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

See [Claude Code plugin](docs/plugin.md#claude-code-plugin) below for how a report reaches
the MasterMind.

### Muse

The same plugin serves muse, installed from the same repository:

    muse plugins marketplace add relevo https://github.com/fuad-daoud/relevo.git
    muse plugins install relevo@relevo
    muse plugins approve relevo

The last line is muse's own gate: a plugin's hooks and MCP servers stay
inactive until it is approved. `relevo doctor` reports each state — installed,
enabled, approved, version — through `muse plugins inspect relevo --json` and
prints these commands as its fixes. After upgrading relevo, refresh the
marketplace and reinstall the plugin to match (`relevo doctor` warns when they
differ). `muse plugins update` leaves a marketplace plugin at its old version,
and a new version's hooks and MCP server wait for approval again:

    muse plugins marketplace update relevo
    muse plugins remove relevo
    muse plugins install relevo@relevo
    muse plugins approve relevo

Muse's sandbox is the one place its MasterMind differs from Claude Code's. It
blocks unix sockets, so the verbs that reach the owner daemon over
`relevo.sock` — `bind`, `chain` and `config` — need muse unsandboxed: run
them under `muse --yolo` or `muse --disable-sandbox`. The MCP tools work
either way.

## First run on a clean machine

On a clean machine, install relevo and run the setup command once; it probes
the machine, asks what is still open, writes the starter configuration, and
runs the plugin, the service and one real round:

1. Install relevo (see [Install](#install)).
2. Run the setup wizard in a terminal:
   ```
   relevo setup
   ```
   It prints one inventory line over what it probed, e.g.:
   ```
   codex: logged in · claude: logged in · agy: on PATH, not logged in -> run agy once to sign in
   ```
   then asks one question at a time. Where opencode is installed it first
   asks which opencode models relevo may use (`opencode`): pick providers
   from what `opencode models --standalone` lists, then models -- a
   provider's whole list when it has 30 or fewer, else a few models to
   start from (for openrouter: `z-ai/glm-5.3-flash`,
   `deepseek/deepseek-v4.1-flash`, `xiaomi/mimo-v2.6-flash`) and a name
   search. Nothing is recommended or pre-picked: Enter skips opencode, and
   only the models picked are written as candidates. Then: which models build (`builder`), who
   writes plans (`planner`), then shows what the other roles get from those
   picks -- `librarian` (writes docs only), `reviewer`, `security` and
   `lite-planner` -- and asks whether to keep them or pick each one, then the
   permission tier (`tier`), whether to install
   the relevo plugin into Claude Code (`plugin`), whether to run the daemon as
   a user service (`service`), and whether to run one small real round now
   (`smoke`). A question that cannot apply is dropped. Press Enter to take the
   recommended pick; an ordered pick is typed as comma-separated numbers,
   `1,3`, and a yes/no takes `y`/`n`. Each picked builder or planner model
   that runs at more than one effort then gets its own effort question:
   medium is recommended for builders, which follow a plan, and high for the
   planner, which writes it. Each answer is echoed back (`-> gpt-6-luna
   (medium), haiku (medium)`).
3. Run the literal fix commands the last line names, or run `relevo setup`
   again with every answer it named:
   ```
   relevo setup --answer builder=<...> --answer tier=<...>
   ```
   A value is the choice's own value; a candidate value is
   `<harness>/<provider>/<model>`, optionally `:<effort>` with one of the
   choice's `efforts`. The role questions are `optional`: leave them out and
   each role follows the builder and planner answers. So is `opencode`: it
   recommends nothing and lists a few openrouter models, but any model
   `opencode models --standalone` prints is accepted as
   `opencode/<provider>/<model>`, and the builder and planner questions then
   offer those picks. Off a terminal (or under `--json`), a run
   with questions still open prints a questions document to stdout and exits
   3; nothing is written. A repeated `--answer <id>=<value>` answers more than
   one question, and an answer for a question that no longer applies is
   ignored with a note on stderr. Applying writes the configuration — one
   builder candidate per probed model, the `librarian`, `planner`,
   `reviewer`, `security` and `lite-planner` actors at the answered tier —
   as one revision, installs the agent definitions for each logged-in
   harness, installs the plugin with
   `claude plugin marketplace add fuad-daoud/relevo && claude plugin install
   relevo@relevo`, installs the service unit as `relevo service install`
   does, and, on `smoke`, binds `setup-smoke` on a throwaway git repository
   under the state root and runs one real round that must leave `HELLO.md`
   containing `relevo works`. While it runs, setup prints the builder holding
   the round, each tool call it makes, and a switch when a usage limit hands
   it to the next builder; a pass prints the file and the builder's commit.
   A round whose only builders hit a usage limit fails with the reset time
   and `next: relevo setup`, to pick a second builder as a fallback.
   A passing round unbinds and removes the
   throwaway state; a failed one keeps everything and prints
   `relevo show setup-smoke --round 1 --transcript`. Every step lands in the
   result document (`relevo setup --json`), exit 0 when all passed, 1 with
   the failed step's name otherwise.
4. Run `relevo doctor` to confirm `0 failures`. It inspects the background
   daemon, each harness binary on `PATH`, the relevo plugin and its hook in
   Claude Code, and the builder's agent files. Then open Claude Code in a
   repository and ask it to plan something.

Running it again keeps what is configured: the stored builder, planner and
tier values are the recommended picks, so Enter-Enter-Enter keeps them, and
applying writes a new configuration revision the revision history
(`relevo config log`) can roll back.

### Setting up through an AI agent

An agent asked to install relevo runs the same command, without a terminal
to prompt on: `relevo setup --json` prints the probe and the open questions
and exits 3 with nothing written. The agent shows each question to you with
its recommended answer marked, never answers the `tier` question itself,
and then runs the `next` command the document names with your answers
filled in, e.g.
`relevo setup --answer builder=codex/openai/gpt-6-luna:high --answer tier=yolo ...`.
That run applies everything, smoke round included, and prints one result
row per step.

### Scripted install

Prefer to assemble the configuration by hand or in script? `relevo config
init` writes the same starter configuration — one builder candidate per
probed model, the policy at tier `yolo` and the derived actor set — and
installs the agent definitions, then leaves the plugin, service and smoke
round to their own verbs:
```
relevo config init
relevo doctor
```
It refuses to overwrite the candidates, policy or actors section without
`--force`, and `--no-agents` skips the definitions. The configuration lives
in relevo.db under the state root, not in a file; a file you drop into
`~/.config/relevo` is imported on the next command and removed. With claude
as the only harness on `PATH`, the builder is written with no candidates,
and init prints the command to add one. **Edit the model names it wrote** to
the models your accounts may run (see [Candidates](docs/candidates.md#candidates)); `relevo
config` prints what you configured. When more than one candidate serves
`builder`, `relevo config` shows the order relevo would pick them in and
says `would refuse` until the actor's candidate list suits you (see
[Actors and agents](docs/candidates.md#actors-and-agents)).

Bind your first builder from the MasterMind session:
```
relevo bind --no-feature
```
relevo starts the candidate `relevo config` marks `<- would pick` under
`builder`. To run another, pass its name, `relevo bind --no-feature
--candidate <name>`; it must be on `actors.builder.candidates` (see
[Choosing a candidate](docs/candidates.md#choosing-a-candidate)). A fresh `relevo bind`
must name exactly one of `--feature <label>` / `--no-feature`. Doctor does
not check that a harness is logged in: a builder that cannot authenticate
ends its first round without a report, and `relevo status` shows the
harness's error.

To install the agent definitions into each harness on `PATH` by hand instead —
the daemon refreshes unmodified definitions on every start and upgrade, a file
you edited is kept, and `relevo config agents --force` replaces it:
```
relevo config agents
```
One line per file says `wrote`, `updated (unchanged since relevo wrote it)`,
`kept (identical)` or `kept (differs; --force to overwrite)`. Pass `--kind` to
name a harness that is not on `PATH` yet, `--agent` for one definition,
`--dry-run` to look first. This writes `plan-executor`, `researcher`, `reviewer`,
`security-reviewer`, `architect`, `librarian` and `designer` for every kind; `relevo config agents --dry-run` shows what
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

`security-reviewer` is a second read-only consult, separate from `reviewer`: it
audits a diff, area or question for exploitable issues and returns findings
with severity, file:line evidence and preconditions. relevo does not run it
automatically; add a `security` actor whose candidate list names the models
you want, then bind with `--actor security`. The definition supports a
two-round protocol: send round one to one model, then hand its findings to a
second model in round two to confirm or refute each one and hunt what the
first missed -- two models cover each other's blind spots, and the agent
definition tells the second round how.

codex agent definitions are TOML profiles at `~/.codex/<name>.config.toml` selected
with `-p`; the researcher profile runs every codex builder's research
sub-agents at `medium` effort on the candidate's own model.

Then set the candidates section (see [Candidates](docs/candidates.md#candidates)) and check it
with `relevo config`. If more than one candidate serves `builder`, order them
in the builder actor's `candidates` list (see [Actors and agents](docs/candidates.md#actors-and-agents)); `relevo config` shows what
relevo would pick and says `would refuse` until you do. `relevo doctor` warns
about this too and prints a starter policy built from your candidates.

## Quick start

On a clean machine, run the setup wizard first (see
[First run on a clean machine](#first-run-on-a-clean-machine)), or assemble it
in script per its [Scripted install](#scripted-install) note:

```
relevo setup                       # probe, ask, write the starter configuration, install the plugin and service, run one round
```

From the MasterMind session, in the repository you want worked on:

```
relevo bind --no-feature           # start a builder on this tree
relevo send --file plan.md         # hand it the plan; the builder starts working
relevo status                      # watch the round
relevo wait                        # block until the round closes; prints the report
relevo done <name>                 # stop relaying when you are satisfied
```

The builder writes `NNN-report.md` when it has finished and then creates an
empty `NNN-done` as its last action; relevo closes the round on that marker.
A builder that exits without the marker still closes the round, and relevo
delivers its report flagged `unmarked`.

`relevo bind` identifies the calling MasterMind through `RELEVO_MASTERMIND`, which
the relevo plugin's `SessionStart` hook exports, or through the harness
process the `relevo mcp` server shares with the session. Run
`relevo mastermind list` to see the MasterMinds relevo knows.

Its `chat` column names each opencode MasterMind as a person sees it: the
session's own title, read from opencode's database when the command runs. Every
other kind, and an opencode session whose title cannot be read, shows `-`. The
label is never stored. The same label follows the MasterMind's name in `relevo
status` and `relevo doctor`. `relevo mastermind rename <id|name> <new-name>`
gives a MasterMind a name of your own.

## Documentation

- [Command surface](docs/commands.md) — every verb, the round report and the status line.
- [The cockpit](docs/cockpit.md) — the `relevo ui` terminal interface.
- [Headless builders](docs/rounds.md) — rounds, gates and running several builders at once.
- [Remote builders](docs/remote.md) — the server, tenant isolation and the client.
- [Candidates](docs/candidates.md) — models, policy, actors and permission tiers.
- [Reader actors](docs/readers.md) — readers, planners, usage and the MasterMind.
- [The database](docs/database.md) — the record, cloud sync and the daemon.
- [Lifecycle hooks](docs/hooks.md) — hook events, webhooks and harness setup.
- [Claude Code plugin](docs/plugin.md) — the MCP server, hooks and slash commands.

## Status line

`relevo status --line` shows this MasterMind's bindings, one row each (one entry
per chain in place of its members), under the Claude Code prompt; it shows nothing
on error and never probes a builder.
The first line names the MasterMind (`MasterMind architect-14`), so each terminal
shows which MasterMind it is; `relevo mastermind list` maps that name to its chat.
While a live board runs for this MasterMind, the next line is `board <mastermind> · <url>`,
dim, carrying the full token-bearing URL; `status --line --json` carries the same
as the top-level `board` block (`name`, `scope`, `url`, null when absent), and the
OpenCode plugin's sidebar draws the same line. The block is a file read only --
the pointer, `server.json` and a pid check -- and never reads the database.
Each row shows the round's harness (`harness@server` for a remote builder),
what it is waiting on, this round's tokens, and the round's length: ticking while
it runs, frozen once the report is in. While a round runs the row also says what
the runner is doing -- `working`, `quiet X` once its progress has gone silent,
`stalled X`, `exploring X`, `gating X`, `exited`, `running`, `queued` -- in place
of the phase word, and the round's live diff against its baseline (`+A/-R in F`,
`(shared)` for a `--cwd` binding whose tree is the mastermind's own).

Add this to `~/.claude/settings.json`:

```json
"statusLine": { "type": "command", "command": "relevo status --line", "refreshInterval": 1 }
```

One precondition: `relevo` must be on the `PATH` of the Claude Code process.
The MasterMind session is identified by `RELEVO_MASTERMIND`, which the relevo plugin's
hook exports.

Claude Code renders a few cells less than `COLUMNS`; relevo subtracts 4 by
default (measured on the fullscreen TUI). If the right-hand round clock (with
a state word only when it is not ACTIVE) is clipped or sits short of the edge,
measure yours and set `RELEVO_STATUSLINE_MARGIN` in the environment Claude Code
starts from. To measure, put this in `statusLine.command` for one refresh and
count the cells before Claude Code's `…`:

    sh -c 'printf "%s" "$(seq -s . 1 $COLUMNS | cut -c1-$COLUMNS)"'

Each row is `○ name  rN · builder · what relevo is waiting on  …  age · STATE`,
where `builder` is the harness segment of the candidate token, `age` is time
since the last prompt, report or question crossed, and a `+A/-R in F` segment in
the middle is the round's live diff against its baseline. `relevo status --line
--json` carries that diff as an additive `live` object on the row (`files`,
`added`, `removed`, and `shared` for a `--cwd` binding); the key is present only
while a round runs. It also carries `text`: the row exactly as the text mode
renders it, without colour codes, which the OpenCode sidebar prints.

## Platform support

**Linux and macOS.** Both are exercised in CI, on the Go 1.25 floor and on
current stable.

Windows is not supported. State locking is behind a build tag
(`internal/store/lock_unix.go`) and could be implemented there, but the
harnesses and process supervision relevo relies on are unix-shaped. The tree
still cross-compiles for `windows/amd64` (CI checks it), and relevo will refuse
at runtime with a clear error rather than running without a state lock.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). In short: open an issue first, keep it
stdlib-only, write the test, and make sure `make check-affected` passes — or
`make check` for the full run.

`make e2e` runs one headless relevo round end to end -- MasterMind init, bind,
send, delivery -- with a fake harness binary on `PATH`. It runs in CI and is not
part of `make check`.

## License

[MIT](LICENSE) © Fuad Daoud
