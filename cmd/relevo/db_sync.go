package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// dbSyncUsage is what a bare `relevo db sync` prints. The verbs are the four
// states sync can be moved between plus the two one-shot calls; every one of
// them writes machine-local rows, so none of them takes a flag the dispatcher
// itself would parse.
const dbSyncUsage = "usage: relevo db sync enable [--token-stdin] [--seed-uploaded] [--timeout D] [--json]\n" +
	"       relevo db sync disable [--timeout D] [--json]\n" +
	"       relevo db sync status [--json]\n" +
	"       relevo db sync push [--timeout D] [--json]\n" +
	"       relevo db sync pull [--timeout D] [--json]\n\n" +
	"enable runs the checks, decides how the remote is seeded and marks this\n" +
	"machine on. --token-stdin reads the turso.token from standard input and\n" +
	"beats " + relevosync.EnvToken + "; with neither, enable refuses. The value is\n" +
	"stored in the machine-local file and appears in no log, no error and no\n" +
	"payload. --seed-uploaded says the documented turso db import already ran\n" +
	"against the seed copy a previous enable named.\n" +
	"disable makes one last bounded push attempt, marks this machine off,\n" +
	"forgets the token and closes the handle. Local files keep every row and\n" +
	"stay servable, and the remote is left alone.\n"

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
	asJSON       *bool
	tokenStdin   *bool
	seedUploaded *bool
	timeout      *time.Duration
}

// dbSyncEnableFlagSet defines those flags on fs and returns what they parse
// into.
func dbSyncEnableFlagSet(fs *flag.FlagSet) *dbSyncEnableFlagValues {
	v := &dbSyncEnableFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the document the enable produced")
	v.tokenStdin = fs.Bool("token-stdin", false, "read the turso.token from standard input")
	v.seedUploaded = fs.Bool("seed-uploaded", false, "the documented turso db import already ran against the seed copy a previous enable named")
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
// did, and for disable the warning a failed final push produces. It carries the
// seed case by name rather than as a number so an agent reading it does not have
// to know the order the cases are declared in.
type dbSyncOutcomeDoc struct {
	Enabled   bool     `json:"enabled"`
	SeedCase  string   `json:"seed_case,omitempty"`
	Seed      string   `json:"seed,omitempty"`
	Applied   bool     `json:"applied,omitempty"`
	Steps     []string `json:"steps,omitempty"`
	FinalPush bool     `json:"final_push"`
	Warning   string   `json:"warning,omitempty"`
}

// dbSyncCallDoc is what push and pull print under --json.
type dbSyncCallDoc struct {
	Applied bool `json:"applied"`
}

// cmdDBSyncEnable runs the enable path: the token from its two routes, the
// checks, the seed decision, the mark, and the open.
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

	shared, local, err := openDBSync()
	if err != nil {
		return err
	}
	defer func() { _ = shared.Close() }()

	settings, err := relevosync.ReadSettings(local)
	if err != nil {
		return failWrap(codeConfigInvalid, err, "relevo db sync enable")
	}

	intake := relevosync.TokenIntake{EnvValue: dbSyncGetenv(relevosync.EnvToken)}
	if *v.tokenStdin {
		token, err := io.ReadAll(dbSyncTokenStdin)
		if err != nil {
			return failWrap(codeUsage, err, "relevo db sync enable --token-stdin")
		}
		intake.FromStdin, intake.Stdin = true, token
	}

	enabler := &relevosync.Enabler{
		Local:           local,
		Preflight:       func() db.Preflight { return db.EnablePreflight(shared) },
		LocalHasHistory: func() (bool, error) { return db.HasSharedHistory(shared) },
		CloudEmpty: func(ctx context.Context, token []byte) (bool, error) {
			return relevosyncCloudEmpty(ctx, settings, token)
		},
		SeedCopy:     func(path string) error { return shared.SeedCopy(path) },
		SeedPath:     dbSyncSeedPath(),
		Open:         dbSyncOpener(settings),
		Intake:       intake,
		SeedUploaded: *v.seedUploaded,
		Timeout:      *v.timeout,
	}

	res, err := enabler.Enable(context.Background())
	if err != nil {
		return dbSyncClassify(err)
	}
	if *v.asJSON {
		return printDoc(dbSyncOutcomeDoc{
			Enabled:  true,
			SeedCase: string(res.Case),
			Seed:     res.Seed,
			Applied:  res.Applied,
		})
	}
	fmt.Printf("sync enabled on this machine (seed: %s)\n", res.Case)
	return nil
}

// cmdDBSyncDisable runs the turn-off in the order the contract fixes.
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

	shared, local, err := openDBSync()
	if err != nil {
		return err
	}
	defer func() { _ = shared.Close() }()

	settings, err := relevosync.ReadSettings(local)
	if err != nil {
		return failWrap(codeConfigInvalid, err, "relevo db sync disable")
	}
	token, hasToken, err := relevosync.ReadToken(local)
	if err != nil {
		return failWrap(codeConfigInvalid, err, "relevo db sync disable")
	}

	disabler := &relevosync.Disabler{Local: local, Timeout: *v.timeout}
	if hasToken && settings.RemoteURL != "" {
		// The handle is opened only when there is something to push through and
		// something to push with. A machine with no token or no remote has
		// nothing a final push could do, so the turn-off does not open one and
		// does not pay a dial to find out.
		handle, oerr := relevosync.OpenRemote(context.Background(), dbSyncOpenConfig(settings, local, token, true))
		if oerr != nil {
			// A remote that will not open is the strongest reason of all to stop
			// syncing, so the turn-off continues without a handle rather than
			// refusing and leaving the machine pushing at a dead remote.
			fmt.Fprintf(os.Stderr, "relevo db sync disable: the final push will be skipped: %v\n", oerr)
		} else {
			disabler.Client = handle
			disabler.Close = func() error { return nil }
		}
	}

	res, err := disabler.Disable(context.Background())
	if err != nil {
		return dbSyncClassify(err)
	}
	warning := ""
	if res.FinalPushErr != nil {
		warning = res.FinalPushErr.Error()
	}
	if *v.asJSON {
		return printDoc(dbSyncOutcomeDoc{
			Enabled:   false,
			Steps:     res.Steps,
			FinalPush: res.FinalPush,
			Warning:   warning,
		})
	}
	if warning != "" {
		fmt.Fprintf(os.Stderr, "relevo db sync disable: %s\n", warning)
	}
	fmt.Println("sync disabled on this machine; local files unchanged and still servable")
	return nil
}

// cmdDBSyncStatus answers what this machine is set to be. It reads the local
// marks and nothing else, so it answers with the network blackholed and no
// handle open.
//
// It reaches them through the owner (openDBSyncStatus) rather than through
// openDBSync, and the difference is the point of the verb: a status that opened
// the file directly could only answer while the daemon was stopped, so the one
// question a machine could always ask about itself was the one question a
// running daemon refused to answer. Every byte below is unchanged by the route:
// what it prints is a function of the document alone.
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

// dbSyncSeedPath is where the seed copy an existing-history enable writes lands:
// beside the database it is a copy of, in a directory only this user reaches. The
// name is fixed so a refusal that names it names the same file every time.
func dbSyncSeedPath() string {
	return filepath.Join(filepath.Dir(machineDBPath()), "relevo-seed.db")
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
