package syncpipe

// The daemon-side wiring an enable drives: a worker transport from the just-stored
// machine-local rows, and the worker-owned replica beside the shared database.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/db"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/syncworker"
)

// OpenSupervisor builds the transport an enable drives from the just-stored rows:
// remote, token and replica path. It starts no process: the first call starts the
// worker and bootstraps the replica, so a refused preflight leaves no child. The
// replica path travels on the command line because the worker owns the only handle;
// a self-derived path could name an unintended file. An incomplete bucket set is
// refused here; a join opens through OpenNoBucketSupervisor.
func OpenSupervisor(shared *db.DB, local relevosync.Local) (*Supervisor, error) {
	cfg, err := workerConfig(shared, local, true)
	if err != nil {
		return nil, err
	}
	return NewSupervisor(cfg, relevosync.NewBreaker(local)), nil
}

// OpenNoBucketSupervisor builds the transport a join's passphrase check and
// secrets pre-pass drive. An incomplete stored bucket set starts a worker with none
// instead of refusing: the joiner holds no credentials until the fleet's secrets
// document applies, and that document is only on the remote this worker reaches. A
// complete set gives a bucket exactly as OpenSupervisor would. The bool answers
// whether this worker started bucketless, read off the same rows, without
// re-reading later.
func OpenNoBucketSupervisor(shared *db.DB, local relevosync.Local) (*Supervisor, bool, error) {
	cfg, err := workerConfig(shared, local, false)
	if err != nil {
		return nil, false, err
	}
	sup := NewSupervisor(cfg, relevosync.NewBreaker(local))
	return sup, cfg.R2 == nil, nil
}

// workerConfig is the body both openers build from. Credentials are read here, so
// nothing outside ever holds them and the handshake assembles in one place.
// refuseIncomplete is the caller's answer to an incomplete set: the enable's
// preflight should have caught it, while the join's first worker opens for exactly
// the enable bringing them, so none gathered is none needed and its blob verbs
// refuse.
func workerConfig(shared *db.DB, local relevosync.Local, refuseIncomplete bool) (Config, error) {
	if shared == nil || local == nil {
		return Config{}, fmt.Errorf("syncpipe: no database to sync: %w", db.ErrInvalid)
	}
	settings, err := relevosync.ReadSettings(local)
	if err != nil {
		return Config{}, err
	}
	if settings.RemoteURL == "" {
		return Config{}, relevosync.ErrNoRemote
	}
	token, ok, err := relevosync.ReadToken(local)
	if err != nil {
		return Config{}, err
	}
	if !ok || len(bytes.TrimSpace(token)) == 0 {
		return Config{}, relevosync.ErrNoToken
	}
	r2, err := relevosync.ReadR2(local)
	if err != nil {
		return Config{}, err
	}
	var bucket *syncworker.R2Config
	if r2.Complete() {
		bucket = &syncworker.R2Config{Endpoint: r2.Endpoint, Bucket: r2.Bucket, KeyID: r2.KeyID, Secret: r2.Secret}
	} else if refuseIncomplete {
		return Config{}, relevosync.ErrNoR2
	}
	staging, err := EnsureBlobStagingDir(shared.Path())
	if err != nil {
		return Config{}, err
	}
	return Config{
		Args:        []string{"sync-worker", "--replica", ReplicaPath(shared.Path())},
		Origin:      shared.Origin(),
		URL:         settings.RemoteURL,
		Token:       string(token),
		R2:          bucket,
		BlobStaging: staging,
	}, nil
}

// ReplicaPath is the replica beside a shared database: the driver's one file,
// named from the shared path so daemon and worker cannot disagree on the state
// directory.
func ReplicaPath(sharedPath string) string {
	return filepath.Join(filepath.Dir(sharedPath), "relevo-sync.db")
}

// BlobStagingDir is the folder bodies pass through to and from R2, beside the
// replica for the same one-place reason. A body here is in transit, stored nowhere
// else yet, so the directory is 0700: no other local account reads it.
func BlobStagingDir(sharedPath string) string {
	return filepath.Join(filepath.Dir(sharedPath), stagingDirName)
}

const stagingDirName = "sync-blobs"

// EnsureBlobStagingDir creates the staging folder and returns its path. It is a
// call, not a side effect of BlobStagingDir: a path function that writes can fail,
// and every namer would handle that.
func EnsureBlobStagingDir(sharedPath string) (string, error) {
	dir := BlobStagingDir(sharedPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("syncpipe: create %s: %w", dir, err)
	}
	// MkdirAll leaves an existing mode alone, so an earlier looser-umask folder is
	// tightened here, not trusted.
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("syncpipe: secure %s: %w", dir, err)
	}
	return dir, nil
}
