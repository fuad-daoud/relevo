package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// Gates, latency, the claims, the wait claims, the run log and the registry
// are all this machine's own records: a pid, a listen address, a row about a
// round that ran here. After the split they live in the machine-local file, so
// every surface the runtime wires has to bind that file, or it reads back empty
// on exactly the machines that have the records.

// localPairRuntime builds a runtime over a split pair in a fresh root, the shape
// the daemon and a verb both get, and returns it with the pair's shared handle
// so a test can read both files.
func localPairRuntime(t *testing.T) (relevo.Runtime, *db.DB) {
	t.Helper()
	root := t.TempDir()
	d, err := db.OpenSplit(filepath.Join(root, "relevo.db"), db.Options{Origin: "01ORIGIN"})
	if err != nil {
		t.Fatalf("OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	rt, err := newRuntimeOn(root, d)
	if err != nil {
		t.Fatalf("newRuntimeOn: %v", err)
	}
	return rt, d
}

// assertNotOnShared reports a row that a local-bound surface wrote where the
// shared file can still see it.
func assertNotOnShared(t *testing.T, d *db.DB, key string) {
	t.Helper()
	if _, ok, err := d.KVGet(key); err != nil {
		t.Fatalf("read %s from the shared file: %v", key, err)
	} else if ok {
		t.Errorf("%s is readable on the shared file, so the surface wrote there", key)
	}
}

// TestTheRuntimeGatesReadTheMachineLocalFile pins the ledger and the latency
// history the runtime wires: both are the local file's, and neither is reachable
// through the shared handle behind the same runtime.
func TestTheRuntimeGatesReadTheMachineLocalFile(t *testing.T) {
	rt, d := localPairRuntime(t)
	if rt.Gates == nil || rt.Latency == nil {
		t.Fatal("the runtime wired no gates or no latency store")
	}

	if err := rt.Gates.KVPut("availability.ledger", []byte(`{"entries":[]}`)); err != nil {
		t.Fatalf("write the ledger through Gates: %v", err)
	}
	assertNotOnShared(t, d, "availability.ledger")
	if _, err := availability.LoadLedger(rt.Gates); err != nil {
		t.Fatalf("LoadLedger through Gates: %v", err)
	}

	if err := rt.Latency.KVPut("latency", []byte(`{"samples":[]}`)); err != nil {
		t.Fatalf("write the latency history through Latency: %v", err)
	}
	assertNotOnShared(t, d, "latency")
	if _, err := availability.LoadLatency(rt.Latency); err != nil {
		t.Fatalf("LoadLatency through Latency: %v", err)
	}
}

// TestTheRuntimeClaimsReadTheMachineLocalFile pins the delivery claims: a claim
// is a pid on this machine, so claims on the shared file would be visible to
// every machine and invisible to the one that has to honour them.
func TestTheRuntimeClaimsReadTheMachineLocalFile(t *testing.T) {
	rt, d := localPairRuntime(t)
	if rt.Channels == nil {
		t.Fatal("the runtime wired no channel claim store")
	}
	const id = "mm_abcdefgh2345"

	now := time.Now().UTC()
	claim := delivery.Claim{MasterMind: id, PID: os.Getpid(), StartedAt: now, SeenAt: now}
	if err := rt.Channels.Write(claim, now); err != nil {
		t.Fatalf("Write: %v", err)
	}
	assertNotOnShared(t, d, "claim/"+id)
	back, err := rt.Channels.Live(id, now)
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if back == nil || back.PID != claim.PID {
		t.Errorf("Live = %+v, want the claim this machine wrote", back)
	}
	if err := rt.Channels.Remove(id, claim.PID); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if rt.Waits == nil {
		t.Fatal("the runtime wired no wait claim store")
	}
	if err := rt.Waits.Write(delivery.WaitClaim{Name: "waiting-name", PID: 4242, SeenAt: now}, now); err != nil {
		t.Fatalf("wait Write: %v", err)
	}
	assertNotOnShared(t, d, "wait/waiting-name")
}

// TestTheRuntimeMastermindRegistryReadsTheMachineLocalFile pins the registry: a
// record names a pid, a cwd and a host, none of which mean anything on another
// machine.
func TestTheRuntimeMastermindRegistryReadsTheMachineLocalFile(t *testing.T) {
	rt, d := localPairRuntime(t)
	if rt.MasterMinds == nil {
		t.Fatal("the runtime wired no mastermind registry")
	}
	const id = "mm_abcdefgh2345"
	rec := mastermind.Record{ID: id, Name: "build", HarnessKind: "claude", SessionID: "ses_abc", CWD: "/work/build"}
	if _, err := rt.MasterMinds.Create(rec); err != nil {
		t.Fatalf("Create: %v", err)
	}
	assertNotOnShared(t, d, "mastermind/"+id)

	back, err := rt.MasterMinds.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if back.Name != "build" || back.CWD != "/work/build" {
		t.Errorf("Get = %+v, want the record the registry wrote", back)
	}
}

// TestTheHooksRunLogReadsTheMachineLocalFile pins the run log on its own: it is
// the row every hook run and webhook failure is recorded in, and a log that
// cannot be read back is a record of nothing.
func TestTheHooksRunLogReadsTheMachineLocalFile(t *testing.T) {
	rt, d := localPairRuntime(t)

	log := hooksRunLog(d)
	if log == nil {
		t.Fatal("hooksRunLog over an open handle is nil")
	}
	run := hooks.HookRun{At: time.Now(), Event: "state_changed", ExitCode: 0}
	if err := log.Append(run); err != nil {
		t.Fatalf("Append: %v", err)
	}
	assertNotOnShared(t, d, "hooks.log")

	// The RunLog surface is Append only, so the read is the same row through
	// the store's kv handle: a log that is appended to and never readable is a
	// record of nothing.
	runs, ok, err := rt.Gates.KVGet("hooks.log")
	if err != nil || !ok {
		t.Fatalf("read the run log row through Gates: %v (present %t)", err, ok)
	}
	if !strings.Contains(string(runs), "state_changed") {
		t.Errorf("the run log row = %s, want the appended run", runs)
	}
}

// TestTheReadOnlyConfigLoadReadsTheMachineLocalFile pins the peek path, which
// opens the file itself rather than dialling the owner: a read-only open has to
// carry the local file too, or a peek on a machine whose daemon happens to be
// down reads an empty config and reports a machine that is configured fine.
func TestTheReadOnlyConfigLoadReadsTheMachineLocalFile(t *testing.T) {
	root := t.TempDir()
	d, err := db.OpenSplit(filepath.Join(root, "relevo.db"), db.Options{Origin: "01ORIGIN"})
	if err != nil {
		t.Fatalf("OpenSplit: %v", err)
	}
	now := time.Now().UTC()
	if err := d.Local().Tx(func(tx *db.Tx) error {
		return tx.ConfigPut("candidates",
			[]byte(`[{"harness":"claude","provider":"p","model":"m","roles":["builder"]}]`), now)
	}); err != nil {
		t.Fatalf("seed candidates into the local file: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close the pair: %v", err)
	}

	L, err := loadConfigFromFile(filepath.Join(root, "relevo.db"))
	if err != nil {
		t.Fatalf("loadConfigFromFile: %v", err)
	}
	if L.Candidates.Len() != 1 {
		t.Errorf("candidates = %d, want the one row in the local file", L.Candidates.Len())
	}
}

// TestAReadOnlyOpenOfAnUnsplitDatabaseStillAnswers pins the fallback in the
// split read-only open: a machine whose split never ran has no local file, and
// every row it has is in the shared one, so the open succeeds and reads it.
func TestAReadOnlyOpenOfAnUnsplitDatabaseStillAnswers(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "relevo.db")
	writable, err := db.OpenSplit(path, db.Options{Origin: "01ORIGIN"})
	if err != nil {
		t.Fatalf("OpenSplit: %v", err)
	}
	now := time.Now().UTC()
	if err := writable.Tx(func(tx *db.Tx) error {
		return tx.ConfigPut("candidates",
			[]byte(`[{"harness":"claude","provider":"p","model":"m","roles":["builder"]}]`), now)
	}); err != nil {
		t.Fatalf("seed the shared file: %v", err)
	}
	if err := writable.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := os.Remove(db.SplitPath(path)); err != nil {
		t.Fatalf("remove the local file: %v", err)
	}

	L, err := loadConfigFromFile(path)
	if err != nil {
		t.Fatalf("loadConfigFromFile over a database with no local file: %v", err)
	}
	if L.Candidates.Len() != 1 {
		t.Errorf("candidates = %d, want the one row in the shared file", L.Candidates.Len())
	}
}

// TestTheStoreHandleCarriesTheMachineLocalFile pins the handle a store opens for
// itself. Without the local file attached, every surface bound through it falls
// back to the shared file, and none of the tests above would see it.
func TestTheStoreHandleCarriesTheMachineLocalFile(t *testing.T) {
	st := store.New(t.TempDir())
	d, err := st.DB()
	if err != nil {
		t.Fatalf("st.DB: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if d.Local() == nil {
		t.Fatal("a store-opened machine database carries no local file, so every surface bound through it reads the shared file")
	}
}
