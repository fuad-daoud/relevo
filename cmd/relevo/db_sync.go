package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/db/wire/client"
	"github.com/fuad-daoud/relevo/internal/sanitize"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// dbSyncUsage is what a bare `relevo db sync` prints. It names the states
// sync can be moved between plus the three one-shot calls, and each verb parses
// its own flags rather than leaving them to the dispatcher. The R2 flags, their
// line and their prose are in db_sync_r2.go, which is where they are installed.
const dbSyncUsage = "usage: relevo db sync enable [--url URL] [--token-stdin] [--timeout D] [--json]\n" +
	dbSyncR2Usage +
	"       relevo db sync disable [--timeout D] [--json]\n" +
	"       relevo db sync retry [--json]\n" +
	"       relevo db sync status [--json]\n" +
	"       relevo db sync push [--timeout D] [--json]\n" +
	"       relevo db sync pull [--timeout D] [--json]\n\n" +
	"enable joins this machine to the remote: it checks the origin gate, stores\n" +
	"the token and the remote, then imports the other origins and exports this\n" +
	"one's history before the mark goes on. An interrupted enable resumes.\n" +
	"push drains this machine's outbox into the log and pull reads the other\n" +
	"origins' entries and imports them; each is the half of the steady exchange\n" +
	"it names. retry clears a latched breaker and drops the worker, so the next\n" +
	"tick starts a fresh one.\n" +
	"Writing the machine-local sync section by hand stays the advanced\n" +
	"route: `relevo config set sync '{\"remote_url\":\"...\"}'`.\n" +
	"--token-stdin reads the turso.token from standard input and\n" +
	"beats " + relevosync.EnvToken + "; with neither, enable refuses. The value\n" +
	"appears in no log, no error and no payload.\n" +
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
	asJSON     *bool
	remoteURL  *string
	tokenStdin *bool
	timeout    *time.Duration
	dbSyncR2Flags
}

// dbSyncEnableFlagSet defines those flags on fs and returns what they parse
// into.
func dbSyncEnableFlagSet(fs *flag.FlagSet) *dbSyncEnableFlagValues {
	v := &dbSyncEnableFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the document the enable produced")
	v.remoteURL = fs.String("url", "", "the remote to sync with, stored in the machine-local sync section")
	v.tokenStdin = fs.Bool("token-stdin", false, "read the turso.token from standard input")
	v.timeout = fs.Duration("timeout", dbSyncDefaultTimeout, "bound the remote probe and the open")
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
	asJSON *bool
}

