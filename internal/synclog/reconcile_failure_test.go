package synclog

import (
	"errors"
	"fmt"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// What reconcile does when the exchange underneath it refuses: a transport that
// cannot answer head, a transport that will not take an append, and a file that
// is not readable.
//
// Each of these is a failure a machine actually meets -- a remote that is down, a
// log that refuses an origin's entries, a file restored underneath an open handle
// -- and each has one required behaviour: report it, append nothing, and leave
// the next run to try again from head.

// brokenHead is a transport whose head cannot be read. A remote that is
// unreachable reports exactly this, and a reconcile that guessed past it would
// propose every row it owns on no evidence at all.
type brokenHead struct {
	*MemTransport
	failHead   bool
	failAppend bool
}

// Head fails where the case set it to and otherwise answers, so a case can turn
// one failure off and watch the round carry on.
func (b *brokenHead) Head(origin string) ([]HeadRow, error) {
	if b.failHead {
		return nil, fmt.Errorf("synclog: the log did not answer")
	}
	return b.MemTransport.Head(origin)
}

// Append refuses the batches where the case set it to refuse, the way a log
// refuses an origin's entries. The refusal must reach the caller and nothing may
// be counted as appended.
func (b *brokenHead) Append(entries []Entry) ([]Entry, error) {
	if b.failAppend {
		return nil, fmt.Errorf("synclog: the log refused the append")
	}
	return b.MemTransport.Append(entries)
}

// A head that cannot be read stops the round with the failure reported, and
// nothing is proposed: every row would have been proposed on no evidence.
func TestReconcileReportsAHeadItCannotRead(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	seed(t, path, insertBinding("01A", "m1"))

	r := &brokenHead{MemTransport: NewMemTransport("m1"), failHead: true}
	got, err := reconciler(d, r).Reconcile()
	if err == nil {
		t.Fatal("reconcile against an unreadable head succeeded, want the failure reported")
	}
	if got.Batches != 0 || got.Upserts != 0 || got.Deletes != 0 {
		t.Fatalf("reconcile = %+v, want nothing counted when head could not be read", got)
	}

	// The same file reconciles once the log answers, so the failure was the
	// transport's and not a difference that would have been proposed either way.
	r.failHead = false
	if _, err := reconciler(d, r).Reconcile(); err != nil {
		t.Fatalf("reconcile once the log answers: %v", err)
	}
	if head, err := r.Head("m1"); err != nil || len(head) != 1 {
		t.Fatalf("head holds %d rows (%v), want the one the successful round appended", len(head), err)
	}
}

// A refused append is reported and nothing is counted, and the run does not spin
// on it: a reconcile that retried its own refusal would append the same batch
// forever against a log that will never take it.
func TestReconcileReportsARefusedAppend(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	seed(t, path, insertBinding("01A", "m1"))

	r := &brokenHead{MemTransport: NewMemTransport("m1"), failAppend: true}
	got, err := reconciler(d, r).Reconcile()
	if err == nil {
		t.Fatal("reconcile through a refusing log succeeded, want the refusal reported")
	}
	if got.Batches != 0 || got.Upserts != 0 || got.Deletes != 0 {
		t.Fatalf("reconcile = %+v, want nothing counted for a refused append", got)
	}

	head, err := r.MemTransport.Head("m1")
	if err != nil {
		t.Fatalf("read head after the refusal: %v", err)
	}
	if len(head) != 0 {
		t.Fatalf("head holds %d rows, want none: a refused append wrote nothing", len(head))
	}

	// The next run proposes the same rows, because a refused append left head as
	// it was. Re-proposing is safe: import is an idempotent upsert, so a log
	// holding a batch twice costs a reader one redundant write rather than a
	// wrong one.
	recording := &recording{MemTransport: r.MemTransport}
	if _, err := reconciler(d, recording).Reconcile(); err != nil {
		t.Fatalf("reconcile once the log accepts: %v", err)
	}
	if shape := recording.shape(); shape != `binding ["01A"] upsert` {
		t.Fatalf("the retry appended %q, want the batch the refusal held back", shape)
	}
}

// A file that cannot be read stops the round where the read failed rather than
// proposing the rows it could not check. A row whose state is unknown is not a
// row that changed.
func TestReconcileReportsAFileItCannotRead(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	seed(t, path, insertBinding("01A", "m1"))

	r := &recording{MemTransport: NewMemTransport("m1")}
	if _, err := reconciler(d, r).Reconcile(); err != nil {
		t.Fatalf("reconcile the readable file: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close the file under the reconcile: %v", err)
	}

	got, err := reconciler(d, r).Reconcile()
	if err == nil {
		t.Fatal("reconcile against an unreadable file succeeded, want the failure reported")
	}
	if got.Batches != 0 || got.Upserts != 0 {
		t.Fatalf("reconcile = %+v, want no rows proposed from a file that could not be read", got)
	}
}

// A head row naming a key the file cannot parse is reported rather than skipped.
// The key came from a log rather than from this machine, so a key that is not the
// shape this schema uses is a question the walk cannot answer -- and skipping it
// would leave the row unproposed for ever, which is the same as saying it had not
// changed.
func TestReconcileReportsAHeadKeyItCannotRead(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	seed(t, path, insertBinding("01A", "m1"))

	log := NewMemTransport("m1")
	// The entry passes the shape check: an upsert carrying a body is well formed.
	// What makes the key unreadable is that it is not the json_array of this
	// table's key columns, which only a row read can discover -- so the row lands
	// in head and the failure surfaces when the walk asks whether it is still here.
	appendBatch(t, log, Entry{
		Table:         "binding",
		PK:            "not-a-key",
		Op:            OpUpsert,
		SchemaVersion: knownVersion(t, d),
		Body:          bodyFor(t, map[string]any{"id": "not-a-key"}),
	})

	_, err := reconciler(d, &recording{MemTransport: log}).Reconcile()
	if err == nil {
		t.Fatal("reconcile with an unreadable head key succeeded, want the failure reported")
	}
	if !errors.Is(err, db.ErrInvalid) {
		t.Fatalf("reconcile = %v, want the refusal to carry ErrInvalid", err)
	}
}

// A batch that fills exactly as the deletes begin is cut there rather than
// overflowing the chunk, so the run stops between the two groups and the next pass
// resumes at the delete. The cut is what keeps a chunked run's sequence the same
// as an unchunked one's.
func TestReconcileCutsBetweenUpsertsAndDeletes(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	seed(t, path,
		insertBinding("01A", "m1"),
		insertRound("02A", "01A"),
		insertBinding("03A", "m1"),
	)

	log := NewMemTransport("m1")
	if _, err := reconciler(d, &recording{MemTransport: log}).Reconcile(); err != nil {
		t.Fatalf("reconcile the settled file: %v", err)
	}
	// The change leaves one upsert and two deletes, so a chunk of two fills in the
	// middle of the deletes and cannot carry them all.
	seed(t, path,
		`UPDATE binding SET cwd = '/moved' WHERE id = '03A'`,
		`DELETE FROM round WHERE id = '02A'`,
		`DELETE FROM binding WHERE id = '01A'`,
	)

	r := &recording{MemTransport: log}
	e := reconciler(d, r)
	e.chunk = 2
	one, err := e.ReconcileBatch()
	if err != nil {
		t.Fatalf("reconcile one batch: %v", err)
	}
	if one.Batches != 1 || one.Upserts != 1 || one.Deletes != 1 {
		t.Fatalf("the first batch = %+v, want the upsert and the first delete", one)
	}
	if shape := r.shape(); shape != `binding ["03A"] upsert|round ["02A"] delete` {
		t.Fatalf("the first batch was %q, want the upsert ahead of the delete", shape)
	}

	// The rest arrives in the next batch, and nothing is proposed twice.
	next := &recording{MemTransport: log}
	if _, err := reconciler(d, next).ReconcileBatch(); err != nil {
		t.Fatalf("reconcile the next batch: %v", err)
	}
	if shape := next.shape(); shape != `binding ["01A"] delete` {
		t.Fatalf("the next batch was %q, want the removal the first could not carry", shape)
	}
	if _, err := reconciler(d, next).Reconcile(); err != nil {
		t.Fatalf("reconcile once every batch is through: %v", err)
	}
}
