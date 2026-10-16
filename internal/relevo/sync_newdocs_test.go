package relevo

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/db"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// These tests cover the documents the config document's neighbours added --
// gates, repository consent, session consent and the secrets document -- at the
// seams a pipeline stage can get wrong: which stage binds them, what a late
// joiner does to them, and what a locked machine does with them.

// stageConstructors are the calls that build an exporter, an importer or a
// reconciler, which answer for no document of this machine until BindDocs binds
// them.
var stageConstructors = []string{"NewExporter(", "NewImporter(", "NewReconciler("}

// TestAllPipelineStagesBindTheNewDocs pins the wiring audit: every place a
// pipeline stage is built binds the machine's documents, so a document added to
// sharedDocs reaches the steady pipeline, a join, an unlock and every verb.
//
// A stage built and left unbound answers from the process-wide registry, which
// holds none of this machine's documents, so it would hold their origin or skip
// them without any error. The count per file is the audit: a new construction
// site that forgets the bind makes the numbers differ.
func TestAllPipelineStagesBindTheNewDocs(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..")
	built := 0
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") ||
			strings.Contains(filepath.ToSlash(path), "internal/synclog/") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(raw)
		stages := 0
		for _, c := range stageConstructors {
			stages += strings.Count(src, "synclog."+c)
		}
		if stages == 0 {
			return nil
		}
		built += stages
		if bound := strings.Count(src, "BindDocs("); bound != stages {
			t.Errorf("%s builds %d pipeline stages and binds the documents on %d; each stage must be bound",
				path, stages, bound)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk the sources: %v", err)
	}
	if built < 7 {
		t.Errorf("the audit found %d pipeline stages, want the seven known ones: the walk is not reaching them", built)
	}
}

// TestKeyedVerbStagesCarryTheNewDocs pins the same wiring by what it does: the
// stages the verbs build with the stored key export and import all three
// documents, with nothing bound by hand.
func TestKeyedVerbStagesCarryTheNewDocs(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newNewDocsMachine(t, log, cfgSyncKey, true)
	b := newNewDocsMachine(t, log, cfgSyncKey, true)

	a.writeAll(t, time.Now().Add(time.Hour).UTC(), db.ConsentNo)
	a.export(t)
	b.pull(t)

	b.assertHolds(t, "the keyed verb stages", db.ConsentNo)
}

// newDocsMachine is one machine that can write and read each of the three
// documents through the paths a person's edit reaches.
type newDocsMachine struct {
	*gatesMachine
	consent *consentMachine
}

func newNewDocsMachine(t *testing.T, log *synclog.MemTransport, key []byte, enabled bool) *newDocsMachine {
	t.Helper()
	g := newGatesMachine(t, log, key, enabled)
	return &newDocsMachine{gatesMachine: g, consent: &consentMachine{cfgMachine: g.cfgMachine}}
}

// writeAll records one gate and one answer for each consent document.
func (m *newDocsMachine) writeAll(t *testing.T, until time.Time, c db.Consent) {
	t.Helper()
	m.gate(t, "other", until)
	m.consent.answerRepo(t, consentURL, c)
	m.consent.answerSession(t, "claude", "s1", c)
}

// gateUntil is the end of the machine's live rate limit on a subject.
func (m *newDocsMachine) gateUntil(t *testing.T, subject string) time.Time {
	t.Helper()
	l, err := availability.LoadLedger(m.deps.Gates)
	if err != nil {
		t.Fatalf("LoadLedger: %v", err)
	}
	for _, e := range l.Prune(m.deps.Now()).Entries {
		if e.Kind == availability.RateLimited && e.Subject == subject {
			return e.Until
		}
	}
	return time.Time{}
}

// assertHolds fails unless the machine holds the gate and both answers.
func (m *newDocsMachine) assertHolds(t *testing.T, why string, c db.Consent) {
	t.Helper()
	if got := m.liveGates(t); len(got) != 1 || got[0] != "other" {
		t.Errorf("%s: live gates = %v, want the peer's rate limit", why, got)
	}
	if got := m.consent.repoAnswer(t, consentURL); got != c {
		t.Errorf("%s: repository answer = %q, want %q", why, got, c)
	}
	if got := m.consent.sessionAnswer(t, "claude", "s1"); got != c {
		t.Errorf("%s: session answer = %q, want %q", why, got, c)
	}
}

