package syncpipe

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
	"github.com/fuad-daoud/relevo/internal/syncworker"
)

// The fake worker is this test binary running itself as a worker, so the client
// under test spawns a real process and speaks a real pipe. A worker written as
// a function would be a call the client happens to make; a worker that is a
// process can die on cue, which is the half of the contract a function call
// cannot exercise.
const (
	fakeWorkerEnv = "RELEVO_SYNCPIPE_FAKE_WORKER"
	// modeHang answers the handshake and then never answers again, which is what
	// a driver wedged inside a call looks like from the daemon's side.
	modeHang = "hang"
	// modeHangOnce answers the handshake and never answers its first stats
	// call, then answers everything else. A worker started after that one is a
	// new process whose first stats call is still to come, so it answers, which
	// is how a fresh process is told from a reused one.
	modeHangOnce = "hang-once"
	// modeDie answers the handshake and exits, which is what a driver abort
	// looks like from the daemon's side.
	modeDie = "die"
	// modeRefuse refuses the handshake with a class that repeats, which is what
	// a remote that is not a relevo log looks like from the daemon's side.
	modeRefuse = "refuse"
	// modeRefuseVerbs answers the handshake and then refuses every verb with a
	// class that repeats, which is a remote that accepted the pipe and then
	// refused the log.
	modeRefuseVerbs = "refuse-verbs"
	// modeRefuseTransient answers the handshake and then refuses every verb
	// with no class, which is a refusal a later attempt can still get past.
	modeRefuseTransient = "refuse-transient"
	// modeWrongID answers the handshake for a request that is not the one it
	// read, which is what a crossed pipe looks like from the daemon's side.
	modeWrongID = "wrong-id"
	// modeStaleID answers the handshake honestly and then answers every later
	// request with the handshake's id, which is a stream one reply out of step:
	// each reply is a real reply, to a request that is already answered.
	modeStaleID = "stale-id"
	// modeNormal is the worker the client is meant to drive.
	modeNormal = "normal"
)

// TestMain hands this binary over to the fake worker before the suite starts,
// and only for the invocation that asked for it. The variable is read once and
// cleared, so a child that went on to run the suite could not hand itself over
// again.
func TestMain(m *testing.M) {
	mode := os.Getenv(fakeWorkerEnv)
	if mode == "" {
		os.Exit(m.Run())
	}
	_ = os.Unsetenv(fakeWorkerEnv)
	os.Exit(runFakeWorker(mode))
}

// runFakeWorker serves the pipe in one mode and reports the exit code the client
// should see. A refusal is a worker that stays up to be asked again; the
// hanging one never returns on its own and is killed by the client that gave up
// on it.
func runFakeWorker(mode string) int {
	switch mode {
	case modeHang:
		if !answerHandshake(sameID) {
			return 1
		}
		for {
			time.Sleep(time.Hour)
		}
	case modeHangOnce:
		if err := syncworker.Serve(os.Stdin, os.Stdout, &hangOnceBackend{}); err != nil {
			return 1
		}
		return 0
	case modeDie:
		if !answerHandshake(sameID) {
			return 1
		}
		// Read the next request and exit without answering it: the worker dies
		// mid-call, so the client's read meets the closed pipe rather than a
		// process that was already gone when the call was written.
		if _, err := readLine(os.Stdin); err != nil {
			return 1
		}
		return 0
	case modeWrongID:
		if !answerHandshake(otherID) {
			return 1
		}
		return 0
	case modeStaleID:
		return serveStaleID()
	case modeRefuse:
		if err := syncworker.Serve(os.Stdin, os.Stdout, refusingBackend{code: syncworker.CodeRemote}); err != nil {
			return 1
		}
		return 0
	case modeRefuseVerbs:
		if err := syncworker.Serve(os.Stdin, os.Stdout, verbsRefusingBackend{code: syncworker.CodeRemote}); err != nil {
			return 1
		}
		return 0
	case modeRefuseTransient:
		if err := syncworker.Serve(os.Stdin, os.Stdout, verbsRefusingBackend{}); err != nil {
			return 1
		}
		return 0
	default:
		if err := syncworker.Serve(os.Stdin, os.Stdout, normalBackend{}); err != nil {
			fmt.Fprintln(os.Stderr, "fake worker:", err)
			return 1
		}
		return 0
	}
}

