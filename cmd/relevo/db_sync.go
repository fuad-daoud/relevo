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
	"github.com/fuad-daoud/relevo/internal/db/wire/client"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// dbSyncUsage is what a bare `relevo db sync` prints. It names the states
// sync can be moved between plus the three one-shot calls, and each verb parses
// its own flags rather than leaving them to the dispatcher. The R2 flags, their
// line and their prose are in db_sync_r2.go, which is where they are installed.
const dbSyncUsage = "usage: relevo db sync enable [--url URL] [--token-stdin] [--passphrase-stdin]\n" +
	"       relevo db sync enable [--join-stdin] [--yes] [--timeout D] [--json]\n" +
	"       relevo db sync enable --as INSTALLATION --yes [--url URL] [--timeout D] [--json]\n" +
	dbSyncR2Usage +
	"       relevo db sync disable [--timeout D] [--json]\n" +
	"       relevo db sync retry [--rebuild] [--json]\n" +
	"       relevo db sync status [--json]\n" +
	"       relevo db sync push [--timeout D] [--json]\n" +
	"       relevo db sync pull [--timeout D] [--json]\n" +
	"       relevo db sync reconcile [--dry-run] [--timeout D] [--json]\n" +
	"       relevo db sync unlock [--new] [--passphrase-stdin] [--json]\n" +
	"       relevo db sync rekey [--passphrase-stdin] [--json]\n" +
	"       relevo db sync invite [--json]\n\n" +
	"enable joins this machine to the remote: it checks the origin gate, stores\n" +
	"the token and the remote, then imports the other origins and exports this\n" +
	"one's history before the mark goes on. An interrupted enable resumes.\n" +
	"push drains this machine's outbox into the log and pull reads the other\n" +
	"origins' entries and imports them; each is the half of the steady exchange\n" +
	"it names. retry clears a latched breaker and drops the worker, so the next\n" +
	"tick starts a fresh one. --rebuild moves a replica the worker cannot read\n" +
	"aside before the next attempt, and the next attempt bootstraps a fresh one\n" +
	"from the remote with the stored token and key. reconcile compares this\n" +
	"machine's rows against what the log holds for it and appends the difference,\n" +
	"deletes included: the repair for a log that missed this machine's writes.\n" +
	"--dry-run lists the same rows and appends nothing. unlock stores\n" +
	"the sync key derived from the passphrase; --new creates it on a remote that\n" +
	"holds none. The passphrase comes from --passphrase-stdin, else " + dbSyncPassphraseEnv + ",\n" +
	"else a prompt. rekey rotates the log's key to a new epoch: the machine must\n" +
	"be unlocked, the current passphrase is checked against the remote before\n" +
	"anything is written, and the new one comes from " + dbSyncNewPassphraseEnv + "\n" +
	"or a prompt asked twice -- stdin carries at most one secret, so it is the\n" +
	"current passphrase's route alone. invite prints one join code on stdout for\n" +
	"a new machine to join with; it carries the turso.token, so it goes to the\n" +
	"terminal and never to a log. A machine that is not syncing, or a remote\n" +
	"holding no key yet, is refused by name.\n" +
	"Writing the machine-local sync section by hand stays the advanced\n" +
	"route: `relevo config set sync '{\"remote_url\":\"...\"}'`.\n" +
	"--join-stdin reads the code `relevo db sync invite` printed from standard\n" +
	"input, never from the command line, where it would sit in shell history\n" +
	"and the process table. The code names the remote and the token, so neither\n" +
	"--url nor --token-stdin is needed and passing --url beside it is refused.\n" +
	"It shares standard input with no other secret. The passphrase is read\n" +
	"the same way unlock reads it and is checked against the remote before\n" +
	"anything is stored, so a wrong one leaves this machine as it was.\n" +
	"--token-stdin reads the turso.token from standard input and\n" +
	"beats " + relevosync.EnvToken + "; with neither, the stored token is used,\n" +
	"and enable refuses only if none is stored. The value appears nowhere else.\n" +
	"--as continues as an installation the remote already holds instead of\n" +
	"joining as a new one. It spends this machine's identity and cannot be\n" +
	"undone, so it needs --yes, and it never goes beside --join-stdin.\n" +
	dbSyncR2UsageText +
	"disable marks this machine off, forgets the token, stops the worker,\n" +
	"deletes relevo-sync.db and the driver's files beside it, and drops the\n" +
	"handle. Local files keep every row and stay servable, and the remote is\n" +
	"left alone.\n"

