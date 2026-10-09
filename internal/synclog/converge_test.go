package synclog

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// The two proofs the exchange has to give: two machines that joined after both
// had history end up holding the same rows, and a machine whose file is restored
// from a backup converges rather than drifting or double-numbering the log.
//
// Both run the whole round -- exporter, importer and reconcile -- across two
// real files and the same fake, so what converges is what the three parts
// actually do together and not a hand-fed sequence of entries.

// roundResult is one machine's round: what it exported, imported and reconciled.
type roundResult struct {
	exported   ExportResult
	imported   ImportResult
	reconciled ReconcileResult
}

// moved reports whether the round changed anything on that machine. A round that
// moved nothing is the fixed point the convergence cases check for.
func (r roundResult) moved() bool {
	return r.exported.Appended != 0 || r.imported.Applied != 0 || r.reconciled.Batches != 0
}

// runRound is one machine's full pass over the exchange, in the order the parts
// depend on each other: the outbox first, so the log learns what this machine
// wrote; then the other origins' entries, so the file learns what they wrote;
// then reconcile, which compares the file against head and proposes whatever
// neither of the first two covered.
func runRound(t *testing.T, log *MemTransport, d *db.DB) roundResult {
	t.Helper()
	// One handle per machine on the one log: a machine appends as its own origin
	// and reads everyone else's, which is what the two OnLog handles are for. A
	// single shared handle would refuse every append but the first machine's.
	handle := log.OnLog(d.Origin())
	var got roundResult
	exported, err := NewExporter(d, handle).Export()
	if err != nil {
		t.Fatalf("%s: export: %v", d.Origin(), err)
	}
	got.exported = exported

	imported, err := NewImporter(d, handle).Import()
	if err != nil {
		t.Fatalf("%s: import: %v", d.Origin(), err)
	}
	got.imported = imported

	reconciled, err := NewReconciler(d, handle).Reconcile()
	if err != nil {
		t.Fatalf("%s: reconcile: %v", d.Origin(), err)
	}
	got.reconciled = reconciled
	return got
}

// syncRound runs a round on both machines of a pair, the order a worker would:
// one machine's pass in full, then the other's.
func syncRound(t *testing.T, log *MemTransport, first, second *db.DB) (roundResult, roundResult) {
	t.Helper()
	return runRound(t, log, first), runRound(t, log, second)
}

// assertReferencesClean fails when a file holds a row whose references the file
// does not satisfy. Two files holding the same rows with a missing parent would
// still compare equal by row list, so the row lists alone cannot say the pair
// converged while its references are broken.
func assertReferencesClean(t *testing.T, d *db.DB) {
	t.Helper()
	raw, err := db.OpenRawReadOnly(d.Path())
	if err != nil {
		t.Fatalf("open %s for reading: %v", d.Path(), err)
	}
	defer func() { _ = raw.Close() }()

	rows, err := raw.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("check %s's references: %v", d.Path(), err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		t.Fatalf("%s holds a row whose references are broken", d.Path())
	}
}

// maxJoinRounds is how many rounds a convergence case will run before it calls a
// pair that has not settled a failure. A round on each machine is enough to cross
// history in both directions, and reconcile settles whatever a round's export and
// import did not, so a pair that needs more than a couple of rounds is not
// converging at all.
const maxJoinRounds = 4

// settle runs rounds on the pair until neither machine moved in one, and reports
// which machines applied entries of another origin at some point. It is what
// "joined" means for a pair that started with disjoint history: each ended up
// holding rows it never wrote.
func settle(t *testing.T, log *MemTransport, first, second *db.DB) (map[string]bool, int) {
	t.Helper()
	crossed := map[string]bool{first.Origin(): false, second.Origin(): false}
	for round := 1; round <= maxJoinRounds; round++ {
		a, b := syncRound(t, log, first, second)
		if a.imported.Applied > 0 {
			crossed[first.Origin()] = true
		}
		if b.imported.Applied > 0 {
			crossed[second.Origin()] = true
		}
		if !a.moved() && !b.moved() {
			return crossed, round
		}
	}
	return crossed, maxJoinRounds
}

// assertConverged fails unless the two files hold the same rows, those rows are
// the ones named, and both files satisfy their own references.
func assertConverged(t *testing.T, mine, theirs *db.DB, want string) {
	t.Helper()
	if have := sharedRows(t, mine); have != want {
		t.Fatalf("this machine holds %q, want %q", have, want)
	}
	if have := sharedRows(t, theirs); have != want {
		t.Fatalf("the other machine holds %q, want %q", have, want)
	}
	assertReferencesClean(t, mine)
	assertReferencesClean(t, theirs)
}

