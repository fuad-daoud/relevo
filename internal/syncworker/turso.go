package syncworker

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	turso "turso.tech/database/tursogo"
)

// The log's own vocabulary. An entry is one of these two writes, and the remote
// columns are named after them so what crosses the pipe is what the log stores.
const (
	opUpsert = "upsert"
	opDelete = "delete"
)

// ReplicaDriver is the half of the replica only a sync engine can do: bootstrap
// the local replica from the remote, push the replica's changes, apply the
// remote's, and report the engine's own health. Everything else -- the log,
// head and meta tables, sequence assignment, paging -- is SQL over the
// connection Open hands back, so the replica's rules can be tested against a
// plain file with no engine and no network.
//
// Open is the only way a replica file is ever reached: the backend holds no
// path and opens nothing itself, which is what keeps every statement on the
// connection the sync constructor owns.
type ReplicaDriver interface {
	// Open bootstraps the replica from the remote and returns the pool every
	// statement runs through. It runs once, from hello.
	Open(ctx context.Context, spec Spec) (*sql.DB, error)
	// Push sends the replica's local changes to the remote.
	Push(ctx context.Context) error
	// Pull applies the remote's changes to the replica.
	Pull(ctx context.Context) error
	// Stats is the engine's own health call, and it hands back the engine's own
	// network counters with it. The transport's stats are the log's size, read in
	// SQL; asking the engine first keeps a size from being reported for a replica
	// nobody can reach, and the counters are what the byte totals are measured
	// from.
	Stats(ctx context.Context) (turso.TursoSyncDbStats, error)
	// Checkpoint folds the replica's WAL into its database file. The engine
	// never does this on its own for a sync replica.
	Checkpoint(ctx context.Context) error
}

// TursoBackend is the log behind the worker's replica. It is what serves the
// pipe's export, pull, head and stats verbs.
type TursoBackend struct {
	driver ReplicaDriver
	spec   Spec
	db     *sql.DB
	// page bounds one pull's read. It is a field so a test can lower it and pin
	// that a page which cuts a batch still hands the batch back whole.
	page int
	// bytes holds what the replica has moved over this worker's life, split by
	// direction. They are cumulative and start at zero on a fresh backend rather
	// than being read off the replica file: the file carries the engine's own
	// counters from before this process existed, and those bytes were some
	// earlier worker's to report.
	bytes Stats
	// lastEngine is the engine's counters as of the last transfer this backend
	// made, and haveLast says whether one was ever read. Each push and pull takes
	// a reading before and after itself and adds the difference, so the totals
	// count what this worker moved rather than what the replica has held since it
	// was written. The flag is what stops a failed reading from being taken for a
	// zero: an unreadable counter is not a counter that did not move.
	lastEngine turso.TursoSyncDbStats
	haveLast   bool
	// warn is where a failed checkpoint is reported: the worker's stderr, which
	// is the daemon's log. Nil reports nothing.
	warn io.Writer
}

// The backend is what the worker serves the pipe with. Naming it here means a
// verb the pipe speaks cannot lose its implementation without the tree failing
// to build.
var _ Backend = (*TursoBackend)(nil)

// NewTursoBackend returns the log behind the replica driver owns. The driver is
// supplied rather than named so a test serves the pipe against a plain file.
func NewTursoBackend(driver ReplicaDriver) *TursoBackend {
	return &TursoBackend{driver: driver, page: pullPageSize, warn: os.Stderr}
}

// NewTursoBackendForPath returns the backend production runs: the replica at
// replicaPath, opened only through the sync constructor the driver wraps.
func NewTursoBackendForPath(replicaPath string) Backend {
	return NewTursoBackend(newTursoDriver(replicaPath))
}

// Open bootstraps the replica from the remote and makes sure the remote holds
// the log's schema before anything reads or writes it.
func (b *TursoBackend) Open(spec Spec) error {
	ctx := context.Background()
	// A second hello is a fresh handshake on a pipe that is already open; the
	// first replica is released before the next is taken so no file is held
	// twice.
	if b.db != nil {
		_ = b.Close()
	}
	db, err := b.driver.Open(ctx, spec)
	if err != nil {
		return fmt.Errorf("syncworker: open replica: %w", err)
	}
	b.spec = spec
	b.db = db
	if err := b.ensureLog(ctx, spec.URL); err != nil {
		_ = b.Close()
		return err
	}
	return nil
}

