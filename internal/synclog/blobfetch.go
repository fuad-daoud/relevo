package synclog

// Refs become bytes before the batch's transaction opens: a fetch is network
// and can be slow, and every check runs before any row is written, so a body
// failing one leaves the file and the mark where they were.

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/blobstore"
)

// ErrBlobMismatch reports a fetched object that is not the one its ref names.
// It is transient on purpose -- a store that served a truncated or tampered
// object may serve the right one next time -- so the origin is held and retried
// rather than latched or applied.
var ErrBlobMismatch = errors.New("synclog: the fetched body is not the one its ref names")

var errNoMover = errors.New("synclog: this machine has no blob store to fetch a body from")

// missingBlob is a ref whose object the store does not hold, carrying the
// entry so the importer can name the row.
type missingBlob struct {
	entry  Entry
	column string
}

func (m *missingBlob) Error() string {
	return fmt.Sprintf("synclog: %s %s column %s: %s", m.entry.Table, m.entry.PK, m.column, ErrBlobMissing)
}

func (m *missingBlob) Unwrap() error { return ErrBlobMissing }

// Stall is one origin held for a run: a body its batch refers to could not be
// fetched and checked, or the snapshot up to its floor could not be applied.
// Nothing of it applied and the mark did not move, so the next attempt reads the
// same entries again. Reason says which.
type Stall struct {
	Origin string
	// Label is the name that installation's own file carries.
	Label  string
	Reason string
}

func (s Stall) String() string {
	return fmt.Sprintf("%s is held: %s; it is retried on the next attempt", s.Label, s.Reason)
}

// stallError carries a stall to the loop recording it.
type stallError struct {
	origin string
	cause  error
}

func (s *stallError) Error() string { return s.cause.Error() }

func (s *stallError) Unwrap() error { return s.cause }

// refSlot is one ref in a batch: its entry and the column it stands in for.
type refSlot struct {
	entry  int
	column blobColumn
	ref    BlobRef
}

// resolveRefs replaces every ref with the bytes it names: a missing object is a
// *missingBlob, a failed check ErrBlobMismatch, a codec disagreeing with the row
// ErrInvalid, since the entry contradicts itself and no fetch can fix that.
func resolveRefs(m BlobMover, stagingDir string, batch []Entry) error {
	fields := make(map[int]map[string]json.RawMessage)
	slots, err := refSlots(batch, fields)
	if err != nil || len(slots) == 0 {
		return err
	}
	if err := checkBatchBytes(batch, slots); err != nil {
		return err
	}
	if m == nil || stagingDir == "" {
		return errNoMover
	}
	bodies, err := fetchAll(m, stagingDir, batch, slots)
	if err != nil {
		return err
	}
	for n, s := range slots {
		encoded, err := json.Marshal(map[string]string{blobTag: base64.StdEncoding.EncodeToString(bodies[n])})
		if err != nil {
			return fmt.Errorf("synclog: encode a fetched body: %w", err)
		}
		fields[s.entry][s.column.column] = encoded
	}
	for k, f := range fields {
		body, err := json.Marshal(f)
		if err != nil {
			return fmt.Errorf("synclog: encode %s %s: %w", batch[k].Table, batch[k].PK, err)
		}
		batch[k].Body = body
	}
	return nil
}

// refSlots finds the refs in a batch, checking each against the row's codec
// column. An unparsable body is left for applyEntry to refuse, which names it.
func refSlots(batch []Entry, fields map[int]map[string]json.RawMessage) ([]refSlot, error) {
	var slots []refSlot
	for k, e := range batch {
		cols := columnsFor(e.Table)
		if e.Op != OpUpsert || len(cols) == 0 {
			continue
		}
		var f map[string]json.RawMessage
		if json.Unmarshal(e.Body, &f) != nil {
			continue
		}
		for _, c := range cols {
			raw, ok := f[c.column]
			if !ok {
				continue
			}
			ref, isRef, err := DecodeRef(raw)
			if err != nil {
				return nil, err
			}
			if !isRef {
				continue
			}
			// The origin becomes the first segment of the object key, and a remote
			// entry may carry any string there.
			if err := blobstore.CheckOrigin(e.Origin); err != nil {
				return nil, fmt.Errorf("synclog: %s %s: %w: %w", e.Table, e.PK, err, ErrInvalid)
			}
			if got := codecOf(f, c.codec); got != ref.Codec {
				return nil, fmt.Errorf("synclog: %s %s: %s is %d but its ref says %d: %w",
					e.Table, e.PK, c.codec, got, ref.Codec, ErrInvalid)
			}
			fields[k] = f
			slots = append(slots, refSlot{entry: k, column: c, ref: ref})
		}
	}
	return slots, nil
}

