package syncpipe

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/syncworker"
)

// The wiring cases: the transport an enable drives is built from the rows the
// preflight stored, the replica goes beside the shared database, and a machine
// with no remote or no token is refused rather than handed a worker that can
// only fail.

// wiringPair opens a split pair as one installation with a machine-local file
// to read the sync rows from.
func wiringPair(t *testing.T) (*db.DB, relevosync.Local) {
	t.Helper()
	shared, err := db.OpenSplit(filepath.Join(t.TempDir(), "relevo.db"), db.Options{Origin: "m1"})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	local, err := relevosync.LocalHandle(shared)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	return shared, local
}

// wiringR2 is the bucket configuration a machine holds in these tests. The
// values are not credentials and reach no bucket: the supervisor only reads them
// to build the handshake, and the worker that would use them is never started.
var wiringR2 = relevosync.R2Secrets{
	Endpoint: "https://acct.r2.cloudflarestorage.com",
	Bucket:   "relevo-sync",
	KeyID:    "key-1",
	Secret:   "secret-1",
}

// TestOpenSupervisorReadsTheStoredRows pins that the worker is pointed at what
// the preflight stored: the origin, the remote, the token and the bucket all
// come from the machine-local rows, and the replica travels on the command
// line.
func TestOpenSupervisorReadsTheStoredRows(t *testing.T) {
	t.Parallel()
	shared, local := wiringPair(t)
	const remote = "libsql://wiring.invalid"
	const token = "FIXTURE-WIRING-TOKEN-0123456789abcdef0123456789"
	now := time.Unix(0, 0).UTC()
	if err := relevosync.PutSettings(local, relevosync.Settings{RemoteURL: remote}, now); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	if err := relevosync.SetToken(local, []byte(token), now); err != nil {
		t.Fatalf("SetToken: %v", err)
	}
	if err := relevosync.SetR2(local, wiringR2, now); err != nil {
		t.Fatalf("SetR2: %v", err)
	}

	sup, err := OpenSupervisor(shared, local)
	if err != nil {
		t.Fatalf("OpenSupervisor: %v", err)
	}
	t.Cleanup(func() { _ = sup.Close() })
	if sup.client != nil {
		t.Error("OpenSupervisor started a worker")
	}
	if sup.cfg.Origin != shared.Origin() {
		t.Errorf("worker origin = %q, want %q", sup.cfg.Origin, shared.Origin())
	}
	if sup.cfg.URL != remote {
		t.Errorf("worker url = %q, want %q", sup.cfg.URL, remote)
	}
	if sup.cfg.Token != token {
		t.Error("the worker is not pointed at the stored token")
	}
	if sup.cfg.R2 == nil {
		t.Fatal("the handshake carries no R2 settings")
	}
	if got := *sup.cfg.R2; got.Endpoint != wiringR2.Endpoint || got.Bucket != wiringR2.Bucket ||
		got.KeyID != wiringR2.KeyID || got.Secret != wiringR2.Secret {
		t.Errorf("the handshake carries %+v, want the stored credentials", got)
	}
	want := []string{"sync-worker", "--replica", ReplicaPath(shared.Path())}
	if !slices.Equal(sup.cfg.Args, want) {
		t.Errorf("worker args = %v, want %v", sup.cfg.Args, want)
	}
}

