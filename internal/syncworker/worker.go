package syncworker

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/fuad-daoud/relevo/internal/blobstore"
)

// Spec is what hello tells the worker: the origin it appends as, and the remote
// it reaches. The token arrives here and nowhere else, so the worker holds it
// without its command line or its environment naming it.
type Spec struct {
	// Version is the pipe version the daemon speaks.
	Version int
	// Origin is the installation this worker appends as, and the only origin
	// whose entries an export may carry.
	Origin string
	// URL and Token reach the remote.
	URL   string
	Token string
	// R2 is the bucket bodies move through, or nil on a machine that has none.
	// It arrives on hello with the token and stays for the worker's life, so a
	// blob verb never carries a credential of its own.
	R2 *R2Config
}

// Boundary is the first signed sequence an export predicted for its origin and
// the tag binding it to the key.
type Boundary struct {
	Seq int
	Tag []byte
}

// boundaryBackend is a backend that writes the signed-from row with a signed
// batch.
type boundaryBackend interface {
	AppendSigned(entries []Entry, boundary Boundary) ([]Entry, error)
}

// Backend is what the worker serves the pipe with: the log itself, behind the
// replica this process owns. Open comes before every other call, and only hello
// reaches it; Close runs when the loop ends for any reason, because a replica
// left open locks out the next worker.
type Backend interface {
	// Open takes the remote for the rest of this worker's life.
	Open(Spec) error
	// Append writes one batch of this origin's entries as a single atomic
	// write, numbering them from one past the highest sequence this origin
	// already holds, and returns them numbered. Entries of another origin are
	// refused: an installation appends only for the rows it owns.
	Append(entries []Entry) ([]Entry, error)
	// Pull returns one page per origin of every other origin's entries past the
	// mark given for it, in sequence order and in whole batches. An origin with
	// no mark is read from the start of its log.
	Pull(marks map[string]int) ([]Entry, error)
	// Head returns a page of one origin's latest entry per row, starting after
	// the given key. An empty after starts at the beginning and a limit of zero
	// means the whole rest.
	Head(origin, after string, limit int) ([]HeadRow, error)
	// Stats reports what the log holds.
	Stats() (Stats, error)
	// PublishMarks records how far this worker has applied of each origin, and
	// when. The reader is the worker's own origin from hello, never a call
	// field: a mark is what a compaction floor is computed from.
	PublishMarks(marks map[string]int, at time.Time) error
	// ReadMarks answers with every published mark, the floors the remote has
	// recorded, and each origin's head and entry count.
	ReadMarks() (MarkState, error)
	// PullRange reads one page of origin's entries with after < seq <= upto from
	// the replica as it stands, without pulling first: a newer compaction pulled
	// in between could drop entries the range needs.
	PullRange(origin string, after, upto int) ([]Entry, error)
	// Compact drops this worker's own superseded entries up to floor and records
	// the floor, in one transaction and one push.
	Compact(floor int) (CompactResult, error)
	// KeyMeta reads the passphrase material the remote holds, answering
	// Present false on a remote that has none.
	KeyMeta() (KeyMeta, error)
	// KeyInit writes that material where the remote holds none and answers with
	// what it holds afterwards, so two machines racing the first one both learn
	// the winner's salt.
	KeyInit(key KeyMeta) (KeyMeta, error)
	// KeyRekey rotates the log's key to the epoch the spec names, in one
	// transaction that also writes the sealed key it replaced and every origin's
	// cut, and refuses whole on a moved head or a moved epoch.
	KeyRekey(spec KeyRekeySpec) (KeyMeta, error)
	// PublishEpochCut extends this worker's own origin's cut, forward only and
	// within the log. The origin is the worker's own from hello, never a call
	// field: vouching for history decides what another reader may refuse.
	PublishEpochCut(cut EpochCut) error
	// ClaimIdentity writes the claim token for origin where the remote holds
	// none, holds the caller's own, or holds one older than synclog's TTL, then
	// answers with the claim the remote holds. An empty token only reads.
	ClaimIdentity(origin, token string, at time.Time) (string, time.Time, error)
	// Close releases the remote and the replica.
	Close() error
}

