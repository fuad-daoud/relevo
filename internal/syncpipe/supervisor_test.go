package syncpipe

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// syncLocal opens the machine-local file a holder's breaker writes its markers
// into, so a test drives the daemon's holder rather than a bare supervisor.
func syncLocal(t *testing.T) relevosync.Local {
	t.Helper()
	shared, err := db.OpenSplit(filepath.Join(t.TempDir(), "relevo.db"), db.Options{})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	local, err := relevosync.LocalHandle(shared)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	return local
}

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

// TestDaemonBuildsOnePipeClient pins that the daemon's holder constructs one
// pipe client however many calls it carries: two calls in a row, driven through
// the holder rather than a bare supervisor, reach the same worker process, so a
// verb and a tick sharing the holder share one client rather than each starting
// its own. The mutation is building a client per call, which leaves the second
// call on a different process.
func TestDaemonBuildsOnePipeClient(t *testing.T) {
	runner := NewSyncRunner(fakeWorkerCfg(modeNormal), syncLocal(t))
	s, ok := runner.Client.(*Supervisor)
	if !ok {
		t.Fatalf("the holder's transport = %T, want the supervisor", runner.Client)
	}
	t.Cleanup(func() { _ = s.Close() })

	if _, err := runner.Client.Stats(); err != nil {
		t.Fatalf("the first call: %v", err)
	}
	first := s.current()
	if first == nil || first.cmd.Process == nil {
		t.Fatal("the first call left no worker")
	}

	if _, err := runner.Client.Pull(nil); err != nil {
		t.Fatalf("the second call: %v", err)
	}
	second := s.current()
	if second == nil || second.cmd.Process == nil {
		t.Fatal("the second call left no worker")
	}
	if first.cmd.Process.Pid != second.cmd.Process.Pid {
		t.Errorf("calls ran on workers %d and %d, want one client for one daemon",
			first.cmd.Process.Pid, second.cmd.Process.Pid)
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

// TestCancelInterruptsACallInFlight pins the stop a cancel gives a call that is
// already running: it returns without waiting the call out, the interrupted
// call comes back, and the next call starts a fresh process rather than the one
// the cancel killed.
func TestCancelInterruptsACallInFlight(t *testing.T) {
	cfg := fakeWorkerCfg(modeHangOnce)
	cfg.Timeout = 10 * time.Second
	s := newSupervisor(t, cfg, breakerOver(newMemKV()))

	done := make(chan error, 1)
	go func() {
		_, err := s.Stats()
		done <- err
	}()

	first := waitForWorker(t, s)

	start := time.Now()
	s.Cancel()
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("Cancel took %s while a call was in flight, want it not to wait for the call", elapsed)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Error("the cancelled call came back as a success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled call did not come back")
	}
	if s.current() != nil {
		t.Error("Cancel left a worker to reuse")
	}

	if _, err := s.Head("origin-a"); err != nil {
		t.Fatalf("the call after a cancel: %v", err)
	}
	if second := s.current().cmd.Process.Pid; first == second {
		t.Errorf("the call after a cancel reused worker %d, want a fresh process", first)
	}
}

// waitForWorker returns the pid of the worker a call is running on, and fails
// when the supervisor starts none within the grace.
func waitForWorker(t *testing.T, s *Supervisor) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if c := s.current(); c != nil && c.cmd.Process != nil {
			return c.cmd.Process.Pid
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the supervisor started no worker for the call")
	return 0
}

// TestSupervisorRefusalIsNotADeath pins that a worker which answers and declines
// with no class is healthy: the refusal travels back and the breaker stays
// unlatched, because counting it would latch a machine whose remote is working.
func TestSupervisorRefusalIsNotADeath(t *testing.T) {
	b := breakerOver(newMemKV())
	s := newSupervisor(t, fakeWorkerCfg(modeRefuseTransient), b)

	if _, err := s.Stats(); !errors.Is(err, ErrRefused) {
		t.Fatalf("Stats over a refusing worker = %v, want the refusal", err)
	}
	if got, _ := b.Deaths(); got != 0 {
		t.Errorf("deaths = %d, want a refusal left uncounted", got)
	}
	if latched, _ := b.Latched(); latched {
		t.Error("a refusal a later attempt can get past latched the machine")
	}
}

// TestSupervisorLatchesOnAPermanentRefusal pins the pipe's permanent-refusal
// path end to end: the worker's marked class reaches the breaker as the
// sentinel it names, which latches with the fixed cause and the sync:err token
// instead of waiting out three deaths it can already predict.
func TestSupervisorLatchesOnAPermanentRefusal(t *testing.T) {
	kv := newMemKV()
	if err := kv.KVPut(relevosync.KeyEnabled, []byte("true")); err != nil {
		t.Fatalf("enable the machine: %v", err)
	}
	b := breakerOver(kv)
	s := newSupervisor(t, fakeWorkerCfg(modeRefuseVerbs), b)

	if _, err := s.Stats(); !errors.Is(err, relevosync.ErrRemoteRefused) {
		t.Fatalf("Stats = %v, want the permanent sentinel", err)
	}
	if latched, _ := b.Latched(); !latched {
		t.Fatal("a permanent refusal did not latch the machine")
	}
	if got, _ := b.Deaths(); got != 0 {
		t.Errorf("deaths = %d, want the refusal latched instead of counted", got)
	}
	want := "sync: the remote refused this machine's sync log"
	if cause, _ := b.LatchCause(); cause != want {
		t.Errorf("latch cause = %q, want %q", cause, want)
	}
	if tok, _ := relevosync.StatusToken(kv); tok != relevosync.TokenErr {
		t.Errorf("token = %q, want %q", tok, relevosync.TokenErr)
	}
}

// TestSupervisorLatchesOnARefusedHandshake pins the case where the refusal
// meets the worker at its first call: a handshake the remote refuses
// permanently latches the same way a refused verb does, with no death counted.
func TestSupervisorLatchesOnARefusedHandshake(t *testing.T) {
	b := breakerOver(newMemKV())
	s := newSupervisor(t, fakeWorkerCfg(modeRefuse), b)

	if _, err := s.Stats(); !errors.Is(err, relevosync.ErrRemoteRefused) {
		t.Fatalf("Stats over a refused handshake = %v, want the permanent sentinel", err)
	}
	if latched, _ := b.Latched(); !latched {
		t.Error("a handshake refused permanently did not latch the machine")
	}
	if got, _ := b.Deaths(); got != 0 {
		t.Errorf("deaths = %d, want none", got)
	}
}

// TestSupervisorLatchesOnEveryVerbRefusedPermanently pins that the latch does
// not depend on which verb met the refusal: every call the pipe carries is
// declined the same way and reaches the same latch.
func TestSupervisorLatchesOnEveryVerbRefusedPermanently(t *testing.T) {
	calls := map[string]func(*Supervisor) error{
		"export": func(s *Supervisor) error {
			_, err := s.Append([]synclog.Entry{{
				Origin: "origin-a", Table: "task", PK: `["t"]`, Op: synclog.OpUpsert,
				SchemaVersion: 3, Body: []byte(`{"title":"mine"}`), At: pinnedAt,
			}})
			return err
		},
		"pull":  func(s *Supervisor) error { _, err := s.Pull(nil); return err },
		"head":  func(s *Supervisor) error { _, err := s.Head("origin-a"); return err },
		"stats": func(s *Supervisor) error { _, err := s.Stats(); return err },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			b := breakerOver(newMemKV())
			s := newSupervisor(t, fakeWorkerCfg(modeRefuseVerbs), b)

			if err := call(s); !errors.Is(err, relevosync.ErrRemoteRefused) {
				t.Fatalf("%s = %v, want the permanent sentinel", name, err)
			}
			if latched, _ := b.Latched(); !latched {
				t.Errorf("%s did not latch the machine", name)
			}
			if got, _ := b.Deaths(); got != 0 {
				t.Errorf("deaths = %d, want none", got)
			}
		})
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
				err = s.settle(nil, tc.settle)
			} else {
				_, err = s.Stats()
			}
			if err == nil {
				t.Fatal("the supervisor swallowed an accounting failure")
			}
		})
	}
}
