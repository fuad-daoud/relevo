package mcp

import "github.com/fuad-daoud/relevo/internal/mastermind"

// InstructionsChannel is the model-facing prelude for channel mode: events
// arrive as <channel source="relevo"> blocks, and the model acts on each.
const InstructionsChannel = `relevo is handing you round events over this channel instead of
typing them into your input box. A <channel source="relevo" ...> block can
arrive at any time, including mid-turn; when it does, act on it -- it is not
a distraction from your current task, it is the next step of it.

Event kinds, from the block's kind attribute:

  - kind="report": a runner's round closed. The block's body is the
    output, prefixed with which binding and round it is from. A very large
    output is cut short, and the block ends with a line naming the
    relevo show command that prints it in full. Run the project's check
    command and compare the diff against the plan before calling done -- do
    not call done on the output's arrival alone.
  - kind="state" state="needs_you": a binding is stalled on a human
    decision (a repeated failure, an ambiguous plan). Read the body's
    reason, then run relevo status --name <binding> and decide: gate, or
    stop.

An event for a binding you did not personally send still belongs to you --
every binding on this MasterMind shares this one channel. Do not ignore an event
because you do not recognize the binding name; run relevo status to catch up.

Five verbs are tools here, callable directly instead of through the shell:

  - status(name?, all?): one binding, or every binding on this MasterMind, or
    (all: true) every binding relevo knows about.
  - send(name, file, tier?, verify?, regate?, dry_run?): hand a binding's
    runner a new round.
  - done(name): mark a binding done once its round is verified.
  - show(name, round?, section?): read one round -- prompt (the default),
    report, diff, drift, log, transcript, gate log, output or artifacts.
  - gate(token, for?, reason?, clear?): record a provider rate limit, or clear
    it with clear: true.

Every other relevo verb -- bind, wait, config, and the rest -- is not a tool
here; run it with Bash. relevo help --json lists every verb, its flags, its
output document and its error codes.
`

// InstructionsTools is the model-facing prelude for tools mode: nothing is
// pushed, so the model waits with the blocking wait tool.
const InstructionsTools = `relevo is running in tools mode: no events arrive on their own. Everything
relevo tells you arrives as the output of a call you made.

After every send, close the round with the wait tool for that binding. The send
tool's result ends by pointing at it, carrying the round's budget; it looks
like this:

  wait tool:
    wait(name: "<binding>", timeout: "<budget>")

Call wait and it blocks until the round closes, needs attention, or its
timeout elapses. The result is the round's output text -- the same payload a
channel event would have carried -- and it marks the entry delivered.

Omit name to wait on every active binding this MasterMind owns, and round to
wait for a specific round; omitting round waits for the newest planned one.

The first line of every wait result names the binding, the round and one
outcome: closed, unmarked, needs-you, halted, gone, not-started, or still-open.
Act on it like this:

  - closed, with payload text: a runner's round closed. Run the project's
    check command and compare the diff against the plan before calling done --
    do not call done on the payload's arrival alone.
  - needs-you, unmarked, halted, gone or not-started: the same line carries the
    reason; run status(name) and decide: send the next round, gate, or stop.
  - still-open: the round is still running. Call wait again when you want to
    wait longer.
  - An outcome line with no payload text: another route already delivered the
    output; nothing is owed.

A very long payload comes back as the outcome line plus a "relevo show"
command instead of the text; run it to read the whole report.

An output or a needs_you for a binding you did not personally send still
belongs to you -- every binding on this MasterMind is yours. Do not ignore a
payload because you do not recognize the binding name; call status to catch up.

While a wait is blocked the server sends a progress notification every half
minute, which keeps the call alive; there is nothing to poll and no background
shell to start.

In headless "claude -p" there is no turn to end and no progress notification, so
the wait's result comes back inline in the same reply.

Six verbs are tools here, callable directly instead of through the shell:

  - status(name?, all?): one binding, or every binding on this MasterMind, or
    (all: true) every binding relevo knows about.
  - send(name, file, tier?, verify?, regate?, dry_run?): hand a binding's
    runner a new round.
  - wait(name?, round?, timeout?): block until a round closes, needs attention,
    or the timeout elapses.
  - done(name): mark a binding done once its round is verified.
  - show(name, round?, section?): read one round -- prompt (the default),
    report, diff, drift, log, transcript, gate log, output or artifacts.
  - gate(token, for?, reason?, clear?): record a provider rate limit, or clear
    it with clear: true.

Chains are not waitable from here; wait on a chain with "relevo wait --name
<chain>" in the shell.

Every other relevo verb -- bind, config, and the rest -- is not a tool here;
run it with Bash. relevo help --json lists every verb, its flags, its output
document and its error codes.
`

// InstructionsOpencode is the model-facing prelude for an OpenCode MasterMind.
// Outputs arrive as new turns pushed by relevo's opencode deliverer; there is
// no wait to start, and the tools are the five verbs.
const InstructionsOpencode = `relevo is running as your tools server. Outputs and needs-you payloads arrive
as new turns in this session, pushed by relevo itself: after a send, end your
turn and the output comes on its own. Do not start a wait.

Act on an output as soon as it arrives: run the project's check command and
compare the diff against the plan before calling done -- do not call done on
the output's arrival alone. A needs-you turn gives the reason; call
relevo_status and decide: send the next round, gate, or stop.

An output or a needs_you for a binding you did not personally send still
belongs to you -- every binding on this MasterMind is yours. Do not ignore a
turn because you do not recognize the binding name; call relevo_status to
catch up.

Five verbs are tools here, callable directly instead of through the shell:

  - status(name?, all?): one binding, or every binding on this MasterMind, or
    (all: true) every binding relevo knows about.
  - send(name, file, tier?, verify?, regate?, dry_run?): hand a binding's
    runner a new round.
  - done(name): mark a binding done once its round is verified.
  - show(name, round?, section?): read one round -- prompt (the default),
    report, diff, drift, log, transcript, gate log, output or artifacts.
  - gate(token, for?, reason?, clear?): record a provider rate limit, or clear
    it with clear: true.

Every other relevo verb -- bind, wait, config, and the rest -- is not a tool
here; run it with the shell. relevo help --json lists every verb, its flags,
its output document and its error codes.
`

// InstructionsFor picks the kind's or mode's prelude and appends the shared
// guide: the guide is the last thing the model reads.
func InstructionsFor(mode Mode, kind string) string {
	prelude := InstructionsTools
	switch {
	case kind == "opencode":
		prelude = InstructionsOpencode
	case mode == ModeChannel:
		prelude = InstructionsChannel
	}
	return prelude + "\n" + mastermind.Guide()
}
