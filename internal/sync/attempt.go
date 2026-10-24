package sync

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// KeyLastAttempt is the marker the daemon writes for each steady attempt. The
// tick marker says only whether the last one succeeded; this one says when it
// ran, what it moved and what stopped it, so a machine that keeps failing reads
// as failing on every surface that reads markers.
const KeyLastAttempt = "sync.last_attempt"

// KeyAttempts is the capped ring of recent attempts beside the last one. The
// last attempt says what just happened; the ring says at what rate it keeps
// happening -- pulls per hour and bytes per pull -- which is the shape a
// traffic question is asked in.
const KeyAttempts = "sync.attempts"

// maxAttemptRecords caps the ring. Old attempts age out rather than growing a
// marker without bound; twenty attempts are four hours of idle ticks, enough to
// average over.
const maxAttemptRecords = 20

// Attempt is the last steady attempt as the daemon recorded it. A zero Start
// means none was recorded.
type Attempt struct {
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	Exported  int       `json:"exported"`
	Applied   int       `json:"applied"`
	Pulls     int       `json:"pulls"`
	Pushes    int       `json:"pushes"`
	TursoPush int64     `json:"turso_push"`
	TursoPull int64     `json:"turso_pull"`
	Error     string    `json:"error,omitempty"`
	Attention bool      `json:"attention,omitempty"`
}

// Failed reports whether the attempt ended in an error.
func (a Attempt) Failed() bool { return a.Error != "" }

// RecordAttempt writes the attempt that began at start and ended with out. The
// error text is the error's own: a render sanitizes it, and nothing here puts a
// credential in an error.
func (r *Runner) RecordAttempt(start time.Time, out SteadyResult) {
	a := Attempt{
		Start:     start.UTC(),
		End:       time.Now().UTC(),
		Exported:  out.Exported,
		Applied:   out.Applied,
		Pulls:     out.Transfers.Pulls,
		Pushes:    out.Transfers.Pushes,
		TursoPush: out.Transfers.TursoPush,
		TursoPull: out.Transfers.TursoPull,
		Attention: out.Attention,
	}
	if out.Err != nil {
		a.Error = out.Err.Error()
	}
	r.put(KeyLastAttempt, a)
	r.recordAttempts(a)
}

// ReadAttempts returns the recent-attempt ring, oldest first. A machine that
// never recorded one reads as empty rather than as an error.
func ReadAttempts(kv db.KV) ([]Attempt, error) {
	var out []Attempt
	if err := marker(kv, KeyAttempts, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// recordAttempts appends a to the ring, keeping the newest records. A marker
// that will not parse restarts the ring: losing history costs a window of
// averaging, while refusing to record would cost every window after it.
func (r *Runner) recordAttempts(a Attempt) {
	var prior []Attempt
	if err := marker(r.Local, KeyAttempts, &prior); err != nil {
		slog.Warn("sync: read the attempt ring", "err", err)
		prior = nil
	}
	prior = append(prior, a)
	if len(prior) > maxAttemptRecords {
		prior = prior[len(prior)-maxAttemptRecords:]
	}
	r.put(KeyAttempts, prior)
}

// FormatAttempts is the status clause for the attempt ring: the engine pulls
// and pushes the recorded attempts ran and the Turso bytes they carried. The
// counts beside the bytes are what turn one sandbox run into pulls per hour
// and bytes per pull.
func FormatAttempts(rs []Attempt) string {
	var pulls, pushes int
	var push, pull int64
	for _, r := range rs {
		pulls += r.Pulls
		pushes += r.Pushes
		push += r.TursoPush
		pull += r.TursoPull
	}
	unit := "attempts"
	if len(rs) == 1 {
		unit = "attempt"
	}
	return fmt.Sprintf("%d pulls, %d pushes, pull %s, push %s in the last %d %s",
		pulls, pushes, humanBytes(pull), humanBytes(push), len(rs), unit)
}

// failedAttempt is the result an attempt that never reached the pipeline
// records.
func failedAttempt(err error) SteadyResult {
	if err == nil {
		err = errors.New("sync attempt failed")
	}
	return SteadyResult{Err: err}
}

// RecordFailure records an attempt that stopped before the pipeline ran, such
// as one that could not open its transport.
func (r *Runner) RecordFailure(start time.Time, err error) {
	r.RecordAttempt(start, failedAttempt(err))
}
