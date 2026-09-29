package view

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// mapQuestion turns a map keyed "name:round" into a questionOf func for
// WaitingOn's tests.
func mapQuestion(m map[string]string) func(name string, round int) string {
	return func(name string, round int) string {
		return m[fmt.Sprintf("%s:%d", name, round)]
	}
}
func TestWaitingLine(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

	t.Run("blocked with a sub-hour age", func(t *testing.T) {
		w := Waiting{
			Name: "e2e-round", Round: 4, Cause: "blocked",
			Line: "Do you want to proceed? > 1. Yes", Since: now.Add(-23 * time.Minute),
			Hint: "relevo status --name e2e-round",
		}
		want := "waiting on you: e2e-round round 4 blocked 23m -- Do you want to proceed? > 1. Yes  (relevo status --name e2e-round)"
		if got := WaitingLine(w, now); got != want {
			t.Errorf("WaitingLine =\n%q\nwant\n%q", got, want)
		}
	})

	t.Run("halted with an hour-plus age", func(t *testing.T) {
		w := Waiting{
			Name: "spaceapi", Round: 2, Cause: "halted",
			Line:  "builder gone for 30s (agy/google/x); already switched 2 time(s) this round (max_switches 2)",
			Since: now.Add(-(1*time.Hour + 5*time.Minute)),
			Hint:  "relevo status --name spaceapi",
		}
		want := "waiting on you: spaceapi round 2 halted 1h 5m -- builder gone for 30s (agy/google/x); already switched 2 time(s) this round (max_switches 2)  (relevo status --name spaceapi)"
		if got := WaitingLine(w, now); got != want {
			t.Errorf("WaitingLine =\n%q\nwant\n%q", got, want)
		}
	})

	t.Run("orphaned with a zero Since has no age", func(t *testing.T) {
		w := Waiting{
			Name: "old", Round: 3, Cause: "orphaned",
			Line: "mastermind pane is gone", Hint: "relevo bind --resume --name old",
		}
		want := "waiting on you: old round 3 orphaned -- mastermind pane is gone  (relevo bind --resume --name old)"
		if got := WaitingLine(w, now); got != want {
			t.Errorf("WaitingLine =\n%q\nwant\n%q", got, want)
		}
	})
}

const (
	answerHint = "relevo status --name api"
	statusHint = "relevo status --name api"
	rebindHint = "relevo bind --resume --name api --rebind"
	bindHint   = "relevo bind --resume --name api"
)

func TestWaitingOn(t *testing.T) {
	t.Parallel()

	t.Run("done is not waiting", func(t *testing.T) {
		b := store.Binding{Name: "api", Round: 4, State: store.StateDone}
		if _, ok := WaitingOn(b, nil, mapQuestion(nil)); ok {
			t.Error("a done binding must not be waiting")
		}
	})

	t.Run("needs_you with a question entry is blocked", func(t *testing.T) {
		ts := time.Unix(1757000000, 0).UTC()
		b := store.Binding{Name: "api", Round: 4, State: store.StateNeedsYou}
		entries := []store.LogEntry{
			{Round: 4, Direction: store.DirToMasterMind, Kind: store.KindQuestion, TS: ts, Path: "/x/api/004-question.md"},
		}
		q := mapQuestion(map[string]string{"api:4": "Do you want to proceed?"})

		w, ok := WaitingOn(b, entries, q)
		if !ok {
			t.Fatal("want ok")
		}
		if w.Cause != "blocked" || w.Line != "Do you want to proceed?" || !w.Since.Equal(ts) || w.Hint != answerHint {
			t.Errorf("Waiting = %+v", w)
		}
	})

	t.Run("blocked wins over a halt on the same binding", func(t *testing.T) {
		ts := time.Unix(1757000000, 0).UTC()
		b := store.Binding{
			Name: "api", Round: 4, State: store.StateNeedsYou,
			Halt: "round 4 has run past 2h0m0s", HaltAt: ts.Add(time.Hour),
		}
		entries := []store.LogEntry{
			{Round: 4, Direction: store.DirToMasterMind, Kind: store.KindQuestion, TS: ts, Path: "/x/api/004-question.md"},
		}
		q := mapQuestion(map[string]string{"api:4": "Do you want to proceed?"})

		w, ok := WaitingOn(b, entries, q)
		if !ok {
			t.Fatal("want ok")
		}
		if w.Cause != "blocked" {
			t.Errorf("Cause = %q, want blocked: blocked must be checked before halted", w.Cause)
		}
	})

}