// Both machines hold history the other never saw, and neither has a mark for the
// other: the log starts empty and each machine's rows predate the join.
//
// The order the rounds run in is the one that has to work. Exporting first gives
// each machine's own rows to the log, so the other's import carries the pre-join
// history across. Reconcile then compares each file against head, which is what
// covers the rows a file that upgraded under migration 022 cannot account for:
// its triggers saw no write, so its outbox never recorded its rows, and head is
// the only place that knows whether the log has them.
func TestJoinWithHistoryOnBothSides(t *testing.T) {
	t.Parallel()
	theirs, theirPath := exporterFile(t, "m1")
	mine, _ := peerFile(t, "m2")

	// Disjoint pre-join history: m1's binding with a round under it, and m2's
	// record with two children that cascade from it. Each side's rows can only
	// arrive through the exchange, since neither machine wrote the other's.
	seed(t, theirPath,
		insertBinding("01A", "m1"),
		insertRound("02A", "01A"),
	)
	seed(t, mine.Path(),
		insertRecord("05A", "m2"),
		insertRecordEvent("05A", 1),
		insertRoundFile("05A", "f", 1),
	)
	if got := sharedRows(t, mine); got != `binding_record ["05A"]|binding_event ["05A",1]|round_file ["05A","f"]` {
		t.Fatalf("this machine starts with %q, want its own record and two children", got)
	}

	log := NewMemTransport("m1")
	// The two machines do not see each other's history in one pass: the first
	// machine's export is what the second imports, so the rows cross in opposite
	// directions on different passes. Running to a fixed point is the join, and
	// the bound is what keeps a non-converging pair from looping forever.
	crossed, rounds := settle(t, log, mine, theirs)
	if !crossed[mine.Origin()] {
		t.Fatal("this machine never applied the other machine's pre-join history")
	}
	if !crossed[theirs.Origin()] {
		t.Fatal("the other machine never applied this machine's pre-join history")
	}

	want := `binding_record ["05A"]|binding ["01A"]|binding_event ["05A",1]|round_file ["05A","f"]|round ["02A"]`
	assertConverged(t, mine, theirs, want)

	// The pair settled within the bound, and the round after it moved nothing:
	// converged means the two files agree with each other and with the log, so
	// there is no change left for another round to propose.
	if rounds == maxJoinRounds {
		t.Fatalf("the pair needed the whole bound of %d rounds, want it to settle", maxJoinRounds)
	}

	// Neither machine proposed the other's rows. An entry for a row another
	// installation owns would travel back to the machine that wrote it and be
	// applied here a second time, which is the echo the exporter already refuses.
	mineHead, err := log.Head("m2")
	if err != nil {
		t.Fatalf("read this machine's head: %v", err)
	}
	for _, row := range mineHead {
		if row.Table == "binding" || row.Table == "round" {
			t.Fatalf("this machine's head names %s %s, which the other machine owns", row.Table, row.PK)
		}
	}
}

