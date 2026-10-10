package synclog

// Which stored values leave the log, and the one rule that decides it. Every
// path that writes an entry -- the exporter's drain and reconcile's walk --
// asks refColumns what to move and uploads what comes back before the entry is
// appended, so the rule lives here rather than being written twice.

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/blobstore"
	"github.com/fuad-daoud/relevo/internal/db"
)

// BlobMover is where a body goes when it stops travelling through the log. The
// daemon's pipe client is the real one; the tests in this package use a mover
// over a MemBlobStore, so neither the exporter nor reconcile needs a bucket to
// exist for its own behaviour to be pinned.
//
// A mover works through a staging file rather than the bytes themselves, because
// the pipe carries requests and not bodies: the value is written beside the
// replica, the worker is told where it is, and nothing over the pipe is large
// enough to need a second framing.
type BlobMover interface {
	// PutBlob uploads the body at staging under key. skipped is true when the
	// store already held it, which is not a failure: another machine stored the
	// same bytes under the same digest, and the entry that refers to the object
	// may still be appended.
	PutBlob(key, staging string) (bytes int64, skipped bool, err error)
	// GetBlob writes the body under key to staging.
	GetBlob(key, staging string) (int64, error)
}

// BlobTransport is a LogTransport that can also move bodies: the daemon's
// supervisor in production, and a fake over a MemBlobStore in a test. The
// exporter and reconcile take their mover from the transport they were handed
// rather than being built a second time, so the object that numbers the entries
// is the object that stores the bodies they point at.
//
// The staging folder is part of the contract rather than a separate argument:
// a mover with nowhere to write a body cannot be used at all, so a transport
// that names no folder is read as one that moves nothing.
type BlobTransport interface {
	LogTransport
	BlobMover
	// BlobStagingDir is the folder bodies are written to before they are put.
	BlobStagingDir() string
}

// blobWiring is the mover and staging folder one transport offers, or no mover
// at all. A transport that is not a BlobTransport -- a plain MemTransport, or a
// log this package talks to without a bucket -- moves nothing, and every value
// it writes travels inline exactly as it did before bodies could move.
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

// blobColumn is one column that can move, and the codec column beside it that
// says what the stored bytes are. The two are named together because a ref
// carries the codec with it: an importer has to know whether the object holds a
// plain value or a zstd frame before it can put the bytes back where the row
// expects them, and that answer travels with the ref rather than being looked up
// in a schema the importer may not have.
type blobColumn struct {
	table  string
	column string
	codec  string
}

// blobColumns is every column whose value moves out of the log when it is large,
// and it is the whole list. The exporter reads it to decide what a batch
// uploads, the cleanup that sweeps unreferenced objects reads it to know which
// values can leave one behind, and a column added to it later has to be added in
// one place rather than in every path that writes an entry.
//
// The list is the columns carrying the bulk of the bytes and nothing else: a
// column whose values are names or counters is never over the threshold, so
// putting it here would cost a lookup per row to learn that.
var blobColumns = []blobColumn{
	{table: "round_file", column: "body", codec: "body_codec"},
	{table: "transcript", column: "record_json", codec: "record_json_codec"},
	{table: "transcript", column: "rendered", codec: "rendered_codec"},
}

// columnsFor returns the moving columns of one table. A table that carries none
// gets none, so an entry for it is written exactly as it was before this rule
// existed.
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
// may be appended. The bytes are held rather than the file they will be written
// to, because the staging file is scratch: it is removed as soon as the mover
// answers, and the value has to survive a batch that moves several of them.
type pendingBlob struct {
	Key   string
	Bytes []byte
	Ref   BlobRef
}

// refColumns rewrites an eligible []byte value over BlobRefThreshold into a
// RefValue and returns the values that must be uploaded before the entry may be
// appended.
//
// The row is returned rather than edited: the caller holds the drained row it
// read inside a transaction, and a body built from a row someone else still owns
// would be a body built from the wrong bytes. The copy is shallow and the
// columns are replaced whole, which is why the values that move are gone from
// the returned row rather than nil beside the ref.
//
// A row is left exactly as it was when nothing in it moves: a value under the
// threshold, a value that is not bytes (a transcript column still stored as
// plain text is text on this machine too), or a table with no moving columns.
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

// rowCodecs reads every codec column of one row by name. The values are read
// from the row rather than assumed, because the ref has to describe the bytes as
// this machine stores them: a value the file holds plain and a value it holds
// as a frame are the same ref only if the codec travels with them.
//
// A codec column the row does not carry reads as plain. That is the value an
// uncompressed file has for every one of these columns, so a row read from such
// a file is described correctly rather than refused over a column it never had.
func rowCodecs(columns []db.ExchangeColumn) map[string]int {
	codecs := make(map[string]int, len(columns))
	for _, col := range columns {
		if v, ok := col.Value.(int64); ok {
			codecs[col.Name] = int(v)
		}
	}
	return codecs
}

// columnIndex returns where name sits in columns, or -1.
func columnIndex(columns []db.ExchangeColumn, name string) int {
	for i, col := range columns {
		if col.Name == name {
			return i
		}
	}
	return -1
}

// uploadPending writes each value to the staging folder and hands it to the
// mover, in the order the batch found them.
//
// The file is written under the digest the key names, so a staging folder left
// behind by a killed process holds files this code recognises rather than a
// second naming scheme. It is removed whether or not the put came back: a
// failed put means the entry is not appended, so nothing will fetch that file,
// and leaving it would fill the folder with bodies the bucket never took.
func uploadPending(m BlobMover, stagingDir string, blobs []pendingBlob) error {
	for _, b := range blobs {
		if err := uploadOne(m, stagingDir, b); err != nil {
			return err
		}
	}
	return nil
}

// uploadOne stages one value and puts it. The write and the removal are both
// reported only as far as the caller can act: a removal that fails after a
// successful put is not a failed export, so it is not turned into one.
func uploadOne(m BlobMover, stagingDir string, b pendingBlob) error {
	path := filepath.Join(stagingDir, b.Ref.SHA256)
	// The folder holds bodies that exist nowhere else until the put lands, so
	// the file is owner-only even inside a folder the daemon already made 0700:
	// the file outlives this call, and the mode is what a reader of the folder
	// listing sees.
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
