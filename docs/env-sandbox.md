# The env sandbox

An env sandbox is a directory under `~/.local/share/relevo-sandboxes` whose
`HOME`, `XDG_*`, `CLAUDE_CONFIG_DIR` and `CODEX_HOME` all point inside it.
Nothing else is involved: the isolation is the exports, and everything a process
writes goes through one of those nine variables. `scripts/relevo-sandbox.sh` wraps them
into one command and adds the refusals that make it hard to misuse.

It needs no root, no account and no systemd unit. The stronger per-user flow is
[dev-sandbox.md](dev-sandbox.md); this one is the default, because it costs
nothing.

## Setup

```
scripts/relevo-sandbox.sh create demo
scripts/relevo-sandbox.sh shell demo
```

`create` needs the invoking directory to be a checkout of this repo, because it
builds the binary out of it. `--no-build` skips that step when there is no
toolchain to hand.

`create` also finishes the harness setup: it copies the claude login, config,
agent definitions and hooks plus the codex login and config (never session data),
copies your tuned candidates, policy, servers, actors and agents section by
section with `config get`/`config set`, and runs `doctor` once. A section you
never set is skipped; a host with no database falls back to `config init
--no-agents`. Secrets never travel (export omits them), so remote placements
need `config secret set` inside afterwards, and gates start clean. A host with
candidates but no actors section still yields working binds through the
built-in fallback -- only the candidates view reads `off`, which needs no
manual step. Steps that fail warn with the manual repair instead of failing
the create; `--no-build` skips seed and check along with the build.

Inside the shell, everything resolves under
`~/.local/share/relevo-sandboxes/demo/`:

| Variable | Points at | What reads it |
| --- | --- | --- |
| `XDG_STATE_HOME` | `<sandbox>/state` | `store.DefaultRoot` returns `$XDG_STATE_HOME/relevo`, so the database, the daemon socket and every binding land in the sandbox |
| `XDG_CONFIG_HOME` | `<sandbox>/config` | `userConfigRoot()`, so candidates, actors and policy land in the sandbox |
| `XDG_DATA_HOME` | `<sandbox>/data` | anything that follows the XDG data base |
| `XDG_CACHE_HOME` | `<sandbox>/cache` | anything that follows the XDG cache base |
| `CLAUDE_CONFIG_DIR` | `<sandbox>/claude` | the claude harness: its settings, its `agents/` and its credentials |
| `CODEX_HOME` | `<sandbox>/codex` | the codex harness: its config and its per-agent profiles |
| `HOME` | `<sandbox>/home` | every path built from the home directory instead of an export: harness install paths, git's `~/.gitconfig`, agy's `~/.gemini`, Go's module cache |
| `SB` | `<sandbox>` | the sandbox path itself, for scripts |
| `RELEVO_SANDBOX` | `<sandbox>/name` | marker: set iff inside the sandbox; scripts refuse to run without it |
| `PATH` | `<sandbox>/bin` first | the sandbox's own `relevo` build, ahead of the developer's `~/.local/bin` |

The layout under `<sandbox>/` is `bin/ state/ config/ data/ cache/ claude/
codex/ home/ env.sh` plus the `sandbox` marker, every directory `0700` and every file
`0600`: the sandbox holds a database and harness credentials, and nothing under
it needs another uid's read. `home/` holds links onto the sandbox's own roots
(`.config`, `.cache`, `.claude`, `.codex`, `.local/state`, `.local/share`), so
`$HOME/.config/opencode` and `$XDG_CONFIG_HOME/opencode` name the same file, plus
a copy of your `~/.gitconfig` so builders can commit.

`env.sh` is the whole recipe, so it can be sourced by hand from a script that is
not `shell`:

```sh
. ~/.local/share/relevo-sandboxes/demo/env.sh
relevo status
```

## The guard line

`RELEVO_SANDBOX` is set iff the exports above are in force, so a script that
was handed out to run *inside the sandbox* can tell whether it is. Any such
script or documentation snippet starts with exactly this line, before it
sources `env.sh` or does anything else:

```sh
test -n "$RELEVO_SANDBOX" || { echo 'not in a sandbox' >&2; exit 1; }
```

Outside a sandbox the variable is empty and the script stops, instead of
running `config set` or `bind` against the developer's own state root because
`env.sh` was never sourced. `relevo sandbox shell <name>` sets it for the whole
session; nothing else sets it.

## Two standing rules

**Never run `relevo config agents` with only the `XDG_*` exports set.** It
resolves harness homes through `HOME`, so outside the full `env.sh` it would
write your own definitions with the sandbox binary's shipped copies. Inside
`shell`, or under a sourced `env.sh`, `HOME` is the sandbox's and the write
stays there.

