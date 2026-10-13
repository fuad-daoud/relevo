package relevo

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/config"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

func cfgCandidateNamed(name, model string) string {
	return fmt.Sprintf(`{"name":%q,"harness":"claude","provider":"anthropic","model":%q,"roles":["builder"]}`, name, model)
}

// setAt is set with the store's clock held at at.
func (m *cfgMachine) setAt(t *testing.T, at time.Time, sec config.Section, body string) {
	t.Helper()
	store := m.cfg.WithClock(func() time.Time { return at }).As("config set", "")
	if _, err := store.Put(sec, []byte(body)); err != nil {
		t.Fatalf("config set %s: %v", sec, err)
	}
}

func (m *cfgMachine) export(t *testing.T) {
	t.Helper()
	if _, err := exportOnce(m.mdb, m.log.OnLog(m.mdb.Origin())); err != nil {
		t.Fatalf("export: %v", err)
	}
}

func (m *cfgMachine) pull(t *testing.T) {
	t.Helper()
	if _, err := importOnce(m.mdb, m.log.OnLog(m.mdb.Origin())); err != nil {
		t.Fatalf("import: %v", err)
	}
}

func (m *cfgMachine) writeStamp(t *testing.T, key string) (synclog.Stamp, bool) {
	t.Helper()
	text, ok, err := m.mdb.ConfigWriteStamp(key)
	if err != nil {
		t.Fatalf("ConfigWriteStamp(%s): %v", key, err)
	}
	if !ok {
		return 0, false
	}
	stamp, err := synclog.ParseStamp(text)
	if err != nil {
		t.Fatalf("ParseStamp(%q): %v", text, err)
	}
	return stamp, true
}

// syncedPair is two machines that both hold the candidate "m" as written by A
// at base, with A's first export already on the log.
func syncedPair(t *testing.T, base time.Time) (a, b *cfgMachine) {
	t.Helper()
	log := synclog.NewMemTransport("x")
	a = newCfgMachine(t, log, cfgSyncKey, true)
	b = newCfgMachine(t, log, cfgSyncKey, true)
	a.setAt(t, base, config.Candidates, "["+cfgCandidateNamed("m", "first")+"]")
	a.export(t)
	b.pull(t)
	if got := b.body(t, config.Candidates); !strings.Contains(got, "first") {
		t.Fatalf("B's candidates = %s, want A's first edit", got)
	}
	return a, b
}

func assertConverged(t *testing.T, a, b *cfgMachine, want string) {
	t.Helper()
	for name, m := range map[string]*cfgMachine{"A": a, "B": b} {
		if got := m.body(t, config.Candidates); !strings.Contains(got, want) {
			t.Errorf("%s holds %s, want the %s edit", name, got, want)
		}
	}
}

func TestLateExportDoesNotOutrankANewerEdit(t *testing.T) {
	t.Parallel()
	base := time.Now().Add(-time.Hour).UTC()
	a, b := syncedPair(t, base)

	a.setAt(t, base.Add(time.Minute), config.Candidates, "["+cfgCandidateNamed("m", "older-from-a")+"]")
	b.setAt(t, base.Add(2*time.Minute), config.Candidates, "["+cfgCandidateNamed("m", "newer-from-b")+"]")
	b.export(t)
	a.export(t)
	a.pull(t)
	b.pull(t)

	assertConverged(t, a, b, "newer-from-b")
	if want := "sync:" + b.label(t); !a.hasSource(t, want) {
		t.Errorf("A's revision sources %v lack %q", a.sources(t), want)
	}
	if want := "sync-superseded:" + a.label(t); !b.hasSource(t, want) {
		t.Errorf("B's revision sources %v lack %q", b.sources(t), want)
	}
}

func TestLockedMachineEditLosesToANewerEditAfterUnlock(t *testing.T) {
	t.Parallel()
	base := time.Now().Add(-time.Hour).UTC()
	a, b := syncedPair(t, base)

	if err := relevosync.DeleteKey(a.local); err != nil {
		t.Fatalf("lock A: %v", err)
	}
	a.setAt(t, base.Add(time.Minute), config.Candidates, "["+cfgCandidateNamed("m", "older-from-a")+"]")
	b.setAt(t, base.Add(2*time.Minute), config.Candidates, "["+cfgCandidateNamed("m", "newer-from-b")+"]")
	b.export(t)
	if err := relevosync.SetKey(a.local, cfgSyncKey, time.Now().UTC()); err != nil {
		t.Fatalf("unlock A: %v", err)
	}
	a.export(t)
	a.pull(t)
	b.pull(t)

	assertConverged(t, a, b, "newer-from-b")
}

