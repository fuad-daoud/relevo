package relevo

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
)

// servedShapeRuntime returns rt as internal/serve's runtimeAt builds one: the
// machine database's ledger bound under the "serve." prefix for Gates, and --
// the fix's seam -- the same machine database's unprefixed row as GatesLocal.
// Without GatesLocal wired, the served pick sees only the prefixed row.
func servedShapeRuntime(t *testing.T, rt Runtime) Runtime {
	t.Helper()
	local := rt.Gates
	rt.Gates = db.PrefixKV{KV: local, Prefix: "serve."}
	rt.GatesLocal = local
	return rt
}

// gateHostLocal writes a live rate-limit gate on subject into the machine's
// unprefixed ledger row -- exactly what `relevo gate <token>` leaves on the
// server host (cmd/relevo/gate.go: gateUnavailable -> availability.Unavailable
// -> saveLedger on the machine-local file).
func gateHostLocal(t *testing.T, local db.KV, subject string) {
	t.Helper()
	err := availability.SaveLedger(local, availability.Ledger{Entries: []availability.Entry{
		{Kind: availability.RateLimited, Subject: subject, At: baseTime, Until: baseTime.Add(time.Hour), Source: "relevo"},
	}})
	if err != nil {
		t.Fatalf("gate the host ledger: %v", err)
	}
}

// TestServedPickSkipsAGateInTheHostMachineLocalLedger is the round's red test:
// a served runtime whose machine-local ledger row holds a live rate limit on
// the first-ranked candidate's provider must skip that candidate, exactly as
// the same gate skips it on a local pick. Untouched code reads only the
// "serve."-prefixed row and picks the gated candidate anyway.
func TestServedPickSkipsAGateInTheHostMachineLocalLedger(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, `[
  {"harness":"claude","provider":"test","model":"a","roles":["builder"]},
  {"harness":"agy","provider":"other","model":"b","roles":["builder"]}
]`)
	reg := rolesFileRegistry(t, set, policy.Policy{}, map[string]roles.Row{
		"builder": {Candidates: []string{"claude/test/a", "agy/other/b"}},
	})
	rt := Runtime{Candidates: set, Registry: reg, Gates: testGateKV(t), Now: func() time.Time { return baseTime }}
	rt = servedShapeRuntime(t, rt)

	gateHostLocal(t, rt.GatesLocal, "test")

	token, _, err := PickServedCandidate(rt, "")
	if err != nil {
		t.Fatalf("PickServedCandidate: %v", err)
	}
	if token != "agy/other/b" {
		t.Errorf("served pick = %q, want %q: a live gate in the host's machine-local ledger must steer the served pick", token, "agy/other/b")
	}
}

