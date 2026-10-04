package db

import (
	"errors"
	"fmt"
	"strings"
)

// The enable preflight. Four checks stand between a database and an enable, and
// each one refuses with the fix it names: the origin gate, the shared file's
// secret table, the finished compress pass, and the shape the upload path
// asserts. Every refusal is reported rather than only the first, so one run
// names every fix the user owes before the next run.

// The four checks, as they name themselves in a refusal.
const (
	checkEmptyOrigin   = "empty origin"
	checkSharedSecrets = "shared secrets"
	checkCompressDone  = "compress pass incomplete"
	checkUploadShape   = "upload shape"
)

// The fix each check names. They are constants because a refusal is read by
// someone deciding what to run, and the command to run is part of the message.
const (
	fixEmptyOrigin = "run the origin backfill (BackfillOriginOnce) or upgrade before enabling sync"
	fixSharedSecs  = "run the local split (SplitOnce) so the secret table moves into the machine-local file"
	fixCompress    = "run the zstd compress pass (CompressHistoryOnce) before enabling sync"
	fixUploadShape = "the seed copy (SeedCopy) drains the log; a page size no pragma can change needs a rebuilt database"
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
// with the refusal class attached underneath them.
//
// It is a type rather than a sentinel folded into the format string so the line a
// reader sees stays exactly the sentence the refusals make. The class is there
// for a caller to branch on; spelling it in the message would only repeat what
// the CLI already prints as the error code. It unwraps to the joined refusals, so
// a sentinel one of them carries stays reachable underneath.
type preflightRefusedError struct{ err error }

func (e *preflightRefusedError) Error() string { return e.err.Error() }

func (e *preflightRefusedError) Unwrap() error { return e.err }

// Is reports the refusal class.
func (e *preflightRefusedError) Is(target error) bool { return target == ErrPreflightRefused }

// Err joins the refusals into the one error an enable path can return, or nil
// when every check passed. The count is in the message so a caller that shows
// one line still says how much is left to fix, and every refusal keeps its own
// wording and its own fix inside that line.
//
// The error matches ErrPreflightRefused, so a caller classifies it as the
// refusal it is instead of reading the prose to work out that nothing failed.
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

// refuse records a check that did not pass.
func (p *Preflight) refuse(check, detail string) {
	p.Refusals = append(p.Refusals, PreflightRefusal{Check: check, Detail: detail})
}

// unanswerable records a check this handle could not answer. An unreadable
// answer is not a pass, and it carries the same fix the check would have named,
// so the user is never left with a refusal that says only "error".
func (p *Preflight) unanswerable(check string, err error, fix string) {
	p.refuse(check, fmt.Sprintf("the check could not be answered (%v); %s", err, fix))
}

// EnablePreflight runs the four checks against the shared handle and reports
// every one that refuses. A refusal is the answer, not an error: the caller
// decides whether to show them, print the first, or act on the list.
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
	p.refuseUploadShape(d)
	return p
}

// refuseSharedSecrets refuses while the shared file still holds secret rows. It
// reads the shared handle, never the local file beside it: the whole point of
// the split is that the local file holds the secrets, so a check that looked
// there would pass on exactly the database that is about to upload them.
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
// finish. The marker is the pass's own record of having converted its columns;
// without it the seed copy would upload plain bodies, and the conversion would
// then have to run again against a database two machines already hold.
func (p *Preflight) refuseCompressIncomplete(d *DB) {
	if _, ok, err := d.KVGet(compressKVKey); err != nil {
		p.unanswerable(checkCompressDone, err, fixCompress)
		return
	} else if !ok {
		p.refuse(checkCompressDone, fmt.Sprintf("no %s kv row, so the pass has not finished: %s", compressKVKey, fixCompress))
	}
}

// refuseUploadShape refuses on the first of the three assertions the upload path
// makes, naming which one failed and what its own fix is.
func (p *Preflight) refuseUploadShape(d *DB) {
	shape, err := d.UploadShape()
	if err != nil {
		p.unanswerable(checkUploadShape, err, fixUploadShape)
		return
	}
	if failing := shape.Failing(); failing != "" {
		p.refuse(checkUploadShape, failing+"; "+fixUploadShape)
	}
}
