package sync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// Turning sync off is six steps in a fixed order, and the order is the whole
// contract: make one last attempt to hand the remote what this machine holds,
// mark the machine off, forget the token, stop the worker, delete the replica
// and the driver's files beside it, and drop the client. The attempt comes
// first because a machine that leaves with an unsent round is a machine whose
// cloud copy silently lacks it, the token goes after the mark because a machine
// marked off with a live token is a machine the next enable would inherit a
// credential for without being asked, and the worker stops before its file goes
// because a replica a running worker holds open cannot be removed.
//
// Nothing else is touched. The shared file keeps every row it holds and stays
// servable, and the remote is left alone: the user deletes that with Turso's own
// tooling, because a typo must not be able to destroy a record.

// The steps turn-off runs, in order. They are named rather than numbered in the
// message so a caller reporting one can say which step it was.
const (
	stepFinalPush     = "final export"
	stepMarkOff       = "mark off"
	stepDeleteToke    = "delete token"
	stepStopWorker    = "stop worker"
	stepDeleteReplica = "delete replica"
	stepDropClient    = "drop client"
)

// Disabler runs one turn-off. Every input it cannot answer for itself is a
// field, so the whole path is drivable with no remote and no handle.
type Disabler struct {
	// Local is the machine-local file: the section, the mark and the token all
	// live there.
	Local Local
	// FinalExport is the one call the turn-off's final attempt makes: drain
	// this machine's pending rows into the log before the mark goes off. It is
	// a single call rather than a whole transport because the turn-off reads
	// and lists nothing else; nil means this machine has nothing to hand over
	// through and the attempt is skipped rather than failed.
	FinalExport func(ctx context.Context) error
	// Stop stops the worker that owns the replica, so the file can be deleted
	// rather than held open. Nil means no worker was started and the step is
	// recorded and skipped so the order still reads whole.
	Stop func() error
	// ReplicaPath names the replica file. It and every driver file named after
	// it go; empty means this machine never opened a worker and there is
	// nothing to delete.
	ReplicaPath string
	// Drop releases the client the worker was driven through. Nil means the
	// caller keeps no client and the step is skipped.
	Drop func() error
	// Now stamps the mark. Nil means time.Now.
	Now func() time.Time
	// Timeout bounds the final export. It is the reason the attempt is
	// best-effort rather than merely ignored: a remote that never answers costs
	// one bounded wait and not a hung turn-off. Zero means DefaultTimeout.
	Timeout time.Duration
}

// DisableResult is what one turn-off did. Steps is the order it ran in, which
// is the contract a caller and a test read the same way.
type DisableResult struct {
	// Steps is the order the turn-off took, one name per step.
	Steps []string
	// FinalPush is whether the final export attempt reported success. It is
	// false on a machine with nothing to export through, which is not a failure.
	FinalPush bool
	// FinalPushErr is what the final export hit, kept so the caller can report
	// it as a warning. It never decides anything: a remote that cannot be
	// reached is the reason a machine leaves, not a reason it stays.
	FinalPushErr error
	// Stopped is whether a worker was stopped.
	Stopped bool
	// Deleted is every replica or driver file the turn-off removed.
	Deleted []string
	// Dropped is whether a client was released.
	Dropped bool
}

// Disable runs the six steps in order. The final export is the only step whose
// failure is kept rather than returned, and it is kept precisely so the other
// five still run: a machine that cannot reach its remote has the strongest
// reason of all to stop syncing, and refusing to stop would leave it pushing at
// a remote it cannot reach.
//
// Every later step is a refusal that returns. That is deliberate: a machine
// whose mark was not written would keep ticking, and a machine whose token was
// not deleted would keep a live credential past the point it needs one. Either
// is worth telling the user about rather than reporting a clean turn-off.
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

	out.Steps = append(out.Steps, stepStopWorker)
	if d.Stop != nil {
		if err := d.Stop(); err != nil {
			return out, fmt.Errorf("sync: disable: stop the worker: %w", err)
		}
		out.Stopped = true
	}

	out.Steps = append(out.Steps, stepDeleteReplica)
	deleted, err := deleteReplica(d.ReplicaPath)
	if err != nil {
		return out, fmt.Errorf("sync: disable: delete the replica: %w", err)
	}
	out.Deleted = deleted

	out.Steps = append(out.Steps, stepDropClient)
	if d.Drop != nil {
		if err := d.Drop(); err != nil {
			return out, fmt.Errorf("sync: disable: drop the client: %w", err)
		}
		out.Dropped = true
	}
	return out, nil
}

// finalPush makes the one attempt the turn-off gets, under the bound. It
// records what happened either way and returns nothing: whatever went wrong
// here is a warning for the caller, not a decision for this path.
func (d *Disabler) finalPush(ctx context.Context, out *DisableResult) {
	if d.FinalExport == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, d.timeout())
	defer cancel()

	if err := d.FinalExport(ctx); err != nil {
		out.FinalPushErr = fmt.Errorf("sync: disable: the final export did not land: %w", err)
		return
	}
	out.FinalPush = true
}

// deleteReplica removes the replica and every driver file named after it: the
// driver derives each of its files from the replica's own name, so the prefix
// is what identifies them. A file that is not there is not a failure, because a
// machine that never opened a worker has none, and the main file goes with its
// siblings rather than before them.
func deleteReplica(path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	dir := filepath.Dir(path)
	base := filepath.Base(path)

	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var deleted []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name != base && !strings.HasPrefix(name, base+"-") && !strings.HasPrefix(name, base+".") {
			continue
		}
		full := filepath.Join(dir, name)
		if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
			return deleted, err
		}
		deleted = append(deleted, full)
	}
	return deleted, nil
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
