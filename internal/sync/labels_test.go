package sync

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// labelsMachine opens a shared database scoped to origin, the machine the
// label tests record peers for.
func labelsMachine(t *testing.T, origin string) *db.DB {
	t.Helper()
	shared, err := db.OpenSplit(filepath.Join(t.TempDir(), "relevo.db"), db.Options{Origin: origin})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	return shared
}

// seedInstallation writes the directory row a case starts from: an older
// stamp, so a write the case must not make shows up as a moved stamp.
func seedInstallation(t *testing.T, shared *db.DB, id, label string, at time.Time) {
	t.Helper()
	if err := shared.Tx(func(tx *db.Tx) error {
		return tx.InstallationTouch(id, label, at)
	}); err != nil {
		t.Fatalf("InstallationTouch %s: %v", id, err)
	}
}

// installationByID reads the directory row id holds, reporting whether one is
// there: the tests pin the row itself, not the ordering of the listing.
func installationByID(t *testing.T, shared *db.DB, id string) (db.Installation, bool) {
	t.Helper()
	listed, err := shared.InstallationList()
	if err != nil {
		t.Fatalf("InstallationList: %v", err)
	}
	for _, in := range listed {
		if in.ID == id {
			return in, true
		}
	}
	return db.Installation{}, false
}

// installationOutbox counts the outbox rows naming the installation directory:
// every directory write leaves one behind through the trigger, so a case that
// must not write has none to clear.
func installationOutbox(t *testing.T, shared *db.DB) int {
	t.Helper()
	drained, err := shared.DrainOutbox(1000)
	if err != nil {
		t.Fatalf("DrainOutbox: %v", err)
	}
	count := 0
	for _, d := range drained {
		if d.Table == "installation" {
			count++
		}
	}
	return count
}

// clearOutbox deletes every outbox row, so a case starts with nothing the
// trigger left behind from its seeding.
func clearOutbox(t *testing.T, shared *db.DB) {
	t.Helper()
	drained, err := shared.DrainOutbox(1000)
	if err != nil {
		t.Fatalf("DrainOutbox: %v", err)
	}
	if len(drained) == 0 {
		return
	}
	if err := shared.DeleteDrainedOutbox(drained[len(drained)-1].Seq); err != nil {
		t.Fatalf("DeleteDrainedOutbox: %v", err)
	}
}

var labelsSeen = time.Date(2026, time.November, 1, 10, 0, 0, 0, time.UTC)

// TestRecordMachineLabelsAddsNewOriginLabel pins the insert: a peer the log
// names appears in the directory with its label, stamped once for both ends.
func TestRecordMachineLabelsAddsNewOriginLabel(t *testing.T) {
	t.Parallel()

	shared := labelsMachine(t, "m1")
	log := synclog.NewMemTransport("m1")
	log.SetLabel("m2", "laptop")

	if err := RecordMachineLabels(shared, log); err != nil {
		t.Fatalf("RecordMachineLabels: %v", err)
	}
	row, ok := installationByID(t, shared, "m2")
	if !ok {
		t.Fatal("no directory row for the peer the log named")
	}
	if row.Label != "laptop" {
		t.Errorf("label = %q, want laptop", row.Label)
	}
	if row.FirstSeen.IsZero() || !row.FirstSeen.Equal(row.LastSeen) {
		t.Errorf("first_seen = %v, last_seen = %v, want one stamp for both", row.FirstSeen, row.LastSeen)
	}
}

// TestRecordMachineLabelsUpdatesChangedLabelKeepsFirstSeen pins the update: a
// changed label moves, last_seen moves with it, and first_seen stays at the
// seed stamp, because the update rewrites the stamp the directory already
// holds; the test pins that the rewrite does not move it.
func TestRecordMachineLabelsUpdatesChangedLabelKeepsFirstSeen(t *testing.T) {
	t.Parallel()

	shared := labelsMachine(t, "m1")
	seedInstallation(t, shared, "m2", "old", labelsSeen)
	log := synclog.NewMemTransport("m1")
	log.SetLabel("m2", "new")

	if err := RecordMachineLabels(shared, log); err != nil {
		t.Fatalf("RecordMachineLabels: %v", err)
	}
	row, ok := installationByID(t, shared, "m2")
	if !ok {
		t.Fatal("the peer row is gone")
	}
	if row.Label != "new" {
		t.Errorf("label = %q, want new", row.Label)
	}
	if !row.FirstSeen.Equal(labelsSeen) {
		t.Errorf("first_seen = %v, want the seed stamp %v", row.FirstSeen, labelsSeen)
	}
	if !row.LastSeen.After(labelsSeen) {
		t.Errorf("last_seen = %v, want a stamp past the seed %v", row.LastSeen, labelsSeen)
	}
}

