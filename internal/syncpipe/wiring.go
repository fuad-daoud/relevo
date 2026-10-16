package syncpipe

// The daemon-side wiring an enable drives: a worker transport built from the
// machine-local rows the enable stored, and the replica beside the shared
// database the worker owns.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/db"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/syncworker"
)

// OpenSupervisor builds the transport an enable drives from the rows the
// preflight has just stored: where the remote is, which token reaches it, and
// where the worker's replica belongs.
//
// It starts no process. The first call starts the worker, which is also what
// bootstraps the replica, so a preflight that refuses never leaves a child
// behind. The replica's path travels on the worker's command line because the
// worker owns the only handle on that file, and a path it derived itself could
// name a file the daemon did not mean. An incomplete bucket set is refused
// rather than started without -- the enable preflight is what should have
// caught it -- and a join opens its first worker through OpenNoBucketSupervisor.
func OpenSupervisor(shared *db.DB, local relevosync.Local) (*Supervisor, error) {
	cfg, err := workerConfig(shared, local, true)
	if err != nil {
		return nil, err
	}
	return NewSupervisor(cfg, relevosync.NewBreaker(local)), nil
}

// OpenNoBucketSupervisor builds the transport a join's passphrase check and its
// secrets pre-pass drive: the same rows OpenSupervisor reads, but a machine
// whose stored bucket set is incomplete starts a worker with none instead of
// being refused. A joiner holds no credentials until the fleet's secrets
// document has been applied, and that document is only on the remote the worker
// is opened to reach -- a worker a hello starts with no bucket works for
// everything that carries no body and refuses its first blob verb, which is
// the shape a join's first worker is.
//
// A complete stored set gives the worker a bucket exactly as OpenSupervisor
// would, so a caller cannot tell the two openers apart while it is credentials-
// complete.
//
// The bool is the answer about the worker this opener built: whether it started
// that worker with no bucket stored, read off the same rows the handshake was
// built from. It stands on its own, without re-reading the rows later, because
// the opener is the thing that made the decision.
func OpenNoBucketSupervisor(shared *db.DB, local relevosync.Local) (*Supervisor, bool, error) {
	cfg, err := workerConfig(shared, local, false)
	if err != nil {
		return nil, false, err
	}
	sup := NewSupervisor(cfg, relevosync.NewBreaker(local))
	return sup, cfg.R2 == nil, nil
}

// workerConfig is the body both openers build the worker from.
//
// The R2 credentials are read here rather than passed in, so nothing outside
// this function ever holds them and the handshake is assembled in one place.
// refuseIncomplete is the caller's answer to an incomplete set: the enable that
// opens through OpenSupervisor had its preflight to catch an incomplete one, so
// the refusal there is the truth about the machine; the join's first worker is
// opened for exactly the enable that brings them, so none gathered is none
// needed, and the caller answers for what its blob verbs refuse.
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

// ReplicaPath is the replica beside a shared database: the one file the sync
// driver owns, named from the shared path so the daemon and the worker cannot
// disagree about which state directory it is in.
func ReplicaPath(sharedPath string) string {
	return filepath.Join(filepath.Dir(sharedPath), "relevo-sync.db")
}

// BlobStagingDir is the folder bodies are written to and read from on their way
// to and from R2. It sits beside the replica rather than inside it, for the same
// reason the replica is named from the shared path: both are derived from one
// place so the daemon and the worker cannot disagree about which state directory
// they are in.
//
// A body in this folder is in transit. It is the user's content that has not
// reached the bucket yet and has not been applied yet either, so the directory is
// created 0700: another account on this machine must not be able to read it out
// of a directory its own user does not own.
func BlobStagingDir(sharedPath string) string {
	return filepath.Join(filepath.Dir(sharedPath), stagingDirName)
}

// stagingDirName is the folder beside the replica that bodies pass through.
const stagingDirName = "sync-blobs"

// EnsureBlobStagingDir creates the staging folder if it is not there, and
// returns its path. It is a call rather than a side effect of BlobStagingDir
// because a path function that wrote to disk would be a path function that can
// fail, and every caller that only wanted to name the folder would have to
// handle that.
func EnsureBlobStagingDir(sharedPath string) (string, error) {
	dir := BlobStagingDir(sharedPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("syncpipe: create %s: %w", dir, err)
	}
	// MkdirAll leaves an existing folder's mode alone, so a folder created by an
	// earlier run under a different umask is tightened here rather than trusted:
	// it holds bodies that have not been stored anywhere else yet.
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("syncpipe: secure %s: %w", dir, err)
	}
	return dir, nil
}
