// Package syncworker is the worker half of the sync pipe: the protocol the
// daemon and its `relevo sync-worker` child share, and the loop that serves it.
// The package reaches no database of this installation's own, which is what
// keeps a driver abort inside the worker from ever touching the record.
package syncworker

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ProtocolVersion is what hello announces and what a worker refuses a higher
// number of. The version travels on the first request rather than in the
// command line, so the daemon learns what its own child speaks before it sends
// anything it would otherwise have to take back.
//
// Version 2 added the blob verbs and the R2 settings hello carries. A daemon on
// version 1 and a worker on version 2 refuse each other here rather than
// discovering it at the first put_blob, which would be after the exporter had
// already written entries pointing at objects nobody uploaded.
const ProtocolVersion = 2

// The refusals the worker answers with rather than dying over. A request that
// breaks one of them has a well-formed frame around it, so the reply can carry
// the reason and the pipe stays usable; only a frame the worker cannot read at
// all ends the loop.
var (
	// ErrProtocol is a line that is not a request this pipe speaks, including a
	// request with no id: with no id there is nothing to answer, so the worker
	// stops rather than send a reply the caller could not match.
	ErrProtocol = errors.New("not the sync pipe protocol")
	// ErrNoHello is a verb that needs the handshake and arrived before it. The
	// worker holds nothing it could answer with until hello names the origin.
	ErrNoHello = errors.New("no hello yet")
	// ErrForeignOrigin is an export naming an entry another installation owns.
	// An installation appends only for its own rows, and refusing here is what
	// stops two machines writing the same one.
	ErrForeignOrigin = errors.New("another origin's entry")
)

// Verb is what one request asks the worker to do.
type Verb string

const (
	// VerbHello opens the pipe: it carries the version, this installation's
	// origin and the credentials the worker owns for the rest of its life.
	VerbHello Verb = "hello"
	// VerbExport appends one batch of this origin's entries as one write.
	VerbExport Verb = "export"
	// VerbPull reads every other origin's entries past the marks given.
	VerbPull Verb = "pull"
	// VerbHead reads one page of an origin's head.
	VerbHead Verb = "head"
	// VerbPutBlob uploads one staged body into R2 under a key.
	VerbPutBlob Verb = "put_blob"
	// VerbGetBlob fetches one body out of R2 into a staged file.
	VerbGetBlob Verb = "get_blob"
	// VerbStats reports what the log holds.
	VerbStats Verb = "stats"
	// VerbShutdown ends the pipe after its reply.
	VerbShutdown Verb = "shutdown"
)

// R2Config is the bucket the worker moves bodies through, as hello carries it.
//
// It travels in hello and nowhere else, like the token: the daemon reads it from
// this machine's secrets, hands it over the pipe, and the worker holds it for
// life without its command line or its environment ever naming it. Every later
// verb names an object by key alone.
type R2Config struct {
	// Endpoint is the account-scoped S3 URL, which carries no bucket of its own.
	Endpoint string `json:"endpoint"`
	// Bucket is the bucket objects are stored under, one per installation.
	Bucket string `json:"bucket"`
	// KeyID is the access key that signs every request.
	KeyID string `json:"key_id"`
	// Secret is that key's secret. It is never formatted into an error, and the
	// status surface reports whether it is present rather than what it holds.
	Secret string `json:"secret"`
}

