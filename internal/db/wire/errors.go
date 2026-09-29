package wire

import "errors"

// ErrConnLost marks a connection lost after a request was already sent: the
// owner may have run the statement, so the error deliberately does not match
// driver.ErrBadConn and database/sql must not retry it automatically.
var ErrConnLost = errors.New("wire: connection lost after the request was sent")

// Error is a rebuilt SQLite error: it carries the owner's code so the client's
// existing code masks keep working, and its text so the message fallbacks do.
type Error struct {
	Header
	ErrCode  int    `json:"code"`
	Extended int    `json:"extended_code"`
	Message  string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

// Code returns the SQLite result code the owner observed.
func (e *Error) Code() int { return e.ErrCode }

// ExtendedCode returns the extended result code, or the primary one when the
// owner had no separate extended value.
func (e *Error) ExtendedCode() int { return e.Extended }

// NewError builds an error response for a request.
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
)

// coder is anything exposing a SQLite result code, which is both the modernc
// error and the Error this package rebuilds.
type coder interface{ Code() int }

// CodeOf returns the SQLite result code an error carries.
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