// A file restored from a backup comes back with the rows and the import marks it
// held at that moment, and with an outbox that has already given those rows up.
// The rows the machine wrote since are still in the log, so head knows them and
// the file does not: reconcile proposes them again, and the transport numbers
// them from its own high-water mark rather than from the outbox, so a restored
// file cannot hand out a number the log already holds.
//
// What the case pins is the convergence, not that the restored file remembers
// what it lost. The rows come back or go away because the log still knew them:
// the one the backup lost is proposed as a delete, which is the only reading
// reconcile can act on, and the next append is numbered above everything the log
// already holds rather than from the rewound outbox.
func TestRestoreFromBackupConverges(t *testing.T) {
	t.Parallel()
	mine, _ := peerFile(t, "m1")
	theirs, _ := peerFile(t, "m2")
	seed(t, mine.Path(), insertBinding("01A", "m1"), insertRound("02A", "01A"))
	seed(t, theirs.Path(), insertBinding("04B", "m2"))

	log := NewMemTransport("m1")
	crossed, _ := settle(t, log, mine, theirs)
	if !crossed[mine.Origin()] || !crossed[theirs.Origin()] {
		t.Fatalf("the pair joined as %v, want each machine to have applied the other's rows", crossed)
	}
	assertConverged(t, mine, theirs, `binding ["01A"]|binding ["04B"]|round ["02A"]`)

	// The backup is a copy of the file with its handle closed, which is the only
	// copy worth taking: an open handle's write-ahead log holds committed rows
	// the main file does not, so a copy taken beside it is a file that never
	// existed.
	snapshot, mine := backupOf(t, mine)

	// The machine moves on: a binding it owns is written and exported, so the log
	// holds a row the backup does not, and the other machine applies it.
	seed(t, mine.Path(), insertBinding("03A", "m1"))
	if _, err := NewExporter(mine, log).Export(); err != nil {
		t.Fatalf("export the row written after the backup: %v", err)
	}
	if _, err := NewImporter(theirs, log.OnLog("m2")).Import(); err != nil {
		t.Fatalf("import the row written after the backup: %v", err)
	}

	// Restoring rewinds the file: the row written since is gone from it, and the
	// mark for m2 comes back with the file. The handle is reopened afterwards
	// because the restored bytes are a file nothing has open yet.
	restored := restoreOver(t, mine, snapshot)

	// Re-importing first is what a restored file does, and it is idempotent: the
	// mark came back with the file, so there is nothing of m2's left to apply.
	imported, err := NewImporter(restored, log.OnLog("m1")).Import()
	if err != nil {
		t.Fatalf("import after the restore: %v", err)
	}
	if imported.Applied != 0 {
		t.Fatalf("the restored file applied %d entries, want its mark to have come back with it", imported.Applied)
	}

	// What the file lost, reconcile proposes as a delete: the log holds a row for
	// this origin that the restored file does not, and the only honest reading of
	// that is that this machine does not hold it. The proposal carries the
	// binding's key and no body, because there is no row left to encode one from.
	reconciled, err := NewReconciler(restored, log.OnLog("m1")).Reconcile()
	if err != nil {
		t.Fatalf("reconcile after the restore: %v", err)
	}
	if reconciled.Deletes != 1 || reconciled.Upserts != 0 {
		t.Fatalf("reconcile = %+v, want the one row the backup lost proposed as a delete", reconciled)
	}

	// The other machine applies it, and the pair is back to the backup's state: a
	// restored file that converges takes the log's later rows with it, rather than
	// leaving one machine ahead of the other for good.
	if _, err := NewImporter(theirs, log.OnLog("m2")).Import(); err != nil {
		t.Fatalf("import the restored machine's proposal: %v", err)
	}
	assertConverged(t, restored, theirs, `binding ["01A"]|binding ["04B"]|round ["02A"]`)

	// A further round on both moves nothing: the restore converged rather than
	// needing repair beyond the one round the difference called for.
	settled, _ := syncRound(t, log, restored, theirs)
	if settled.moved() {
		t.Fatalf("a further round on the restored machine moved %+v, want nothing", settled)
	}
}

// A restored machine numbers its next append from the log's high-water mark, not
// from the outbox the restore rewound. That outbox is back at the sequence it held
// when the backup was taken, so a machine that numbered from it would hand the log
// a number that origin already holds and two entries would claim one position --
// which an importer cannot apply, since it reads whole batches by sequence.
//
// The case is separate from the convergence one because it is a claim about the
// log's numbering rather than about the files' contents: the pair converges
// either way, and only the numbers say whether a restored file wrote safely.
func TestRestoredFileNumbersItsAppendsAboveTheLog(t *testing.T) {
	t.Parallel()
	mine, _ := peerFile(t, "m1")
	theirs, _ := peerFile(t, "m2")
	seed(t, mine.Path(), insertBinding("01A", "m1"))
	seed(t, theirs.Path(), insertBinding("04B", "m2"))

	log := NewMemTransport("m1")
	settle(t, log, mine, theirs)

	// The backup is taken once the pair has joined, so the restored file's outbox
	// has already given its rows up and its only sequence left is behind the log's
	// high-water mark.
	snapshot, mine := backupOf(t, mine)
	before, err := log.Stats()
	if err != nil {
		t.Fatalf("read the log's stats before the restore: %v", err)
	}
	if before.Seq == 0 {
		t.Fatal("the log holds no entries, want the join to have written some")
	}
	restored := restoreOver(t, mine, snapshot)

	seed(t, restored.Path(), insertBinding("06A", "m1"))
	if _, err := NewExporter(restored, log.OnLog("m1")).Export(); err != nil {
		t.Fatalf("export from the restored file: %v", err)
	}

	// No number is claimed twice, and the restored file's new entry sits above
	// everything the log held before it. Both are the same claim seen two ways:
	// one says no collision, the other says where the numbering came from.
	head, err := log.Head("m1")
	if err != nil {
		t.Fatalf("read head after the restored machine wrote: %v", err)
	}
	if !distinct(headSeqs(head)) {
		t.Fatalf("head sequence numbers are %v, want no number claimed twice after a restore", headSeqs(head))
	}
	fresh := bindingHead(t, log, "m1", `["06A"]`)
	if fresh <= before.Seq {
		t.Fatalf("the restored file's entry numbered %d, want it above the log's high-water mark %d",
			fresh, before.Seq)
	}

	// The pair converges on the row the restored machine just wrote, which is the
	// ordinary path: the numbering was safe, so the entry is applicable.
	if _, err := NewImporter(theirs, log.OnLog("m2")).Import(); err != nil {
		t.Fatalf("import the restored machine's new row: %v", err)
	}
	assertConverged(t, restored, theirs, `binding ["01A"]|binding ["04B"]|binding ["06A"]`)
}

