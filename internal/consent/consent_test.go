package consent

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
)

// baseTime is the instant every fixed clock in this package's tests reports.
var baseTime = time.Unix(1757000000, 0).UTC()

const testURL = "https://github.com/o/r"

// fixture is one machine over a real split pair: the shared file the repository
// rows live in, and the machine-local file the stamps, the signals and the
// session rows live in. A kv double could not prove that a write reaches the
// files it is supposed to, and the split is the whole of what these documents
// are careful about.
type fixture struct {
	shared *db.DB
	local  *db.DB
	repo   *RepoDoc
	sess   *SessionDoc
}

// newFixture is one machine's documents over a real split pair. origin names the
// installation, so a test can hold a peer's row beside this machine's own.
func newFixture(t *testing.T, origin string) *fixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "relevo.db")
	shared, err := db.OpenSplit(path, db.Options{Origin: origin})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	local := shared.LocalOrSelf()
	return &fixture{
		shared: shared, local: local,
		repo: OpenRepo(shared), sess: OpenSessions(local),
	}
}

// applyRepo is one arriving repository item, applied the way the importer
// applies it, in the importer's own transaction, whose outcome the caller sees.
func (f *fixture) applyRepo(t *testing.T, key string, value []byte) (refusal, err error) {
	t.Helper()
	err = f.local.Tx(func(tx *db.Tx) error {
		refusal, err = f.repo.ApplyIn(tx, key, value, "peer")
		return err
	})
	return refusal, err
}

// applySession is one arriving session item, applied in the importer's own
// transaction.
func (f *fixture) applySession(t *testing.T, key string, value []byte) (refusal, err error) {
	t.Helper()
	err = f.local.Tx(func(tx *db.Tx) error {
		refusal, err = f.sess.ApplyIn(tx, key, value, "peer")
		return err
	})
	return refusal, err
}

// answer is one document value as a peer would write it.
func answer(t *testing.T, c db.Consent, at time.Time) []byte {
	t.Helper()
	raw, err := encode(c, at)
	if err != nil {
		t.Fatalf("encode %q: %v", c, err)
	}
	return raw
}

// repoItems is the repository document's keys, for a failure message.
func repoItems(t *testing.T, d *RepoDoc) map[string][]byte {
	t.Helper()
	items, err := d.Items()
	if err != nil {
		t.Fatalf("RepoDoc.Items: %v", err)
	}
	out := make(map[string][]byte, len(items))
	for k, v := range items {
		out[k] = v
	}
	return out
}

// sessItems is the session document's keys, for a failure message.
func sessItems(t *testing.T, d *SessionDoc) map[string][]byte {
	t.Helper()
	items, err := d.Items()
	if err != nil {
		t.Fatalf("SessionDoc.Items: %v", err)
	}
	out := make(map[string][]byte, len(items))
	for k, v := range items {
		out[k] = v
	}
	return out
}

// answerOf is the answer a fixture's reader sees for a repository URL.
func answerOf(t *testing.T, f *fixture, url string) db.Consent {
	t.Helper()
	c, err := f.shared.RepoConsent(db.Repo{OriginURL: &url})
	if err != nil {
		t.Fatalf("RepoConsent(%s): %v", url, err)
	}
	return c
}

// TestRepoConsentItemsSkipRowsWithoutURL pins what the document is a document
// of: one key per remote URL a person answered for. A repository with no remote
// has no key, so it cannot be carried to a machine that has no such checkout,
// and a repository whose answer was cleared has no key either -- the key's
// absence is the tombstone, and a value saying "unset" would be a third state
// the reader has no rule for.
func TestRepoConsentItemsSkipRowsWithoutURL(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "01HQ0000000000000000000000")

	// A URL with an answer: the one key the document holds.
	if _, err := f.shared.SetRepoConsent(db.Repo{OriginURL: strPtr(testURL)}, db.ConsentYes, baseTime); err != nil {
		t.Fatalf("SetRepoConsent for the URL: %v", err)
	}
	// A common dir and no URL: no key exists to carry it under.
	if _, err := f.shared.SetRepoConsent(db.Repo{CommonDir: strPtr("/repo/.git")}, db.ConsentNo, baseTime); err != nil {
		t.Fatalf("SetRepoConsent for the dir: %v", err)
	}
	// An answer set and then cleared: no value is left to carry.
	if _, err := f.shared.SetRepoConsent(db.Repo{OriginURL: strPtr("https://github.com/o/cleared")}, db.ConsentYes, baseTime); err != nil {
		t.Fatalf("SetRepoConsent yes: %v", err)
	}
	if _, err := f.shared.SetRepoConsent(db.Repo{OriginURL: strPtr("https://github.com/o/cleared")}, db.ConsentUnset, baseTime); err != nil {
		t.Fatalf("SetRepoConsent unset: %v", err)
	}

	items := repoItems(t, f.repo)
	if len(items) != 1 {
		t.Fatalf("the document's keys = %v, want only the answered URL", items)
	}
	if _, held := items[testURL]; !held {
		t.Errorf("the document's keys = %v, want the answered URL %q", items, testURL)
	}
}

