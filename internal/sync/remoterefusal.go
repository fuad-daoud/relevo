package sync

// A remote that refused the SQL this machine sent it.
//
// The driver reports a remote-side refusal in its own words, and none of those
// words is a class this tree can branch on, so the refusal is recognised here,
// once, and re-raised as a sentinel a caller classifies by: a refusing remote
// is answering, not failing.
//
// Nothing of the remote's own text reaches the message: the reason is fixed
// text written in this repository, so a remote cannot choose what a reader is
// told.

import (
	"errors"
	"strings"

	"github.com/fuad-daoud/relevo/internal/synclog"
)

// ErrRemoteRefused reports a remote that refused SQL this machine's change set
// carried. It is a refusal rather than a failure because a reader can act on it:
// what the remote refused says which of the two faults it is, and each has its
// own way out.
var ErrRemoteRefused = errors.New("sync: the remote refused the SQL in this change set")

// ErrRemoteSchema is the one refusal the change set's order cannot fix: the
// remote has no table to write to. It is its own sentinel because the fix
// differs -- a remote never taught this schema needs the DDL pushed over the
// sync connection, not a re-recorded row -- so a caller can tell which of the
// two it holds without reading a message.
var ErrRemoteSchema = errors.New("sync: the remote has no table for this machine's change set")

// ErrQuotaReached reports a remote that refused because the plan's quota is
// used up. It repeats until the billing month turns, so it is permanent.
var ErrQuotaReached = errors.New("sync: the Turso plan quota is reached")

// IsPermanentRefusal reports whether err is a remote refusal that repeats on
// every attempt: no table for this machine's rows, a change set re-sending
// cannot fix, a body an entry names gone from the store, or a plan quota used
// up. A breaker latches on one of these rather than waiting out three predictable
// deaths.
func IsPermanentRefusal(err error) bool {
	return errors.Is(err, ErrRemoteSchema) || errors.Is(err, ErrRemoteRefused) ||
		errors.Is(err, synclog.ErrBlobMissing) || errors.Is(err, ErrQuotaReached)
}

// The driver shapes this matches on. They are the SQLite result codes the engine
// puts beside its message, so each is one code rather than a sentence and a
// remote that words its refusal differently is still classified.
const (
	remoteCodeConstraint = "SQLITE_CONSTRAINT"
	remoteCodeError      = "SQLITE_ERROR"
	// The constraint's own name, which the driver puts in the message beside the
	// code. A refusal naming it is the one this tree's ordering addresses.
	remoteNameForeignKey = "FOREIGN KEY"
)

// remoteRefusal is a driver's refusal carrying the verb it refused and the class
// it refused under, so a caller can name both without parsing prose.
type remoteRefusal struct {
	verb   string
	class  error
	reason string
	cause  error
}

func (r *remoteRefusal) Error() string { return r.reason }

func (r *remoteRefusal) Unwrap() []error { return []error{r.class, r.cause} }

// RefusedVerb is the call the remote refused -- push or pull -- and whether err
// was a remote refusal at all, so a caller can name the verb in its own message.
func RefusedVerb(err error) (string, bool) {
	var refused *remoteRefusal
	if !errors.As(err, &refused) {
		return "", false
	}
	return refused.verb, true
}

// classifyRemoteRefusal turns a driver's refusal into the class this package
// reports, and returns every other error unchanged.
//
// It matches on the driver's shape rather than its sentence: the engine prefix
// says a call failed, the execute clause says the remote refused a statement
// rather than the network refusing the call, and the SQLite code says which
// refusal. Anything less is left for the caller, because a failure that is not
// a remote refusal has a different answer.
func classifyRemoteRefusal(verb string, err error) error {
	if err == nil || !isRemoteRefusal(err) {
		return err
	}
	reason, class := remoteRefusalClass(verb, err.Error())
	return &remoteRefusal{verb: verb, class: class, reason: reason, cause: err}
}

func isRemoteRefusal(err error) bool {
	for e := err; e != nil; e = errors.Unwrap(e) {
		text := e.Error()
		if strings.Contains(text, "sync engine") && strings.Contains(text, "failed to execute sql") {
			return true
		}
	}
	return false
}

func isMissingTable(text string) bool { return strings.Contains(text, "no such table") }

// remoteRefusalClass is the fixed sentence for this refusal and the class under
// it. Each sentence names the verb and the reason, so a reader learns which call
// the remote refused and why without a word the remote chose.
func remoteRefusalClass(verb, text string) (string, error) {
	name := refusalVerb(verb)
	switch {
	case isMissingTable(text):
		return "sync: " + name + ": the remote has no table to write this machine's " +
				"rows to, so it refused the statement: the remote was never taught this database's schema",
			ErrRemoteSchema
	case strings.Contains(text, remoteCodeConstraint) && strings.Contains(text, remoteNameForeignKey):
		return "sync: " + name + ": " + remoteNameForeignKey + " constraint failed " +
				"on the remote, which refused this machine's rows in the order the change set carried them",
			ErrRemoteRefused
	case strings.Contains(text, remoteCodeConstraint):
		return "sync: " + name + ": " + remoteCodeConstraint + ": the remote refused " +
				"a statement of this machine's change set on a constraint of its own",
			ErrRemoteRefused
	case strings.Contains(text, remoteCodeError):
		return "sync: " + name + ": " + remoteCodeError + ": the remote refused a " +
				"statement of this machine's change set rather than applying it",
			ErrRemoteRefused
	default:
		return "sync: " + name + ": the remote refused a statement of this machine's " +
			"change set", ErrRemoteRefused
	}
}

// refusalVerb is the call as the fixed sentence names it. A call this package
// did not name falls back to the word a reader can act on either way.
func refusalVerb(verb string) string {
	switch verb {
	case "push":
		return "push: the remote refused this machine's change set"
	case "pull":
		return "pull: the remote refused this machine's change set"
	default:
		return "the remote refused this machine's change set"
	}
}
