package syncworker

import (
	"bufio"
	"errors"
	"fmt"
	"io"
)

// Spec is what hello tells the worker, and the only way it ever learns what it
// is working for: the origin it appends as, and the remote it reaches. The
// token arrives here and nowhere else, so the worker process holds it without
// its command line or its environment naming it.
type Spec struct {
	// Version is the pipe version the daemon speaks.
	Version int
	// Origin is the installation this worker appends as, and the only origin
	// whose entries an export may carry.
	Origin string
	// URL and Token reach the remote.
	URL   string
	Token string
}

// Backend is what the worker serves the pipe with: the log itself, behind the
// replica this process owns.
//
// Open comes before every other call, and only hello reaches it, so a backend
// never holds a remote handle for a request that did not name one. Close runs
// when the loop ends for any reason, including a pipe the daemon walked away
// from, because a replica left open is the next worker locked out of it.
type Backend interface {
	// Open takes the remote for the rest of this worker's life.
	Open(Spec) error
	// Append writes one batch of this origin's entries as a single atomic
	// write, numbering them from one past the highest sequence this origin
	// already holds, and returns them numbered. Entries of another origin are
	// refused: an installation appends only for the rows it owns.
	Append(entries []Entry) ([]Entry, error)
	// Pull returns every other origin's entries past the mark given for it, in
	// sequence order and in whole batches. An origin with no mark is read from
	// the start of its log.
	Pull(marks map[string]int) ([]Entry, error)
	// Head returns a page of one origin's latest entry per row, starting after
	// the given key. An empty after starts at the beginning and a limit of zero
	// means the whole rest.
	Head(origin, after string, limit int) ([]HeadRow, error)
	// Stats reports what the log holds.
	Stats() (Stats, error)
	// Close releases the remote and the replica.
	Close() error
}

// server is one worker's state between requests: the backend, and the spec
// hello filled in. It holds nothing before hello, so a verb that arrives early
// has no origin to append as and no remote to answer from.
type server struct {
	backend Backend
	spec    *Spec
}

// Serve answers requests from in until the pipe ends, writing one reply per
// request to out.
//
// The loop ends three ways, and all three close the backend: stdin closes (the
// daemon went away or asked to stop), a verb says shutdown, or a line arrives
// that is not a request this pipe speaks. The last is the only one that is not
// an ordinary exit, because a line that cannot be read has no id to answer, so
// the caller has to learn about it by the worker dying.
func Serve(in io.Reader, out io.Writer, b Backend) error {
	r := bufio.NewReader(in)
	w := bufio.NewWriter(out)
	s := &server{backend: b}

	err := s.loop(r, w)
	// The close error is reported only when the loop itself ended cleanly, so a
	// pipe that failed is not reported as a replica that would not shut.
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

// dispatch answers one request. A verb it refuses is answered with a reply and
// the pipe carries on: the daemon asked a question, and a question with an
// answer it did not expect is not a broken pipe.
func (s *server) dispatch(req Request) Response {
	if s.spec == nil && req.Verb != VerbHello {
		return refuse(req, ErrNoHello)
	}
	switch req.Verb {
	case VerbHello:
		return s.hello(req)
	case VerbExport:
		return s.export(req)
	case VerbPull:
		return s.pull(req)
	case VerbHead:
		return s.head(req)
	case VerbStats:
		return s.stats(req)
	case VerbShutdown:
		return Response{ID: req.ID, OK: true}
	default:
		return refuse(req, fmt.Errorf("%w: verb %q", ErrProtocol, req.Verb))
	}
}

// hello opens the backend for the rest of the pipe and takes the origin every
// later request is checked against. A version the worker does not speak is
// refused here rather than answered into: every answer after it would be in a
// vocabulary the daemon would read wrongly.
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
	spec := Spec{Version: req.Version, Origin: req.Origin, URL: req.URL, Token: req.Token}
	if err := s.backend.Open(spec); err != nil {
		return refuse(req, err)
	}
	s.spec = &spec
	return Response{ID: req.ID, OK: true}
}

// export appends one batch, after refusing anything the log would not take. The
// refusals happen here rather than in the backend so they are the worker's own
// rule, stated once: the batch is this origin's, and every entry in it has the
// shape the log defines.
func (s *server) export(req Request) Response {
	for _, e := range req.Entries {
		if e.Origin != s.spec.Origin {
			return refuse(req, fmt.Errorf("export %s %s: %w", e.Tbl, e.PK, ErrForeignOrigin))
		}
		if err := checkEntry(e); err != nil {
			return refuse(req, err)
		}
	}
	written, err := s.backend.Append(req.Entries)
	if err != nil {
		return refuse(req, err)
	}
	return Response{ID: req.ID, OK: true, Entries: written}
}

// pull answers with the entries of every origin but this one that follow the
// marks given.
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

// stats answers with what the log holds.
func (s *server) stats(req Request) Response {
	stats, err := s.backend.Stats()
	if err != nil {
		return refuse(req, err)
	}
	return Response{ID: req.ID, OK: true, Stats: &stats}
}

// checkEntry refuses an entry the log has no room for: an unknown op, a delete
// carrying a body, or an upsert carrying none. Both directions are refused here,
// because an entry that reached the backend with the wrong shape has already
// been read off a pipe and would be written before anything looked again.
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
// reason off the pipe rather than out of the worker's stderr, which is the
// daemon's log and is not a channel it reads back. The class travels beside the
// reason so a refusal that will repeat is acted on without parsing its words.
func refuse(req Request, err error) Response {
	return Response{ID: req.ID, OK: false, Error: err.Error(), Code: RefusalCodeOf(err)}
}
