package relevo

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/consent"
	"github.com/fuad-daoud/relevo/internal/db"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// These tests drive the two consent documents through the real sync pipeline on
// the in-memory log: nothing here binds a document by hand, so a construction
// site that forgets to bind one fails.

const consentURL = "https://github.com/o/r"

// consentMachine is one machine over the same split pair the other sync tests
// use, so the repository rows are in the shared file and the stamps, the signals
// and the session rows are in the machine-local one.
type consentMachine struct {
	*cfgMachine
}

// newConsentMachine is one machine whose documents are the real ones, reached
// through sharedDocs the way every pipeline stage reaches them.
func newConsentMachine(t *testing.T, log *synclog.MemTransport, key []byte, enabled bool) *consentMachine {
	t.Helper()
	return &consentMachine{cfgMachine: newCfgMachine(t, log, key, enabled)}
}

// answerRepo records a repository answer as a person does, through the same
// write path the CLI reaches.
func (m *consentMachine) answerRepo(t *testing.T, url string, c db.Consent) {
	t.Helper()
	u := url
	if _, err := m.mdb.SetRepoConsent(db.Repo{OriginURL: &u}, c, time.Now().UTC()); err != nil {
		t.Fatalf("SetRepoConsent(%s, %q): %v", url, c, err)
	}
}

// answerSession records a session answer as a person does.
func (m *consentMachine) answerSession(t *testing.T, kind, session string, c db.Consent) {
	t.Helper()
	if err := m.mdb.LocalOrSelf().SetSessionConsent(kind, session, c, time.Now().UTC()); err != nil {
		t.Fatalf("SetSessionConsent(%s/%s, %q): %v", kind, session, c, err)
	}
}

// repoAnswer is the answer this machine's own row holds for a URL.
func (m *consentMachine) repoAnswer(t *testing.T, url string) db.Consent {
	t.Helper()
	u := url
	c, err := m.mdb.RepoConsent(db.Repo{OriginURL: &u})
	if err != nil {
		t.Fatalf("RepoConsent(%s): %v", url, err)
	}
	return c
}

// sessionAnswer is the answer this machine holds for a session.
func (m *consentMachine) sessionAnswer(t *testing.T, kind, session string) db.Consent {
	t.Helper()
	c, err := m.mdb.LocalOrSelf().SessionConsent(kind, session)
	if err != nil {
		t.Fatalf("SessionConsent(%s/%s): %v", kind, session, err)
	}
	return c
}

// TestTwoMachinesConvergeConsents pins the whole exchange for both documents on
// the real steady pipeline: an answer a person gives on one machine reaches the
// other, and where both machines answer the same question the later edit wins on
// both with the earlier one recorded as superseded rather than applied second.
//
// Nothing binds a consent document by hand here, so a wiring site that forgot
// one would leave both machines answering from their own rows.
func TestTwoMachinesConvergeConsents(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newConsentMachine(t, log, cfgSyncKey, true)
	b := newConsentMachine(t, log, cfgSyncKey, true)

	a.answerRepo(t, consentURL, db.ConsentYes)
	a.answerSession(t, "claude", "s1", db.ConsentNo)
	a.attempt(t)
	b.attempt(t)

	if got := b.repoAnswer(t, consentURL); got != db.ConsentYes {
		t.Errorf("B's repository answer = %q, want A's yes", got)
	}
	if got := b.sessionAnswer(t, "claude", "s1"); got != db.ConsentNo {
		t.Errorf("B's session answer = %q, want A's no", got)
	}

	// The loser: B answers the same two questions later, so A's earlier answers
	// lose once B's arrive, and each machine ends on the later edit.
	time.Sleep(20 * time.Millisecond)
	b.answerRepo(t, consentURL, db.ConsentNo)
	b.answerSession(t, "claude", "s1", db.ConsentYes)
	b.attempt(t)
	a.attempt(t)

	if got := a.repoAnswer(t, consentURL); got != db.ConsentNo {
		t.Errorf("A's repository answer = %q, want the later no", got)
	}
	if got := a.sessionAnswer(t, "claude", "s1"); got != db.ConsentYes {
		t.Errorf("A's session answer = %q, want the later yes", got)
	}
	// The loser is recorded as superseded: the applied entry names the machine
	// whose answer won, so a redelivery of the older one is a no-op.
	var applied db.DocApplied
	var ok bool
	if err := a.localOrSelfDB().Tx(func(tx *db.Tx) error {
		var err error
		applied, ok, err = tx.DocApplied(db.ConsentDoc, consentURL)
		return err
	}); err != nil || !ok {
		t.Fatalf("A's applied state for the URL = (ok %v, %v), want one", ok, err)
	}
	if applied.Origin != b.mdb.Origin() {
		t.Errorf("A's applied entry names origin %q, want B's %q: the later edit wins", applied.Origin, b.mdb.Origin())
	}
}

// TestClearedAnswerReachesThePeer pins the tombstone through the real pipeline:
// a cleared answer is the key's absence, and the machine that still holds one
// has to be told to lift it. A pipeline that only ever carried answers would
// leave the peer asking forever.
func TestClearedAnswerReachesThePeer(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newConsentMachine(t, log, cfgSyncKey, true)
	b := newConsentMachine(t, log, cfgSyncKey, true)

	a.answerSession(t, "claude", "s1", db.ConsentYes)
	a.attempt(t)
	b.attempt(t)
	if got := b.sessionAnswer(t, "claude", "s1"); got != db.ConsentYes {
		t.Fatalf("B's session answer = %q, want A's yes", got)
	}

	// A resets the session: the answer goes, the told baseline goes, and the
	// key leaves the document.
	if err := a.localOrSelfDB().ClearSessionConsent("claude", "s1"); err != nil {
		t.Fatalf("ClearSessionConsent: %v", err)
	}
	a.attempt(t)
	b.attempt(t)

	if got := b.sessionAnswer(t, "claude", "s1"); got != db.ConsentUnset {
		t.Errorf("B's session answer after the clear = %q, want unset", got)
	}
}