// TestLateJoinerDoesNotOutrankGatesOrConsents pins that a machine joining after
// a peer already recorded a value does not win the key because it wrote its own
// later in wall-clock time. What it held before it joined was written before it
// had a peer, so the peer's value, already shared, stands on both machines.
func TestLateJoinerDoesNotOutrankGatesOrConsents(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newNewDocsMachine(t, log, cfgSyncKey, true)
	b := newNewDocsMachine(t, log, cfgSyncKey, false)

	peerUntil := time.Now().Add(time.Hour).UTC()
	a.writeAll(t, peerUntil, db.ConsentNo)
	a.attempt(t)

	// B wrote the same three keys later and differently, before it joined.
	time.Sleep(20 * time.Millisecond)
	b.writeAll(t, peerUntil.Add(time.Hour), db.ConsentYes)

	b.join(t)
	b.attempt(t)
	a.attempt(t)
	b.attempt(t)

	a.assertHolds(t, "the machine that was already in", db.ConsentNo)
	b.assertHolds(t, "the late joiner", db.ConsentNo)
	if got, want := b.gateUntil(t, "other"), peerUntil; !got.Equal(want) {
		t.Errorf("the late joiner holds a gate until %v, want the peer's %v", got, want)
	}
	if got := a.gateUntil(t, "other"); !got.Equal(peerUntil) {
		t.Errorf("the peer's gate moved to %v: the late joiner outranked it", got)
	}
}

// TestLockedJoinParksNewDocs pins that a machine that joins without the key
// parks the three documents' entries rather than applying them, and applies
// them when it is unlocked.
func TestLockedJoinParksNewDocs(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newNewDocsMachine(t, log, cfgSyncKey, true)
	b := newNewDocsMachine(t, log, nil, false)

	a.writeAll(t, time.Now().Add(time.Hour).UTC(), db.ConsentNo)
	a.attempt(t)

	b.join(t)
	b.attempt(t)

	if got := b.liveGates(t); len(got) != 0 {
		t.Errorf("the locked machine applied the gates %v, want them parked", got)
	}
	if got := b.consent.repoAnswer(t, consentURL); got != db.ConsentUnset {
		t.Errorf("the locked machine applied the repository answer %q, want it parked", got)
	}
	parked := map[string]bool{}
	pending, err := b.mdb.PendingDocs()
	if err != nil {
		t.Fatalf("PendingDocs: %v", err)
	}
	for _, p := range pending {
		for _, name := range []string{availability.GatesName, db.ConsentDoc, db.SessionConsentDoc} {
			if strings.Contains(p.Entry, name) {
				parked[name] = true
			}
		}
	}
	for _, name := range []string{availability.GatesName, db.ConsentDoc, db.SessionConsentDoc} {
		if !parked[name] {
			t.Errorf("no parked entry names the %s document: %d parked in all", name, len(pending))
		}
	}

	if err := relevosync.SetKey(b.local, cfgSyncKey, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	if _, err := relevosync.DrainParkedDocs(b.mdb, b.local, log.OnLog(b.mdb.Origin())); err != nil {
		t.Fatalf("DrainParkedDocs: %v", err)
	}
	b.assertHolds(t, "after the unlock", db.ConsentNo)
}

// TestKeyedImportDrainsParkedNewDocs pins that an ordinary keyed import, not
// only the unlock verb, applies what a locked machine parked: the machine is
// unlocked by setting the key and the next pull brings the entries in.
func TestKeyedImportDrainsParkedNewDocs(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newNewDocsMachine(t, log, cfgSyncKey, true)
	b := newNewDocsMachine(t, log, nil, true)

	a.writeAll(t, time.Now().Add(time.Hour).UTC(), db.ConsentNo)
	a.attempt(t)
	b.attempt(t)
	if pending, err := b.mdb.PendingDocs(); err != nil || len(pending) == 0 {
		t.Fatalf("pending = %d, %v; want the entries parked", len(pending), err)
	}

	if err := relevosync.SetKey(b.local, cfgSyncKey, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	b.pull(t)

	b.assertHolds(t, "after a keyed import", db.ConsentNo)
	if pending, err := b.mdb.PendingDocs(); err != nil || len(pending) != 0 {
		t.Errorf("pending after the drain = %d, %v; want none left", len(pending), err)
	}
}
