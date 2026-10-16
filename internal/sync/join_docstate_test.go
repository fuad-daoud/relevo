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

// TestJoinKeepsOnlyThePeersSecretsBookkeeping pins what a fresh join keeps of
// the secrets document's export state: a row for a value taken from a peer
// stays, so the joiner does not send the fleet's own value back, and a row for
// a value this machine sent to a log it wrote before goes, with the document
// signalled, so the log it is joining receives that value.
func TestJoinKeepsOnlyThePeersSecretsBookkeeping(t *testing.T) {
	shared, local := joinPair(t, "m1")
	passGate(t, shared)
	self := shared.Origin()
	if err := local.RecordDocExport(db.DocSignal{Doc: SecretsDocName}, []db.DocExportWrite{
		{Key: SecretTypesafe, Origin: self, Stamp: "5", Digest: "own"},
		{Key: SecretR2Bucket, Origin: "peer", Stamp: "6", Digest: "theirs"},
	}); err != nil {
		t.Fatalf("seed the export state: %v", err)
	}
	if err := local.Tx(func(tx *db.Tx) error { return tx.ClearDocSignals(SecretsDocName) }); err != nil {
		t.Fatalf("clear the seeded signal: %v", err)
	}

	if _, err := runEnable(t, shared, local, []byte(joinToken), synclog.NewMemTransport("m1")); err != nil {
		t.Fatalf("enable: %v", err)
	}

	state, err := local.DocExportState(SecretsDocName)
	if err != nil {
		t.Fatalf("export state after the join: %v", err)
	}
	if _, kept := state[SecretTypesafe]; kept {
		t.Errorf("the row for a value this machine sent to another log survived the join: %+v", state)
	}
	if row, kept := state[SecretR2Bucket]; !kept || row.Origin != "peer" || row.Digest != "theirs" {
		t.Errorf("the row for a value taken from a peer = %+v, %v; want it kept as it was", row, kept)
	}
	signals, err := local.DocSignals()
	if err != nil {
		t.Fatalf("signals after the join: %v", err)
	}
	signalled := false
	for _, s := range signals {
		signalled = signalled || s.Doc == SecretsDocName
	}
	if !signalled {
		t.Errorf("signals after the join = %v; want the secrets document signalled", signals)
	}
}