// checkBatchBytes refuses a batch past MaxBatchBlobBytes from declared lengths,
// before anything is fetched, so a forged batch costs no download.
func checkBatchBytes(batch []Entry, slots []refSlot) error {
	sizes := make(map[string]int64, len(slots))
	var total int64
	for _, s := range slots {
		key := blobstore.Key(batch[s.entry].Origin, s.ref.SHA256)
		if _, seen := sizes[key]; seen {
			continue
		}
		sizes[key] = s.ref.Bytes
		total += s.ref.Bytes
	}
	if total > MaxBatchBlobBytes {
		return fmt.Errorf("synclog: a batch from %s names %d bytes of bodies, over the %d one batch may: %w",
			batch[0].Origin, total, MaxBatchBlobBytes, ErrInvalid)
	}
	return nil
}

// codecOf reads a codec column out of a body. Absent is plain, as in a file
// predating the column; non-whole reads as -1, which no ref carries.
func codecOf(f map[string]json.RawMessage, name string) int {
	raw, ok := f[name]
	if !ok {
		return 0
	}
	var n int
	if json.Unmarshal(raw, &n) != nil {
		return -1
	}
	return n
}

// fetchAll fetches each distinct object once, in the mover's bounded chunks,
// one call after another: the pipe carries one request in flight, and the
// worker bounds the transfers inside a batch. Same-object slots share one
// fetch, or both would stage under one file name at once.
func fetchAll(m BlobMover, stagingDir string, batch []Entry, slots []refSlot) ([][]byte, error) {
	var order []string
	first := make(map[string]int)
	for n, s := range slots {
		key := blobstore.Key(batch[s.entry].Origin, s.ref.SHA256)
		if _, seen := first[key]; !seen {
			first[key] = n
			order = append(order, key)
		}
	}
	bodies := make([]batchBody, len(order))
	for n, key := range order {
		bodies[n] = batchBody{key: key, size: slots[first[key]].ref.Bytes}
	}
	got := make([][]byte, len(order))
	for _, chunk := range batchChunk(bodies) {
		if err := fetchChunk(m, stagingDir, batch, slots, first, order, got, chunk); err != nil {
			return nil, err
		}
	}
	out := make([][]byte, len(slots))
	for n, s := range slots {
		out[n] = got[first[blobstore.Key(batch[s.entry].Origin, s.ref.SHA256)]]
	}
	return out, nil
}

// fetchChunk moves one chunk of distinct keys through one batch call and
// reads the bodies back, gating each on the length and digest its ref declares
// before it enters the batch. The staged files are removed whether their body
// passed or not, and decidingFailure picks the key the batch's error names, so a
// repeated import names the same row. The call's own error rides with it, and
// fails a chunk whose keys all answered, since it says the breaker lost a write.
func fetchChunk(m BlobMover, stagingDir string, batch []Entry, slots []refSlot,
	first map[string]int, order []string, got [][]byte, chunk []int) error {
	items := make([]BlobBatchItem, len(chunk))
	for n, i := range chunk {
		ref := slots[first[order[i]]].ref
		items[n] = BlobBatchItem{
			Key:     order[i],
			Staging: filepath.Join(stagingDir, ref.SHA256),
			Max:     ref.Bytes,
		}
	}
	results, callErr := m.GetBlobs(items)
	defer func() {
		for _, item := range items {
			_ = os.Remove(item.Staging)
		}
	}()
	if len(results) != len(items) {
		if callErr == nil {
			callErr = errNoVerdict
		}
		// A whole-call failure answers no key, so it stands against the
		// first one the chunk asked for.
		i := chunk[0]
		return failedFetch(batch, slots[first[order[i]]], fetchWrap(order[i], callErr))
	}
	if n := decidingFailure(results); n >= 0 {
		i := chunk[n]
		return failedFetch(batch, slots[first[order[i]]], fetchWrap(order[i], errors.Join(callErr, results[n].Err)))
	}
	if callErr != nil {
		return fetchWrap(order[chunk[0]], callErr)
	}
	for n, i := range chunk {
		body, err := readStagedRef(order[i], items[n].Staging, slots[first[order[i]]].ref)
		if err != nil {
			return failedFetch(batch, slots[first[order[i]]], err)
		}
		got[i] = body
	}
	return nil
}

