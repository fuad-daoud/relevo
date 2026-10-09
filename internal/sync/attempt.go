package sync

import (
	"errors"
	"time"
)

// KeyLastAttempt is the marker the daemon writes for each steady attempt. The
// tick marker says only whether the last one succeeded; this one says when it
// ran, what it moved and what stopped it, so a machine that keeps failing reads
// as failing on every surface that reads markers.
const KeyLastAttempt = "sync.last_attempt"

// Attempt is the last steady attempt as the daemon recorded it. A zero Start
// means none was recorded.
type Attempt struct {
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	Exported  int       `json:"exported"`
	Applied   int       `json:"applied"`
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
		Attention: out.Attention,
	}
	if out.Err != nil {
		a.Error = out.Err.Error()
	}
	r.put(KeyLastAttempt, a)
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
