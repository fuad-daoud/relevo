package synclog

// Which stored values leave the log, and the one rule deciding it. Both entry
// writers ask refColumns and upload before appending, so the rule lives once here.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/blobstore"
	"github.com/fuad-daoud/relevo/internal/db"
)

// BlobMover is where a body goes when it stops travelling through the log: the
// daemon's pipe client in production, a MemBlobStore mover in tests, so neither
// writer needs a bucket to pin its behaviour. A mover works through a staging file
// since the pipe carries requests, not bodies, and it moves them in batches: one
// call per chunk of a batch, so a worker round trip is paid per chunk rather than
// per body.
type BlobMover interface {
	// PutBlobs uploads the staged bodies under their keys. Results answer every
	// item, in item order; skipped is true for a key whose body the store already
	// held under the same digest: not a failure, the entry may still be appended.
	PutBlobs(items []BlobBatchItem) ([]BlobBatchResult, error)
	// GetBlobs writes the bodies under the items' keys to the staging files the
	// items name, refusing one longer than its item's max before it is all
	// downloaded.
	GetBlobs(items []BlobBatchItem) ([]BlobBatchResult, error)
}

// BlobBatchItem is one body of a mover batch: the object key, the staging file it
// is read from or written to, and for a fetch the most bytes it may be -- the
// length the ref declares. A put ignores max, since the staged file's own length
// is what it moves.
type BlobBatchItem struct {
	Key     string
	Staging string
	Max     int64
}

// BlobBatchResult is what one key of a mover batch came to: the bytes it moved,
// whether a put skipped it because the store already held the body, and the
// reason it did not move when it failed. Every item gets a verdict even when a
// sibling fails: one key's refusal is no verdict on the rest of its batch.
type BlobBatchResult struct {
	Key     string
	Bytes   int64
	Skipped bool
	Err     error
}

// errNoVerdict reports a mover that answered a batch without answering any of
// its keys. Bodies are in the store or they are not, so a call that left that
// unknown cannot be treated as one that succeeded.
var errNoVerdict = errors.New("synclog: the mover answered the batch with no verdict per key")

// The bounds of one mover call. Sixteen keys and 64 MiB of declared body bytes
// keep a call well inside the pipe's data-call bound, and bound the worker's
// memory because its store buffers each whole body; the key bound stops a long
// batch of small bodies from arriving as one request that outstays it.
const (
	moveBatchMaxKeys  = 16
	moveBatchMaxBytes = 64 << 20
)

// batchBody is one body's shape for chunking: the object key and the bytes it
// declares. Chunking reads nothing but these, so the split is one function both
// directions and their tests share rather than two rules that can drift.
type batchBody struct {
	key  string
	size int64
}

// batchChunk splits a batch's distinct bodies into mover calls of at most
// moveBatchMaxKeys keys and moveBatchMaxBytes declared bytes, in the order the
// bodies were given. A body bigger than the whole byte cap travels alone: no
// other body fits beside it without breaking the cap. Such a body cannot
// actually reach a batch -- the per-row ceiling (MaxBlobBytes) equals the cap,
// and rows and refs are checked against it first -- so the alone rule is what
// keeps the split honest for the sizes a caller declares, not a lane a live
// upload takes.
func batchChunk(bodies []batchBody) [][]int {
	var chunks [][]int
	var cur []int
	var curBytes int64
	for n, b := range bodies {
		if len(cur) > 0 && (len(cur) == moveBatchMaxKeys || curBytes+b.size > moveBatchMaxBytes) {
			chunks = append(chunks, cur)
			cur, curBytes = nil, 0
		}
		cur = append(cur, n)
		curBytes += b.size
	}
	if len(cur) > 0 {
		chunks = append(chunks, cur)
	}
	return chunks
}