// TestOpenSupervisorRefusesWithoutRemoteOrToken pins the two preflight refusals
// the wiring repeats: a machine with no remote or no token gets the sentinel
// rather than a worker that would fail at its handshake.
func TestOpenSupervisorRefusesWithoutRemoteOrToken(t *testing.T) {
	t.Parallel()
	now := time.Unix(0, 0).UTC()
	t.Run("no remote", func(t *testing.T) {
		t.Parallel()
		shared, local := wiringPair(t)
		if err := relevosync.SetToken(local, []byte("token"), now); err != nil {
			t.Fatalf("SetToken: %v", err)
		}
		if _, err := OpenSupervisor(shared, local); !errors.Is(err, relevosync.ErrNoRemote) {
			t.Fatalf("OpenSupervisor = %v, want ErrNoRemote", err)
		}
	})
	t.Run("no token", func(t *testing.T) {
		t.Parallel()
		shared, local := wiringPair(t)
		if err := relevosync.PutSettings(local, relevosync.Settings{RemoteURL: "libsql://wiring.invalid"}, now); err != nil {
			t.Fatalf("PutSettings: %v", err)
		}
		if _, err := OpenSupervisor(shared, local); !errors.Is(err, relevosync.ErrNoToken) {
			t.Fatalf("OpenSupervisor = %v, want ErrNoToken", err)
		}
	})
	t.Run("no database", func(t *testing.T) {
		t.Parallel()
		if _, err := OpenSupervisor(nil, nil); err == nil {
			t.Fatal("OpenSupervisor with no database answered without an error")
		}
	})
}

// TestOpenNoBucketSupervisor pins the join's first worker: an incomplete set
// starts the worker with nothing and answers so, and the worker it starts is
// pointed at the same rows every other enable's is. Starting no process means
// the config it would hand a hello is what the refusal question turns on, so
// it is read off the supervisor before the first call. The refusal
// OpenSupervisor answers an incomplete set with is pinned where it was
// introduced, in blobclient_test.
func TestOpenNoBucketSupervisor(t *testing.T) {
	t.Parallel()
	t.Run("no stored set", func(t *testing.T) {
		t.Parallel()
		shared, local := wiringRows(t, false)
		sup, bucketless, err := OpenNoBucketSupervisor(shared, local)
		if err != nil {
			t.Fatalf("OpenNoBucketSupervisor: %v", err)
		}
		t.Cleanup(func() { _ = sup.Close() })
		if sup.client != nil {
			t.Error("OpenNoBucketSupervisor started a worker")
		}
		if sup.cfg.R2 != nil {
			t.Errorf("the handshake carries %+v, want no bucket at all", *sup.cfg.R2)
		}
		if !bucketless {
			t.Error("the opener answers bucketed, want bucketless: it started this worker with no bucket stored")
		}
	})
	t.Run("a complete stored set", func(t *testing.T) {
		t.Parallel()
		shared, local := wiringRows(t, true)
		sup, bucketless, err := OpenNoBucketSupervisor(shared, local)
		if err != nil {
			t.Fatalf("OpenNoBucketSupervisor: %v", err)
		}
		t.Cleanup(func() { _ = sup.Close() })
		if got := *sup.cfg.R2; got != (syncworker.R2Config{
			Endpoint: wiringR2.Endpoint, Bucket: wiringR2.Bucket, KeyID: wiringR2.KeyID, Secret: wiringR2.Secret,
		}) {
			t.Errorf("the handshake carries %+v, want the stored credentials", got)
		}
		if bucketless {
			t.Error("the opener answers bucketless over a worker it started with a bucket")
		}
	})
}

