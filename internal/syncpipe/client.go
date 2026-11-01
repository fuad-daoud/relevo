// Package syncpipe is the daemon's half of the sync pipe: a client driving a
// `relevo sync-worker` child over stdio for the log exchange. The child exists
// because a driver abort kills its process, not the daemon's only writer.
package syncpipe

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
	"github.com/fuad-daoud/relevo/internal/syncworker"
)

var (
	// ErrGone is a worker that stopped answering. The pipe cannot be reused after
	// it: a missing reply leaves the frame out of step, so the child is killed and
	// a fresh one started.
	ErrGone = errors.New("the sync worker is gone")

	// ErrRefused is a verb the worker answered and declined, carrying the driver's
	// reason. The worker is alive and in step, so it is not counted as a death. The
	// text is the sync package's const, so corrupt-replica classification recognises
	// it without a shared import.
	ErrRefused = errors.New(relevosync.WorkerRefusedText)

	// errCancelled is a call a deliberate Cancel stopped: not a fault, so the death
	// count is left alone and no backoff follows.
	errCancelled = errors.New("the call was cancelled")
)

const (
	dataCallTimeout    = 10 * time.Minute
	controlCallTimeout = 30 * time.Second
)

// callBound is the bound one call runs under, by the kind of work it moves. Data
// calls move a transfer: the first bootstrap export is a whole database, measured
// at ~300 MB, which on a ~1 MB/s link is five minutes before margin; the 4 MB
// PullBytesThreshold and 256-entry appends with ~1 MB bodies are the same transfer
// piecemeal. Control calls read a page or marker and answer at once, so they keep
// the short bound. It is keyed by verb so a new verb must say its kind; an unnamed
// verb is refused, since a data verb on the short bound would time out a live
// transfer and a control verb on the long one would park a caller.
func callBound(verb syncworker.Verb) (time.Duration, error) {
	switch verb {
	case syncworker.VerbHello, syncworker.VerbExport, syncworker.VerbPull,
		syncworker.VerbPutBlob, syncworker.VerbGetBlob, syncworker.VerbPublishMarks,
		syncworker.VerbPublishTraffic,
		syncworker.VerbPullRange, syncworker.VerbCompact:
		return dataCallTimeout, nil
	case syncworker.VerbHead, syncworker.VerbStats, syncworker.VerbReadMarks,
		syncworker.VerbKeyMeta, syncworker.VerbKeyInit, syncworker.VerbKeyRekey,
		syncworker.VerbKeyCut, syncworker.VerbClaim,
		syncworker.VerbShutdown:
		return controlCallTimeout, nil
	default:
		return 0, fmt.Errorf("syncpipe: verb %q is neither a data nor a control call", verb)
	}
}

// bound is one call's bound: the caller's override when set, the kind's own
// otherwise. A test shortens it to reach an overrun without waiting the real one.
func (c *Client) bound(verb syncworker.Verb) (time.Duration, error) {
	if c.cfg.Timeout > 0 {
		return c.cfg.Timeout, nil
	}
	return callBound(verb)
}

// Config is how a worker is started and what it is started for.
type Config struct {
	// Exe is the program to spawn, this executable by default: a second binary
	// would be a second pipe version to keep in step.
	Exe  string
	Args []string
	// Env is the child's extra environment. The token is never among it: an
	// environment is readable by every local process.
	Env []string
	// Stderr is where the child's stderr goes, the daemon's log. A refusal a caller
	// must act on travels in the reply instead.
	Stderr io.Writer
	// Origin, URL and Token are the handshake. Token reaches the worker in the
	// first pipe request and is not kept here.
	Origin string
	URL    string
	Token  string
	// R2 is the bucket configuration, carried by hello alone like the token: a
	// credential staying here would be one more in daemon memory. Nil is no bucket,
	// and the blob verbs are then refused.
	R2 *syncworker.R2Config
	// BlobStaging is the folder the daemon stages bodies under and the worker reads.
	// It travels in the config because both name the file: a side computing it alone
	// would name one the other never looks at.
	BlobStaging string
	// Timeout overrides every call's kind bound. Zero maps each verb to its kind; a
	// test shortens it to reach an overrun sooner.
	Timeout time.Duration
	// OnMiss reports a worker that stopped answering an accepted call, for breaker
	// counting. A refusal is not a miss and never reaches it.
	OnMiss func(error)
}