// BlobTransport is a LogTransport that can also move bodies. Exporter and
// reconciler take their mover from the handed transport, so the entry numberer is
// the body storer.
// The staging folder is part of the contract: a mover with nowhere to write is
// unusable, so a folderless transport moves nothing.
type BlobTransport interface {
	LogTransport
	BlobMover
	// BlobStagingDir is the folder bodies are written to before they are put.
	BlobStagingDir() string
}

// blobWiring is the mover and staging folder one transport offers, or no mover at
// all. A non-BlobTransport moves nothing, and every value it writes travels
// inline as before bodies could move.
func blobWiring(t LogTransport) (BlobMover, string) {
	bt, ok := t.(BlobTransport)
	if !ok {
		return nil, ""
	}
	dir := bt.BlobStagingDir()
	if dir == "" {
		return nil, ""
	}
	return bt, dir
}

// blobColumn is one movable column and its codec column. They are named together
// because a ref carries the codec: the importer must know plain from zstd frame
// before restoring bytes, without a schema it may not have.
type blobColumn struct {
	table  string
	column string
	codec  string
}

// blobColumns is every column whose large value moves out of the log, and the whole
// list: one addition point for every entry writer and the orphan sweeper. It holds
// only bulk-byte columns: names or counters never exceed the threshold, so listing
// them would cost a lookup per row to learn that.
var blobColumns = []blobColumn{
	{table: "round_file", column: "body", codec: "body_codec"},
	{table: "transcript", column: "record_json", codec: "record_json_codec"},
	{table: "transcript", column: "rendered", codec: "rendered_codec"},
}

// columnsFor returns the moving columns of one table; a table with none writes
// entries as before the rule.
func columnsFor(table string) []blobColumn {
	var out []blobColumn
	for _, c := range blobColumns {
		if c.table == table {
			out = append(out, c)
		}
	}
	return out
}

// pendingBlob is one value that must be in the store before the entry naming it
// may be appended. Bytes are held, not the scratch staging file that is removed
// once the mover answers.
type pendingBlob struct {
	Key   string
	Bytes []byte
	Ref   BlobRef
	// repair marks a body for a headed row rather than a pending entry.
	repair bool
}

// refColumns rewrites an eligible []byte value over BlobRefThreshold into a
// RefValue and returns the values to upload before appending.
// The row is returned rather than edited: the caller holds the drained row inside a
// transaction, and a body from another owner's row would be the wrong bytes. The
// copy is shallow with columns replaced whole, so moved values are gone, not nil.
// An unmoved row is left as it was.
func refColumns(origin, table string, row db.ExchangeRow) (db.ExchangeRow, []pendingBlob) {
	cols := columnsFor(table)
	if len(cols) == 0 {
		return row, nil
	}
	out := row
	out.Columns = make([]db.ExchangeColumn, len(row.Columns))
	copy(out.Columns, row.Columns)

	codecs := rowCodecs(out.Columns)
	var pending []pendingBlob
	for _, c := range cols {
		i := columnIndex(out.Columns, c.column)
		if i < 0 {
			continue
		}
		stored, ok := out.Columns[i].Value.([]byte)
		if !ok || !NeedsRef(stored) {
			continue
		}
		ref := RefFor(stored, codecs[c.codec])
		out.Columns[i].Value = RefValue{BlobRef: ref}
		pending = append(pending, pendingBlob{
			Key:   blobstore.Key(origin, ref.SHA256),
			Bytes: stored,
			Ref:   ref,
		})
	}
	return out, pending
}

// rowCodecs reads every codec column of one row by name, from the row rather than
// assumed: the ref must describe stored bytes, plain or frame. A missing codec
// column reads as plain, the value of an uncompressed file.
func rowCodecs(columns []db.ExchangeColumn) map[string]int {
	codecs := make(map[string]int, len(columns))
	for _, col := range columns {
		if v, ok := col.Value.(int64); ok {
			codecs[col.Name] = int(v)
		}
	}
	return codecs
}

// columnIndex returns where name sits in columns, -1 when absent.
func columnIndex(columns []db.ExchangeColumn, name string) int {
	for i, col := range columns {
		if col.Name == name {
			return i
		}
	}
	return -1
}