// Request is one line of the pipe, from the daemon to the worker.
//
// Every field is optional in the JSON because a verb reads only what it needs,
// and omitempty keeps a request's line down to what it carries: a token over a
// pipe is read out of the first line, and a longer one only widens what
// another process on this machine could read.
type Request struct {
	// ID matches the reply, and is how one reply is told from another's.
	ID string `json:"id"`
	// Verb is what this request asks for.
	Verb Verb `json:"verb"`
	// Version is the pipe version, on hello alone.
	Version int `json:"version,omitempty"`
	// Origin is this installation's own origin on hello, and the origin whose
	// head is being read on head.
	Origin string `json:"origin,omitempty"`
	// Token and URL reach the worker over the pipe rather than in argv or the
	// environment, where any process on this machine could read them back.
	Token string `json:"token,omitempty"`
	URL   string `json:"url,omitempty"`
	// R2 is the bucket configuration, on hello alone. It is optional because a
	// machine may sync rows with no bucket configured, and the blob verbs are
	// then refused with a message that says so rather than failing on a nil.
	R2 *R2Config `json:"r2,omitempty"`
	// Key names the object a blob verb addresses, as "<origin>/<sha256>". It is
	// never a path into the bucket: the store builds the URL from it and both
	// halves are checked, so a key cannot reach outside its own origin's prefix.
	Key string `json:"key,omitempty"`
	// Staging is the absolute local path the body is read from or written to.
	// Bodies never cross the pipe, so this is the only thing either blob verb
	// carries about them.
	Staging string `json:"staging,omitempty"`
	// Max is the most bytes get_blob may write: the length the ref declares. A
	// body that runs past it is cut off there rather than downloaded whole.
	Max int64 `json:"max,omitempty"`
	// Entries is the batch an export appends as one write.
	Entries []Entry `json:"entries,omitempty"`
	// Marks is each other origin's last applied sequence number, so a pull
	// answers with what follows it and with no mark answered from the start.
	Marks map[string]int `json:"marks,omitempty"`
	// After and Limit page a head read. An empty after starts at the beginning
	// and a limit of zero means the whole rest, which is what a caller that
	// wants the whole head leaves unset.
	After string `json:"after,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

// Response is one line of the pipe, from the worker to the daemon.
type Response struct {
	// ID is the request's, echoed so a reply is matched to its own request.
	ID string `json:"id"`
	// OK says whether the verb was carried out. A refusal is a reply with OK
	// false and a reason: the pipe is still in step after one.
	OK bool `json:"ok"`
	// Error is the refusal's own words, and empty on success.
	Error string `json:"error,omitempty"`
	// Code is the class of a refusal the worker answered with, and empty on
	// success and on a refusal a later attempt can still get past. The daemon
	// reads the class rather than the reason's words, so a remote's own text
	// never decides how a refusal is classified.
	Code RefusalCode `json:"code,omitempty"`
	// Entries is what an export numbered, a pull read, or a head read and did
	// not fit the fields below.
	Entries []Entry `json:"entries,omitempty"`
	// Head is a page of an origin's latest state per row.
	Head []HeadRow `json:"head,omitempty"`
	// Bytes is what one blob verb moved, in each direction. It is the exact
	// length rather than an estimate, because the daemon adds it to what the
	// month has cost and an estimate compounds across every call.
	Bytes int64 `json:"bytes,omitempty"`
	// Skipped says a put_blob found the object already in the store and sent
	// nothing. A caller that uploaded a body another machine had already put
	// reads the skip as the object it wanted being there, not as a failure.
	Skipped bool `json:"skipped,omitempty"`
	// Stats is what the log holds, on stats alone.
	Stats *Stats `json:"stats,omitempty"`
}

// RefusalCode is the class of a refusal the worker answered with. It is the
// worker's own vocabulary: the daemon maps each class to a sentinel of its own,
// so a refusal crosses the pipe as a word this package chose rather than as
// anything the remote said.
type RefusalCode string

const (
	// CodeRemote is a remote that refused this machine's work in a way a later
	// attempt cannot get past: the remote is not this log, or it refused the
	// statement itself.
	CodeRemote RefusalCode = "remote"
	// CodeSchema is a remote that has no table for this machine's rows. It is
	// its own class because its fix is the schema rather than a re-recorded
	// row.
	CodeSchema RefusalCode = "schema"
	// CodeBlobMissing is a body the store does not hold. It is a refusal that
	// repeats, so it needs a class of its own: the daemon latches the origin and
	// names the round, where a bucket that could not be reached is retried.
	CodeBlobMissing RefusalCode = "blob_missing"
	// CodeBlobRefused is a body the worker will not publish under the key it was
	// given, because the staged file's digest is not the key's. The store is
	// fine and the remote is fine; what crossed the pipe is not, and repeating
	// the call unchanged would publish the same wrong body.
	CodeBlobRefused RefusalCode = "blob_refused"
)

// RefusalError is an error a backend marked with the class of refusal it is, so
// a refusal that will repeat carries that fact to the daemon instead of being
// read as a transient fault.
type RefusalError interface {
	error
	// RefusalCode is the class the worker puts on the wire.
	RefusalCode() RefusalCode
}

// MarkRefusal returns err marked with the class of refusal it is. An empty
// class leaves the error alone, and an unmarked refusal is one the daemon reads
// as a fault a later attempt can still get past.
func MarkRefusal(code RefusalCode, err error) error {
	if code == "" {
		return err
	}
	return &markedRefusal{code: code, err: err}
}

// RefusalCodeOf returns the class err was marked with, and empty when it
// carries none.
func RefusalCodeOf(err error) RefusalCode {
	var marked RefusalError
	if errors.As(err, &marked) {
		return marked.RefusalCode()
	}
	return ""
}

// markedRefusal is err carrying the class a backend marked it with. The reason
// stays the same error, so the refusal's own words cross the pipe verbatim.
type markedRefusal struct {
	code RefusalCode
	err  error
}

func (m *markedRefusal) Error() string { return m.err.Error() }

func (m *markedRefusal) Unwrap() error { return m.err }

func (m *markedRefusal) RefusalCode() RefusalCode { return m.code }

// Entry is one log entry as it travels the pipe. Its JSON names are the remote
// table's own columns, so what crosses the pipe is what the log stores and a
// worker reading it needs no second vocabulary.
type Entry struct {
	// Origin is the installation that owns the row. The transport hands out Seq
	// and Batch, never the writer, so a local file restored from a backup can
	// never name a number the log already holds.
	Origin string `json:"origin"`
	// Seq is the entry's position in that origin's log.
	Seq int `json:"seq"`
	// Batch is the Seq of the first entry of the append that wrote this one, so
	// a reader can tell where one atomic batch ends.
	Batch int `json:"batch"`
	// Tbl is the shared table the row is in.
	Tbl string `json:"tbl"`
	// PK is the row's primary key as the json_array text the outbox records.
	PK string `json:"pk"`
	// Op is upsert or delete.
	Op string `json:"op"`
	// SchemaVersion is the writer's schema version, so a reader can tell an
	// entry it understands from one a newer binary wrote.
	SchemaVersion int `json:"schema_version"`
	// Body is the row as JSON, column name to value. It is empty for a
	// delete, which names a row by its key and carries nothing else.
	Body json.RawMessage `json:"body,omitempty"`
	// At is when the entry was written.
	At time.Time `json:"at"`
}

// HeadRow is one row of an origin's head: the latest state of a single shared
// row, as the log holds it. This is the state reconcile compares a local row
// against, so a row missing here is one the origin has not written and a row
// here with another hash is one whose contents changed.
type HeadRow struct {
	// Tbl is the shared table the row is in.
	Tbl string `json:"tbl"`
	// PK is the row's primary key.
	PK string `json:"pk"`
	// Seq is the sequence number of the entry that last wrote the row.
	Seq int `json:"seq"`
	// Hash is that entry's body hash.
	Hash string `json:"hash"`
}

// Stats is what the log holds, so a caller can tell an empty log from a
// transport that is refusing to answer.
type Stats struct {
	// Entries is how many entries the log holds across every origin.
	Entries int `json:"entries"`
	// Origins is how many distinct origins have appended to it.
	Origins int `json:"origins"`
	// Seq is the highest sequence number the log holds. A sequence number
	// belongs to one origin, so this is the largest rather than a count.
	Seq int `json:"seq"`
	// TursoSent and TursoReceived are the network bytes the replica has pushed
	// and pulled over this worker's life, from the deltas of the engine's own
	// counters. They are cumulative for this worker and start at zero, because a
	// process that has just started has moved nothing whatever the replica file
	// was written by before.
	TursoSent     int64 `json:"turso_sent"`
	TursoReceived int64 `json:"turso_received"`
	// R2Put and R2Get are the body bytes sent to and received from the bucket
	// over this worker's life, counted from object sizes rather than estimated.
	// An upload that found the object already present adds nothing: it moved no
	// bytes, and counting it would bill the month for a body nobody sent.
	R2Put int64 `json:"r2_put"`
	R2Get int64 `json:"r2_get"`
}

// readRequest reads one request line. A clean end of the pipe is io.EOF, which
// the caller turns into an ordinary exit; a line that is present but is not a
// request this pipe speaks is ErrProtocol.
func readRequest(r *bufio.Reader) (Request, error) {
	line, err := r.ReadBytes('\n')
	if len(line) == 0 && err != nil {
		return Request{}, err
	}
	trimmed := trimNewline(line)
	if len(trimmed) == 0 {
		return Request{}, fmt.Errorf("%w: an empty line", ErrProtocol)
	}

	var req Request
	if err := json.Unmarshal(trimmed, &req); err != nil {
		return Request{}, fmt.Errorf("%w: %w", ErrProtocol, err)
	}
	if req.ID == "" {
		return Request{}, fmt.Errorf("%w: a request with no id", ErrProtocol)
	}
	if req.Verb == "" {
		return Request{}, fmt.Errorf("%w: a request with no verb", ErrProtocol)
	}
	return req, nil
}

// trimNewline drops the line's own terminator. The last line of a pipe that
// closes without one is still a line, so the terminator is removed whether or
// not it was there.
func trimNewline(line []byte) []byte {
	for len(line) > 0 && (line[len(line)-1] == '\n' || line[len(line)-1] == '\r') {
		line = line[:len(line)-1]
	}
	return line
}

// writeResponse writes one reply line and flushes it. The flush is the whole
// point of the function: the daemon waits for this reply before it decides
// whether a call was made, so a reply still sitting in a buffer is a call that
// never came back.
func writeResponse(w *bufio.Writer, resp Response) error {
	line, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("syncworker: marshal reply %s: %w", resp.ID, err)
	}
	if _, err := w.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("syncworker: write reply %s: %w", resp.ID, err)
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("syncworker: flush reply %s: %w", resp.ID, err)
	}
	return nil
}
