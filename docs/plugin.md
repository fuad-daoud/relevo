# Claude Code plugin

The relevo plugin gives a Claude Code MasterMind two things: the `relevo mcp` MCP
server, which exposes `status`, `send`, `wait`, `done`, `show` and `gate` as tools
(`--kind opencode` serves the five without `wait`), and
a `SessionStart` hook that runs `relevo mastermind init`. The hook exports
`RELEVO_MASTERMIND` and tells the model its MasterMind name. Install it once
per machine as [The Claude Code plugin](../README.md#the-claude-code-plugin)
describes; that section also covers how the plugin's scripts reach `relevo`.

The plugin also carries slash commands over relevo's verbs:

- `/relevo:status [--name <binding>] [--all]` -- the bindings, round and state.
- `/relevo:show [<binding>] [--round N] [--diff|--drift|--log|--report|--prompt|--transcript]` -- one round's prompt, report, diff, drift, log or transcript, already fetched.
- `/relevo:enable` -- this session answers the consent question yes.
- `/relevo:enable-repo` -- this repository answers yes from now on.
- `/relevo:disable` -- this session answers no.
- `/relevo:disable-repo` -- never in this repository.
- `/relevo:reset` -- ask the consent question again in this repository and this session.

The plugin also ships a `planner-loop` skill that walks the send → wait → read the report → verify with the project's check → done loop.

Then launch Claude Code normally:

    claude --agent architect --model opus

**The `wait` tool covers sessions without a holder.** After each `relevo send`, the MasterMind
calls

    wait(name: "<n>", timeout: "<budget>")

and ends its turn. The call blocks until the round closes. Where no push holder
runs, its output is the report (or the reason the round stopped); where the
mod already delivered the report, its output is only the outcome line
("delivered by the mod"). The `relevo mcp` send result
points at that tool for the binding, carrying the round's budget, so the model
does not have to remember it. Act on the wait's output
after every return except `still-open`; on a timeout, run `relevo status --name
<n>` and wait again if the round is still running.

**Push is the Claude Code default where the mod loads.** A `relevo push` holder
writes reports, consult answers and edge artifacts to the session's mod as
NDJSON the moment the daemon has them. The session's mod starts the holder and
acks each line with `relevo push --ack <binding> <seq>`, which confirms that
entry and exits; the holder sees the confirm and sends the next line, so by the
time the `wait` tool returns its outcome line the report is already in the
session. Meanwhile the status line under the prompt keeps the bindings, round
and state in view (see [Status line](commands.md#status-line)). Without a holder -- the mod
not loaded, another harness, a session where no MasterMind resolved -- reports
arrive through the `wait` tool above instead. `relevo help --json push`
documents both routes of the verb.

**How reports arrive.** relevo identifies the MasterMind session itself -- the
plugin's hook registers it, and `relevo mastermind list` shows the records -- so no
verb has to guess who is calling. A report then reaches the MasterMind by exactly
one of four routes: the push holder (the Claude Code default where the mod
loads), the `wait` tool (where no holder runs), a deliverer for a harness
that has one (opencode, agy), or
`relevo wait` by hand. Nothing is ever typed into a terminal.

An agy MasterMind runs `relevo mastermind init` once inside agy, with no flags: relevo
detects the session from agy's own environment, so nothing has to be exported by
hand. Every relevo command that MasterMind runs refreshes the session's local
agentapi credentials, which relevo keeps 0600 under its state directory and never
prints. A report relevo pushes through those credentials wakes the idle agy
session, so an agy MasterMind is woken by a report rather than polling for it --
and that wake-up costs one turn of the agy session.