// TestServedPickHonoursGateRecordedByHostGateVerb is the round's fourth case
// end of the writer: the gate is recorded through the same call
// `relevo gate <token>` makes -- availability.Unavailable on a runtime whose
// Gates is the machine-local row -- and the served pick must honour it.
func TestServedPickHonoursGateRecordedByHostGateVerb(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Candidates = candidateSet(t, `[
  {"harness":"claude","provider":"test","model":"a","roles":["builder"]},
  {"harness":"agy","provider":"other","model":"b","roles":["builder"]}
]`)
	rt.Registry = rolesFileRegistry(t, rt.Candidates, policy.Policy{}, map[string]roles.Row{
		"builder": {Candidates: []string{"claude/test/a", "agy/other/b"}},
	})
	// The host's own runtime, exactly as cmd/relevo/wire.go buildRuntime makes
	// one on the server host: Gates is the unprefixed machine-local row.
	if _, err := availability.Unavailable(AvailabilityDeps(rt), "claude/test/a", baseTime.Add(time.Hour), "quota"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	served := servedShapeRuntime(t, rt)
	token, _, err := PickServedCandidate(served, "")
	if err != nil {
		t.Fatalf("PickServedCandidate: %v", err)
	}
	if token != "agy/other/b" {
		t.Errorf("served pick after a host `relevo gate` = %q, want %q", token, "agy/other/b")
	}
}

// TestGateUnionCountsASubjectHeldByBothRowsOnce: a rate limit on one subject
// recorded in the serve row AND live in the host row -- the shape a gate
// reaches when a client forwarded it and the synced gates document later
// applied it -- renders exactly one gate for the token it gates.
func TestGateUnionCountsASubjectHeldByBothRowsOnce(t *testing.T) {
	t.Parallel()

	rt := servedShapeRuntime(t, Runtime{
		Candidates: candidateSet(t, `[
  {"harness":"claude","provider":"test","model":"a","roles":["builder"]}
]`),
		Gates: testGateKV(t),
		Now:   func() time.Time { return baseTime },
	})
	if err := availability.SaveLedger(rt.Gates, availability.Ledger{Entries: []availability.Entry{
		{Kind: availability.RateLimited, Subject: "test", At: baseTime, Until: baseTime.Add(2 * time.Hour), Source: "relevo"},
	}}); err != nil {
		t.Fatalf("gate the serve row: %v", err)
	}
	gateHostLocal(t, rt.GatesLocal, "test")

	var gated int
	for _, g := range availability.Gates(AvailabilityDeps(rt)) {
		if g.Kind == availability.RateLimited && g.Token == "claude/test/a" {
			gated++
		}
	}
	if gated != 1 {
		t.Errorf("live rate-limit gates on claude/test/a = %d, want 1: a subject held by both rows counts once", gated)
	}
}

func TestGateUnionShowsAHostGateWhoseServeCopyExpired(t *testing.T) {
	t.Parallel()

	rt := servedShapeRuntime(t, Runtime{
		Candidates: candidateSet(t, `[
  {"harness":"claude","provider":"test","model":"a","roles":["builder"]}
]`),
		Gates: testGateKV(t),
		Now:   func() time.Time { return baseTime },
	})
	if err := availability.SaveLedger(rt.Gates, availability.Ledger{Entries: []availability.Entry{
		{Kind: availability.RateLimited, Subject: "test", At: baseTime.Add(-3 * time.Hour), Until: baseTime.Add(-time.Hour), Source: "relevo"},
	}}); err != nil {
		t.Fatalf("gate the serve row: %v", err)
	}
	gateHostLocal(t, rt.GatesLocal, "test")

	var gated int
	for _, g := range availability.Gates(AvailabilityDeps(rt)) {
		if g.Kind == availability.RateLimited && g.Token == "claude/test/a" {
			gated++
		}
	}
	if gated != 1 {
		t.Errorf("live rate-limit gates on claude/test/a = %d, want 1: an expired serve-row entry must not hide the host row's live gate", gated)
	}
}

// TestGateUnionKeepsWritesInTheirOwnRows pins the write rule a served runtime
// needs to keep honouring the union: a gate recorded through the served
// runtime lands in the "serve." row alone, and a ledger document the union is
// asked to save that verbatim repeats a host-row entry is not written into
// the serve row again -- the host row's own copy already gates the subject,
// so no duplicate write is added.
func TestGateUnionKeepsWritesInTheirOwnRows(t *testing.T) {
	t.Parallel()

	rt := servedShapeRuntime(t, Runtime{
		Candidates: candidateSet(t, `[
  {"harness":"claude","provider":"test","model":"a","roles":["builder"]}
]`),
		Store: store.New(t.TempDir()),
		Gates: testGateKV(t),
		Now:   func() time.Time { return baseTime },
	})

	// A served write (the daemon's 429 detection and handleUnavailable's
	// shape): Unavailable through the union's Deps.
	if _, err := availability.Unavailable(AvailabilityDeps(rt), "claude/test/a", baseTime.Add(time.Hour), "5h window"); err != nil {
		t.Fatalf("Unavailable over the union: %v", err)
	}
	serveRow, err := availability.LoadLedger(rt.Gates)
	if err != nil {
		t.Fatal(err)
	}
	if len(serveRow.Entries) != 1 || serveRow.Entries[0].Subject != "test" {
		t.Errorf("serve row = %+v, want the one gate written there", serveRow.Entries)
	}
	hostRow, err := availability.LoadLedger(rt.GatesLocal)
	if err != nil {
		t.Fatal(err)
	}
	if len(hostRow.Entries) != 0 {
		t.Errorf("host row = %+v, want untouched by a served write", hostRow.Entries)
	}

	// The same document, saved again, repeats an entry the host row now
	// holds: gateHostLocal's entry differs in note and source, so first make
	// the host hold exactly what is being saved.
	exact := availability.Ledger{Entries: []availability.Entry{{
		Kind: availability.RateLimited, Subject: "other", At: baseTime, Until: baseTime.Add(time.Hour), Note: "dup", Source: "relevo",
	}}}
	if err := availability.SaveLedger(rt.GatesLocal, exact); err != nil {
		t.Fatalf("seed the host row: %v", err)
	}
	if err := availability.SaveLedger(AvailabilityDeps(rt).Gates, exact); err != nil {
		t.Fatalf("save an entry the host row verbatim holds: %v", err)
	}
	serveRow, err = availability.LoadLedger(rt.Gates)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range serveRow.Entries {
		if e.Subject == "other" {
			t.Errorf("serve row gained a duplicate host entry %+v; want the subject to stay in the host row alone", e)
		}
	}
	hostRow, err = availability.LoadLedger(rt.GatesLocal)
	if err != nil {
		t.Fatal(err)
	}
	if len(hostRow.Entries) != 1 || hostRow.Entries[0].Subject != "other" {
		t.Errorf("host row = %+v, want its own entry kept", hostRow.Entries)
	}
}