// Append writes one batch of this origin's entries as a single replica
// transaction and pushes it. The sequence numbers come from the replica's own
// high-water mark, never from the request: a local file restored from a backup
// arrives with entries it never wrote, and a request's numbering would start
// over and reuse a number the log already holds.
//
// The push comes before the reply. The daemon clears the outbox rows an append
// covered only once it has the numbered entries back, so an append answered
// before the remote holds it would drop changes the log never received.
func (b *TursoBackend) Append(entries []Entry) ([]Entry, error) {
	return b.AppendSigned(entries, Boundary{})
}

// AppendSigned is Append with the boundary the exporter predicted for its first
// signed entry.
func (b *TursoBackend) AppendSigned(entries []Entry, boundary Boundary) ([]Entry, error) {
	if err := b.ready(); err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return []Entry{}, nil
	}
	for _, e := range entries {
		if e.Origin != b.spec.Origin {
			return nil, fmt.Errorf("syncworker: append %s %s as %s: %w", e.Tbl, e.PK, e.Origin, ErrForeignOrigin)
		}
	}
	ctx := context.Background()
	written, err := b.writeBatch(ctx, entries, boundary)
	if err != nil {
		return nil, err
	}
	if err := b.transfer(ctx, true, b.driver.Push); err != nil {
		return nil, fmt.Errorf("syncworker: push: %w", markTransferRefusal(err))
	}
	return written, nil
}

// writeBatch numbers one batch and writes its log rows and head updates in one
// transaction, so a reader sees the whole batch or none of it.
func (b *TursoBackend) writeBatch(ctx context.Context, entries []Entry, boundary Boundary) ([]Entry, error) {
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("syncworker: append: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var high sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		"SELECT MAX(seq) FROM log WHERE origin = ?", b.spec.Origin).Scan(&high); err != nil {
		return nil, fmt.Errorf("syncworker: read the high sequence: %w", err)
	}
	next := int(high.Int64) + 1
	written := make([]Entry, 0, len(entries))
	for i, e := range entries {
		e.Origin = b.spec.Origin
		e.Seq = next + i
		// Every entry of one request carries the first sequence of that request
		// so a reader can tell where the batch ends and apply it whole.
		e.Batch = next
		if err := insertEntry(ctx, tx, e); err != nil {
			return nil, err
		}
		if err := updateHead(ctx, tx, e); err != nil {
			return nil, err
		}
		written = append(written, e)
	}
	if err := writeSignedFrom(ctx, tx, b.spec.Origin, written, boundary); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("syncworker: append: %w", err)
	}
	return written, nil
}

// insertEntry writes one entry's log row. A delete carries no body, so it is
// stored as NULL rather than as an empty string that would read as a body. An
// unsigned entry's MAC goes in the same way: NULL is what says "this entry was
// written before this machine had a key", and it is not the same answer as a
// MAC that failed to verify.
func insertEntry(ctx context.Context, tx *sql.Tx, e Entry) error {
	var body any
	if e.Op == opUpsert {
		body = string(e.Body)
	}
	var mac any
	if len(e.MAC) > 0 {
		mac = e.MAC
	}
	var afterOrigin any
	if e.AfterOrigin != "" {
		afterOrigin = e.AfterOrigin
	}
	var afterSeq any
	if e.AfterSeq > 0 {
		afterSeq = e.AfterSeq
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO log (origin, seq, batch, tbl, pk, op, schema_version, body, at, mac, after_origin, after_seq)"+
			" VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		e.Origin, e.Seq, e.Batch, e.Tbl, e.PK, e.Op, e.SchemaVersion, body,
		e.At.UTC().Format(time.RFC3339Nano), mac, afterOrigin, afterSeq); err != nil {
		return fmt.Errorf("syncworker: append %s %s: %w", e.Tbl, e.PK, err)
	}
	return nil
}

