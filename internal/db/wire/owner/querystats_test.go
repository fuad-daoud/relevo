//go:build unix

package owner

import (
	"bytes"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/fuad-daoud/relevo/internal/db/wire"
)

// syncLog is a slog writer a test can read while the reporter goroutine is
// still writing it.
type syncLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *syncLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *syncLog) snapshot() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// waitForLog polls the buffer until want appears in it or the deadline passes.
func waitForLog(t *testing.T, l *syncLog, want string, d time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		out := l.snapshot()
		if strings.Contains(out, want) {
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("log never contained %q, got:\n%s", want, out)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// captureStatsLog swaps the default logger into a buffer for the test.
func captureStatsLog(t *testing.T) *syncLog {
	t.Helper()
	l := &syncLog{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(l, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return l
}

// sendAdHocHello writes a hello marking this connection ad-hoc, the handshake
// `relevo db query` sends.
func sendAdHocHello(t *testing.T, w *wire.Conn) {
	t.Helper()
	payload, err := wire.Encode(wire.KindHello, &wire.Hello{
		Header:  wire.Header{Type: wire.TypeHello},
		Proto:   wire.Proto,
		Version: wire.Version,
		AdHoc:   true,
	}, nil)
	if err != nil {
		t.Fatalf("Encode hello: %v", err)
	}
	if err := w.Write(payload); err != nil {
		t.Fatalf("Write hello: %v", err)
	}
}

// readRowsThenDone consumes a query's rows batches until its done frame.
func readRowsThenDone(t *testing.T, w *wire.Conn, nc net.Conn) {
	t.Helper()
	if err := nc.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	for {
		frame, err := w.Read()
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		kind, err := wire.Kind(frame)
		if err != nil {
			t.Fatalf("Kind: %v", err)
		}
		if kind == wire.KindDone {
			return
		}
		if kind != wire.KindRows {
			t.Fatalf("frame kind = %d, want rows then done", kind)
		}
	}
}

func TestNormalizeShapeFoldsStatementsToTheirShape(t *testing.T) {
	cases := []struct{ name, query, want string }{
		{"whitespace collapses", "SELECT  a\n\tFROM   t", "SELECT a FROM t"},
		{"leading and trailing trim", "  SELECT 1  ", "SELECT ?"},
		{"string literal folds", "WHERE name = 'bob'", "WHERE name = ?"},
		{"doubled quote is content", "WHERE v = 'it''s'", "WHERE v = ?"},
		{"unterminated literal ends the statement", "SELECT 'abc", "SELECT ?"},
		{"numbers fold", "IN (1, 22, 333)", "IN (?, ?, ?)"},
		{"fraction folds whole", "n > 1.5", "n > ?"},
		{"identifier digits stay", "SELECT t1.a FROM x", "SELECT t1.a FROM x"},
		{"numbered parameter keeps its slot", "WHERE a = ?1", "WHERE a = ?1"},
		{"same shape from different literals",
			"SELECT x FROM t WHERE n = 2", "SELECT x FROM t WHERE n = ?"},
		{"placeholders survive", "SELECT a FROM t WHERE b = ? AND c = ?",
			"SELECT a FROM t WHERE b = ? AND c = ?"},
		{"long shapes are capped", "SELECT " + strings.Repeat("x", 300),
			"SELECT " + strings.Repeat("x", 193) + "..."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeShape(tc.query); got != tc.want {
				t.Errorf("normalizeShape(%q) = %q, want %q", tc.query, got, tc.want)
			}
		})
	}

	// The cap may not cut a multi-byte rune in half: a shape of ASCII and a
	// three-byte arrow has its last whole rune end below the cap, and the cut
	// must land on a rune start.
	long := strings.Repeat("a", 199) + "→tail"
	got := normalizeShape(long)
	if !utf8.ValidString(got) {
		t.Errorf("capped shape %q is not valid UTF-8", got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("capped shape %q does not end with the cap mark", got)
	}
}

// TestServedStatementsCountPerShapeAndFlags pins the seam: every statement
// the owner runs is counted once, under its folded shape, its frame kind, and
// the flags its connection was handshaken with; two statements that differ
// only by a pasted literal share one counter; a refused statement is counted
// nowhere.
func TestServedStatementsCountPerShapeAndFlags(t *testing.T) {
	s := startSplitServer(t, true)

	a, na := dialRaw(t, s.sock)
	sendHello(t, a, wire.Version)
	welcome(t, a)
	execRaw(t, a, 1, "CREATE TABLE t (n INTEGER)")
	readDone(t, a, na)
	execRaw(t, a, 2, "INSERT INTO t VALUES (1)")
	readDone(t, a, na)
	execRaw(t, a, 3, "INSERT INTO t VALUES (2)")
	readDone(t, a, na)
	if err := a.Write(queryFrame("SELECT n FROM t WHERE n = 1")); err != nil {
		t.Fatalf("Write query: %v", err)
	}
	readRowsThenDone(t, a, na)

	b, nb := dialRaw(t, s.sock)
	sendAdHocHello(t, b)
	welcome(t, b)
	execRaw(t, b, 1, "INSERT INTO t VALUES (3)")
	readDone(t, b, nb)

	c, nc := dialRaw(t, s.sock)
	if err := c.Write(handshakeFrame(wire.ScopeLocal)); err != nil {
		t.Fatalf("Write hello: %v", err)
	}
	welcome(t, c)
	execRaw(t, c, 1, "INSERT INTO kv (k, v) VALUES ('z', 'z')")
	readDone(t, c, nc)

	// A drain refuses this request before its pin, so the statement never
	// reaches the engine and never enters a counter.
	s.srv.beginDrain()
	execRaw(t, a, 4, "INSERT INTO t VALUES (9)")
	got := refusal(t, a)
	if got.Code != wire.RefuseRestarting {
		t.Fatalf("refusal code = %q, want %q", got.Code, wire.RefuseRestarting)
	}

	stats := s.srv.drainQueryStats()
	want := map[shapeKey]int64{
		{kind: wire.KindExec, shape: "CREATE TABLE t (n INTEGER)"}:                       1,
		{kind: wire.KindExec, shape: "INSERT INTO t VALUES (?)"}:                         2,
		{kind: wire.KindQuery, shape: "SELECT n FROM t WHERE n = ?"}:                     1,
		{kind: wire.KindExec, adHoc: true, shape: "INSERT INTO t VALUES (?)"}:            1,
		{kind: wire.KindExec, local: true, shape: "INSERT INTO kv (k, v) VALUES (?, ?)"}: 1,
	}
	if !maps.Equal(stats, want) {
		t.Errorf("window counters = %v, want %v", stats, want)
	}
	if again := s.srv.drainQueryStats(); len(again) != 0 {
		t.Errorf("the window after the drain = %v, want empty", again)
	}
}

// TestQueryStatsWindowReportedPerShape pins the window report on a server no
// client touches any more: the summary counts every shape and resets, the
// shape lines arrive largest first so the bulk callers read before the noise,
// and a quiet window still answers, because a report that goes silent cannot
// be told apart from a reporter that died.
func TestQueryStatsWindowReportedPerShape(t *testing.T) {
	srv := New(nil, 0, 0, "01ORIGIN", nil)
	srv.noteQuery(wire.KindExec, false, false, "INSERT INTO t VALUES (1)")
	srv.noteQuery(wire.KindExec, false, false, "INSERT INTO t VALUES (2)")
	srv.noteQuery(wire.KindExec, false, false, "INSERT INTO t VALUES (3)")
	srv.noteQuery(wire.KindExec, true, false, "INSERT INTO t VALUES (4)")
	srv.noteQuery(wire.KindQuery, false, false, "SELECT n FROM t WHERE n = 1")

	logged := captureStatsLog(t)
	srv.reportQueryStats(time.Minute)

	out := logged.snapshot()
	for _, want := range []string{
		`msg="owner: served statements window"`,
		"window=1m0s",
		"total=5",
		"shapes=3",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary line lacks %q in:\n%s", want, out)
		}
	}

	shapeLines := []string{}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "owner: served statement shape") {
			shapeLines = append(shapeLines, line)
		}
	}
	if len(shapeLines) != 3 {
		t.Fatalf("shape lines = %d, want 3:\n%s", len(shapeLines), strings.Join(shapeLines, "\n"))
	}
	// Count-descending, then shape: the three plain inserts first, then the
	// ties -- INSERT(ad-hoc) before SELECT by shape.
	for i, want := range []string{"count=3", "count=1", "count=1"} {
		if !strings.Contains(shapeLines[i], want) {
			t.Errorf("shape line %d = %s, want it to carry %s", i, shapeLines[i], want)
		}
	}
	if !strings.Contains(shapeLines[0], "INSERT INTO t VALUES (?)") ||
		strings.Contains(shapeLines[0], "ad_hoc=true") {
		t.Errorf("largest line = %s, want the non-ad-hoc INSERT", shapeLines[0])
	}
	if !strings.Contains(shapeLines[1], "ad_hoc=true") ||
		!strings.Contains(shapeLines[1], "INSERT INTO t VALUES (?)") {
		t.Errorf("second line = %s, want the ad-hoc INSERT", shapeLines[1])
	}
	if !strings.Contains(shapeLines[2], "kind=query") ||
		!strings.Contains(shapeLines[2], "SELECT n FROM t WHERE n = ?") {
		t.Errorf("third line = %s, want the SELECT", shapeLines[2])
	}

	// The window drained: the next report sees the fresh counters only.
	logged.mu.Lock()
	logged.buf.Reset()
	logged.mu.Unlock()
	srv.reportQueryStats(time.Minute)
	quiet := logged.snapshot()
	if !strings.Contains(quiet, "total=0") || !strings.Contains(quiet, "shapes=0") {
		t.Errorf("the window after the drain = %s, want a zero summary", quiet)
	}
	if strings.Contains(quiet, "owner: served statement shape") {
		t.Errorf("a quiet window emitted shape lines:\n%s", quiet)
	}
}

// TestQueryStatsWindowCapsTheShapeLines pins the journal's cost: a window with
// more shapes than the cap writes only the largest, and its summary still
// counts them all.
func TestQueryStatsWindowCapsTheShapeLines(t *testing.T) {
	srv := New(nil, 0, 0, "01ORIGIN", nil)
	extra := 5
	for i := range maxShapesLogged + extra {
		srv.noteQuery(wire.KindQuery, false, false, "SELECT x FROM t_"+strings.Repeat("a", i+1))
	}
	bulk := "SELECT y FROM bulk"
	srv.noteQuery(wire.KindQuery, false, false, bulk)
	srv.noteQuery(wire.KindQuery, false, false, bulk)

	logged := captureStatsLog(t)
	srv.reportQueryStats(time.Minute)

	out := logged.snapshot()
	if n := strings.Count(out, "owner: served statement shape"); n != maxShapesLogged {
		t.Errorf("shape lines = %d, want the cap %d:\n%s", n, maxShapesLogged, out)
	}
	if !strings.Contains(out, bulk) {
		t.Errorf("the largest shape %q was cut:\n%s", bulk, out)
	}
	if want := fmt.Sprintf("shapes=%d", maxShapesLogged+extra+1); !strings.Contains(out, want) {
		t.Errorf("summary lacks %q:\n%s", want, out)
	}
}

// TestQueryStatsReporterRunsWhileServing pins that Serve starts the window
// reporter and that a window's lines reach the log without a test calling the
// report by hand.
func TestQueryStatsReporterRunsWhileServing(t *testing.T) {
	old := statsWindow
	statsWindow = 20 * time.Millisecond
	t.Cleanup(func() { statsWindow = old })

	_, sock := startServer(t)
	logged := captureStatsLog(t)

	w, nc := dialRaw(t, sock)
	sendHello(t, w, wire.Version)
	welcome(t, w)
	execRaw(t, w, 1, "CREATE TABLE t (n INTEGER)")
	readDone(t, w, nc)

	out := waitForLog(t, logged, "owner: served statement shape", 5*time.Second)
	if !strings.Contains(out, "CREATE TABLE t (n INTEGER)") {
		t.Errorf("shape line does not name the statement:\n%s", out)
	}
	// A later window carries the summary alone: the statements were drained
	// with the first shape lines, so the last shape line is followed by a
	// zero total and nothing else of its own.
	deadline := time.Now().Add(5 * time.Second)
	for {
		out = logged.snapshot()
		last := strings.LastIndex(out, "owner: served statement shape")
		if last >= 0 && strings.Contains(out[last:], "total=0") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the reporter never closed a drained window with a zero summary:\n%s", out)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
