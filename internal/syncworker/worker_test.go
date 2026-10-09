package syncworker

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBackend is a log with no remote behind it: it records what it was asked
// for and answers with what the test set up. It is what every test in this
// package serves the pipe with, so the wire is exercised without a driver and
// without a network.
//
// The lock is because the worker runs on its own goroutine while the test
// reads what it recorded: a record of the last call is only the last call if
// reading it is ordered against the write that made it.
type fakeBackend struct {
	mu      sync.Mutex
	opened  []Spec
	appends [][]Entry
	pulls   []map[string]int
	heads   []headCall
	closed  int

	written []Entry
	pulled  []Entry
	rows    []HeadRow
	stats   Stats
	fail    error
}

// headCall is one head read as it arrived, so a test can assert the paging
// arguments crossed the pipe rather than being re-derived here.
type headCall struct {
	origin string
	after  string
	limit  int
}

func (f *fakeBackend) Open(s Spec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = append(f.opened, s)
	return f.fail
}

func (f *fakeBackend) Append(entries []Entry) ([]Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.appends = append(f.appends, entries)
	if f.fail != nil {
		return nil, f.fail
	}
	return f.written, nil
}

func (f *fakeBackend) Pull(marks map[string]int) ([]Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulls = append(f.pulls, marks)
	if f.fail != nil {
		return nil, f.fail
	}
	return f.pulled, nil
}

func (f *fakeBackend) Head(origin, after string, limit int) ([]HeadRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heads = append(f.heads, headCall{origin: origin, after: after, limit: limit})
	if f.fail != nil {
		return nil, f.fail
	}
	return f.rows, nil
}

func (f *fakeBackend) Stats() (Stats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return Stats{}, f.fail
	}
	return f.stats, nil
}

func (f *fakeBackend) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
	return nil
}

// specs, counts, lastHead and lastPull read the record under the lock, so a
// test asserts what the backend was asked for rather than what it saw race
// past.
func (f *fakeBackend) specs() []Spec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Spec(nil), f.opened...)
}

func (f *fakeBackend) counts() (opens, appends, pulls, heads int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.opened), len(f.appends), len(f.pulls), len(f.heads)
}

func (f *fakeBackend) lastHead() headCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.heads[len(f.heads)-1]
}

func (f *fakeBackend) lastPull() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pulls[len(f.pulls)-1]
}

func (f *fakeBackend) closes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// upsertEntry is a well-formed entry of the origin under test, which is what a
// test that is not about the entry's shape needs every entry to be.
func upsertEntry(origin, table, pk string, body string) Entry {
	return Entry{
		Origin:        origin,
		Tbl:           table,
		PK:            pk,
		Op:            "upsert",
		SchemaVersion: 3,
		Body:          json.RawMessage(body),
		At:            time.Date(2026, 10, 8, 9, 30, 0, 0, time.UTC),
	}
}

// helloRequest is the handshake every conversation but the refusal tests opens
// with, so a test states the verb it is about and not the handshake before it.
func helloRequest(id string) Request {
	return Request{
		ID:      id,
		Verb:    VerbHello,
		Version: ProtocolVersion,
		Origin:  "origin-a",
		URL:     "libsql://remote.example",
		Token:   "the-token",
	}
}

// session is one worker serving a backend over a pipe, with the ends a daemon
// would hold. A test asks on it as many times as its conversation needs, because
// the handshake belongs to the worker and not to any one request: a second call
// on a fresh worker would be answered by one that was never told what it is
// working for.
type session struct {
	t    *testing.T
	p    *pipe
	done chan error

	once sync.Once
	err  error
}

// startWorker starts a worker serving b and returns the session that drives it.
// Closing the session closes the request pipe, which is what a daemon does when
// it has nothing more to ask, and waits for the worker to let go of its backend.
func startWorker(t *testing.T, b Backend) *session {
	t.Helper()
	p := newPipe()
	s := &session{t: t, p: p, done: make(chan error, 1)}
	go func() { s.done <- Serve(p.workerIn, p.workerOut, b) }()
	t.Cleanup(func() {
		_ = p.requests.Close()
		_ = s.wait()
		_ = p.Close()
	})
	return s
}

// wait is how the worker ended, and it may be asked twice: a test that closes
// the pipe itself wants the answer there, and the cleanup wants it too.
func (s *session) wait() error {
	s.once.Do(func() { s.err = <-s.done })
	return s.err
}