// TestRepoConsentApplyUpsertsByURLOnly pins that an arriving answer writes the
// row this machine reads back for that URL, and finds it through the URL alone.
//
// The URL-only lookup is the point. A person's answer arrives naming a URL and
// nothing else, so a lookup that also consulted the common dir would resolve the
// key differently depending on which row this machine happened to hold: the same
// arriving answer would land on a checkout row on one machine and on a URL row
// on another, and RepoConsent would read whichever its own key order reached
// first.
func TestRepoConsentApplyUpsertsByURLOnly(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "01HQ0000000000000000000000")

	// This machine already holds a row for the same checkout under its dir.
	if _, err := f.shared.SetRepoConsent(
		db.Repo{OriginURL: strPtr(testURL), CommonDir: strPtr("/repo/.git")}, db.ConsentUnset, baseTime); err != nil {
		t.Fatalf("SetRepoConsent for the checkout: %v", err)
	}

	if refusal, err := f.applyRepo(t, testURL, answer(t, db.ConsentYes, baseTime)); refusal != nil || err != nil {
		t.Fatalf("ApplyIn = (%v, %v), want the answer to land", refusal, err)
	}

	if got := answerOf(t, f, testURL); got != db.ConsentYes {
		t.Errorf("RepoConsent(%s) = %q, want the arriving yes", testURL, got)
	}
	// The answer did not arrive naming a dir, so nothing wrote one, and the
	// checkout this machine already knew keeps the identity it had.
	rows, err := f.shared.RepoConsents()
	if err != nil {
		t.Fatalf("RepoConsents: %v", err)
	}
	if len(rows) != 1 || rows[0].OriginURL != testURL {
		t.Fatalf("the rows carrying an answer = %+v, want one for the URL", rows)
	}

	// A second arrival is the same state, which is what makes a redelivery a
	// no-op rather than a second edit.
	if refusal, err := f.applyRepo(t, testURL, answer(t, db.ConsentYes, baseTime)); refusal != nil || err != nil {
		t.Fatalf("second ApplyIn = (%v, %v), want it to be a no-op", refusal, err)
	}
	if got := answerOf(t, f, testURL); got != db.ConsentYes {
		t.Errorf("RepoConsent after the redelivery = %q, want yes", got)
	}
}

// TestRepoConsentClearIsATombstone pins what a nil value means: the answer is
// unset, the row is still there, and the key leaves the document so the peers
// holding that answer are told to lift it.
//
// The row surviving is what a tombstone has to mean here. A repository answer
// lives on a row that also holds the URL, the common dir and the first-seen
// stamp, and a peer clearing an answer is not asking this machine to forget
// which repository it has seen.
func TestRepoConsentClearIsATombstone(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "01HQ0000000000000000000000")

	if _, err := f.shared.SetRepoConsent(
		db.Repo{OriginURL: strPtr(testURL), CommonDir: strPtr("/repo/.git")},
		db.ConsentYes, baseTime); err != nil {
		t.Fatalf("SetRepoConsent yes: %v", err)
	}

	if refusal, err := f.applyRepo(t, testURL, nil); refusal != nil || err != nil {
		t.Fatalf("ApplyIn(nil) = (%v, %v), want the clear to land", refusal, err)
	}

	if got := answerOf(t, f, testURL); got != db.ConsentUnset {
		t.Errorf("RepoConsent after the clear = %q, want unset", got)
	}
	if items := repoItems(t, f.repo); len(items) != 0 {
		t.Errorf("the document's keys = %v, want the cleared URL gone", items)
	}
	// The row is still this machine's repository; only the answer left.
	if _, err := f.shared.RepoConsents(); err != nil {
		t.Fatalf("RepoConsents after the clear: %v", err)
	}
}

