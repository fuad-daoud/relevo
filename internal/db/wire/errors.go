package wire

import (
	"errors"
	"strings"
)

// ErrConnLost marks a connection lost after a request was already sent: the
// owner may have run the statement, so the error deliberately does not match
// driver.ErrBadConn and database/sql must not retry it automatically.
var ErrConnLost = errors.New("wire: connection lost after the request was sent")

// ErrBusy marks a request the owner refused because every connection slot was
// held. The request never ran, but a retry on a fresh connection would meet
// the same cap, so it deliberately does not match driver.ErrBadConn and
// database/sql must not retry it automatically. The refusal stays inspectable
// through the wrap, and the owner's message names the counts, so the client
// passes it through untouched.
var ErrBusy = errors.New("wire: owner is busy")

// Error is a rebuilt SQLite error: it carries the owner's code so the client's
// existing code masks keep working, and its text so the message fallbacks do.
type Error struct {
	Header
	ErrCode  int    `json:"code"`
	Extended int    `json:"extended_code"`
	Message  string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

func (e *Error) Code() int { return e.ErrCode }

// ExtendedCode returns the extended result code, or the primary one when the
// owner had no separate extended value.
func (e *Error) ExtendedCode() int { return e.Extended }

func NewError(id, code, extended int, message string) *Error {
	return &Error{
		Header:   Header{Type: TypeError, ID: id},
		ErrCode:  code,
		Extended: extended,
		Message:  message,
	}
}

// The refusal codes the owner may send. They are strings, not SQLite codes, so
// mapBusy-style matching can never swallow one.
const (
	RefuseWrongProto   = "wrong_proto"
	RefuseShuttingDown = "shutting_down"
	RefuseRestarting   = "restarting"
	// RefuseReaping answers a request on an ad-hoc connection while the owner
	// is ending a statement that will not stop. It is never sent for the
	// handshake and never for a connection that did not mark itself ad-hoc.
	RefuseReaping = "reaping"
	// RefuseBusy answers a request that needs a pinned connection while every
	// slot is held. The request never ran, but a fresh connection would meet
	// the same cap, so the client reports it without retrying.
	RefuseBusy = "busy"
	// RefuseNoLocal answers a handshake that asked for the machine-local file
	// from an owner serving only the shared one. The client asked for a file
	// that does not exist on this owner, and answering from the shared file
	// would return the rows the split keeps out of it.
	RefuseNoLocal = "no_local_file"
	// RefuseNoSyncVerb answers a sync verb on an owner built without a sync
	// executor: the daemon installs one, and a caller that reached an owner
	// without it cannot be answered rather than silently no-opped.
	RefuseNoSyncVerb = "no_sync_verb"
)

// coder is anything exposing a SQLite result code, which is both the modernc
// error and the Error this package rebuilds.
type coder interface{ Code() int }

func CodeOf(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	var c coder
	if errors.As(err, &c) {
		return c.Code(), true
	}
	return 0, false
}

// shortReadWAL is the Turso text for a WAL frame read past the log's end: a
// truncate under a live pool leaves the index pointing at frames that are
// gone. It is matched as a substring because the engine appends the frame
// offset and the byte counts to it.
const shortReadWAL = "short read on WAL frame"

// IsShortReadWAL reports whether err is the stale-WAL read failure: a truncate
// under a live pool leaves its index pointing at frames past the log's end,
// and every later read through that pool fails. Either the engine's own text
// names the WAL frame, or a code-10 I/O error's text names the WAL.
func IsShortReadWAL(err error) bool {
	if err == nil {
		return false
	}
	if strings.Contains(err.Error(), shortReadWAL) {
		return true
	}
	code, ok := CodeOf(err)
	if !ok || code != 10 {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "wal")
}

type extendedCoder interface{ ExtendedCode() int }

// ExtendedCodeOf returns the extended result code when the error distinguishes
// one, and the primary code otherwise.
func ExtendedCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var e extendedCoder
	if errors.As(err, &e) {
		return e.ExtendedCode()
	}
	code, _ := CodeOf(err)
	return code
}
