package relevo

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/store"
)

// Round 3 made every display read combine every origin's history rows, because
// what a panel shows is the window across the fleet. The other half of that
// round is what must not change: a pick reads the live ledger and the gates
// document, and neither the ledger nor a history row is where a peer's outage
// or a peer's latency reaches this machine's scheduling.
//
// A peer's rate limit does gate here, by design: a provider's quota is the
// provider's, so the gates document carries it to every machine. A peer's
// history is a different thing -- it is a record of what happened on a machine
// this one does not run a round on.

// historyPickRuntime is a runtime over the three-ref candidate set with a real
// shared file behind it, which is where a peer's history rows land.
func historyPickRuntime(t *testing.T, origin string) (Runtime, *db.DB) {
	t.Helper()
	shared, err := db.OpenWith(filepath.Join(t.TempDir(), "relevo.db"), db.Options{Origin: origin})
	if err != nil {
		t.Fatalf("open the shared file: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })

	rt := Runtime{
		Store:      store.New(t.TempDir()),
		Candidates: candidateSet(t, testCandidatesJSON),
		Gates:      testGates(t),
		History:    shared,
		Now:        func() time.Time { return baseTime },
	}
	return rt, shared
}

// seedPeerHistory writes one outage event and one slow sample under another
// machine's origin, exactly as an import leaves them: this machine holds both
// and owns neither.
func seedPeerHistory(t *testing.T, shared *db.DB, peer string, token string) {
	t.Helper()
	at := db.UnixMS(baseTime.Add(-time.Hour))
	if err := shared.InsertAvailabilityEvent(peer, db.AvailabilityEvent{
		ID: db.NewID(), AtMS: at, Kind: string(availability.RateLimited),
		Provider: "test", Token: token, Source: "planner", Note: "5h",
	}); err != nil {
		t.Fatalf("seed the peer outage event: %v", err)
	}
	if err := shared.InsertLatencySample(peer, db.LatencySample{
		ID: db.NewID(), AtMS: at, Token: token, Host: "peer-host", TTFTMS: 30_000, TotalMS: 40_000,
	}); err != nil {
		t.Fatalf("seed the peer slow sample: %v", err)
	}
}

// TestPeerHistoryNeverGatesAPick is Round 3 step 2: a peer's outage and a peer's
// slow sample are present, and this machine's pick is the one it would have made
// with an empty shared file. The peer-origin rows do reach a display read -- the
// assertion below proves they are readable, so the test cannot pass by the rows
// having failed to arrive -- and they still change nothing a pick reads.
func TestPeerHistoryNeverGatesAPick(t *testing.T) {
	t.Parallel()

	peer := "01PEERORIGIN"

	// The same two runtimes, one with a peer's rows and one without, so the
	// claim is a comparison rather than an expectation about one machine.
	bare, _ := historyPickRuntime(t, "01BAREORIGIN")
	loaded, shared := historyPickRuntime(t, "01LOADEDORIG")
	seedPeerHistory(t, shared, peer, testClaudeRef)

	// The rows are there and a display read sees them. Without this the test
	// would also pass on a machine that had simply failed to import.
	h, err := availability.LoadHistory(AvailabilityDeps(loaded))
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if len(h.Events) != 1 || h.Events[0].Token != testClaudeRef {
		t.Fatalf("the display read sees %+v, want the peer's event", h.Events)
	}
	lat, err := availability.LoadLatency(AvailabilityDeps(loaded))
	if err != nil {
		t.Fatalf("LoadLatency: %v", err)
	}
	if s := lat.Summary(testClaudeRef); s.N != 1 || s.TTFTP50MS != 30_000 {
		t.Fatalf("the display summary is %+v, want the peer's slow sample", s)
	}

	// The order decides the pick, so an empty token asks the question a round
	// asks: given this machine's ledger, which candidate serves the role.
	pol := orderOf("builder", testOpencodeRef, testClaudeRef, testAgyRef)

	before, err := resolve.Resolve(bare.Candidates, pol, availability.Gates(AvailabilityDeps(bare)), "", "builder")
	if err != nil {
		t.Fatalf("resolveCandidate on the bare runtime: %v", err)
	}
	after, err := resolve.Resolve(loaded.Candidates, pol, availability.Gates(AvailabilityDeps(loaded)), "", "builder")
	if err != nil {
		t.Fatalf("resolveCandidate with the peer's history present: %v", err)
	}

	if !reflect.DeepEqual(after, before) {
		t.Errorf("pick = %+v, want the bare machine's %+v: a peer's history is not this machine's to schedule around", after, before)
	}
	if after.Candidate.Ref().String() != testOpencodeRef {
		t.Errorf("picked %s, want %s", after.Candidate.Ref().String(), testOpencodeRef)
	}
	if len(after.Skipped) != 0 {
		t.Errorf("skipped = %+v, want none: the peer's outage must not skip this machine's candidate", after.Skipped)
	}
}

// TestSpawnFailedStaysLocal is the second half of the locality rule: a
// spawn_failed mirror is this machine's own record of a process it could not
// start, and it never leaves. A peer-origin spawn_failed row in the history is
// therefore a row this machine holds and must not act on -- a gate from it would
// park this machine's builder on a cooldown for a failure that happened
// elsewhere.
func TestSpawnFailedStaysLocal(t *testing.T) {
	t.Parallel()

	peer := "01PEERORIGIN"
	rt, shared := historyPickRuntime(t, "01OWNORIGIN")

	// The peer's spawn_failed mirror, present in the shared file.
	if err := shared.InsertAvailabilityEvent(peer, db.AvailabilityEvent{
		ID: db.NewID(), AtMS: db.UnixMS(baseTime.Add(-time.Minute)),
		Kind: string(availability.SpawnFailed), Provider: "test",
		Token: testOpencodeRef, Source: "relevo", Binding: "peers-binding",
	}); err != nil {
		t.Fatalf("seed the peer's spawn_failed mirror: %v", err)
	}

	gates := availability.Gates(AvailabilityDeps(rt))
	for _, g := range gates {
		if g.Kind == availability.SpawnFailed && g.Token == testOpencodeRef {
			t.Errorf("gates carry a spawn_failed on %s, which only this machine can raise", testOpencodeRef)
		}
	}

	pol := orderOf("builder", testOpencodeRef, testClaudeRef, testAgyRef)
	got, err := resolve.Resolve(rt.Candidates, pol, gates, "", "builder")
	if err != nil {
		t.Fatalf("resolveCandidate: %v", err)
	}
	if got.Candidate.Ref().String() != testOpencodeRef {
		t.Errorf("picked %s, want %s: a peer's spawn_failed must not move this machine's pick", got.Candidate.Ref().String(), testOpencodeRef)
	}
}
