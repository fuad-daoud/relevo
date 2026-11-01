package syncpipe

// The supervisor is the daemon's half of the worker's life: one worker at a time,
// the in-call marker around every call, a count of calls never returning, and a
// drop of unusable pipes. It is the synclog transport the exchange is handed, so
// driving and failure meaning live in one place.

import (
	"context"
	"errors"
	"sync"
	"time"

	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// Supervisor drives a worker through the breaker's accounting, one call at a time
// under its lock, since the pipe carries one request in flight.
type Supervisor struct {
	cfg     Config
	breaker *relevosync.Breaker

	mu sync.Mutex
	// clientMu guards client, and is separate from mu so Cancel can reach the
	// worker a call is blocked on without waiting that call out.
	clientMu sync.Mutex
	client   *Client
}

func NewSupervisor(cfg Config, b *relevosync.Breaker) *Supervisor {
	return &Supervisor{cfg: cfg, breaker: b}
}

// NewSyncRunner returns the daemon's sync holder: a runner on a supervisor
// starting workers from cfg through the local breaker. It is the one construction
// site, so a daemon holds a single worker. cfg stays empty until an enable decides
// the remote: construction starts no process.
func NewSyncRunner(cfg Config, local relevosync.Local) *relevosync.Runner {
	return &relevosync.Runner{
		Client: NewSupervisor(cfg, relevosync.NewBreaker(local)),
		Local:  local,
	}
}

// The supervisor is the driven transport; naming it here turns a lost method into
// a build error. It is also the blob mover: the entry numberer is the body storer,
// so no caller pairs an append path with an absent bucket.
var _ synclog.LogTransport = (*Supervisor)(nil)
var _ synclog.BlobTransport = (*Supervisor)(nil)

// Ready reports whether this supervisor names a remote and a token. It reads the
// config and starts nothing.
func (s *Supervisor) Ready() bool { return s.cfg.URL != "" && s.cfg.Token != "" }

func (s *Supervisor) Append(entries []synclog.Entry) ([]synclog.Entry, error) {
	return s.AppendSigned(entries, synclog.Boundary{})
}

// AppendSigned is Append with the boundary the exporter predicted for its first
// signed entry.
func (s *Supervisor) AppendSigned(entries []synclog.Entry, boundary synclog.Boundary) ([]synclog.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []synclog.Entry
	err := s.drive("export", func(c *Client) error {
		var e error
		out, e = c.AppendSigned(entries, boundary)
		return e
	})
	return out, err
}

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

// PublishMarks records how far this installation applied of each origin, through
// the breaker like every call: a worker silent on a publish is dead whatever it
// was doing.
func (s *Supervisor) PublishMarks(marks map[string]int, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.drive("publish_marks", func(c *Client) error { return c.PublishMarks(marks, at) })
}

// PublishTraffic writes this installation's traffic row and answers with every
// origin's, through the breaker for the reason PublishMarks gives.
func (s *Supervisor) PublishTraffic(row synclog.TrafficRow) (map[string]synclog.TrafficRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out map[string]synclog.TrafficRow
	err := s.drive("publish_traffic", func(c *Client) error {
		var e error
		out, e = c.PublishTraffic(row)
		return e
	})
	return out, err
}

// The traffic publisher is asserted at use, so a lost method would build green
// while silently stopping publishing; these make that a build error.
var (
	_ synclog.TrafficPublisher = (*Supervisor)(nil)
	_ synclog.TrafficPublisher = (*Client)(nil)
)

func (s *Supervisor) ReadMarks() (synclog.MarkState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out synclog.MarkState
	err := s.drive("read_marks", func(c *Client) error {
		var e error
		out, e = c.ReadMarks()
		return e
	})
	return out, err
}

func (s *Supervisor) PullRange(origin string, after, upto int) ([]synclog.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []synclog.Entry
	err := s.drive("pull_range", func(c *Client) error {
		var e error
		out, e = c.PullRange(origin, after, upto)
		return e
	})
	return out, err
}

func (s *Supervisor) CompactOwn(floor int) (synclog.CompactResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out synclog.CompactResult
	err := s.drive("compact", func(c *Client) error {
		var e error
		out, e = c.CompactOwn(floor)
		return e
	})
	return out, err
}

func (s *Supervisor) KeyMeta(ctx context.Context) (synclog.KeyMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out synclog.KeyMeta
	err := s.drive("key_meta", func(c *Client) error {
		var e error
		out, e = c.KeyMeta(ctx)
		return e
	})
	return out, err
}

// KeyInit writes the passphrase material where the remote holds none, answering
// with what it holds.
func (s *Supervisor) KeyInit(ctx context.Context, key synclog.KeyMeta) (synclog.KeyMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out synclog.KeyMeta
	err := s.drive("key_init", func(c *Client) error {
		var e error
		out, e = c.KeyInit(ctx, key)
		return e
	})
	return out, err
}

func (s *Supervisor) KeyRekey(ctx context.Context, spec synclog.KeyRekeySpec) (synclog.KeyMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out synclog.KeyMeta
	err := s.drive("key_rekey", func(c *Client) error {
		var e error
		out, e = c.KeyRekey(ctx, spec)
		return e
	})
	return out, err
}

func (s *Supervisor) PublishEpochCut(ctx context.Context, cut synclog.EpochCut) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.drive("key_cut", func(c *Client) error { return c.PublishEpochCut(ctx, cut) })
}