**One `relevo serve --listen` per sandbox.** `--listen` defaults to `:7777`, which
is the production serve's port on this host. Two serves on one port means the
second fails to bind, or worse, one sandbox answers for the other's requests.
Give each sandbox its own address:

```
relevo serve --listen :7801
```

The `port` doctor row warns when a sandbox's serve sits on 7777 or on a port
other than the one its sandbox was made for.

## Credentials

`create` copies the claude login (`.credentials.json`), its config
(`settings.json`, `settings.shared.json`) and its `agents/` definitions into
`<sandbox>/claude/`, plus the codex login (`auth.json`), config
(`config.toml`) and definitions into `<sandbox>/codex/`. Session data --
`projects/`, `history.jsonl`, todos, databases -- is never copied, so a
sandbox reuses your login without inheriting your sessions. Anything absent is
skipped with a warning naming the manual step.

The caveat is a race, and it is the operator's to know about: the credentials
file is a rotating secret on the host, and the copy is a snapshot. If the host
rotates after the copy, the sandbox keeps using the older snapshot until it
expires, and a sandbox created before a rotation never sees the new one. Copy
again, or log in inside the sandbox, when a sandboxed run starts failing
authentication. An expired host login fails everywhere at once -- that is never
a sandbox problem, and re-login fixes both sides.

opencode is the other direction. A second opencode service cannot start while
yours runs: opencode pins its managed port, so the sandbox instance retries
your port and never binds. `create` copies your opencode agent definitions
(`~/.config/opencode/agents/`) into `<sandbox>/config/opencode/agents/` --
without them an opencode builder fails with `Agent not found` -- and copies
`service.json` and `auth.json`, which attach an interactive opencode inside
the sandbox to your running service. To re-attach by hand (same user,
re-copyable; copy `service.json` again after your service restarts, since its
password rotates):

```
mkdir -p <sandbox>/state/opencode <sandbox>/data/opencode
cp ~/.local/state/opencode/service.json <sandbox>/state/opencode/service.json
cp ~/.local/share/opencode/auth.json <sandbox>/data/opencode/auth.json
```

relevo itself runs opencode `--standalone`, which does not use that service:
it reads provider credentials from opencode's own database
(`<sandbox>/data/opencode/opencode.db`), and since opencode 2 that is where
subscription logins such as opencode-go and cline-pass live, not `auth.json`.
The database also holds your sessions, so `create` never copies it. Before an
opencode-backed builder or reader runs in a sandbox, log in inside it with
`opencode auth login`; until then the round fails with `provider.no-route:
Model unavailable` and relevo switches to the next candidate.

`relevo doctor` is the check to run after any sandbox setup: it reports which
harnesses are installed, which are logged in and where each resolves.

## Limitations

agy (gemini) has no home selector of its own, so it follows `HOME` into
`<sandbox>/home/.gemini`. `create` copies its agent definitions there but not its
login, so agy starts logged out inside a sandbox, like opencode.

Go's module cache follows `HOME` too (`<sandbox>/home/go/pkg/mod`), so the first
build or test inside a fresh sandbox downloads its modules. Go writes that cache
read-only; `destroy` makes the tree writable before it removes it.

The other limitation is what the sandbox does not separate: the checkout. Two
sandboxes sharing one git work tree share one `.git`, which means they fight over
branches, `git fetch` refusals and `git remote prune`. For a test that needs two
sandboxes on the same repo at once, use the per-user flow instead.

## What the script never does

`scripts/relevo-sandbox.sh` never touches `~/.local/bin`, never runs `make
install` or `make service`, and never installs harness definitions into your
home -- it copies the already-installed claude and codex definitions into the
sandbox instead, and seeds candidates with `config init --no-agents`, which
writes only the sandbox database. The binary lands in `<sandbox>/bin`, ahead
of `PATH`, so the sandbox's own build is the one a sandboxed shell finds.

## Listing and rollback

```
scripts/relevo-sandbox.sh list
scripts/relevo-sandbox.sh destroy demo
```

`destroy` is the rollback: `rm -rf` of the sandbox directory, which is where all
of its state, config, data, cache and harness homes live. Two refusals stand in
the way. A directory with no `sandbox` marker is refused outright, so the
`rm -rf` can never be aimed at an ordinary directory that happens to sit under
the root. A held `<sandbox>/state/.daemon.lock` means a foreground daemon is
still writing into the directory about to be removed, so `destroy` refuses unless
you pass `--force`.

`RELEVO_SANDBOX_ROOT` moves the root, and the name must match
`^[a-z][a-z0-9-]{0,12}$`. Every subcommand takes `--dry-run`, which prints the
actions and writes nothing.