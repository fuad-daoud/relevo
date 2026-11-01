package db

import (
	"errors"
	"fmt"
	"strings"
)

// The enable preflight. Three checks stand between a database and an enable,
// and each one refuses with the fix it names: the origin gate, the shared
// file's secret table and the finished compress pass. Every refusal is reported
// rather than only the first, so one run names every fix the user owes before
// the next run. A check only refuses on what the user has to do: the log is
// written from rows this machine owns.

// The checks, as they name themselves in a refusal.
const (
	checkEmptyOrigin   = "empty origin"
	checkSharedSecrets = "shared secrets"
	checkCompressDone  = "compress pass incomplete"
)

// The fix each check names: a refusal is read by someone deciding what to run,
// and the command to run is part of the message.
const (
	fixEmptyOrigin = "run the origin backfill (BackfillOriginOnce) or upgrade before enabling sync"
	fixSharedSecs  = "run the local split (SplitOnce) so the secret table moves into the machine-local file"
	fixCompress    = "run the zstd compress pass (CompressHistoryOnce) before enabling sync"
)

// PreflightRefusal is one check that refused, with the fix it names.
type PreflightRefusal struct {
	Check  string `json:"check"`
	Detail string `json:"detail"`
}

func (r PreflightRefusal) Error() string { return r.Check + ": " + r.Detail }

// Preflight is what the enable checks found: the origin gate's counts whether or
// not the gate passed, and one entry per check that did not.
type Preflight struct {
	EmptyOrigin OriginGateCounts   `json:"empty_origin"`
	Refusals    []PreflightRefusal `json:"refusals,omitempty"`
}

// OK reports whether every check passed.
func (p Preflight) OK() bool { return len(p.Refusals) == 0 }

// preflightRefusedError is what Err returns: the refusals' own sentences, joined,
// with the refusal class attached underneath them. It is a type rather than a
// sentinel folded into the format string, so the class stays available for a
// caller to branch on without repeating what the CLI already prints. It unwraps
// to the joined refusals, so a sentinel one of them carries stays reachable.
type preflightRefusedError struct{ err error }

func (e *preflightRefusedError) Error() string { return e.err.Error() }

func (e *preflightRefusedError) Unwrap() error { return e.err }

// Is reports the refusal class.
func (e *preflightRefusedError) Is(target error) bool { return target == ErrPreflightRefused }

// Err joins the refusals into the one error an enable path can return, or nil
// when every check passed. The count is in the message so a caller that shows
// one line still says how much is left to fix. The error matches
// ErrPreflightRefused, so a caller classifies it without reading the prose.
func (p Preflight) Err() error {
	if p.OK() {
		return nil
	}
	errs := make([]error, len(p.Refusals))
	for i, r := range p.Refusals {
		errs[i] = fmt.Errorf("db: enable: %w", r)
	}
	return &preflightRefusedError{
		err: fmt.Errorf("db: enable preflight: %d checks refused: %w", len(p.Refusals), errors.Join(errs...)),
	}
}

func (p *Preflight) refuse(check, detail string) {
	p.Refusals = append(p.Refusals, PreflightRefusal{Check: check, Detail: detail})
}

// unanswerable records a check this handle could not answer. An unreadable
// answer is not a pass, and it carries the same fix the check would have named,
// so the user is never left with a refusal that says only "error".
func (p *Preflight) unanswerable(check string, err error, fix string) {
	p.refuse(check, fmt.Sprintf("the check could not be answered (%v); %s", err, fix))
}

// EnablePreflight runs the checks against the shared handle and reports every
// one that refuses. A refusal is the answer, not an error: the caller decides
// whether to show them, print the first, or act on the list.
func EnablePreflight(d *DB) Preflight {
	var p Preflight

	counts, err := CountEmptyOrigins(d)
	if err != nil {
		p.unanswerable(checkEmptyOrigin, err, fixEmptyOrigin)
	} else {
		p.EmptyOrigin = counts
		if !counts.Empty() {
			p.refuse(checkEmptyOrigin, counts.String()+" rows with no origin must be stamped before two machines share this file; "+fixEmptyOrigin)
		}
	}

	p.refuseSharedSecrets(d)
	p.refuseCompressIncomplete(d)
	return p
}

// refuseSharedSecrets refuses while the shared file still holds secret rows. It
// reads the shared handle, never the local file beside it: the local file
// holds the secrets, so a check that looked there would pass on exactly the
// database that is about to upload them.
func (p *Preflight) refuseSharedSecrets(d *DB) {
	names, err := d.SecretNames()
	if err != nil {
		p.unanswerable(checkSharedSecrets, err, fixSharedSecs)
		return
	}
	if len(names) == 0 {
		return
	}
	p.refuse(checkSharedSecrets, fmt.Sprintf("the shared file still holds %d secret row(s) (%s), which would be uploaded: %s",
		len(names), strings.Join(names, ", "), fixSharedSecs))
}

// refuseCompressIncomplete refuses while the compress pass has not recorded a
// finish. Without the marker the history would leave this machine as plain
// bodies, and the conversion would have to run again against rows another
// machine already holds. It is read from the machine-local file, which is
// where the pass wrote it: reading the shared handle instead asks about a file
// the marker was never in, and keeps refusing a database the pass already
// converted.
func (p *Preflight) refuseCompressIncomplete(d *DB) {
	if _, ok, err := d.LocalOrSelf().KVGet(compressKVKey); err != nil {
		p.unanswerable(checkCompressDone, err, fixCompress)
		return
	} else if !ok {
		p.refuse(checkCompressDone, fmt.Sprintf("no %s kv row, so the pass has not finished: %s", compressKVKey, fixCompress))
	}
}