// TestSessionConsentSyncsAnswerNotTold pins the half of the session row that
// travels and the half that does not.
//
// The told baseline is what this machine has already shown this session here.
// Sending it would tell a peer about a delivery it never made, and a peer that
// believed it had already told the session something would stop telling it. So
// an arriving answer overwrites the answer and leaves the baseline alone, in
// both directions: an arriving answer does not clear it, and clearing the
// answer on this machine does not send it.
func TestSessionConsentSyncsAnswerNotTold(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "01HQ0000000000000000000000")

	// A person answered, and a hook already told the session.
	if err := f.local.SetSessionConsent("claude", "s1", db.ConsentNo, baseTime); err != nil {
		t.Fatalf("SetSessionConsent no: %v", err)
	}
	if err := f.local.SetSessionTold("claude", "s1", "mastermind:architect-1", baseTime); err != nil {
		t.Fatalf("SetSessionTold: %v", err)
	}

	// An arriving answer: the peer says yes for this session.
	if refusal, err := f.applySession(t, "claude/s1", answer(t, db.ConsentYes, baseTime)); refusal != nil || err != nil {
		t.Fatalf("ApplyIn = (%v, %v), want the answer to land", refusal, err)
	}

	got, err := f.local.SessionConsent("claude", "s1")
	if err != nil || got != db.ConsentYes {
		t.Fatalf("SessionConsent after the apply = (%q, %v), want the arriving yes", got, err)
	}
	told, ok, err := f.local.SessionTold("claude", "s1")
	if err != nil || !ok || told != "mastermind:architect-1" {
		t.Errorf("SessionTold after the apply = (%q, %v, %v), want the local baseline kept", told, ok, err)
	}

	// The other direction: the document never carries the baseline at all, so
	// there is no value a peer could put into this machine's told column.
	items := sessItems(t, f.sess)
	raw, held := items["claude/s1"]
	if !held {
		t.Fatalf("the document's keys = %v, want the answered session", items)
	}
	if got := string(raw); contains(got, "told") {
		t.Errorf("the exported value = %s, want the answer alone", got)
	}
}

// TestSessionConsentKeyRefusesBadShape pins the key rule: exactly one kind and
// one id, separated by one slash, neither empty.
//
// Each bad shape is a key that would name a session this machine has never
// seen. A key with no separator names neither; an empty half names a session
// with no id, which every harness would call the same one; and a second slash
// reads as an id containing one, so "claude/a/b" would be stored for session
// "a/b" while the person meant something else entirely.
func TestSessionConsentKeyRefusesBadShape(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "01HQ0000000000000000000000")

	for name, key := range map[string]string{
		"no separator":   "claude",
		"an empty kind":  "/s1",
		"an empty id":    "claude/",
		"only a slash":   "/",
		"two separators": "claude/a/b",
		"the empty key":  "",
	} {
		if refusal, err := f.applySession(t, key, answer(t, db.ConsentYes, baseTime)); refusal == nil && err == nil {
			t.Errorf("ApplyIn accepted a key with %s: %q", name, key)
		}
	}

	rows, err := f.local.SessionConsents()
	if err != nil {
		t.Fatalf("SessionConsents: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("the session rows after the refusals = %+v, want none written", rows)
	}
}

