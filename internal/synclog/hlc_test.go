package synclog

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// memKV is a kv that holds its rows in a map, so a clock can be handed one in a
// test that is about the clock rather than about a database.
type memKV struct{ rows map[string][]byte }

func newMemKV() *memKV { return &memKV{rows: map[string][]byte{}} }

func (m *memKV) KVGet(key string) ([]byte, bool, error) {
	value, ok := m.rows[key]
	return value, ok, nil
}

func (m *memKV) KVPut(key string, value []byte) error {
	m.rows[key] = value
	return nil
}

func (m *memKV) KVDelete(key string) error {
	delete(m.rows, key)
	return nil
}

// TestHybridClockNeverRunsBackwards pins the clock's only promise: two calls
// hand out two stamps, and the second is above the first whatever the wall
// clock does. The wall clock is stepped back between the calls, which is not a
// trick -- a machine whose clock is corrected, or restored from a backup, sees
// the same thing, and stamps that went backwards would let a write this
// machine made later lose to one it made earlier.
func TestHybridClockNeverRunsBackwards(t *testing.T) {
	store := newMemKV()
	clock := NewHybridClock(store)

	wall := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	clock.now = func() time.Time { return wall }

	first, err := clock.Now()
	if err != nil {
		t.Fatalf("Now: %v", err)
	}

	// The wall clock steps back a full minute. Every stamp after it must still
	// be above the one before.
	wall = wall.Add(-time.Minute)
	second, err := clock.Now()
	if err != nil {
		t.Fatalf("Now after the clock stepped back: %v", err)
	}
	if second <= first {
		t.Errorf("stamps %d then %d; the second must be above the first", first, second)
	}

	// Time standing still is the other half: two stamps inside one millisecond
	// are separated by the counter, not by the clock.
	for i := 0; i < 8; i++ {
		next, err := clock.Now()
		if err != nil {
			t.Fatalf("Now: %v", err)
		}
		if next <= second {
			t.Fatalf("stamp %d did not advance past %d", next, second)
		}
		second = next
	}
}

// TestHybridClockObserveMovesPast pins that a stamp seen from elsewhere pushes
// this machine's own stamps above it. Without that, a write this machine makes
// now would carry a stamp another machine's later write already beats, and the
// two would resolve the wrong way round.
func TestHybridClockObserveMovesPast(t *testing.T) {
	store := newMemKV()
	clock := NewHybridClock(store)

	wall := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	clock.now = func() time.Time { return wall }

	// A stamp from a machine whose wall clock reads an hour ahead, so nothing
	// about this machine's own clock could produce it.
	seen := stampAt(wall.Add(time.Hour), 0)
	if err := clock.Observe(seen); err != nil {
		t.Fatalf("Observe: %v", err)
	}

	next, err := clock.Now()
	if err != nil {
		t.Fatalf("Now: %v", err)
	}
	if next <= seen {
		t.Errorf("Now gave %d after observing %d; the next stamp must be past it", next, seen)
	}
}

// TestHybridClockObserveBelowCurrentChangesNothing pins that observing a stamp
// this clock already covers does not push it forward. Every re-delivered entry
// is observed, and a clock that jumped to each one would leave its stamps
// ordered by nothing at all.
func TestHybridClockObserveBelowCurrentChangesNothing(t *testing.T) {
	store := newMemKV()
	clock := NewHybridClock(store)
	clock.now = func() time.Time { return time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC) }

	issued, err := clock.Now()
	if err != nil {
		t.Fatalf("Now: %v", err)
	}
	if err := clock.Observe(Stamp(1)); err != nil {
		t.Fatalf("Observe: %v", err)
	}

	next, err := clock.Now()
	if err != nil {
		t.Fatalf("Now: %v", err)
	}
	if next != issued+1 {
		t.Errorf("stamps after observing an older one: %d then %d, want %d then %d",
			issued, next, issued, issued+1)
	}
}

// TestHybridClockSurvivesRestart pins that the position outlives the process
// that reached it. A clock that restarted at zero would hand out a stamp the
// machine had already used, and the two would be indistinguishable to every
// reader that compares stamps.
func TestHybridClockSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	wall := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

	before, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	first := NewHybridClock(before.LocalOrSelf())
	first.now = func() time.Time { return wall }
	issued, err := first.Now()
	if err != nil {
		t.Fatalf("Now: %v", err)
	}
	if err := before.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	after, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open again: %v", err)
	}
	t.Cleanup(func() { _ = after.Close() })

	second := NewHybridClock(after.LocalOrSelf())
	// The wall clock has gone backwards across the restart, which is exactly
	// the case the stored position exists to cover.
	second.now = func() time.Time { return wall.Add(-time.Hour) }

	resumed, err := second.Now()
	if err != nil {
		t.Fatalf("Now after restart: %v", err)
	}
	if resumed <= issued {
		t.Errorf("stamp after restart %d, before restart %d; the clock resumed below where it stopped",
			resumed, issued)
	}
}

// TestHybridClockRefusesAStoredPositionItCannotRead pins that a kv row this
// build cannot read as a stamp is a refusal rather than a zero. Reading it as
// zero would start the clock at the beginning and reissue every stamp the row
// was holding the place of. The two shapes are both here because they are
// refused by different steps: one is not the JSON the row is defined as, the
// other is JSON of the wrong shape inside it.
func TestHybridClockRefusesAStoredPositionItCannotRead(t *testing.T) {
	for _, stored := range []string{`"not-a-stamp"`, `-1`, `01`, `7`, `{"stamp":"7"}`} {
		store := newMemKV()
		if err := store.KVPut(hybridClockKey, []byte(stored)); err != nil {
			t.Fatalf("KVPut: %v", err)
		}

		clock := NewHybridClock(store)
		if _, err := clock.Now(); err == nil {
			t.Errorf("Now accepted the stored position %s, want a refusal", stored)
		}
	}
}

// TestHybridClockKeepsGoingWhenTheKvIsGone pins the clock's failure mode: it
// refuses rather than handing out a stamp it cannot record. A stamp issued and
// not stored would be reissued after a restart.
func TestHybridClockKeepsGoingWhenTheKvIsGone(t *testing.T) {
	store := newMemKV()
	clock := NewHybridClock(store)
	if _, err := clock.Now(); err != nil {
		t.Fatalf("Now: %v", err)
	}

	broken := &brokenKV{inner: store}
	clock.store = broken
	clock.loaded = false

	if _, err := clock.Now(); err == nil {
		t.Error("Now issued a stamp while the position could not be stored, want a refusal")
	}
}

// brokenKV reads a row and fails to write one.
type brokenKV struct{ inner *memKV }

func (b *brokenKV) KVGet(key string) ([]byte, bool, error) { return b.inner.KVGet(key) }

func (b *brokenKV) KVPut(string, []byte) error { return errNoStore }

func (b *brokenKV) KVDelete(key string) error { return b.inner.KVDelete(key) }

// errNoStore is the write failure the broken kv answers with.
var errNoStore = errors.New("the kv is not writable")
