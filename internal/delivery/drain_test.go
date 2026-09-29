package delivery

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

// recordedPush is one call a fakePusher captured.
type recordedPush struct {
	content string
	meta    map[string]string
}

// fakePusher is the fake Pusher the plan asks Drain's tests to use: it
// records every push, and its first failCalls calls fail (so a test can
// prove a push failure leaves the entry pending and a later poll retries).
type fakePusher struct {
	pushes    []recordedPush
	failCalls int
	calls     int
}

func (f *fakePusher) Push(_ context.Context, content string, meta map[string]string) error {
	f.calls++
	if f.calls <= f.failCalls {
		return fmt.Errorf("push %d failed", f.calls)
	}
	cp := make(map[string]string, len(meta))
	for k, v := range meta {
		cp[k] = v
	}
	f.pushes = append(f.pushes, recordedPush{content: content, meta: cp})
	return nil
}

func saveDrainBinding(t *testing.T, s *store.Store, name, pane string, state store.State) store.Binding {
	t.Helper()
	b := store.Binding{
		Name:         name,
		CWD:          "/repo/" + name,
		MasterMind:   store.Endpoint{PaneID: pane},
		MasterMindID: testClaimMasterMind,
		Builder:      store.Endpoint{PaneID: "b:1"},
		Round:        1,
		State:        state,
	}
	if err := s.Save(b); err != nil {
		t.Fatalf("save binding %q: %v", name, err)
	}
	return b
}

func queueDrainEntry(t *testing.T, s *store.Store, name string, round int, kind store.Kind, path, payload string) {
	t.Helper()
	entry := store.LogEntry{Round: round, Direction: store.DirToMasterMind, Kind: kind, Path: path, Payload: payload}
	if err := s.WithLock(func(tx *store.Tx) error { return tx.AppendLog(name, entry) }); err != nil {
		t.Fatalf("append log for %q: %v", name, err)
	}
}

func TestDrainRequiresMasterMind(t *testing.T) {
	t.Parallel()

	rt := Deps{Store: store.New(t.TempDir())}
	if _, err := Drain(context.Background(), rt, &DrainState{}, &fakePusher{}); err == nil {
		t.Fatal("Drain with an empty MasterMind must error")
	}
}

// TestDrainFiltersByMasterMindID is the required case: Drain
// pushes only the bindings whose MasterMindID is the one it drains. The two
// bindings here share a pane, so only the mastermind id can tell them apart.
func TestDrainFiltersByMasterMindID(t *testing.T) {
	t.Parallel()

	s := store.New(t.TempDir())
	rt := Deps{Store: s}
	mine := saveDrainBinding(t, s, "mine", "w2:p3", store.StateActive)
	other := store.Binding{
		Name:         "other",
		CWD:          "/repo/other",
		MasterMind:   store.Endpoint{PaneID: "w2:p3"},
		MasterMindID: otherClaimMasterMind,
		Builder:      store.Endpoint{PaneID: "b:1"},
		Round:        1,
		State:        store.StateActive,
	}
	if err := s.Save(other); err != nil {
		t.Fatalf("save other: %v", err)
	}

	queueDrainEntry(t, s, "mine", 1, store.KindReport, "", "mine body")
	queueDrainEntry(t, s, "other", 1, store.KindReport, "", "other body")

	pusher := &fakePusher{}
	res, err := Drain(context.Background(), rt, &DrainState{MasterMind: mine.MasterMindID}, pusher)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if res.Pushed != 1 {
		t.Fatalf("Pushed = %d, want 1", res.Pushed)
	}
	if len(pusher.pushes) != 1 || pusher.pushes[0].meta["binding"] != "mine" {
		t.Fatalf("pushes = %+v, want only mine's", pusher.pushes)
	}
	if _, pending, err := s.PendingForMasterMind("other"); err != nil || !pending {
		t.Errorf("the other mastermind's entry must stay pending: pending=%v err=%v", pending, err)
	}
}