// TestConsentWritesRefuseBadAnswers pins that a value another machine wrote is
// checked rather than stored.
//
// Every refusal here is one an attacker or a newer binary could reach: an answer
// no reader has a rule for, a time that would order the key at a moment of the
// writer's choosing, a field this build has never read, and a shape that is not
// the agreed object at all. Storing any of them would leave a row before every
// reader that treats the answer as a decision.
func TestConsentWritesRefuseBadAnswers(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "01HQ0000000000000000000000")

	for name, value := range map[string][]byte{
		"an answer that is not yes or no": []byte(`{"answer":"maybe","at":"2026-01-01T00:00:00Z"}`),
		"the unset answer":                []byte(`{"answer":"","at":"2026-01-01T00:00:00Z"}`),
		"no answer at all":                []byte(`{"at":"2026-01-01T00:00:00Z"}`),
		"no time at all":                  []byte(`{"answer":"yes"}`),
		"a zero time":                     []byte(`{"answer":"yes","at":"0001-01-01T00:00:00Z"}`),
		"an unparseable time":             []byte(`{"answer":"yes","at":"last thursday"}`),
		"an unknown field":                []byte(`{"answer":"yes","at":"2026-01-01T00:00:00Z","told":"x"}`),
		"an array not an object":          []byte(`["yes","2026-01-01T00:00:00Z"]`),
		"malformed json":                  []byte(`{`),
		"a value of no bytes":             []byte(``),
	} {
		if refusal, err := f.applyRepo(t, testURL, value); refusal == nil && err == nil {
			t.Errorf("the repository document accepted %s", name)
		}
		if refusal, err := f.applySession(t, "claude/s1", value); refusal == nil && err == nil {
			t.Errorf("the session document accepted %s", name)
		}
	}

	if got := answerOf(t, f, testURL); got != db.ConsentUnset {
		t.Errorf("RepoConsent after the refusals = %q, want unset", got)
	}
	rows, err := f.local.SessionConsents()
	if err != nil {
		t.Fatalf("SessionConsents: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("the session rows after the refusals = %+v, want none", rows)
	}
}

// TestConsentWriteStampsAndSignals pins the marks a person's edit leaves, for
// both documents and for a clear.
//
// Each answers a different question: when the key was written here, that it
// changed at all, and, for the session answer, that the two halves of the write
// -- the answer and the stamp -- commit together. A write leaving one behind is
// only half exported, or exported under a time the write did not happen at.
func TestConsentWriteStampsAndSignals(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "01HQ0000000000000000000000")

	if _, err := f.shared.SetRepoConsent(db.Repo{OriginURL: strPtr(testURL)}, db.ConsentYes, baseTime); err != nil {
		t.Fatalf("SetRepoConsent: %v", err)
	}
	if _, ok, err := f.repo.ItemWriteStamp(testURL); err != nil || !ok {
		t.Errorf("the repository key's write stamp = (ok %v, %v), want one", ok, err)
	}
	if signals := docSignals(t, f.local, db.ConsentDoc); len(signals) != 1 {
		t.Errorf("the %s signals = %+v, want one", db.ConsentDoc, signals)
	}

	// A repository with no remote URL has no document key, so it stamps and
	// signals nothing: there is nothing any document carries.
	if _, err := f.shared.SetRepoConsent(db.Repo{CommonDir: strPtr("/other/.git")}, db.ConsentYes, baseTime); err != nil {
		t.Fatalf("SetRepoConsent for the dir: %v", err)
	}
	if signals := docSignals(t, f.local, db.ConsentDoc); len(signals) != 1 {
		t.Errorf("the %s signals after a dir-only answer = %+v, want still one", db.ConsentDoc, signals)
	}

	if err := f.local.SetSessionConsent("claude", "s1", db.ConsentNo, baseTime); err != nil {
		t.Fatalf("SetSessionConsent: %v", err)
	}
	if _, ok, err := f.sess.ItemWriteStamp("claude/s1"); err != nil || !ok {
		t.Errorf("the session key's write stamp = (ok %v, %v), want one", ok, err)
	}
	if signals := docSignals(t, f.local, db.SessionConsentDoc); len(signals) != 1 {
		t.Errorf("the %s signals = %+v, want one", db.SessionConsentDoc, signals)
	}

	// A clear is a person's edit of the answer too, and a cleared answer has to
	// reach the peers still holding one -- so it stamps and signals as well.
	before, _, err := f.sess.ItemWriteStamp("claude/s1")
	if err != nil {
		t.Fatalf("the stamp before the clear: %v", err)
	}
	if err := f.local.ClearSessionConsent("claude", "s1"); err != nil {
		t.Fatalf("ClearSessionConsent: %v", err)
	}
	after, ok, err := f.sess.ItemWriteStamp("claude/s1")
	if err != nil || !ok {
		t.Fatalf("the cleared key's write stamp = (ok %v, %v), want one", ok, err)
	}
	if after <= before {
		t.Errorf("the clear's stamp %d does not follow the write's %d: a cleared answer must still travel", after, before)
	}
}

