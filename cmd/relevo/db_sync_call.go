package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/db/wire/client"
	"github.com/fuad-daoud/relevo/internal/store"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// The one-shot calls. They share every step but the one the verb exists to make,
// so they share the body too: two near-copies of "open the handle, make the
// call" would be two places for the next flag to be added in one and forgotten
// in the other.
// cmdDBSyncPush sends this machine's local change set.
func cmdDBSyncPush(args []string) error {
	return dbSyncOneShot("db sync push", args, dbSyncPushFlagSet, wire.SyncVerbPush)
}

// cmdDBSyncPull fetches the remote's changes and rebases the local ones on top.
func cmdDBSyncPull(args []string) error {
	return dbSyncOneShot("db sync pull", args, dbSyncPullFlagSet, wire.SyncVerbPull)
}

// dbSyncOneShot is the body push and pull share: dial the owner, read the
// local settings and the stored token, send one verb, and print what it says.
// The two verbs differ only in the name, so they share every other step rather
// than being two near-copies that can drift apart.
//
// Nothing here opens a Turso handle. The daemon holds the file under its lock
// and performs the verb with its own handles, which is what removes the stop
// dance: a writer no longer competes for the lock it would have had to hold, so
// it never meets the conflict the old direct open mapped.
func dbSyncOneShot(name string, args []string, set func(*flag.FlagSet) *dbSyncCallFlagValues, verb string) error {
	fs := flag.NewFlagSet("relevo "+name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := set(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fail(codeUsage, "relevo %s takes no arguments, got %d", name, fs.NArg())
	}

	shared, err := dialSyncVerb()
	if err != nil {
		return err
	}
	defer func() { _ = shared.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), *v.timeout)
	defer cancel()

	res, err := sendSyncVerb(ctx, shared, verb, dbSyncVerbOptions{Timeout: *v.timeout})
	if err != nil {
		return err
	}
	if *v.asJSON {
		return printDoc(dbSyncCallDoc{Applied: res.Applied})
	}
	if res.Applied {
		fmt.Printf("relevo %s applied\n", name)
		return nil
	}
	fmt.Printf("relevo %s: nothing to apply\n", name)
	return nil
}

// dialSyncVerb reaches the owner serving this root, which is the only route a
// sync verb has: the daemon opens the file, so the client dials.
//
// The local file is not attached and is not needed here. The verb reads the
// settings and the token itself, on the daemon's side, with the daemon's own
// handles -- so the token never travels between two processes at all, and this
// client holds nothing but the socket.
func dialSyncVerb() (*db.DB, error) {
	root, err := store.DefaultRoot()
	if err != nil {
		return nil, failWrap(codeRefused, err, "relevo db sync: no state root")
	}
	shared, err := dialOwner(context.Background(), root, verbDialBudget)
	if err != nil {
		sock, _ := ownerSocket(root)
		return nil, failWrap(codeRefused, err, "relevo db sync: the owner at %s did not answer", sock)
	}
	return shared, nil
}

// dbSyncVerbOptions is what a verb request carries beyond its name: the flags
// the caller parsed, and nothing the daemon can read for itself.
type dbSyncVerbOptions struct {
	// RemoteURL is `--url`, empty when the flag was not passed.
	RemoteURL string
	// Timeout bounds the verb's network work. Zero selects the daemon's own.
	Timeout time.Duration
}

// sendSyncVerb sends one verb over the framed surface and classifies the answer.
//
// The refusal arrives as a result rather than as an error, so the mapping from
// the owner's code onto this CLI's exit codes happens here -- in one place, on
// the closed set of codes -- rather than in the owner, which does not know what
// a caller calls a failure. The token is not an argument: the daemon reads the
// stored one with its own local handle, so no credential crosses this call.
func sendSyncVerb(ctx context.Context, shared *db.DB, verb string, opts dbSyncVerbOptions) (*wire.SyncResult, error) {
	return sendSyncVerbToken(ctx, shared, verb, nil, opts)
}

// sendSyncVerbToken is sendSyncVerb for the one verb that carries a credential.
//
// The token is the frame's raw tail and nothing else. It is not logged, not
// formatted into an error and not sent back: the owner's only token field is a
// bool, so there is no code path on either end that can print the value even by
// accident. The socket lives under the 0700 state root and every peer passes the
// uid check, so sender and daemon are the same user on the same machine, and
// the value crosses the stream between them and nowhere else.
func sendSyncVerbToken(ctx context.Context, shared *db.DB, verb string, token []byte, opts dbSyncVerbOptions) (*wire.SyncResult, error) {
	req := &wire.SyncVerb{
		Header:    wire.Header{Type: wire.TypeSyncVerb},
		Verb:      verb,
		RemoteURL: opts.RemoteURL,
		TimeoutMS: opts.Timeout.Milliseconds(),
	}
	res, err := shared.SyncVerb(ctx, req, token)
	if err != nil {
		return nil, dbSyncVerbTransport(err)
	}
	if verr := client.VerbError(res); verr != nil {
		return nil, dbSyncVerbRefusal(verb, verr)
	}
	return res, nil
}

