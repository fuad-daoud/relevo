package synclog

// Which stored values leave the log, and the one rule deciding it. Both entry
// writers ask refColumns and upload before appending, so the rule lives once here.

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/blobstore"
	"github.com/fuad-daoud/relevo/internal/db"
)

// BlobMover is where a body goes when it stops travelling through the log: the
// daemon's pipe client in production, a MemBlobStore mover in tests, so neither
// writer needs a bucket to pin its behaviour. A mover works through a staging file
// since the pipe carries requests, not bodies.
type BlobMover interface {
	// PutBlob uploads the body at staging under key. skipped is true when the
	// store already held it under the same digest: not a failure, the entry may
	// still be appended.
	PutBlob(key, staging string) (bytes int64, skipped bool, err error)
	// GetBlob writes the body under key to staging, refusing one longer than
	// max before it is all downloaded.
	GetBlob(key, staging string, max int64) (int64, error)
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

// uploadPending writes each value to the staging folder and hands it to the
// mover, in batch order. The file is written under the digest the key names and
// removed whether the put lands or not: a failed put appends no entry, so the
// file would only fill the folder with untaken bodies.
func uploadPending(m BlobMover, stagingDir string, blobs []pendingBlob) error {
	for _, b := range blobs {
		if err := uploadOne(m, stagingDir, b); err != nil {
			return err
		}
	}
	return nil
}

// uploadOne stages one value and puts it. A removal failing after a successful put
// is not a failed export.
func uploadOne(m BlobMover, stagingDir string, b pendingBlob) error {
	path := filepath.Join(stagingDir, b.Ref.SHA256)
	// The folder holds bodies existing nowhere else until the put lands, so the
	// file is owner-only: it outlives this call inside a 0700 folder.
	if err := os.WriteFile(path, b.Bytes, 0o600); err != nil {
		return fmt.Errorf("synclog: stage %s: %w", path, err)
	}
	_, _, putErr := m.PutBlob(b.Key, path)
	_ = os.Remove(path)
	if putErr != nil {
		return fmt.Errorf("synclog: upload %s: %w", b.Key, putErr)
	}
	return nil
}

// checkBlobSizes refuses a row holding a value past MaxBlobBytes. Every importer
// refuses that large a ref, so the export stops on it, naming the row.
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
