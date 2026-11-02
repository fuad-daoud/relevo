package serve

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/burst"
	"github.com/fuad-daoud/relevo/internal/policy"
)

const burstTestFingerprint = "sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// TestBurstTableReloadKeepsOrder pins the restart path: a head that saved
// its node table comes back with the same rows in the same order --
// provider response order, which placement's tie-break reads -- carrying
// the verified answer, the problem and its since, and the forward marks.
func TestBurstTableReloadKeepsOrder(t *testing.T) {
	d := testServeDB(t)
	at := time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC)
	sent := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	saved := &burst.NodeTable{
		Nodes: []burst.NodeRow{
			{
				ID: "b2", Name: "two", URL: "https://b2.example", Fingerprint: burstTestFingerprint,
				Cap: 2, State: "ready", Verified: true, Problem: "unreachable", ProblemSince: at,
			},
			{
				ID: "b1", Name: "one", URL: "https://b1.example", Fingerprint: burstTestFingerprint,
				Cap: 1, State: "draining", ForwardedAt: &sent, LastSeen: at,
			},
		},
		LastReport: at, LastGood: at.Add(-time.Minute), Interval: 30 * time.Second,
	}
	if err := saveBurstTable(d.LocalOrSelf(), saved); err != nil {
		t.Fatalf("save: %v", err)
	}

	s, err := New(Config{
		DB:        d,
		Root:      t.TempDir(),
		Now:       time.Now,
		ClientKey: []byte("pem-client-key"),
		Burst:     &policy.BurstPolicy{Command: []string{"burst", "report"}},
	})
	if err != nil {
		t.Fatalf("New over a saved table: %v", err)
	}
	got := s.burstTable
	var ids []string
	for _, r := range got.Nodes {
		ids = append(ids, r.ID)
	}
	if !slices.Equal(ids, []string{"b2", "b1"}) {
		t.Fatalf("reloaded order = %v, want the provider response order [b2 b1]", ids)
	}
	r0 := got.Nodes[0]
	if !r0.Verified || r0.Problem != "unreachable" || !r0.ProblemSince.Equal(at) || r0.State != "ready" {
		t.Errorf("reloaded b2 = %+v, want the verified answer and problem carried", r0)
	}
	r1 := got.Nodes[1]
	if r1.ForwardedAt == nil || !r1.ForwardedAt.Equal(sent) || r1.State != "draining" {
		t.Errorf("reloaded b1 = %+v, want the forward mark and state carried", r1)
	}
	if !got.LastReport.Equal(at) || !got.LastGood.Equal(at.Add(-time.Minute)) || got.Interval != 30*time.Second {
		t.Errorf("reloaded marks = %v %v %v, want the report cadence carried", got.LastReport, got.LastGood, got.Interval)
	}
}

// TestBurstTableCorruptRowRefusesStartup pins that a head with bursting on
// boots only over a decodable table: the database keeps the row valid JSON,
// so an undecodable one is a wrong shape, and naming the row in the
// startup error beats running a loop on half a node list.
func TestBurstTableCorruptRowRefusesStartup(t *testing.T) {
	d := testServeDB(t)
	if err := d.LocalOrSelf().KVPut("serve.burst.nodes", []byte(`["not","rows"]`)); err != nil {
		t.Fatalf("seed corrupt row: %v", err)
	}
	_, err := New(Config{
		DB:        d,
		Root:      t.TempDir(),
		Now:       time.Now,
		ClientKey: []byte("pem-client-key"),
		Burst:     &policy.BurstPolicy{Command: []string{"burst", "report"}},
	})
	if err == nil {
		t.Fatal("New over a corrupt node table = nil, want startup refused")
	}
	if !strings.Contains(err.Error(), "serve.burst.nodes") {
		t.Errorf("error = %v, want it to name the row", err)
	}
}

// TestBurstTableNotReadWhileBurstOff pins the gate's promise that a
// misconfigured head keeps serving: with bursting off the row is not even
// read, so a corrupt table cannot stop the local head.
func TestBurstTableNotReadWhileBurstOff(t *testing.T) {
	d := testServeDB(t)
	if err := d.LocalOrSelf().KVPut("serve.burst.nodes", []byte(`["not","rows"]`)); err != nil {
		t.Fatalf("seed corrupt row: %v", err)
	}
	s, err := New(Config{
		DB:    d,
		Root:  t.TempDir(),
		Now:   time.Now,
		Burst: &policy.BurstPolicy{Command: []string{"burst", "report"}},
	})
	if err != nil {
		t.Fatalf("New with bursting off = %v, want the head to start anyway", err)
	}
	if s.burstTable != nil {
		t.Errorf("burstTable = %+v, want none while bursting is off", s.burstTable)
	}
}

// TestBurstTableKvFailureSurfaces pins that both halves report a failed
// row access rather than answering with an empty table or a silent write:
// save-after-merge is the loop's durability, and a lost write must be seen.
func TestBurstTableKvFailureSurfaces(t *testing.T) {
	d := testServeDB(t)
	kv := d.LocalOrSelf()
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := loadBurstTable(kv); err == nil {
		t.Error("loadBurstTable on a closed database = nil, want the read error")
	}
	if err := saveBurstTable(kv, &burst.NodeTable{}); err == nil {
		t.Error("saveBurstTable on a closed database = nil, want the write error")
	}
}
