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

	mu     sync.Mutex
	client *Client
}

// NewSupervisor returns a supervisor that starts workers from cfg and keeps
// their accounting in b.
func NewSupervisor(cfg Config, b *relevosync.Breaker) *Supervisor {
	return &Supervisor{cfg: cfg, breaker: b}
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

// Cancel stops the current worker and keeps nothing to reuse. A cancelled call
// is a deliberate stop, not a fault, so it is not counted; the next call starts
// a fresh process rather than driving a pipe whose other end is gone.
func (s *Supervisor) Cancel() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.drop()
}

// Close stops the worker for good. It is what the daemon runs on shutdown and
// before a re-exec, so no worker outlives the daemon that started it.
func (s *Supervisor) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client == nil {
		return nil
	}
	err := s.client.Close()
	s.client = nil
	return err
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
		return s.settle(err)
	}
	return s.settle(fn(c))
}

// worker returns the running worker, starting one if none is open.
func (s *Supervisor) worker() (*Client, error) {
	if s.client != nil {
		return s.client, nil
	}
	c, err := Start(s.cfg)
	if err != nil {
		return nil, err
	}
	s.client = c
	return c, nil
}

// settle records what the call did. A reply -- success or refusal -- clears the
// in-call marker and keeps the worker; a call that did not come back drops the
// worker and counts the death the marker names.
func (s *Supervisor) settle(err error) error {
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
	default:
		s.client = nil
		if oerr := s.breaker.Observe(); oerr != nil {
			return errors.Join(err, oerr)
		}
		return err
	}
}

// drop kills the current worker and forgets it.
func (s *Supervisor) drop() {
	if s.client != nil {
		_ = s.client.Close()
		s.client = nil
	}
}