// dbSyncRetryFlagSet defines that flag on fs and returns what it parses into.
func dbSyncRetryFlagSet(fs *flag.FlagSet) *dbSyncRetryFlagValues {
	v := &dbSyncRetryFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the document the retry produced")
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

// dbSyncStatusDoc is `db sync status --json`: what this machine is set to be,
// what its last exchange measured, and which origins need a human. TokenPresent
// is a bool because the value is the one thing on this surface that must never
// be printed. The R2 fields are omitempty for the same reason an unconfigured
// machine renders no R2 clause on the line: bodies in a bucket are opt-in, and a
// machine that has not turned them on is in the ordinary state.
type dbSyncStatusDoc struct {
	Enabled      bool   `json:"enabled"`
	RemoteURL    string `json:"remote_url,omitempty"`
	Namespace    string `json:"namespace,omitempty"`
	TokenPresent bool   `json:"token_present"`
	// R2Configured says whether this machine holds a complete set of bucket
	// credentials. R2Endpoint and R2Bucket describe where bodies would go, which
	// is what a reader needs to tell this machine's bucket from another's. There
	// is deliberately no field for the key id or the secret: this document is
	// printed on request and read aloud, and a credential has no reason to be in
	// either. See db_sync_r2.go, which is where the reader and renderer live.
	R2Configured bool   `json:"r2_configured,omitempty"`
	R2Endpoint   string `json:"r2_endpoint,omitempty"`
	R2Bucket     string `json:"r2_bucket,omitempty"`
	// Latched and LatchCause describe a breaker that stopped the machine until
	// `relevo db sync retry`.
	Latched    bool   `json:"latched,omitempty"`
	LatchCause string `json:"latch_cause,omitempty"`
	// Backlog is the count of this machine's operations the last attempt left
	// unsent.
	Backlog int64 `json:"backlog,omitempty"`
	// LastExport and LastImport are the UTC instants the last completed
	// exchange stamped, empty when it never completed one.
	LastExport string `json:"last_export,omitempty"`
	LastImport string `json:"last_import,omitempty"`
	// HeldOrigins, Dropped and Gaps are the last import's report: an origin a
	// newer writer held, a batch a refusal dropped, a sequence gap a hole left.
	HeldOrigins []string `json:"held_origins,omitempty"`
	Dropped     []string `json:"dropped,omitempty"`
	Gaps        []string `json:"gaps,omitempty"`
	// Joining says a join is in progress and JoinSince is when it began, read
	// from the machine-local join marker. The daemon finishes the join whether
	// the enable that started it waited or not, so a machine can be joining
	// while reading off.
	Joining   bool   `json:"joining,omitempty"`
	JoinSince string `json:"join_since,omitempty"`
	// LastAttempt is the daemon's last steady attempt, absent until it has run
	// one.
	LastAttempt *dbSyncAttemptDoc `json:"last_attempt,omitempty"`
	// Bytes is this month's traffic, absent until something moved, and
	// QuotaTursoSync the allowance the Turso total is read against.
	Bytes          *relevosync.ByteCounters `json:"bytes,omitempty"`
	QuotaTursoSync int64                    `json:"quota_turso_sync,omitempty"`
}

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

	token, err := dbSyncEnableToken(*v.tokenStdin)
	if err != nil {
		return err
	}

	if err := dbSyncEnableR2(*v.tokenStdin, v.dbSyncR2Flags); err != nil {
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

	if _, err := sendSyncVerb(ctx, shared, wire.SyncVerbRetry, dbSyncVerbOptions{}); err != nil {
		return err
	}
	if *v.asJSON {
		return printDoc(dbSyncCallDoc{Applied: true})
	}
	fmt.Println("sync retry: the latch is cleared and the worker will restart")
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
	line := fmt.Sprintf("sync %s (remote: %s, token: %s)", state, orNone(doc.RemoteURL), presentOrAbsent(doc.TokenPresent))
	if r2 := dbSyncR2Line(doc); r2 != "" {
		line += ", r2: " + r2
	}
	return line + dbSyncStatusDetails(doc) + "\n"
}

// dbSyncStatusDetails is the tail of the status line: each fact a reader can act
// on, in a fixed order and only when it is set. A latch comes first because it
// is the one state that stops the machine until a human acts; the rest is what
// the last exchange measured and what it could not apply.
func dbSyncStatusDetails(doc dbSyncStatusDoc) string {
	var b strings.Builder
	if doc.Joining {
		fmt.Fprintf(&b, " · joining since %s", doc.JoinSince)
	}
	if doc.Latched {
		fmt.Fprintf(&b, " · latched: %s", doc.LatchCause)
	}
	if doc.Backlog > 0 {
		fmt.Fprintf(&b, " · backlog: %d", doc.Backlog)
	}
	if doc.LastExport != "" {
		fmt.Fprintf(&b, " · exported: %s", doc.LastExport)
	}
	if doc.LastImport != "" {
		fmt.Fprintf(&b, " · imported: %s", doc.LastImport)
	}
	for _, held := range doc.HeldOrigins {
		fmt.Fprintf(&b, " · held: %s", sanitize.Text(held))
	}
	for _, dropped := range doc.Dropped {
		fmt.Fprintf(&b, " · dropped: %s", sanitize.Text(dropped))
	}
	for _, gap := range doc.Gaps {
		fmt.Fprintf(&b, " · gap: %s", sanitize.Text(gap))
	}
	if doc.Bytes != nil {
		fmt.Fprintf(&b, " · %s", relevosync.FormatBytes(*doc.Bytes, doc.QuotaTursoSync))
	}
	b.WriteString(dbSyncAttemptDetails(doc.LastAttempt))
	return b.String()
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
	case errors.Is(err, relevosync.ErrNoRemote), errors.Is(err, relevosync.ErrNoR2):
		return fail(codeUsage, "%v", err)
	case errors.Is(err, relevosync.ErrRemoteConflict):
		return fail(codeRefused, "%v", err)
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