// ask sends one request and returns the reply, which the worker matches back to
// the id this sent.
func (s *session) ask(req Request) Response {
	s.t.Helper()
	line, err := json.Marshal(req)
	if err != nil {
		s.t.Fatalf("marshal request: %v", err)
	}
	if _, err := s.p.requests.Write(append(line, '\n')); err != nil {
		s.t.Fatalf("write %s request: %v", req.Verb, err)
	}
	reply, err := readLine(s.p.replies)
	if err != nil {
		s.t.Fatalf("read reply for %s: %v", req.Verb, err)
	}
	var resp Response
	if err := json.Unmarshal(reply, &resp); err != nil {
		s.t.Fatalf("decode reply for %s: %v", req.Verb, err)
	}
	if resp.ID != req.ID {
		s.t.Fatalf("reply id = %q, want %q: a reply is only the answer to its own request", resp.ID, req.ID)
	}
	return resp
}

// askHello opens a conversation and fails the test if the handshake is refused.
func (s *session) askHello() {
	s.t.Helper()
	if resp := s.ask(helloRequest("h")); !resp.OK {
		s.t.Fatalf("hello = %+v, want ok", resp)
	}
}

// pipe is one worker's stdio as two pipes, with the ends named for who holds
// them. A worker reads its own end of the requests and writes its own end of
// the replies, which is what a child process's stdin and stdout are: two
// unidirectional pipes rather than one both sides write to.
type pipe struct {
	requests  *os.File
	replies   *os.File
	workerIn  *os.File
	workerOut *os.File
}

func newPipe() *pipe {
	workerIn, daemonWrites, err := os.Pipe()
	if err != nil {
		panic(err)
	}
	daemonReads, workerOut, err := os.Pipe()
	if err != nil {
		panic(err)
	}
	return &pipe{
		requests:  daemonWrites,
		replies:   daemonReads,
		workerIn:  workerIn,
		workerOut: workerOut,
	}
}

func (p *pipe) Close() error {
	for _, f := range []*os.File{p.requests, p.replies, p.workerIn, p.workerOut} {
		_ = f.Close()
	}
	return nil
}

// readLine reads one newline-terminated line a byte at a time, so a reply is
// exactly the line and not whatever the pipe had buffered by the time it was
// looked at.
func readLine(r io.Reader) ([]byte, error) {
	var out []byte
	one := make([]byte, 1)
	for {
		n, err := r.Read(one)
		if n == 1 {
			if one[0] == '\n' {
				return bytes.TrimRight(out, "\r"), nil
			}
			out = append(out, one[0])
			continue
		}
		if err != nil {
			return nil, err
		}
	}
}

// TestWorkerServesExportPullHeadStatsShutdown drives every verb the pipe
// speaks and pins what each one carries across it, so a verb that stopped
// reaching its backend, or reached it with the wrong arguments, fails here
// rather than somewhere a reader has to interpret.
func TestWorkerServesExportPullHeadStatsShutdown(t *testing.T) {
	const origin = "origin-a"
	b := &fakeBackend{
		written: []Entry{{Origin: origin, Seq: 1, Batch: 1, Tbl: "board", PK: `["b"]`}},
		pulled:  []Entry{{Origin: "origin-b", Seq: 4, Tbl: "task", PK: `["t"]`}},
		rows:    []HeadRow{{Tbl: "board", PK: `["b"]`, Seq: 7, Hash: "abc"}},
		stats:   Stats{Entries: 11, Origins: 2, Seq: 9},
	}
	w := startWorker(t, b)
	w.askHello()

	resp := w.ask(Request{ID: "1", Verb: VerbExport, Entries: []Entry{
		upsertEntry(origin, "board", `["b"]`, `{"title":"one"}`),
	}})
	if !resp.OK || len(resp.Entries) != 1 || resp.Entries[0].Seq != 1 {
		t.Errorf("export = %+v, want the numbered batch back", resp)
	}

	resp = w.ask(Request{ID: "2", Verb: VerbPull, Marks: map[string]int{"origin-b": 3}})
	if !resp.OK || len(resp.Entries) != 1 || resp.Entries[0].Origin != "origin-b" {
		t.Errorf("pull = %+v, want the entries past the mark", resp)
	}

	resp = w.ask(Request{ID: "3", Verb: VerbHead, Origin: origin, After: `["b"]`, Limit: 5})
	if !resp.OK || len(resp.Head) != 1 || resp.Head[0].Seq != 7 {
		t.Errorf("head = %+v, want the page back", resp)
	}

	resp = w.ask(Request{ID: "4", Verb: VerbStats})
	if !resp.OK || resp.Stats == nil || resp.Stats.Entries != 11 || resp.Stats.Seq != 9 {
		t.Errorf("stats = %+v, want what the log holds", resp)
	}

	resp = w.ask(Request{ID: "5", Verb: VerbShutdown})
	if !resp.OK {
		t.Errorf("shutdown = %+v, want ok", resp)
	}

	assertOpenedOnce(t, b, origin)
	_, appends, pulls, heads := b.counts()
	if appends != 1 || pulls != 1 || heads != 1 {
		t.Errorf("backend saw appends=%d pulls=%d heads=%d, want one of each", appends, pulls, heads)
	}
	if got := b.lastHead(); got.origin != origin || got.after != `["b"]` || got.limit != 5 {
		t.Errorf("Head read as %+v, want the paging the request carried", got)
	}
	if got := b.lastPull(); got["origin-b"] != 3 {
		t.Errorf("Pull read as %v, want the marks the request carried", got)
	}
}

