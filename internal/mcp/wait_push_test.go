package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// liveClaimStore is a ClaimStore holding one live claim for id.
type liveClaimStore struct{ id string }

func (l liveClaimStore) Live(mastermind string, now time.Time) (*delivery.Claim, error) {
	if mastermind == l.id {
		return &delivery.Claim{MasterMind: l.id, PID: 1}, nil
	}
	return nil, nil
}
func (l liveClaimStore) Write(delivery.Claim, time.Time) error { return nil }
func (l liveClaimStore) Remove(string, int) error              { return nil }

// TestWaitUnderPushClaimDoesNotClaimOrConfirm is the gating rule: with a live
// push claim, the wait reports the outcome line plus "delivered by the mod" and
// touches no entry.
func TestWaitUnderPushClaimDoesNotClaimOrConfirm(t *testing.T) {
	s := store.New(t.TempDir())
	saveVerbBinding(t, s, store.Binding{Name: "webshop", CWD: "/repo-ws", MasterMindID: mcpTestMasterMindA, Round: 1, State: store.StateActive})

	reportBody := "# Report\nRound 1 completed successfully."
	writeStoreFile(t, s.ReportPath("webshop", 1), reportBody)
	if err := s.AppendLog("webshop", store.LogEntry{
		Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport,
		Path: s.ReportPath("webshop", 1), Payload: "origin",
	}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	v := &RelevoVerbs{
		RT:           relevo.Runtime{Store: s, Now: time.Now, Channels: liveClaimStore{id: mcpTestMasterMindA}},
		MasterMind:   mcpTestMasterMindA,
		WaitInterval: time.Millisecond,
	}
	text := waitText(t, func() (any, error) { return v.Wait(context.Background(), "", WaitArgs{Name: "webshop"}) })

	if !strings.HasPrefix(text, "webshop round 1 closed") {
		t.Errorf("wait text = %q, want the closed outcome line", text)
	}
	if !strings.HasSuffix(text, "delivered by the mod") {
		t.Errorf("wait text = %q, want the delivered-by-the-mod note", text)
	}
	if strings.Contains(text, reportBody) {
		t.Errorf("wait text = %q, must not carry a payload the push holder owns", text)
	}

	entries, err := s.ReadLog("webshop")
	if err != nil || len(entries) != 1 {
		t.Fatalf("ReadLog: %v (%d entries)", err, len(entries))
	}
	if entries[0].Confirmed {
		t.Error("a wait under a live push claim must not confirm the entry")
	}
}

// TestWaitUnderPushClaimWhileHolderDrains pins the whole race: the holder
// writes and acks an entry while the wait is under the same live claim, the
// wait still claims nothing, and the log shows exactly one confirm, route push.
func TestWaitUnderPushClaimWhileHolderDrains(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	claims := &delivery.KVClaims{KV: db.TxKV{DB: d}, Alive: func(int) bool { return true }}

	s := store.New(t.TempDir())
	saveVerbBinding(t, s, store.Binding{Name: "webshop", CWD: "/repo-ws", MasterMindID: mcpTestMasterMindA, Round: 1, State: store.StateActive})
	if err := s.AppendLog("webshop", store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt}); err != nil {
		t.Fatalf("AppendLog prompt: %v", err)
	}

	deps := delivery.Deps{Store: s, Now: time.Now, Channels: claims}
	if err := s.WithLock(func(tx *store.Tx) error {
		return delivery.Queue(context.Background(), deps, tx, "webshop", store.LogEntry{
			Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport,
			Path: s.ReportPath("webshop", 1), Payload: "origin",
		})
	}); err != nil {
		t.Fatalf("Queue: %v", err)
	}

	lineOut, lineWriter := io.Pipe()
	defer func() { _ = lineOut.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- delivery.RunPush(ctx, deps, mcpTestMasterMindA, lineWriter) }()

	ev := readPushEvent(t, lineOut)
	if ev.Binding != "webshop" || ev.Kind != string(store.KindReport) {
		t.Fatalf("push event = %+v, want the webshop report", ev)
	}

	// Wait under the same live claim: outcome line only, no claim, no confirm.
	v := &RelevoVerbs{
		RT:           relevo.Runtime{Store: s, Now: time.Now, Channels: claims},
		MasterMind:   mcpTestMasterMindA,
		WaitInterval: time.Millisecond,
	}
	text := waitText(t, func() (any, error) { return v.Wait(context.Background(), "", WaitArgs{Name: "webshop"}) })
	if !strings.HasPrefix(text, "webshop round 1 closed") || !strings.HasSuffix(text, "delivered by the mod") {
		t.Fatalf("wait text = %q, want the closed line plus the mod note", text)
	}

	if err := delivery.AckPush(deps, mcpTestMasterMindA, "webshop", ev.Seq); err != nil {
		t.Fatalf("AckPush: %v", err)
	}
	waitForSinglePushConfirm(t, s, "webshop")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunPush did not return after its context was cancelled")
	}
}

// readPushEvent reads one NDJSON push event.
func readPushEvent(t *testing.T, r io.Reader) delivery.PushEvent {
	t.Helper()
	line, err := bufio.NewReader(r).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read push line: %v", err)
	}
	var ev delivery.PushEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		t.Fatalf("decode push line %q: %v", line, err)
	}
	return ev
}

// waitForSinglePushConfirm waits until exactly one entry of name is confirmed
// with route push.
func waitForSinglePushConfirm(t *testing.T, s *store.Store, name string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pushConfirmCount(t, s, name) == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the holder did not confirm exactly one entry with route push")
}

// pushConfirmCount counts confirmed entries of name, failing if any carries a
// route other than push.
func pushConfirmCount(t *testing.T, s *store.Store, name string) int {
	t.Helper()
	entries, err := s.ReadLog(name)
	if err != nil {
		return 0
	}
	confirmed := 0
	for _, e := range entries {
		if !e.Confirmed {
			continue
		}
		confirmed++
		if e.Route != "push" {
			t.Errorf("entry confirmed with route %q, want push", e.Route)
		}
	}
	return confirmed
}
