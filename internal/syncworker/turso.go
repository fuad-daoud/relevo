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

	"github.com/fuad-daoud/relevo/internal/blobstore"
	turso "turso.tech/database/tursogo"
)

// The log's own vocabulary: the remote columns are named after these writes, so
// what crosses the pipe is what the log stores.
const (
	opUpsert = "upsert"
	opDelete = "delete"
)

// ReplicaDriver is the half of the replica only a sync engine can do: bootstrap
// the replica from the remote, push the replica's changes, apply the remote's,
// and report the engine's own health. Everything else is SQL over the
// connection Open hands back, so the replica's rules test against a plain file
// with no engine and no network. Open is the only way a replica file is ever
// reached: the backend holds no path and opens nothing itself.
type ReplicaDriver interface {
	// Open bootstraps the replica from the remote and returns the pool every
	// statement runs through. It runs once, from hello.
	Open(ctx context.Context, spec Spec) (*sql.DB, error)
	// Bootstrapped is the bytes the last Open downloaded to create the replica,
	// zero when the replica already existed.
	Bootstrapped() int64
	// Push sends the replica's local changes to the remote.
	Push(ctx context.Context) error
	// Pull applies the remote's changes to the replica.
	Pull(ctx context.Context) error
	// Stats is the engine's own health call, and the network counters come back
	// with it: asking the engine first keeps a size from being reported for a
	// replica nobody can reach.
	Stats(ctx context.Context) (turso.TursoSyncDbStats, error)
	// Checkpoint folds the replica's WAL into its database file. The engine
	// never does this on its own for a sync replica.
	Checkpoint(ctx context.Context) error
	// Discard removes the replica and the driver's files so the next Open
	// bootstraps it again.
	Discard(ctx context.Context) error
}

// TursoBackend is the log behind the worker's replica. It is what serves the
// pipe's export, pull, head and stats verbs.
type TursoBackend struct {
	driver ReplicaDriver
	spec   Spec
	db     *sql.DB
	// blobs is the bucket segments are written to and fetched from, nil when
	// hello named no R2 settings.
	blobs blobstore.BlobStore
	// openStore builds blobs from hello's settings. It is per backend so tests
	// running in parallel can each hand their own backend a store without
	// swapping the package's constructor under one another.
	openStore func(R2Config) blobstore.BlobStore
	// page bounds one pull's read. It is a field so a test can lower it and pin
	// that a cut batch still comes back whole.
	page int
	// bytes holds what the replica has moved over this worker's life, split by
	// direction, cumulative from zero: the replica file carries the engine's own
	// counters from before this process existed, which were an earlier worker's
	// to report.
	bytes Stats
	// lastEngine is the engine's counters as of the last transfer, and haveLast
	// says whether one was ever read. The flag stops a failed reading from being
	// taken for a zero: an unreadable counter is not a counter that did not
	// move.
	lastEngine turso.TursoSyncDbStats
	haveLast   bool
	// warn is where a failed checkpoint is reported: the worker's stderr, which
	// is the daemon's log; nil reports nothing.
	warn io.Writer
}

// The backend is what the worker serves the pipe with: naming it here means a
// verb the pipe speaks cannot lose its implementation without failing the
// build.
var _ Backend = (*TursoBackend)(nil)

// NewTursoBackend returns the log behind the replica driver owns. The driver is
// supplied rather than named so a test serves the pipe against a plain file.
func NewTursoBackend(driver ReplicaDriver) *TursoBackend {
	return &TursoBackend{
		driver:    driver,
		page:      pullPageSize,
		warn:      os.Stderr,
		openStore: func(cfg R2Config) blobstore.BlobStore { return newBlobStore(cfg) },
	}
}

// NewTursoBackendForPath returns the backend production runs: the replica at
// replicaPath, opened only through the sync constructor the driver wraps.
func NewTursoBackendForPath(replicaPath string) Backend {
	return NewTursoBackend(newTursoDriver(replicaPath))
}

// Open bootstraps the replica from the remote and makes sure the remote holds
// the log's schema before anything else touches it.
func (b *TursoBackend) Open(spec Spec) error {
	ctx := context.Background()
	// A second hello is a fresh handshake on an open pipe; the first replica is
	// released before the next is taken so no file is held twice.
	if b.db != nil {
		_ = b.Close()
	}
	db, err := b.driver.Open(ctx, spec)
	if err != nil {
		return fmt.Errorf("syncworker: open replica: %w", markOpenRefusal(err))
	}
	b.spec = spec
	b.db = db
	if spec.R2 != nil {
		b.blobs = b.openStore(*spec.R2)
	} else {
		b.blobs = nil
	}
	b.bytes.TursoReceived += b.driver.Bootstrapped()
	if err := b.resetUnknownShape(ctx); err != nil {
		_ = b.Close()
		return err
	}
	if err := b.ensureLog(ctx, spec.URL); err != nil {
		_ = b.Close()
		return err
	}
	return nil
}