// TestToldWriteIsUnstamped pins the housekeeping that surrounds an answer and
// records nothing.
//
// A told baseline is not the answer and does not change it, so a write of one
// must not spend a stamp: a stamp says this machine last wrote the answer, and
// spending one on a delivery would let a hook that merely displayed the session
// outrank a peer's newer answer to the question.
func TestToldWriteIsUnstamped(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "01HQ0000000000000000000000")

	if err := f.local.SetSessionConsent("claude", "s1", db.ConsentYes, baseTime); err != nil {
		t.Fatalf("SetSessionConsent: %v", err)
	}
	stamped, ok, err := f.sess.ItemWriteStamp("claude/s1")
	if err != nil || !ok {
		t.Fatalf("the answer's write stamp = (ok %v, %v), want one", ok, err)
	}
	signals := len(docSignals(t, f.local, db.SessionConsentDoc))

	if err := f.local.SetSessionTold("claude", "s1", "mastermind:architect-2", baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("SetSessionTold: %v", err)
	}

	after, ok, err := f.sess.ItemWriteStamp("claude/s1")
	if err != nil || !ok {
		t.Fatalf("the stamp after the told write = (ok %v, %v), want one", ok, err)
	}
	if after != stamped {
		t.Errorf("the told write moved the stamp from %d to %d, want it unchanged", stamped, after)
	}
	if got := len(docSignals(t, f.local, db.SessionConsentDoc)); got != signals {
		t.Errorf("the told write added %d signals, want none", got-signals)
	}
	// And the answer itself is untouched: the document still carries it.
	if items := sessItems(t, f.sess); len(items) != 1 {
		t.Errorf("the document's keys = %v, want the answered session", items)
	}
}

// TestAnArrivingAnswerStampsNothing pins the last half of the rule: a write
// that is not a person's edit of the answer spends no stamp.
//
// The stamp is what tells a peer that this machine last wrote the key, and a
// peer wrote it over there. Stamping one anyway would make this machine's
// applied history claim a write it never made, and the next arrival stamped
// between the two would be weighed against a write that does not exist.
func TestAnArrivingAnswerStampsNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "01HQ0000000000000000000000")

	if _, err := f.shared.SetRepoConsent(db.Repo{OriginURL: strPtr(testURL)}, db.ConsentYes, baseTime); err != nil {
		t.Fatalf("SetRepoConsent: %v", err)
	}
	before, _, err := f.repo.ItemWriteStamp(testURL)
	if err != nil {
		t.Fatalf("the stamp before the arrival: %v", err)
	}

	if refusal, err := f.applyRepo(t, testURL, answer(t, db.ConsentNo, baseTime.Add(time.Hour))); refusal != nil || err != nil {
		t.Fatalf("ApplyIn = (%v, %v), want the answer to land", refusal, err)
	}

	after, ok, err := f.repo.ItemWriteStamp(testURL)
	if err != nil || !ok {
		t.Fatalf("the stamp after the arrival = (ok %v, %v), want one", ok, err)
	}
	if after != before {
		t.Errorf("the arriving answer moved the stamp from %d to %d, want it unchanged", before, after)
	}
	// The answer did land -- an unstamped write is not an unapplied one.
	if got := answerOf(t, f, testURL); got != db.ConsentNo {
		t.Errorf("RepoConsent after the arrival = %q, want the arriving no", got)
	}
}

// TestAPeerRowDoesNotAnswerForThisMachine is the correction this round exists
// for: a repository row reaches every machine through the row log, so a machine
// holding several installations' rows for one URL holds several answers to the
// same question. Only this machine's own row answers for it.
//
// Before the correction the read asked only for the URL, so a machine that had
// synced with a peer returned whichever row the query reached first -- the
// peer's answer, or its own, by an order nothing chose. A person's decision
// about a repository must not be answerable from what another person decided
// about it on another machine, and the two orders have to be the two machines'
// own rows every time.
func TestAPeerRowDoesNotAnswerForThisMachine(t *testing.T) {
	t.Parallel()
	const peerOrigin = "01HQPEER0000000000000000000"
	f := newFixture(t, "01HQ0000000000000000000000")

	// Put a peer's row on this machine's file, the way the row log would have
	// delivered it: the same row another installation wrote, arriving with its
	// origin attached and its own answer on it.
	seedPeerRepoRow(t, f.shared, peerOrigin, testURL, db.ConsentYes, baseTime)

	// This machine answers no for itself.
	if _, err := f.shared.SetRepoConsent(db.Repo{OriginURL: strPtr(testURL)}, db.ConsentNo, baseTime); err != nil {
		t.Fatalf("SetRepoConsent: %v", err)
	}

	// The peer's row is here and says yes. It must not answer for this machine.
	if got := answerOf(t, f, testURL); got != db.ConsentNo {
		t.Errorf("RepoConsent = %q, want this machine's own no, not the peer's row", got)
	}
	// And the document carries only this machine's answer, so a machine holding
	// both rows does not export the peer's as its own.
	items := repoItems(t, f.repo)
	if len(items) != 1 {
		t.Errorf("the document's keys = %v, want only this machine's own row", items)
	}
}