// ClaimIdentity records this machine's claim on origin; an empty token only reads.
func (s *Supervisor) ClaimIdentity(ctx context.Context, origin, token string, at time.Time) (string, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var winner string
	var claimedAt time.Time
	err := s.drive("claim_identity", func(c *Client) error {
		var e error
		winner, claimedAt, e = c.ClaimIdentity(ctx, origin, token, at)
		return e
	})
	return winner, claimedAt, err
}

// BlobStagingDir is the folder bodies stage in before a put: the supervisor's own,
// so writer and worker name it from one place.
func (s *Supervisor) BlobStagingDir() string { return s.cfg.BlobStaging }

// PutBlob uploads the staged body under key, through the breaker like every call:
// a worker silent on a put is dead whatever it was doing.
func (s *Supervisor) PutBlob(key, stagingPath string) (int64, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var (
		bytes   int64
		skipped bool
	)
	err := s.drive("put_blob", func(c *Client) error {
		var e error
		bytes, skipped, e = c.PutBlob(key, stagingPath)
		return e
	})
	return bytes, skipped, err
}

func (s *Supervisor) GetBlob(key, stagingPath string, max int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	err := s.drive("get_blob", func(c *Client) error {
		var e error
		n, e = c.GetBlob(key, stagingPath, max)
		return e
	})
	return n, err
}

// Cancel stops the current worker and keeps nothing to reuse, without the call
// lock, so a cancel during a call returns at once. The kill releases that call on
// the deliberate-stop path, uncounted; the next call starts fresh rather than
// driving a dead pipe.
func (s *Supervisor) Cancel() {
	c := s.current()
	if c == nil {
		return
	}
	s.forget(c)
	c.cancel()
}

// Close stops the worker for good, on shutdown and before re-exec, so no worker
// outlives its daemon.
func (s *Supervisor) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.drop()
}

// drive runs one call through the current worker under the breaker's accounting.
// The marker is written before and settled after, deciding whether the worker is
// kept.
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

// settle records what the call did. Success or refusal clears the marker and keeps
// the worker; a call never returning drops the worker and counts the death. A
// Cancelled call clears the marker uncounted.
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

// current returns the worker in use without waiting out a call: Cancel reaches a
// blocked call's worker this way, not through mu.
func (s *Supervisor) current() *Client {
	s.clientMu.Lock()
	defer s.clientMu.Unlock()
	return s.client
}

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

// drop stops the current worker for good and forgets it. Shutdown is asked first
// for a clean exit; a call-held worker is reached by Cancel instead.
func (s *Supervisor) drop() error {
	c := s.current()
	if c == nil {
		return nil
	}
	s.forget(c)
	return c.Close()
}
