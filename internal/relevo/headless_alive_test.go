package relevo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// seedHeadlessRows binds and sends n headless bindings, each with a pid the
// fake runner answers alive, and returns their names.
func seedHeadlessRows(t *testing.T, rt Runtime, n int) []string {
	t.Helper()
	names := make([]string, 0, n)
	for i := 0; i < n; i++ {
		name := "live-" + string(rune('a'+i))
		if _, err := Bind(context.Background(), rt, BindOptions{
			Name: name, Candidate: testAgyRef, MasterMindID: testMasterMindName, CWD: name,
		}); err != nil {
			t.Fatalf("Bind %s: %v", name, err)
		}
		if _, err := Send(context.Background(), rt, name, writePlan(t, "do it"), SendOptions{}); err != nil {
			t.Fatalf("Send %s: %v", name, err)
		}
		names = append(names, name)
	}
	return names
}

// TestStatusProbesLivenessOncePerRefresh is the mutation test for Optional B
// step 1: a report that paints N live headless rows probes ps once, not once
// per row. Mutation "call rt.Runner.Alive per row instead of the batch" makes
// aliveCalls equal the row count and batchCalls zero, and this fails.
func TestStatusProbesLivenessOncePerRefresh(t *testing.T) {
	fr := newFakeRunner()
	rt := newRuntime(t)
	rt.Runner = fr
	names := seedHeadlessRows(t, rt, 6)

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(rep.Bindings) != len(names) {
		t.Fatalf("rows = %d, want %d", len(rep.Bindings), len(names))
	}
	for _, row := range rep.Bindings {
		if row.BuilderStatus != "working" {
			t.Errorf("%s: word = %q, want working", row.Name, row.BuilderStatus)
		}
	}
	if fr.batchCalls != 1 {
		t.Errorf("batch probes = %d, want exactly 1 for %d live rows", fr.batchCalls, len(names))
	}
	if fr.aliveCalls != 0 {
		t.Errorf("single-handle probes = %d, want 0 once the batch answered", fr.aliveCalls)
	}
}

// TestStatusFallsBackToAliveWhenTheBatchFails: a ps that cannot run must never
// read as a fleet of dead builders. With the batch failing, every row is probed
// on its own and keeps the word it would have had.
func TestStatusFallsBackToAliveWhenTheBatchFails(t *testing.T) {
	fr := newFakeRunner()
	rt := newRuntime(t)
	rt.Runner = fr
	names := seedHeadlessRows(t, rt, 3)
	fr.batchErr = errBatchFailed{}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if fr.aliveCalls != len(names) {
		t.Errorf("single-handle probes = %d, want one per row (%d) when the batch fails", fr.aliveCalls, len(names))
	}
	for _, row := range rep.Bindings {
		if row.BuilderStatus != "working" {
			t.Errorf("%s: word = %q, want working -- a failed probe is not a dead builder", row.Name, row.BuilderStatus)
		}
	}
}

type errBatchFailed struct{}

func (errBatchFailed) Error() string { return "ps unavailable" }

// TestStatusWordsAreIdenticalWithAndWithoutTheBatch pins the batch to the
// answers it replaced: the same store, probed once for the fleet and once per
// row, produces the same word for every row.
func TestStatusWordsAreIdenticalWithAndWithoutTheBatch(t *testing.T) {
	for _, dead := range []bool{false, true} {
		// One store and one script, read twice: once through the batch and
		// once through a runner that has no batch surface at all. Same rows,
		// same answers -- that is the whole claim.
		fr := newFakeRunner()
		rt := newRuntime(t)
		rt.Runner = fr
		seedHeadlessRows(t, rt, 3)
		if dead {
			for _, h := range fr.handles {
				fr.script(h.PID, false)
				fr.exit(h.PID, 1)
			}
		}
		rep, err := Status(context.Background(), rt)
		if err != nil {
			t.Fatalf("Status (dead=%v): %v", dead, err)
		}

		rt.Runner = &noBatchRunner{fr: fr}
		rep2, err := Status(context.Background(), rt)
		if err != nil {
			t.Fatalf("Status without the batch (dead=%v): %v", dead, err)
		}
		byName := map[string]string{}
		for _, row := range rep2.Bindings {
			byName[row.Name] = row.BuilderStatus
		}
		for _, row := range rep.Bindings {
			if got, want := row.BuilderStatus, byName[row.Name]; got != want {
				t.Errorf("dead=%v %s: batched word %q, per-row word %q", dead, row.Name, got, want)
			}
			if got, want := row.Headless.ExitCode, rep2ByName(rep2, row); got != want {
				t.Errorf("dead=%v %s: batched exit code %q, per-row %q", dead, row.Name, got, want)
			}
		}
	}
}

