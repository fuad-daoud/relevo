package synclog

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/blobstore"
)

// MemTransport is the in-memory LogTransport tests run against. It is one
// installation's handle: it appends as its own origin, reads what other origins
// wrote, and numbers from the log's high-water mark rather than a kept counter,
// so a backup-restored file cannot reissue a held number.
// It holds entries, per-row latest state, and each origin's sequence, writing each
// append as one unit so a reader sees batch ends. It is the spec exporter and
// importer are written against, so seq, mark or head disagreements show up here first.
type MemTransport struct {
	origin string
	log    *memLog
}

// memLog is the log itself: entries in append order, per-row latest state, each
// origin's sequence, and published marks. It is separate from the transport so
// two installations can hold the same log, the arrangement the exchange runs in.
type memLog struct {
	mu      sync.Mutex
	entries []Entry
	head    map[string][]HeadRow
	// marks is keyed by reader then origin: a handle records only its own reader's
	// rows, and a second handle on the same log sees them, which is the rule the
	// remote enforces by key.
	marks map[string]map[string]Mark
	// floors is what each origin has compacted to.
	floors map[string]int
	// signedFrom is the first sequence of each origin that carried a MAC.
	signedFrom map[string]int
	// signedTag is the tag stored with each signedFrom entry.
	signedTag map[string][]byte
	// key is the passphrase material the log holds.
	key KeyMeta
	// epochCut is the cut each origin's history is fenced at, as a rekey wrote
	// it: one row per origin, the newest rekey's standing.
	epochCut map[string]EpochCut
	// claims is the adoption claim each identity holds.
	claims map[string]claimRow
	// traffic is each origin's published month of sync traffic: one row per
	// machine, overwritten by its own publishes.
	traffic map[string]TrafficRow
}

// NewMemTransport returns a handle on an empty log for the installation named
// origin. Every other origin handed entries is a machine elsewhere: their entries
// are read, never appended as.
func NewMemTransport(origin string) *MemTransport {
	return &MemTransport{origin: origin, log: newMemLog()}
}

// newMemLog is an empty log.
func newMemLog() *memLog {
	return &memLog{head: make(map[string][]HeadRow), marks: make(map[string]map[string]Mark)}
}

// OnLog returns a second installation's handle on the same log: each keeps its own
// origin and sequence and reads the other's entries.
func (m *MemTransport) OnLog(origin string) *MemTransport {
	return &MemTransport{origin: origin, log: m.log}
}

// append records a batch as one write, numbering from one past this origin's
// highest sequence. Everything failable is checked before writing, so a refused
// batch leaves the log as it was.
func (m *memLog) append(origin string, entries []Entry, boundary Boundary) ([]Entry, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	hashes := make([]string, len(entries))
	for i, e := range entries {
		if e.Origin != "" && e.Origin != origin {
			return nil, fmt.Errorf("synclog: append %s %s as %s: %w",
				e.Table, e.PK, e.Origin, ErrInvalid)
		}
		if err := checkShape(e); err != nil {
			return nil, err
		}
		if e.Op == OpUpsert {
			hash, err := entryHash(e)
			if err != nil {
				return nil, fmt.Errorf("synclog: append %s %s: %w", e.Table, e.PK, err)
			}
			hashes[i] = hash
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// max+1 rather than a counter: a backup-restored machine would otherwise
	// renumber from zero and collide with the log's held numbers.
	next := m.latestSeq(origin) + 1
	if err := m.checkBoundary(origin, entries, next, boundary); err != nil {
		return nil, err
	}
	written := make([]Entry, 0, len(entries))
	for i, e := range entries {
		e.Origin = origin
		e.Seq = next + i
		// Every entry of one append carries its first's sequence, so a reader finds
		// batch ends and applies them whole.
		e.Batch = next
		m.entries = append(m.entries, e)
		m.writeHead(e, hashes[i])
		written = append(written, e)
	}
	m.recordSignedFrom(written, boundary)
	return written, nil
}

// pull returns every origin's entries past its mark, in sequence order and whole
// batches. A batch at or before the mark is skipped whole: handing half over
// would apply an order the exporter never wrote.
func (m *memLog) pull(origin string, marks map[string]int) []Entry {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []Entry
	for _, source := range m.originsOtherThan(origin) {
		mark := marks[source]
		for _, e := range m.entries {
			if e.Origin != source || e.Seq <= mark || e.Batch <= mark {
				continue
			}
			out = append(out, e)
		}
	}
	return out
}

// headOf returns one origin's latest state per row, table then key order, so a
// page walk sees the same sequence every time.
func (m *memLog) headOf(origin string) []HeadRow {
	m.mu.Lock()
	defer m.mu.Unlock()

	rows := append([]HeadRow(nil), m.head[origin]...)
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Table != rows[j].Table {
			return tableIndex(rows[i].Table) < tableIndex(rows[j].Table)
		}
		return rows[i].PK < rows[j].PK
	})
	return rows
}

