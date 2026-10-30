package relevo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// These tests drive the gates document through the real sync pipeline on the
// in-memory log: nothing here binds the document by hand, so a construction
// site that forgets to bind it fails.

// gatesFixtureCandidates is one candidate on a provider named "other", so a
// rate limit on that provider gates it and nothing else would be ambiguous.
const gatesFixtureCandidates = `[{"harness":"claude","provider":"other","model":"m","roles":["builder"]}]`

// gatesMachine is one machine's gate surface over a real split pair, so the
// ledger, the write stamps and the export signal all live in the file the
// gates document is bound to.
type gatesMachine struct {
	*cfgMachine
	local  *db.DB
	deps   availability.Deps
	queued int
}

// newGatesMachine is one machine over the same split pair the config tests use.
func newGatesMachine(t *testing.T, log *synclog.MemTransport, key []byte, enabled bool) *gatesMachine {
	t.Helper()
	m := newCfgMachine(t, log, key, enabled)
	local := m.mdb.LocalOrSelf()
	g := &gatesMachine{cfgMachine: m, local: local}
	g.deps = availability.Deps{
		Store:   m.st,
		Gates:   local,
		History: m.mdb,
		Local:   local,
		Now:     time.Now,
		Candidates: func() *candidate.Set {
			set, err := candidate.Load(writeCandidateFixture(t))
			if err != nil {
				t.Fatalf("load the candidate fixture: %v", err)
			}
			return set
		}(),
		SyncNow: func() { g.queued++ },
	}
	return g
}

// writeCandidateFixture writes the one-candidate set and returns its path.
func writeCandidateFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "candidates.json")
	if err := os.WriteFile(path, []byte(gatesFixtureCandidates), 0o644); err != nil {
		t.Fatalf("write the candidate fixture: %v", err)
	}
	return path
}

// gate records a rate limit on subject as a person does, through the same
// write path the CLI and the serve daemon reach.
func (m *gatesMachine) gate(t *testing.T, subject string, until time.Time) {
	t.Helper()
	if _, err := availability.Unavailable(m.deps, subject, until, "quota"); err != nil {
		t.Fatalf("Unavailable(%s): %v", subject, err)
	}
}