// Pull applies the remote's changes to the replica, then returns one page per
// origin of every other origin's entries past the mark given for it, read from
// the bucket's segments the heads rows name. A backlog larger than a page
// drains one page per sync tick, and a batch beginning at or before a mark is
// skipped whole: half of one would apply an order the exporter never wrote. A
// hello that named no bucket is refused before any transfer: the segments have
// nowhere to be read from, and the refusal costs no traffic. With HistoryNone,
// Pull answers from the shared table only and never lists or fetches segments,
// even when hello named no bucket.
func (b *TursoBackend) Pull(marks map[string]int) ([]Entry, error) {
	if err := b.ready(); err != nil {
		return nil, err
	}
	ctx := context.Background()
	if b.spec.History == HistoryNone {
		if err := b.transfer(ctx, b.driver.Pull); err != nil {
			return nil, fmt.Errorf("syncworker: pull: %w", markTransferRefusal(err))
		}
		return b.sharedPull(ctx, marks)
	}
	// A full-history worker with no bucket gets ErrNoR2: answering it from
	// shared would let the importer move a mark past rows nothing fetched.
	if b.blobs == nil {
		return nil, ErrNoR2
	}
	if err := b.transfer(ctx, b.driver.Pull); err != nil {
		return nil, fmt.Errorf("syncworker: pull: %w", markTransferRefusal(err))
	}
	return b.readEntries(ctx, marks)
}

// Head returns one page of an origin's latest state per row, in ascending
// sequence order, derived from the segments the origin's heads row names. An
// empty cursor starts at the beginning and a limit of zero means the whole
// rest.
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
	ctx := context.Background()
	h, ok, err := b.readOriginHead(ctx, origin)
	if err != nil {
		return nil, err
	}
	if !ok {
		return []HeadRow{}, nil
	}
	if b.blobs == nil {
		return nil, ErrNoR2
	}
	return b.deriveHead(ctx, h, start, limit)
}

// Stats reports what the log holds: entries, origins, and the highest sequence.
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
		"SELECT COALESCE(SUM(entries), 0), COUNT(*), COALESCE(MAX(seq), 0) FROM heads").
		Scan(&s.Entries, &s.Origins, &s.Seq); err != nil {
		return Stats{}, fmt.Errorf("syncworker: read the log's size: %w", err)
	}
	// The byte counters ride along with the log's size, read from this worker's
	// accumulated totals rather than the engine's own running counters.
	s.TursoSent, s.TursoReceived = b.bytes.TursoSent, b.bytes.TursoReceived
	s.R2Put, s.R2Get = b.bytes.R2Put, b.bytes.R2Get
	return s, nil
}

// Close releases the replica's connections. The driver's own handle is dropped
// rather than closed, because the bindings expose no close, and the worker
// process ending right after is what releases the file it still holds.
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

// ready reports whether hello has opened the replica. A verb served only after
// the handshake refuses rather than panics on a nil handle: this second gate
// does not rest on the dispatch ordering.
func (b *TursoBackend) ready() error {
	if b.db == nil {
		return fmt.Errorf("syncworker: no replica: %w", ErrNoHello)
	}
	return nil
}

// The shape the engine's own refusal takes: the call failed to execute SQL,
// and -- when this machine's table is the one missing -- which table it was.
// The worker matches the shape itself rather than sharing internal/sync's
// classifier (remoterefusal.go), because it may not reach that package's
// database. The two must agree.
const (
	remoteRefusalMarker = "failed to execute sql"
	remoteMissingTable  = "no such table"
)

// markDriverRefusal returns err marked with the class of remote refusal it is,
// and every other error unchanged. A missing table is its own class because its
// fix is the schema; any other refused statement repeats until the change set
// changes.
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
// canonical encoding. The daemon encodes every body once with sorted columns
// and the wire carries exactly those bytes, so re-marshalling reproduces
// synclog.BodyHash without importing internal/db across the worker's boundary.
// A test pins the two digests equal.
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
