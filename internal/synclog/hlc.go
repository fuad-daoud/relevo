package synclog

import (
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// hybridClockKey is the local kv row the clock's position is kept in. A wall
// clock alone orders nothing across machines, so a stamp has to carry this
// machine's own count of the stamps it has already issued, and that count has
// to outlive the process -- a clock that restarted at zero would hand out a
// stamp another machine had already used.
const hybridClockKey = "sync.hybrid_clock.v1"

// stampCounterBits is the width of the counter held in a stamp's low bits.
// Sixteen is enough that a machine never exhausts it within one millisecond,
// while leaving the wall clock the 47 bits it needs to run past the year 6000.
const stampCounterBits = 16

// stampCounterMask selects a stamp's counter from its low bits.
const stampCounterMask = Stamp(1<<stampCounterBits - 1)

// wallBits clears a stamp's counter, leaving the milliseconds it was issued in.
const wallBits = ^stampCounterMask

// Stamp is one position in this machine's 64-bit hybrid clock: the wall-clock
// milliseconds it was issued in, shifted up, with a counter of how many stamps
// the same millisecond has already produced.
//
// The two halves answer different questions. The wall half keeps stamps
// readable against a clock a person can check, and orders this machine's work
// against its own past. The counter keeps two stamps issued inside one
// millisecond apart, which no amount of clock resolution would do.
type Stamp uint64

// String renders the stamp as the decimal text a doc body carries.
func (s Stamp) String() string { return strconv.FormatUint(uint64(s), 10) }

// stampAt packs a wall time and a counter into one stamp.
func stampAt(at time.Time, counter uint64) Stamp {
	return Stamp(uint64(at.UTC().UnixMilli())<<stampCounterBits | (counter & uint64(stampCounterMask)))
}

// ParseStamp reads the decimal text a doc body carries. A stamp reaches this
// as bytes another machine wrote, so the text has to be exactly one unsigned
// decimal number: a leading sign, a leading zero, whitespace or a base prefix
// would all parse into a number whose text is not the number, and the same key
// could then be stamped two ways.
func ParseStamp(text string) (Stamp, error) {
	value, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("synclog: stamp %q is not a decimal number: %w: %w", text, err, ErrInvalid)
	}
	if text != strconv.FormatUint(value, 10) {
		return 0, fmt.Errorf("synclog: stamp %q is not in canonical decimal form: %w", text, ErrInvalid)
	}
	return Stamp(value), nil
}

// HybridClock is the source of the stamps doc entries are written from. It is
// backed by the local kv rather than held only in memory, so the position a
// machine reached survives the process that reached it: a restart reads back
// the stamp it last issued and continues above it, and two machines that have
// never met never compare equal.
//
// The clock never goes backwards, whatever the wall clock does. Observe moves
// it past a stamp another machine issued, so the next stamp this one hands out
// loses to nothing it has already seen.
type HybridClock struct {
	// store is the local kv the position is kept in, and key the row it is
	// kept under.
	store db.KV
	key   string
	// now reads the wall clock. It is a field so a test can hold time still.
	now func() time.Time

	mu     sync.Mutex
	last   Stamp
	loaded bool
}

// NewHybridClock binds a clock to the kv it keeps its position in. The store is
// the machine-local file's: the position is about what this machine has
// stamped, so it must not travel.
func NewHybridClock(store db.KV) *HybridClock {
	return &HybridClock{store: store, key: hybridClockKey, now: time.Now}
}

// Now returns the next stamp, which is strictly greater than every stamp this
// clock has issued or observed.
func (c *HybridClock) Now() (Stamp, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.load(); err != nil {
		return 0, err
	}
	next := c.next(c.last)
	if err := c.persist(next); err != nil {
		return 0, err
	}
	c.last = next
	return next, nil
}

// Observe moves the clock past stamp, so a stamp this machine has seen from
// elsewhere is never issued here. A stamp already covered changes nothing: the
// clock does not go forwards to reach a position it already holds, which is
// what keeps a re-delivered entry from pushing this machine's own stamps up.
func (c *HybridClock) Observe(stamp Stamp) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.load(); err != nil {
		return err
	}
	if stamp <= c.last {
		return nil
	}
	if err := c.persist(stamp); err != nil {
		return err
	}
	c.last = stamp
	return nil
}

// load reads the stored position once. Until it has, this clock has issued
// nothing, so the zero stamp is a position and not a guess at one.
func (c *HybridClock) load() error {
	if c.loaded {
		return nil
	}
	raw, ok, err := c.store.KVGet(c.key)
	if err != nil {
		return fmt.Errorf("synclog: hybrid clock: %w", err)
	}
	if ok {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			// A row a hand-edited file put here is not a position to resume
			// from, and reading it as zero would reissue stamps it already
			// spent. It is refused rather than repaired, so the file is looked
			// at rather than quietly worked around.
			return fmt.Errorf("synclog: hybrid clock: stored position is not a stamp: %w: %w", err, ErrInvalid)
		}
		stamp, err := ParseStamp(text)
		if err != nil {
			return err
		}
		c.last = stamp
	}
	c.loaded = true
	return nil
}

// next is the first stamp above from. A wall clock that has moved on starts a
// fresh count from zero in the new millisecond; a wall clock still inside the
// millisecond from was issued in, and a wall clock that has gone backwards, both
// count on from from instead, because the point of a stamp is that it orders
// this machine's writes and following a backwards clock would reorder them. The
// cost of that is a stamp whose wall half names a moment the clock has not
// reached, which is the trade a hybrid clock makes for never regressing.
//
// Counting past the counter's top bit rolls into the millisecond half, which is
// what keeps the arithmetic correct when more than 65536 stamps are issued
// inside one millisecond rather than wrapping back to the start of it.
func (c *HybridClock) next(from Stamp) Stamp {
	now := stampAt(c.now(), 0)
	if now&wallBits > from&wallBits {
		return now
	}
	return from + 1
}

// persist writes the position so the next process starts above it.
func (c *HybridClock) persist(stamp Stamp) error {
	value, err := json.Marshal(stamp.String())
	if err != nil {
		return fmt.Errorf("synclog: hybrid clock: encode %s: %w", stamp, err)
	}
	if err := c.store.KVPut(c.key, value); err != nil {
		return fmt.Errorf("synclog: hybrid clock: store %s: %w", stamp, err)
	}
	return nil
}
