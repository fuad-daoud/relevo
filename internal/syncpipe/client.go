// Package syncpipe is the daemon's half of the sync pipe: a client that drives
// a `relevo sync-worker` child over its stdin and stdout, and is the transport
// the exporter, importer and reconcile are handed.
//
// The client spawns a child rather than opening a driver itself because a
// driver abort kills its process. Out here that costs one worker, which the
// breaker counts; in the daemon it would cost the record's only writer.
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
	// ErrGone is a worker that has stopped answering. The pipe cannot be used
	// again after it: a reply that never came leaves the frame out of step, so
	// the child is killed and a fresh one started rather than reused.
	ErrGone = errors.New("the sync worker is gone")

	// ErrRefused is a verb the worker answered and declined, carrying the
	// driver's own reason. It is not a miss: the worker is alive and in step,
	// and counting it as a death would latch a breaker over a remote that is
	// answering perfectly well.
	ErrRefused = errors.New("the sync worker refused")

	// errCancelled is a call a deliberate Cancel stopped. It is its own error
	// because a stopped call is not a fault: the supervisor clears the in-call
	// marker and leaves the death count alone, so a machine somebody stopped by
	// hand is not backed off as if its worker had died.
	errCancelled = errors.New("the call was cancelled")
)

// The bounds one call runs under, by the kind of work it moves. A data call --
// hello, export, pull -- moves a transfer: the first bootstrap or join export
// is a whole database, which docs/sync-driver-panic.md measured at ~300 MB, and
// on a ~1 MB/s link that is five minutes before any margin; the 4 MB
// PullBytesThreshold and the 256-entry appends with ~1 MB bodies are the same
// transfer in smaller pieces. A control call -- stats, head, shutdown -- reads
// a page or a marker and answers at once, so it keeps the short bound.
const (
	dataCallTimeout    = 10 * time.Minute
	controlCallTimeout = 30 * time.Second
)

// callBound is the bound one call runs under, by the kind of work it moves. It
// is keyed by verb rather than passed at each call site so a new verb has to
// say which kind it is. A verb it does not name is refused rather than handed a
// default bound: a data verb that silently took the short bound would time out
// a transfer the driver was still moving, and a control verb handed the long
// one would park a caller behind a call that answers at once.
func callBound(verb syncworker.Verb) (time.Duration, error) {
	switch verb {
	case syncworker.VerbHello, syncworker.VerbExport, syncworker.VerbPull,
		syncworker.VerbPutBlob, syncworker.VerbGetBlob:
		return dataCallTimeout, nil
	case syncworker.VerbHead, syncworker.VerbStats, syncworker.VerbShutdown:
		return controlCallTimeout, nil
	default:
		return 0, fmt.Errorf("syncpipe: verb %q is neither a data nor a control call", verb)
	}
}

// bound is the bound one call runs under: the caller's override when one was
// set, the kind's own otherwise. A test shortens it so a call that overruns is
// reached without waiting the real bound out.
func (c *Client) bound(verb syncworker.Verb) (time.Duration, error) {
	if c.cfg.Timeout > 0 {
		return c.cfg.Timeout, nil
	}
	return callBound(verb)
}

// Config is how a worker is started and what it is started for.
type Config struct {
	// Exe is the program to spawn. It is this executable by default, because the
	// worker is a child of the daemon's own image and a second binary on disk
	// would be a second version of the pipe to keep in step.
	Exe string
	// Args follow the program name and name the hidden subcommand.
	Args []string
	// Env is the child's extra environment, on top of this one's. The token is
	// never among it: an environment is readable by every process on this
	// machine, and the pipe exists so that it is not.
	Env []string
	// Stderr is where the child's stderr goes, which is the daemon's log. It is
	// not a channel the daemon reads back: a refusal a caller must act on
	// travels in the reply instead.
	Stderr io.Writer
	// Origin, URL and Token are the handshake. Token reaches the worker in the
	// first request over the pipe and is not kept here afterwards.
	Origin string
	URL    string
	Token  string
	// R2 is the bucket configuration, carried by hello alone for the same reason
	// the token is: a credential that stayed in this process would be one more
	// thing in the daemon's memory, and one that travels on the pipe is one the
	// worker can hold without its argv or environment naming it. Nil is a
	// machine with no bucket, and the blob verbs are then refused.
	R2 *syncworker.R2Config
	// BlobStaging is the folder the daemon writes bodies to and the worker
	// reads them from. It travels in the config rather than being derived in
	// either half because both name the file: the daemon stages under a digest
	// and tells the worker that path, so a folder either side computed on its
	// own would be a file the other side never looks at.
	BlobStaging string
	// Timeout overrides the kind's own call bound for every call. Zero maps
	// each verb to its kind's bound; a test shortens it so a call that overruns
	// is reached without waiting the real bound out.
	Timeout time.Duration
	// OnMiss reports a worker that stopped answering a call it had accepted, so
	// the breaker can count it and back off. A refusal is not a miss and does
	// not reach it.
	OnMiss func(error)
}

