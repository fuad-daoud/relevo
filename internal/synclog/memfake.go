package synclog

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fuad-daoud/relevo/internal/blobstore"
)

// MemTransport is the in-memory LogTransport every test in this slice runs
// against. It is one installation's handle on the log: it appends as its own
// origin, reads what other origins wrote, and numbers what it appends from the
// log's own high-water mark rather than from a counter it kept, so a machine
// whose local file was restored from a backup cannot hand out a number the log
// already holds.
//
// It holds the three things the log is defined as -- the entries themselves, the
// per-row latest state, and the sequence each origin reached -- and writes each
// append as one unit, so a reader can see where a batch ended. It is the
// specification the exporter and importer are written against, so where the two
// could disagree about seq assignment, mark filtering or head maintenance, the
// disagreement shows up here first.
type MemTransport struct {
	origin string
	log    *memLog
	// pushes and pulls count the transfers this handle completed, mirroring
	// the worker's engine-transfer counters. They are per handle rather than
	// per log because each installation moves its own bytes; atomics because
	// nothing else here serializes one handle's calls.
	pushes atomic.Int64
	pulls  atomic.Int64
}

// memLog is the log itself: the entries in append order, each origin's latest
// state per row, the sequence each origin reached, and the marks readers
// published. It is separate from the transport so two installations can hold
// the same log, which is the arrangement the exchange actually runs in.
type memLog struct {
	mu      sync.Mutex
	entries []Entry
	head    map[string][]HeadRow
	// marks is keyed by reader and then origin, so a memLog built for one
	// reader records only that reader's rows and a second handle on the same
	// log sees them -- which is the rule the remote enforces by key, reproduced
	// here rather than by a transport that checks who asked.
	marks map[string]map[string]Mark
	// floors is what each origin has compacted to.
	floors map[string]int
	// signedFrom is the first sequence of each origin that carried a MAC.
	signedFrom map[string]int
	// signedTag is the tag stored with each signedFrom entry.
	signedTag map[string][]byte
	// key is the passphrase material the log holds.
	key KeyMeta
	// epochCut is the cut each origin's history is fenced at, as a rekey
	// wrote it. A row is one origin's, and the newest rekey's row is the one
	// that stands.
	epochCut map[string]EpochCut
	// claims is the adoption claim each identity holds.
	claims map[string]claimRow
}

// NewMemTransport returns a handle on an empty log for the installation named
// origin. Every other origin it is handed entries for is a machine elsewhere:
// it will read their entries and refuse to append as them.
func NewMemTransport(origin string) *MemTransport {
	return &MemTransport{origin: origin, log: newMemLog()}
}

// newMemLog is an empty log.
func newMemLog() *memLog {
	return &memLog{head: make(map[string][]HeadRow), marks: make(map[string]map[string]Mark)}
}

// OnLog returns a second installation's handle on the same log, which is how two
// machines reach one remote: each has its own origin and its own sequence, and
// each reads the other's entries.
func (m *MemTransport) OnLog(origin string) *MemTransport {
	return &MemTransport{origin: origin, log: m.log}
}

// append records a batch as one write, numbering the entries from one past the
// highest sequence this origin already holds. Everything that can fail is checked
// before anything is written, so a refused batch leaves the log as it was.
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

	// max+1 rather than a counter: a machine restored from a backup arrives with
	// entries it never wrote and its own numbering would start at zero again.
	// Reading the log's highest number is what keeps the two from handing out
	// the same one.
	next := m.latestSeq(origin) + 1
	if err := m.checkBoundary(origin, entries, next, boundary); err != nil {
		return nil, err
	}
	written := make([]Entry, 0, len(entries))
	for i, e := range entries {
		e.Origin = origin
		e.Seq = next + i
		// Every entry of one append carries the sequence number of its first,
		// so a reader can find where the batch ended and apply it whole.
		e.Batch = next
		m.entries = append(m.entries, e)
		m.writeHead(e, hashes[i])
		written = append(written, e)
	}
	m.recordSignedFrom(written, boundary)
	return written, nil
}

// pull returns every origin's entries past its mark, in sequence order within
// each origin and in whole batches. A batch that starts at or before the mark is
// skipped whole rather than trimmed: the importer applies whole batches, and
// handing it half of one would apply an order the exporter never wrote.
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

// head returns one origin's latest state per row, in table order then key order
// so a reader walking the pages sees the same sequence every time.
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

// stats reports the log's size: how many entries it holds, how many machines
// have written to it, and the highest sequence number in it.
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

// latestSeq is the highest sequence the log holds for origin, and zero when it
// holds none.
func (m *memLog) latestSeq(origin string) int {
	high := 0
	for _, e := range m.entries {
		if e.Origin == origin && e.Seq > high {
			high = e.Seq
		}
	}
	return high
}

// writeHead moves one origin's latest state for the row the entry names. An
// upsert replaces the row's entry, so `head` holds the last state the log
// recorded for it; a delete drops it, because a row the origin has deleted is
// not a row reconcile should keep proposing.
func (m *memLog) writeHead(e Entry, hash string) {
	if e.Op == OpDelete {
		m.head[e.Origin] = removeHead(m.head[e.Origin], e.Table, e.PK)
		return
	}
	m.head[e.Origin] = replaceHead(m.head[e.Origin], e, hash)
}

