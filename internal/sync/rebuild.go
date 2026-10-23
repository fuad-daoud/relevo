package sync

// The recovery from a replica this machine can no longer read.
//
// The replica is the file the sync driver owns. Left alone, a corrupt one meets
// the same torn page on every attempt and the machine falls behind until a
// human notices. Rebuilding it loses no data: the outbox is cleared only
// after the log confirmed the append, so what this machine has not yet handed
// over is still waiting; and the marks that say how far this machine has
// applied live in the machine-local file, not the replica, so a fresh replica
// re-reads the remote log and resumes exactly where the old one stopped. The
// stored token and key are local rows the fresh worker reads the same way the
// old one did, so the rebuild asks for no passphrase, no URL and no disable.
//
// The recovery is one ordered step -- stop the worker, move the replica and
// every driver file named after it into an aside folder, record that it
// happened -- behind a cap of one automatic rebuild a day. A fault that keeps
// coming back inside the cap is not answered with another move: the machine
// latches and names the manual rebuild, which is the retry verb's.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// WorkerRefusedText is the fixed sentence the sync pipe's refusal carries.
//
// The corrupt classification needs it because only a worker's answer can be
// about the replica's own file: the daemon's own machine-local file is a Turso
// file read through the same driver, and a "database is corrupt" from it must
// never move the replica aside. This package cannot import internal/syncpipe,
// which imports this package, so the sentence lives here and the pipe builds
// its refusal sentinel from it -- the two cannot drift, because there is only
// one text.
const WorkerRefusedText = "the sync worker refused"

// ErrNoReplica is what a rebuild with no replica file to move reports. An
// empty path means this machine never opened a worker; a path whose file is
// gone means there is nothing left to keep as evidence. Neither is a fault:
// the next attempt bootstraps a fresh replica either way.
var ErrNoReplica = errors.New("sync: no replica file to move aside")

// corruptNeedle is one shape the replica's own unreadability arrives in. need
// is the fragment or fragments one link of the error chain must all carry, and
// reason is what the shape stands for, so the table reads as the classification
// it is rather than as a list of strings.
type corruptNeedle struct {
	reason string
	need   []string
}

// The needles IsReplicaCorrupt matches. The first two are the driver's and
// SQLite's own wording for an unreadable file. The third is database/sql's
// wording for the NULL the log's `batch` column forbids: the column is
// INTEGER NOT NULL from the first format, so no upgrade can leave a legitimate
// NULL there, and a read that finds one is a read of a file the schema does not
// describe -- which is what the rest of the file then reported as a torn page.
var corruptNeedles = []corruptNeedle{
	{reason: "the driver's own wording for a torn page", need: []string{"database is corrupt"}},
	{reason: "SQLite's own wording for a damaged image", need: []string{"database disk image is malformed"}},
	{reason: "the NULL the log's NOT NULL batch column forbids",
		need: []string{"converting NULL to int", `name "batch"`}},
}

// IsReplicaCorrupt reports whether err is the replica's own file being
// unreadable rather than a remote or a network failure.
//
// Two things must hold, and both are matched on text because the corrupt shapes
// are the driver's and the database's own words, not classes anything in this
// tree raises. The chain must carry the worker's refusal marker, so only an
// answer the sync worker gave can classify -- the machine's own file reports
// the same driver words and must never move the replica. And one link of the
// chain must match a needle whole, meaning every fragment of one entry, because
// the NULL-batch shape is two fragments of one sentence.
func IsReplicaCorrupt(err error) bool {
	if err == nil {
		return false
	}
	marked, corrupt := false, false
	for e := err; e != nil && (!marked || !corrupt); e = errors.Unwrap(e) {
		text := e.Error()
		if strings.Contains(text, WorkerRefusedText) {
			marked = true
		}
		if corrupt {
			continue
		}
		for _, n := range corruptNeedles {
			if corruptMatches(text, n.need) {
				corrupt = true
				break
			}
		}
	}
	return marked && corrupt
}

