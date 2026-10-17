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
// Version 2 added the blob verbs and the R2 settings hello carries. Version 3
// added key_rekey, which writes the key's new epoch beside it; a daemon on an
// older version and a worker on this one refuse each other here rather than
// discovering the difference at the first key_rekey, which would be after one
// side had already answered from material the other cannot read.
//
// Version 4 adds AfterOrigin and AfterSeq on Entry, ClaimIdentity, and the
// ctl:takeover entry kind.
const ProtocolVersion = 4

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

	// ErrBoundary is a signed batch for an origin with no boundary row whose
	// first signed sequence is not the one the export predicted.
	ErrBoundary = errors.New("the signed-from boundary was not the one predicted")
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
	// VerbPublishMarks records how far this worker has applied of every other
	// origin, as this worker's own rows.
	VerbPublishMarks Verb = "publish_marks"
	// VerbReadMarks reads every published mark, the floors recorded so far, and
	// each origin's head and entry count.
	VerbReadMarks Verb = "read_marks"
	// VerbPullRange reads one page of one origin's entries between two
	// sequences, for a reader whose mark is below the origin's floor.
	VerbPullRange Verb = "pull_range"
	// VerbCompact drops this worker's own superseded entries up to a floor and
	// records the floor, in one write.
	VerbCompact Verb = "compact"
	// VerbKeyMeta reads the passphrase material the remote holds: the KDF
	// parameters and the check value derived from the key.
	VerbKeyMeta Verb = "key_meta"
	// VerbKeyInit writes that material if the remote holds none, and answers
	// with whatever it holds afterwards.
	VerbKeyInit Verb = "key_init"
	// VerbKeyRekey rotates the log's key to a new epoch: the new material, the
	// key it replaced sealed for the machines still holding it, and every
	// origin's cut, all in one transaction that refuses a remote whose epoch
	// or whose heads moved under the caller.
	VerbKeyRekey Verb = "key_rekey"
	// VerbKeyCut extends this worker's own origin's cut, forward only and
	// within the log. It is how a machine that unlocked with the new
	// passphrase -- or whose own head has grown past the cut it holds --
	// vouches for the tail it appended before it learned of the rekey.
	VerbKeyCut Verb = "key_cut"
	// VerbClaim records, and reads back, the claim a machine adopting an
	// identity holds on it. A request with no claim token only reads.
	VerbClaim Verb = "claim_identity"
	// VerbShutdown ends the pipe after its reply.
	VerbShutdown Verb = "shutdown"
)

// KeyMeta is the passphrase material a remote holds, as the pipe carries it.
//
// It travels as base64 like the MAC, because encoding/json gives a byte slice
// that and because the remote stores these in text meta rows. The salt and the
// check are not secret -- the remote holds them so a second machine can derive
// and compare the same key -- and they are still treated as untrusted: every
// value here came from a remote this machine does not control.
type KeyMeta struct {
	// Salt is the KDF salt, empty on a remote that has never been given one.
	Salt []byte `json:"salt,omitempty"`
	// KDF names the derivation this remote's key was stretched with.
	KDF string `json:"kdf,omitempty"`
	// Check is the value a wrong passphrase does not reproduce.
	Check []byte `json:"check,omitempty"`
	// Present says whether the remote holds a key at all. It is its own field
	// rather than being read from the salt, because a remote can hold a check
	// without a salt and the two absences mean different things to a caller.
	Present bool `json:"present,omitempty"`
	// Epoch is the epoch this material's key is. A remote that holds no
	// key_epoch row is at epoch 1, and the worker hands every reader that 1
	// rather than a zero each caller would have to default itself.
	Epoch int `json:"epoch,omitempty"`
	// Wrapped is the older epochs' keys the remote holds sealed, keyed by the
	// epoch each one is. It is nil on a remote that holds none, which is every
	// remote before its first rekey.
	Wrapped map[int][]byte `json:"wrapped,omitempty"`
}

// EpochCut is one origin's cut as a key_rekey carries it: the epoch entries
// above the cut must verify under, the sequence the cut sits at, and the tag
// binding origin, epoch and sequence to the epoch's key.
type EpochCut struct {
	// Epoch is the epoch the cut fences for.
	Epoch int `json:"epoch,omitempty"`
	// Seq is the highest sequence the cut vouches for as history.
	Seq int `json:"seq,omitempty"`
	// Tag is the HMAC that binds the three to the epoch's key.
	Tag []byte `json:"tag,omitempty"`
}

