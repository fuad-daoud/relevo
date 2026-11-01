package sync

// The recovery from a replica this machine can no longer read. Rebuilding loses
// no data: the outbox clears only after the log confirms the append, and applied
// marks live in the machine-local file, so a fresh replica resumes where the old
// one stopped with no passphrase, URL or disable. One ordered step -- stop the
// worker, move the replica and its driver files aside, record it -- behind a cap
// of one automatic rebuild a day; a fault inside the cap latches instead.

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

// WorkerRefusedText is the fixed sentence the sync pipe's refusal carries. Only a
// worker's answer can be about the replica's own file: a "database is corrupt"
// from the daemon's own file reports the same driver words and must never move
// the replica aside. syncpipe imports this package, so the sentence lives here and
// the pipe builds its sentinel from it: the two cannot drift with only one text.
const WorkerRefusedText = "the sync worker refused"

// ErrNoReplica is what a rebuild with no replica file to move reports: an empty
// path means this machine never opened a worker, and a missing file leaves nothing
// to keep as evidence. Neither is a fault; the next attempt bootstraps fresh
// either way.
var ErrNoReplica = errors.New("sync: no replica file to move aside")

// corruptNeedle is one shape the replica's unreadability arrives in: the fragments
// one chain link must all carry, and what the shape stands for.
type corruptNeedle struct {
	reason string
	need   []string
}

// The needles IsReplicaCorrupt matches: the driver's and SQLite's wording for an
// unreadable file, and database/sql's wording for the NULL the log's NOT NULL
// `batch` column forbids -- a read of a file the schema does not describe.
var corruptNeedles = []corruptNeedle{
	{reason: "the driver's own wording for a torn page", need: []string{"database is corrupt"}},
	{reason: "SQLite's own wording for a damaged image", need: []string{"database disk image is malformed"}},
	{reason: "the NULL the log's NOT NULL batch column forbids",
		need: []string{"converting NULL to int", `name "batch"`}},
}

// IsReplicaCorrupt reports whether err is the replica's own file unreadable rather
// than a remote or network failure. Two things must hold, and both are text-matched:
// these are the driver's words, not error classes this tree raises. The chain must
// carry the worker's refusal marker, so only a sync worker's answer classifies, and
// one link must match a needle whole -- every fragment of one entry.
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

// corruptMatches reports whether text carries every fragment of one needle entry.
func corruptMatches(text string, need []string) bool {
	for _, n := range need {
		if !strings.Contains(text, n) {
			return false
		}
	}
	return true
}

// Rebuild is the record one automatic rebuild leaves: when it ran and where the
// copy is, in the machine-local kv -- a fact about this machine's file no other
// machine needs.
type Rebuild struct {
	At    time.Time `json:"at"`
	Aside string    `json:"aside,omitempty"`
}

const (
	// KeyRebuild is the machine-local kv key the rebuild record lives under.
	KeyRebuild = "sync.rebuild"

	// RebuildWindow is how long an automatic rebuild waits for the next one: a
	// fault inside it is the same fault answering again, so the machine latches
	// and names the manual rebuild.
	RebuildWindow = 24 * time.Hour
)

// ReadRebuild returns the last recorded rebuild, the zero value when never. An
// unparsable record reads as none: a corrupt record must not wedge the machine,
// and it costs at most one move the cap should have paced.
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

// RebuildDue reports whether an automatic rebuild may run at now: due when never
// rebuilt, or when the last rebuild is a window old, boundary inclusive.
func RebuildDue(kv db.KV, now time.Time) (bool, error) {
	last, err := ReadRebuild(kv)
	if err != nil {
		return false, err
	}
	return last.At.IsZero() || !now.Before(last.At.Add(RebuildWindow)), nil
}

// WriteRebuild records a rebuild: when it ran and where the copy is.
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

// LatchCorruptReplica latches the machine on a corruption the cap will not move
// again, naming the one way out: the retry verb's manual rebuild.
func LatchCorruptReplica(kv db.KV, now time.Time) error {
	return SetAttention(kv, latchReplicaCorrupt, now)
}

// RebuildResult is what one rebuild moved: when it ran, the aside folder, and
// every file that went with it.
type RebuildResult struct {
	At    time.Time
	Aside string
	Moved []string
}

// RebuildReplica is the mechanism: stop the worker, move the files, record. The
// worker stops first because a held-open replica cannot be renamed -- a corrupt
// read is a refusal, not a death, so the worker is still alive. A failed move
// returns before recording, so the cap is not spent on a rebuild that never
// happened.
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

// RebuildCorrupt is the policy over the mechanism: rebuild while the cap allows,
// latch with the manual sentence once it does not. rebuilt is false when latched.
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

// asideDirName is the aside folder's fixed name, beside the replica. Fixed so a
// daily rebuild for a month holds one copy, not thirty: evidence, not a synced row.
const asideDirName = "relevo-sync-aside"

// asideDir is the folder a replica's copy is kept in, beside the replica itself.
func asideDir(replicaPath string) string {
	return filepath.Join(filepath.Dir(replicaPath), asideDirName)
}

// asideReplica moves the replica and every driver file named after it into the
// aside folder, returning folder and files in move order. The previous copy goes
// first so the folder holds one copy; siblings go before the main file, so a
// halfway failure leaves a bootstrappable replica. A missing file is not moved
// and is not an error; a replica with no file at all is ErrNoReplica.
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

// stopWorker stops the worker holding the replica open, so the files can be
// renamed. A closeable transport closes and the next call starts fresh, costing
// the pipe nothing it cannot rebuild; every other transport cancels like a step
// overrun.
func stopWorker(client synclog.LogTransport) error {
	if closer, ok := client.(interface{ Close() error }); ok {
		return closer.Close()
	}
	cancelWorker(client)
	return nil
}
