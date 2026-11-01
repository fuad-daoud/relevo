package synclog

import (
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// hybridClockKey is the local kv row the clock's position is kept in. A wall clock
// orders nothing across machines, so a stamp carries this machine's issued count,
// persisted past restarts that would otherwise reissue a used stamp.
const hybridClockKey = "sync.hybrid_clock.v1"

// stampCounterBits is the width of the stamp's low-bit counter: 16 never exhausts
// in a millisecond while leaving the wall clock 47 bits past the year 6000.
const stampCounterBits = 16

// stampCounterMask selects a stamp's counter from its low bits.
const stampCounterMask = Stamp(1<<stampCounterBits - 1)

// wallBits clears a stamp's counter, leaving its issue milliseconds.
const wallBits = ^stampCounterMask

// Stamp is one position in this machine's 64-bit hybrid clock: issue
// milliseconds shifted up plus a same-millisecond counter. The wall half orders
// against a checkable clock and this machine's past; the counter separates two
// stamps in one millisecond.
type Stamp uint64

// String renders the stamp as the decimal text a doc body carries.
func (s Stamp) String() string { return strconv.FormatUint(uint64(s), 10) }

// stampAt packs a wall time and a counter into one stamp.
func stampAt(at time.Time, counter uint64) Stamp {
	return Stamp(uint64(at.UTC().UnixMilli())<<stampCounterBits | (counter & uint64(stampCounterMask)))
}

// ParseStamp reads the decimal text a doc body carries. The bytes came from
// another machine, so the text must be exactly one canonical unsigned decimal:
// sign, leading zero, whitespace or prefix would stamp one key two ways.
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

// HybridClock is the source of the stamps doc entries are written from, backed by
// local kv so the reached position survives restarts that continue above it. The
// clock never goes backwards: Observe moves it past another machine's stamp, so the
// next stamp handed out loses to nothing seen.
type HybridClock struct {
	// store is the local kv the position is kept in, and key its row.
	store db.KV
	key   string
	// now reads the wall clock; a field so a test can hold time still.
	now func() time.Time

	mu     sync.Mutex
	last   Stamp
	loaded bool
}

// NewHybridClock binds a clock to the kv it keeps its position in. The store is
// the machine-local file's and must not travel: the position is what this machine
// stamped.
func NewHybridClock(store db.KV) *HybridClock {
	return &HybridClock{store: store, key: hybridClockKey, now: time.Now}
}

// NewHybridClockAt is NewHybridClock with the wall clock read from now.
func NewHybridClockAt(store db.KV, now func() time.Time) *HybridClock {
	c := NewHybridClock(store)
	c.now = now
	return c
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

// Observe moves the clock past stamp, so a seen stamp is never issued here. A
// covered stamp changes nothing, keeping a redelivery from pushing stamps up.
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

// load reads the stored position once; until then the zero stamp is a position,
// not a guess.
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
			// A hand-edited row is no position to resume from, and zero would
			// reissue spent stamps. It refuses rather than repairs, for looking at.
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

// next is the first stamp above from. A moved-on wall clock starts fresh in the new
// millisecond; a same-millisecond or regressed one counts on from from, since stamps
// order this machine's writes and a backwards clock would reorder them, at the cost
// of a wall half the clock has not reached. Counting past the counter top rolls into
// the millisecond half, correct past 65536 stamps in one millisecond.
func (c *HybridClock) next(from Stamp) Stamp {
	now := stampAt(c.now(), 0)
	if now&wallBits > from&wallBits {
		return now
	}
	return from + 1
}

// persist writes the position for the next process to start above.
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