// The name the remote is told this client is called. It is fixed rather than
// configurable because it is identification, not a setting, and a per-machine
// value would make the remote's view of a fleet unreadable.
const dbSyncHandleName = "relevo"

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
	case len(args) > 0 && args[0] == "retry":
		return cmdDBSyncRetry(args[1:])
	case len(args) > 0 && args[0] == "status":
		return cmdDBSyncStatus(args[1:])
	case len(args) > 0 && args[0] == "push":
		return cmdDBSyncPush(args[1:])
	case len(args) > 0 && args[0] == "pull":
		return cmdDBSyncPull(args[1:])
	case len(args) > 0 && args[0] == "reconcile":
		return cmdDBSyncReconcile(args[1:])
	case len(args) > 0 && args[0] == "unlock":
		return cmdDBSyncUnlock(args[1:])
	case len(args) > 0 && args[0] == "rekey":
		return cmdDBSyncRekey(args[1:])
	case len(args) > 0 && args[0] == "invite":
		return cmdDBSyncInvite(args[1:])
	case len(args) > 0 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help"):
		fmt.Fprint(os.Stderr, dbSyncUsage)
		return errHelpShown
	}
	fmt.Fprint(os.Stderr, dbSyncUsage)
	return errUsagePrinted
}

// dbSyncEnableFlagValues holds the pointers `db sync enable` parses into. The
// R2 half is declared in db_sync_r2.go with the flags that install it.
type dbSyncEnableFlagValues struct {
	asJSON          *bool
	remoteURL       *string
	tokenStdin      *bool
	passphraseStdin *bool
	joinStdin       *bool
	timeout         *time.Duration
	yes             *bool
	adoptAs         *string
	dbSyncR2Flags
}

// dbSyncEnableFlagSet defines those flags on fs and returns what they parse
// into.
func dbSyncEnableFlagSet(fs *flag.FlagSet) *dbSyncEnableFlagValues {
	v := &dbSyncEnableFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the document the enable produced")
	v.remoteURL = fs.String("url", "", "the remote to sync with, stored in the machine-local sync section")
	v.tokenStdin = fs.Bool("token-stdin", false, "read the turso.token from standard input")
	v.passphraseStdin = fs.Bool("passphrase-stdin", false, "read the sync passphrase from standard input")
	v.joinStdin = fs.Bool("join-stdin", false, "read the join code `relevo db sync invite` printed from standard input, instead of naming the remote")
	v.timeout = fs.Duration("timeout", dbSyncDefaultTimeout, "bound the remote probe and the open")
	v.yes = fs.Bool("yes", false, "join even when the estimate exceeds what is left of this month's Turso quota")
	v.adoptAs = fs.String("as", "", "continue as this installation id instead of joining as a new one; needs --yes")
	installR2Flags(fs, &v.dbSyncR2Flags)
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
	v.timeout = fs.Duration("timeout", dbSyncDefaultTimeout, "bound the final export")
	return v
}

// dbSyncRetryFlagValues holds the pointers `db sync retry` parses into.
type dbSyncRetryFlagValues struct {
	asJSON  *bool
	rebuild *bool
}

