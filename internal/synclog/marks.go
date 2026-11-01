package synclog

// The floor below which an origin's log may compact is a pure function of
// published marks, checkable without a database.

import "time"

// markLifetime is how long a reader's mark counts towards a floor. A reader
// silent past it counts as gone, or the floor would never move; the window is
// generous, so a fortnight-closed laptop still counts.
const markLifetime = 30 * 24 * time.Hour

// Mark is one reader's published position in one origin's log.
type Mark struct {
	Reader string
	Origin string
	Seq    int
	// At is when the reader reached that sequence, deciding whether it still
	// counts.
	At time.Time
}

// markSkew is how far future-stamped a mark may be and still count: clock drift,
// not a forever-live mark.
const markSkew = time.Hour

// FloorFor is the highest sequence of origin's log every live reader has applied.
// Live means published inside markLifetime. A machine's mark of its own log is
// uncounted: it says nothing about other machines' needs. A mark past head or
// further future than drift is uncounted: either would pin the floor where no
// reader is. With no live reader the floor is head, so a lone machine still
// compacts and a returning reader re-imports from the start.
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
