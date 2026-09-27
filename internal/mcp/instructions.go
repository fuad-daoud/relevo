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
    report, prefixed with which binding and round it is from. A very large
    report is cut short, and the block ends with a line naming the
    relevo show command that prints it in full. Run the project's check
    command and compare the diff against the plan before calling done -- do
    not call done on the report's arrival alone.
  - kind="state" state="needs_you": a binding is stalled on a human
    decision (a repeated failure, an ambiguous plan). Read the body's
    reason, then run relevo status --name <binding> and decide: gate, or
    stop.

An event for a binding you did not personally send still belongs to you --
every binding on this MasterMind shares this one channel. Do not ignore an event
because you do not recognize the binding name; run relevo status to catch up.

Three verbs are tools here, callable directly instead of through the shell:

  - status(name?, all?): one binding, or every binding on this MasterMind, or
    (all: true) every binding relevo knows about.
  - send(name, file, tier?, verify?, regate?, dry_run?): hand a binding's
    runner a new round.
  - done(name): mark a binding done once its round is verified.

Every other relevo verb -- bind, show, wait, gate, config, and the rest --
is not a tool here; run it with Bash.
`

// InstructionsTools is the model-facing prelude for tools mode: nothing is
// pushed, so the model runs a background wait after every send.
const InstructionsTools = `relevo is running in tools mode: no events arrive on their own. Everything
relevo tells you arrives as the output of a command you started.

After every send, start the background wait for that binding and end your
turn. The send tool's result carries the exact command; it looks like this:

  background wait (run with run_in_background, then end your turn):
    relevo wait --name <binding> --timeout <budget>

Run that with the Bash tool's run_in_background, then end your turn. Claude
Code re-invokes you when the command exits, with its output in the new turn.
The wait's output is the report text -- the same payload a channel event would
have carried -- and it marks the entry delivered. An unmarked or halted
round's report is printed by relevo wait too.

Act on the wait's output after every wait exit except WaitTimeout.

  - Report text: a runner's round closed. Run the project's check command
    and compare the diff against the plan before calling done -- do not call
    done on the report's arrival alone.
  - A needs-you outcome: relevo wait's own line gives the reason; run
    relevo status --name <binding> and decide: send the next round,
    gate, or stop.
  - WaitTimeout (the round is still running): run
    relevo status --name <binding>, and start the background wait again if
    the round is still open.
  - An outcome line with no report text: another route already delivered the
    report; nothing is owed.

A report or a needs_you for a binding you did not personally send still
belongs to you -- every binding on this MasterMind is yours. Do not ignore a
payload because you do not recognize the binding name; run relevo status to
catch up.

Three verbs are tools here, callable directly instead of through the shell:

  - status(name?, all?): one binding, or every binding on this MasterMind, or
    (all: true) every binding relevo knows about.
  - send(name, file, tier?, verify?, regate?, dry_run?): hand a binding's
    runner a new round.
  - done(name): mark a binding done once its round is verified.

Every other relevo verb -- bind, show, wait, gate, config, and the rest --
is not a tool here; run it with Bash.
`

// InstructionsFor picks the mode's prelude and appends the shared guide: the
// guide is the last thing either mode's model reads.
func InstructionsFor(mode Mode) string {
	prelude := InstructionsTools
	if mode == ModeChannel {
		prelude = InstructionsChannel
	}
	return prelude + "\n" + mastermind.Guide()
}
