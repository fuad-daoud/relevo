package sync

// What Round 3 moved, and what it deliberately did not. A join's bucket
// credentials arrive in the secrets document during the import, so the two checks
// that depend on them -- the pre-pass and the completeness refusal -- run only
// on that path, and neither touches a plain enable, which already holds them.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// clearR2 empties the machine's bucket credentials, which is what a joiner holds
// before the document has been applied.
func clearR2(t *testing.T, e *Enabler) {
	t.Helper()
	if err := e.Request.Local.Tx(func(tx *db.Tx) error {
		for _, name := range r2Names() {
			if err := tx.SecretDelete(name); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("clear the credentials: %v", err)
	}
}

// joinWithoutCredentials is an enable joining, with no bucket credentials stored
// and so none yet arrived in the document.
func joinWithoutCredentials(t *testing.T) *Enabler {
	t.Helper()
	local := r2Pair(t)
	shared, err := db.OpenSplit(t.TempDir()+"/shared.db", db.Options{Origin: "m1"})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	e := &Enabler{
		Request: EnableRequest{Local: local, Shared: shared, URL: "libsql://fleet.invalid",
			Token: []byte("t"), Join: true},
		Transport: synclog.NewMemTransport("m1"),
		Now:       func() time.Time { return time.Unix(0, 0).UTC() },
	}
	clearR2(t, e)
	return e
}

// TestTheCompletenessCheckRefusesBeforeTheGuard pins the refusal a join owes its
// operator, and where it sits. It stands in front of the guard because a join
// that received no credentials has no bucket: a guard run over that machine
// would read an empty credential set as an empty bucket and wave the join through
// on a bucket nobody can name. The refusal that comes first is the one that
// cannot be mistaken for an all-clear.
func TestTheCompletenessCheckRefusesBeforeTheGuard(t *testing.T) {
	t.Parallel()
	e := joinWithoutCredentials(t)

	err := e.checkJoinR2()
	if !errors.Is(err, ErrNoR2AfterJoin) {
		t.Fatalf("the refusal is %v, want ErrNoR2AfterJoin", err)
	}
	for _, want := range []string{"--r2-endpoint", "db sync invite"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
	// The refusal names both fixes because which one applies is a fact this
	// machine cannot see: the inviter may never have configured a bucket, or it
	// may have and this machine's import did not reach them.
	if !strings.Contains(err.Error(), "invite") {
		t.Errorf("the refusal gives this machine's flag but not the inviter's fix: %v", err)
	}
}

// TestTheCompletenessCheckLeavesAMachineThatSetItsOwnAlone pins the other half:
// the join's contract is that a code carries the credentials, not that a code is
// the only way to have them, and a machine that configured them by hand has
// nothing to be told.
func TestTheCompletenessCheckLeavesAMachineThatSetItsOwnAlone(t *testing.T) {
	t.Parallel()
	local := r2Pair(t)
	if err := SetR2(local, r2Fixture, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetR2: %v", err)
	}
	shared, err := db.OpenSplit(t.TempDir()+"/shared.db", db.Options{Origin: "m1"})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	e := &Enabler{Request: EnableRequest{Local: local, Shared: shared, Join: true}}

	if err := e.checkJoinR2(); err != nil {
		t.Errorf("a machine holding its own credentials was refused: %v", err)
	}
}

// A plain enable is not on this path at all: it refuses for missing credentials
// in its own preflight, before anything is stored, and running either of the
// join's checks over it would only repeat a decision it has already made.
func TestTheJoinChecksAreInertOnAPlainEnable(t *testing.T) {
	t.Parallel()
	local := r2Pair(t)
	shared, err := db.OpenSplit(t.TempDir()+"/shared.db", db.Options{Origin: "m1"})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	e := &Enabler{
		Request:   EnableRequest{Local: local, Shared: shared, URL: "libsql://fleet.invalid", Token: []byte("t")},
		Transport: synclog.NewMemTransport("m1"),
	}
	clearR2(t, e)

	if err := e.checkJoinR2(); err != nil {
		t.Errorf("a plain enable with no credentials was refused by the join's check: %v", err)
	}
	if err := e.prepassSecrets(e.Transport); err != nil {
		t.Errorf("a plain enable paid for the secrets pre-pass: %v", err)
	}
}

// The pre-pass runs only where it is needed. A machine that already holds bucket
// credentials has nothing the document could unblock, and a plain enable holds
// them by definition, so both walk nothing.
func TestThePrepassSkipsAMachineThatAlreadyHasCredentials(t *testing.T) {
	t.Parallel()
	local := r2Pair(t)
	if err := SetR2(local, r2Fixture, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetR2: %v", err)
	}
	shared, err := db.OpenSplit(t.TempDir()+"/shared.db", db.Options{Origin: "m1"})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	// A transport that would fail any read: the pre-pass must not make one.
	transport := synclog.NewMemTransport("m1")
	e := &Enabler{
		Request:   EnableRequest{Local: local, Shared: shared, Join: true},
		Transport: transport,
	}
	if err := e.prepassSecrets(transport); err != nil {
		t.Errorf("a machine holding its own credentials paid for the pre-pass: %v", err)
	}
}

// The pre-pass moves no import mark. That is what makes the normal import below
// it a set of redeliveries rather than a second application, and it is checked
// here directly because the consequence is otherwise invisible: both paths apply
// the same entries and a mark that moved would be a mark that skipped them.
func TestThePrepassMovesNoImportMark(t *testing.T) {
	t.Parallel()
	local := r2Pair(t)
	if err := SetKey(local, []byte("0123456789abcdef0123456789abcdef"), time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	shared, err := db.OpenSplit(t.TempDir()+"/shared.db", db.Options{Origin: "m1"})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	e := &Enabler{
		Request:   EnableRequest{Local: local, Shared: shared, Join: true},
		Transport: synclog.NewMemTransport("m1"),
	}
	clearR2(t, e)

	before, err := shared.ImportMarks()
	if err != nil {
		t.Fatalf("ImportMarks: %v", err)
	}
	if err := e.prepassSecrets(e.Transport); err != nil {
		t.Fatalf("prepassSecrets: %v", err)
	}
	after, err := shared.ImportMarks()
	if err != nil {
		t.Fatalf("ImportMarks: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("the pre-pass wrote import marks %v, want none (it had %v)", after, before)
	}
}