// assertOpenedOnce pins that hello is what opens the remote, and that what it
// carried reached the backend whole: the origin every later request is checked
// against, and the token the worker owns for the rest of its life.
func assertOpenedOnce(t *testing.T, b *fakeBackend, origin string) {
	t.Helper()
	specs := b.specs()
	if len(specs) != 1 {
		t.Fatalf("backend opened %d times, want once", len(specs))
	}
	if got := specs[0]; got.Origin != origin || got.Token != "the-token" || got.URL == "" {
		t.Errorf("Open = %+v, want hello's spec", got)
	}
}

// TestWorkerRefusesAnEntryThatIsNotThisOrigins is the worker's own rule and not
// the backend's: an export naming another installation's row is refused before
// anything is written, so the log is never handed a batch it would have to
// unpick.
func TestWorkerRefusesAnEntryThatIsNotThisOrigins(t *testing.T) {
	b := &fakeBackend{}
	w := startWorker(t, b)
	w.askHello()

	resp := w.ask(Request{ID: "1", Verb: VerbExport, Entries: []Entry{
		upsertEntry("origin-b", "board", `["b"]`, `{"title":"theirs"}`),
	}})
	if resp.OK || !strings.Contains(resp.Error, ErrForeignOrigin.Error()) {
		t.Errorf("export of another origin's entry = %+v, want a refusal naming the rule", resp)
	}
	if _, appends, _, _ := b.counts(); appends != 0 {
		t.Errorf("backend appended %d batches, want none: a refused batch is never written", appends)
	}
}

// TestWorkerRefusesAnEntryOfTheWrongShape pins both directions of the shape
// rule: a delete that carries a body and an upsert that carries none each
// arrive at the same reader with two answers to one question, so neither is
// written.
func TestWorkerRefusesAnEntryOfTheWrongShape(t *testing.T) {
	const origin = "origin-a"
	b := &fakeBackend{}
	w := startWorker(t, b)
	w.askHello()

	cases := map[string]Entry{
		"delete with a body": {
			Origin: origin, Tbl: "board", PK: `["b"]`, Op: "delete",
			Body: json.RawMessage(`{"title":"one"}`),
		},
		"upsert with no body": {Origin: origin, Tbl: "board", PK: `["b"]`, Op: "upsert"},
		"an op the log has no room for": {
			Origin: origin, Tbl: "board", PK: `["b"]`, Op: "replace",
			Body: json.RawMessage(`{}`),
		},
		"an entry naming no row": {Origin: origin, Op: "delete"},
	}
	for name, entry := range cases {
		t.Run(name, func(t *testing.T) {
			resp := w.ask(Request{ID: "1", Verb: VerbExport, Entries: []Entry{entry}})
			if resp.OK || !strings.Contains(resp.Error, ErrProtocol.Error()) {
				t.Errorf("export = %+v, want a refusal naming the shape rule", resp)
			}
		})
	}
	if _, appends, _, _ := b.counts(); appends != 0 {
		t.Errorf("backend appended %d batches, want none", appends)
	}
}

// TestWorkerRefusesAVerbBeforeHello pins that the loop answers rather than
// guesses: without a handshake the worker holds no origin to append as and no
// remote to answer from, so the only true reply is the one saying so.
func TestWorkerRefusesAVerbBeforeHello(t *testing.T) {
	b := &fakeBackend{}
	w := startWorker(t, b)
	for _, verb := range []Verb{VerbExport, VerbPull, VerbHead, VerbStats} {
		resp := w.ask(Request{ID: "1", Verb: verb, Origin: "origin-a"})
		if resp.OK || !strings.Contains(resp.Error, ErrNoHello.Error()) {
			t.Errorf("%s before hello = %+v, want a refusal", verb, resp)
		}
	}
	if opens, _, _, _ := b.counts(); opens != 0 {
		t.Errorf("backend opened %d times, want none", opens)
	}
}