// corruptMatches reports whether text carries every fragment of one needle
// entry, so an entry is matched whole or not at all.
func corruptMatches(text string, need []string) bool {
	for _, n := range need {
		if !strings.Contains(text, n) {
			return false
		}
	}
	return true
}

// Rebuild is the record one automatic rebuild leaves: when it ran and where
// the copy it kept is. It lives in the machine-local kv beside the breaker's
// keys, because it is a fact about this machine's file and no other machine
// needs it.
type Rebuild struct {
	At    time.Time `json:"at"`
	Aside string    `json:"aside,omitempty"`
}

const (
	// KeyRebuild is the machine-local kv key the rebuild record lives under,
	// beside the breaker's own keys.
	KeyRebuild = "sync.rebuild"

	// RebuildWindow is how long an automatic rebuild waits for the next one.
	// A corruption inside the window is the same fault answering again, and
	// moving the replica again teaches it nothing: the machine latches instead
	// and names the manual rebuild.
	RebuildWindow = 24 * time.Hour
)

// ReadRebuild returns the last rebuild the machine recorded, the zero value
// when it never has. A record that will not parse reads as none: the record
// exists to pace the recovery, and a record too corrupt to read must not wedge
// the machine it was written on -- it costs at most one move it should have
// paced.
func ReadRebuild(kv db.KV) (Rebuild, error) {
	body, ok, err := kv.KVGet(KeyRebuild)
	if err != nil {
		return Rebuild{}, fmt.Errorf("sync: rebuild record: %w", err)
	}
	if !ok {
		return Rebuild{}, nil
	}
	var r Rebuild
	if err := json.Unmarshal(body, &r); err != nil {
		return Rebuild{}, nil
	}
	return r, nil
}

// RebuildDue reports whether an automatic rebuild may run at now: a machine
// that never rebuilt is due, and so is one whose last rebuild is a window old.
// The boundary is inclusive, so a record exactly a window old is due with the
// ones older than it.
func RebuildDue(kv db.KV, now time.Time) (bool, error) {
	last, err := ReadRebuild(kv)
	if err != nil {
		return false, err
	}
	return last.At.IsZero() || !now.Before(last.At.Add(RebuildWindow)), nil
}

// WriteRebuild records a rebuild: when it ran and where the copy it kept is.
func WriteRebuild(kv db.KV, at time.Time, aside string) error {
	body, err := json.Marshal(Rebuild{At: at.UTC(), Aside: aside})
	if err != nil {
		return fmt.Errorf("sync: rebuild record: %w", err)
	}
	if err := kv.KVPut(KeyRebuild, body); err != nil {
		return fmt.Errorf("sync: rebuild record: %w", err)
	}
	return nil
}

// LatchCorruptReplica latches the machine on a corruption the cap will not
// answer with another move. The sentence is fixed text like every latch, and
// it names the one way out: the manual rebuild the retry verb carries.
func LatchCorruptReplica(kv db.KV, now time.Time) error {
	return SetAttention(kv, latchReplicaCorrupt, now)
}

// RebuildResult is what one rebuild moved: when it ran, the folder the copy it
// kept is in, and every file that went with it.
type RebuildResult struct {
	At    time.Time
	Aside string
	Moved []string
}

// RebuildReplica is the mechanism: stop the worker, move the files, record.
//
// The order is not best-effort. The worker stops first because a replica a
// running worker holds open cannot be renamed, and because the supervisor keeps
// a worker that refused -- a corrupt read is a refusal, not a death, so the
// worker that produced it is still alive here. A move that fails returns
// before anything is recorded, so the cap is not spent on a rebuild that did
// not happen and the next attempt tries again.
func (r *Runner) RebuildReplica(now time.Time) (RebuildResult, error) {
	if r == nil || r.Local == nil {
		return RebuildResult{}, errNoSync
	}
	if err := stopWorker(r.Client); err != nil {
		return RebuildResult{}, fmt.Errorf("sync: stop the worker: %w", err)
	}
	aside, moved, err := asideReplica(r.ReplicaPath)
	if err != nil {
		return RebuildResult{}, err
	}
	if err := WriteRebuild(r.Local, now, aside); err != nil {
		return RebuildResult{At: now, Aside: aside, Moved: moved}, fmt.Errorf("sync: record the rebuild: %w", err)
	}
	return RebuildResult{At: now, Aside: aside, Moved: moved}, nil
}