// answerHandshake reads the one request these modes answer and replies to it,
// naming the reply's id as answerID says. It is the handshake read by hand
// rather than through the loop because these modes are about what happens to
// the worker around a reply, not about serving one.
func answerHandshake(answerID func(string) string) bool {
	line, err := readLine(os.Stdin)
	if err != nil {
		return false
	}
	var req syncworker.Request
	if err := json.Unmarshal(line, &req); err != nil {
		return false
	}
	out, err := json.Marshal(syncworker.Response{ID: answerID(req.ID), OK: true})
	if err != nil {
		return false
	}
	_, err = os.Stdout.Write(append(out, '\n'))
	return err == nil
}

// sameID answers the request that was read; otherID answers one that is not.
func sameID(id string) string { return id }

func otherID(string) string { return "not-the-request-that-was-sent" }

// serveStaleID answers the handshake honestly and then answers everything else
// with the handshake's own id. Every reply is well-formed and true of some
// request, which is exactly what makes it a trap: a client that matched on
// "a reply arrived" rather than on "a reply arrived for me" would take the
// handshake's answer as the answer to the call it is still waiting on.
func serveStaleID() int {
	if !answerHandshake(sameID) {
		return 1
	}
	for {
		if _, err := readLine(os.Stdin); err != nil {
			return 0
		}
		// The first id the pipe ever saw belonged to the handshake, which is
		// already answered, so every reply from here names a spent request.
		out, err := json.Marshal(syncworker.Response{
			ID: "1", OK: true, Stats: &syncworker.Stats{},
		})
		if err != nil {
			return 1
		}
		if _, err := os.Stdout.Write(append(out, '\n')); err != nil {
			return 1
		}
	}
}

// pinnedAt is the moment the fake log's entries were written, so what crosses
// the pipe is a value the test can name rather than a clock reading.
var pinnedAt = time.Date(2026, 10, 8, 9, 30, 0, 0, time.UTC)

// normalBackend is the log the well-behaved fake worker serves. It answers with
// fixed entries, rows and counts, so a test asserts what crossed the pipe rather
// than what the worker happened to hold.
type normalBackend struct{}

func (normalBackend) Open(syncworker.Spec) error { return nil }

// Append numbers the batch the way a transport must: from one past the highest
// sequence the origin holds, ignoring whatever the request carried.
func (normalBackend) Append(entries []syncworker.Entry) ([]syncworker.Entry, error) {
	out := make([]syncworker.Entry, 0, len(entries))
	for i, e := range entries {
		e.Seq = i + 1
		e.Batch = 1
		out = append(out, e)
	}
	return out, nil
}

func (normalBackend) Pull(map[string]int) ([]syncworker.Entry, error) {
	return []syncworker.Entry{{
		Origin: "origin-b", Seq: 4, Batch: 4, Tbl: "task", PK: `["t"]`,
		Op: "upsert", SchemaVersion: 3,
		Body: json.RawMessage(`{"title":"theirs"}`), At: pinnedAt,
	}}, nil
}

func (normalBackend) Head(string, string, int) ([]syncworker.HeadRow, error) {
	return []syncworker.HeadRow{{Tbl: "task", PK: `["t"]`, Seq: 7, Hash: "abc"}}, nil
}

func (normalBackend) Stats() (syncworker.Stats, error) {
	return syncworker.Stats{Entries: 11, Origins: 2, Seq: 9}, nil
}

func (normalBackend) Close() error { return nil }

// errRemoteRefused is the reason a refused remote carries, in the words a
// remote would use. The reason is what a caller acts on, so it is the thing
// pinned; the class beside it is what the daemon classifies on.
var errRemoteRefused = errors.New("remote refused: that database is not a relevo log")

// refusingBackend refuses every verb, the handshake included, with the class it
// carries. An empty class is a refusal a later attempt can still get past; a
// non-empty one is a refusal that repeats.
type refusingBackend struct{ code syncworker.RefusalCode }

func (r refusingBackend) fail() error { return syncworker.MarkRefusal(r.code, errRemoteRefused) }

