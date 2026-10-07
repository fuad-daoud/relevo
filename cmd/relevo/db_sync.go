package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// dbSyncUsage is what a bare `relevo db sync` prints. The verbs are the four
// states sync can be moved between plus the two one-shot calls; every one of
// them writes machine-local rows, so none of them takes a flag the dispatcher
// itself would parse.
const dbSyncUsage = "usage: relevo db sync enable [--url URL] [--token-stdin] [--timeout D] [--json]\n" +
	"       relevo db sync disable [--timeout D] [--json]\n" +
	"       relevo db sync status [--json]\n" +
	"       relevo db sync push [--timeout D] [--json]\n" +
	"       relevo db sync pull [--timeout D] [--json]\n\n" +
	"enable, push and pull refuse in this build: there is no sync engine behind\n" +
	"them, and they would rather say so than open something. disable still runs\n" +
	"whole, and status still reports what this machine is set to be.\n" +
	"--url names the remote and stores it in the machine-local sync\n" +
	"section; a stored remote that --url contradicts refuses, and with neither\n" +
	"enable refuses. Writing the section by hand stays the advanced\n" +
	"route: `relevo config set sync '{\"remote_url\":\"...\"}'`.\n" +
	"--token-stdin reads the turso.token from standard input and\n" +
	"beats " + relevosync.EnvToken + "; with neither, enable refuses. The value is\n" +
	"stored in the machine-local file and appears in no log, no error and no\n" +
	"payload.\n" +
	"disable marks this machine off, forgets the token and closes the handle.\n" +
	"Local files keep every row and stay servable, and the remote is left alone.\n"

// The client name the remote is told this client is called. It is fixed rather
// than configurable because it is identification, not a setting, and a
// per-machine value would make the remote's view of a fleet unreadable.
const dbSyncClientName = "relevo"

// The default bound on one sync verb. It is the runner's own bound: a machine
// that cannot reach its remote must be able to stop syncing, and to ask for a
// status, without either waiting on a network that is not answering.
const dbSyncDefaultTimeout = relevosync.DefaultTimeout

// dbSyncFlagSet declares the bare `db sync` dispatcher's flags: none. It is a
// dispatcher, and the registry lists it beside the other dispatchers with an
// empty flag set.
func dbSyncFlagSet(*flag.FlagSet) {}

// cmdDBSync dispatches `relevo db sync`'s verbs. A bare `relevo db sync` prints
// the usage line, because the verb itself has nothing to run.
func cmdDBSync(args []string) error {
	switch {
	case len(args) > 0 && args[0] == "enable":
		return cmdDBSyncEnable(args[1:])
	case len(args) > 0 && args[0] == "disable":
		return cmdDBSyncDisable(args[1:])
	case len(args) > 0 && args[0] == "status":
		return cmdDBSyncStatus(args[1:])
	case len(args) > 0 && args[0] == "push":
		return cmdDBSyncPush(args[1:])
	case len(args) > 0 && args[0] == "pull":
		return cmdDBSyncPull(args[1:])
	case len(args) > 0 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help"):
		fmt.Fprint(os.Stderr, dbSyncUsage)
		return errHelpShown
	}
	fmt.Fprint(os.Stderr, dbSyncUsage)
	return errUsagePrinted
}

// dbSyncEnableFlagValues holds the pointers `db sync enable` parses into.
type dbSyncEnableFlagValues struct {
	asJSON     *bool
	remoteURL  *string
	tokenStdin *bool
	timeout    *time.Duration
}

// dbSyncEnableFlagSet defines those flags on fs and returns what they parse
// into.
func dbSyncEnableFlagSet(fs *flag.FlagSet) *dbSyncEnableFlagValues {
	v := &dbSyncEnableFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the document the enable produced")
	v.remoteURL = fs.String("url", "", "the remote to sync with, stored in the machine-local sync section")
	v.tokenStdin = fs.Bool("token-stdin", false, "read the turso.token from standard input")
	v.timeout = fs.Duration("timeout", dbSyncDefaultTimeout, "bound the remote probe and the open")
	return v
}

