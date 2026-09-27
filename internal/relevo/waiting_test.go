package relevo

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

func TestQuestionFirstLine(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	if err := rt.Store.Save(store.Binding{Name: "api", CWD: "/repo"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	path := rt.Store.QuestionPath("api", 1)
	if err := os.WriteFile(path, []byte("\nDo you want to proceed?\nmore text\n"), 0o644); err != nil {
		t.Fatalf("write question: %v", err)
	}

	qf := questionFirstLine(rt)
	if got := qf("api", 1); got != "Do you want to proceed?" {
		t.Errorf("questionFirstLine = %q, want %q", got, "Do you want to proceed?")
	}
	if got := qf("api", 2); got != "" {
		t.Errorf("questionFirstLine for a missing file = %q, want empty", got)
	}
}
func TestWaitingOnYou(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)

	a := store.Binding{Name: "a", CWD: "/repo/a", Round: 1, State: store.StateNeedsYou}
	if err := rt.Store.Save(a); err != nil {
		t.Fatalf("Save a: %v", err)
	}
	qPath := rt.Store.QuestionPath("a", 1)
	if err := os.WriteFile(qPath, []byte("Do you want to proceed?"), 0o644); err != nil {
		t.Fatalf("write question: %v", err)
	}
	if err := rt.Store.AppendLog("a", store.LogEntry{
		TS: rt.Now().UTC(), Round: 1, Direction: store.DirToMasterMind, Kind: store.KindQuestion,
		Path: qPath, Payload: "Builder is blocked at a dialog.",
	}); err != nil {
		t.Fatalf("AppendLog a: %v", err)
	}

	b := store.Binding{Name: "b", CWD: "/repo/b", Round: 1, State: store.StateActive}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save b: %v", err)
	}

	c := store.Binding{Name: "c", CWD: "/repo/c", Round: 1, State: store.StateDone}
	if err := rt.Store.Save(c); err != nil {
		t.Fatalf("Save c: %v", err)
	}

	d := store.Binding{
		Name: "d", CWD: "/repo/d", Round: 2, State: store.StateNeedsYou,
		Halt: "round 2 has run past 2h0m0s", HaltAt: rt.Now().UTC(),
	}
	if err := rt.Store.Save(d); err != nil {
		t.Fatalf("Save d: %v", err)
	}

	lines, err := WaitingOnYou(rt, "d")
	if err != nil {
		t.Fatalf("WaitingOnYou: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("lines = %v, want exactly 1", lines)
	}
	if !strings.HasPrefix(lines[0], "waiting on you: a round ") {
		t.Errorf("line = %q, want prefix %q", lines[0], "waiting on you: a round ")
	}
	if !strings.Contains(lines[0], "Do you want to proceed?") {
		t.Errorf("line = %q, want it to contain the question", lines[0])
	}
	if !strings.Contains(lines[0], "(relevo status --name a)") {
		t.Errorf("line = %q, want the status hint", lines[0])
	}

	all, err := WaitingOnYou(rt, "")
	if err != nil {
		t.Fatalf("WaitingOnYou: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("lines = %v, want exactly 2", all)
	}
	if !strings.HasPrefix(all[0], "waiting on you: a round ") || !strings.HasPrefix(all[1], "waiting on you: d round ") {
		t.Errorf("lines = %v, want a then d (List order)", all)
	}
}

// mapQuestion turns a map keyed "name:round" into a questionOf func for
// view.WaitingOn's tests.
func mapQuestion(m map[string]string) func(name string, round int) string {
	return func(name string, round int) string {
		return m[fmt.Sprintf("%s:%d", name, round)]
	}
}