// liveGates is the ledger's live rate-limit subjects.
func (m *gatesMachine) liveGates(t *testing.T) []string {
	t.Helper()
	l, err := availability.LoadLedger(m.deps.Gates)
	if err != nil {
		t.Fatalf("LoadLedger: %v", err)
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range l.Prune(m.deps.Now()).Entries {
		if e.Kind != availability.RateLimited || seen[e.Subject] {
			continue
		}
		seen[e.Subject] = true
		out = append(out, e.Subject)
	}
	return out
}

// stamp is the gates document's write stamp for one subject.
func (m *gatesMachine) stamp(t *testing.T, subject string) (synclog.Stamp, bool) {
	t.Helper()
	stamp, ok, err := availability.OpenGates(m.local, nil).ItemWriteStamp(subject)
	if err != nil {
		t.Fatalf("ItemWriteStamp(%s): %v", subject, err)
	}
	return stamp, ok
}

// gateSignals is the export signals naming the gates document on this machine.
func (m *gatesMachine) gateSignals(t *testing.T) []string {
	t.Helper()
	signals, err := m.local.DocSignals()
	if err != nil {
		t.Fatalf("DocSignals: %v", err)
	}
	var out []string
	for _, sig := range signals {
		if sig.Doc == availability.GatesName {
			out = append(out, sig.Doc)
		}
	}
	return out
}

// TestTwoMachinesConvergeGatesWithLoserSuperseded pins the whole exchange on
// the real steady pipeline: a rate limit a person records on one machine reaches
// the other, and where both machines gate the same provider the later edit wins
// on both, with the earlier one recorded as superseded rather than applied
// second. Nothing binds the gates document by hand here, so a wiring site that
// forgot it would leave both machines reading their own ledger.
func TestTwoMachinesConvergeGatesWithLoserSuperseded(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newGatesMachine(t, log, cfgSyncKey, true)
	b := newGatesMachine(t, log, cfgSyncKey, true)

	until := time.Now().Add(time.Hour).UTC()
	a.gate(t, "other", until)
	a.attempt(t)
	b.attempt(t)

	if got := b.liveGates(t); len(got) != 1 || got[0] != "other" {
		t.Fatalf("B's live gates = %v, want A's rate limit on the shared provider", got)
	}

	// The loser: B records a later gate on the same provider, so A's earlier
	// entry loses once B's arrives.
	time.Sleep(20 * time.Millisecond)
	b.gate(t, "other", until)
	b.attempt(t)
	a.attempt(t)

	if got := a.liveGates(t); len(got) != 1 || got[0] != "other" {
		t.Errorf("A's live gates = %v, want the one subject", got)
	}
	var applied db.DocApplied
	var ok bool
	if err := a.local.Tx(func(tx *db.Tx) error {
		var err error
		applied, ok, err = tx.DocApplied(availability.GatesName, "other")
		return err
	}); err != nil || !ok {
		t.Fatalf("A's applied state for the subject = (ok %v, %v), want one", ok, err)
	}
	if applied.Origin != b.mdb.Origin() {
		t.Errorf("A's applied entry names origin %q, want B's %q: the later edit wins", applied.Origin, b.mdb.Origin())
	}
}

// TestJoinClearsGatesStamps pins that a join forgets this machine's own gate
// history, so a machine joining with a gate it recorded before it had a peer
// re-emits that gate rather than claiming it outranks whatever the peer recorded
// since. The stamps are the whole of that claim: they are what says this
// machine last wrote the key, and a join is exactly when that stops being true
// of anything the machine held before.
func TestJoinClearsGatesStamps(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newGatesMachine(t, log, cfgSyncKey, true)
	b := newGatesMachine(t, log, cfgSyncKey, false)

	b.gate(t, "other", time.Now().Add(time.Hour).UTC())
	if _, ok := b.stamp(t, "other"); !ok {
		t.Fatal("the gate write recorded no stamp before the join")
	}

	b.join(t)

	if _, ok := b.stamp(t, "other"); ok {
		t.Error("the join left the machine's pre-join gate stamp behind")
	}
	if len(b.gateSignals(t)) == 0 {
		t.Error("the join did not signal the gates document, so the re-emit never runs")
	}

	// The re-emit is what actually leaves: the gate the machine held before it
	// joined is exported again under a fresh stamp rather than being withheld.
	b.export(t)
	a.attempt(t)
	if got := a.liveGates(t); len(got) != 1 || got[0] != "other" {
		t.Errorf("A's live gates = %v, want the joining machine's gate re-emitted", got)
	}
}

// TestGateWriteInsideDebounceStartsAnAttempt pins that a gate write reaches the
// other machines even when it lands inside the export debounce. The debounce
// exists so a burst of writes costs one attempt rather than one each; a gate a
// person has just recorded is the one write whose whole point is that the other
// machines see it now, so it skips the age check -- and only the age check.
func TestGateWriteInsideDebounceStartsAnAttempt(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newGatesMachine(t, log, cfgSyncKey, true)
	b := newGatesMachine(t, log, cfgSyncKey, true)

	// The daemon's runtime carries the trigger, which is how a gate the daemon
	// itself records reaches the daemon's own slot.
	d := NewDaemon(Runtime{Store: a.st, Sync: a.runner}, time.Second)
	d.BindSyncNow(context.Background())

	// One attempt through the daemon first, so its last start is now and every
	// trigger from here on falls inside the export debounce.
	d.SyncNow(context.Background())
	waitSyncIdle(t, d)
	d.syncMu.Lock()
	started := d.live.StartedAt
	d.syncMu.Unlock()
	if started.IsZero() {
		t.Fatal("the daemon recorded no attempt start, so nothing below is inside the debounce")
	}

	// The gate write is now well inside the gap, and asks for an attempt.
	a.deps.SyncNow = func() { d.SyncNow(context.Background()) }
	a.gate(t, "other", time.Now().Add(time.Hour).UTC())
	waitSyncIdle(t, d)

	if signals := a.gateSignals(t); len(signals) != 0 {
		t.Errorf("the gates signals after the triggered attempt = %+v, want the debounced attempt to have run", signals)
	}
	b.attempt(t)
	if got := b.liveGates(t); len(got) != 1 || got[0] != "other" {
		t.Errorf("B's live gates = %v, want the gate the debounce would have delayed", got)
	}
}

// TestGateBurstInsideOneAttemptCostsOneRerun pins the coalescing half of the
// same rule: several gate writes arriving while one attempt holds the slot mark
// that attempt dirty rather than starting one attempt each. The burst is what a
// person gating several accounts of one provider produces, and each of those
// writes asked for an attempt, so a trigger that started a new one each would
// put six attempts behind a network that is the slow part.
func TestGateBurstInsideOneAttemptCostsOneRerun(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newGatesMachine(t, log, cfgSyncKey, true)
	d := NewDaemon(Runtime{Store: a.st, Sync: a.runner}, time.Second)

	// Hold the slot, so every trigger below lands inside an attempt that is
	// already running rather than starting one of its own.
	d.syncMu.Lock()
	d.syncInFlight = true
	d.syncMu.Unlock()

	for range 6 {
		a.gate(t, "other", time.Now().Add(time.Hour).UTC())
		d.SyncNow(context.Background())
	}

	d.syncMu.Lock()
	dirty := d.live.Dirty
	inFlight := d.syncInFlight
	d.syncMu.Unlock()
	if !dirty {
		t.Error("the bursts did not mark the in-flight attempt dirty, so the writes would be dropped")
	}
	if !inFlight {
		t.Error("a trigger while the slot was held released the slot, so the next trigger would start a second attempt")
	}
}

// TestGatesDocIsBoundThroughSharedDocs pins the wiring rule: the document is
// built by the one function every pipeline stage reaches, so the steady
// pipeline, a join and a keyed verb all see it without any of them naming one.
func TestGatesDocIsBoundThroughSharedDocs(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newGatesMachine(t, log, cfgSyncKey, true)

	var names []string
	for _, d := range relevosync.SharedDocs(a.mdb) {
		names = append(names, d.Name())
	}
	if !strings.Contains(strings.Join(names, ","), availability.GatesName) {
		t.Errorf("the machine's documents = %v, want the gates document among them", names)
	}
}
