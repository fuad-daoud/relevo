package synclog

// Turning the refs in a batch back into the stored bytes they name, before the
// batch's transaction opens. A fetch is network and can be slow, so it happens
// outside the transaction; and every check on what came back happens before any
// row is written, so a body that fails one leaves the file and the mark where
// they were.

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/fuad-daoud/relevo/internal/blobstore"
)

// fetchWorkers bounds the fetches in flight for one batch. Four keeps a batch of
// many large bodies from serialising on round trips without opening a connection
// per body.
const fetchWorkers = 4

// ErrBlobMismatch reports a fetched object that is not the one its ref names:
// the wrong length or the wrong digest. It is transient on purpose -- a store
// that served a truncated or tampered object may serve the right one next time --
// so the origin is held and retried rather than latched or applied.
var ErrBlobMismatch = errors.New("synclog: the fetched body is not the one its ref names")

var errNoMover = errors.New("synclog: this machine has no blob store to fetch a body from")

// missingBlob is a ref whose object the store does not hold. It carries the
// entry so the importer can name the row, which the bare sentinel cannot.
type missingBlob struct {
	entry  Entry
	column string
}

func (m *missingBlob) Error() string {
	return fmt.Sprintf("synclog: %s %s column %s: %s", m.entry.Table, m.entry.PK, m.column, ErrBlobMissing)
}

func (m *missingBlob) Unwrap() error { return ErrBlobMissing }

// Stall is one origin held for a run because a body its batch refers to could not
// be fetched and checked. Nothing of the batch applied and the mark did not move,
// so the next attempt reads the same batch again.
type Stall struct {
	Origin string
	// Label is the name that installation's own file carries.
	Label  string
	Reason string
}

// String is the report a stall reads as: the installation and why it waits.
func (s Stall) String() string {
	return fmt.Sprintf("%s is held: a body it refers to could not be fetched, %s; it is retried on the next attempt",
		s.Label, s.Reason)
}

// stallError carries a stall up from the fetch to the loop that records it.
type stallError struct {
	origin string
	cause  error
}

func (s *stallError) Error() string { return s.cause.Error() }

func (s *stallError) Unwrap() error { return s.cause }

// refSlot is one ref in a batch: the entry that holds it, and the column it
// stands in for.
type refSlot struct {
	entry  int
	column blobColumn
	ref    BlobRef
}

// resolveRefs replaces every ref in the batch's bodies with the stored bytes it
// names. An object the store does not hold is a *missingBlob; a fetched body
// that fails its length or digest check is ErrBlobMismatch; a ref whose codec
// disagrees with the row's own codec column is ErrInvalid, because the entry
// contradicts itself and no fetch can fix that.
func resolveRefs(m BlobMover, stagingDir string, batch []Entry) error {
	fields := make(map[int]map[string]json.RawMessage)
	slots, err := refSlots(batch, fields)
	if err != nil || len(slots) == 0 {
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

// refSlots finds the refs in a batch and checks each against the row's codec
// column. A body that does not parse is left for applyEntry to refuse, which
// names it properly.
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

// codecOf reads a codec column out of a body. An absent column is plain, as in a
// file that predates the column; a value that is not a whole number reads as -1,
// which no ref carries.
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

// fetchAll fetches each distinct object once, four at a time, and returns the
// bytes per slot. Two slots naming the same object share one fetch, because both
// would otherwise stage under the same file name at once.
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
	got := make([][]byte, len(order))
	errs := make([]error, len(order))
	sem := make(chan struct{}, fetchWorkers)
	var wg sync.WaitGroup
	for n, key := range order {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			got[n], errs[n] = fetchOne(m, stagingDir, key, slots[first[key]].ref)
		}()
	}
	wg.Wait()
	byKey := make(map[string][]byte, len(order))
	for n, key := range order {
		if errs[n] != nil {
			return nil, failedFetch(batch, slots[first[key]], errs[n])
		}
		byKey[key] = got[n]
	}
	out := make([][]byte, len(slots))
	for n, s := range slots {
		out[n] = byKey[blobstore.Key(batch[s.entry].Origin, s.ref.SHA256)]
	}
	return out, nil
}

// failedFetch makes a missing object a *missingBlob that names the row, and
// leaves every other failure as it was.
func failedFetch(batch []Entry, s refSlot, err error) error {
	if errors.Is(err, ErrBlobMissing) {
		return &missingBlob{entry: batch[s.entry], column: s.column.column}
	}
	return err
}

// fetchOne stages one object, checks its length and digest, and returns its
// bytes. The staging file is removed whatever the outcome.
func fetchOne(m BlobMover, stagingDir, key string, ref BlobRef) ([]byte, error) {
	path := filepath.Join(stagingDir, ref.SHA256)
	defer func() { _ = os.Remove(path) }()
	if _, err := m.GetBlob(key, path); err != nil {
		if errors.Is(err, ErrBlobMissing) || errors.Is(err, blobstore.ErrNotFound) {
			return nil, fmt.Errorf("synclog: fetch %s: %w", key, ErrBlobMissing)
		}
		return nil, fmt.Errorf("synclog: fetch %s: %w", key, err)
	}
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

// fetchAndApply resolves a batch's refs and applies it. What the fetch can say
// decides what the batch becomes: a missing object ends the run with a message
// naming the row, a self-contradicting ref is a refusal and so dropped, and
// anything else holds the origin with nothing applied.
func (i *Importer) fetchAndApply(batch []Entry, marks map[string]int) (int, error) {
	err := resolveRefs(i.mover, i.staging, batch)
	var missing *missingBlob
	switch {
	case err == nil:
		return i.applyEntries(batch, marks)
	case errors.As(err, &missing):
		return 0, i.missingError(missing)
	case refused(err):
		return 0, err
	default:
		return 0, &stallError{origin: batch[0].Origin, cause: err}
	}
}

// missingError names the row whose body is gone: for a round file the binding,
// round and file, for a transcript the owner and seq, and always the origin's
// label, because the fix is to restore the object or re-export from that machine.
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

// describeRow says which row an entry names in the terms a reader knows.
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