// TestSessionRowsForUnknownSessionsSitHarmlessly pins that a session this
// machine has never seen is still a valid key.
//
// A session id is not a repository: a peer that has run a session this machine
// has not will hand over its answer, and refusing the key because the session
// is unknown here would mean the answer could never arrive in the first place.
// The document carries the answer and nothing that needs the session to exist,
// so the row lands and the told baseline stays empty.
func TestSessionRowsForUnknownSessionsSitHarmlessly(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "01HQ0000000000000000000000")

	if refusal, err := f.applySession(t, "opencode/ses_unknown", answer(t, db.ConsentYes, baseTime)); refusal != nil || err != nil {
		t.Fatalf("ApplyIn for an unseen session = (%v, %v), want the answer to land", refusal, err)
	}
	got, err := f.local.SessionConsent("opencode", "ses_unknown")
	if err != nil || got != db.ConsentYes {
		t.Errorf("SessionConsent for the unseen session = (%q, %v), want the arriving yes", got, err)
	}
	if _, ok, err := f.local.SessionTold("opencode", "ses_unknown"); err != nil || ok {
		t.Errorf("SessionTold for the unseen session = (ok %v, %v), want no baseline invented", ok, err)
	}
}

// seedPeerRepoRow writes another installation's repository row onto this
// machine's shared file, the way the row log delivers one: a row carrying the
// writer's origin, its own consent and its own time.
//
// It opens a second handle over the same file rather than reaching into the
// package's SQL, because what the test needs to be true is that the row is
// reachable the way a delivered row is -- written by an installation with a
// different origin, sitting in this machine's own table. A row inserted by hand
// would be the same row, but reaching the schema directly would make the test
// depend on its columns rather than on the delivery it is standing for.
func seedPeerRepoRow(t *testing.T, shared *db.DB, origin, url string, c db.Consent, at time.Time) {
	t.Helper()
	path := shared.Path()
	peer, err := db.OpenSplit(path, db.Options{Origin: origin})
	if err != nil {
		t.Fatalf("db.OpenSplit as the peer: %v", err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	if _, err := peer.SetRepoConsent(db.Repo{OriginURL: &url}, c, at); err != nil {
		t.Fatalf("the peer's SetRepoConsent: %v", err)
	}
}

// TestNoConfigSectionIsAConsentStampPrefix pins that the consent stamps can
// share one table with the config's bare keys.
//
// They can, because a document's stamps are prefixed and the two sets cannot
// meet: no config section is named after a document, so a section can never be
// read back as a document's stamp key, or the reverse. Without that, a config
// section called "consent" and a document called "consent" would write the same
// stamp key -- and the two mean different things, so a config write would claim
// to have answered a repository.
func TestNoConfigSectionIsAConsentStampPrefix(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{db.ConsentDoc, db.SessionConsentDoc} {
		for _, sec := range config.Sections {
			if string(sec) == prefix || strings.HasPrefix(string(sec), prefix+"/") {
				t.Errorf("the config section %q meets the consent stamp prefix %q", sec, prefix)
			}
		}
	}
}

// docSignals is the export signals naming one document on a machine.
func docSignals(t *testing.T, d *db.DB, doc string) []string {
	t.Helper()
	signals, err := d.DocSignals()
	if err != nil {
		t.Fatalf("DocSignals: %v", err)
	}
	var out []string
	for _, sig := range signals {
		if sig.Doc == doc {
			out = append(out, sig.Doc)
		}
	}
	return out
}

func strPtr(s string) *string { return &s }

// contains reports whether the value carries a field name, used by the told
// test to read an exported value as text rather than decoding it twice.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
