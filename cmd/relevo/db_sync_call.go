package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/db/wire/client"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/tmpdir"
)

// cmdDBSyncPush sends this machine's local change set. Push and pull share the
// body below so the next flag cannot land in one and not the other.
func cmdDBSyncPush(args []string) error {
	return dbSyncOneShot("db sync push", args, dbSyncPushFlagSet, wire.SyncVerbPush)
}

// cmdDBSyncPull fetches the remote's changes and rebases the local ones on top.
func cmdDBSyncPull(args []string) error {
	return dbSyncOneShot("db sync pull", args, dbSyncPullFlagSet, wire.SyncVerbPull)
}

// dbSyncOneShot is the body push and pull share: dial the owner, send one verb,
// and print what it says. Nothing here opens a Turso handle: the daemon holds
// the file under its lock and performs the verb with its own handles, so a
// writer here never competes for the lock.
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
		return printDoc(syncCallDoc(res))
	}
	fmt.Print(syncCallText(name, res))
	return nil
}

// syncCallDoc is the --json answer of a push or pull: the applied flag, each
// hold a pull found, and the command that reads them again.
func syncCallDoc(res *wire.SyncResult) dbSyncCallDoc {
	doc := dbSyncCallDoc{Applied: res.Applied, Holds: res.Holds}
	if len(res.Holds) > 0 {
		doc.Next = dbSyncHoldNext
	}
	return doc
}

// dbSyncHoldNext is where a reader goes to see a hold a pull reported.
const dbSyncHoldNext = "relevo db sync status"

// syncCallText is the text answer of a push or pull. A pull that held an origin
// names each hold, so a run that stopped short does not read as one that finished.
func syncCallText(name string, res *wire.SyncResult) string {
	var b strings.Builder
	if res.Applied {
		fmt.Fprintf(&b, "relevo %s applied\n", name)
	} else {
		fmt.Fprintf(&b, "relevo %s: nothing to apply\n", name)
	}
	for _, hold := range res.Holds {
		fmt.Fprintf(&b, "held: %s\n", hold)
	}
	if len(res.Holds) > 0 {
		fmt.Fprintf(&b, "next: %s\n", dbSyncHoldNext)
	}
	return b.String()
}

// dialSyncVerb reaches the owner serving this root, the only route a sync verb
// has. The verb reads the settings and the token itself on the daemon's side, so
// the token never travels between two processes and this client holds nothing
// but the socket.
func dialSyncVerb() (*db.DB, error) {
	root, err := tmpdir.DefaultRoot()
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
	// R2Endpoint, R2Bucket and R2KeyID are enable's bucket flags, and
	// R2SecretLen the length of the bucket secret at the end of the tail.
	R2Endpoint  string
	R2Bucket    string
	R2KeyID     string
	R2SecretLen int
	// PassphraseLen is the length of the passphrase in the tail, and Join says
	// the enable carries a code rather than a hand-entered remote: the code names
	// the remote and the token, which leaves the tail's middle to the passphrase.
	PassphraseLen int
	// NewPassphraseLen is the length of the new passphrase behind it, which a
	// rekey's tail carries.
	NewPassphraseLen int
	Join             bool
	Yes              bool
	AdoptAs          string
	DryRun           bool
	New              bool
	// Rebuild is retry's --rebuild: it asks the retry to move the replica
	// aside before the next attempt, ignoring the window an automatic
	// rebuild waits out.
	Rebuild bool
}

// sendSyncVerb sends one verb over the framed surface and classifies the answer.
//
// The refusal arrives as a result rather than as an error, so the mapping from
// the owner's code onto this CLI's exit codes happens here, on the closed set of
// codes. The token is not an argument: the daemon reads the stored one with its
// own handle, so no credential crosses this call.
func sendSyncVerb(ctx context.Context, shared *db.DB, verb string, opts dbSyncVerbOptions) (*wire.SyncResult, error) {
	return sendSyncVerbToken(ctx, shared, verb, nil, opts)
}

// sendSyncVerbToken is sendSyncVerb for the one verb that carries a credential.
//
// The token is the frame's raw tail and nothing else: it is not logged or
// formatted into an error, and the socket's uid check means sender and daemon
// are the same user, so the value crosses the stream between them and nowhere else.
func sendSyncVerbToken(ctx context.Context, shared *db.DB, verb string, token []byte, opts dbSyncVerbOptions) (*wire.SyncResult, error) {
	req := &wire.SyncVerb{
		Header:           wire.Header{Type: wire.TypeSyncVerb},
		Verb:             verb,
		RemoteURL:        opts.RemoteURL,
		TimeoutMS:        opts.Timeout.Milliseconds(),
		R2Endpoint:       opts.R2Endpoint,
		R2Bucket:         opts.R2Bucket,
		R2KeyID:          opts.R2KeyID,
		R2SecretLen:      opts.R2SecretLen,
		PassphraseLen:    opts.PassphraseLen,
		NewPassphraseLen: opts.NewPassphraseLen,
		Join:             opts.Join,
		Yes:              opts.Yes,
		AdoptAs:          opts.AdoptAs,
		DryRun:           opts.DryRun,
		New:              opts.New,
		Rebuild:          opts.Rebuild,
	}
	res, err := shared.SyncVerb(ctx, req, token)
	if err != nil {
		return nil, dbSyncVerbTransport(err)
	}
	if verr := client.VerbError(res); verr != nil {
		return nil, dbSyncVerbRefusalFor(verb, opts.RemoteURL, verr)
	}
	return res, nil
}