func (r refusingBackend) Open(syncworker.Spec) error { return r.fail() }

func (r refusingBackend) Append([]syncworker.Entry) ([]syncworker.Entry, error) {
	return nil, r.fail()
}

func (r refusingBackend) Pull(map[string]int) ([]syncworker.Entry, error) {
	return nil, r.fail()
}

func (r refusingBackend) Head(string, string, int) ([]syncworker.HeadRow, error) {
	return nil, r.fail()
}

func (r refusingBackend) Stats() (syncworker.Stats, error) {
	return syncworker.Stats{}, r.fail()
}

func (r refusingBackend) Close() error { return nil }

// verbsRefusingBackend answers the handshake and then refuses every verb with
// the class it carries, which is what a remote that accepted the pipe and then
// refused the log looks like from the daemon's side.
type verbsRefusingBackend struct{ code syncworker.RefusalCode }

func (verbsRefusingBackend) Open(syncworker.Spec) error { return nil }

func (r verbsRefusingBackend) fail() error { return syncworker.MarkRefusal(r.code, errRemoteRefused) }

func (r verbsRefusingBackend) Append([]syncworker.Entry) ([]syncworker.Entry, error) {
	return nil, r.fail()
}

func (r verbsRefusingBackend) Pull(map[string]int) ([]syncworker.Entry, error) {
	return nil, r.fail()
}

func (r verbsRefusingBackend) Head(string, string, int) ([]syncworker.HeadRow, error) {
	return nil, r.fail()
}

func (r verbsRefusingBackend) Stats() (syncworker.Stats, error) {
	return syncworker.Stats{}, r.fail()
}

func (r verbsRefusingBackend) Close() error { return nil }

// hangOnceBackend answers everything but its first stats call, which it never
// answers. A worker started after a cancel is a new process with its own first
// call, so it answers, which is how a fresh process is told from a reused one.
type hangOnceBackend struct {
	normalBackend
	mu   sync.Mutex
	hung bool
}

func (b *hangOnceBackend) Stats() (syncworker.Stats, error) {
	b.mu.Lock()
	first := !b.hung
	b.hung = true
	b.mu.Unlock()
	if first {
		time.Sleep(time.Hour)
	}
	return b.normalBackend.Stats()
}

// handshake is what every test starts a worker with: an origin, a remote and a
// token that reaches the worker over the pipe and nowhere else.
func handshake() Config {
	return Config{
		Origin:  "origin-a",
		URL:     "libsql://remote.example",
		Token:   "the-token",
		Stderr:  io.Discard,
		Timeout: 10 * time.Second,
	}
}

// startFakeWorker spawns this test binary as a worker in the given mode and
// returns the client over it. The mode is named through the environment rather
// than the arguments because the client's arguments are the hidden subcommand's,
// which a real spawn fills in itself; leaving them unset here also keeps the
// default under test.
func startFakeWorker(t *testing.T, mode string, cfg Config) (*Client, error) {
	t.Helper()
	cfg.Exe = os.Args[0]
	cfg.Env = append(cfg.Env, fakeWorkerEnv+"="+mode)
	c, err := Start(cfg)
	if c != nil {
		t.Cleanup(func() { _ = c.Close() })
	}
	return c, err
}