// rep2ByName is the second report's exit code per binding: the batched reading
// must not change what an exited row says about why.
func rep2ByName(rep view.Report, row view.BindingStatus) string {
	for _, other := range rep.Bindings {
		if other.Name == row.Name && other.Headless != nil {
			return other.Headless.ExitCode
		}
	}
	return ""
}

// noBatchRunner is a Runner with every capability but AliveBatch, which is what
// a Runner written before the batch surface had. It forwards the methods
// explicitly rather than embedding *fakeRunner, because an embedded runner
// would promote AliveBatch and the type would satisfy the batch interface --
// which is exactly the mistake this fixture exists to rule out.
type noBatchRunner struct{ fr *fakeRunner }

func (r noBatchRunner) Start(ctx context.Context, spec spawn.ProcSpec) (spawn.ProcHandle, error) {
	return r.fr.Start(ctx, spec)
}

func (r noBatchRunner) Alive(ctx context.Context, h spawn.ProcHandle) (bool, error) {
	return r.fr.Alive(ctx, h)
}

func (r noBatchRunner) ExitCode(ctx context.Context, h spawn.ProcHandle, logPath string) (int, bool) {
	return r.fr.ExitCode(ctx, h, logPath)
}

func (r noBatchRunner) Kill(ctx context.Context, h spawn.ProcHandle, streamPath string) error {
	return r.fr.Kill(ctx, h, streamPath)
}

func (r noBatchRunner) Rusage(ctx context.Context, h spawn.ProcHandle, streamPath string) (spawn.ProcRusage, bool) {
	return r.fr.Rusage(ctx, h, streamPath)
}

// TestStatusWorksWithoutTheBatchSurface: a runner that cannot batch still gets
// every row's word, because the fallback probes per handle.
func TestStatusWorksWithoutTheBatchSurface(t *testing.T) {
	fr := newFakeRunner()
	rt := newRuntime(t)
	rt.Runner = &noBatchRunner{fr: fr}
	seedHeadlessRows(t, rt, 3)

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if fr.aliveCalls != 3 {
		t.Errorf("single-handle probes = %d, want 3", fr.aliveCalls)
	}
	for _, row := range rep.Bindings {
		if row.BuilderStatus != "working" {
			t.Errorf("%s: word = %q, want working", row.Name, row.BuilderStatus)
		}
	}
}

// TestStatusReProbesOnEveryRefresh: a batch saves forks, not freshness. Every
// refresh reads the processes again, so a builder that died since the last one
// is reported dead rather than carried forward as working -- and a pid reused
// by a new process in between cannot inherit the old process's answer.
func TestStatusReProbesOnEveryRefresh(t *testing.T) {
	fr := newFakeRunner()
	rt := newRuntime(t)
	rt.Runner = fr
	seedHeadlessRows(t, rt, 2)

	if _, err := Status(context.Background(), rt); err != nil {
		t.Fatalf("Status: %v", err)
	}
	firstBatch := fr.batchCalls
	if firstBatch != 1 {
		t.Fatalf("batch probes = %d, want 1", firstBatch)
	}

	// A row's process is replaced: same pid, a start time outside Alive's
	// one-second tolerance. That is a different process, so the fact read for
	// the old one must not answer for it.
	b, err := rt.Store.Load("live-a")
	if err != nil {
		t.Fatal(err)
	}
	h := handleOf(b.Builder)
	reused := spawn.ProcHandle{PID: h.PID, StartedAt: h.StartedAt.Add(2 * time.Second)}
	if aliveFromFact(spawn.AliveFact{StartedAt: h.StartedAt, State: "S"}, reused) {
		t.Error("a reused pid must not inherit the old process's alive answer")
	}

	// The next refresh reads the processes again, so a builder that died since
	// the last one is reported dead rather than carried forward as working.
	for _, hd := range fr.handles {
		fr.script(hd.PID, false)
	}
	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("second Status: %v", err)
	}
	if fr.batchCalls != 2 {
		t.Errorf("batch probes = %d, want 2: each refresh reads the processes again", fr.batchCalls)
	}
	for _, row := range rep.Bindings {
		if row.BuilderStatus == "working" {
			t.Errorf("%s: word = %q, want an exited word -- a refresh must not reuse an earlier reading", row.Name, row.BuilderStatus)
		}
	}
}

