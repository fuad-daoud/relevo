# relevo for Claude Code

relevo automates the plan and report handoff between two AI coding agents. You talk to a **MasterMind** session in Claude Code. The MasterMind hands each round of work to a **builder** agent, and relevo moves the plan to the builder and its report back, so you never copy them by hand. This plugin makes a Claude Code session a relevo MasterMind: it registers the session, pushes builder reports and "needs you" stops into it, and gives it relevo's `status`, `send`, `show`, `gate` and `done` as tools.

## You need the relevo binary first

The plugin only connects Claude Code to relevo. Every hook, command and tool runs the `relevo` binary, which must already be on your `PATH`. Install it one of these ways:

- **Release binary:** download an archive for Linux or macOS (amd64 or arm64) from [the releases page](https://github.com/fuad-daoud/relevo/releases), unpack it, and put `relevo` on your `PATH`.
- **Go:** `go install github.com/fuad-daoud/relevo/cmd/relevo@latest`
- **From source:** clone the repository and run `make install`. See [Install](https://github.com/fuad-daoud/relevo#install).

Then run `relevo config init` and `relevo doctor` to set up and check your builders.

Without the binary, the plugin stays out of your way. Its hooks do nothing, and its commands and tools print one line telling you to install relevo.

To install the plugin from relevo's own marketplace instead of the directory:

```
/plugin marketplace add fuad-daoud/relevo
/plugin install relevo@relevo
```

## Claude Code only

relevo works only in Claude Code. claude.ai chat and Cowork can't run it: chat ignores the plugin's hooks and its local MCP server, and neither can reach a `relevo` binary on your machine. If a relevo command or skill loads somewhere it can't work, it says so and stops.

## What the plugin runs

Everything goes through small shell scripts in `scripts/`, which run `relevo` from your `PATH`:

- **On every session start:** `relevo mastermind init --hook claude`, which registers the session and asks whether it should be a MasterMind in this repository.
- **On every prompt you send:** `relevo mastermind notice --hook claude`, which tells the session when its MasterMind status changed: enabled, disabled or renamed.
- **The MCP server:** `relevo mcp`, which provides relevo's tools.
- **The commands:** `/relevo:enable`, `/relevo:disable`, `/relevo:enable-repo`, `/relevo:disable-repo`, `/relevo:reset`, `/relevo:status` and `/relevo:show`, each running the matching `relevo` command.

## Builder permissions

Builders run without a person watching, so relevo starts each one at a permission tier that you set per candidate or actor:

- `harness` (the default): the builder's own settings decide.
- `read`: plan mode only.
- `edit`: file edits are allowed. For `claude`, this is `--permission-mode acceptEdits`.
- `yolo`: the builder's permission prompts are skipped. For `claude` and `agy`, this is `--dangerously-skip-permissions`.

relevo never goes above `edit` unless you say so. The `max_tier` policy defaults to `edit`, and a tier above it is refused unless you pass `--allow-yolo` or raise `max_tier` yourself. Each builder works in its own git worktree unless you bind it to an existing tree.

## What relevo reads, sends and keeps

relevo has no telemetry or analytics, and nothing is sent to its developer.

- **Local state.** Everything relevo records is kept in a database file, `~/.local/state/relevo/relevo.db` (or under `$XDG_STATE_HOME/relevo/`). It is never synced anywhere.
- **Your conversation.** relevo never reads your own Claude Code conversation, its transcript or its title.
- **Builders.** relevo starts the AI coding agents you configure: `opencode`, `codex`, `agy` and `claude`. Each sends your plans and code to its own model provider, under that provider's policy. relevo records the output of the agents it starts, including headless `claude -p` runs, to show you their transcripts and token usage. It never reads Claude Code's own session files.
- **GitHub, automatically.** The relevo daemon checks `api.github.com` for the latest release on a timer. This is an ordinary HTTPS request that carries nothing about you or your code.
- **GitHub, when you ask.** `relevo update` downloads the new release from GitHub.
- **`api.typesafe.ai`.** Only when `TYPESAFE_API_KEY` is set: the jev classifier sends it the text being classified.
- **Webhooks and `relevo serve` servers.** Only when you configure them. relevo sends lifecycle events, or a round's work, to the endpoints and servers you chose.

Privacy policy: https://relevo.sh/privacy

## More

- Source, issues and the full manual: https://github.com/fuad-daoud/relevo
- License: Apache-2.0