// TestPipeClientDrivesAWorkerOverAPipe is the client's whole contract in one
// conversation: a real child process, a real pipe, and one call in flight at a
// time. Every reply is matched by its id, so a reply handed back for the wrong
// request fails here rather than being acted on.
func TestPipeClientDrivesAWorkerOverAPipe(t *testing.T) {
	c, err := startFakeWorker(t, modeNormal, handshake())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	written, err := c.Append([]synclog.Entry{{
		Origin: "origin-a", Table: "task", PK: `["t"]`, Op: synclog.OpUpsert,
		SchemaVersion: 3, Body: []byte(`{"title":"mine"}`), At: pinnedAt,
	}})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if len(written) != 1 || written[0].Seq != 1 || written[0].Table != "task" {
		t.Errorf("Append = %+v, want the entry the transport numbered", written)
	}

	pulled, err := c.Pull(map[string]int{"origin-b": 3})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if len(pulled) != 1 || pulled[0].Origin != "origin-b" || pulled[0].Seq != 4 {
		t.Errorf("Pull = %+v, want the other origin's entry", pulled)
	}

	rows, err := c.Head("origin-a")
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if len(rows) != 1 || rows[0].Table != "task" || rows[0].Hash != "abc" {
		t.Errorf("Head = %+v, want the row the log holds", rows)
	}

	stats, err := c.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if want := (synclog.Stats{Entries: 11, Origins: 2, Seq: 9}); stats != want {
		t.Errorf("Stats = %+v, want %+v", stats, want)
	}

	// A last call after the four before it is what proves the ids advanced and
	// the pipe stayed in step, rather than the first reply having been read for
	// every one of them.
	if _, err := c.Stats(); err != nil {
		t.Errorf("the second stats: %v", err)
	}

	// One drive over a worker whose replies are a reply out of step is what
	// makes the id the thing being matched on. Every reply that worker sends is
	// a true reply, so a client that read "a reply arrived" instead of "a reply
	// arrived for me" would answer this call out of the handshake.
	stale, err := startFakeWorker(t, modeStaleID, handshake())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := stale.Stats(); err == nil {
		t.Error("Stats over a worker a reply out of step = nil, want the crossed reply refused")
	}
}

// TestPipeClientRefusesAReplyWithAnotherId pins the one reply the client must
// not accept. A reply naming another request means the stream is out of step,
// and every reply after it would be read against the wrong call, so the client
// ends the worker rather than carry on.
func TestPipeClientRefusesAReplyWithAnotherId(t *testing.T) {
	cases := map[string]string{
		"a reply for a request that was never sent": modeWrongID,
		"a reply for a request already answered":    modeStaleID,
	}
	for name, mode := range cases {
		t.Run(name, func(t *testing.T) {
			c, err := startFakeWorker(t, mode, handshake())
			if err != nil {
				// The handshake itself was the crossed reply, so the client
				// refused to come up at all.
				return
			}
			// Otherwise the handshake was honest and the crossing comes later, so
			// the refusal belongs to the first call after it.
			if _, err := c.Stats(); err == nil {
				t.Errorf("a call over %s = nil, want the crossed reply refused", mode)
			}
		})
	}
}

// TestFakeWorkerDeathSurfacesAsCallError pins that a worker which stops
// answering is a call failure and not a success. A daemon reading a dead pipe
// as an answer would clear its outbox rows against entries the remote never took.
func TestFakeWorkerDeathSurfacesAsCallError(t *testing.T) {
	c, err := startFakeWorker(t, modeDie, handshake())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	_, err = c.Stats()
	if err == nil {
		t.Fatal("Stats over a worker that died = nil, want a call error")
	}
	if !strings.Contains(err.Error(), "read reply") {
		t.Errorf("Stats = %v, want the worker's death surfaced as the call's failure", err)
	}
	// The worker is gone for good: a second call fails without reaching a pipe
	// whose other end no longer exists.
	if _, err := c.Stats(); err == nil {
		t.Error("the second stats over a dead worker = nil, want a failure")
	}
}