func TestDrainPushesThenConfirms(t *testing.T) {
	t.Parallel()

	s := store.New(t.TempDir())
	rt := Deps{Store: s}
	pane := "w2:p3"
	saveDrainBinding(t, s, "judge", pane, store.StateActive)
	queueDrainEntry(t, s, "judge", 3, store.KindReport, "/x/003-report.md", "the report body")

	st := &DrainState{MasterMind: testClaimMasterMind}
	pusher := &fakePusher{}

	res, err := Drain(context.Background(), rt, st, pusher)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if res.Pushed != 1 {
		t.Fatalf("Pushed = %d, want 1", res.Pushed)
	}
	if len(pusher.pushes) != 1 {
		t.Fatalf("pushes = %+v, want one", pusher.pushes)
	}

	got := pusher.pushes[0]
	if got.content != "the report body" {
		t.Errorf("content = %q, want the payload verbatim", got.content)
	}
	want := map[string]string{"binding": "judge", "round": "3", "kind": "report", "seq": "1", "show": "relevo show judge --round 3 --report"}
	if !reflect.DeepEqual(got.meta, want) {
		t.Errorf("meta = %+v, want %+v", got.meta, want)
	}

	if _, pending, err := s.PendingForMasterMind("judge"); err != nil || pending {
		t.Errorf("a pushed entry must be confirmed: pending=%v err=%v", pending, err)
	}
}

