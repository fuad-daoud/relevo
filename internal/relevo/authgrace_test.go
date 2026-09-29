package relevo

import (
	"testing"
	"time"
)

// TestAuthGraceNoteKeepsTheFirstTime pins that a grace starts at the first
// sight and every later Note answers with that same instant, which is what
// makes the halt text's duration honest.
func TestAuthGraceNoteKeepsTheFirstTime(t *testing.T) {
	t.Parallel()

	g := NewAuthGrace()
	first := g.Note("api", baseTime)
	if !first.Equal(baseTime) {
		t.Fatalf("first Note = %v, want %v", first, baseTime)
	}

	later := g.Note("api", baseTime.Add(time.Minute))
	if !later.Equal(baseTime) {
		t.Fatalf("second Note = %v, want the first sight %v", later, baseTime)
	}

	// Another binding has its own clock.
	other := g.Note("web", baseTime.Add(2*time.Minute))
	if !other.Equal(baseTime.Add(2 * time.Minute)) {
		t.Fatalf("Note(web) = %v, want its own first sight", other)
	}
}

// TestAuthGraceExpiredAtTheLimit pins the boundary: a grace expires exactly at
// the limit, not before.
func TestAuthGraceExpiredAtTheLimit(t *testing.T) {
	t.Parallel()

	g := NewAuthGrace()
	g.Note("api", baseTime)

	if g.Expired("api", baseTime.Add(authGraceLimit-time.Second), authGraceLimit) {
		t.Fatal("expired 1s before the limit")
	}
	if !g.Expired("api", baseTime.Add(authGraceLimit), authGraceLimit) {
		t.Fatal("not expired at the limit")
	}
	if !g.Expired("api", baseTime.Add(16*time.Minute), authGraceLimit) {
		t.Fatal("not expired 16m in")
	}
	// A binding never seen has no grace at all.
	if g.Expired("web", baseTime.Add(time.Hour), authGraceLimit) {
		t.Fatal("a name never noted reported expired")
	}
}

// TestAuthGraceClearRestartsTheClock pins that a success between two transient
// errors resets the grace, so the second one starts its own 15 minutes.
func TestAuthGraceClearRestartsTheClock(t *testing.T) {
	t.Parallel()

	g := NewAuthGrace()
	g.Note("api", baseTime)
	g.Clear("api")

	if g.Expired("api", baseTime.Add(time.Hour), authGraceLimit) {
		t.Fatal("a cleared name is still reported expired")
	}

	// A later error is a fresh sight, not the old one.
	again := g.Note("api", baseTime.Add(time.Hour))
	if !again.Equal(baseTime.Add(time.Hour)) {
		t.Fatalf("Note after Clear = %v, want the new sight", again)
	}
	if g.Expired("api", baseTime.Add(time.Hour), authGraceLimit) {
		t.Fatal("expired immediately after the new sight")
	}
}

// TestAuthGraceNilIsSafe pins §3's nil case: a CLI one-shot carries no grace,
// so it never expires -- and none of the methods panic.
func TestAuthGraceNilIsSafe(t *testing.T) {
	t.Parallel()

	var g *AuthGrace
	first := g.Note("api", baseTime)
	if !first.Equal(baseTime) {
		t.Fatalf("nil Note = %v, want now", first)
	}
	if g.Expired("api", baseTime.Add(24*time.Hour), authGraceLimit) {
		t.Fatal("a nil AuthGrace reported expired")
	}
	g.Clear("api")
}
