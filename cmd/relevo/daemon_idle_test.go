package main

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

func TestIdleExit(t *testing.T) {
	for _, c := range []struct {
		name    string
		a       daemonActivity
		idleFor time.Duration
		after   time.Duration
		want    bool
	}{
		{"live connection", daemonActivity{Conns: 1, RootExists: true}, time.Hour, time.Minute, false},
		{"running builder", daemonActivity{Running: true, RootExists: true}, time.Hour, time.Minute, false},
		{"queued round", daemonActivity{Queued: true, RootExists: true}, time.Hour, time.Minute, false},
		{"sync on", daemonActivity{SyncOn: true, RootExists: true}, time.Hour, time.Minute, false},
		{"sync on with the root gone", daemonActivity{SyncOn: true}, 0, time.Minute, true},
		{"all idle under the period", daemonActivity{RootExists: true}, 30 * time.Second, time.Minute, false},
		{"all idle past the period", daemonActivity{RootExists: true}, 2 * time.Minute, time.Minute, true},
		{"root gone while busy", daemonActivity{Conns: 1, Running: true, Queued: true}, 0, time.Minute, true},
		{"after zero never exits", daemonActivity{RootExists: true}, time.Hour, 0, false},
		{"no activity past the period", daemonActivity{RootExists: true}, time.Hour, time.Minute, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := idleExit(c.a, c.idleFor, c.after); got != c.want {
				t.Errorf("idleExit(%+v, %s, %s) = %v, want %v", c.a, c.idleFor, c.after, got, c.want)
			}
		})
	}
}

