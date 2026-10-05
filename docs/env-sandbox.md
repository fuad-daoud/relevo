# The env sandbox

An env sandbox is a directory under `~/.local/share/relevo-sandboxes` whose
`XDG_*`, `CLAUDE_CONFIG_DIR` and `CODEX_HOME` all point inside it. Nothing else
is involved: the isolation is the exports, and everything a process writes goes
through one of those seven variables. `scripts/relevo-sandbox.sh` wraps them
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
| `PATH` | `<sandbox>/bin` first | the sandbox's own `relevo` build, ahead of the developer's `~/.local/bin` |

The layout under `<sandbox>/` is `bin/ state/ config/ data/ cache/ claude/
codex/ env.sh` plus the `sandbox` marker, every directory `0700` and every file
`0600`: the sandbox holds a database and harness credentials, and nothing under
it needs another uid's read.

`env.sh` is the whole recipe, so it can be sourced by hand from a script that is
not `shell`:

```sh
. ~/.local/share/relevo-sandboxes/demo/env.sh
relevo status
```

## Two standing rules

**Never run `relevo config agents` from inside a sandbox.** The sandboxed binary
would write harness definitions into the sandbox's config root, where the
developer's own harnesses never read them, and would report a clean install that
nothing outside the sandbox can see. Run `relevo config agents` outside, against
your own install, and let the sandbox inherit the result through
`CLAUDE_CONFIG_DIR`/`CODEX_HOME` only when you have copied credentials in.

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

`create` copies `~/.claude/.credentials.json` into `<sandbox>/claude/` when that
file exists, and warns when it does not. The copy is what lets a sandbox reuse
an existing login instead of asking for a new one.

The caveat is a race, and it is the operator's to know about: the credentials
file is a rotating secret on the host, and the copy is a snapshot. If the host
rotates after the copy, the sandbox keeps using the older snapshot until it
expires, and a sandbox created before a rotation never sees the new one. Copy
again, or log in inside the sandbox, when a sandboxed claude run starts
failing authentication.

opencode is the other direction. A fresh `XDG_CONFIG_HOME` starts opencode
logged out, because that is where its own credential lives, and there is no
opencode per-process home variable to redirect (only claude and codex have one).
So inside a fresh sandbox, log opencode in once:

```
opencode
# then, inside the sandbox:
relevo doctor
```

`relevo doctor` is the check to run after any sandbox setup: it reports which
harnesses are installed, which are logged in and where each resolves.

## Limitations

agy (gemini) offers no home selector, so `~/.gemini` stays shared across every
sandbox and your own sessions. agy inside a sandbox therefore reads and writes
the same files as agy outside it. Treat agy-backed runs as shared, or do not use
agy in a sandbox.

The other limitation is what the sandbox does not separate: the checkout. Two
sandboxes sharing one git work tree share one `.git`, which means they fight over
branches, `git fetch` refusals and `git remote prune`. For a test that needs two
sandboxes on the same repo at once, use the per-user flow instead.

## What the script never does

`scripts/relevo-sandbox.sh` never touches `~/.local/bin`, never runs `make
install` or `make service`, and never runs `relevo config agents`. The binary
lands in `<sandbox>/bin`, ahead of `PATH`, so the sandbox's own build is the one
a sandboxed shell finds. Installing and logging the harnesses in stay your own
work, against your own install.

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