// RebuildCorrupt is the policy over the mechanism: rebuild while the cap
// allows, and latch with the manual sentence once it does not. rebuilt is
// false when the machine was latched instead, and the files were not touched.
func (r *Runner) RebuildCorrupt(now time.Time) (RebuildResult, bool, error) {
	if r == nil || r.Local == nil {
		return RebuildResult{}, false, errNoSync
	}
	due, err := RebuildDue(r.Local, now)
	if err != nil {
		return RebuildResult{}, false, err
	}
	if !due {
		if err := LatchCorruptReplica(r.Local, now); err != nil {
			return RebuildResult{}, false, fmt.Errorf("sync: latch the corrupt replica: %w", err)
		}
		return RebuildResult{}, false, nil
	}
	out, err := r.RebuildReplica(now)
	if err != nil {
		return RebuildResult{}, false, err
	}
	return out, true, nil
}

// asideDirName is the aside folder's fixed name, beside the replica in the
// state directory. Fixed on purpose: a rebuild removes the copy before it and
// moves the new one in, so a machine that rebuilds once a day for a month
// holds one copy, not thirty. The folder is evidence a human may want to look
// at, not a row anything syncs, and nothing reads it back.
const asideDirName = "relevo-sync-aside"

// asideDir is the folder a replica's copy is kept in: beside it, in the
// directory the replica itself lives in.
func asideDir(replicaPath string) string {
	return filepath.Join(filepath.Dir(replicaPath), asideDirName)
}

// asideReplica moves the replica and every driver file named after it into the
// aside folder, and returns the folder and the files that went, in the order
// they were moved.
//
// The order is the contract. The previous copy goes first, so the folder
// always holds one copy. The siblings go next and the main file last, so a
// move that fails halfway leaves the machine a replica it can still be
// bootstrapped from rather than a state directory that has lost its file and
// kept only its sidecars. A file that is not there is not moved and is not an
// error; a replica with no file at all is ErrNoReplica, because there is
// nothing left to keep.
func asideReplica(path string) (string, []string, error) {
	if path == "" {
		return "", nil, ErrNoReplica
	}
	files, err := ReplicaFiles(path)
	if err != nil {
		return "", nil, fmt.Errorf("sync: list the replica's files: %w", err)
	}
	if len(files) == 0 {
		return "", nil, ErrNoReplica
	}
	aside := asideDir(path)
	if err := os.RemoveAll(aside); err != nil {
		return "", nil, fmt.Errorf("sync: clear the previous aside copy: %w", err)
	}
	if err := os.MkdirAll(aside, 0o700); err != nil {
		return "", nil, fmt.Errorf("sync: make the aside folder: %w", err)
	}
	base := filepath.Base(path)
	var moved []string
	for _, full := range files {
		if filepath.Base(full) == base {
			continue
		}
		if err := os.Rename(full, filepath.Join(aside, filepath.Base(full))); err != nil {
			return aside, moved, fmt.Errorf("sync: move %s aside: %w", filepath.Base(full), err)
		}
		moved = append(moved, full)
	}
	if err := os.Rename(path, filepath.Join(aside, base)); err != nil {
		return aside, moved, fmt.Errorf("sync: move the replica aside: %w", err)
	}
	return aside, append(moved, path), nil
}

// stopWorker stops the worker that holds the replica open, so the files can be
// renamed. A transport that can close closes: the supervisor's Close takes its
// call lock and drops only the worker it holds, which it kills and reaps, and
// the next call starts a fresh one -- so the stop costs the pipe nothing it
// cannot rebuild. Every other transport is cancelled the way a step overrun
// is.
func stopWorker(client synclog.LogTransport) error {
	if closer, ok := client.(interface{ Close() error }); ok {
		return closer.Close()
	}
	cancelWorker(client)
	return nil
}
