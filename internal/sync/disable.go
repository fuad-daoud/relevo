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

// Turning sync off is seven steps in a fixed order: final export, mark off, delete
// token and key, stop the worker, delete the replica and its driver files, drop
// the client. The attempt comes first so no unsent round is silently left behind;
// the token goes after the mark so no live credential outlives the mark for the
// next enable to inherit; the worker stops before its file goes because a held-open
// replica cannot be removed. Nothing else is touched: the shared file stays
// servable and the remote is left alone, for Turso's own tooling to delete, so
// a typo cannot destroy a record.

// The steps turn-off runs, in order: named so a reporter says which step it was.
const (
	stepFinalPush     = "final export"
	stepMarkOff       = "mark off"
	stepDeleteToke    = "delete token"
	stepDeleteKey     = "delete key"
	stepStopWorker    = "stop worker"
	stepDeleteReplica = "delete replica"
	stepDropClient    = "drop client"
)

// Disabler runs one turn-off; every unanswerable input is a field, so the path is
// drivable with no remote and no handle.
type Disabler struct {
	// Local is the machine-local file holding section, mark and token.
	Local Local
	// Shared is the file the signed-from records live in; nil skips clearing them.
	Shared *db.DB
	// FinalExport is the turn-off's one final-attempt call: drain pending rows into
	// the log before the mark goes off. Nil means nothing to hand over, so the
	// attempt is skipped rather than failed.
	FinalExport func(ctx context.Context) error
	// Stop stops the worker owning the replica so the file deletes rather than held
	// open. Nil means no worker was started; the step is recorded and skipped.
	Stop func() error
	// ReplicaPath names the replica file; it and every driver file named after it
	// go. Empty means no worker was ever opened and nothing deletes.
	ReplicaPath string
	// StagingDir is the folder bodies pass through to and from the bucket; it goes
	// with the replica, empty meaning none.
	StagingDir string
	// Drop releases the client the worker ran through. Nil means no client to drop.
	Drop func() error
	// Cancel releases any data call in flight, so a hung pull never stalls the
	// turn-off. Nil means no worker is reachable; a cancelled call is a deliberate
	// stop, not a death, leaving the breaker count unchanged.
	Cancel func()
	// Now stamps the mark. Nil means time.Now.
	Now func() time.Time
	// Timeout bounds the final export, making the attempt best-effort: a silent
	// remote costs one bounded wait, not a hung turn-off. Zero means DefaultTimeout.
	Timeout time.Duration
}

// DisableResult is what one turn-off did; Steps is the order it ran in, the
// contract caller and test read the same way.
type DisableResult struct {
	// Steps is the order the turn-off took, one name per step.
	Steps []string
	// FinalPush is whether the final export reported success; false with nothing to
	// export through, which is not a failure.
	FinalPush bool
	// FinalPushErr is what the final export hit, kept as a warning only: an
	// unreachable remote is why a machine leaves, not why it stays.
	FinalPushErr error
	// Stopped is whether a worker was stopped.
	Stopped bool
	// Deleted is every replica or driver file the turn-off removed.
	Deleted []string
	// Dropped is whether a client was released.
	Dropped bool
}

// Disable runs the seven steps in order. Only the final export's failure is kept
// rather than returned, so the rest still run: a machine that cannot reach its
// remote has the strongest reason to stop. Every later step returns on failure:
// an unwritten mark would keep ticking, an undeleted token would keep a live
// credential past need.
func (d *Disabler) Disable(ctx context.Context) (DisableResult, error) {
	var out DisableResult
	// A data call in flight releases first: the final export cannot run while a pull
	// holds the worker, and a disable during a join is the stop the join honours.
	d.cancel()
	out.Steps = append(out.Steps, stepFinalPush)
	d.finalPush(ctx, &out)

	out.Steps = append(out.Steps, stepMarkOff)
	if err := MarkEnabled(d.Local, false, d.now()); err != nil {
		return out, fmt.Errorf("sync: disable: %w", err)
	}
	// The join marker goes with the mark: off is not joining, and a leftover would
	// show a join on a machine that is not moving.
	if err := ClearJoin(d.Local); err != nil {
		return out, fmt.Errorf("sync: disable: clear the join marker: %w", err)
	}

	out.Steps = append(out.Steps, stepDeleteToke)
	if err := DeleteToken(d.Local); err != nil {
		return out, fmt.Errorf("sync: disable: %w", err)
	}
	out.Steps = append(out.Steps, stepDeleteKey)
	if err := DeleteKey(d.Local); err != nil {
		return out, fmt.Errorf("sync: disable: %w", err)
	}
	// The R2 credentials go with the token, on the same reasoning: no live bucket
	// credential outlives the mark for the next enable to inherit.
	if err := DeleteR2(d.Local); err != nil {
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
	if d.StagingDir != "" {
		if err := os.RemoveAll(d.StagingDir); err != nil {
			return out, fmt.Errorf("sync: disable: delete the staging folder: %w", err)
		}
		deleted = append(deleted, d.StagingDir)
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

// finalPush makes the turn-off's one attempt under the bound, recording either
// way and returning nothing: a failure here warns the caller, never decides.
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

// ReplicaFiles lists the replica and every driver file named after it by prefix; a
// missing directory lists nothing. The turn-off deletes exactly this set and the
// daemon's start tightens it.
func ReplicaFiles(path string) ([]string, error) {
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

	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name != base && !strings.HasPrefix(name, base+"-") && !strings.HasPrefix(name, base+".") {
			continue
		}
		files = append(files, filepath.Join(dir, name))
	}
	return files, nil
}

// deleteReplica removes the replica and every driver file named after it; a missing
// file is not a failure.
func deleteReplica(path string) ([]string, error) {
	files, err := ReplicaFiles(path)
	if err != nil {
		return nil, err
	}
	var deleted []string
	for _, full := range files {
		if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
			return deleted, err
		}
		deleted = append(deleted, full)
	}
	return deleted, nil
}

// cancel releases any data call in flight when the caller installed one.
func (d *Disabler) cancel() {
	if d.Cancel != nil {
		d.Cancel()
	}
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

// Enabled reports whether this machine is syncing, from the local mark alone: a
// machine whose remote is down still says what it is set to be.
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