// dbSyncRetryFlagSet defines those flags on fs and returns what they parse
// into.
func dbSyncRetryFlagSet(fs *flag.FlagSet) *dbSyncRetryFlagValues {
	v := &dbSyncRetryFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the document the retry produced")
	v.rebuild = fs.Bool("rebuild", false, "move the replica the worker cannot read aside before the next attempt")
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

// dbSyncOutcomeDoc is what enable and disable print under --json: what the run
// did, and for disable the warning a failed final export produces. RemoteURL is
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
	// Joining says the CLI's own bound ran out while the daemon kept joining,
	// so the enable exited without a refusal.
	Joining bool `json:"joining,omitempty"`
}

// dbSyncCallDoc is what push and pull print under --json, and retry with it:
// Aside is the folder a `retry --rebuild` moved the replica's copy into.
type dbSyncCallDoc struct {
	Applied bool     `json:"applied"`
	Holds   []string `json:"holds,omitempty"`
	Next    string   `json:"next,omitempty"`
	Aside   string   `json:"aside,omitempty"`
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
// is why this command needs no daemon stopped and no file lock of its own.
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

	if err := dbSyncEnableIntake(*v.tokenStdin, *v.passphraseStdin, *v.joinStdin, v.dbSyncR2Flags); err != nil {
		return err
	}

	// The code is parsed here, before anything is dialed. It is a pure function
	// over the string, so a code this build cannot read -- a version it does not
	// know, a truncated paste, something that is not a code at all -- is refused
	// before a daemon is reached or a worker is started, and the remote and
	// token it names are the ones that go on the wire.
	code, err := dbSyncReadJoinCode(*v.joinStdin)
	if err != nil {
		return err
	}
	join, err := dbSyncEnableJoin(code, *v.remoteURL)
	if err != nil {
		return err
	}
	token, err := dbSyncEnableToken(join, *v.tokenStdin)
	if err != nil {
		return err
	}
	pass, err := dbSyncEnablePassphrase(*v.passphraseStdin, join.joining)
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

	if err := dbSyncAdoptIntake(*v.adoptAs, *v.yes, join.joining); err != nil {
		return err
	}
	opts := dbSyncVerbOptions{RemoteURL: join.remote, Timeout: *v.timeout, Yes: *v.yes, Join: join.joining, AdoptAs: *v.adoptAs}
	tail, err := dbSyncEnableR2Tail(token, pass, v.dbSyncR2Flags, &opts)
	if err != nil {
		return err
	}
	res, err := sendSyncVerbToken(ctx, shared, wire.SyncVerbEnable, tail, opts)
	if err != nil {
		if !enableContinuesInDaemon(err) {
			return err
		}
		if *v.asJSON {
			return printDoc(dbSyncOutcomeDoc{Joining: true})
		}
		fmt.Println(enableContinuesLine)
		return nil
	}
	if *v.asJSON {
		return printDoc(dbSyncOutcomeDoc{
			Enabled:   true,
			RemoteURL: res.RemoteURL,
			Applied:   res.Applied,
		})
	}
	if res.Warning != "" {
		fmt.Fprintln(os.Stderr, "warning: "+res.Warning)
	}
	if *v.adoptAs != "" {
		fmt.Println(dbSyncAdoptedLine(*v.adoptAs))
		return nil
	}
	fmt.Println("sync enabled on this machine")
	return nil
}

// enableContinuesLine is what an enable prints when the client's own wait runs
// out before the daemon's reply. The join is not abandoned: the daemon performs
// it with its own handles and does not stop when this process gives up on the
// reply, so the line says where to watch it and how to resume it.
const enableContinuesLine = "sync enable: the join is still running in the daemon; `relevo db sync status` shows it"

