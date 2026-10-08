package ui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// slowSource parks Status for as long as its test holds it, so a test can
// watch what the shell does while the first fetch is still out. A test
// releases the park before the shell's refetch runs, so that fetch answers
// immediately. Every other method is the wrapped source's.
type slowSource struct {
	inner   Source
	release chan struct{}

	mu     sync.Mutex
	parked bool
	calls  int
}

func newSlowSource(inner Source) *slowSource {
	return &slowSource{inner: inner, release: make(chan struct{}), parked: true}
}

func (s *slowSource) Status(ctx context.Context) (view.Report, error) {
	s.mu.Lock()
	s.calls++
	parked := s.parked
	s.mu.Unlock()
	if parked {
		<-s.release
	}
	return s.inner.Status(ctx)
}

func (s *slowSource) Runtime(key string) (relevo.Runtime, string, bool) {
	return s.inner.Runtime(key)
}
func (s *slowSource) Base() relevo.Runtime  { return s.inner.Base() }
func (s *slowSource) MarkViewed(key string) { s.inner.MarkViewed(key) }

// unblock ends the park. It is safe to call twice, so a test can land the
// fetch and a cleanup can still release a goroutine left behind.
func (s *slowSource) unblock() {
	s.mu.Lock()
	s.parked = false
	s.mu.Unlock()
	select {
	case <-s.release:
	default:
		close(s.release)
	}
}

// callCount is how many times Status was entered.
func (s *slowSource) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// pending is the parked first statusMsg, delivered once src is released.
type pending struct {
	src  *slowSource
	done chan tea.Msg
}

// land releases the first fetch and returns the statusMsg it delivers.
func (p pending) land(t *testing.T) statusMsg {
	t.Helper()
	p.src.unblock()
	select {
	case msg := <-p.done:
		sm, ok := msg.(statusMsg)
		if !ok {
			t.Fatalf("the landing must be a statusMsg, got %T", msg)
		}
		return sm
	case <-time.After(30 * time.Second):
		t.Fatal("the released first fetch never landed")
		return statusMsg{}
	}
}

// stillBlocked reports whether the first fetch is genuinely out: it fails the
// test if a statusMsg arrived before the release.
func (p pending) stillBlocked(t *testing.T) {
	t.Helper()
	select {
	case msg := <-p.done:
		t.Fatalf("a parked Source.Status must not deliver a statusMsg, got %#v", msg)
	case <-time.After(50 * time.Millisecond):
	}
}

// slowShell is a shell whose first fetch is in flight as bubbletea runs it,
// with the watchdog already fired and a sized box.
type slowShell struct {
	src  *slowSource
	m    Model
	pend pending
}

// newSlowShell builds the slow shell; the caller lands the fetch when it
// chooses, or never.
func newSlowShell(t *testing.T) slowShell {
	t.Helper()
	st := store.New(t.TempDir())
	for _, name := range []string{"api", "webshop"} {
		b := newTestBinding(name)
		b.CWD = "/tmp/" + name
		if err := st.Save(b); err != nil {
			t.Fatalf("Save(%s): %v", name, err)
		}
	}
	src := newSlowSource(mastermindSource{relevo.Runtime{Store: st}})
	m := newModel(context.Background(), src, Options{Interval: time.Millisecond})
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m = res.(Model)

	pend := pending{src: src, done: make(chan tea.Msg, 1)}
	fetch := fetchStatus(m.ctx, src)
	go func() { pend.done <- fetch() }()
	t.Cleanup(src.unblock)

	res, cmd := m.Update(watchdogMsg(time.Now()))
	m = res.(Model)
	if cmd != nil {
		t.Fatal("the watchdog must return no command")
	}
	if !m.firstFetchSlow {
		t.Fatal("a watchdog that fires before the first status must mark the fetch slow")
	}
	return slowShell{src: src, m: m, pend: pend}
}

// statusFetches reports whether a returned command issued a status fetch. It
// invokes every command exactly once, as the bubbletea loop does: a bubbletea
// timer is a single-shot closure, so running one twice blocks forever. That
// is why this cannot go through extractBatch, which already invokes the
// batch it unwraps.
func statusFetches(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			if statusFetches(c) {
				return true
			}
		}
	case statusMsg:
		return true
	}
	return false
}

// fastShell is a sized shell whose first status already landed inside the
// box, with no watchdog outstanding.
func fastShell(t *testing.T) Model {
	t.Helper()
	st := store.New(t.TempDir())
	m := newModel(context.Background(), mastermindSource{relevo.Runtime{Store: st}},
		Options{Interval: time.Second})
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m = res.(Model)
	res, _ = m.Update(statusMsg{report: view.Report{}})
	if res.(Model).firstFetchSlow {
		t.Fatal("a fast shell must not be marked slow")
	}
	return res.(Model)
}