// TestWorkerConfigCarriesHistoryLabelAndMirrorTables pins the three hello-alone
// fields workerConfig fills from what the daemon holds: a none section travels
// as the worker's none mode while a default travels empty for the worker to
// read as full, the mirror tables are the mirrored documents' own tables, and
// the label is the installation directory's row for this origin. Both openers
// share the function, so the no-bucket opener's config carries history and
// mirror tables too: a joiner that asked for none must have its bucketless
// worker answer from shared.
func TestWorkerConfigCarriesHistoryLabelAndMirrorTables(t *testing.T) {
	t.Parallel()
	now := time.Unix(0, 0).UTC()
	newRows := func(t *testing.T, history string) (*db.DB, relevosync.Local) {
		t.Helper()
		shared, local := wiringPair(t)
		if err := relevosync.PutSettings(local, relevosync.Settings{
			RemoteURL: "libsql://wiring.invalid", History: history,
		}, now); err != nil {
			t.Fatalf("PutSettings: %v", err)
		}
		if err := relevosync.SetToken(local, []byte("token"), now); err != nil {
			t.Fatalf("SetToken: %v", err)
		}
		return shared, local
	}
	wantTables := []string{"doc:config", "doc:secrets"}

	t.Run("none maps to the worker's none mode", func(t *testing.T) {
		t.Parallel()
		shared, local := newRows(t, relevosync.HistoryNone)
		cfg, err := workerConfig(shared, local, false)
		if err != nil {
			t.Fatalf("workerConfig: %v", err)
		}
		if cfg.History != syncworker.HistoryNone {
			t.Errorf("worker history = %q, want %q", cfg.History, syncworker.HistoryNone)
		}
		if !slices.Equal(cfg.MirrorTables, wantTables) {
			t.Errorf("worker mirror tables = %v, want %v", cfg.MirrorTables, wantTables)
		}
	})

	t.Run("default travels empty for the worker to read as full", func(t *testing.T) {
		t.Parallel()
		shared, local := newRows(t, "")
		cfg, err := workerConfig(shared, local, false)
		if err != nil {
			t.Fatalf("workerConfig: %v", err)
		}
		if cfg.History != "" {
			t.Errorf("worker history = %q, want empty for full", cfg.History)
		}
		if !slices.Equal(cfg.MirrorTables, wantTables) {
			t.Errorf("worker mirror tables = %v, want %v", cfg.MirrorTables, wantTables)
		}
	})

	t.Run("label follows the installation directory row", func(t *testing.T) {
		t.Parallel()
		shared, local := newRows(t, "")
		cfg, err := workerConfig(shared, local, false)
		if err != nil {
			t.Fatalf("workerConfig: %v", err)
		}
		if cfg.Label != "" {
			t.Errorf("worker label = %q, want empty with no directory row", cfg.Label)
		}
		if err := shared.Tx(func(tx *db.Tx) error {
			return tx.InstallationTouch(shared.Origin(), "laptop", now)
		}); err != nil {
			t.Fatalf("InstallationTouch: %v", err)
		}
		cfg, err = workerConfig(shared, local, false)
		if err != nil {
			t.Fatalf("workerConfig: %v", err)
		}
		if cfg.Label != "laptop" {
			t.Errorf("worker label = %q, want the directory row's label", cfg.Label)
		}
	})

	t.Run("no-bucket opener carries history and mirror tables", func(t *testing.T) {
		t.Parallel()
		shared, local := newRows(t, relevosync.HistoryNone)
		sup, _, err := OpenNoBucketSupervisor(shared, local)
		if err != nil {
			t.Fatalf("OpenNoBucketSupervisor: %v", err)
		}
		t.Cleanup(func() { _ = sup.Close() })
		if sup.cfg.History != syncworker.HistoryNone {
			t.Errorf("bucketless worker history = %q, want %q", sup.cfg.History, syncworker.HistoryNone)
		}
		if !slices.Equal(sup.cfg.MirrorTables, wantTables) {
			t.Errorf("bucketless worker mirror tables = %v, want %v", sup.cfg.MirrorTables, wantTables)
		}
	})
}

// wiringRows opens the split pair with the two rows every opener reads and, per
// complete, the whole bucket set.
func wiringRows(t *testing.T, complete bool) (*db.DB, relevosync.Local) {
	t.Helper()
	shared, local := wiringPair(t)
	now := time.Unix(0, 0).UTC()
	if err := relevosync.PutSettings(local, relevosync.Settings{RemoteURL: "libsql://wiring.invalid"}, now); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	if err := relevosync.SetToken(local, []byte("token"), now); err != nil {
		t.Fatalf("SetToken: %v", err)
	}
	if complete {
		if err := relevosync.SetR2(local, wiringR2, now); err != nil {
			t.Fatalf("SetR2: %v", err)
		}
	}
	return shared, local
}

// TestReplicaPathSitsBesideTheSharedDatabase pins the replica's name: the one
// file the driver owns is a sibling of the shared database, so a worker and the
// daemon cannot disagree about which state directory it is in.
func TestReplicaPathSitsBesideTheSharedDatabase(t *testing.T) {
	t.Parallel()
	got := ReplicaPath("/state/relevo/relevo.db")
	if want := "/state/relevo/relevo-sync.db"; got != want {
		t.Errorf("ReplicaPath = %q, want %q", got, want)
	}
}