// decidingFailure picks the result a failed chunk is reported against, or -1
// when no key failed: the first missing body, since that is the row the importer
// names and the operator must restore, otherwise the first failed key.
func decidingFailure(results []BlobBatchResult) int {
	first := -1
	for n, r := range results {
		if r.Err == nil {
			continue
		}
		if errors.Is(r.Err, ErrBlobMissing) || errors.Is(r.Err, blobstore.ErrNotFound) {
			return n
		}
		if first < 0 {
			first = n
		}
	}
	return first
}

// failedFetch names the row of a missing object, leaving other failures as is.
func failedFetch(batch []Entry, s refSlot, err error) error {
	if errors.Is(err, ErrBlobMissing) {
		return &missingBlob{entry: batch[s.entry], column: s.column.column}
	}
	return err
}

// fetchWrap names the key a fetch was asked under and keeps the deciding
// sentinel: a body the store does not hold reaches ErrBlobMissing whether the
// mover mapped its refusal to that sentinel or only said the object was not
// there, because the importer latches on the sentinel and not the words.
func fetchWrap(key string, err error) error {
	if errors.Is(err, ErrBlobMissing) || errors.Is(err, blobstore.ErrNotFound) {
		return fmt.Errorf("synclog: fetch %s: %w", key, ErrBlobMissing)
	}
	return fmt.Errorf("synclog: fetch %s: %w", key, err)
}

// readStagedRef reads one staged body and gates it against its ref: length
// against the declared bytes before the read, so an over-long object is not
// read into memory to be found out, then the digest.
func readStagedRef(key, path string, ref BlobRef) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("synclog: fetch %s: %w", key, err)
	}
	if info.Size() != ref.Bytes {
		return nil, fmt.Errorf("synclog: fetch %s: %d bytes, the ref says %d: %w", key, info.Size(), ref.Bytes, ErrBlobMismatch)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("synclog: fetch %s: %w", key, err)
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != ref.SHA256 {
		return nil, fmt.Errorf("synclog: fetch %s: the digest is not the ref's: %w", key, ErrBlobMismatch)
	}
	return body, nil
}

// fetchAndApply resolves a batch's refs and applies it: a missing object ends the
// run naming the row, a self-contradicting ref is dropped, anything else holds
// the origin with nothing applied.
func (i *Importer) fetchAndApply(batch, rows []Entry, docs int, marks map[string]int) (int, []Drop, error) {
	err := resolveRefs(i.mover, i.staging, rows)
	var missing *missingBlob
	switch {
	case err == nil:
		return i.applyEntries(batch, rows, docs, marks)
	case errors.As(err, &missing):
		return 0, nil, i.missingError(missing)
	case refused(err):
		return 0, nil, err
	default:
		return 0, nil, &stallError{origin: batch[0].Origin, cause: fmt.Errorf("a body it refers to could not be fetched, %w", err)}
	}
}

// missingError names the row whose body is gone, with the origin's label,
// because the fix is to restore the object or re-export from that machine.
func (i *Importer) missingError(m *missingBlob) error {
	names, err := i.labels()
	if err != nil {
		return err
	}
	label := names[m.entry.Origin]
	if label == "" {
		label = m.entry.Origin
	}
	return fmt.Errorf("synclog: import from %s: %s: its %s is not in the blob store: %w",
		label, i.describeRow(m.entry), m.column, ErrBlobMissing)
}

func (i *Importer) describeRow(e Entry) string {
	values, err := DecodeBody(e.Body)
	if err != nil {
		return fmt.Sprintf("%s %s", e.Table, e.PK)
	}
	switch e.Table {
	case "round_file":
		id, _ := values["record_id"].(string)
		binding := id
		if rec, found, err := i.db.RecordGetByID(id); err == nil && found {
			binding = rec.Name
		}
		return fmt.Sprintf("round_file %q of binding %s, round %v", values["name"], binding, values["round"])
	case "transcript":
		return fmt.Sprintf("transcript of owner %v, seq %v", values["owner_id"], values["seq"])
	default:
		return fmt.Sprintf("%s %s", e.Table, e.PK)
	}
}
