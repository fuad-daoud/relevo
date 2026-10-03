package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/delivery"
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

// TestRelevoVerbsWaitOversizeDeliversEveryEntry pins the over-cap branch: the
// wait confirmed every entry PullPendingThrough found, so it has to show every
// one of them. The old branch confirmed them all and printed one path plus a
// claiming hint, which dropped the content of each entry it had just taken --
// the caller could not read round 1 at all and reading the pointer consumed the
// next pending payload of round 2.
//
// What is asserted is the invariant rather than one exact string: round 1 is
// delivered, round 2 is either delivered or pointed at, and the bare path the
// old branch printed appears nowhere. Every pointer carries --peek, so reading
// one back is not a second claim.
func TestRelevoVerbsWaitOversizeDeliversEveryEntry(t *testing.T) {
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

	if !strings.HasPrefix(text, "big round 2 closed\n── round 1: not delivered earlier ("+path1+") ──\n") {
		t.Errorf("wait text = %q, want the outcome line then round 1 under its header", text)
	}
	if !strings.Contains(text, body) {
		t.Error("wait text does not carry round 1's body: an entry was confirmed but never delivered")
	}

	// Round 2 has 36 KiB of budget left after round 1, so it is cut to that --
	// and the cut line is what names the round, so it is still represented.
	const peek2 = "relevo show big --round 2 --report --peek"
	if !strings.Contains(text, peek2) {
		t.Errorf("wait text = %q, want it to name round 2 by its peek pointer %q", text, peek2)
	}

	// The old branch's whole answer was a path and a claiming hint. Neither may
	// survive: the path is not a delivery, and the hint without --peek claims.
	if strings.Contains(text, "\n"+path2+"\n") || strings.HasSuffix(text, "\n"+path2) {
		t.Errorf("wait text = %q, want no bare report path in place of the payload", text)
	}
	if strings.Contains(text, "relevo show big --round 2 --report\n") {
		t.Errorf("wait text = %q, want no claiming `relevo show` pointer", text)
	}

	// Every confirmed entry was taken, so a second wait on the same round has
	// nothing left to deliver and reports the outcome alone.
	second := waitText(t, func() (any, error) { return v.Wait(context.Background(), "", WaitArgs{Name: "big", Round: 2}) })
	if second != "big round 2 closed" {
		t.Errorf("second wait text = %q, want exactly 'big round 2 closed'", second)
	}
}

// TestRelevoVerbsWaitOversizeSingleEntryIsCutNotPointedAt covers the other
// shape: one entry whose own text is over the cap. It is cut to the cap and
// named in the cut line, so the caller gets most of the report rather than a
// filename -- the old branch answered this case with the path alone.
func TestRelevoVerbsWaitOversizeSingleEntryIsCutNotPointedAt(t *testing.T) {
	s := store.New(t.TempDir())
	saveVerbBinding(t, s, store.Binding{Name: "one", CWD: "/repo-one", MasterMindID: mcpTestMasterMindA, Round: 1, State: store.StateActive})

	// A 40 KiB origin line plus a 60 KiB report: past the 96 KiB cap on its
	// own, so the single entry is the whole over-cap payload.
	payload := strings.Repeat("p", 40*1024)
	path := s.ReportPath("one", 1)
	writeStoreFile(t, path, strings.Repeat("A", 60*1024))

	for _, e := range []store.LogEntry{
		{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt},
		{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: path, Payload: payload},
	} {
		if err := s.AppendLog("one", e); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}

	v := waitVerbsFor(s, time.Now)
	text := waitText(t, func() (any, error) { return v.Wait(context.Background(), "", WaitArgs{Name: "one", Round: 1}) })

	if !strings.HasPrefix(text, "one round 1 closed\n"+payload) {
		t.Error("wait text does not open with the entry's own text: a single oversize entry must still be delivered")
	}
	if !strings.Contains(text, "[truncated at 98304 bytes -- full text: relevo show one --round 1 --report --peek]") {
		t.Errorf("wait text = %q, want the cap-cut line naming the peek pointer", text)
	}
	if strings.Contains(text, path) {
		t.Errorf("wait text = %q, want no bare report path in place of the payload", text)
	}
}