// TestJoinClearsConsentStamps pins that a join forgets this machine's own answer
// history, so a machine joining with an answer it gave before it had a peer
// re-emits that answer rather than claiming it outranks whatever the peer
// recorded since.
//
// The stamps are the whole of that claim: they say this machine last wrote the
// key, and a join is exactly when that stops being true of anything the machine
// held before it.
func TestJoinClearsConsentStamps(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newConsentMachine(t, log, cfgSyncKey, true)
	b := newConsentMachine(t, log, cfgSyncKey, false)

	b.answerRepo(t, consentURL, db.ConsentYes)
	if _, ok, err := consent.OpenRepo(b.mdb).ItemWriteStamp(consentURL); err != nil || !ok {
		t.Fatalf("the answer recorded no stamp before the join (ok %v, %v)", ok, err)
	}

	b.join(t)

	if _, ok, err := consent.OpenRepo(b.mdb).ItemWriteStamp(consentURL); err != nil || ok {
		t.Errorf("the join left the machine's pre-join consent stamp behind (ok %v, %v)", ok, err)
	}
	if signals := consentSignals(t, b.mdb); len(signals) == 0 {
		t.Error("the join did not signal the consent document, so the re-emit never runs")
	}

	// The re-emit is what actually leaves: the answer the machine held before it
	// joined is exported again under a fresh stamp rather than being withheld.
	b.export(t)
	a.attempt(t)
	if got := a.repoAnswer(t, consentURL); got != db.ConsentYes {
		t.Errorf("A's repository answer = %q, want the joining machine's answer re-emitted", got)
	}
}

// TestLockedPeerParksConsentsUntilUnlock pins that a locked machine parks a
// consent entry rather than applying it, and applies it on unlock.
//
// A consent answer is a person's decision about a repository, so applying one
// this machine cannot verify would be acting on a peer's rule with no key to
// check it against -- the same reason the config document parks.
func TestLockedPeerParksConsentsUntilUnlock(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newConsentMachine(t, log, cfgSyncKey, true)
	b := newConsentMachine(t, log, nil, true)

	a.answerSession(t, "claude", "s1", db.ConsentNo)
	a.attempt(t)
	b.attempt(t)

	if got := b.sessionAnswer(t, "claude", "s1"); got != db.ConsentUnset {
		t.Fatalf("the locked machine applied %q, want it parked", got)
	}
	if parked, err := b.mdb.PendingDocs(); err != nil || len(parked) == 0 {
		t.Fatalf("pending = %v, %v; want A's entry parked", parked, err)
	}

	if err := relevosync.SetKey(b.local, cfgSyncKey, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	if _, err := relevosync.DrainParkedDocs(b.mdb, b.local, log.OnLog(b.mdb.Origin())); err != nil {
		t.Fatalf("DrainParkedDocs: %v", err)
	}
	if got := b.sessionAnswer(t, "claude", "s1"); got != db.ConsentNo {
		t.Errorf("the machine's session answer after the unlock = %q, want A's answer", got)
	}
}

// TestConsentDocsAreBoundThroughSharedDocs pins the wiring rule: both documents
// are built by the one function every pipeline stage reaches, so the steady
// pipeline, a join and a keyed verb all see them without any of them naming one.
func TestConsentDocsAreBoundThroughSharedDocs(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newConsentMachine(t, log, cfgSyncKey, true)

	var names []string
	for _, d := range relevosync.SharedDocs(a.mdb) {
		names = append(names, d.Name())
	}
	joined := strings.Join(names, ",")
	for _, want := range []string{db.ConsentDoc, db.SessionConsentDoc} {
		if !strings.Contains(joined, want) {
			t.Errorf("the machine's documents = %v, want %q among them", names, want)
		}
	}
}

// TestConsentSignalsReachTheSteadyExporter pins the other half of the wiring: a
// person's answer marks the document as needing an export, so the daemon counts
// it pending rather than waiting for the next unrelated write.
//
// This is what makes an answer reach a peer at all. The stamp says when the key
// was written; the signal is what tells the exporter there is something to look
// at, and a document whose keys changed with nothing signalled never goes out.
func TestConsentSignalsReachTheSteadyExporter(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	m := newConsentMachine(t, log, cfgSyncKey, true)
	d := NewDaemon(Runtime{Store: m.st, Sync: m.runner}, time.Second)
	m.attempt(t)
	if d.outboxPending() {
		t.Fatal("outbox pending after the first export, want nothing waiting")
	}

	m.answerRepo(t, consentURL, db.ConsentYes)
	if !d.outboxPending() {
		t.Error("outbox not pending after a repository answer")
	}
	m.attempt(t)
	if d.outboxPending() {
		t.Error("outbox still pending after the export")
	}
}

// consentSignals is the export signals naming either consent document.
func consentSignals(t *testing.T, m *db.DB) []string {
	t.Helper()
	signals, err := m.LocalOrSelf().DocSignals()
	if err != nil {
		t.Fatalf("DocSignals: %v", err)
	}
	var out []string
	for _, sig := range signals {
		if sig.Doc == db.ConsentDoc || sig.Doc == db.SessionConsentDoc {
			out = append(out, sig.Doc)
		}
	}
	return out
}

// localOrSelfDB is the machine-local file, which is where the consent documents'
// stamps, signals and session rows live.
func (m *cfgMachine) localOrSelfDB() *db.DB { return m.mdb.LocalOrSelf() }

var _ = context.Background
