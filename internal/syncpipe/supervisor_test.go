package syncpipe

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// memKV is the machine-local file's kv surface in memory, so the supervisor's
// tests write markers and counts without opening a database.
type memKV struct{ m map[string][]byte }

func newMemKV() *memKV { return &memKV{m: map[string][]byte{}} }

func (k *memKV) KVGet(key string) ([]byte, bool, error) {
	v, ok := k.m[key]
	return v, ok, nil
}

func (k *memKV) KVPut(key string, value []byte) error {
	k.m[key] = append([]byte(nil), value...)
	return nil
}

func (k *memKV) KVDelete(key string) error {
	delete(k.m, key)
	return nil
}

// errKV is what the failing kv reports. The breaker's accounting is best-effort
// on top of a call whose outcome is already decided, so a test needs the state
// write itself to fail.
var errKV = errors.New("kv is unavailable")

// failKV fails the one operation a test names, so a branch that reports an
// accounting failure can be reached without breaking the call.
type failKV struct {
	inner      *memKV
	failPutKey string
	failDelete bool
}

func (k *failKV) KVGet(key string) ([]byte, bool, error) { return k.inner.KVGet(key) }

func (k *failKV) KVPut(key string, value []byte) error {
	if key == k.failPutKey {
		return errKV
	}
	return k.inner.KVPut(key, value)
}

func (k *failKV) KVDelete(key string) error {
	if k.failDelete {
		return errKV
	}
	return k.inner.KVDelete(key)
}

// fakeWorkerCfg is a handshake that spawns this test binary as a worker in the
// given mode. The mode travels in the environment because the client's arguments
// are the hidden subcommand's, which a real spawn fills in itself.
func fakeWorkerCfg(mode string) Config {
	cfg := handshake()
	cfg.Exe = os.Args[0]
	cfg.Env = append(cfg.Env, fakeWorkerEnv+"="+mode)
	return cfg
}