// TestRecordMachineLabelsWritesNothingForEmptyUnchangedAndSelf pins every
// skip: an empty label neither creates nor overwrites, an unchanged label
// leaves the stamp alone, and this machine's own label is never a directory
// row. No write means no transaction, so the outbox the triggers feed stays
// empty too.
func TestRecordMachineLabelsWritesNothingForEmptyUnchangedAndSelf(t *testing.T) {
	t.Parallel()

	shared := labelsMachine(t, "m1")
	seedInstallation(t, shared, "m2", "peer", labelsSeen)
	seedInstallation(t, shared, "m4", "same", labelsSeen)
	clearOutbox(t, shared)
	log := synclog.NewMemTransport("m1")
	log.SetLabel("m1", "self")
	log.SetLabel("m2", "")
	log.SetLabel("m3", "")
	log.SetLabel("m4", "same")

	if err := RecordMachineLabels(shared, log); err != nil {
		t.Fatalf("RecordMachineLabels: %v", err)
	}
	if row, ok := installationByID(t, shared, "m2"); !ok {
		t.Error("an empty label removed the good row")
	} else if row.Label != "peer" || !row.LastSeen.Equal(labelsSeen) {
		t.Errorf("peer row = %+v, want the seed row untouched", row)
	}
	if _, ok := installationByID(t, shared, "m3"); ok {
		t.Error("an empty label created a row")
	}
	if row, ok := installationByID(t, shared, "m4"); !ok {
		t.Error("the unchanged row is gone")
	} else if !row.LastSeen.Equal(labelsSeen) {
		t.Errorf("unchanged last_seen = %v, want the seed stamp", row.LastSeen)
	}
	if _, ok := installationByID(t, shared, "m1"); ok {
		t.Error("this machine's own label became a directory row")
	}
	if n := installationOutbox(t, shared); n != 0 {
		t.Errorf("installation outbox rows = %d, want none written", n)
	}
}

// TestRecordMachineLabelsExportsNothingForPeerRows pins the choice of writer:
// after the call an exporter pass appends no entry for the peer row, and the
// outbox holds nothing for it, so the label never travels back as this
// machine's.
func TestRecordMachineLabelsExportsNothingForPeerRows(t *testing.T) {
	t.Parallel()

	shared := labelsMachine(t, "m1")
	log := synclog.NewMemTransport("m1")
	log.SetLabel("m2", "laptop")
	if err := RecordMachineLabels(shared, log); err != nil {
		t.Fatalf("RecordMachineLabels: %v", err)
	}
	res, err := synclog.NewExporter(shared, log).Export()
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if res.Appended != 0 {
		t.Errorf("appended = %d, want nothing exported", res.Appended)
	}
	if _, ok := installationByID(t, shared, "m2"); !ok {
		t.Error("the export cleared the directory row itself")
	}
	if n := installationOutbox(t, shared); n != 0 {
		t.Errorf("installation outbox rows = %d, want the pass to hold none", n)
	}
}

// failingMarks is a transport whose marks refuse: only ReadMarks is replaced,
// the rest stays the in-memory fake's.
type failingMarks struct {
	*synclog.MemTransport
	err error
}

// ReadMarks refuses instead of answering with the log's shape.
func (f failingMarks) ReadMarks() (synclog.MarkState, error) {
	return synclog.MarkState{}, f.err
}

// TestRecordMachineLabelsWrapsReadMarksError pins that a transport that will
// not answer its marks fails the call with that error, not silence.
func TestRecordMachineLabelsWrapsReadMarksError(t *testing.T) {
	t.Parallel()

	shared := labelsMachine(t, "m1")
	boom := errors.New("marks refused")
	if err := RecordMachineLabels(shared, failingMarks{MemTransport: synclog.NewMemTransport("m1"), err: boom}); !errors.Is(err, boom) {
		t.Fatalf("RecordMachineLabels = %v, want the marks error", err)
	}
}

// TestRecordMachineLabelsFailsWithoutDirectory pins that a directory that
// will not read fails the call rather than writing blind.
func TestRecordMachineLabelsFailsWithoutDirectory(t *testing.T) {
	t.Parallel()

	shared := labelsMachine(t, "m1")
	log := synclog.NewMemTransport("m1")
	log.SetLabel("m2", "laptop")
	if err := shared.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	err := RecordMachineLabels(shared, log)
	if err == nil {
		t.Fatal("RecordMachineLabels on a closed database returned no error")
	}
	if !strings.Contains(err.Error(), "record machine labels") {
		t.Errorf("error = %v, want the call named", err)
	}
}