// The migrator cases drive OpenMigrator against this test binary as the worker,
// named through the environment the child inherits. They stay sequential:
// that inheritance would leak into parallel siblings' children, which name
// their own mode through the same variable.

// TestOpenMigratorDrivesOneWorkerAndReapsIt pins the one-shot transport: it
// opens from the stored rows, the migrate verb crosses it, and Close reaps the
// worker. The verb succeeding proves the hello carried the migrate flag, since
// the worker refuses the verb on any other pipe.
func TestOpenMigratorDrivesOneWorkerAndReapsIt(t *testing.T) {
	shared, local := wiringRows(t, true)
	t.Setenv(fakeWorkerEnv, modeNormal)
	m, err := OpenMigrator(shared, local)
	if err != nil {
		t.Fatalf("OpenMigrator: %v", err)
	}
	got, err := m.Migrate()
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if want := wantMigratedResult(); !reflect.DeepEqual(got, want) {
		t.Errorf("Migrate = %+v, want %+v", got, want)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	mm, ok := m.(*migrator)
	if !ok {
		t.Fatalf("OpenMigrator holds %T, want the one-shot transport", m)
	}
	if !waitForExit(t, mm.client, 10*time.Second) {
		t.Error("the migrate worker is still running after Close")
	}
}

// TestOpenMigratorRefusesWithoutStoredRows pins that the migrator answers a
// machine with no remote the way the supervisor's opener does: with the
// sentinel rather than a worker that can only fail at its handshake.
func TestOpenMigratorRefusesWithoutStoredRows(t *testing.T) {
	shared, local := wiringPair(t)
	if _, err := OpenMigrator(shared, local); !errors.Is(err, relevosync.ErrNoRemote) {
		t.Fatalf("OpenMigrator = %v, want ErrNoRemote", err)
	}
}

// TestOpenMigratorSurfacesAHelloRefusalWithNoWorkerLeft pins that a refused
// hello propagates from OpenMigrator's start and leaves no worker running: no
// transport escapes, and the recorded pid goes quiet once the parent closes
// the pipe it refused over.
func TestOpenMigratorSurfacesAHelloRefusalWithNoWorkerLeft(t *testing.T) {
	shared, local := wiringRows(t, true)
	pidFile := filepath.Join(t.TempDir(), "worker.pid")
	t.Setenv(fakeWorkerEnv, modeRefuseHelloPid)
	t.Setenv(fakeWorkerPidFileEnv, pidFile)
	m, err := OpenMigrator(shared, local)
	if err == nil {
		t.Fatal("OpenMigrator over a refusing worker = nil, want the refusal")
	}
	if m != nil {
		t.Error("OpenMigrator answered a transport with the refusal, want nil")
	}
	if !errors.Is(err, ErrRefused) {
		t.Errorf("OpenMigrator = %v, want a refusal rather than a failure", err)
	}
	pid := readWorkerPid(t, pidFile, 10*time.Second)
	if alive, ok := workerPidAlive(pid); !ok {
		t.Skip("the reaping check needs a unix pid probe")
	} else if alive && !waitForPidGone(pid, 10*time.Second) {
		t.Errorf("the refused worker %d is still running", pid)
	}
}

// readWorkerPid polls the file the fake worker records its pid in, failing
// the test when the child never starts rather than reading an absent file.
func readWorkerPid(t *testing.T, file string, grace time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(grace)
	for {
		data, err := os.ReadFile(file)
		if err == nil {
			pid, err := strconv.Atoi(string(data))
			if err == nil {
				return pid
			}
			t.Fatalf("worker pid %q does not parse: %v", data, err)
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the fake worker never recorded its pid in %s", file)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitForPidGone reports whether pid stops answering the liveness probe
// within the grace: a reaped worker's pid goes quiet once its pipe closes.
func waitForPidGone(pid int, grace time.Duration) bool {
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if alive, ok := workerPidAlive(pid); !ok || !alive {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	alive, _ := workerPidAlive(pid)
	return !alive
}