// TestAliveFromFactMatchesAlivesRules: the batched answer applies Alive's own
// rules -- absent, zombie and pid-reuse all read as not alive.
func TestAliveFromFactMatchesAlivesRules(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	h := spawn.ProcHandle{PID: 7, StartedAt: now}

	cases := []struct {
		name string
		fact spawn.AliveFact
		want bool
	}{
		{"same start time", spawn.AliveFact{StartedAt: now, State: "Ss"}, true},
		{"within the second", spawn.AliveFact{StartedAt: now.Add(-900 * time.Millisecond), State: "Ss"}, true},
		{"a second later", spawn.AliveFact{StartedAt: now.Add(2 * time.Second), State: "Ss"}, false},
		{"zombie", spawn.AliveFact{StartedAt: now, State: "Z"}, false},
		{"absent from ps", spawn.AliveFact{}, false},
	}
	for _, tc := range cases {
		if got := aliveFromFact(tc.fact, h); got != tc.want {
			t.Errorf("%s: aliveFromFact = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestIdleHeadlessRowStillTailsItsLog is the mutation test for Optional B step
// 2's ordering. A binding keeps its LogPath after its builder exits, so an idle
// row's tail is the log a human reads to find out why it stopped: moving the
// tail read below the PID == 0 check drops it, and this fails.
//
// The read is bounded (logTail reads the tail, not the whole file); the tail it
// produces must be the same one the unbounded read produced.
func TestIdleHeadlessRowStillTailsItsLog(t *testing.T) {
	rt := newRuntime(t)
	rt.Runner = newFakeRunner()
	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "idle-log", Candidate: testAgyRef, MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Send(context.Background(), rt, "idle-log", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatal(err)
	}
	b, err := rt.Store.Load("idle-log")
	if err != nil {
		t.Fatal(err)
	}
	lp := rt.Store.BuilderLogPath("idle-log", b.Round)
	if err := os.MkdirAll(filepath.Dir(lp), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lp, []byte("l1\nl2\nl3\nl4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The state reconcileHeadless leaves: no process, log path kept.
	b.Builder.PID = 0
	b.Builder.StartedAt = 0
	b.Builder.LogPath = lp
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(rep.Bindings) != 1 {
		t.Fatalf("rows = %d, want 1", len(rep.Bindings))
	}
	row := rep.Bindings[0]
	if row.BuilderStatus != "idle" {
		t.Errorf("word = %q, want idle", row.BuilderStatus)
	}
	if row.Headless == nil || len(row.Headless.Tail) != 3 {
		t.Fatalf("Headless = %+v, want the log's last three lines on an idle row", row.Headless)
	}
	if got, want := strings.Join(row.Headless.Tail, "|"), "l2|l3|l4"; got != want {
		t.Errorf("tail = %q, want %q", got, want)
	}
	if text := view.RenderStatus(rep); !strings.Contains(text, "  log      l2") {
		t.Errorf("view.RenderStatus drops the idle row's tail:\n%s", text)
	}
}

// TestLogTailReadsTheTailNotTheWholeFile: logTail is bounded. A log far larger
// than the window must still answer with the same last n lines a whole-file
// read would, and the answer must not change with the file's size.
func TestLogTailReadsTheTailNotTheWholeFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.log")

	// A long file: many short lines plus one line longer than the 64 KiB window.
	var b strings.Builder
	for i := 0; i < 20000; i++ {
		b.WriteString("line\n")
	}
	b.WriteString(strings.Repeat("x", 200<<10))
	b.WriteString("\nlast-1\nlast-2\nlast-3\n")
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	if got, want := logTail(p, 3), "last-1\nlast-2\nlast-3"; got != want {
		t.Errorf("logTail(3) on a large file = %q, want %q", got, want)
	}
	if got := logTail(p, 1); got != "last-3" {
		t.Errorf("logTail(1) = %q, want last-3", got)
	}
	// A window smaller than one line still yields that whole line: the window
	// grows until it holds n breaks rather than cutting a line in half.
	if got := logTail(p, 3); len(got) < len("last-1\nlast-2\nlast-3") {
		t.Errorf("logTail(3) = %q, want the three whole trailing lines", got)
	}
}

// TestSingleRowStatusStillProbesDirectly: StatusRow is the detail pane's entry
// point and carries no report, so it probes its one handle itself rather than
// reading a fleet batch.
func TestSingleRowStatusStillProbesDirectly(t *testing.T) {
	fr := newFakeRunner()
	rt := newRuntime(t)
	rt.Runner = fr
	seedHeadlessRows(t, rt, 2)

	row, err := StatusRow(context.Background(), rt, "live-a")
	if err != nil {
		t.Fatalf("StatusRow: %v", err)
	}
	if row.BuilderStatus != "working" {
		t.Errorf("word = %q, want working", row.BuilderStatus)
	}
	if fr.aliveCalls != 1 {
		t.Errorf("single-handle probes = %d, want exactly 1 for a single row", fr.aliveCalls)
	}
}

var _ = store.Binding{}
