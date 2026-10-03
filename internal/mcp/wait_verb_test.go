package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

func writeStoreFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// waitVerbsFor returns verbs over s that poll fast, so a test never waits on
// the store's own tick.
func waitVerbsFor(s *store.Store, now func() time.Time) *RelevoVerbs {
	return &RelevoVerbs{
		RT:           relevo.Runtime{Store: s, Now: now},
		MasterMind:   mcpTestMasterMindA,
		WaitInterval: time.Millisecond,
	}
}

func waitText(t *testing.T, call func() (any, error)) string {
	t.Helper()
	res, err := call()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	text, ok := res.(string)
	if !ok {
		t.Fatalf("res = %T, want string", res)
	}
	return text
}

func TestRelevoVerbsWaitTimeoutResolutionAndClamps(t *testing.T) {
	s := store.New(t.TempDir())
	saveVerbBinding(t, s, store.Binding{Name: "b1", CWD: "/repo1", RoundTimeoutMS: int(time.Hour / time.Millisecond)})
	saveVerbBinding(t, s, store.Binding{Name: "bdef", CWD: "/repo2"})

	tests := []struct {
		name string
		args WaitArgs
		want time.Duration
	}{
		{"omitted timeout takes the binding's round budget", WaitArgs{Name: "b1", HasProgressToken: true}, time.Hour},
		{"omitted timeout with no token is capped at the idle window", WaitArgs{Name: "bdef"}, 25 * time.Minute},
		{"omitted timeout with a token takes the store default", WaitArgs{Name: "bdef", HasProgressToken: true}, 24 * time.Hour},
		{"past the tool-timeout wall clamps to 27h", WaitArgs{Timeout: "30h", HasProgressToken: true}, 27 * time.Hour},
		{"past the wall with no token clamps to the idle window", WaitArgs{Timeout: "30h"}, 25 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := resolveWaitTimeout(tt.args, s)
			if err != nil {
				t.Fatalf("resolveWaitTimeout: %v", err)
			}
			if d != tt.want {
				t.Errorf("resolveWaitTimeout = %v, want %v", d, tt.want)
			}
		})
	}
}

// TestWaitOutcomeWordCoversEveryWaitCode pins the wire word each Wait exit
// code carries, including the fallback an unrecognised code gets.
func TestWaitOutcomeWordCoversEveryWaitCode(t *testing.T) {
	tests := []struct {
		code int
		want string
	}{
		{relevo.WaitClosed, "closed"},
		{relevo.WaitUnmarked, "unmarked"},
		{relevo.WaitNeedsYou, "needs-you"},
		{relevo.WaitGone, "gone"},
		{relevo.WaitHalted, "halted"},
		{relevo.WaitNotStarted, "not-started"},
		{relevo.WaitTimeout, "still-open"},
		{-7, "code--7"},
	}
	for _, tt := range tests {
		if got := waitOutcomeWord(tt.code); got != tt.want {
			t.Errorf("waitOutcomeWord(%d) = %q, want %q", tt.code, got, tt.want)
		}
	}
}

func TestRelevoVerbsWaitRejectsBadArgs(t *testing.T) {
	tests := []struct {
		name string
		args WaitArgs
	}{
		{"unparseable timeout", WaitArgs{Timeout: "invalid"}},
		{"negative timeout", WaitArgs{Timeout: "-5s"}},
		{"zero timeout", WaitArgs{Timeout: "0s"}},
		{"negative round", WaitArgs{Round: -1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateWaitArgs(tt.args); err == nil {
				t.Fatalf("validateWaitArgs(%+v) = nil, want an error", tt.args)
			}
		})
	}
}

func TestRelevoVerbsWaitRefusesChain(t *testing.T) {
	s := store.New(t.TempDir())
	if err := s.WithLock(func(tx *store.Tx) error {
		return tx.ChainPut(db.ChainRow{
			ID:     "ch_123",
			Name:   "mychain",
			Status: "active",
			Phase:  "build",
			Step:   "run",
			Plan:   1,
			Plans:  1,
		})
	}); err != nil {
		t.Fatalf("ChainPut: %v", err)
	}

	v := waitVerbsFor(s, time.Now)
	_, err := v.Wait(context.Background(), "", WaitArgs{Name: "mychain"})
	if err == nil {
		t.Fatal("waiting on a chain = nil error, want a refusal")
	}
	for _, want := range []string{"CLI-only", "relevo wait --name mychain"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q must name %q", err.Error(), want)
		}
	}
}