// newSupervisor returns a supervisor over cfg and the breaker it accounts
// through.
func newSupervisor(t *testing.T, cfg Config, b *relevosync.Breaker) *Supervisor {
	t.Helper()
	s := NewSupervisor(cfg, b)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// breakerOver returns a breaker over the kv, for the tests that need to fail one
// of its writes.
func breakerOver(kv db.KV) *relevosync.Breaker { return relevosync.NewBreaker(kv) }

// TestSupervisorDrivesEveryVerb pins the transport surface: each verb reaches
// the worker and returns what it answered, so the exchange can be handed the
// supervisor for any of its four calls.
func TestSupervisorDrivesEveryVerb(t *testing.T) {
	s := newSupervisor(t, fakeWorkerCfg(modeNormal), breakerOver(newMemKV()))

	if written, err := s.Append([]synclog.Entry{{
		Origin: "origin-a", Table: "task", PK: `["t"]`, Op: synclog.OpUpsert,
		SchemaVersion: 3, Body: []byte(`{"title":"mine"}`), At: pinnedAt,
	}}); err != nil || len(written) != 1 {
		t.Fatalf("Append = %+v, %v; want the numbered entry", written, err)
	}
	if pulled, err := s.Pull(map[string]int{"origin-b": 3}); err != nil || len(pulled) != 1 {
		t.Fatalf("Pull = %+v, %v; want the other origin's entry", pulled, err)
	}
	if rows, err := s.Head("origin-a"); err != nil || len(rows) != 1 {
		t.Fatalf("Head = %+v, %v; want the row the log holds", rows, err)
	}
	if stats, err := s.Stats(); err != nil || stats.Entries != 11 {
		t.Fatalf("Stats = %+v, %v; want what the log holds", stats, err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// TestWorkerDeathIsCounted pins that a worker which exits mid-call is dropped
// and counted, so the daemon neither keeps serving it nor loses the death.
func TestWorkerDeathIsCounted(t *testing.T) {
	b := breakerOver(newMemKV())
	s := newSupervisor(t, fakeWorkerCfg(modeDie), b)

	if _, err := s.Stats(); err == nil {
		t.Fatal("Stats over a worker that died = nil, want the call to fail")
	}
	if got, err := b.Deaths(); err != nil || got != 1 {
		t.Fatalf("deaths = %d, %v; want the death counted once", got, err)
	}
	if s.client != nil {
		t.Error("the dead worker was kept, want it dropped so the next call starts a fresh one")
	}
}

// TestMissedDeadlineKillsAndCounts pins that a worker which accepts a call and
// never answers costs the deadline and a counted death, and is killed rather
// than left wedged.
func TestMissedDeadlineKillsAndCounts(t *testing.T) {
	cfg := fakeWorkerCfg(modeHang)
	cfg.Timeout = 300 * time.Millisecond
	b := breakerOver(newMemKV())
	s := newSupervisor(t, cfg, b)

	if _, err := s.Stats(); err == nil {
		t.Fatal("Stats over a worker that never answers = nil, want the deadline")
	}
	if got, err := b.Deaths(); err != nil || got != 1 {
		t.Fatalf("deaths = %d, %v; want the deadline counted once", got, err)
	}
	if s.client != nil {
		t.Error("the wedged worker was kept, want it killed and dropped")
	}
}

// TestCancelledWorkerIsRestartedNeverReused pins that a cancelled worker is not
// handed the next call: the supervisor starts a fresh process rather than
// driving a pipe whose other end is gone.
func TestCancelledWorkerIsRestartedNeverReused(t *testing.T) {
	s := newSupervisor(t, fakeWorkerCfg(modeNormal), breakerOver(newMemKV()))

	if _, err := s.Stats(); err != nil {
		t.Fatalf("the first call: %v", err)
	}
	first := s.client.cmd.Process.Pid

	s.Cancel()
	if s.client != nil {
		t.Fatal("Cancel kept the worker, want nothing left to reuse")
	}
	if _, err := s.Stats(); err != nil {
		t.Fatalf("the call after a cancel: %v", err)
	}
	if second := s.client.cmd.Process.Pid; first == second {
		t.Errorf("the call after a cancel reused worker %d, want a fresh process", first)
	}
}

// TestSupervisorRefusalIsNotADeath pins that a worker which answers and declines
// is healthy: the refusal travels back and the breaker stays unlatched, because
// counting it would latch a machine whose remote is working.
func TestSupervisorRefusalIsNotADeath(t *testing.T) {
	b := breakerOver(newMemKV())
	s := newSupervisor(t, fakeWorkerCfg(modeRefuse), b)

	if _, err := s.Stats(); !errors.Is(err, ErrRefused) {
		t.Fatalf("Stats over a refusing worker = %v, want the refusal", err)
	}
	if got, _ := b.Deaths(); got != 0 {
		t.Errorf("deaths = %d, want a refusal left uncounted", got)
	}
}

// TestSupervisorStopsOnceLatched pins that a latched breaker refuses the call
// before a worker is spawned, so a machine that has stopped does not keep
// starting processes against a fault a human has to clear.
func TestSupervisorStopsOnceLatched(t *testing.T) {
	b := breakerOver(newMemKV())
	if err := b.Refused(fmt.Errorf("push: %w", relevosync.ErrRemoteSchema)); err != nil {
		t.Fatalf("Refused: %v", err)
	}
	s := newSupervisor(t, fakeWorkerCfg(modeNormal), b)

	if _, err := s.Stats(); !errors.Is(err, relevosync.ErrLatched) {
		t.Fatalf("Stats while latched = %v, want ErrLatched", err)
	}
	if s.client != nil {
		t.Error("a latched supervisor started a worker")
	}
}

// TestSupervisorCountsAWorkerItCannotStart pins that a worker the daemon cannot
// spawn is a counted death too, so a broken executable backs off instead of
// being retried on every tick.
func TestSupervisorCountsAWorkerItCannotStart(t *testing.T) {
	cfg := fakeWorkerCfg(modeNormal)
	cfg.Exe = os.Args[0] + ".does-not-exist"
	b := breakerOver(newMemKV())
	s := newSupervisor(t, cfg, b)

	if _, err := s.Stats(); err == nil {
		t.Fatal("Stats over a worker that will not start = nil, want the failure")
	}
	if got, err := b.Deaths(); err != nil || got != 1 {
		t.Fatalf("deaths = %d, %v; want the failed start counted once", got, err)
	}
}

// TestSupervisorReportsAccountingFailures pins that a marker the supervisor
// cannot write or clear is reported rather than swallowed: a machine whose
// breaker cannot record a death must not be told the call succeeded.
func TestSupervisorReportsAccountingFailures(t *testing.T) {
	cases := map[string]struct {
		mode   string
		kv     *failKV
		settle error
	}{
		"a marker that cannot be cleared": {
			mode: modeNormal,
			kv:   &failKV{inner: newMemKV(), failDelete: true},
		},
		"a count that cannot be saved": {
			mode: modeNormal,
			kv:   &failKV{inner: newMemKV(), failPutKey: relevosync.KeyDeaths},
		},
		"a death that cannot be recorded": {
			mode: modeDie,
			kv:   &failKV{inner: newMemKV(), failPutKey: relevosync.KeyDeaths},
		},
		"a latch that cannot be written": {
			mode:   modeNormal,
			kv:     &failKV{inner: newMemKV(), failPutKey: relevosync.KeyAttention},
			settle: fmt.Errorf("push: %w: %w", ErrRefused, relevosync.ErrRemoteSchema),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := newSupervisor(t, fakeWorkerCfg(tc.mode), breakerOver(tc.kv))
			var err error
			if tc.settle != nil {
				err = s.settle(tc.settle)
			} else {
				_, err = s.Stats()
			}
			if err == nil {
				t.Fatal("the supervisor swallowed an accounting failure")
			}
		})
	}
}