func TestWaitingOnHalts(t *testing.T) {
	t.Parallel()

	t.Run("needs_you with only a halt is halted", func(t *testing.T) {
		ts := time.Unix(1757000000, 0).UTC()
		b := store.Binding{
			Name: "api", Round: 4, State: store.StateNeedsYou,
			Halt: "round 4 has run past 2h0m0s", HaltAt: ts,
		}

		w, ok := WaitingOn(b, nil, mapQuestion(nil))
		if !ok {
			t.Fatal("want ok")
		}
		if w.Cause != "halted" || w.Line != "round 4 has run past 2h0m0s" || !w.Since.Equal(ts) || w.Hint != statusHint {
			t.Errorf("Waiting = %+v", w)
		}
	})

	t.Run("needs_you with neither is honest", func(t *testing.T) {
		b := store.Binding{Name: "api", Round: 4, State: store.StateNeedsYou}

		w, ok := WaitingOn(b, nil, mapQuestion(nil))
		if !ok {
			t.Fatal("want ok")
		}
		if w.Cause != "needs you" ||
			w.Line != "no reason recorded (binding predates the halt record)" ||
			!w.Since.IsZero() || w.Hint != statusHint {
			t.Errorf("Waiting = %+v", w)
		}
	})

}

func TestWaitingOnBrokenAndCaps(t *testing.T) {
	t.Parallel()

	t.Run("broken and switchable is not yet a human's problem", func(t *testing.T) {
		b := store.Binding{
			Name: "api", Round: 4, State: store.StateBroken,
			BuilderCandidate: "agy/x/y", RoundStartedAt: time.Unix(1757000000, 0),
		}
		if _, ok := WaitingOn(b, nil, mapQuestion(nil)); ok {
			t.Error("a switchable broken binding must not be waiting")
		}
	})

	t.Run("a long dialog line is capped at 120 runes", func(t *testing.T) {
		ts := time.Unix(1757000000, 0).UTC()
		b := store.Binding{Name: "api", Round: 4, State: store.StateNeedsYou}
		entries := []store.LogEntry{
			{Round: 4, Direction: store.DirToMasterMind, Kind: store.KindQuestion, TS: ts, Path: "/x/api/004-question.md"},
		}
		q := mapQuestion(map[string]string{"api:4": strings.Repeat("x", 200)})

		w, ok := WaitingOn(b, entries, q)
		if !ok {
			t.Fatal("want ok")
		}
		r := []rune(w.Line)
		if len(r) != 120 || r[119] != '…' {
			t.Errorf("Line = %q (%d runes), want 120 runes ending in …", w.Line, len(r))
		}
	})

	t.Run("a blank first line falls through to the second", func(t *testing.T) {
		ts := time.Unix(1757000000, 0).UTC()
		b := store.Binding{Name: "api", Round: 4, State: store.StateNeedsYou}
		entries := []store.LogEntry{
			{Round: 4, Direction: store.DirToMasterMind, Kind: store.KindQuestion, TS: ts, Path: "/x/api/004-question.md"},
		}
		q := mapQuestion(map[string]string{"api:4": "\n\n  second  \n"})

		w, ok := WaitingOn(b, entries, q)
		if !ok {
			t.Fatal("want ok")
		}
		if w.Line != "second" {
			t.Errorf("Line = %q, want %q", w.Line, "second")
		}
	})

	t.Run("an unreadable question file falls back to the captured path", func(t *testing.T) {
		ts := time.Unix(1757000000, 0).UTC()
		b := store.Binding{Name: "api", Round: 4, State: store.StateNeedsYou}
		entries := []store.LogEntry{
			{Round: 4, Direction: store.DirToMasterMind, Kind: store.KindQuestion, TS: ts, Path: "/x/api/001-question.md"},
		}

		w, ok := WaitingOn(b, entries, mapQuestion(nil))
		if !ok {
			t.Fatal("want ok")
		}
		want := "dialog captured at /x/api/001-question.md"
		if w.Line != want {
			t.Errorf("Line = %q, want %q", w.Line, want)
		}
	})
}