// dbSyncDisableFlagValues holds the pointers `db sync disable` parses into.
type dbSyncDisableFlagValues struct {
	asJSON  *bool
	timeout *time.Duration
}

// dbSyncDisableFlagSet defines those flags on fs and returns what they parse
// into.
func dbSyncDisableFlagSet(fs *flag.FlagSet) *dbSyncDisableFlagValues {
	v := &dbSyncDisableFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the document the disable produced")
	v.timeout = fs.Duration("timeout", dbSyncDefaultTimeout, "bound the final push")
	return v
}

// dbSyncStatusFlagValues holds the pointers `db sync status` parses into.
type dbSyncStatusFlagValues struct {
	asJSON *bool
}

// dbSyncStatusFlagSet defines that flag on fs and returns what it parses into.
func dbSyncStatusFlagSet(fs *flag.FlagSet) *dbSyncStatusFlagValues {
	v := &dbSyncStatusFlagValues{}
	v.asJSON = fs.Bool("json", false, "print this machine's sync state as a JSON document")
	return v
}

// dbSyncCallFlagValues is what push and pull share: the document flag and the
// bound. Two verbs with the same two flags get one value struct rather than two
// that can drift apart.
type dbSyncCallFlagValues struct {
	asJSON  *bool
	timeout *time.Duration
}

// dbSyncPushFlagSet and dbSyncPullFlagSet are the two installers the registry
// walks. They declare the same two flags under the name each verb's help says.
func dbSyncPushFlagSet(fs *flag.FlagSet) *dbSyncCallFlagValues {
	v := &dbSyncCallFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the document the push produced")
	v.timeout = fs.Duration("timeout", dbSyncDefaultTimeout, "bound the push")
	return v
}

func dbSyncPullFlagSet(fs *flag.FlagSet) *dbSyncCallFlagValues {
	v := &dbSyncCallFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the document the pull produced")
	v.timeout = fs.Duration("timeout", dbSyncDefaultTimeout, "bound the pull")
	return v
}

// dbSyncTokenStdin is the reader --token-stdin reads. It is a var so a test can
// hand the verb a token without a pipe, and so nothing here reads the caller's
// real stdin by accident.
var dbSyncTokenStdin io.Reader = os.Stdin

// dbSyncGetenv is the environment lookup the env route uses. It is a var for
// the same reason, and it is the only place the environment is read.
var dbSyncGetenv = os.Getenv

// dbSyncStatusDoc is `db sync status --json`: what this machine is set to be.
// TokenPresent is a bool because the value is the one thing on this surface that
// must never be printed.
type dbSyncStatusDoc struct {
	Enabled      bool   `json:"enabled"`
	RemoteURL    string `json:"remote_url,omitempty"`
	Namespace    string `json:"namespace,omitempty"`
	TokenPresent bool   `json:"token_present"`
}

// dbSyncOutcomeDoc is what enable and disable print under --json: what the run
// did, and for disable the warning a failed final push produces. RemoteURL is
// what enable stored, and never the token: the URL is the thing a caller needs
// to open the same remote, and the token is the one value on this surface that
// must not be printed.
type dbSyncOutcomeDoc struct {
	Enabled   bool     `json:"enabled"`
	RemoteURL string   `json:"remote_url,omitempty"`
	Applied   bool     `json:"applied,omitempty"`
	Steps     []string `json:"steps,omitempty"`
	FinalPush bool     `json:"final_push"`
	Warning   string   `json:"warning,omitempty"`
}

// dbSyncCallDoc is what push and pull print under --json.
type dbSyncCallDoc struct {
	Applied bool `json:"applied"`
}

