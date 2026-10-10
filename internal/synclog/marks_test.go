package synclog

// The floor rule as a pure function, tested without a log or a remote: a
// mistake here is what drops entries a reader still needs, and it cannot be
// found by reading the answer back.

import (
	"testing"
	"time"
)

// markNow is the moment every case is measured from, so a case states an age or
// a publish time rather than a clock reading. The fake's marks cases share it:
// what a publish wrote and what the floor rule reads are the same marks.
var markNow = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

// aged is a mark taken days ago, negative for one in the future.
func aged(reader, origin string, seq int, days int) Mark {
	return Mark{
		Reader: reader,
		Origin: origin,
		Seq:    seq,
		At:     markNow.Add(-time.Duration(days) * 24 * time.Hour),
	}
}

// TestFloorIsMinimumOverLiveReaders pins the floor as the slowest live reader's
// mark: the log may keep everything every live machine has, and dropping past
// the slowest one would take entries it still has to read.
func TestFloorIsMinimumOverLiveReaders(t *testing.T) {
	t.Parallel()
	// A reader past the lifetime is among them deliberately: it is the one a
	// minimum over live readers must not count, and a case with only live
	// readers would pass the same whether or not the filter existed.
	marks := []Mark{
		aged("m1", "origin-a", 40, 1),
		aged("m2", "origin-a", 12, 2),
		aged("m3", "origin-a", 30, 3),
		aged("m4", "origin-a", 2, 45),
	}
	if got := FloorFor(marks, "origin-a", 100, markNow); got != 12 {
		t.Errorf("FloorFor = %d, want the slowest live reader's mark 12: "+
			"the reader 45 days old is not live", got)
	}
}

// TestFloorIgnoresTheOriginsOwnMark pins that a machine's mark of its own log
// says nothing about what other machines need. Counting it would hold the floor
// at wherever the origin itself has got to, which is ahead of every reader.
func TestFloorIgnoresTheOriginsOwnMark(t *testing.T) {
	t.Parallel()
	marks := []Mark{
		aged("origin-a", "origin-a", 90, 1),
		aged("m2", "origin-a", 25, 1),
	}
	if got := FloorFor(marks, "origin-a", 100, markNow); got != 25 {
		t.Errorf("FloorFor = %d, want the other reader's mark 25, not its own 90", got)
	}

	// A log with no other reader at all is the same case: its own mark is the
	// only one there is, and it must not become the floor.
	own := []Mark{aged("origin-a", "origin-a", 90, 1)}
	if got := FloorFor(own, "origin-a", 100, markNow); got != 100 {
		t.Errorf("FloorFor with only the origin's own mark = %d, want head 100", got)
	}
}

// TestFloorWithNoLiveReaderIsHead pins the floor as the origin's own head when
// no reader is live. A machine syncing alone still needs its log compacted, and
// a reader that turns up afterwards has a mark below the floor, which is what
// tells it to re-import rather than read a log with a hole in it.
func TestFloorWithNoLiveReaderIsHead(t *testing.T) {
	t.Parallel()
	// Every mark here is past the lifetime, so none of them is live. The origin
	// is its own reader on one of them, which must not make it live either.
	marks := []Mark{
		aged("m1", "origin-a", 5, 40),
		aged("m2", "origin-a", 8, 31),
		aged("origin-a", "origin-a", 90, 1),
	}
	if got := FloorFor(marks, "origin-a", 100, markNow); got != 100 {
		t.Errorf("FloorFor = %d, want head 100 with no live reader", got)
	}

	// No marks at all is the same case, and is what a log nobody has read yet
	// looks like.
	if got := FloorFor(nil, "origin-a", 7, markNow); got != 7 {
		t.Errorf("FloorFor with no marks = %d, want head 7", got)
	}
}

// TestFloorIgnoresAnotherOriginsMarks pins that the floor is one origin's: a
// reader that is far behind on one origin says nothing about another's, and
// letting it in would drop entries an origin whose readers are all caught up
// has no need to lose.
func TestFloorIgnoresAnotherOriginsMarks(t *testing.T) {
	t.Parallel()
	marks := []Mark{
		aged("m1", "origin-a", 60, 1),
		aged("m1", "origin-b", 2, 1),
	}
	if got := FloorFor(marks, "origin-a", 100, markNow); got != 60 {
		t.Errorf("FloorFor for origin-a = %d, want 60: the other origin's mark is not this one's", got)
	}
}

// TestFloorCountsAMarkAtTheEdgeOfItsLifetime pins the boundary itself. A reader
// exactly markLifetime old still counts, because the window is what a reader
// inside it is trusted to be, and a reader that went quiet a moment early would
// otherwise have its log compacted under it.
func TestFloorCountsAMarkAtTheEdgeOfItsLifetime(t *testing.T) {
	t.Parallel()
	atEdge := Mark{Reader: "m1", Origin: "origin-a", Seq: 20, At: markNow.Add(-markLifetime)}
	if got := FloorFor([]Mark{atEdge}, "origin-a", 100, markNow); got != 20 {
		t.Errorf("FloorFor at the edge of the lifetime = %d, want the live reader's mark 20", got)
	}

	// A moment past the edge and the same reader is gone, which is what makes
	// the window a rule rather than a floor that never moves.
	past := Mark{Reader: "m1", Origin: "origin-a", Seq: 20, At: markNow.Add(-markLifetime - time.Second)}
	if got := FloorFor([]Mark{past}, "origin-a", 100, markNow); got != 100 {
		t.Errorf("FloorFor past the edge = %d, want head 100", got)
	}
}

// A mark stamped far in the future would stay live forever, and one with a seq
// of 0 would then pin the floor for good. Clock drift is allowed; a mark past
// it is not counted.
func TestFloorIgnoresAMarkFromTheFuture(t *testing.T) {
	t.Parallel()
	marks := []Mark{aged("m1", "origin-a", 30, 1), aged("m2", "origin-a", 0, -400)}
	if got := FloorFor(marks, "origin-a", 50, markNow); got != 30 {
		t.Fatalf("floor = %d, want 30 with the future mark ignored", got)
	}
}

// A mark past the origin's head claims entries the log never held, so it says
// nothing about what a reader needs.
func TestFloorIgnoresAMarkPastTheHead(t *testing.T) {
	t.Parallel()
	marks := []Mark{aged("m1", "origin-a", 900, 1)}
	if got := FloorFor(marks, "origin-a", 50, markNow); got != 50 {
		t.Fatalf("floor = %d, want the head 50 with the impossible mark ignored", got)
	}
}