// withDefaults fills in what the caller left alone, so a config says only what
// differs.
func (c Config) withDefaults() (Config, error) {
	if c.Exe == "" {
		exe, err := os.Executable()
		if err != nil {
			return Config{}, fmt.Errorf("syncpipe: locate this executable: %w", err)
		}
		c.Exe = exe
	}
	if c.Args == nil {
		c.Args = []string{"sync-worker"}
	}
	if c.Stderr == nil {
		c.Stderr = os.Stderr
	}
	return c, nil
}

// Client is one running worker and the pipe to it: the synclog transport the
// daemon drives the log through. One call is in flight at a time under the mutex:
// two interleaved calls would need reply ids to tell apart, so the simpler rule
// leaves no room for the mistake.
type Client struct {
	cfg      Config
	cmd      *exec.Cmd
	requests *os.File
	replies  *bufio.Reader

	mu       sync.Mutex
	exited   error
	reaped   sync.Once
	waitErr  chan error
	finished chan struct{}
	nextID   int
	// cancelled says a Cancel stopped this client, so an in-flight call releases
	// as a deliberate stop, not a miss.
	cancelled atomic.Bool
}

// The client is the daemon's pipe transport; naming it here turns a lost method
// into a build error.
var _ synclog.LogTransport = (*Client)(nil)

// Start spawns the worker, handshakes, and returns the client once it answers. A
// worker refusing the handshake is not returned: every call over it would answer
// from that refusal.
func Start(cfg Config) (*Client, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}

	childIn, requests, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("syncpipe: pipe to the worker: %w", err)
	}
	replies, childOut, err := os.Pipe()
	if err != nil {
		_ = childIn.Close()
		_ = requests.Close()
		_ = replies.Close()
		return nil, fmt.Errorf("syncpipe: pipe from the worker: %w", err)
	}

	cmd := exec.Command(cfg.Exe, cfg.Args...)
	cmd.Stdin = childIn
	cmd.Stdout = childOut
	cmd.Stderr = cfg.Stderr
	if cfg.Env != nil {
		cmd.Env = append(os.Environ(), cfg.Env...)
	}
	if err := cmd.Start(); err != nil {
		closeAll(childIn, requests, replies, childOut)
		return nil, fmt.Errorf("syncpipe: start %s: %w", cfg.Exe, err)
	}
	// The parent keeps only its own ends of both pipes. Closing the child's
	// copies here is what lets the pipe reach end of file when the worker goes,
	// which is how a dead worker reads as a dead worker and not as a hang.
	_ = childIn.Close()
	_ = childOut.Close()

	c := &Client{
		cfg:      cfg,
		cmd:      cmd,
		requests: requests,
		replies:  bufio.NewReader(replies),
		waitErr:  make(chan error, 1),
		finished: make(chan struct{}),
	}
	go func() {
		c.waitErr <- cmd.Wait()
		close(c.finished)
	}()

	if _, err := c.call(helloRequest(cfg)); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// helloRequest is the handshake: pipe version, origin, remote and token.
func helloRequest(cfg Config) syncworker.Request {
	return syncworker.Request{
		Verb:    syncworker.VerbHello,
		Version: syncworker.ProtocolVersion,
		Origin:  cfg.Origin,
		URL:     cfg.URL,
		Token:   cfg.Token,
		R2:      cfg.R2,
	}
}

// Close ends the pipe with no worker outliving it. Shutdown is asked first for a
// clean exit, then the kill follows anyway: a worker ignoring shutdown must not
// keep the replica open against the next one.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.isGone() {
		// Its own refusal is not this call's failure: the worker goes either way.
		_, _ = c.callLocked(syncworker.Request{Verb: syncworker.VerbShutdown})
	}
	c.kill()
	return nil
}

func (c *Client) Append(entries []synclog.Entry) ([]synclog.Entry, error) {
	return c.AppendSigned(entries, synclog.Boundary{})
}

// AppendSigned is Append with the boundary the exporter predicted for its first
// signed entry.
func (c *Client) AppendSigned(entries []synclog.Entry, boundary synclog.Boundary) ([]synclog.Entry, error) {
	resp, err := c.call(syncworker.Request{
		Verb:        syncworker.VerbExport,
		Entries:     entriesToWire(entries),
		BoundarySeq: boundary.Seq,
		BoundaryTag: boundary.Tag,
	})
	if err != nil {
		return nil, err
	}
	return entriesFromWire(resp.Entries), nil
}

