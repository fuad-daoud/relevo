package pick

import (
	"context"
	"errors"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// stdoutStat is os.Stdout.Stat, replaceable so the terminal refusal path can
// be tested without a terminal.
var stdoutStat = os.Stdout.Stat

// Run opens the picker for opts.Verb and returns when the human is done.
//
// Preconditions:  stdout is a character device; rt.Store non-nil.
// Postconditions: the terminal is restored. On ErrCancelled, "cancelled" has
// been printed to stdout after the alternate screen closed.
// Errors:         nil after a successful verb; ErrCancelled, ErrNothingToPick
// or ErrVerbFailed for the three outcomes already shown on screen; any other
// error is a startup or terminal failure the caller should print.
func Run(ctx context.Context, rt relevo.Runtime, opts Options) error {
	info, err := stdoutStat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return errors.New("--pick needs a terminal; name the binding instead")
	}
	if rt.Store == nil {
		return errors.New("runtime requires Store")
	}

	p := tea.NewProgram(newModel(ctx, rt, opts), tea.WithAltScreen(), tea.WithContext(ctx))
	final, err := p.Run()
	m, _ := final.(Model)
	err = runResult(ctx, err, m)
	if errors.Is(err, ErrCancelled) {
		fmt.Println("cancelled")
	}
	return err
}

// runResult maps bubbletea's exit into Run's contract. A cancelled context
// is a cancel, not a crash; a clean exit returns whatever the model decided.
// Kept separate from Run so it can be tested without a tty.
func runResult(ctx context.Context, err error, m Model) error {
	if ctx.Err() != nil && (errors.Is(err, tea.ErrProgramKilled) || errors.Is(err, tea.ErrInterrupted)) {
		return ErrCancelled
	}
	if err != nil {
		return err
	}
	return m.outcome
}
