package sync

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// TestJoinClearsExportState pins that a fresh join forgets what was exported to
// the log this machine had before, and signals the doc so the next export sends
// every key again.
func TestJoinClearsExportState(t *testing.T) {
	shared, local := joinPair(t, "m1")
	passGate(t, shared)
	seeded, err := local.DocSignals()
	if err != nil || len(seeded) != 1 {
		t.Fatalf("signals at open = %v, %v; want the migration's seed", seeded, err)
	}
	if err := local.RecordDocExport(seeded[0], []db.DocExportWrite{
		{Key: "policy", Origin: "m1", Stamp: "5", Digest: "d1"},
		{Key: "roles", Origin: "m1", Stamp: "6", Digest: "d2"},
	}); err != nil {
		t.Fatalf("seed the export state: %v", err)
	}
	if signals, err := local.DocSignals(); err != nil || len(signals) != 0 {
		t.Fatalf("signals before the join = %v, %v; want none", signals, err)
	}

	if _, err := runEnable(t, shared, local, []byte(joinToken), synclog.NewMemTransport("m1")); err != nil {
		t.Fatalf("enable: %v", err)
	}

	state, err := local.DocExportState("config")
	if err != nil || len(state) != 0 {
		t.Fatalf("export state after the join = %v, %v; want it cleared", state, err)
	}
	// The config doc is signalled because the join cleared its export state. The
	// secrets doc is signalled too, and stays signalled: the enable stamped the
	// R2 credentials the caller had already stored, and a joiner is owed them,
	// so swallowing that signal would withhold the credentials from the fleet.
	signals, err := local.DocSignals()
	if err != nil {
		t.Fatalf("signals after the join: %v", err)
	}
	got := map[string]bool{}
	for _, s := range signals {
		got[s.Doc] = true
	}
	if !got["config"] || !got[SecretsDocName] {
		t.Fatalf("signals after the join = %v, %v; want the config doc and the secrets doc signalled",
			signals, err)
	}
}