func (c *Client) Pull(marks map[string]int) ([]synclog.Entry, error) {
	resp, err := c.call(syncworker.Request{Verb: syncworker.VerbPull, Marks: marks})
	if err != nil {
		return nil, err
	}
	return entriesFromWire(resp.Entries), nil
}

// Head returns one origin's latest entry per row. The whole head is asked for:
// paging a large one is reconcile's work, and the pipe carries page arguments for
// callers wanting them.
func (c *Client) Head(origin string) ([]synclog.HeadRow, error) {
	resp, err := c.call(syncworker.Request{Verb: syncworker.VerbHead, Origin: origin})
	if err != nil {
		return nil, err
	}
	return headFromWire(resp.Head), nil
}

func (c *Client) Stats() (synclog.Stats, error) {
	resp, err := c.call(syncworker.Request{Verb: syncworker.VerbStats})
	if err != nil {
		return synclog.Stats{}, err
	}
	if resp.Stats == nil {
		return synclog.Stats{}, errors.New("syncpipe: the worker answered stats with no stats")
	}
	return statsFromWire(*resp.Stats), nil
}

// PutBlob uploads the staged body under key unless the bucket already holds it.
// A skip is not a failure: another machine stored the same body, so the referring
// entry may still append.
func (c *Client) PutBlob(key, stagingPath string) (int64, bool, error) {
	resp, err := c.call(syncworker.Request{
		Verb:    syncworker.VerbPutBlob,
		Key:     key,
		Staging: stagingPath,
	})
	if err != nil {
		return 0, false, err
	}
	return resp.Bytes, resp.Skipped, nil
}

// GetBlob fetches the body under key to stagingPath, whole or not at all. A
// missing body is synclog.ErrBlobMissing: the importer latches the origin on it
// instead of retrying what asking again will not restore.
func (c *Client) GetBlob(key, stagingPath string, max int64) (int64, error) {
	resp, err := c.call(syncworker.Request{
		Verb:    syncworker.VerbGetBlob,
		Key:     key,
		Staging: stagingPath,
		Max:     max,
	})
	if err != nil {
		return 0, err
	}
	return resp.Bytes, nil
}

func (c *Client) call(req syncworker.Request) (syncworker.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.callLocked(req)
}

// callLocked is call without the lock, so Close can shut down while holding it.
func (c *Client) callLocked(req syncworker.Request) (syncworker.Response, error) {
	if c.isGone() {
		if c.cancelled.Load() {
			return syncworker.Response{}, fmt.Errorf("syncpipe: %w", errCancelled)
		}
		return syncworker.Response{}, c.gone()
	}
	c.nextID++
	req.ID = fmt.Sprint(c.nextID)

	line, err := json.Marshal(req)
	if err != nil {
		return syncworker.Response{}, fmt.Errorf("syncpipe: marshal %s: %w", req.Verb, err)
	}
	if _, err := c.requests.Write(append(line, '\n')); err != nil {
		return syncworker.Response{}, c.fail(fmt.Errorf("write %s: %w", req.Verb, err))
	}

	resp, err := c.readReply(req.Verb)
	if err != nil {
		return syncworker.Response{}, c.fail(err)
	}
	// The id pairs a reply with its request. A reply naming another means the pipe
	// is out of step, so the worker goes rather than carry on.
	if resp.ID != req.ID {
		return syncworker.Response{}, c.miss(fmt.Errorf(
			"reply %s answers request %s", resp.ID, req.ID))
	}
	if !resp.OK {
		return resp, refusalError(resp)
	}
	return resp, nil
}