// replaceHead records e as the latest state of its row, keeping the list's
// length and order so the caller does not have to sort to update it.
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

// removeHead drops the row from an origin's latest state, which is what a delete
// means for a row that origin still listed.
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

// origins is every origin the log holds entries for, in the order the first of
// each appears.
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

// originsOtherThan is the origins a pull has entries to return: everyone but the
// caller, because a machine's own writes are already in its file and reading them
// back would apply them twice.
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

// Append numbers the batch and records it as one write.
func (m *MemTransport) Append(entries []Entry) ([]Entry, error) {
	return m.AppendSigned(entries, Boundary{})
}

// AppendSigned is Append with the boundary the exporter predicted for its first
// signed entry.
func (m *MemTransport) AppendSigned(entries []Entry, boundary Boundary) ([]Entry, error) {
	written, err := m.log.append(m.origin, entries, boundary)
	if err != nil {
		return nil, err
	}
	// An empty append moves nothing, as on the worker where it returns before
	// the push, so it counts no transfer.
	if len(entries) > 0 {
		m.pushes.Add(1)
	}
	if written == nil {
		return []Entry{}, nil
	}
	return written, nil
}

// Pull returns the entries of every origin but this one that follow the mark
// given for it, in sequence order and in whole batches. An origin with no mark
// is read from the start of its log.
func (m *MemTransport) Pull(marks map[string]int) ([]Entry, error) {
	m.pulls.Add(1)
	return m.log.pull(m.origin, marks), nil
}

// Head returns the latest entry per row of one origin: the row, the sequence
// number of the entry that last wrote it, and that entry's body hash.
func (m *MemTransport) Head(origin string) ([]HeadRow, error) {
	return m.log.headOf(origin), nil
}

// Stats reports what the log holds, so a caller can tell an empty log from a
// transport that is refusing to answer.
func (m *MemTransport) Stats() (Stats, error) {
	stats := m.log.stats()
	stats.TursoPushes = int(m.pushes.Load())
	stats.TursoPulls = int(m.pulls.Load())
	return stats, nil
}

// PublishMarks records this reader's position in each origin given. The reader
// is the transport's own, so a handle built for one installation cannot write
// another's rows -- the rule the remote enforces by key, which a test cannot
// exercise without two machines.
func (m *MemTransport) PublishMarks(marks map[string]int, at time.Time) error {
	// A publish of nothing writes nothing, as on the worker where it returns
	// before the push, so it counts no transfer.
	if len(marks) > 0 {
		m.pushes.Add(1)
	}
	m.log.publishMarks(m.origin, marks, at)
	return nil
}

// ReadMarks answers with every published mark, the floors compaction recorded
// and each origin's size.
func (m *MemTransport) ReadMarks() (MarkState, error) {
	return m.log.markState(), nil
}

// publishMarks writes one reader's rows. A mark only grows, so a repeat that
// named a lower sequence is ignored rather than moving the row back: a mark is
// read as the furthest a reader has got, and a stale publish must not make it
// look as though the reader went backwards.
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
// origin order so a reader walking the answer sees the same row every time.
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

// MemBlobMover is the BlobMover every test in this slice runs bodies through,
// over the same MemBlobStore the store's own tests use. It stands where the
// supervisor's pipe client stands, so a test exercises the real staging round
// trip -- write the file, read it back, remove it -- without a worker, a pipe
// or a bucket.
//
// The failure it can be told to return is the point of it: the rule being pinned
// is that a body is in the store before the entry naming it is appended, and
// that can only be shown by a mover which refuses.
type MemBlobMover struct {
	store *blobstore.MemBlobStore
	// mu guards the counters, because a batch's fetches run side by side.
	mu sync.Mutex
	// Err is what PutBlob returns instead of storing. Nil puts.
	Err error
	// GetErr is what GetBlob returns instead of fetching. Nil fetches.
	GetErr error
	// Puts counts the bodies it was asked to store and Gets the ones it
	// fetched, so a test can say a batch uploaded nothing rather than reading
	// the store to find out.
	Puts int
	Gets int
}

// NewMemBlobMover returns a mover over an empty in-memory store.
func NewMemBlobMover() *MemBlobMover {
	return &MemBlobMover{store: blobstore.NewMemBlobStore(nil)}
}

// Store is the store underneath, so a test reads the objects a run left behind.
func (m *MemBlobMover) Store() *blobstore.MemBlobStore { return m.store }

// PutBlob stores the body staged at stagingPath under key. It works through the
// staging file rather than through the caller's bytes, which is what the real
// mover does and what makes a staging file that was never written visible here
// as the failure it is.
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
// ErrNotFound, which the importer latches on rather than retrying.
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

// tableIndex is a table's position in SharedTables, so head rows come back in
// the same parents-first order a local walk uses. A name no shared table carries
// sorts last: it cannot be a row this machine reads, and putting it first would
// let an unknown name displace a parent.
func tableIndex(tbl string) int {
	order := SharedTablesInOrder()
	for i, name := range order {
		if name == tbl {
			return i
		}
	}
	return len(order)
}
