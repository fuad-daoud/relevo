package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/fuad-daoud/relevo/internal/db"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// The one-shot calls. They share every step but the one the verb exists to make,
// so they share the body too: two near-copies of "open the handle, make the
// call" would be two places for the next flag to be added in one and forgotten
// in the other.
// cmdDBSyncPush sends this machine's local change set.
func cmdDBSyncPush(args []string) error {
	return dbSyncOneShot("db sync push", args, dbSyncPushFlagSet, func(ctx context.Context, client relevosync.SyncClient) (bool, error) {
		return false, client.Push(ctx)
	})
}

// cmdDBSyncPull fetches the remote's changes and rebases the local ones on top.
func cmdDBSyncPull(args []string) error {
	return dbSyncOneShot("db sync pull", args, dbSyncPullFlagSet, func(ctx context.Context, client relevosync.SyncClient) (bool, error) {
		return client.Pull(ctx)
	})
}

// dbSyncOneShot is the body push and pull share: open the handle the settings
// and the token name, make the one call, and let the process exit with the
// handle. The two verbs differ only in the call, so they share every other step
// rather than being two near-copies that can drift apart.
func dbSyncOneShot(name string, args []string, set func(*flag.FlagSet) *dbSyncCallFlagValues, call func(context.Context, relevosync.SyncClient) (bool, error)) error {
	fs := flag.NewFlagSet("relevo "+name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := set(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fail(codeUsage, "relevo %s takes no arguments, got %d", name, fs.NArg())
	}

	shared, local, err := openDBSync()
	if err != nil {
		return err
	}
	defer func() { _ = shared.Close() }()

	handle, err := dbSyncHandle(name, local)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), *v.timeout)
	defer cancel()
	applied, err := call(ctx, handle)
	if err != nil {
		return failWrap(codeRefused, err, "relevo %s", name)
	}
	if *v.asJSON {
		return printDoc(dbSyncCallDoc{Applied: applied})
	}
	if applied {
		fmt.Printf("relevo %s applied\n", name)
		return nil
	}
	fmt.Printf("relevo %s: nothing to apply\n", name)
	return nil
}

// dbSyncHandle opens the handle the machine-local settings and token name, and
// refuses when either is missing. A push or a pull with no remote or no token
// has nothing to reach and nothing to authenticate with, and saying which one
// is missing is the whole difference between a refusal the user can act on and
// one they have to guess at.
func dbSyncHandle(name string, local *db.DB) (relevosync.SyncClient, error) {
	settings, err := relevosync.ReadSettings(local)
	if err != nil {
		return nil, failWrap(codeConfigInvalid, err, "relevo %s", name)
	}
	if settings.RemoteURL == "" {
		return nil, fail(codeRefused, "relevo %s: no remote is configured on this machine; run `relevo db sync enable`", name)
	}
	token, hasToken, err := relevosync.ReadToken(local)
	if err != nil {
		return nil, failWrap(codeConfigInvalid, err, "relevo %s", name)
	}
	if !hasToken {
		return nil, fail(codeRefused, "relevo %s: no %s on this machine; run `relevo db sync enable --token-stdin`", name, relevosync.SecretToken)
	}

	handle, err := relevosync.OpenRemote(context.Background(), relevosync.OpenConfig{
		Path:             machineDBPath(),
		RemoteURL:        settings.RemoteURL,
		Namespace:        settings.Namespace,
		ClientName:       dbSyncClientName,
		AuthToken:        token,
		BootstrapIfEmpty: false,
	})
	if err != nil {
		return nil, dbSyncClassify(err)
	}
	return handle, nil
}