// server is one worker's state between requests: the backend, and the spec
// hello filled in. Before hello it holds nothing, so an early verb has no
// origin to append as and no remote to answer from.
type server struct {
	backend Backend
	spec    *Spec
	// blobs is the store hello configured, built on the first blob verb and kept
	// for the rest of the pipe; nil until then, as a pre-handshake verb finds.
	blobs blobstore.BlobStore
	// r2 is what the blob verbs have moved over this pipe's life, added to the
	// backend's own totals when stats answers.
	r2 Stats
}

// Serve answers requests from in until the pipe ends, writing one reply per
// request to out. The loop ends three ways, and all three close the backend:
// stdin closes, a verb says shutdown, or a line arrives that is not a request
// this pipe speaks. The last is no ordinary exit: an unreadable line has no id
// to answer, so the caller learns of it by the worker dying.
func Serve(in io.Reader, out io.Writer, b Backend) error {
	r := bufio.NewReader(in)
	w := bufio.NewWriter(out)
	s := &server{backend: b}

	err := s.loop(r, w)
	// The close error reports only when the loop ended cleanly, so a failed pipe
	// is not reported as a replica that would not shut.
	if cerr := b.Close(); cerr != nil && err == nil {
		return fmt.Errorf("syncworker: close backend: %w", cerr)
	}
	return err
}

// loop reads and answers requests until the pipe ends.
func (s *server) loop(r *bufio.Reader, w *bufio.Writer) error {
	for {
		req, err := readRequest(r)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := writeResponse(w, s.dispatch(req)); err != nil {
			return err
		}
		if req.Verb == VerbShutdown {
			return nil
		}
	}
}

// verbHandlers is every request verb this pipe speaks, mapped to the method that
// serves it. The set lives in one table so it is enumerable: the server
// dispatches from it, and a caller classifying every verb walks it rather than
// keeping a second list in step.
var verbHandlers = map[Verb]func(*server, Request) Response{
	VerbHello:          (*server).hello,
	VerbExport:         (*server).export,
	VerbPull:           (*server).pull,
	VerbHead:           (*server).head,
	VerbPutBlob:        (*server).putBlobVerb,
	VerbGetBlob:        (*server).getBlobVerb,
	VerbStats:          (*server).stats,
	VerbPublishMarks:   (*server).publishMarks,
	VerbReadMarks:      (*server).readMarks,
	VerbPublishTraffic: (*server).publishTraffic,
	VerbPullRange:      (*server).pullRange,
	VerbCompact:        (*server).compact,
	VerbKeyMeta:        (*server).keyMeta,
	VerbKeyInit:        (*server).keyInit,
	VerbKeyRekey:       (*server).keyRekey,
	VerbKeyCut:         (*server).keyCut,
	VerbClaim:          (*server).claimIdentity,
	VerbShutdown:       (*server).shutdown,
}