// enableContinuesInDaemon reports whether a failed enable call is the client's
// own bound running out after the frame reached the daemon, rather than a
// request the daemon never received. A reply wait the caller's deadline ended
// is a join in progress: the daemon holds the verb and finishes it either way,
// and re-running enable resumes it if the daemon restarted. A deadline from the
// dial, the handshake, or a frame only partly written is a real failure -- no
// join was ever started, so there is nothing to report as still running.
func enableContinuesInDaemon(err error) bool {
	return errors.Is(err, client.ErrAwaitingReply) && errors.Is(err, context.DeadlineExceeded)
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
// Neither route yielding anything is not a refusal here but an empty answer: the
// daemon holds the stored token and falls back to it, so a tokenless enable
// resumes an interrupted join rather than raising a usage error, and a machine
// with nothing stored is refused by the daemon's own preflight. This process
// never says which route was tried or what either held.
func dbSyncEnableToken(join dbSyncJoin, fromStdin bool) ([]byte, error) {
	// A code carries the token, and it carries the only one: a joiner is not
	// handed this machine's environment, and a token from elsewhere would reach a
	// remote the code never named.
	if join.joining {
		return join.token, nil
	}
	intake := relevosync.TokenIntake{EnvValue: dbSyncGetenv(relevosync.EnvToken)}
	if fromStdin {
		token, err := io.ReadAll(dbSyncTokenStdin)
		if err != nil {
			return nil, failWrap(codeUsage, err, "relevo db sync enable --token-stdin")
		}
		intake.FromStdin, intake.Stdin = true, token
	}
	token, _, err := intake.Resolve()
	if errors.Is(err, relevosync.ErrNoToken) {
		// Neither route carried a token, and that is not this process's refusal
		// to make: the daemon falls back to the stored one exactly as it does for
		// the bucket secret. The tail goes out empty, and a machine with nothing
		// stored is refused there, by the preflight, naming both routes.
		return nil, nil
	}
	if err != nil {
		return nil, fail(codeUsage, "%v", err)
	}
	return token, nil
}

// cmdDBSyncDisable runs the turn-off through the daemon, in the order the
// contract fixes.
//
// No token is read here and none is passed: the daemon reads the stored one with
// its own local handle and decides whether there is anything a final export could
// do. A machine with no token or no remote pays no dial to find that out, and
// one whose remote will not open still turns sync off -- which is the whole
// reason the final export is best-effort.
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

// cmdDBSyncRetry clears a latched breaker through the daemon. It writes no
// remote and moves no change set, so it needs no token and no timeout: the work
// is the daemon's own local write, and the answer takes no network.
func cmdDBSyncRetry(args []string) error {
	fs := flag.NewFlagSet("db sync retry", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := dbSyncRetryFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fail(codeUsage, "relevo db sync retry takes no arguments, got %d", fs.NArg())
	}

	shared, err := dialSyncVerb()
	if err != nil {
		return err
	}
	defer func() { _ = shared.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), dbSyncDefaultTimeout+verbDialSlack)
	defer cancel()

	res, err := sendSyncVerb(ctx, shared, wire.SyncVerbRetry, dbSyncVerbOptions{Rebuild: *v.rebuild})
	if err != nil {
		return err
	}
	if *v.asJSON {
		return printDoc(dbSyncCallDoc{Applied: true, Aside: res.Aside})
	}
	if res.Aside != "" {
		fmt.Println(dbSyncRebuiltLine(res.Aside))
		return nil
	}
	fmt.Println("sync retry: the latch is cleared and the worker will restart")
	return nil
}

// dbSyncRebuiltLine is what a retry --rebuild prints: the latch is cleared and
// the copy's folder is named, so the user can go straight to the evidence the
// move kept.
func dbSyncRebuiltLine(aside string) string {
	return fmt.Sprintf("sync retry: the latch is cleared; the replica is kept in %s and the next attempt bootstraps a fresh one", aside)
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
	case errors.Is(err, relevosync.ErrNoRemote), errors.Is(err, relevosync.ErrNoR2),
		errors.Is(err, relevosync.ErrJoinOverQuota):
		return fail(codeUsage, "%v", err)
	case errors.Is(err, relevosync.ErrRemoteConflict):
		var conflict *relevosync.RemoteConflictError
		errors.As(err, &conflict)
		return failNext(codeRefused, remoteConflictNext(conflict), "%v", err)
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