// refusalError is the error a declined reply becomes: the worker's reason under
// ErrRefused plus the marked class's sentinel. A classless refusal stays alone,
// which the breaker leaves to the ordinary path rather than latching on.
func refusalError(resp syncworker.Response) error {
	refused := fmt.Errorf("%w: %s", ErrRefused, resp.Error)
	switch resp.Code {
	case syncworker.CodeSchema:
		return fmt.Errorf("%w: %w", refused, relevosync.ErrRemoteSchema)
	case syncworker.CodeRemote:
		return fmt.Errorf("%w: %w", refused, relevosync.ErrRemoteRefused)
	case syncworker.CodeQuota:
		return fmt.Errorf("%w: %w", refused, relevosync.ErrQuotaReached)
	case syncworker.CodeBoundary:
		return fmt.Errorf("%w: %w", refused, synclog.ErrBoundary)
	case syncworker.CodeEpochRace:
		// The remote's epoch moved under this caller: another machine rekeyed
		// first. Nothing was written and no retry with the same material passes,
		// so the answer is the winner's passphrase at unlock.
		return fmt.Errorf("%w: %w", refused, relevosync.ErrEpochRace)
	case syncworker.CodeBlobMissing:
		// The bucket answered and the object is not there: a fact about the bucket,
		// mapping to the latch sentinel instead of a retried refusal.
		return fmt.Errorf("%w: %w", refused, synclog.ErrBlobMissing)
	case syncworker.CodeBlobRefused:
		// The staged file was not the body its key named, so nothing published.
		// Retrying would publish the same wrong body; the staging folder must be
		// rewritten.
		return fmt.Errorf("%w: %w", refused, synclog.ErrInvalid)
	default:
		return refused
	}
}

// readReply reads one reply line, giving up after the timeout. The read runs on
// its own goroutine, not under a file deadline: recovery kills the worker, and a
// held deadline would refuse the next worker too.
func (c *Client) readReply(verb syncworker.Verb) (syncworker.Response, error) {
	type result struct {
		line []byte
		err  error
	}
	// The channel is buffered so an abandoned read still ends: the kill closes the
	// pipe and the goroutine delivers into a slot nobody waits on.
	got := make(chan result, 1)
	go func() {
		line, err := readLine(c.replies)
		got <- result{line: line, err: err}
	}()

	bound, err := c.bound(verb)
	if err != nil {
		return syncworker.Response{}, err
	}
	timer := time.NewTimer(bound)
	defer timer.Stop()
	select {
	case out := <-got:
		if out.err != nil {
			return syncworker.Response{}, fmt.Errorf("read reply: %w", out.err)
		}
		var resp syncworker.Response
		if err := json.Unmarshal(out.line, &resp); err != nil {
			return syncworker.Response{}, fmt.Errorf("decode reply: %w", err)
		}
		return resp, nil
	case <-timer.C:
		return syncworker.Response{}, fmt.Errorf("no reply within %s", bound)
	}
}

// fail reports a call that did not come back. A cancelled call is named as such
// and not counted; every other failure is the miss it was.
func (c *Client) fail(cause error) error {
	if c.cancelled.Load() {
		return fmt.Errorf("syncpipe: %w", errCancelled)
	}
	return c.miss(cause)
}

// cancel stops this client's worker without the call lock, so an in-flight caller
// releases instead of waiting the call out.
func (c *Client) cancel() {
	c.cancelled.Store(true)
	c.kill()
}

// miss ends the worker and reports why, for breaker counting. The worker is killed
// rather than left for the next call to find broken.
func (c *Client) miss(cause error) error {
	c.kill()
	if c.cfg.OnMiss != nil {
		c.cfg.OnMiss(cause)
	}
	return fmt.Errorf("syncpipe: %w", cause)
}

func (c *Client) kill() {
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	c.reap()
	_ = c.requests.Close()
}

func (c *Client) isGone() bool {
	select {
	case <-c.finished:
		return true
	default:
		return false
	}
}

// reap collects the worker's exit status exactly once, so no second reader waits
// on a spent channel.
func (c *Client) reap() {
	c.reaped.Do(func() { c.exited = <-c.waitErr })
}

func (c *Client) exitErr() error {
	c.reap()
	return c.exited
}

// gone is the error over an exited worker, naming the exit status: death on a
// signal differs from refusal to start.
func (c *Client) gone() error {
	err := c.exitErr()
	if err == nil {
		err = ErrGone
	}
	return fmt.Errorf("syncpipe: worker gone: %w", err)
}

// readLine reads one newline-terminated line a byte at a time, so a reply is
// exactly its own line.
func readLine(r io.Reader) ([]byte, error) {
	var out []byte
	one := make([]byte, 1)
	for {
		n, err := r.Read(one)
		if n == 1 {
			if one[0] == '\n' {
				return trimCR(out), nil
			}
			out = append(out, one[0])
			continue
		}
		if err != nil {
			return nil, err
		}
	}
}

func trimCR(line []byte) []byte {
	if n := len(line); n > 0 && line[n-1] == '\r' {
		return line[:n-1]
	}
	return line
}

func closeAll(files ...*os.File) {
	for _, f := range files {
		_ = f.Close()
	}
}
