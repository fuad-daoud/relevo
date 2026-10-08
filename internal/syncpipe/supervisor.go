package syncpipe

// The supervisor is the daemon's half of the worker's life: it drives one worker
// at a time, writes the in-call marker around every call, counts a call that
// does not come back, and drops a worker the pipe can no longer use so the next
// call gets a fresh process. It is the synclog transport the exporter, importer
// and reconcile are handed, so the daemon has one place that both drives the
// pipe and decides what a failed call means.

import (
	"errors"
	"sync"

	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// Supervisor drives a worker through the breaker's accounting. One call runs at
// a time under its lock, so two triggers sharing it cannot interleave on a pipe
// that carries one request in flight.
type Supervisor struct {
	cfg     Config
	breaker *relevosync.Breaker

	// mu serializes calls: one call runs at a time because the pipe carries one
	// request at a time.
	mu sync.Mutex
	// clientMu guards client, and is separate from mu so Cancel can reach the
	// worker a call is blocked on without waiting that call out.
	clientMu sync.Mutex
	client   *Client
}

// NewSupervisor returns a supervisor that starts workers from cfg and keeps
// their accounting in b.
func NewSupervisor(cfg Config, b *relevosync.Breaker) *Supervisor {
	return &Supervisor{cfg: cfg, breaker: b}
}

// NewSyncRunner returns the daemon's sync holder: a runner whose log transport
// is a supervisor that starts workers from cfg and accounts for them through the
// machine-local kv local names. It is the one place a pipe client is
// constructed, so a daemon holds a single worker however many calls it carries.
// cfg names what the worker is started with; a caller leaves it empty until an
// enable has decided the remote, because construction alone starts no process.
func NewSyncRunner(cfg Config, local relevosync.Local) *relevosync.Runner {
	return &relevosync.Runner{
		Client: NewSupervisor(cfg, relevosync.NewBreaker(local)),
		Local:  local,
	}
}

// The supervisor is the transport the exchange drives. Naming it here means a
// method the transport needs cannot be lost without the tree failing to build.
var _ synclog.LogTransport = (*Supervisor)(nil)

// Append writes one batch of this origin's entries.
func (s *Supervisor) Append(entries []synclog.Entry) ([]synclog.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []synclog.Entry
	err := s.drive("export", func(c *Client) error {
		var e error
		out, e = c.Append(entries)
		return e
	})
	return out, err
}

// Pull reads every other origin's entries past the marks given.
func (s *Supervisor) Pull(marks map[string]int) ([]synclog.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []synclog.Entry
	err := s.drive("pull", func(c *Client) error {
		var e error
		out, e = c.Pull(marks)
		return e
	})
	return out, err
}

// Head returns one origin's latest entry per row.
func (s *Supervisor) Head(origin string) ([]synclog.HeadRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []synclog.HeadRow
	err := s.drive("head", func(c *Client) error {
		var e error
		out, e = c.Head(origin)
		return e
	})
	return out, err
}

// Stats reports what the log holds.
func (s *Supervisor) Stats() (synclog.Stats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out synclog.Stats
	err := s.drive("stats", func(c *Client) error {
		var e error
		out, e = c.Stats()
		return e
	})
	return out, err
}

// Cancel stops the current worker and keeps nothing to reuse. It does not take
// the call lock, so a cancel issued while a call is in flight returns at once:
// the kill releases that call, which settles once on the deliberate-stop path
// and is not counted as a death. The next call starts a fresh process rather
// than driving a pipe whose other end is gone.
func (s *Supervisor) Cancel() {
	c := s.current()
	if c == nil {
		return
	}
	s.forget(c)
	c.cancel()
}

// Close stops the worker for good. It is what the daemon runs on shutdown and
// before a re-exec, so no worker outlives the daemon that started it.
func (s *Supervisor) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.drop()
}

// drive runs one call through the current worker under the breaker's
// accounting. The marker is written before the call and settled once the call
// returns, which is what decides whether the worker is kept.
func (s *Supervisor) drive(verb string, fn func(*Client) error) error {
	if err := s.breaker.Begin(verb); err != nil {
		return err
	}
	c, err := s.worker()
	if err != nil {
		return s.settle(nil, err)
	}
	return s.settle(c, fn(c))
}

// worker returns the running worker, starting one if none is open.
func (s *Supervisor) worker() (*Client, error) {
	if c := s.current(); c != nil {
		return c, nil
	}
	c, err := Start(s.cfg)
	if err != nil {
		return nil, err
	}
	s.install(c)
	return c, nil
}

// settle records what the call did. A reply -- success or refusal -- clears the
// in-call marker and keeps the worker; a call that did not come back drops the
// worker and counts the death the marker names. A call stopped by Cancel is the
// one deliberate stop: it clears the marker so the next start does not count it,
// and leaves the death count alone.
func (s *Supervisor) settle(c *Client, err error) error {
	switch {
	case err == nil:
		if eerr := s.breaker.End(); eerr != nil {
			return eerr
		}
		return s.breaker.Success()
	case errors.Is(err, ErrRefused):
		if eerr := s.breaker.End(); eerr != nil {
			return errors.Join(err, eerr)
		}
		if rerr := s.breaker.Refused(err); rerr != nil {
			return errors.Join(err, rerr)
		}
		return err
	case errors.Is(err, errCancelled):
		s.forget(c)
		if eerr := s.breaker.End(); eerr != nil {
			return errors.Join(err, eerr)
		}
		return err
	default:
		s.forget(c)
		if oerr := s.breaker.Observe(); oerr != nil {
			return errors.Join(err, oerr)
		}
		return err
	}
}

// current returns the worker a call would use, without waiting for one in
// flight: Cancel reaches a blocked call's worker this way instead of through mu.
func (s *Supervisor) current() *Client {
	s.clientMu.Lock()
	defer s.clientMu.Unlock()
	return s.client
}

// install makes c the worker calls use.
func (s *Supervisor) install(c *Client) {
	s.clientMu.Lock()
	s.client = c
	s.clientMu.Unlock()
}

// forget drops c as the worker calls use, leaving any other worker in place.
func (s *Supervisor) forget(c *Client) {
	s.clientMu.Lock()
	if s.client == c {
		s.client = nil
	}
	s.clientMu.Unlock()
}

// drop stops the current worker for good and forgets it. The shutdown request
// comes first so a worker that honours it exits cleanly; a worker a call holds
// is reached by Cancel instead, which kills it without the call lock.
func (s *Supervisor) drop() error {
	c := s.current()
	if c == nil {
		return nil
	}
	s.forget(c)
	return c.Close()
}
