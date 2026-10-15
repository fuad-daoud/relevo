package syncpipe

import (
	"errors"
	"path/filepath"
	"slices"
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
// starts the worker with nothing, and the worker it starts is pointed at the
// same rows every other enable's is. Starting no process means the config it
// would hand a hello is what the refusal question turns on, so it is read off
// the supervisor before the first call. The refusal OpenSupervisor answers an
// incomplete set with is pinned where it was introduced, in blobclient_test.
func TestOpenNoBucketSupervisor(t *testing.T) {
	t.Parallel()
	t.Run("no stored set", func(t *testing.T) {
		t.Parallel()
		shared, local := wiringRows(t, false)
		sup, err := OpenNoBucketSupervisor(shared, local)
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
	})
	t.Run("a complete stored set", func(t *testing.T) {
		t.Parallel()
		shared, local := wiringRows(t, true)
		sup, err := OpenNoBucketSupervisor(shared, local)
		if err != nil {
			t.Fatalf("OpenNoBucketSupervisor: %v", err)
		}
		t.Cleanup(func() { _ = sup.Close() })
		if got := *sup.cfg.R2; got != (syncworker.R2Config{
			Endpoint: wiringR2.Endpoint, Bucket: wiringR2.Bucket, KeyID: wiringR2.KeyID, Secret: wiringR2.Secret,
		}) {
			t.Errorf("the handshake carries %+v, want the stored credentials", got)
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
