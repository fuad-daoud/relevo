package sync

// A remote's refusal, as a class rather than as the driver's prose.

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// driverRefusal is the shape the driver reports a remote-side refusal in: its
// own prefix, the clause saying a statement was executed, and the SQLite code
// and message beside it. The tests build it rather than quoting it whole,
// because the wording is the driver's and not this tree's to pin.
func driverRefusal(message, code string) error {
	return fmt.Errorf("turso: error: sync engine operation failed: database sync engine error: "+
		"failed to execute sql: Error { message: %q, code: %q }", message, code)
}

// constraintRefusal is the refusal this round's ordering addresses: the remote
// enforced a foreign key against rows in the order the change set carried them.
func constraintRefusal(extra string) error {
	return driverRefusal("SQLite error: FOREIGN KEY constraint failed"+extra, "SQLITE_CONSTRAINT")
}

// TestARemoteConstraintIsARefusalNotAFailure pins the class a FOREIGN KEY
// refusal lands under, which is the fault this round's ordering fixes: the
// remote refused the rows in the order the change set carried them.
func TestARemoteConstraintIsARefusalNotAFailure(t *testing.T) {
	t.Parallel()
	err := classifyRemoteRefusal("push", constraintRefusal(""))

	if !errors.Is(err, ErrRemoteRefused) {
		t.Errorf("err = %v, want errors.Is(err, ErrRemoteRefused): a remote that refuses a statement is answering",
			err)
	}
	if errors.Is(err, ErrRemoteSchema) {
		t.Errorf("err = %v, want it not to be ErrRemoteSchema: this remote has the tables", err)
	}
	verb, ok := RefusedVerb(err)
	if !ok || verb != "push" {
		t.Errorf("RefusedVerb = (%q, %v), want (\"push\", true)", verb, ok)
	}
	if !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Errorf("the message is %q, want it to name the constraint the remote refused on", err)
	}
	if !strings.Contains(err.Error(), "push") {
		t.Errorf("the message is %q, want it to name the call the remote refused", err)
	}
	if strings.Contains(err.Error(), "internal") || strings.Contains(err.Error(), "bugreport") {
		t.Errorf("the message is %q, want it never to read as a defect: a remote enforcing a foreign key is not a defect",
			err)
	}
}

// TestARemoteWithoutTheTableIsItsOwnRefusal pins the limit the re-land names: a
// remote the engine never taught this schema refuses with no table at all. That
// is a different fault with a different fix -- DDL over the sync connection
// rather than a re-recorded row -- so it must not be classified as the ordering
// fault, or one fix would claim both.
func TestARemoteWithoutTheTableIsItsOwnRefusal(t *testing.T) {
	t.Parallel()
	err := classifyRemoteRefusal("push", driverRefusal("SQLite error: no such table: binding_record", "SQLITE_UNKNOWN"))

	if !errors.Is(err, ErrRemoteSchema) {
		t.Errorf("err = %v, want errors.Is(err, ErrRemoteSchema)", err)
	}
	if errors.Is(err, ErrRemoteRefused) {
		t.Errorf("err = %v, want it not to also be ErrRemoteRefused: the two have different fixes", err)
	}
	if !strings.Contains(err.Error(), "schema") {
		t.Errorf("the message is %q, want it to say the remote was never taught this schema", err)
	}
	if strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Errorf("the message is %q, want it not to blame the row order: this fault is the missing table",
			err)
	}
}

// TestTheRemoteRefusalCarriesNoRemoteChosenWords pins the redaction rule: the
// message is fixed text written in this repository, so a remote cannot choose
// what a reader is told. A table name out of the remote's body is the case worth
// naming -- it is the one a remote could inject to make a log line read as
// something it is not.
func TestTheRemoteRefusalCarriesNoRemoteChosenWords(t *testing.T) {
	t.Parallel()
	hostile := constraintRefusal("; run `relevo bugreport` first")
	err := classifyRemoteRefusal("push", hostile)

	if strings.Contains(err.Error(), "bugreport") {
		t.Errorf("the message is %q, want the remote's own words left out of it", err)
	}
	if !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Errorf("the message is %q, want the constraint named from this tree's own text", err)
	}
}