func TestRelevoVerbsWaitRefusesUnresolvableTargets(t *testing.T) {
	t.Run("a binding that does not exist", func(t *testing.T) {
		v := waitVerbsFor(store.New(t.TempDir()), time.Now)
		if _, err := v.Wait(context.Background(), "", WaitArgs{Name: "missing"}); err == nil {
			t.Fatal("wait on a missing binding = nil error, want an error")
		}
	})

	t.Run("a mastermind with no active bindings", func(t *testing.T) {
		v := waitVerbsFor(store.New(t.TempDir()), time.Now)
		_, err := v.Wait(context.Background(), "", WaitArgs{})
		if err == nil {
			t.Fatal("wait with no owned bindings = nil error, want an error naming the mastermind")
		}
		if !strings.Contains(err.Error(), mcpTestMasterMindA) {
			t.Errorf("refusal %q must name the mastermind id", err.Error())
		}
	})
}

func TestRelevoVerbsWaitDeliversClosedRoundOnce(t *testing.T) {
	s := store.New(t.TempDir())
	saveVerbBinding(t, s, store.Binding{Name: "webshop", CWD: "/repo-ws", MasterMindID: mcpTestMasterMindA, Round: 1, State: store.StateActive})

	reportBody := "# Report\nRound 1 completed successfully."
	writeStoreFile(t, s.ReportPath("webshop", 1), reportBody)
	if err := s.AppendLog("webshop", store.LogEntry{
		Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: s.ReportPath("webshop", 1),
	}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	v := waitVerbsFor(s, time.Now)
	text := waitText(t, func() (any, error) { return v.Wait(context.Background(), "", WaitArgs{Name: "webshop"}) })
	if !strings.HasPrefix(text, "webshop round 1 closed") {
		t.Errorf("text = %q, want it to start 'webshop round 1 closed'", text)
	}
	if !strings.Contains(text, reportBody) {
		t.Errorf("text = %q, want it to carry the report body", text)
	}

	// The payload is claimed first-wins, so a second waiter on the same round
	// gets the same outcome line and no payload.
	second := waitText(t, func() (any, error) { return v.Wait(context.Background(), "", WaitArgs{Name: "webshop"}) })
	if second != "webshop round 1 closed" {
		t.Errorf("second wait text = %q, want exactly 'webshop round 1 closed'", second)
	}
}

func TestRelevoVerbsWaitStillOpenWhenBoundElapses(t *testing.T) {
	s := store.New(t.TempDir())
	saveVerbBinding(t, s, store.Binding{Name: "active", CWD: "/repo-act", MasterMindID: mcpTestMasterMindA, Round: 1, State: store.StateActive})
	if err := s.AppendLog("active", store.LogEntry{
		Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt,
	}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	base := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	tick := 0
	nowFn := func() time.Time {
		now := base.Add(time.Duration(tick) * time.Minute)
		tick += 10
		return now
	}

	v := waitVerbsFor(s, nowFn)
	text := waitText(t, func() (any, error) { return v.Wait(context.Background(), "", WaitArgs{Name: "active", Timeout: "5m"}) })
	want := "active round 1 still-open\nround still open, call wait again"
	if text != want {
		t.Errorf("wait text = %q, want %q", text, want)
	}
}

func TestRelevoVerbsWaitCapsOversizePayloadToShowHint(t *testing.T) {
	s := store.New(t.TempDir())
	saveVerbBinding(t, s, store.Binding{Name: "big", CWD: "/repo-big", MasterMindID: mcpTestMasterMindA, Round: 2, State: store.StateActive})

	// Two rounds of 60 KiB each, plus their headers, pass the cap that a
	// single 64 KiB round would sit under.
	body := strings.Repeat("A", 60*1024)
	path1 := s.ReportPath("big", 1)
	path2 := s.ReportPath("big", 2)
	writeStoreFile(t, path1, body)
	writeStoreFile(t, path2, body)

	for _, e := range []store.LogEntry{
		{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt},
		{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: path1},
		{Round: 2, Direction: store.DirToBuilder, Kind: store.KindPrompt},
		{Round: 2, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: path2},
	} {
		if err := s.AppendLog("big", e); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}

	v := waitVerbsFor(s, time.Now)
	text := waitText(t, func() (any, error) { return v.Wait(context.Background(), "", WaitArgs{Name: "big", Round: 2}) })
	want := "big round 2 closed\n" + path2 + "\nrelevo show big --round 2 --report"
	if text != want {
		t.Errorf("wait text = %q, want %q", text, want)
	}
}

func TestRelevoVerbsWaitCancelledContextIsNormalResult(t *testing.T) {
	s := store.New(t.TempDir())
	saveVerbBinding(t, s, store.Binding{Name: "canc", CWD: "/repo-canc", MasterMindID: mcpTestMasterMindA, Round: 1, State: store.StateActive})
	if err := s.AppendLog("canc", store.LogEntry{
		Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt,
	}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	v := waitVerbsFor(s, time.Now)
	text := waitText(t, func() (any, error) { return v.Wait(ctx, "", WaitArgs{Name: "canc", Timeout: "1h"}) })
	if text != "canc round 1 cancelled" {
		t.Errorf("text = %q, want 'canc round 1 cancelled'", text)
	}
}

// TestRelevoVerbsWaitNeedsYouIsANormalResult: a round stalled on a human
// answers with its needs-you line and no error, which is what toolResultFrom
// turns into an isError:false result the model can act on.
func TestRelevoVerbsWaitNeedsYouIsANormalResult(t *testing.T) {
	s := store.New(t.TempDir())
	halt := "rate limit: gate the builder before the next round"
	saveVerbBinding(t, s, store.Binding{
		Name: "stalled", CWD: "/repo-stalled", MasterMindID: mcpTestMasterMindA,
		Round: 1, State: store.StateNeedsYou,
		Halt: halt, HaltAt: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC),
	})
	// The round was sent and is still open: only the halt, not a report entry,
	// makes the classifier call it needs-you.
	if err := s.AppendLog("stalled", store.LogEntry{
		Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt,
	}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	v := waitVerbsFor(s, time.Now)
	text := waitText(t, func() (any, error) { return v.Wait(context.Background(), "", WaitArgs{Name: "stalled"}) })
	want := "stalled round 1 needs-you\n" + halt
	if text != want {
		t.Errorf("wait text = %q, want %q", text, want)
	}
}

// TestRelevoVerbsWaitNoNameTakesTheDefaultBudget: with no name and no explicit
// timeout, the store's own default is the budget the poll runs on, so it has
// to arrive at the poll positive -- a zero there is refused outright.
func TestRelevoVerbsWaitNoNameTakesTheDefaultBudget(t *testing.T) {
	s := store.New(t.TempDir())
	saveVerbBinding(t, s, store.Binding{Name: "owned", CWD: "/repo-owned", MasterMindID: mcpTestMasterMindA, Round: 1, State: store.StateActive})
	if err := s.AppendLog("owned", store.LogEntry{
		Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt,
	}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	base := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	tick := 0
	nowFn := func() time.Time {
		now := base.Add(time.Duration(tick) * time.Minute)
		tick += 360
		return now
	}

	v := waitVerbsFor(s, nowFn)
	text := waitText(t, func() (any, error) {
		return v.Wait(context.Background(), "", WaitArgs{HasProgressToken: true})
	})
	// A timeout leaves WaitOwned with no binding and no round to name, so the
	// still-open line opens with blanks; what matters here is that the default
	// budget reached the poll at all, since a non-positive one is refused.
	want := " round 0 still-open\nround still open, call wait again"
	if text != want {
		t.Errorf("wait text = %q, want %q", text, want)
	}
}