// Verbs returns every request verb this pipe speaks, sorted for a stable order,
// derived from the dispatch table rather than written beside it.
func Verbs() []Verb {
	out := make([]Verb, 0, len(verbHandlers))
	for verb := range verbHandlers {
		out = append(out, verb)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// dispatch answers one request. A refused verb is answered with a reply and the
// pipe carries on: an unexpected answer is not a broken pipe.
func (s *server) dispatch(req Request) Response {
	if s.spec == nil && req.Verb != VerbHello {
		return refuse(req, ErrNoHello)
	}
	handle, ok := verbHandlers[req.Verb]
	if !ok {
		return refuse(req, fmt.Errorf("%w: verb %q", ErrProtocol, req.Verb))
	}
	return handle(s, req)
}

// shutdown ends the pipe after this reply, which the loop reads.
func (s *server) shutdown(req Request) Response { return Response{ID: req.ID, OK: true} }

// hello opens the backend for the rest of the pipe and takes the origin every
// later request is checked against. An unspoken version is refused here rather
// than answered into: every answer after it would read wrongly.
func (s *server) hello(req Request) Response {
	if req.Version != ProtocolVersion {
		return refuse(req, fmt.Errorf("%w: pipe version %d, this worker speaks %d",
			ErrProtocol, req.Version, ProtocolVersion))
	}
	if req.Origin == "" {
		return refuse(req, fmt.Errorf("%w: hello without an origin", ErrProtocol))
	}
	if req.URL == "" || req.Token == "" {
		return refuse(req, fmt.Errorf("%w: hello without a remote", ErrProtocol))
	}
	spec := Spec{Version: req.Version, Origin: req.Origin, URL: req.URL, Token: req.Token, R2: req.R2}
	if err := s.backend.Open(spec); err != nil {
		return refuse(req, err)
	}
	s.spec = &spec
	return Response{ID: req.ID, OK: true}
}

// export appends one batch, after refusing anything the log would not take. The
// refusals live here rather than in the backend so the rule is stated once:
// the batch is this origin's, and every entry has the shape the log defines.
func (s *server) export(req Request) Response {
	for _, e := range req.Entries {
		if e.Origin != s.spec.Origin {
			return refuse(req, fmt.Errorf("export %s %s: %w", e.Tbl, e.PK, ErrForeignOrigin))
		}
		if err := checkEntry(e); err != nil {
			return refuse(req, err)
		}
	}
	var written []Entry
	var err error
	if bb, ok := s.backend.(boundaryBackend); ok {
		written, err = bb.AppendSigned(req.Entries, Boundary{Seq: req.BoundarySeq, Tag: req.BoundaryTag})
	} else {
		written, err = s.backend.Append(req.Entries)
	}
	if err != nil {
		return refuse(req, err)
	}
	return Response{ID: req.ID, OK: true, Entries: written}
}

// pull answers with the entries of every origin but this one past the marks
// given.
func (s *server) pull(req Request) Response {
	entries, err := s.backend.Pull(req.Marks)
	if err != nil {
		return refuse(req, err)
	}
	return Response{ID: req.ID, OK: true, Entries: entries}
}

// head answers with one page of an origin's latest state per row.
func (s *server) head(req Request) Response {
	rows, err := s.backend.Head(req.Origin, req.After, req.Limit)
	if err != nil {
		return refuse(req, err)
	}
	return Response{ID: req.ID, OK: true, Head: rows}
}

// stats answers with what the log holds, and with what this worker has moved.
// The halves are added rather than read from one place because they are counted
// by different things: the backend measures the replica's traffic and the verbs
// measure the bucket's.
func (s *server) stats(req Request) Response {
	stats, err := s.backend.Stats()
	if err != nil {
		return refuse(req, err)
	}
	stats.R2Put += s.r2.R2Put
	stats.R2Get += s.r2.R2Get
	return Response{ID: req.ID, OK: true, Stats: &stats}
}

// checkEntry refuses an entry the log has no room for: an unknown op, a delete
// carrying a body, or an upsert carrying none. Wrong shapes are refused here,
// before the backend could write one read off the pipe.
func checkEntry(e Entry) error {
	switch e.Op {
	case "upsert":
		if len(e.Body) == 0 {
			return fmt.Errorf("%w: upsert %s %s has no body", ErrProtocol, e.Tbl, e.PK)
		}
	case "delete":
		if len(e.Body) != 0 {
			return fmt.Errorf("%w: delete %s %s carries a body", ErrProtocol, e.Tbl, e.PK)
		}
	default:
		return fmt.Errorf("%w: entry %s %s has op %q", ErrProtocol, e.Tbl, e.PK, e.Op)
	}
	if e.Tbl == "" || e.PK == "" {
		return fmt.Errorf("%w: entry with no table or key", ErrProtocol)
	}
	return nil
}

// refuse turns an error into the reply that carries it. The daemon reads the
// reason off the pipe rather than out of the worker's stderr, which it never
// reads back. The class travels beside the reason so a repeating refusal is
// acted on without parsing its words.
func refuse(req Request, err error) Response {
	return Response{ID: req.ID, OK: false, Error: err.Error(), Code: RefusalCodeOf(err)}
}