// bindingHead is the sequence number head holds for one binding, so a case can
// say where in the origin's log a row's latest entry sits.
func bindingHead(t *testing.T, log *MemTransport, origin, pk string) int {
	t.Helper()
	head, err := log.Head(origin)
	if err != nil {
		t.Fatalf("read %s's head: %v", origin, err)
	}
	for _, row := range head {
		if row.Table == "binding" && row.PK == pk {
			return row.Seq
		}
	}
	t.Fatalf("head holds no binding %s", pk)
	return 0
}

// headSeqs is the sequence number of every head row, in the order head returned
// them. Head is ordered by table and key rather than by sequence, because that is
// the order a walk reads it in.
func headSeqs(head []HeadRow) []int {
	out := make([]int, 0, len(head))
	for _, row := range head {
		out = append(out, row.Seq)
	}
	return out
}

// distinct reports whether no number appears twice. One origin's head is its
// latest state per row, so two rows carrying one sequence number would mean two
// entries claimed one position -- which is what a restored file would cause if it
// numbered its appends from the outbox the restore rewound.
func distinct(seqs []int) bool {
	seen := make(map[int]bool, len(seqs))
	for _, seq := range seqs {
		if seen[seq] {
			return false
		}
		seen[seq] = true
	}
	return true
}

// backupOf copies a database file aside and hands back the copy with a handle on
// the original, since closing a handle gives up its path.
//
// Closing first is what makes the copy the file a restore would put back: the
// close drains the write-ahead log into the main file, and a copy taken beside an
// open handle is a file whose committed rows are still in a log it does not
// carry.
func backupOf(t *testing.T, d *db.DB) (string, *db.DB) {
	t.Helper()
	path := d.Path()
	if err := d.Close(); err != nil {
		t.Fatalf("close %s before copying it: %v", path, err)
	}
	dst := filepath.Join(t.TempDir(), "backup.db")
	copyDatabase(t, path, dst)
	return dst, reopen(t, path, d.Origin())
}

// restoreOver puts a backup back over a database file and returns a handle on the
// restored bytes. The write-ahead log and shared-memory siblings are removed
// first, because they belong to the file that was there: a -wal left beside a
// restored main file carries frames the restored pages never saw, and the next
// open would apply them.
func restoreOver(t *testing.T, d *db.DB, backup string) *db.DB {
	t.Helper()
	path := d.Path()
	if err := d.Close(); err != nil {
		t.Fatalf("close %s before restoring over it: %v", path, err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove %s%s: %v", path, suffix, err)
		}
	}
	copyDatabase(t, backup, path)
	return reopen(t, path, d.Origin())
}

// copyDatabase writes one's file's bytes at another's path, owner-readable only.
func copyDatabase(t *testing.T, src, dst string) {
	t.Helper()
	contents, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	if err := os.WriteFile(dst, contents, 0o600); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
}

// reopen opens a file as the installation it was, the way that machine opens it:
// as the owner of its own rows, which is what an entry's origin is decided by.
func reopen(t *testing.T, path, origin string) *db.DB {
	t.Helper()
	d, err := db.OpenWith(path, db.Options{Origin: origin})
	if err != nil {
		t.Fatalf("reopen %s as %s: %v", path, origin, err)
	}
	return d
}