// uploadPending stages every distinct pending body and puts them in bounded
// batches, in batch order. Bodies are deduped by digest before staging: staging
// files are named by digest, so two equal bodies would otherwise collide on disk
// within one batch -- the same reason fetchAll dedupes downloads.
func uploadPending(m BlobMover, stagingDir string, blobs []pendingBlob) error {
	uniq := make([]pendingBlob, 0, len(blobs))
	seen := make(map[string]bool, len(blobs))
	for _, b := range blobs {
		if seen[b.Ref.SHA256] {
			continue
		}
		seen[b.Ref.SHA256] = true
		uniq = append(uniq, b)
	}
	if len(uniq) == 0 {
		return nil
	}
	bodies := make([]batchBody, len(uniq))
	for n, b := range uniq {
		bodies[n] = batchBody{key: b.Key, size: int64(len(b.Bytes))}
	}
	for _, chunk := range batchChunk(bodies) {
		if err := uploadChunk(m, stagingDir, uniq, chunk); err != nil {
			return err
		}
	}
	return nil
}

// uploadChunk stages one chunk's bodies, puts them, and removes every staged
// file whether the put lands or not: a failed put appends no entry, so the file
// would only fill the folder with untaken bodies. The first failed key decides
// the batch's error, and the call's own error rides with it -- a refusal the
// mover's accounting could not record is still a failure the caller hears. A
// removal failing after the put landed is not a failed export.
func uploadChunk(m BlobMover, stagingDir string, uniq []pendingBlob, chunk []int) error {
	items := make([]BlobBatchItem, len(chunk))
	for n, i := range chunk {
		b := uniq[i]
		path := filepath.Join(stagingDir, b.Ref.SHA256)
		// The folder holds bodies existing nowhere else until the put lands, so
		// the file is owner-only even inside the 0700 folder: it outlives this
		// call, and its mode is what a folder listing shows.
		if err := os.WriteFile(path, b.Bytes, 0o600); err != nil {
			for _, staged := range items[:n] {
				_ = os.Remove(staged.Staging)
			}
			return fmt.Errorf("synclog: stage %s: %w", path, err)
		}
		items[n] = BlobBatchItem{Key: b.Key, Staging: path}
	}
	results, callErr := m.PutBlobs(items)
	for _, item := range items {
		_ = os.Remove(item.Staging)
	}
	if len(results) != len(items) {
		if callErr == nil {
			callErr = errNoVerdict
		}
		return fmt.Errorf("synclog: upload %s: %w", uniq[chunk[0]].Key, callErr)
	}
	for n, r := range results {
		if r.Err != nil {
			return fmt.Errorf("synclog: upload %s: %w",
				uniq[chunk[n]].Key, errors.Join(callErr, r.Err))
		}
	}
	if callErr != nil {
		return fmt.Errorf("synclog: upload %s: %w", uniq[chunk[0]].Key, callErr)
	}
	return nil
}

// checkBlobSizes refuses a row holding a value past MaxBlobBytes. Every importer
// refuses that large a ref, so exporting it would drop the row on every other
// machine; the export stops on it instead, naming the row.
func checkBlobSizes(table, pk string, blobs []pendingBlob) error {
	for _, b := range blobs {
		if len(b.Bytes) > MaxBlobBytes {
			return fmt.Errorf("synclog: %s %s holds a %d-byte value, past the %d bytes sync moves: %w",
				table, pk, len(b.Bytes), MaxBlobBytes, ErrInvalid)
		}
	}
	return nil
}

// pendingBytes is the distinct-object length a batch uploads, as an importer counts
// against MaxBatchBlobBytes.
func pendingBytes(blobs []pendingBlob) int64 {
	seen := make(map[string]bool, len(blobs))
	var total int64
	for _, b := range blobs {
		if !seen[b.Key] {
			seen[b.Key] = true
			total += int64(len(b.Bytes))
		}
	}
	return total
}