// openDBSync opens the shared database and names the machine-local file beside
// it, which is where every row a sync verb writes lives.
//
// The open is direct rather than through the owner, and that is a constraint
// rather than a choice: the machine-local file is this machine's own, and the
// owner's dial surface is the shared file alone, so there is no owner path to a
// local write. It also means the daemon must not be running -- the file lock is
// what says so -- which is the same requirement the seed upload path has, since
// an upload reads the very file this verb writes.
func openDBSync() (shared *db.DB, local *db.DB, err error) {
	path := machineDBPath()
	if path == "" {
		return nil, nil, fail(codeRefused, "no state root: set XDG_STATE_HOME or HOME")
	}
	if !fileExists(path) {
		return nil, nil, fail(codeRefused, "no relevo.db at %s", path)
	}

	shared, err = openDBDirect(path)
	if err != nil {
		if errors.Is(err, db.ErrLocked) {
			return nil, nil, failNext(codeConflict, "relevo daemon stop",
				"relevo.db is held (%s.lock); sync writes the machine-local file beside it, so the daemon must not be running", path)
		}
		return nil, nil, failWrap(codeInternal, err, "open %s", path)
	}
	local, err = relevosync.LocalHandle(shared)
	if err != nil {
		_ = shared.Close()
		return nil, nil, failWrap(codeInternal, err, "open %s", db.SplitPath(path))
	}
	return shared, local, nil
}

// dbSyncOpenConfig is the open config the settings and the stored token describe.
// bootstrap says whether this open may take the remote's initial state, which is
// only ever true for a machine with no history to lose.
func dbSyncOpenConfig(settings relevosync.Settings, local *db.DB, token []byte, bootstrap bool) relevosync.OpenConfig {
	return relevosync.OpenConfig{
		Path:             machineDBPath(),
		RemoteURL:        settings.RemoteURL,
		Namespace:        settings.Namespace,
		ClientName:       dbSyncClientName,
		AuthToken:        token,
		BootstrapIfEmpty: bootstrap,
	}
}

// dbSyncOpener is the opener enable builds its handle with. It fills in what
// only this machine knows -- the file, the remote, the namespace and the client
// name -- and leaves the bootstrap decision and the token exactly as the enable
// path put them, so neither is decided twice.
func dbSyncOpener(settings relevosync.Settings) relevosync.Opener {
	return func(ctx context.Context, cfg relevosync.OpenConfig) (relevosync.SyncClient, error) {
		if settings.RemoteURL == "" {
			return nil, fmt.Errorf("sync: enable: no remote is configured on this machine: %w", db.ErrInvalid)
		}
		cfg.Path = machineDBPath()
		cfg.RemoteURL = settings.RemoteURL
		cfg.Namespace = settings.Namespace
		cfg.ClientName = dbSyncClientName
		return relevosync.OpenRemote(ctx, cfg)
	}
}

// relevosyncCloudEmpty reports whether the remote holds nothing yet.
//
// It answers by opening the remote with the bootstrap explicitly off and asking
// whether a pull brought anything back: a remote with nothing in it applies no
// changes, and a remote with something in it applies at least one. The pull is
// the only question the driver's own surface answers about a remote's contents,
// so it is the whole of the probe -- and it runs before anything is marked, so a
// refusal here leaves the machine untouched.
//
// The token arrives as an argument rather than out of the local file, because
// the enable has resolved it and deliberately not stored it yet.
func relevosyncCloudEmpty(ctx context.Context, settings relevosync.Settings, token []byte) (bool, error) {
	if settings.RemoteURL == "" {
		return false, fail(codeRefused, "relevo db sync enable: no remote is configured on this machine; set the sync section's remote_url first")
	}
	handle, err := relevosync.OpenRemote(ctx, relevosync.OpenConfig{
		Path:             machineDBPath(),
		RemoteURL:        settings.RemoteURL,
		Namespace:        settings.Namespace,
		ClientName:       dbSyncClientName,
		AuthToken:        token,
		BootstrapIfEmpty: false,
	})
	if err != nil {
		return false, dbSyncClassify(err)
	}
	applied, err := handle.Pull(ctx)
	if err != nil {
		return false, failWrap(codeRemoteUnreachable, err, "relevo db sync enable: read the remote")
	}
	return !applied, nil
}
