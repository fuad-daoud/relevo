package ui

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// Options configures the reader. Interval is the only knob: the ReadAgent line
// count is always the viewport height, so it is not one.
type Options struct {
	// Interval is the list poll period. Floored at minInterval, default 2s to
	// match the daemon tick.
	Interval time.Duration

	// Prefs is where the ui keeps its own preferences (spec §6.0; P3b plan
	// §4.4). A zero Prefs (KV nil) keeps the ui stateless -- nothing loaded,
	// nothing saved.
	Prefs PrefsStore

	// Notice is shown once, on the first frame, and cleared on the first
	// keypress like any other notice -- cmdUI's own db-open failure
	// ("no database: <err>") lands here so the ui still runs, in live
	// scope, rather than ever failing to start over it (§6).
	Notice string

	// PipeHint is the full refusal line RunSource prints when stdout is
	// not a terminal -- not a suffix. "" keeps the mastermind's own text,
	// which names `relevo status`.
	PipeHint string

	// Start is the command line to run once the first status has arrived,
	// e.g. `rounds harness:agy`. "" starts at :fleet.
	Start string

	// Actions is the cockpit's write seam (§1, §4.2). Nil hides every
	// action key and makes one do nothing when pressed: `relevo serve ui`
	// passes none, and `relevo ui` gets the mastermind adapter Run builds.
	Actions Actions

	// ProbeExec is what Actions.Probe runs a candidate's harness through
	// (§4.2): cmd/relevo passes its os/exec seam so a probe spawns a real
	// harness from the cockpit.
	ProbeExec availability.LineExec

	// Version is the running binary's version string (§2.2). "" hides the
	// version in the header.
	Version string
}

const minInterval = 500 * time.Millisecond
const defaultInterval = 2 * time.Second

// stdoutStat is os.Stdout.Stat, replaceable so the terminal refusal path can be
// tested without a terminal.
var stdoutStat = os.Stdout.Stat

// Run renders relevo's state until the user quits or ctx is cancelled.
// It never mutates state on its own: every write goes through Actions, which
// Run fills with the real mastermind adapter when the caller passed none (§1).
//
// Preconditions:  stdout is a character device; rt.Store non-nil.
// Postconditions: the terminal is restored, including on panic.
// Errors:         startup failures only. Refresh failures never escape.
func Run(ctx context.Context, rt relevo.Runtime, opts Options) error {
	if notTTY() {
		return pipeRefusal(opts.PipeHint)
	}
	if rt.Store == nil {
		return errors.New("runtime requires Store")
	}
	live := newLiveRuntime(rt)
	if opts.Actions == nil {
		opts.Actions = &mastermindActions{live: live, repo: repoRoot(ctx, rt), probe: opts.ProbeExec}
	}
	return RunSource(ctx, liveSource{live}, opts)
}

// repoRoot is the directory bind would create a worktree of: os.Getwd(), or
// "" when it cannot be read or is not inside a git repository (§4.2). A nil
// Git can tell nothing, so it counts as not a repo.
func repoRoot(ctx context.Context, rt relevo.Runtime) string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	if rt.Git == nil {
		return ""
	}
	if _, err := rt.Git.HeadCommit(ctx, cwd); err != nil {
		return ""
	}
	return cwd
}