// TestDrainPushesExpandedReportText proves Drain pushes the report's text,
// not just the pointer payload: a report entry naming a readable
// path is expanded through PushText before it reaches the Pusher.
func TestDrainPushesExpandedReportText(t *testing.T) {
	t.Parallel()

	s := store.New(t.TempDir())
	rt := Deps{Store: s}
	pane := "w2:p3"
	saveDrainBinding(t, s, "judge", pane, store.StateActive)

	reportPath := filepath.Join(t.TempDir(), "003-report.md")
	if err := os.WriteFile(reportPath, []byte("the full report body"), 0o644); err != nil {
		t.Fatalf("write report file: %v", err)
	}
	queueDrainEntry(t, s, "judge", 3, store.KindReport, reportPath, "The runner finished round 3. Report: "+reportPath)

	st := &DrainState{MasterMind: testClaimMasterMind}
	pusher := &fakePusher{}

	if _, err := Drain(context.Background(), rt, st, pusher); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(pusher.pushes) != 1 {
		t.Fatalf("pushes = %+v, want one", pusher.pushes)
	}

	got := pusher.pushes[0].content
	want := "The runner finished round 3. Report: " + reportPath + "\n\nthe full report body"
	if got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestDrainOmitsShowMetaWhenEntryHasNone(t *testing.T) {
	t.Parallel()

	s := store.New(t.TempDir())
	rt := Deps{Store: s}
	pane := "w2:p3"
	saveDrainBinding(t, s, "judge", pane, store.StateActive)
	queueDrainEntry(t, s, "judge", 1, store.KindAnswer, "", "an answer, no file")

	st := &DrainState{MasterMind: testClaimMasterMind}
	pusher := &fakePusher{}
	if _, err := Drain(context.Background(), rt, st, pusher); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if _, ok := pusher.pushes[0].meta["show"]; ok {
		t.Errorf("meta must omit show when the entry has none, got %+v", pusher.pushes[0].meta)
	}
}

func TestDrainPushFailureLeavesPendingThenRetries(t *testing.T) {
	t.Parallel()

	s := store.New(t.TempDir())
	rt := Deps{Store: s}
	pane := "w2:p3"
	saveDrainBinding(t, s, "judge", pane, store.StateActive)
	queueDrainEntry(t, s, "judge", 1, store.KindReport, "", "payload one")

	st := &DrainState{MasterMind: testClaimMasterMind}
	pusher := &fakePusher{failCalls: 1}

	res, err := Drain(context.Background(), rt, st, pusher)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if res.Pushed != 0 || len(res.Failed) != 1 || res.Failed[0] != "judge" {
		t.Fatalf("first poll result = %+v, want Pushed 0, Failed [judge]", res)
	}
	if _, pending, err := s.PendingForMasterMind("judge"); err != nil || !pending {
		t.Errorf("a failed push must leave the entry pending: pending=%v err=%v", pending, err)
	}

	res, err = Drain(context.Background(), rt, st, pusher)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if res.Pushed != 1 {
		t.Fatalf("second poll result = %+v, want Pushed 1", res)
	}
	if _, pending, err := s.PendingForMasterMind("judge"); err != nil || pending {
		t.Errorf("the retried push must confirm: pending=%v err=%v", pending, err)
	}
}

func TestDrainSkipsOtherMasterMindsAndOwnedBindings(t *testing.T) {
	t.Parallel()

	s := store.New(t.TempDir())
	rt := Deps{Store: s}
	pane := "w2:p3"
	saveDrainBinding(t, s, "mine", pane, store.StateActive)

	// Another mastermind's binding, even on this same pane.
	other := store.Binding{
		Name: "other-mastermind", CWD: "/repo/other-mastermind",
		MasterMind: store.Endpoint{PaneID: pane}, MasterMindID: otherClaimMasterMind,
		Round: 1, State: store.StateActive,
	}
	if err := s.Save(other); err != nil {
		t.Fatalf("save other-mastermind binding: %v", err)
	}

	// This mastermind's binding, but owned by a remote client.
	owned := store.Binding{
		Name: "owned", CWD: "/repo/owned",
		MasterMind: store.Endpoint{PaneID: pane}, MasterMindID: testClaimMasterMind, Owner: "client-1",
		Round: 1, State: store.StateActive,
	}
	if err := s.Save(owned); err != nil {
		t.Fatalf("save owned binding: %v", err)
	}

	queueDrainEntry(t, s, "mine", 1, store.KindReport, "", "payload")
	queueDrainEntry(t, s, "other-mastermind", 1, store.KindReport, "", "payload")
	queueDrainEntry(t, s, "owned", 1, store.KindReport, "", "payload")

	st := &DrainState{MasterMind: testClaimMasterMind}
	pusher := &fakePusher{}
	res, err := Drain(context.Background(), rt, st, pusher)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if res.Pushed != 1 {
		t.Fatalf("Pushed = %d, want 1 (only this mastermind's binding, not owned)", res.Pushed)
	}
	if len(pusher.pushes) != 1 || pusher.pushes[0].meta["binding"] != "mine" {
		t.Fatalf("pushes = %+v, want only 'mine'", pusher.pushes)
	}
}

// TestDrainStateEdges walks the state transitions: a
// transition into needs_you/broken/orphaned pushes once, staying in one
// pushes nothing more, and leaving one pushes nothing.
func TestDrainStateEdges(t *testing.T) {
	t.Parallel()

	s := store.New(t.TempDir())
	rt := Deps{Store: s}
	pane := "w2:p3"

	b := saveDrainBinding(t, s, "judge", pane, store.StateActive)
	st := &DrainState{MasterMind: testClaimMasterMind}
	pusher := &fakePusher{}

	// First sight in "active" (not a channel state): nothing pushed.
	if _, err := Drain(context.Background(), rt, st, pusher); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(pusher.pushes) != 0 {
		t.Fatalf("first sight in active must not push, got %d", len(pusher.pushes))
	}

	// active -> needs_you: pushes once.
	b.State = store.StateNeedsYou
	b.Halt = "builder asked a question"
	if err := s.Save(b); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := Drain(context.Background(), rt, st, pusher); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(pusher.pushes) != 1 {
		t.Fatalf("active->needs_you must push once, got %d", len(pusher.pushes))
	}
	meta := pusher.pushes[0].meta
	if meta["state"] != "needs_you" || meta["old_state"] != "active" || meta["kind"] != "state" || meta["binding"] != "judge" {
		t.Errorf("state push meta = %+v", meta)
	}

	// Staying in needs_you: pushes nothing more.
	if _, err := Drain(context.Background(), rt, st, pusher); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(pusher.pushes) != 1 {
		t.Fatalf("staying in needs_you must not push again, got %d total", len(pusher.pushes))
	}

	// needs_you -> active: pushes nothing.
	b.State = store.StateActive
	b.Halt = ""
	if err := s.Save(b); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := Drain(context.Background(), rt, st, pusher); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(pusher.pushes) != 1 {
		t.Fatalf("needs_you->active must not push, got %d total", len(pusher.pushes))
	}

	// A different binding's first-ever sight already in "broken": pushes once,
	// with an empty old_state.
	saveDrainBinding(t, s, "storefront", pane, store.StateBroken)
	if _, err := Drain(context.Background(), rt, st, pusher); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(pusher.pushes) != 2 {
		t.Fatalf("first sight already broken must push once, got %d total", len(pusher.pushes))
	}
	last := pusher.pushes[len(pusher.pushes)-1]
	if last.meta["binding"] != "storefront" || last.meta["state"] != "broken" || last.meta["old_state"] != "" {
		t.Errorf("broken-at-first-sight meta = %+v", last.meta)
	}
}

func TestDrainDropsGoneBindingsFromMemory(t *testing.T) {
	t.Parallel()

	s := store.New(t.TempDir())
	rt := Deps{Store: s}
	pane := "w2:p3"
	saveDrainBinding(t, s, "judge", pane, store.StateNeedsYou)

	st := &DrainState{MasterMind: testClaimMasterMind}
	pusher := &fakePusher{}
	if _, err := Drain(context.Background(), rt, st, pusher); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if _, ok := st.Last["judge"]; !ok {
		t.Fatalf("Last must remember judge after its first poll")
	}

	if err := s.Delete("judge"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := Drain(context.Background(), rt, st, pusher); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if _, ok := st.Last["judge"]; ok {
		t.Errorf("Last must drop a binding once it disappears from the store")
	}
}