// stats reports the log's size: entries held, machines written, highest sequence.
func (m *memLog) stats() Stats {
	m.mu.Lock()
	defer m.mu.Unlock()

	stats := Stats{Entries: len(m.entries), Origins: len(m.origins())}
	for _, e := range m.entries {
		if e.Seq > stats.Seq {
			stats.Seq = e.Seq
		}
	}
	return stats
}

// latestSeq is the highest sequence the log holds for origin, zero when none.
func (m *memLog) latestSeq(origin string) int {
	high := 0
	for _, e := range m.entries {
		if e.Origin == origin && e.Seq > high {
			high = e.Seq
		}
	}
	return high
}

// writeHead moves one origin's latest state for the row the entry names: an
// upsert replaces it, a delete drops it as no longer proposable.
func (m *memLog) writeHead(e Entry, hash string) {
	if e.Op == OpDelete {
		m.head[e.Origin] = removeHead(m.head[e.Origin], e.Table, e.PK)
		return
	}
	m.head[e.Origin] = replaceHead(m.head[e.Origin], e, hash)
}

// replaceHead records e as the latest state of its row, keeping length and order.
func replaceHead(rows []HeadRow, e Entry, hash string) []HeadRow {
	updated := HeadRow{Table: e.Table, PK: e.PK, Seq: e.Seq, Hash: hash}
	for i := range rows {
		if rows[i].Table == e.Table && rows[i].PK == e.PK {
			rows[i] = updated
			return rows
		}
	}
	return append(rows, updated)
}

// removeHead drops the row from an origin's latest state.
func removeHead(rows []HeadRow, table, pk string) []HeadRow {
	out := rows[:0]
	for _, row := range rows {
		if row.Table == table && row.PK == pk {
			continue
		}
		out = append(out, row)
	}
	return out
}

// origins is every origin the log holds entries for, in first-appearance order.
func (m *memLog) origins() []string {
	seen := make(map[string]bool)
	var out []string
	for _, e := range m.entries {
		if seen[e.Origin] {
			continue
		}
		seen[e.Origin] = true
		out = append(out, e.Origin)
	}
	return out
}

// originsOtherThan is the origins a pull returns: everyone but the caller, whose
// own writes are already in its file and reading them back would apply them twice.
func (m *memLog) originsOtherThan(self string) []string {
	var out []string
	for _, origin := range m.origins() {
		if origin != self {
			out = append(out, origin)
		}
	}
	sort.Strings(out)
	return out
}

// Append numbers the batch and records it as one write, without a boundary.
func (m *MemTransport) Append(entries []Entry) ([]Entry, error) {
	return m.AppendSigned(entries, Boundary{})
}

// AppendSigned is Append with the boundary predicted for its first signed entry.
func (m *MemTransport) AppendSigned(entries []Entry, boundary Boundary) ([]Entry, error) {
	written, err := m.log.append(m.origin, entries, boundary)
	if err != nil {
		return nil, err
	}
	if written == nil {
		return []Entry{}, nil
	}
	return written, nil
}

// Pull returns every other origin's entries past its mark, in order and whole
// batches; an unmarked origin is read from its start.
func (m *MemTransport) Pull(marks map[string]int) ([]Entry, error) {
	return m.log.pull(m.origin, marks), nil
}

// Head returns one origin's latest entry per row.
func (m *MemTransport) Head(origin string) ([]HeadRow, error) {
	return m.log.headOf(origin), nil
}

// Stats reports what the log holds, telling an empty log from a refusing transport.
func (m *MemTransport) Stats() (Stats, error) {
	return m.log.stats(), nil
}

// PublishMarks records this reader's position in each origin given. The reader
// is the transport's own, so one installation's handle cannot write another's
// rows: the rule the remote enforces by key.
func (m *MemTransport) PublishMarks(marks map[string]int, at time.Time) error {
	m.log.publishMarks(m.origin, marks, at)
	return nil
}

// ReadMarks answers with every published mark, compaction floors and sizes.
func (m *MemTransport) ReadMarks() (MarkState, error) {
	return m.log.markState(), nil
}

// PublishTraffic records this installation's traffic row and answers with
// every origin's row. The origin is the transport's own, the same rule
// PublishMarks has.
func (m *MemTransport) PublishTraffic(row TrafficRow) (map[string]TrafficRow, error) {
	return m.log.publishTraffic(m.origin, row), nil
}

// publishMarks writes one reader's rows. A mark only grows, so a lower repeat is
// ignored rather than moving back: a stale publish must not rewind the reader.
func (m *memLog) publishMarks(reader string, marks map[string]int, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()

	held := m.marks[reader]
	if held == nil {
		held = make(map[string]Mark, len(marks))
		m.marks[reader] = held
	}
	for origin, seq := range marks {
		if prior, ok := held[origin]; ok && prior.Seq > seq {
			continue
		}
		held[origin] = Mark{Reader: reader, Origin: origin, Seq: seq, At: at}
	}
}

