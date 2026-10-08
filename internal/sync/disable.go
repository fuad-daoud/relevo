package sync

import (
	"context"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// Turning sync off is four steps in a fixed order, and the order is the whole
// contract: make one last attempt to hand the remote what this machine holds,
// mark the machine off, forget the token, and close the handle. The push comes
// first because a machine that leaves with an unsent round is a machine whose
// cloud copy silently lacks it, and the token goes after the mark because a
// machine marked off with a live token is a machine the next enable would
// inherit a credential for without being asked.
//
// Nothing else is touched. The local files keep every row they hold and stay
// servable, and the remote is left alone: the user deletes that with Turso's own
// tooling, because a typo must not be able to destroy a record.

// The steps turn-off runs, in order. They are named rather than numbered in the
// message so a caller reporting one can say which step it was.
const (
	stepFinalPush  = "final push"
	stepMarkOff    = "mark off"
	stepDeleteToke = "delete token"
	stepClose      = "close handle"
)

// pusher is the one call the turn-off's final attempt makes: hand the machine's
// pending rows over before the mark goes off. It is a single call rather than a
// whole transport because the turn-off reads and lists nothing; a machine with
// nothing to hand over through is skipped rather than failed.
type pusher interface {
	Push(ctx context.Context) error
}

// Disabler runs one turn-off. Every input it cannot answer for itself is a
// field, so the whole path is drivable with no remote and no handle.
type Disabler struct {
	// Local is the machine-local file: the section, the mark and the token all
	// live there.
	Local Local
	// Client is the handle the final push goes through. Nil means this machine
	// has no handle open, so there is nothing to push through and the attempt
	// is skipped rather than failed.
	Client pusher
	// Close releases the handle. Nil means the caller has nothing to close, and
	// the step is recorded and skipped so the order still reads whole.
	Close func() error
	// Now stamps the mark. Nil means time.Now.
	Now func() time.Time
	// Timeout bounds the final push. It is the reason the attempt is
	// best-effort rather than merely ignored: a remote that never answers costs
	// one bounded wait and not a hung turn-off. Zero means DefaultTimeout.
	Timeout time.Duration
}

// DisableResult is what one turn-off did. Steps is the order it ran in, which
// is the contract a caller and a test read the same way.
type DisableResult struct {
	// Steps is the order the turn-off took, one name per step.
	Steps []string
	// FinalPush is whether the final push attempt reported success. It is false
	// on a machine with no handle to push through, which is not a failure.
	FinalPush bool
	// FinalPushErr is what the final push hit, kept so the caller can report it
	// as a warning. It never decides anything: a remote that cannot be reached
	// is the reason a machine leaves, not a reason it stays.
	FinalPushErr error
	// Closed is whether a handle was released.
	Closed bool
}

// Disable runs the four steps in order. The final push is the only step whose
// failure is kept rather than returned, and it is kept precisely so the other
// three still run: a machine that cannot reach its remote has the strongest
// reason of all to stop syncing, and refusing to stop would leave it pushing at
// a remote it cannot reach.
//
// Every later step is a refusal that returns. That is deliberate: a machine
// whose mark was not written would keep ticking, and a machine whose token was
// not deleted would keep a credential it no longer uses. Either is worth
// telling the user about rather than reporting a clean turn-off.
func (d *Disabler) Disable(ctx context.Context) (DisableResult, error) {
	var out DisableResult
	out.Steps = append(out.Steps, stepFinalPush)
	d.finalPush(ctx, &out)

	out.Steps = append(out.Steps, stepMarkOff)
	if err := MarkEnabled(d.Local, false, d.now()); err != nil {
		return out, fmt.Errorf("sync: disable: %w", err)
	}

	out.Steps = append(out.Steps, stepDeleteToke)
	if err := DeleteToken(d.Local); err != nil {
		return out, fmt.Errorf("sync: disable: %w", err)
	}

	out.Steps = append(out.Steps, stepClose)
	if d.Close != nil {
		if err := d.Close(); err != nil {
			return out, fmt.Errorf("sync: disable: close the handle: %w", err)
		}
		out.Closed = true
	}
	return out, nil
}

// finalPush makes the one attempt the turn-off gets, under the bound. It
// records what happened either way and returns nothing: whatever went wrong
// here is a warning for the caller, not a decision for this path.
func (d *Disabler) finalPush(ctx context.Context, out *DisableResult) {
	if d.Client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, d.timeout())
	defer cancel()

	if err := d.Client.Push(ctx); err != nil {
		out.FinalPushErr = fmt.Errorf("sync: disable: the final push did not land: %w", err)
		return
	}
	out.FinalPush = true
}

func (d *Disabler) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *Disabler) timeout() time.Duration {
	if d.Timeout > 0 {
		return d.Timeout
	}
	return DefaultTimeout
}

// Enabled reports whether this machine is syncing, read out of the local mark
// and nothing else. It is what a status verb answers with, and answering it
// costs no remote and no handle: a machine whose remote is down still says what
// it is set to be.
func Enabled(l Local) (bool, error) {
	if l == nil {
		return false, fmt.Errorf("sync: no local file beside the shared database: %w", db.ErrInvalid)
	}
	state, err := ReadState(l)
	if err != nil {
		return false, err
	}
	return state.Enabled, nil
}