// cmdDBSyncEnable runs the enable path through the daemon.
//
// The token is resolved here and nowhere else: --token-stdin beats
// TURSO_TOKEN, and the value is handed straight to the verb request. It is not
// stored, logged, formatted into an error or echoed: the daemon puts it into the
// local file with its own handles, and the only thing this process ever holds
// it for is the length of the call below.
//
// Everything else -- the already-on read, the preflight, the seed decision, the
// mark and the open -- happens on the daemon's side with its own handles, which
// is why enable no longer needs the daemon stopped.
func cmdDBSyncEnable(args []string) error {
	fs := flag.NewFlagSet("db sync enable", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := dbSyncEnableFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fail(codeUsage, "relevo db sync enable takes no arguments, got %d", fs.NArg())
	}

	token, err := dbSyncEnableToken(*v.tokenStdin)
	if err != nil {
		return err
	}

	shared, err := dialSyncVerb()
	if err != nil {
		return err
	}
	defer func() { _ = shared.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), *v.timeout+verbDialSlack)
	defer cancel()

	res, err := sendSyncVerbToken(ctx, shared, wire.SyncVerbEnable, token, dbSyncVerbOptions{
		RemoteURL: *v.remoteURL,
		Timeout:   *v.timeout,
	})
	if err != nil {
		return err
	}
	if *v.asJSON {
		return printDoc(dbSyncOutcomeDoc{
			Enabled:   true,
			RemoteURL: res.RemoteURL,
			Applied:   res.Applied,
		})
	}
	fmt.Println("sync enabled on this machine")
	return nil
}

// verbDialSlack is what the local bound adds on top of the caller's timeout, so
// the daemon's own budget and this client's wait for it are both covered: the
// caller asked for the verb's work to take at most its timeout, and the process
// that carries it needs a little longer to hand the answer back.
const verbDialSlack = 5 * time.Second

// dbSyncEnableToken resolves the token from its two routes. The flag beats the
// environment, so a script that pipes a token cannot be overridden by whatever
// the environment happens to carry.
//
// Neither route yielding anything is ErrNoToken, and this is the one fixed line
// that says so: the routes are named, the attempt is not. Every line out of
// enable can end up in a log, so nothing here says which route was tried or what
// either of them held.
func dbSyncEnableToken(fromStdin bool) ([]byte, error) {
	intake := relevosync.TokenIntake{EnvValue: dbSyncGetenv(relevosync.EnvToken)}
	if fromStdin {
		token, err := io.ReadAll(dbSyncTokenStdin)
		if err != nil {
			return nil, failWrap(codeUsage, err, "relevo db sync enable --token-stdin")
		}
		intake.FromStdin, intake.Stdin = true, token
	}
	token, _, err := intake.Resolve()
	if err != nil {
		return nil, fail(codeUsage, "%v", err)
	}
	return token, nil
}

// cmdDBSyncDisable runs the turn-off through the daemon, in the order the
// contract fixes.
//
// No token is read here and none is passed: the daemon reads the stored one with
// its own local handle and decides whether there is anything a final push could
// do. A machine with no token or no remote pays no dial to find that out, and
// one whose remote will not open still turns sync off -- which is the whole
// reason the final push is best-effort.
func cmdDBSyncDisable(args []string) error {
	fs := flag.NewFlagSet("db sync disable", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := dbSyncDisableFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fail(codeUsage, "relevo db sync disable takes no arguments, got %d", fs.NArg())
	}

	shared, err := dialSyncVerb()
	if err != nil {
		return err
	}
	defer func() { _ = shared.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), *v.timeout+verbDialSlack)
	defer cancel()

	res, err := sendSyncVerb(ctx, shared, wire.SyncVerbDisable, dbSyncVerbOptions{Timeout: *v.timeout})
	if err != nil {
		return err
	}
	if *v.asJSON {
		return printDoc(dbSyncOutcomeDoc{
			Enabled:   false,
			Steps:     res.Steps,
			FinalPush: res.FinalPush,
			Warning:   res.Warning,
		})
	}
	if res.Warning != "" {
		fmt.Fprintf(os.Stderr, "relevo db sync disable: %s\n", res.Warning)
	}
	fmt.Println("sync disabled on this machine; local files unchanged and still servable")
	return nil
}

