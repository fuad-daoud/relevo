package delivery

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// memWaitAlives reports every pid as live, so a registration's fake pid does
// not depend on which pids exist on the test machine.
func memWaitAlives(int) bool { return true }

// testWaits returns a KVWaitClaims over a fresh temp database, treating every
// pid as alive.
func testWaits(t *testing.T) *KVWaitClaims {
	t.Helper()
	return &KVWaitClaims{KV: db.TxKV{DB: testSecretDB(t)}, Alive: memWaitAlives}
}

func testNow() time.Time { return baseTime }

// TestKVWaitClaimsRoundTrip pins that a registration survives a write and a
// live read, and that Remove takes it away again.
func TestKVWaitClaimsRoundTrip(t *testing.T) {
	t.Parallel()

	w := testWaits(t)
	now := testNow()
	c := WaitClaim{Name: "webshop", PID: 4242, StartedAt: now, SeenAt: now}

	if err := w.Write(c, now); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := w.Live("webshop", now)
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if got == nil {
		t.Fatal("Live = nil, want the registration just written")
	}
	if got.PID != 4242 {
		t.Errorf("PID = %d, want 4242", got.PID)
	}

	if err := w.Remove("webshop", 4242); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	got, err = w.Live("webshop", now)
	if err != nil {
		t.Fatalf("Live after Remove: %v", err)
	}
	if got != nil {
		t.Errorf("Live after Remove = %+v, want nil", got)
	}
}

// TestKVWaitClaimsAbsentBinding pins that a binding nobody waits on reads as
// not live, rather than erroring or reporting a zero registration.
func TestKVWaitClaimsAbsentBinding(t *testing.T) {
	t.Parallel()

	w := testWaits(t)
	got, err := w.Live("nobody", testNow())
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if got != nil {
		t.Errorf("Live = %+v, want nil for a binding nobody waits on", got)
	}
}

// TestKVWaitClaimsAgesOut pins the TTL half of liveness: a registration whose
// refresh is older than WaitTTL is dead even though its pid is very much
// alive. A wait that hung without releasing must not read as collecting.
func TestKVWaitClaimsAgesOut(t *testing.T) {
	t.Parallel()

	w := testWaits(t)
	start := testNow()
	if err := w.Write(WaitClaim{Name: "webshop", PID: 4242, StartedAt: start, SeenAt: start}, start); err != nil {
		t.Fatalf("Write: %v", err)
	}

	inside := start.Add(WaitTTL - time.Second)
	got, err := w.Live("webshop", inside)
	if err != nil {
		t.Fatalf("Live inside the TTL: %v", err)
	}
	if got == nil {
		t.Fatal("Live inside the TTL = nil, want the refreshed registration")
	}

	outside := start.Add(WaitTTL + time.Second)
	got, err = w.Live("webshop", outside)
	if err != nil {
		t.Fatalf("Live past the TTL: %v", err)
	}
	if got != nil {
		t.Errorf("Live past the TTL = %+v, want nil: a wait that stopped refreshing is not live", got)
	}
}

// TestKVWaitClaimsDeadPID pins the other half: a registration inside its TTL
// whose process is gone is dead. A crashed wait leaves a fresh-looking row.
func TestKVWaitClaimsDeadPID(t *testing.T) {
	t.Parallel()

	w := &KVWaitClaims{KV: db.TxKV{DB: testSecretDB(t)}, Alive: func(int) bool { return false }}
	now := testNow()
	if err := w.Write(WaitClaim{Name: "webshop", PID: 4242, StartedAt: now, SeenAt: now}, now); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := w.Live("webshop", now)
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if got != nil {
		t.Errorf("Live = %+v, want nil for a dead pid", got)
	}
}

// TestKVWaitClaimsRemoveLeavesOtherHolder pins that a wait exiting removes its
// own registration only: a second wait on the same binding is left holding it.
func TestKVWaitClaimsRemoveLeavesOtherHolder(t *testing.T) {
	t.Parallel()

	w := testWaits(t)
	now := testNow()
	if err := w.Write(WaitClaim{Name: "webshop", PID: 2, StartedAt: now, SeenAt: now}, now); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if err := w.Remove("webshop", 1); err != nil {
		t.Fatalf("Remove by a non-holder: %v", err)
	}
	got, err := w.Live("webshop", now)
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if got == nil {
		t.Fatal("Live = nil, want the other holder's registration left alone")
	}
	if got.PID != 2 {
		t.Errorf("PID = %d, want 2", got.PID)
	}
}

// TestKVWaitClaimsRejectsEmptyName pins that a registration with no binding is
// an error rather than a row every unnamed lookup would share.
func TestKVWaitClaimsRejectsEmptyName(t *testing.T) {
	t.Parallel()

	w := testWaits(t)
	if err := w.Write(WaitClaim{Name: "", PID: 1}, testNow()); err == nil {
		t.Error("Write with an empty name = nil, want ErrEmptyWaitName")
	}
	if _, err := w.Live("", testNow()); err == nil {
		t.Error("Live with an empty name = nil, want ErrEmptyWaitName")
	}
	if err := w.Remove("", 1); err == nil {
		t.Error("Remove with an empty name = nil, want ErrEmptyWaitName")
	}
}