// dbSyncVerbRefusal maps the owner's refusal code onto this CLI's exit codes,
// total over the closed set the owner can send: an unknown code is an internal
// failure rather than a silent success. The messages pass through untouched:
// fixed text, and every one of them names the fix.
func dbSyncVerbRefusal(verb string, err error) error {
	return dbSyncVerbRefusalFor(verb, "", err)
}

// remoteConflictNext is the command that points this machine at the remote
// --url named, the way out of a remote conflict: enable has no flag that
// re-points, so no --local.
func remoteConflictNext(conflict *relevosync.RemoteConflictError) string {
	url := "<url>"
	if conflict != nil && conflict.URL != "" {
		url = conflict.URL
	}
	return "relevo config set sync.remote_url " + url
}

// dbSyncVerbRefusalFor is dbSyncVerbRefusal for a caller whose --url a
// remote-conflict refusal's next command repeats.
func dbSyncVerbRefusalFor(verb, url string, err error) error {
	var refusal *client.VerbRefusal
	if !errors.As(err, &refusal) {
		return dbSyncClassify(err)
	}
	name := "relevo db sync " + verb
	switch refusal.Code {
	case wire.SyncCodeNoToken, wire.SyncCodeNoRemote, wire.SyncCodeNoR2, wire.SyncCodeOverQuota:
		return fail(codeUsage, "%s", refusal.Message)
	case wire.SyncCodeAlreadyEnabled:
		return failNext(codeRefused, "relevo db sync status", "%s", refusal.Message)
	case wire.SyncCodeAuthRefused:
		return fail(codeRemoteAuth, "%s", refusal.Message)
	case wire.SyncCodeRemoteConflict:
		return failNext(codeRefused, remoteConflictNext(&relevosync.RemoteConflictError{URL: url}), "%s", refusal.Message)
	case wire.SyncCodePreflightRefused,
		wire.SyncCodeRemoteUnreachable, wire.SyncCodeInvalid:
		return fail(codeRefused, "%s", refusal.Message)
	case wire.SyncCodeContended:
		// Nothing was written and the verb is idempotent over that, so the next
		// hint is the verb itself rather than a report.
		return failNext(codeRefused, "relevo db sync "+verb, "%s", refusal.Message)
	case wire.SyncCodeNoKDF:
		return failNext(codeRefused, "relevo db sync unlock --new", "%s", refusal.Message)
	case wire.SyncCodeWrongPassphrase:
		return fail(codeRefused, "%s", refusal.Message)
	case wire.SyncCodeMachineLocked, wire.SyncCodeEpochRace:
		// Both refusals name the act that fixes them in their own words, and
		// nothing was written either way.
		return fail(codeRefused, "%s", refusal.Message)
	case wire.SyncCodeBackingOff:
		// Nothing was written and the delay ends on its own, so the reader is
		// told that and given the verb that clears it now.
		return failNext(codeRefused, "relevo db sync retry",
			"%s; it ends on its own, or run `relevo db sync retry` now", refusal.Message)
	case wire.SyncCodeRemoteRefused, wire.SyncCodeRemoteSchemaMissing:
		// The remote refused the statement this machine's change set carried: the
		// reader is told the constraint rather than sent to `relevo bugreport`.
		return failNext(codeRefused, "relevo db sync status", "%s", refusal.Message)
	}
	_ = name
	return failWrap(codeInternal, err, "relevo db sync %s", verb)
}

// dbSyncVerbTransport classifies a verb that never reached a refusal. An owner
// without the verb names the upgrade and is refused rather than internal: the
// caller can act on it by upgrading the daemon.
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
// dials the owner and takes the machine-local file off the dialled handle.
// Status reads the three local rows itself because a verb would ask the daemon
// to change something in order to describe the machine, and nothing here starts
// a daemon: a status that had to bring one up would answer by changing the machine.
//
// The local handle is not optional. An owner that serves none is refused rather
// than answered from the shared file, which holds no sync section and no token,
// so a silent downgrade would report a configured machine as unconfigured.
func openDBSyncStatus() (shared *db.DB, local *db.DB, err error) {
	path := machineDBPath()
	if path == "" {
		return nil, nil, fail(codeRefused, "no state root: set XDG_STATE_HOME or HOME")
	}
	if !fileExists(path) {
		return nil, nil, fail(codeRefused, "no relevo.db at %s", path)
	}
	root, err := tmpdir.DefaultRoot()
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

// The CLI holds no remote open: the verb surface removes a direct open from a
// client process, so there is no second copy of the enable path's handle decision here.