// TestWatchdogPaintsTheBoundedLoadingStateAndNothingElse: the watchdog marks
// the first fetch slow and paints the same bounded loading state the fleet
// was already drawing -- it merges no report of its own.
func TestWatchdogPaintsTheBoundedLoadingStateAndNothingElse(t *testing.T) {
	sh := newSlowShell(t)

	if sh.m.statusLoaded {
		t.Error("the watchdog must not mark a status loaded")
	}
	if len(sh.m.report.Bindings) != 0 {
		t.Errorf("the watchdog must carry no report, got %d rows", len(sh.m.report.Bindings))
	}
	if !strings.Contains(sh.m.View(), "loading…") {
		t.Errorf("the bounded loading state must still be painted:\n%s", sh.m.View())
	}
}

// TestWatchdogAfterAFastFirstStatusDoesNothing: a first fetch that landed
// inside the box leaves nothing to re-fetch, so the late watchdog must not
// arm a refetch.
func TestWatchdogAfterAFastFirstStatusDoesNothing(t *testing.T) {
	m := fastShell(t)

	res, _ := m.Update(watchdogMsg(time.Now()))
	if res.(Model).firstFetchSlow {
		t.Error("a watchdog after the first status landed must not mark it slow")
	}
}

// TestSlowFirstStatusRefetchesOnLanding: a first status the watchdog called
// slow refetches the moment it lands, rather than waiting out the tick
// interval, and the single-flight guard covers the refetch.
func TestSlowFirstStatusRefetchesOnLanding(t *testing.T) {
	sh := newSlowShell(t)

	res, cmd := sh.m.Update(sh.pend.land(t))
	landed := res.(Model)

	if !landed.statusLoaded {
		t.Fatal("the landed status must load")
	}
	if !statusFetches(cmd) {
		t.Error("a slow first status must refetch on landing")
	}
	if landed.firstFetchSlow {
		t.Error("the re-fire must be one-shot")
	}
	if !landed.statusInFlight {
		t.Error("the refetch must hold the single-flight guard")
	}

	// The tick that arrives during the refetch starts no second fetch.
	res, cmd = landed.Update(tickMsg(time.Now()))
	if statusFetches(cmd) {
		t.Error("a tick during the refetch must not start a second fetch")
	}
	if !res.(Model).statusInFlight {
		t.Error("the tick must leave the guard set")
	}
}

// TestFastFirstStatusDoesNotRefetch: a first status inside the box waits for
// the tick, so the fast path costs exactly one fetch.
func TestFastFirstStatusDoesNotRefetch(t *testing.T) {
	m := fastShell(t)

	res, cmd := m.Update(statusMsg{report: view.Report{Bindings: []view.BindingStatus{
		{Name: "api", Round: 2, Display: "ACTIVE"},
	}}})
	if statusFetches(cmd) {
		t.Error("a first status inside the box must not refetch on landing")
	}
	if res.(Model).statusInFlight {
		t.Error("no refetch means no in-flight status")
	}
}

// TestSlowSourceStatusNeverYieldsAPartialStatusMsg is the all-or-nothing
// pin: while the first fetch is out the shell holds an empty report and a
// loading paint, and the statusMsg it eventually delivers is the whole fleet
// -- never a partial one the watchdog could have merged.
func TestSlowSourceStatusNeverYieldsAPartialStatusMsg(t *testing.T) {
	sh := newSlowShell(t)

	sh.pend.stillBlocked(t)
	if sh.m.statusLoaded || len(sh.m.report.Bindings) != 0 {
		t.Fatal("the shell must hold no report while the first fetch is out")
	}

	msg := sh.pend.land(t)
	if msg.err != nil {
		t.Fatalf("the landing statusMsg carried an error: %v", msg.err)
	}
	if got := len(msg.report.Bindings); got != 2 {
		t.Errorf("the landed statusMsg must carry the whole fleet: %d rows, want 2", got)
	}
	if got := sh.src.callCount(); got != 1 {
		t.Errorf("a parked Source.Status was entered %d times, want 1", got)
	}
}

// TestInitArmsTheWatchdogBesideTheFirstFetch: Init goes out with the first
// fetch, the tick and the watchdog, so the box is armed exactly once.
func TestInitArmsTheWatchdogBesideTheFirstFetch(t *testing.T) {
	st := store.New(t.TempDir())
	m := newModel(context.Background(), mastermindSource{relevo.Runtime{Store: st}},
		Options{Interval: time.Millisecond})

	var ticks, dogs, fetches int
	for _, c := range extractBatch(m.Init()) {
		switch c().(type) {
		case tickMsg:
			ticks++
		case watchdogMsg:
			dogs++
		case statusMsg:
			fetches++
		}
	}
	if ticks != 1 || dogs != 1 || fetches != 1 {
		t.Errorf("Init must arm one tick, one watchdog and one fetch: ticks=%d watchdog=%d fetches=%d",
			ticks, dogs, fetches)
	}
}