// dbSyncVerbRefusal maps the owner's refusal code onto this CLI's exit codes. It
// is the one place a verb failure becomes a code, and it is total over the
// closed set the owner can send: an unknown code is an internal failure rather
// than a silent success, so a code added on the wire without a mapping here
// shows up as a refusal instead of as nothing.
//
// The messages pass through untouched. They are fixed text written in this
// repository, and every one of them names the fix.
func dbSyncVerbRefusal(verb string, err error) error {
	var refusal *client.VerbRefusal
	if !errors.As(err, &refusal) {
		return dbSyncClassify(err)
	}
	name := "relevo db sync " + verb
	switch refusal.Code {
	case wire.SyncCodeNoToken, wire.SyncCodeNoRemote:
		return fail(codeUsage, "%s", refusal.Message)
	case wire.SyncCodeAlreadyEnabled:
		return failNext(codeRefused, "relevo db sync status", "%s", refusal.Message)
	case wire.SyncCodeAuthRefused:
		return fail(codeRemoteAuth, "%s", refusal.Message)
	case wire.SyncCodePreflightRefused, wire.SyncCodeRemoteConflict,
		wire.SyncCodeRemoteUnreachable, wire.SyncCodeInvalid:
		return fail(codeRefused, "%s", refusal.Message)
	case wire.SyncCodeContended:
		// The database was busy and nothing was written. The verb is idempotent
		// over that, so the next hint is the verb itself rather than a report.
		return failNext(codeRefused, "relevo db sync "+verb, "%s", refusal.Message)
	case wire.SyncCodeRemoteRefused, wire.SyncCodeRemoteSchemaMissing:
		// The remote refused the statement this machine's change set carried, and
		// said which call and which constraint. It is a refusal: the reader is
		// told the constraint rather than sent to `relevo bugreport` for a remote
		// doing what a remote with enforced foreign keys is supposed to do. The
		// next hint is the status verb, which says what this machine is set to be
		// -- the remote's half of that is where the answer is.
		return failNext(codeRefused, "relevo db sync status", "%s", refusal.Message)
	}
	_ = name
	return failWrap(codeInternal, err, "relevo db sync %s", verb)
}

// dbSyncVerbTransport classifies a verb that never reached a refusal: the owner
// was gone, was shutting down, or does not speak the verb surface at all.
//
// An owner without the verb is the refusal that names the upgrade, and it is
// refused rather than internal: the caller can act on it by upgrading the
// daemon, which is exactly what a refusal is for.
func dbSyncVerbTransport(err error) error {
	var refused *wire.Refusal
	if errors.As(err, &refused) {
		if refused.Code == wire.RefuseNoSyncVerb {
			return fail(codeRefused, "%s", refused.Message)
		}
		return fail(codeRefused, "relevo db sync: %s", refused.Message)
	}
	return failWrap(codeRefused, err, "relevo db sync")
}

// openDBSyncStatus reaches the rows the read-only status verb answers from: it
// dials the owner on the machine socket and takes the machine-local file off
// the dialled handle.
//
// That is the whole of what separates status from the writing verbs, and every
// part of it follows from status writing nothing. The writers no longer open the
// file at all: they send a verb and the daemon performs it with the handles it
// already holds, so neither needs the lock the daemon is keeping. status still
// reads the three local rows itself, over the owner's local scope, because a
// read is what it does and a verb would be a request for the daemon to change
// something in order to describe the machine. Nothing here starts a daemon
// either: a status that had to bring one up would answer a question about the
// machine by changing it.
//
// The local handle is not optional, and refusing without one is the point. An
// owner that serves none is refused rather than answered from the shared file --
// the same rule RefuseNoLocal enforces on the wire. The shared file holds no
// sync section and no token, so a silent downgrade would report a configured
// machine as unconfigured and an enabled one as off, which is exactly the
// answer the split between the two files exists to make impossible.
func openDBSyncStatus() (shared *db.DB, local *db.DB, err error) {
	path := machineDBPath()
	if path == "" {
		return nil, nil, fail(codeRefused, "no state root: set XDG_STATE_HOME or HOME")
	}
	if !fileExists(path) {
		return nil, nil, fail(codeRefused, "no relevo.db at %s", path)
	}
	root, err := store.DefaultRoot()
	if err != nil {
		return nil, nil, failWrap(codeRefused, err, "relevo db sync status: no state root")
	}
	sock, err := ownerSocket(root)
	if err != nil {
		return nil, nil, failWrap(codeRefused, err, "relevo db sync status: no owner socket for this root")
	}
	shared, err = dialOwner(context.Background(), root, verbDialBudget)
	if err != nil {
		return nil, nil, failWrap(codeRefused, err, "relevo db sync status: the owner at %s did not answer", sock)
	}
	local, err = relevosync.LocalHandle(shared)
	if err != nil {
		_ = shared.Close()
		return nil, nil, dbSyncClassify(err)
	}
	return shared, local, nil
}

// dbSyncOpenConfig is gone with the direct open: the daemon builds the config
// its enable path decided, so there is no second copy of that decision here.
// dbSyncOpener and relevosyncCloudEmpty are gone for the same reason -- both
// existed only to let a CLI process open a remote, which is precisely what the
// verb surface removes. Their bodies now live in internal/relevo, against the
// daemon's own handles.