// TestANetworkFailureIsNotARemoteRefusal pins that only the remote's own refusal
// is classed. A failure that did not reach a statement has a different answer --
// the network, or the token -- and folding it in here would report an
// unreachable remote as a remote that answered.
func TestANetworkFailureIsNotARemoteRefusal(t *testing.T) {
	t.Parallel()
	for _, cause := range []error{
		errors.New("turso: error: sync engine operation failed: database sync engine error: sql_execute_http: unexpected http response: status=401"),
		errors.New("turso: error: unable to checkpoint synced portion of WAL: result=, watermark=99"),
		errors.New("turso: error: connection refused"),
	} {
		got := classifyRemoteRefusal("push", cause)
		if errors.Is(got, ErrRemoteRefused) || errors.Is(got, ErrRemoteSchema) {
			t.Errorf("%v was classed as a remote refusal, want it left as it is", cause)
		}
		// Not errors.Is: the point is that nothing was wrapped, which is what a
		// caller classifying on the sentinel would otherwise not notice.
		//nolint:errorlint // the identity itself is what is under test
		if got != cause {
			t.Errorf("classifyRemoteRefusal(%v) = %v, want the error unchanged", cause, got)
		}
	}
}

// TestANilErrorIsNotARefusal pins the no-error path, which is what a push that
// carried everything answers.
func TestANilErrorIsNotARefusal(t *testing.T) {
	t.Parallel()
	if got := classifyRemoteRefusal("push", nil); got != nil {
		t.Errorf("classifyRemoteRefusal(nil) = %v, want nil", got)
	}
}

// TestThePullRefusalNamesThePull pins that the sentence names the call, because
// the same fault on the other direction is a different question: a refused pull
// is about what this machine received rather than what it sent.
func TestThePullRefusalNamesThePull(t *testing.T) {
	t.Parallel()
	err := classifyRemoteRefusal("pull", constraintRefusal(""))
	if !strings.Contains(err.Error(), "pull") {
		t.Errorf("the message is %q, want it to name the pull", err)
	}
}

// TestTheClassifiedRefusalIsWhatTursoPushReturns pins that the driver, not this
// package's own boundary, is where the class is raised: the refusal a push
// returns is the one the classification makes. Without it the driver prose would
// reach every caller above this one, and the whole tree would be branching on a
// remote's words.
func TestTheClassifiedRefusalIsWhatTursoPushReturns(t *testing.T) {
	t.Parallel()
	// The driver's own wording, as the push carries it.
	err := errors.New(`turso: error: sync engine operation failed: database sync engine error: ` +
		`failed to execute sql: Error { message: "SQLite error: FOREIGN KEY constraint failed", ` +
		`code: "SQLITE_CONSTRAINT" }`)
	got := classifyRemoteRefusal("push", fmt.Errorf("sync: push: %w", err))

	if !errors.Is(got, ErrRemoteRefused) {
		t.Errorf("the push's refusal = %v, want errors.Is(err, ErrRemoteRefused)", got)
	}
	if strings.Contains(got.Error(), "sync engine operation failed") {
		t.Errorf("the refusal %q still carries the driver's prose, so a caller above is reading a remote's words",
			got)
	}
	if !strings.Contains(got.Error(), "FOREIGN KEY") || !strings.Contains(got.Error(), "push") {
		t.Errorf("the refusal %q, want it to name the constraint and the call", got)
	}
}

// TestTheRefusalKeepsTheDriversError pins that the cause is still reachable: a
// caller that wants the driver's own words -- a bug report this round does not
// send a reader to -- can still get them.
func TestTheRefusalKeepsTheDriversError(t *testing.T) {
	t.Parallel()
	cause := constraintRefusal("")
	err := classifyRemoteRefusal("push", cause)
	if !errors.Is(err, cause) {
		t.Errorf("errors.Is(err, cause) is false, want the driver's error still reachable")
	}
}