// cmdDBSyncStatus answers what this machine is set to be. It reads the local
// marks and nothing else, so it answers with the network blackholed and no
// handle open.
//
// It reaches them through the owner (openDBSyncStatus) and reads them itself
// rather than asking for a verb, and that is the whole of what S7 does not move:
// status is a description of the machine, so it stays a read. Every byte below is
// unchanged by that -- what it prints is a function of the document alone.
func cmdDBSyncStatus(args []string) error {
	fs := flag.NewFlagSet("db sync status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := dbSyncStatusFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fail(codeUsage, "relevo db sync status takes no arguments, got %d", fs.NArg())
	}

	shared, local, err := openDBSyncStatus()
	if err != nil {
		return err
	}
	defer func() { _ = shared.Close() }()

	settings, err := relevosync.ReadSettings(local)
	if err != nil {
		return failWrap(codeConfigInvalid, err, "relevo db sync status")
	}
	_, hasToken, err := relevosync.ReadToken(local)
	if err != nil {
		return failWrap(codeConfigInvalid, err, "relevo db sync status")
	}
	on, err := relevosync.Enabled(local)
	if err != nil {
		return dbSyncClassify(err)
	}

	doc := dbSyncStatusDoc{
		Enabled:      on,
		RemoteURL:    settings.RemoteURL,
		Namespace:    settings.Namespace,
		TokenPresent: hasToken,
	}
	if *v.asJSON {
		return printDoc(doc)
	}
	fmt.Print(dbSyncStatusLine(doc))
	return nil
}

// dbSyncStatusLine is the one line `db sync status` prints, as a function of the
// document and nothing else. Pulling it out of the verb is what lets the mapping
// be pinned by bytes: the route a row arrived over is then nowhere in the line,
// so a table of documents renders to the exact same table of strings whichever
// handle the rows came out of.
func dbSyncStatusLine(doc dbSyncStatusDoc) string {
	state := "off"
	if doc.Enabled {
		state = "on"
	}
	return fmt.Sprintf("sync %s (remote: %s, token: %s)\n", state, orNone(doc.RemoteURL), presentOrAbsent(doc.TokenPresent))
}

// dbSyncClassify maps a failure out of the sync package onto the frame's codes.
// The refusals that name a fix are refused; a driver failure the user cannot fix
// by reading the message is an internal failure with the bugreport command, and
// an authorisation the remote refused is the one failure with its own code.
//
// A failure that is already coded is passed through untouched. Re-wrapping one
// would replace the code a caller chose with internal and append the original
// text a second time, so a refusal raised two layers down would reach the user
// as an internal failure with its own message quoted inside it.
func dbSyncClassify(err error) error {
	var coded *cliError
	if errors.As(err, &coded) {
		return err
	}
	switch {
	case errors.Is(err, relevosync.ErrAlreadyEnabled):
		return failNext(codeRefused, "relevo db sync status", "%v", err)
	case errors.Is(err, relevosync.ErrNoToken):
		return fail(codeUsage, "%v", err)
	case errors.Is(err, relevosync.ErrNoRemote):
		return fail(codeUsage, "%v", err)
	case errors.Is(err, relevosync.ErrRemoteConflict):
		return fail(codeRefused, "%v", err)
	case errors.Is(err, relevosync.ErrSeedUploadRequired):
		return failNext(codeRefused, "relevo db sync status", "%v", err)
	case errors.Is(err, relevosync.ErrAuthRefused):
		return fail(codeRemoteAuth, "%v", err)
	case errors.Is(err, db.ErrPreflightRefused):
		// The preflight's refusals are the answer, not a failure: nothing broke,
		// and every message the joined error carries names the command or the
		// flag that clears it. A bug report has no fix in it, so this code must
		// never be the one the catalog points at `relevo bugreport` from.
		return failWrap(codeRefused, err, "relevo db sync enable")
	case errors.Is(err, db.ErrContended):
		// A busy database is a refusal the reader can act on: nothing was
		// written, the hold was another writer, and the verb may be run again.
		return failWrap(codeRefused, err, "relevo db sync")
	case errors.Is(err, db.ErrInvalid), errors.Is(err, db.ErrLocked):
		return failWrap(codeRefused, err, "relevo db sync")
	}
	return failWrap(codeInternal, err, "relevo db sync")
}

// orNone renders an empty remote as something a reader can tell from a URL.
func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// presentOrAbsent renders a token's presence without rendering the token.
func presentOrAbsent(present bool) string {
	if present {
		return "present"
	}
	return "absent"
}