// withDefaults fills in what the caller left alone, so a config says what makes
// this worker different rather than repeating the defaults.
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

// Client is one running worker and the pipe to it. It is the synclog transport
// the daemon drives the log through.
//
// One call is in flight at a time, which the mutex enforces rather than the
// protocol: two calls interleaved on one pipe would have two replies in the
// stream and each would have to be told apart by its id, so the simpler rule is
// the one that leaves no room for the mistake.
type Client struct {
	cfg Config
	cmd *exec.Cmd
	// requests is this side of the child's stdin, replies this side of its
	// stdout.
	requests *os.File
	replies  *bufio.Reader

	mu       sync.Mutex
	exited   error
	reaped   sync.Once
	waitErr  chan error
	finished chan struct{}
	nextID   int
	// cancelled says a Cancel stopped this client, so a call it was in the
	// middle of is released as a deliberate stop rather than counted as a miss.
	cancelled atomic.Bool
}

// The client is the daemon's transport over the pipe. Naming it here means a
// method the transport needs cannot be lost without the tree failing to build.
var _ synclog.LogTransport = (*Client)(nil)

// Start spawns the worker, hands it the handshake and returns the client once
// it has answered. A worker that will not open is not returned: a client whose
// handshake was refused has a backend with nothing behind it, and every call
// over it would answer from that refusal.
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

// helloRequest is the handshake: the version this pipe speaks, this
// installation's origin and the remote it works for.
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

// Close ends the pipe and makes sure no worker outlives the call. The shutdown
// is asked for first so a worker that honours it exits cleanly, and the kill
// follows anyway: Close promises the worker is gone when it returns, and a
// worker that ignores a shutdown would otherwise keep the replica open against
// the next one.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.isGone() {
		// Its own refusal is not this call's failure: the worker is going away
		// either way, and the reason it gave is about a shutdown nobody asked.
		_, _ = c.callLocked(syncworker.Request{Verb: syncworker.VerbShutdown})
	}
	c.kill()
	return nil
}

// Append writes one batch and returns it numbered.
func (c *Client) Append(entries []synclog.Entry) ([]synclog.Entry, error) {
	resp, err := c.call(syncworker.Request{
		Verb:    syncworker.VerbExport,
		Entries: entriesToWire(entries),
	})
	if err != nil {
		return nil, err
	}
	return entriesFromWire(resp.Entries), nil
}

// Pull reads every other origin's entries past the marks given.
func (c *Client) Pull(marks map[string]int) ([]synclog.Entry, error) {
	resp, err := c.call(syncworker.Request{Verb: syncworker.VerbPull, Marks: marks})
	if err != nil {
		return nil, err
	}
	return entriesFromWire(resp.Entries), nil
}

// Head returns one origin's latest entry per row.
//
// The whole head is asked for rather than a page: the transport's contract is
// the head of one origin, and reconcile is what pages through a large one when
// it needs to. The pipe carries a page's arguments so that paging has somewhere
// to live when a caller wants it.
func (c *Client) Head(origin string) ([]synclog.HeadRow, error) {
	resp, err := c.call(syncworker.Request{Verb: syncworker.VerbHead, Origin: origin})
	if err != nil {
		return nil, err
	}
	return headFromWire(resp.Head), nil
}

// Stats reports what the log holds.
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

// PutBlob uploads the body staged at stagingPath under key, unless the bucket
// already holds it.
//
// A skip is not a failure and not an empty success: it says another machine
// already stored the body this key names, so the entry that refers to it may be
// appended. The caller learns which it was from the return rather than from an
// error, because the two lead to the same next step and only the byte total
// differs.
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

// GetBlob fetches the body under key to stagingPath, where it appears whole or
// not at all.
//
// A body the bucket does not hold is synclog.ErrBlobMissing rather than a plain
// refusal: the importer latches the origin on it instead of retrying, because the
// object an applied entry names is not there and asking again will not put it
// there.
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

// call sends one request and returns the reply that answers it.
func (c *Client) call(req syncworker.Request) (syncworker.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.callLocked(req)
}