// TestDaemonActivityFromStore pins the sampler against a real store: a headless
// builder with a pid and a running gate count as work, a QueuedAt or a remote
// RemoteQueue counts as a queued round, and a root whose database will not open
// reports busy rather than idle.
func TestDaemonActivityFromStore(t *testing.T) {
	newStore := func(t *testing.T) (string, *store.Store) {
		t.Helper()
		root := t.TempDir()
		return root, store.New(root)
	}

	t.Run("headless builder with a pid", func(t *testing.T) {
		root, s := newStore(t)
		if err := s.Save(store.Binding{
			Name: "builder", CWD: filepath.Join(root, "w"), Round: 1, State: store.StateActive,
			Builder: store.Endpoint{Mode: store.ModeHeadless, PID: 4242},
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}
		a := daemonActivityNow(root, nil, s)
		if !a.RootExists || !a.Running || a.Queued || a.Conns != 0 {
			t.Errorf("activity = %+v, want running with the root present", a)
		}
	})

	t.Run("queued binding", func(t *testing.T) {
		root, s := newStore(t)
		if err := s.Save(store.Binding{
			Name: "queued", CWD: filepath.Join(root, "w"), Round: 1, State: store.StateActive,
			QueuedAt: time.Now(),
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}
		a := daemonActivityNow(root, nil, s)
		if !a.Queued || a.Running {
			t.Errorf("activity = %+v, want queued and not running", a)
		}
	})

	t.Run("remote queued binding", func(t *testing.T) {
		root, s := newStore(t)
		if err := s.Save(store.Binding{
			Name: "remote", CWD: filepath.Join(root, "w"), Round: 1, State: store.StateActive,
			Builder: store.Endpoint{
				Mode:        store.ModeRemote,
				Server:      "relevo.example.test",
				RemoteQueue: &store.QueueFacts{Position: 1},
			},
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}
		a := daemonActivityNow(root, nil, s)
		if !a.Queued || a.Running {
			t.Errorf("activity = %+v, want queued and not running", a)
		}
	})

	t.Run("running gate", func(t *testing.T) {
		root, s := newStore(t)
		if err := s.Save(store.Binding{
			Name: "gating", CWD: filepath.Join(root, "w"), Round: 1, State: store.StateActive,
			GateRun: &store.GateRun{PID: 7, Round: 1},
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}
		a := daemonActivityNow(root, nil, s)
		if !a.Running || a.Queued {
			t.Errorf("activity = %+v, want running and not queued", a)
		}
	})

	t.Run("unreadable database", func(t *testing.T) {
		root := t.TempDir()
		// A directory where the database file belongs makes the store read
		// fail; the sampler must report busy rather than idle.
		if err := os.MkdirAll(filepath.Join(root, "relevo.db"), 0o755); err != nil {
			t.Fatalf("mkdir relevo.db: %v", err)
		}
		a := daemonActivityNow(root, nil, store.New(root))
		if !a.RootExists || !a.Running {
			t.Errorf("activity = %+v, want busy with the root present", a)
		}
	})

	t.Run("missing root", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "gone")
		a := daemonActivityNow(root, nil, store.New(root))
		if a.RootExists {
			t.Errorf("activity = %+v, want the root reported gone", a)
		}
	})
}

// TestWatchDaemonIdleCancelsWhenIdlePastThePeriod pins the watcher's exit: an
// injected clock advances a second per read, so the second idle sample passes
// the period and the watcher cancels the context and returns.
func TestWatchDaemonIdleCancelsWhenIdlePastThePeriod(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var samples int32
	sample := func() daemonActivity {
		atomic.AddInt32(&samples, 1)
		return daemonActivity{RootExists: true}
	}
	var clock int64
	now := func() time.Time {
		return time.Unix(0, atomic.AddInt64(&clock, int64(time.Second)))
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		watchDaemonIdle(ctx, cancel, 2*time.Second, time.Millisecond, now, sample)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not exit while idle past the period")
	}
	if ctx.Err() == nil {
		t.Fatal("watcher returned without cancelling the context")
	}
	if got := atomic.LoadInt32(&samples); got < 2 {
		t.Errorf("watcher sampled %d times, want at least 2", got)
	}
}

// TestWatchDaemonIdleStaysWhileBusyAndReturnsOnCancel pins the other half: a
// busy sample never cancels, and a cancelled context ends the watcher. The
// scripted sample blocks between ticks, so the assertions are race-free.
func TestWatchDaemonIdleStaysWhileBusyAndReturnsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	calls := make(chan struct{}, 64)
	sample := func() daemonActivity {
		calls <- struct{}{}
		return daemonActivity{RootExists: true, Running: true}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		watchDaemonIdle(ctx, cancel, time.Millisecond, time.Millisecond, time.Now, sample)
	}()

	for i := 0; i < 3; i++ {
		select {
		case <-calls:
		case <-time.After(5 * time.Second):
			t.Fatal("watcher stopped sampling while busy")
		}
	}
	if ctx.Err() != nil {
		t.Fatal("watcher cancelled while a builder was running")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not return after cancel")
	}
}

// TestDaemonActivityReadsTheSyncMarker pins the sampler's sync-on field against
// a real store: the machine-local enabled mark decides it, a machine that never
// turned sync on is not kept alive, and a marker the sampler cannot read keeps
// the daemon up rather than letting it exit on a sample it never took.
func TestDaemonActivityReadsTheSyncMarker(t *testing.T) {
	for _, tc := range []struct {
		name    string
		on      bool
		corrupt bool
	}{
		{"sync on", true, false},
		{"sync off", false, false},
		{"unreadable marker", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			s := store.New(root)
			mdb, err := s.DB()
			if err != nil {
				t.Fatalf("store db: %v", err)
			}
			local, err := relevosync.LocalHandle(mdb)
			if err != nil {
				t.Fatalf("LocalHandle: %v", err)
			}
			if tc.on && !tc.corrupt {
				if err := relevosync.MarkEnabled(local, true, time.Now()); err != nil {
					t.Fatalf("MarkEnabled: %v", err)
				}
			}
			if tc.corrupt {
				if err := local.KVPut(relevosync.KeyLastAttempt, []byte(`"not an attempt"`)); err != nil {
					t.Fatalf("KVPut: %v", err)
				}
			}
			a := daemonActivityNow(root, nil, s)
			if a.SyncOn != tc.on {
				t.Errorf("activity = %+v, want SyncOn %v", a, tc.on)
			}
		})
	}
}

// TestWatchDaemonIdleStaysWhileSyncIsOn pins that a sync-on sample alone keeps
// the watcher from ever cancelling the daemon.
func TestWatchDaemonIdleStaysWhileSyncIsOn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	calls := make(chan struct{}, 64)
	sample := func() daemonActivity {
		calls <- struct{}{}
		return daemonActivity{RootExists: true, SyncOn: true}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		watchDaemonIdle(ctx, cancel, time.Millisecond, time.Millisecond, time.Now, sample)
	}()
	for i := 0; i < 5; i++ {
		select {
		case <-calls:
		case <-time.After(5 * time.Second):
			t.Fatal("watcher stopped sampling while sync was on")
		}
	}
	if ctx.Err() != nil {
		t.Fatal("watcher cancelled an idle daemon whose sync is on")
	}
	cancel()
	<-done
}