// markState is every mark the log holds and each origin's size, in reader then
// origin order for a stable walk.
func (m *memLog) markState() MarkState {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out MarkState
	readers := make([]string, 0, len(m.marks))
	for reader := range m.marks {
		readers = append(readers, reader)
	}
	sort.Strings(readers)
	for _, reader := range readers {
		held := m.marks[reader]
		origins := make([]string, 0, len(held))
		for origin := range held {
			origins = append(origins, origin)
		}
		sort.Strings(origins)
		for _, origin := range origins {
			out.Marks = append(out.Marks, held[origin])
		}
	}
	for origin, floor := range m.floors {
		if out.Floors == nil {
			out.Floors = make(map[string]int)
		}
		out.Floors[origin] = floor
	}
	if len(m.signedFrom) > 0 {
		out.SignedFrom = make(map[string]int, len(m.signedFrom))
		for origin, seq := range m.signedFrom {
			out.SignedFrom[origin] = seq
		}
		out.SignedTags = make(map[string][]byte, len(m.signedTag))
		for origin, tag := range m.signedTag {
			out.SignedTags[origin] = tag
		}
	}
	if len(m.epochCut) > 0 {
		out.EpochCuts = make(map[string]EpochCut, len(m.epochCut))
		for origin, cut := range m.epochCut {
			out.EpochCuts[origin] = cut
		}
	}
	out.Heads = make(map[string]int)
	out.Entries = make(map[string]int)
	out.Sizes = make(map[string]int64)
	for _, e := range m.entries {
		out.Sizes[e.Origin] += int64(len(e.Body)) + LogRowOverhead
		if e.Seq > out.Heads[e.Origin] {
			out.Heads[e.Origin] = e.Seq
		}
		out.Entries[e.Origin]++
	}
	return out
}

// publishTraffic overwrites one origin's row and reads every row back.
func (m *memLog) publishTraffic(origin string, row TrafficRow) map[string]TrafficRow {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.traffic == nil {
		m.traffic = make(map[string]TrafficRow)
	}
	m.traffic[origin] = row
	out := make(map[string]TrafficRow, len(m.traffic))
	for other, held := range m.traffic {
		out[other] = held
	}
	return out
}

// MemBlobMover is the BlobMover tests run bodies through, over the same
// MemBlobStore the store's tests use. It stands where the supervisor's pipe
// client stands, so a test exercises the staging round trip without a worker,
// pipe or bucket.
// Its injectable failure pins that a body is in the store before the entry naming
// it is appended, which only a refusing mover can show.
type MemBlobMover struct {
	store *blobstore.MemBlobStore
	// mu guards the counters: a batch's fetches run side by side.
	mu sync.Mutex
	// Err is what PutBlob returns instead of storing. Nil puts.
	Err error
	// GetErr is what GetBlob returns instead of fetching. Nil fetches.
	GetErr error
	// Puts counts stored bodies and Gets fetched ones, so a test can tell an
	// upload-free batch without reading the store.
	Puts int
	Gets int
}

// NewMemBlobMover returns a mover over an empty in-memory store.
func NewMemBlobMover() *MemBlobMover {
	return &MemBlobMover{store: blobstore.NewMemBlobStore(nil)}
}

// Store is the store underneath, for reading what a run left behind.
func (m *MemBlobMover) Store() *blobstore.MemBlobStore { return m.store }

// PutBlob stores the body staged at stagingPath under key. It works through the
// staging file like the real mover, so a never-written file surfaces as failure.
func (m *MemBlobMover) PutBlob(key, stagingPath string) (int64, bool, error) {
	if m.Err != nil {
		return 0, false, m.Err
	}
	body, err := os.ReadFile(stagingPath)
	if err != nil {
		return 0, false, fmt.Errorf("synclog: mem mover read %s: %w", stagingPath, err)
	}
	ctx := context.Background()
	if err := m.store.Put(ctx, key, bytes.NewReader(body), int64(len(body))); err != nil {
		return 0, false, err
	}
	m.mu.Lock()
	m.Puts++
	m.mu.Unlock()
	return int64(len(body)), false, nil
}

// GetBlob writes the body under key to stagingPath, or reports the store's
// ErrNotFound, which the importer latches rather than retries.
func (m *MemBlobMover) GetBlob(key, stagingPath string, max int64) (int64, error) {
	m.mu.Lock()
	m.Gets++
	m.mu.Unlock()
	if m.GetErr != nil {
		return 0, m.GetErr
	}
	f, err := os.Create(stagingPath)
	if err != nil {
		return 0, fmt.Errorf("synclog: mem mover create %s: %w", stagingPath, err)
	}
	defer func() { _ = f.Close() }()
	var body bytes.Buffer
	if _, err := m.store.Get(context.Background(), key, &body); err != nil {
		return 0, err
	}
	if int64(body.Len()) > max {
		return 0, fmt.Errorf("synclog: mem mover get %s: %d bytes, past %d", key, body.Len(), max)
	}
	n, err := f.Write(body.Bytes())
	return int64(n), err
}

// tableIndex is a table's position in SharedTables, so head rows come back
// parents-first like a local walk. An unknown name sorts last, never displacing
// a parent.
func tableIndex(tbl string) int {
	order := SharedTablesInOrder()
	for i, name := range order {
		if name == tbl {
			return i
		}
	}
	return len(order)
}