// TestRelevoVerbsWaitOversizePointsAtAHaltEntry: a halt entry a halt queues is
// an ordinary claimable payload, so it is one of the entries an over-cap wait
// confirmed -- and it is delivered or pointed at like any other. The budget is
// gone by then, so the pointer is what arrives; what must not happen is the
// halt being confirmed and printed nowhere.
func TestRelevoVerbsWaitOversizePointsAtAHaltEntry(t *testing.T) {
	s := store.New(t.TempDir())
	now := func() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) }
	halt := "builder exited (code 1) without a report"
	saveVerbBinding(t, s, store.Binding{
		Name: "halty", CWD: "/repo-halty", MasterMindID: mcpTestMasterMindA,
		Round: 3, State: store.StateNeedsYou,
		Halt: halt, HaltAt: now(),
	})

	body := strings.Repeat("A", 60*1024)
	for r := 1; r <= 2; r++ {
		p := s.ReportPath("halty", r)
		writeStoreFile(t, p, body)
		for _, e := range []store.LogEntry{
			{Round: r, Direction: store.DirToBuilder, Kind: store.KindPrompt},
			{Round: r, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: p},
		} {
			if err := s.AppendLog("halty", e); err != nil {
				t.Fatalf("AppendLog: %v", err)
			}
		}
	}
	if err := s.AppendLog("halty", store.LogEntry{Round: 3, Direction: store.DirToBuilder, Kind: store.KindPrompt}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	deps := delivery.Deps{Store: s, Now: now}
	if err := s.WithLock(func(tx *store.Tx) error {
		return delivery.Queue(context.Background(), deps, tx, "halty", store.LogEntry{
			Round: 3, Direction: store.DirToMasterMind, Kind: store.KindHalt,
			Note:    halt,
			Payload: halt + ". relevo status --name halty",
		})
	}); err != nil {
		t.Fatalf("Queue: %v", err)
	}

	v := waitVerbsFor(s, now)
	text := waitText(t, func() (any, error) { return v.Wait(context.Background(), "", WaitArgs{Name: "halty", Round: 3}) })

	if !strings.HasPrefix(text, "halty round 3 needs-you\n") {
		t.Errorf("wait text = %q, want the needs-you outcome line first", text)
	}
	// The halt entry names no show section of its own, so the pointer is the
	// round's log -- and --peek, so following it claims nothing.
	if !strings.Contains(text, "relevo show halty --round 3 --log --peek") {
		t.Errorf("wait text = %q, want a peek pointer naming the round 3 halt entry", text)
	}
	if !strings.Contains(text, "── round 1: not delivered earlier") {
		t.Errorf("wait text = %q, want round 1 delivered before the pointed-at halt", text)
	}
}

