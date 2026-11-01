package relevo

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/store"
)

// builderPrompt is the fixed handoff template. It names both paths explicitly
// because alternate-screen output is unrecoverable, so the report must be a
// file rather than something relevo reads off the terminal. The marker is the
// builder's own "the tree is final": relevo closes the round on it, not on the
// report appearing.
//
// It opens by naming the working tree and a halt rule: a headless
// agy builder has been observed to run its shell somewhere else and execute
// a round against the mastermind's main checkout instead of its own worktree.
// Telling the builder which tree is its own, and to check with `git status`
// before doing anything else, is relevo's second line of defence alongside
// pinning the process's workspace with --add-dir.
const builderPrompt = `Your working tree is: %s
It is the only tree you may touch. Before anything else, run ` + "`git status`" + `
there. If that fails, or reports a different directory or branch than you
expect for this tree, stop: write a report saying so, create the done marker,
and do nothing else.

Read: %s
When you are done, write your report to: %s
Then, as the very last thing you do -- after every edit, test and commit --
create this empty file: %s
End the report with this block as its last lines, filled in honestly:

` + "```relevo" + `
status: done            # done | halted | blocked | deferred
halted_at: ""           # which step, when halted or blocked
changed_paths: []       # repo-relative files you changed
commands_run: []        # every command you ran, as typed
not_done: []            # adjacent work you deliberately left
` + "```" + `
Reply here with only the report path.`

// readerPrompt is a reader round's handoff template: the reader works
// in a throwaway scratch copy and writes its actor's output label in full to
// that label's file in the round's artifact directory -- the named state-dir
// path, which the close saves verbatim. Only a missing or empty file falls back
// to the stream. The first line is exactly `Your working tree is: ` because the
// e2e fake parses it, as it does for builderPrompt.
//
// It is a format string: scratch tree, b.CWD, prompt path, output label, output
// path, done marker.
const readerPrompt = `Your working tree is: %s
It is a throwaway copy of %s for this round: read anything in it, run
anything read-only, change nothing you need to keep -- it is discarded when
the round ends, and nothing in it is ever committed.

Read: %s
Write your %s to %s, in full and finished: that file is the round's saved
output and is kept verbatim. The repository stays read-only; the state
directory holding that path is the one place you may write.
Your final message is then only the short close -- the path you wrote and no
part of the report itself -- and it ends with this block, filled in honestly:

` + "```relevo" + `
status: done            # done | halted | blocked | deferred
halted_at: ""           # which step, when halted or blocked
changed_paths: []       # files you changed in the throwaway tree
commands_run: []        # commands you ran
not_done: []            # what you deliberately left
` + "```" + `
Then create this empty file: %s
The message carrying the block must be the last text you write; the marker is
your final action, created after that message, with nothing written after the marker.`

// chainReaderPrompt is readerPrompt for a chain member: the chain's own block
// -- the reviewer's verdict or the security member's finding count -- replaces
// the reporttail block, and the marker is created before the final message so
// the block is both the round's last text and the artifact the round saves. A
// chain reader that obeyed readerPrompt's "nothing written after the marker"
// rule could only end on an empty reply, and the chain parsed no verdict or
// finding count. The output file is named here exactly as readerPrompt names
// it, so the path wording cannot drift between the two lanes.
//
// It is a format string: scratch tree, b.CWD, prompt path, output label, output
// path, done marker, block.
const chainReaderPrompt = `Your working tree is: %s
It is a throwaway copy of %s for this round: read anything in it, run
anything read-only, change nothing you need to keep -- it is discarded when
the round ends, and nothing in it is ever committed.

Read: %s
Write your %s to %s, in full and finished: that file is the round's saved
output and is kept verbatim. The repository stays read-only; the state
directory holding that path is the one place you may write.
When your review is complete, create this empty file: %s
Then write your final message -- the short close only, naming the path you
wrote. It must end with this block, filled in honestly:

` + "```relevo" + `
%s
` + "```" + ``

// promptHead is enough of the prompt for a human to recognise the handoff
// without printing the whole template.
func promptHead(prompt string) []string {
	var out []string
	for _, line := range strings.Split(prompt, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, line)
		if len(out) == 2 {
			break
		}
	}
	return out
}

// absoluteOr resolves path against the current directory when it can, so a dry
// run can report where the plan came from even when the caller typed a
// relative path.
func absoluteOr(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// composePrompt renders this round's handoff prompt: the reader template for a
// reader binding, the builder template for a writer. rt names a reader's
// scratch tree, artifact dir and output label.
func composePrompt(rt Runtime, b store.Binding, promptPath, reportPath, donePath string) string {
	origin := delivery.OriginLine(b.Name, b.Round, store.DirToBuilder, store.KindPrompt)
	if b.Shape == store.ShapeReader {
		return origin + "\n\n" + readerPromptFor(rt, b, promptPath, donePath)
	}
	body := fmt.Sprintf(builderPrompt, b.CWD, promptPath, reportPath, donePath)
	return origin + "\n\n" + body
}

func composeChainReaderPrompt(rt Runtime, b store.Binding, promptPath, donePath, block string) string {
	origin := delivery.OriginLine(b.Name, b.Round, store.DirToBuilder, store.KindPrompt)
	return origin + "\n\n" + chainReaderPromptFor(rt, b, promptPath, donePath, block)
}

// roundPrompt composes one round's handoff prompt. It is the one composer every
// round starter uses -- the send path, the served admit, a candidate switch and
// a relaunch after a daemon restart -- so a chain member's reader round gets the
// chain's own block wherever the round actually starts.
func roundPrompt(rt Runtime, tx *store.Tx, b store.Binding, promptPath, reportPath, donePath string) string {
	prompt := composePrompt(rt, b, promptPath, reportPath, donePath)
	if b.Shape == store.ShapeReader {
		if block, ok := chainMemberBlock(rt, tx, b); ok {
			prompt = composeChainReaderPrompt(rt, b, promptPath, donePath, block)
		}
	}
	return prompt
}

// chainReaderPromptFor renders chainReaderPrompt for one chain reader round,
// resolving the output label and path exactly as readerPromptFor does.
func chainReaderPromptFor(rt Runtime, b store.Binding, promptPath, donePath, block string) string {
	actor := b.WriterRole()
	label := readerOutputLabel(rt, b)
	outputPath := rt.Store.OutputPath(b.Name, b.Round, actor, label)
	return fmt.Sprintf(chainReaderPrompt, housekeep.RoundTree(HousekeepDeps(rt), b), b.CWD, promptPath, label, outputPath, donePath, block)
}

// readerPromptFor renders readerPrompt for one reader round. The output label
// is the actor's resolved agent definition's label (ActorOutput), which
// defaults to "notes" when nothing names one. The definition falls back to the
// actor name when the role's spec cannot be resolved, so a shipped actor still
// gets its own label. The saved path is the output path reportPathFor records,
// so the prompt and the close cannot drift.
func readerPromptFor(rt Runtime, b store.Binding, promptPath, donePath string) string {
	actor := b.WriterRole()
	label := readerOutputLabel(rt, b)
	outputPath := rt.Store.OutputPath(b.Name, b.Round, actor, label)
	return fmt.Sprintf(readerPrompt, housekeep.RoundTree(HousekeepDeps(rt), b), b.CWD, promptPath, label, outputPath, donePath)
}