// TestWorkerRefusesAHelloItCannotSpeak covers the handshakes a worker has no
// way to serve: a pipe version it does not speak, and one missing what it would
// work for. All are refused before Open, so no remote is opened for a
// conversation that was never going to happen.
func TestWorkerRefusesAHelloItCannotSpeak(t *testing.T) {
	cases := map[string]func(Request) Request{
		"a version it does not speak": func(r Request) Request {
			r.Version = ProtocolVersion + 1
			return r
		},
		"no origin": func(r Request) Request { r.Origin = ""; return r },
		"no remote": func(r Request) Request { r.URL = ""; return r },
		"no token":  func(r Request) Request { r.Token = ""; return r },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			b := &fakeBackend{}
			resp := startWorker(t, b).ask(breakIt(helloRequest("h")))
			if resp.OK || !strings.Contains(resp.Error, ErrProtocol.Error()) {
				t.Errorf("hello = %+v, want a refusal", resp)
			}
			if opens, _, _, _ := b.counts(); opens != 0 {
				t.Errorf("backend opened %d times, want none", opens)
			}
		})
	}
}

// TestWorkerAnswersAnUnknownVerbRatherThanDying pins that a verb it does not
// know is a question with an answer, not a broken pipe: the daemon gets to read
// the reason and the worker stays in step for whatever it asks next.
func TestWorkerAnswersAnUnknownVerbRatherThanDying(t *testing.T) {
	w := startWorker(t, &fakeBackend{})
	w.askHello()
	if resp := w.ask(Request{ID: "1", Verb: "compact"}); resp.OK ||
		!strings.Contains(resp.Error, ErrProtocol.Error()) {
		t.Errorf("compact = %+v, want a refusal naming the verb", resp)
	}
	if resp := w.ask(Request{ID: "2", Verb: VerbStats}); resp.ID != "2" {
		t.Errorf("reply id = %q, want the next request's: the pipe stayed in step", resp.ID)
	}
}

// TestWorkerCarriesABackendRefusalVerbatim pins that the backend's own words
// survive the pipe. The refusal a caller acts on is the driver's reason for it,
// and a paraphrase here would leave a latch reporting something that never
// happened.
func TestWorkerCarriesABackendRefusalVerbatim(t *testing.T) {
	b := &fakeBackend{fail: errors.New("remote refused: not a relevo log")}
	resp := startWorker(t, b).ask(helloRequest("h"))
	if resp.OK || !strings.Contains(resp.Error, "not a relevo log") {
		t.Errorf("hello = %+v, want the backend's reason", resp)
	}
}

// TestWorkerMarksOnlyARefusalThatRepeats pins the class the worker puts on a
// refusal: a backend that marks one as repeating travels with its class, and a
// refusal with no class stays unmarked, which is what leaves it to the ordinary
// call path rather than a latch.
func TestWorkerMarksOnlyARefusalThatRepeats(t *testing.T) {
	cases := map[string]struct {
		fail error
		want RefusalCode
	}{
		"a refusal that repeats": {
			fail: MarkRefusal(CodeRemote, errors.New("remote refused: not a relevo log")),
			want: CodeRemote,
		},
		"a schema refusal that repeats": {
			fail: MarkRefusal(CodeSchema, errors.New("the remote has no table")),
			want: CodeSchema,
		},
		"a refusal a later attempt can get past": {
			fail: errors.New("another origin's entry"),
			want: "",
		},
		"a refusal marked with no class": {
			fail: MarkRefusal("", errors.New("another origin's entry")),
			want: "",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w := startWorker(t, refusingBackend{fail: tc.fail})
			w.askHello()
			resp := w.ask(Request{ID: "1", Verb: VerbStats})
			if resp.OK {
				t.Fatalf("stats = %+v, want a refusal", resp)
			}
			if resp.Code != tc.want {
				t.Errorf("code = %q, want %q", resp.Code, tc.want)
			}
		})
	}
}