// R2Config is the bucket the worker moves bodies through, as hello carries it.
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
	// Publish is what a publish_marks carries: each origin this reader has
	// applied to the sequence named. It is its own field rather than the Marks
	// above because the two shapes are a list and a map, and one frame field
	// cannot be both.
	//
	// A mark's reader is not among them. It is this worker's own origin from
	// hello, so a caller on this machine cannot write another reader's rows --
	// a mark is what a compaction floor is computed from, and choosing one for
	// somebody else is choosing what the log may drop.
	Publish []Mark `json:"publish,omitempty"`
	// At is when this reader reached the marks being published, rather than a
	// time the worker takes for itself: a mark's age decides whether its reader
	// still counts towards a floor, and only the publisher knows when it was
	// really at that sequence. It is a pointer so a request that carries no
	// time at all sends no field, rather than one every verb would have to know
	// to ignore.
	At *time.Time `json:"at,omitempty"`
	// After and Limit page a head read. An empty after starts at the beginning
	// and a limit of zero means the whole rest, which is what a caller that
	// wants the whole head leaves unset.
	After string `json:"after,omitempty"`
	Limit int    `json:"limit,omitempty"`
	// AfterSeq and Upto bound a pull_range to AfterSeq < seq <= Upto of the
	// origin named above; Floor is what a compact drops up to.
	AfterSeq int `json:"after_seq,omitempty"`
	Upto     int `json:"upto,omitempty"`
	Floor    int `json:"floor,omitempty"`
	// KeyMeta is the passphrase material a key_init writes. A key_meta carries
	// none: it asks what the remote holds rather than naming what it should.
	// A key_rekey names the material the remote is to hold afterwards.
	KeyMeta *KeyMeta `json:"key_meta,omitempty"`
	// Epoch is the epoch the caller read the remote at, on a key_rekey alone:
	// the rekey is a compare-and-set against it, so a remote whose epoch moved
	// under the caller refuses rather than writing over another machine's.
	Epoch int `json:"epoch,omitempty"`
	// Cuts is every origin's cut a key_rekey writes, each at the head the
	// caller observed. The worker re-predicts them from the log inside its own
	// transaction and refuses a caller whose observation is stale.
	Cuts map[string]EpochCut `json:"cuts,omitempty"`
	// Cut is the one cut a key_cut extends this worker's own origin to, named
	// by the caller and tagged under the key the caller vouches with. The
	// origin is the worker's from hello and the head is the log's own, so a
	// caller cannot vouch for history the log does not hold.
	Cut *EpochCut `json:"cut,omitempty"`
	// BoundarySeq and BoundaryTag are the first signed sequence an export
	// predicts for its origin and the tag that binds it to the key. The worker
	// stores them only where the origin has no boundary yet.
	BoundarySeq int    `json:"boundary_seq,omitempty"`
	BoundaryTag []byte `json:"boundary_tag,omitempty"`
	// ClaimToken is the random token a claim_identity writes for the identity
	// Origin names, at At. Empty reads the claim without writing one.
	ClaimToken string `json:"claim_token,omitempty"`
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
	// Marks is what a read_marks answered with. It is one object rather than
	// four fields at the top of the reply because one of its names is entries,
	// which an export and a pull already answer under, and a reply may not say
	// two different things under one name.
	Marks *MarkState `json:"marks,omitempty"`
	// Dropped and Floor are what a compact answers with: the entries it removed
	// and the floor the log now records, which is never below the one before.
	Dropped int `json:"dropped,omitempty"`
	Floor   int `json:"floor,omitempty"`
	// KeyMeta is what a key_meta read and a key_init wrote, and is nil on a remote
	// that holds no passphrase material at all.
	KeyMeta *KeyMeta `json:"key_meta,omitempty"`
	// ClaimToken and ClaimAt are the claim a claim_identity found the remote
	// holding after its write: empty and zero when it holds none.
	ClaimToken string     `json:"claim_token,omitempty"`
	ClaimAt    *time.Time `json:"claim_at,omitempty"`
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
	// CodeQuota is a remote refusing writes because the plan's quota is reached.
	// It repeats until the billing month turns, so the daemon latches on it.
	CodeQuota RefusalCode = "quota"
	// CodeBlobRefused is a body the worker will not publish under the key it was
	// given, because the staged file's digest is not the key's. The store is
	// fine and the remote is fine; what crossed the pipe is not, and repeating
	// the call unchanged would publish the same wrong body.
	CodeBlobRefused RefusalCode = "blob_refused"
	// CodeBoundary is a signed batch the worker refused because its boundary was
	// not the one predicted. Nothing was written and the next attempt predicts
	// again.
	CodeBoundary RefusalCode = "boundary"
	// CodeEpochRace is a key_rekey the worker refused because the remote's epoch
	// is not the one the caller read: another machine rekeyed first. Nothing was
	// written, and no retry with the same material can get past it -- the
	// caller's key is an old one now.
	CodeEpochRace RefusalCode = "epoch_race"
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
	// MAC is the HMAC-SHA256 over the entry's canonical encoding, absent on an
	// entry written before this machine had a key. It travels as base64, which
	// is what encoding/json does with a byte slice.
	MAC []byte `json:"mac,omitempty"`
	// AfterOrigin and AfterSeq point at the entry in another origin's log that
	// this entry depends on. They are empty/zero for an entry with no dependency.
	AfterOrigin string `json:"after_origin,omitempty"`
	AfterSeq    int    `json:"after_seq,omitempty"`
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

