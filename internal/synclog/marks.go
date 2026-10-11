package synclog

// The floor is the sequence below which an origin's log may be compacted, and
// it is a pure function of the marks the readers published. Nothing here reads
// the log or the remote: a rule that could only be checked against a database
// would be a rule whose mistake is found in production rather than in a test.

import "time"

// markLifetime is how long a reader's mark counts towards a floor.
//
// A reader that has not published inside this window is treated as gone, because
// a floor that waited on it forever would be a floor that never moved. The
// window is generous rather than tight: a laptop that is closed for a fortnight
// is still a reader that expects its history, and only a machine that has not
// spoken for a month is one whose absence a compaction may act on.
const markLifetime = 30 * 24 * time.Hour

// Mark is one reader's published position in one origin's log.
type Mark struct {
	// Reader is the installation the mark belongs to.
	Reader string
	// Origin is the log the position is in.
	Origin string
	// Seq is the highest sequence of that origin the reader has applied.
	Seq int
	// At is when the reader reached that sequence, and what decides whether it
	// still counts towards the floor.
	At time.Time
}

// markSkew is how far in the future a mark may be stamped and still count:
// clock drift between machines, not a mark that would stay live forever.
const markSkew = time.Hour

// FloorFor is the highest sequence of origin's log every live reader has
// applied: the point below which no live reader still needs an entry.
//
// A reader is live when it published inside markLifetime. Only marks for origin
// and from a reader other than origin itself are counted: a machine's mark of
// its own log says nothing about what any other machine needs, and counting it
// would hold the floor at whatever the origin itself has caught up to.
//
// A mark the origin's own log contradicts is not counted: one past head, or
// one stamped further in the future than clocks drift. The marks table is
// written by other machines, and either would hold the floor where no reader
// is -- a stale far-future mark pinned at zero would stop compaction for good.
//
// With no live reader the floor is head, the origin's own highest sequence. A
// machine syncing alone still needs its log compacted, and a reader that turns
// up later -- or comes back after being away past the window -- has a mark
// below the floor, which is what tells it to re-import from the start rather
// than read a log with a hole in it.
func FloorFor(marks []Mark, origin string, head int, now time.Time) int {
	floor, live := head, false
	for _, m := range marks {
		if m.Origin != origin || m.Reader == origin {
			continue
		}
		if now.Sub(m.At) > markLifetime || m.At.Sub(now) > markSkew || m.Seq > head {
			continue
		}
		if !live || m.Seq < floor {
			floor, live = m.Seq, true
		}
	}
	return floor
}