// TestWorkerMarksADriverRefusalItCannotGetPast pins the class the worker puts
// on the engine's own refusal: a statement the remote will not execute repeats
// until the change set changes, and a missing table keeps its own class because
// its fix is the schema. A remote merely unreachable stays unmarked, so it is
// left to the ordinary call path.
func TestWorkerMarksADriverRefusalItCannotGetPast(t *testing.T) {
	cases := map[string]struct {
		push error
		want RefusalCode
	}{
		"a missing table": {
			push: errors.New("sync engine: failed to execute sql: no such table: log"),
			want: CodeSchema,
		},
		"another refusal of the statement": {
			push: errors.New("sync engine: failed to execute sql: FOREIGN KEY constraint failed"),
			want: CodeRemote,
		},
		"a remote that cannot be reached": {
			push: errors.New("no remote"),
			want: "",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			driver := &localDriver{path: tempReplica(t)}
			b := backendFor(t, driver, "origin-a")
			driver.pushErr = tc.push
			w := startWorker(t, b)
			w.askHello()
			resp := w.ask(Request{ID: "1", Verb: VerbExport,
				Entries: []Entry{upsertEntry("origin-a", "board", `["b"]`, `{"title":"one"}`)}})
			if resp.OK {
				t.Fatalf("export = %+v, want a refusal", resp)
			}
			if resp.Code != tc.want {
				t.Errorf("code = %q, want %q", resp.Code, tc.want)
			}
		})
	}
}

// TestWorkerClosesTheBackendWhenThePipeEnds pins the lifecycle the daemon
// depends on: the pipe ending is the daemon going away, and a replica still
// open is the next worker locked out of it.
func TestWorkerClosesTheBackendWhenThePipeEnds(t *testing.T) {
	b := &fakeBackend{}
	w := startWorker(t, b)
	w.askHello()

	_ = w.p.requests.Close()
	if err := w.wait(); err != nil {
		t.Fatalf("Serve = %v, want a clean end of pipe", err)
	}
	if got := b.closes(); got != 1 {
		t.Errorf("backend closed %d times, want once", got)
	}
}

// TestWorkerStopsOnALineItCannotRead pins the one failure that ends the loop
// rather than becoming a reply: a line with no id has nothing to answer, so the
// worker dies and the caller learns about it by the death.
func TestWorkerStopsOnALineItCannotRead(t *testing.T) {
	cases := map[string]string{
		"a line that is not json":  "{not json",
		"a request with no id":     `{"verb":"stats"}`,
		"a request with no verb":   `{"id":"1"}`,
		"an empty line":            ``,
		"a second value on a line": `{"id":"1","verb":"stats"}{"id":"2","verb":"stats"}`,
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			b := &fakeBackend{}
			w := startWorker(t, b)
			if _, err := w.p.requests.Write([]byte(line + "\n")); err != nil {
				t.Fatalf("write %q: %v", line, err)
			}
			if err := w.wait(); err == nil {
				t.Errorf("Serve(%q) = nil, want the loop to stop on a line it cannot read", line)
			}
			if got := b.closes(); got != 1 {
				t.Errorf("backend closed %d times, want once", got)
			}
		})
	}
}

// TestWorkerPackageDoesNotImportInternalDB pins the boundary that makes a
// driver abort survivable: the worker reaches the replica through its backend
// and nothing else, so a crash on the far side of this pipe cannot take the
// record with it. The check is go list rather than a source read, because an
// import that arrives through another package is exactly the one a source read
// misses.
func TestWorkerPackageDoesNotImportInternalDB(t *testing.T) {
	const dbPath = "github.com/fuad-daoud/relevo/internal/db"

	out, err := exec.Command("go", "list", "-deps", "github.com/fuad-daoud/relevo/internal/syncworker").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	for _, line := range strings.Fields(string(out)) {
		if line == dbPath || strings.HasPrefix(line, dbPath+"/") {
			t.Errorf("internal/syncworker depends on %s; the worker may only reach the log through its Backend", line)
		}
	}
}

// TestVerbsNamesEveryDispatchedVerb pins the exported verb set to what the
// worker serves: it is derived from the dispatch table, so a caller that has to
// classify every verb walks the pipe's own set rather than a list that can
// drift from it.
func TestVerbsNamesEveryDispatchedVerb(t *testing.T) {
	names := make([]string, 0, len(Verbs()))
	for _, verb := range Verbs() {
		names = append(names, string(verb))
	}
	if got := strings.Join(names, ","); got != "export,head,hello,pull,shutdown,stats" {
		t.Errorf("Verbs() = %q, want the pipe's own verb set", got)
	}
}