// Mark is one origin's published position, as a publish_marks carries it: a
// sequence and where it is, and nothing else.
//
// There is no reader and no time among them. The reader is the worker's own
// origin from hello, so a caller cannot name a machine that is not this one;
// the time is the request's own, one value for the whole publish rather than
// one per origin that could disagree with each other.
type Mark struct {
	// Origin is the log the position is in, which is never this reader's own.
	Origin string `json:"origin"`
	// Seq is the highest sequence of that origin the reader has applied.
	Seq int `json:"seq"`
}

// PublishedMark is a mark as a read_marks hands it back, with the reader and the
// time the publish did not carry. It is a separate shape from Mark because a
// reply names who a mark belongs to and when it was taken, and a request that
// could do the same would be letting the publisher choose both.
type PublishedMark struct {
	// Reader is the installation the mark belongs to.
	Reader string `json:"reader"`
	// Origin is the log the position is in.
	Origin string `json:"origin"`
	// Seq is the highest sequence of that origin the reader has applied.
	Seq int `json:"seq"`
	// At is when the reader reached that sequence, which is what decides whether
	// it still counts towards a floor.
	At time.Time `json:"at"`
}

// MarkState is what a read of the marks answers with: every published mark, the
// floors the remote has recorded, and each origin's head and entry count.
//
// It is its own object on the wire rather than four fields beside the rest of
// the reply because one of its names is entries, which an export and a pull
// already answer under. A reply may not say two different things under one
// name, so the three counts are read from here rather than from the top.
//
// The maps are nil rather than empty when there is nothing to report, so an
// absent floor and a floor of zero stay apart -- a remote that has compacted
// nothing and one whose floor is at the start of a log mean different things to
// the caller that reads them.
type MarkState struct {
	// Marks are the published positions, one row per reader per origin.
	Marks []PublishedMark `json:"marks,omitempty"`
	// Floors is what each origin has already been compacted to.
	Floors map[string]int `json:"floors,omitempty"`
	// Heads is each origin's highest sequence in the log.
	Heads map[string]int `json:"heads,omitempty"`
	// Entries is how many entries the log holds for each origin.
	Entries map[string]int `json:"entries,omitempty"`
	// Sizes is each origin's log size in bytes, estimated as the bodies plus a
	// fixed overhead per row.
	Sizes map[string]int64 `json:"sizes,omitempty"`
	// SignedFrom is the first sequence of each origin that was signed. An
	// origin with no entry is not named, which is how a reader tells "nothing
	// here has been signed" from "signed from the first entry".
	SignedFrom map[string]int `json:"signed_from,omitempty"`
	// SignedTags is the tag stored beside each boundary, nil where the stored
	// value has none that parses.
	SignedTags map[string][]byte `json:"signed_tags,omitempty"`
	// EpochCuts is each origin's cut as the log holds it, one row per origin
	// a rekey wrote. A row that does not parse arrives as the cut nothing
	// verifies under, which the reader holds its origin over.
	EpochCuts map[string]EpochCut `json:"epoch_cuts,omitempty"`
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

// CompactResult is what a compaction did, as the backend reports it.
type CompactResult struct {
	Dropped int
	Floor   int
}