func TestWriteStampsOnlyTheChangedItems(t *testing.T) {
	t.Parallel()
	base := time.Now().Add(-time.Hour).UTC()
	m := newCfgMachine(t, synclog.NewMemTransport("x"), cfgSyncKey, true)

	m.setAt(t, base, config.Candidates, "["+cfgCandidateNamed("one", "m1")+","+cfgCandidateNamed("two", "m2")+"]")
	oneBefore, ok := m.writeStamp(t, "candidates/one")
	if !ok {
		t.Fatal("candidates/one has no write stamp after being written")
	}
	twoBefore, _ := m.writeStamp(t, "candidates/two")

	m.setAt(t, base.Add(time.Minute), config.Candidates, "["+cfgCandidateNamed("one", "m1")+","+cfgCandidateNamed("two", "m3")+"]")

	if one, _ := m.writeStamp(t, "candidates/one"); one != oneBefore {
		t.Errorf("unchanged item restamped: %d, was %d", one, oneBefore)
	}
	if two, _ := m.writeStamp(t, "candidates/two"); two <= twoBefore {
		t.Errorf("changed item stamp %d does not exceed its earlier %d", two, twoBefore)
	}
}

func TestAppliedItemRecordsNoWriteStamp(t *testing.T) {
	t.Parallel()
	a, b := syncedPair(t, time.Now().Add(-time.Hour).UTC())

	if _, ok := a.writeStamp(t, "candidates/m"); !ok {
		t.Fatal("the writing machine recorded no write stamp")
	}
	if stamp, ok := b.writeStamp(t, "candidates/m"); ok {
		t.Errorf("the applying machine recorded write stamp %d for an item that arrived", stamp)
	}
}

func TestUnlockKeepsANewerLocalEditOverAnOlderParkedEntry(t *testing.T) {
	t.Parallel()
	base := time.Now().Add(-time.Hour).UTC()
	a, b := syncedPair(t, base)

	if err := relevosync.DeleteKey(a.local); err != nil {
		t.Fatalf("lock A: %v", err)
	}
	b.setAt(t, base.Add(time.Minute), config.Candidates, "["+cfgCandidateNamed("m", "older-from-b")+"]")
	b.export(t)
	a.pull(t)
	if parked, err := a.mdb.PendingDocs(); err != nil || len(parked) == 0 {
		t.Fatalf("pending = %v, %v; want B's entry parked", parked, err)
	}
	a.setAt(t, base.Add(2*time.Minute), config.Candidates, "["+cfgCandidateNamed("m", "newer-from-a")+"]")
	if err := relevosync.SetKey(a.local, cfgSyncKey, time.Now().UTC()); err != nil {
		t.Fatalf("unlock A: %v", err)
	}
	if _, err := relevosync.DrainParkedDocs(a.mdb, a.local, a.log.OnLog(a.mdb.Origin())); err != nil {
		t.Fatalf("DrainParkedDocs: %v", err)
	}

	if got := a.body(t, config.Candidates); !strings.Contains(got, "newer-from-a") {
		t.Errorf("A holds %s after the unlock, want its own newer edit", got)
	}
	if want := "sync-superseded:" + b.label(t); !a.hasSource(t, want) {
		t.Errorf("A's revision sources %v lack %q", a.sources(t), want)
	}
	a.export(t)
	b.pull(t)
	assertConverged(t, a, b, "newer-from-a")
}

func TestPullKeepsANewerUnexportedLocalEdit(t *testing.T) {
	t.Parallel()
	base := time.Now().Add(-time.Hour).UTC()
	a, b := syncedPair(t, base)

	b.setAt(t, base.Add(time.Minute), config.Candidates, "["+cfgCandidateNamed("m", "older-from-b")+"]")
	b.export(t)
	a.setAt(t, base.Add(2*time.Minute), config.Candidates, "["+cfgCandidateNamed("m", "newer-from-a")+"]")
	a.pull(t)

	if got := a.body(t, config.Candidates); !strings.Contains(got, "newer-from-a") {
		t.Errorf("A holds %s after the pull, want its own newer edit", got)
	}
	a.export(t)
	b.pull(t)
	assertConverged(t, a, b, "newer-from-a")
}

func TestNewerArrivingEntryStillBeatsAnOlderLocalEdit(t *testing.T) {
	t.Parallel()
	base := time.Now().Add(-time.Hour).UTC()
	a, b := syncedPair(t, base)

	a.setAt(t, base.Add(time.Minute), config.Candidates, "["+cfgCandidateNamed("m", "older-from-a")+"]")
	b.setAt(t, base.Add(2*time.Minute), config.Candidates, "["+cfgCandidateNamed("m", "newer-from-b")+"]")
	b.export(t)
	a.pull(t)

	if got := a.body(t, config.Candidates); !strings.Contains(got, "newer-from-b") {
		t.Errorf("A holds %s, want the arriving newer entry", got)
	}
}