// TestFakeWorkerHangHitsTheDeadline pins that a worker which accepts a call and
// never answers costs the deadline and not the daemon's tick. The worker is
// killed rather than left hanging, so what comes next is a fresh worker rather
// than one still wedged.
func TestFakeWorkerHangHitsTheDeadline(t *testing.T) {
	cfg := handshake()
	cfg.Timeout = 300 * time.Millisecond
	c, err := startFakeWorker(t, modeHang, cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	start := time.Now()
	_, err = c.Stats()
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("Stats over a worker that never answers = nil, want the deadline")
	}
	if !strings.Contains(err.Error(), "no reply within") {
		t.Errorf("Stats = %v, want the deadline named", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("Stats took %s, want the %s deadline", elapsed, cfg.Timeout)
	}
	if !waitForExit(t, c, 10*time.Second) {
		t.Error("the worker is still running after the deadline: a wedged worker must be killed")
	}
}

// TestFakeWorkerRefusalSurfacesTheCause pins that a refusal travels with the
// driver's own words. The reason a breaker latches on, and the reason a human
// reads, are both in it, so a paraphrase here would report something that never
// happened.
func TestFakeWorkerRefusalSurfacesTheCause(t *testing.T) {
	_, err := startFakeWorker(t, modeRefuse, handshake())
	if err == nil {
		t.Fatal("Start over a refusing worker = nil, want the refusal")
	}
	if !errors.Is(err, ErrRefused) {
		t.Errorf("Start = %v, want a refusal rather than a failure", err)
	}
	if !strings.Contains(err.Error(), "not a relevo log") {
		t.Errorf("Start = %v, want the refusal's own reason", err)
	}
	if !relevosync.IsPermanentRefusal(err) {
		t.Errorf("Start = %v, want a refusal the breaker can latch on rather than retry", err)
	}
}

// TestRefusalErrorCarriesTheClassSentinel pins what a declined reply becomes:
// the worker's reason under ErrRefused, and the daemon's sentinel for the class
// the worker marked. A refusal with no class stays a refusal alone, which the
// breaker leaves to the ordinary call path rather than latching on.
func TestRefusalErrorCarriesTheClassSentinel(t *testing.T) {
	cases := map[string]struct {
		code      syncworker.RefusalCode
		want      error
		permanent bool
	}{
		"a remote refusal": {
			code: syncworker.CodeRemote, want: relevosync.ErrRemoteRefused, permanent: true,
		},
		"a schema refusal": {
			code: syncworker.CodeSchema, want: relevosync.ErrRemoteSchema, permanent: true,
		},
		"a refusal a later attempt can get past": {
			code: "", want: nil, permanent: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := refusalError(syncworker.Response{OK: false, Error: "the worker refused", Code: tc.code})
			if !errors.Is(err, ErrRefused) {
				t.Fatalf("err = %v, want errors.Is(err, ErrRefused)", err)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want errors.Is(err, %v)", err, tc.want)
			}
			if got := relevosync.IsPermanentRefusal(err); got != tc.permanent {
				t.Errorf("IsPermanentRefusal = %v, want %v", got, tc.permanent)
			}
		})
	}
}

// TestPipeClientCountsOnlyAMissAndNotARefusal pins which faults the breaker is
// meant to see. A worker that answers and declines is healthy, and counting it
// would latch the breaker against a remote that is working perfectly.
func TestPipeClientCountsOnlyAMissAndNotARefusal(t *testing.T) {
	counted := 0
	cfg := handshake()
	cfg.OnMiss = func(error) { counted++ }
	cfg.Timeout = 300 * time.Millisecond

	c, err := startFakeWorker(t, modeHang, cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := c.Stats(); err == nil {
		t.Fatal("Stats over a hanging worker = nil, want the deadline")
	}
	if counted != 1 {
		t.Errorf("a miss was counted %d times, want once", counted)
	}

	if _, err := startFakeWorker(t, modeRefuse, cfg); err == nil {
		t.Fatal("Start over a refusing worker = nil, want the refusal")
	}
	if counted != 1 {
		t.Errorf("a refusal reached the miss counter: %d misses, want the one miss alone", counted)
	}
}

// TestWorkerExitsWhenTheDaemonGoesAway pins the lifecycle the daemon relies on
// when it stops or is re-executed: closing the pipe is all the worker is asked,
// and it is enough. A worker outliving that would keep the replica open against
// the worker that replaces it.
func TestWorkerExitsWhenTheDaemonGoesAway(t *testing.T) {
	c, err := startFakeWorker(t, modeNormal, handshake())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := c.Stats(); err != nil {
		t.Fatalf("Stats: %v", err)
	}

	// Dropping the request end is the whole of a daemon going away: no shutdown
	// asked for, the worker told only that its pipe is over.
	if err := c.requests.Close(); err != nil {
		t.Fatalf("close the pipe to the worker: %v", err)
	}
	if !waitForExit(t, c, 10*time.Second) {
		t.Fatal("the worker is still running after the daemon's pipe closed")
	}
}

// waitForExit reports whether the worker process is gone within the grace.
func waitForExit(t *testing.T, c *Client, grace time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if c.isGone() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