// callLocked is call without taking the lock, so Close can ask for a shutdown
// while holding it.
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
	// The id is what makes a reply this request's. A reply that names another
	// one means the pipe is out of step, and every later reply would be read
	// against the wrong request, so the worker goes rather than carry on.
	if resp.ID != req.ID {
		return syncworker.Response{}, c.miss(fmt.Errorf(
			"reply %s answers request %s", resp.ID, req.ID))
	}
	if !resp.OK {
		return resp, refusalError(resp)
	}
	return resp, nil
}

// refusalError is the error a reply the worker declined becomes: the worker's
// own reason under ErrRefused, and the daemon's sentinel for the class the
// worker marked. A refusal with no class stays a refusal alone, which the
// breaker leaves to the ordinary call path rather than latching on.
func refusalError(resp syncworker.Response) error {
	refused := fmt.Errorf("%w: %s", ErrRefused, resp.Error)
	switch resp.Code {
	case syncworker.CodeSchema:
		return fmt.Errorf("%w: %w", refused, relevosync.ErrRemoteSchema)
	case syncworker.CodeRemote:
		return fmt.Errorf("%w: %w", refused, relevosync.ErrRemoteRefused)
	case syncworker.CodeBlobMissing:
		// The bucket answered and the object is not there. That is a fact about
		// the bucket rather than a fault in reaching it, so it maps to the
		// sentinel the importer latches on instead of staying a refusal the
		// breaker would back off and retry.
		return fmt.Errorf("%w: %w", refused, synclog.ErrBlobMissing)
	case syncworker.CodeBlobRefused:
		// The staged file was not the body its key named, so nothing was
		// published. Retrying the same call would publish the same wrong body,
		// which is why it is a class the caller can act on rather than a plain
		// refusal: the staging folder is what has to be rewritten.
		return fmt.Errorf("%w: %w", refused, synclog.ErrInvalid)
	default:
		return refused
	}
}

// readReply reads one reply line, giving up after the timeout.
//
// The read runs on its own goroutine rather than under a file deadline because
// the way a timed-out call recovers is to kill the worker: the pipe is about to
// have no other end, and a deadline the goroutine were still holding would be a
// reason for the next worker to be refused as well.
func (c *Client) readReply(verb syncworker.Verb) (syncworker.Response, error) {
	type result struct {
		line []byte
		err  error
	}
	// The channel is buffered so a read that the timeout abandoned still ends:
	// the kill closes the pipe, the read returns, and the goroutine delivers
	// into a slot nobody has to be waiting for.
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

// fail reports a call that did not come back. A call this client was cancelled
// during is named as such, so the supervisor drops the worker without counting
// a death; every other failure is the miss it was.
func (c *Client) fail(cause error) error {
	if c.cancelled.Load() {
		return fmt.Errorf("syncpipe: %w", errCancelled)
	}
	return c.miss(cause)
}

// cancel stops the worker this client drives without taking the call lock, so a
// caller waiting on a call in flight is released rather than made to wait the
// call out. The lock is what a call holds for its whole life, so taking it here
// would be the wait itself.
func (c *Client) cancel() {
	c.cancelled.Store(true)
	c.kill()
}

// miss ends the worker and reports why, so the breaker can count this call. The
// worker is killed rather than left to be found broken by the next call: the
// pipe is out of step until the process that can answer goes.
func (c *Client) miss(cause error) error {
	c.kill()
	if c.cfg.OnMiss != nil {
		c.cfg.OnMiss(cause)
	}
	return fmt.Errorf("syncpipe: %w", cause)
}

// kill ends the worker and reaps it.
func (c *Client) kill() {
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	c.reap()
	_ = c.requests.Close()
}

// isGone reports whether the worker process has exited, without blocking.
func (c *Client) isGone() bool {
	select {
	case <-c.finished:
		return true
	default:
		return false
	}
}

// reap collects the worker's exit status exactly once, so a second reader is
// not left waiting on a channel that will not be written again.
func (c *Client) reap() {
	c.reaped.Do(func() { c.exited = <-c.waitErr })
}

// exitErr is how the worker ended.
func (c *Client) exitErr() error {
	c.reap()
	return c.exited
}

// gone is the error a call raises over a worker that has already exited, naming
// the exit status because a worker that died on a signal is a different fault
// from one that refused to start.
func (c *Client) gone() error {
	err := c.exitErr()
	if err == nil {
		err = ErrGone
	}
	return fmt.Errorf("syncpipe: worker gone: %w", err)
}

// readLine reads one newline-terminated line a byte at a time, so a reply is
// exactly its own line and not whatever the pipe had buffered behind it.
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