// TestPeekRefIsNeverClaiming pins the pointer itself: whatever kind an entry is,
// the command this branch hands back carries --peek, because a plain
// `relevo show` would claim the oldest pending payload of its round and
// an already-confirmed entry has nothing to give.
func TestPeekRefIsNeverClaiming(t *testing.T) {
	b := store.Binding{Name: "webshop", Shape: store.ShapeWriter}
	reader := store.Binding{Name: "webshop", Shape: store.ShapeReader}

	tests := []struct {
		name    string
		binding store.Binding
		entry   store.LogEntry
		want    string
	}{
		{"a writer's report", b, store.LogEntry{Round: 2, Kind: store.KindReport, Path: "/x/002-report.md"}, "relevo show webshop --round 2 --report --peek"},
		{"a reader's report", reader, store.LogEntry{Round: 2, Kind: store.KindReport, Path: "/x/002-output.md"}, "relevo show webshop --round 2 --output --peek"},
		{"a diff", b, store.LogEntry{Round: 2, Kind: store.KindDiff, Path: "/x/002.diff"}, "relevo show webshop --round 2 --diff --peek"},
		{"a halt entry names no section, so it points at the log", b, store.LogEntry{Round: 3, Kind: store.KindHalt}, "relevo show webshop --round 3 --log --peek"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := peekRef(tt.binding, tt.entry); got != tt.want {
				t.Errorf("peekRef = %q, want %q", got, tt.want)
			}
		})
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

// TestRelevoVerbsWaitPullsAHaltEntryOnce pins the pairing a halt's queued entry
// buys: the first wait carries the halt text as tool output, and the second
// falls back to the needs-you outcome line alone.
//
// Both halves matter. With nothing queued the first wait prints res.Line and
// the second prints res.Line too -- two identical results, and a mastermind
// polling in a loop cannot tell a first halt from a repeat. The entry is what
// makes the first call carry the payload and the second carry nothing, so both
// are asserted rather than either.
func TestRelevoVerbsWaitPullsAHaltEntryOnce(t *testing.T) {
	s := store.New(t.TempDir())
	now := func() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) }
	halt := "builder exited (code 1) without a report"
	saveVerbBinding(t, s, store.Binding{
		Name: "halted", CWD: "/repo-halted", MasterMindID: mcpTestMasterMindA,
		Round: 1, State: store.StateNeedsYou,
		Halt: halt, HaltAt: now(),
	})
	if err := s.AppendLog("halted", store.LogEntry{
		Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt,
	}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	// The entry as haltBinding writes it: queued through delivery.Queue, so the
	// origin line and the unconfirmed state are the real producer's, not a
	// hand-written approximation of them.
	deps := delivery.Deps{Store: s, Now: now}
	if err := s.WithLock(func(tx *store.Tx) error {
		return delivery.Queue(context.Background(), deps, tx, "halted", store.LogEntry{
			Round: 1, Direction: store.DirToMasterMind, Kind: store.KindHalt,
			Note:    halt,
			Payload: halt + ". relevo status --name halted",
		})
	}); err != nil {
		t.Fatalf("Queue: %v", err)
	}

	v := waitVerbsFor(s, now)

	first := waitText(t, func() (any, error) { return v.Wait(context.Background(), "", WaitArgs{Name: "halted"}) })
	if !strings.HasPrefix(first, "halted round 1 needs-you\n") {
		t.Errorf("wait #1 = %q, want it to open with the needs-you outcome line", first)
	}
	if !strings.Contains(first, halt) {
		t.Errorf("wait #1 = %q, want the queued halt text", first)
	}
	if !strings.Contains(first, "relevo status --name halted") {
		t.Errorf("wait #1 = %q, want the halt's pointer too", first)
	}

	// Second wait: the entry was confirmed by the first, so there is nothing
	// left to pull and formatWaitResult falls back to res.Line.
	second := waitText(t, func() (any, error) { return v.Wait(context.Background(), "", WaitArgs{Name: "halted"}) })
	if second != "halted round 1 needs-you\n"+halt {
		t.Errorf("wait #2 = %q, want the needs-you outcome line only", second)
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

// TestOversizeWaitBodyLabelsAnEntryWithNoPath pins the same label in the
// over-cap renderer. A halt entry carries no artifact, so its Path is empty by
// design and the header rendered "not delivered earlier ()" -- a label that
// named nothing while implying that it had. Both renderers share one header
// function, so this and the delivery-side case cannot drift apart.
func TestOversizeWaitBodyLabelsAnEntryWithNoPath(t *testing.T) {
	s := store.New(t.TempDir())

	res := relevo.WaitResult{
		Round: 1,
		Delivered: []delivery.Delivered{
			{Entry: store.LogEntry{Round: 1, Kind: store.KindHalt}, Text: "halting: the round ran past its budget"},
			{Entry: store.LogEntry{Round: 1, Kind: store.KindReport, Path: "/repo-big/001-report.md"}, Text: "round 1 done"},
		},
	}

	body := oversizeWaitBody(s, "big", 1, res)

	if strings.Contains(body, "()") {
		t.Errorf("an entry with no path rendered empty parens:\n%s", body)
	}
	if !strings.Contains(body, "── round 1: not delivered earlier ──\n") {
		t.Errorf("the label does not name the entry's round:\n%s", body)
	}
	if !strings.Contains(body, "halting: the round ran past its budget") {
		t.Errorf("the entry's own text is missing:\n%s", body)
	}
	if !strings.HasSuffix(body, "round 1 done") {
		t.Errorf("the waited entry is not last:\n%s", body)
	}
}