// updateHead moves the row's latest state. An upsert replaces the row's state
// with this entry's hash; a delete drops it, because a row the origin removed
// is not one reconcile should keep proposing.
func updateHead(ctx context.Context, tx *sql.Tx, e Entry) error {
	if e.Op == opDelete {
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM head WHERE origin = ? AND tbl = ? AND pk = ?", e.Origin, e.Tbl, e.PK); err != nil {
			return fmt.Errorf("syncworker: head %s %s: %w", e.Tbl, e.PK, err)
		}
		return nil
	}
	hash, err := bodyHash(e.Body)
	if err != nil {
		return fmt.Errorf("syncworker: hash %s %s: %w", e.Tbl, e.PK, err)
	}
	// An update, then an insert when it moved nothing, rather than one upsert:
	// on a sync connection the engine can refuse an upsert that lands on an
	// existing head row as a corrupt record, while a plain update of the same
	// row goes through. The worker is the replica's only writer, so the two
	// statements in one transaction cannot race another insert of the row.
	res, err := tx.ExecContext(ctx,
		"UPDATE head SET seq = ?, hash = ? WHERE origin = ? AND tbl = ? AND pk = ?",
		e.Seq, hash, e.Origin, e.Tbl, e.PK)
	if err != nil {
		return fmt.Errorf("syncworker: head %s %s: %w", e.Tbl, e.PK, err)
	}
	moved, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("syncworker: head %s %s: %w", e.Tbl, e.PK, err)
	}
	if moved > 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO head (origin, tbl, pk, seq, hash) VALUES (?, ?, ?, ?, ?)",
		e.Origin, e.Tbl, e.PK, e.Seq, hash); err != nil {
		return fmt.Errorf("syncworker: head %s %s: %w", e.Tbl, e.PK, err)
	}
	return nil
}

// Pull applies the remote's changes to the replica, then returns one page per
// origin of every other origin's entries past the mark given for it. The importer
// calls Pull once per run, so a backlog larger than a page drains one page per
// sync tick. A batch that begins at or before a mark is skipped whole: the
// importer applies whole batches, and half of one would apply an order the
// exporter never wrote.
func (b *TursoBackend) Pull(marks map[string]int) ([]Entry, error) {
	if err := b.ready(); err != nil {
		return nil, err
	}
	ctx := context.Background()
	if err := b.transfer(ctx, false, b.driver.Pull); err != nil {
		return nil, fmt.Errorf("syncworker: pull: %w", markTransferRefusal(err))
	}
	entries, err := b.readEntries(ctx, marks)
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// scanEntry reads one log row back into the entry the pipe carries. The MAC is
// read as raw bytes rather than as text: it is a digest, and the column is a
// BLOB, so any decoding of it on the way in would have to be undone on the way
// out for the two to agree.
func scanEntry(rows *sql.Rows) (Entry, error) {
	var (
		e           Entry
		body        sql.NullString
		mac         []byte
		at          string
		afterOrigin sql.NullString
		afterSeq    sql.NullInt64
	)
	if err := rows.Scan(&e.Origin, &e.Seq, &e.Batch, &e.Tbl, &e.PK, &e.Op,
		&e.SchemaVersion, &body, &at, &mac, &afterOrigin, &afterSeq); err != nil {
		return Entry{}, fmt.Errorf("syncworker: read an entry: %w", err)
	}
	if body.Valid {
		e.Body = json.RawMessage(body.String)
	}
	if len(mac) > 0 {
		e.MAC = mac
	}
	parsed, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return Entry{}, fmt.Errorf("syncworker: read %s %s time: %w", e.Tbl, e.PK, err)
	}
	e.At = parsed
	if afterOrigin.Valid && afterOrigin.String != "" {
		e.AfterOrigin = afterOrigin.String
	}
	if afterSeq.Valid && afterSeq.Int64 > 0 {
		e.AfterSeq = int(afterSeq.Int64)
	}
	return e, nil
}

// Head returns one page of an origin's latest state per row, in ascending
// sequence order so a caller can resume from the sequence it last saw. An empty
// cursor starts at the beginning and a limit of zero means the whole rest.
func (b *TursoBackend) Head(origin, after string, limit int) ([]HeadRow, error) {
	if err := b.ready(); err != nil {
		return nil, err
	}
	if origin == "" {
		return nil, fmt.Errorf("syncworker: head without an origin: %w", ErrProtocol)
	}
	start := 0
	if after != "" {
		n, err := strconv.Atoi(after)
		if err != nil {
			return nil, fmt.Errorf("syncworker: head cursor %q: %w", after, ErrProtocol)
		}
		start = n
	}
	query := "SELECT tbl, pk, seq, hash FROM head WHERE origin = ? AND seq > ? ORDER BY seq"
	args := []any{origin, start}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	ctx := context.Background()
	rows, err := b.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("syncworker: read head: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []HeadRow
	for rows.Next() {
		var row HeadRow
		if err := rows.Scan(&row.Tbl, &row.PK, &row.Seq, &row.Hash); err != nil {
			return nil, fmt.Errorf("syncworker: read a head row: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("syncworker: read head: %w", err)
	}
	return out, nil
}

// Stats reports what the log holds: how many entries, how many origins wrote
// them, and the highest sequence in it.
func (b *TursoBackend) Stats() (Stats, error) {
	if err := b.ready(); err != nil {
		return Stats{}, err
	}
	ctx := context.Background()
	if _, err := b.driver.Stats(ctx); err != nil {
		return Stats{}, fmt.Errorf("syncworker: driver stats: %w", markDriverRefusal(err))
	}
	var s Stats
	if err := b.db.QueryRowContext(ctx,
		"SELECT COUNT(*), COUNT(DISTINCT origin), COALESCE(MAX(seq), 0) FROM log").
		Scan(&s.Entries, &s.Origins, &s.Seq); err != nil {
		return Stats{}, fmt.Errorf("syncworker: read the log's size: %w", err)
	}
	// The byte counters ride along with the log's size. They are what this
	// worker moved rather than what the engine has moved, so they are read from
	// the accumulated totals and never seeded from the engine's own running
	// counters.
	s.TursoSent, s.TursoReceived = b.bytes.TursoSent, b.bytes.TursoReceived
	s.TursoPushes, s.TursoPulls = b.bytes.TursoPushes, b.bytes.TursoPulls
	return s, nil
}

// Close releases the replica's connections. The driver's own handle is dropped
// rather than closed -- the bindings expose no close -- and the worker process
// ends right after, which is what releases the file it still holds.
func (b *TursoBackend) Close() error {
	if b.db == nil {
		return nil
	}
	db := b.db
	b.db = nil
	if err := db.Close(); err != nil {
		return fmt.Errorf("syncworker: close the replica: %w", err)
	}
	return nil
}

// ready reports whether hello has opened the replica. A verb the pipe serves
// only after the handshake still refuses rather than panics on a nil handle:
// the worker's dispatch orders the verbs, and this is the second gate that does
// not rest on the first.
func (b *TursoBackend) ready() error {
	if b.db == nil {
		return fmt.Errorf("syncworker: no replica: %w", ErrNoHello)
	}
	return nil
}

// The shape the engine's own refusal takes: the call failed to execute SQL, and
// -- when this machine's table is the one missing -- which table it was. The
// worker matches the shape itself rather than sharing internal/sync's
// classifier, because the class is a word this package owns and the worker may
// not reach that package's database.
const (
	remoteRefusalMarker = "failed to execute sql"
	remoteMissingTable  = "no such table"
)

// markDriverRefusal returns err marked with the class of remote refusal it is,
// and every other error unchanged. A missing table is its own class because its
// fix is the schema; any other refused statement repeats until the change set
// changes, and both meet the same remote on every later attempt.
func markDriverRefusal(err error) error {
	if !strings.Contains(err.Error(), remoteRefusalMarker) {
		return err
	}
	if strings.Contains(err.Error(), remoteMissingTable) {
		return MarkRefusal(CodeSchema, err)
	}
	return MarkRefusal(CodeRemote, err)
}

// bodyHash is the digest head carries for a row: the sha256 of the body's
// canonical encoding. The daemon encodes every body once, with its columns
// sorted, and the wire carries exactly those bytes, so re-marshalling the
// object reproduces synclog.BodyHash without this package importing it -- an
// import that would drag internal/db into the worker and break the boundary the
// worker exists to keep. A test pins the two digests equal.
func bodyHash(body json.RawMessage) (string, error) {
	var columns map[string]json.RawMessage
	if err := json.Unmarshal(body, &columns); err != nil {
		return "", fmt.Errorf("body is not a JSON object: %w", err)
	}
	canonical, err := json.Marshal(columns)
	if err != nil {
		return "", fmt.Errorf("canonical body: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}
