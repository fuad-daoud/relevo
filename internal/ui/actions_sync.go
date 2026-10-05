package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/db/wire/client"
	"github.com/fuad-daoud/relevo/internal/relevo"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// SyncSnapshot is the whole read side of the :sync view: what the machine is
// set to be, what the remote last reported, and which rows are waiting on a
// human. Every field is a value already read; nothing here can open a handle.
//
// The zero Snapshot is what a view renders when no read has answered yet, and
// it is also what a read that found nothing describes: sync off, no remote, no
// token, no measurement, one installation (this one) and nothing unlinked.
type SyncSnapshot struct {
	// State is the machine-local state the statusline token is derived from,
	// and Token is that derivation itself -- the view renders S2's mapping and
	// does not repeat its order.
	State relevosync.State
	Token string
	// Settings is the machine-local sync section: where the remote is and how
	// eager this machine is.
	Settings relevosync.Settings
	// TokenSet is whether a token is stored. The value is never here.
	TokenSet bool
	// Stats is what the remote last reported, and Measured says whether a tick
	// has ever recorded it. A machine that has measured nothing shows
	// placeholders rather than zeros, because a zero backlog and no backlog
	// read identically and mean opposite things.
	Stats    relevosync.Stats
	Measured bool
	// Attention is the message the attention marker carries, when one does.
	Attention string
	// RemoteURL is the configured remote, empty when this machine has none.
	RemoteURL string
	// Installation is the directory: one row per installation that has written
	// here, and OwnID is this machine's own id, so a row that is not this
	// machine's can be shown as read-only attribution.
	Installation []db.Installation
	OwnID        string
	// Unlinked is every remote binding row carrying no link, listed for
	// re-bind. Nothing backfills them: they are here until someone binds them.
	Unlinked []db.Record
	// SharedBytes is the size of the shared file on disk, which is what the
	// first-upload estimate is a multiple of.
	SharedBytes int64
	// Reachable is whether the last test connection reached the remote, and
	// Unknown says no test has run yet. The two are separate because "never
	// tried" and "tried and failed" are different states and one chip cannot
	// say both.
	Reachable bool
	Unknown   bool
}

// syncClientName is the client name this machine's sync handle registers under,
// the same string `relevo db sync` passes. It is fixed rather than derived from
// the host so a push from the cockpit and a push from the CLI are one client as
// far as the remote is concerned.
const syncClientName = "relevo"

// syncHandle is the handle every cockpit sync action opens. It is the only
// value in this file that can reach a network, which is what lets SyncSnapshot
// and every Body below be written as though no network existed.
type syncHandle struct {
	Client relevosync.SyncClient
	local  relevosync.Local
	path   string
}

// ready is the one guard every action shares: a nil adapter is not the only way
// to have no runtime, because the holder itself is nil until the first load. A
// cockpit that has not started yet must get an error rather than a panic -- a
// screen that can take the process down is worse than one that says nothing.
func (a *mastermindActions) ready() error {
	switch {
	case a == nil:
		return errors.New("sync: no actions")
	case a.live == nil:
		return errors.New("sync: no runtime")
	case a.runtime().DB == nil:
		return errors.New("sync: no database")
	}
	return nil
}

// syncCtx bounds one call under the seam's own context and returns the cancel
// with it. The bound is the package default rather than a shorter cockpit one:
// what it buys is that a blackholed network costs one wait instead of a hung
// view, and a view that waited longer than the CLI would be waiting for nothing
// extra. A caller that already supplied a deadline keeps it.
func syncCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, relevosync.DefaultTimeout)
}

// openSync opens the handle the machine-local settings and token name, or says
// which of the two is missing. A push, a pull or a test with no remote or no
// token has nothing to reach and nothing to authenticate with, and naming the
// missing one is the whole difference between a refusal a user can act on and
// one they have to guess at.
func (a *mastermindActions) openSync() (syncHandle, error) {
	var h syncHandle
	if err := a.ready(); err != nil {
		return h, err
	}
	rt := a.runtime()
	local, err := relevosync.LocalHandle(rt.DB)
	if err != nil {
		return h, err
	}
	settings, err := relevosync.ReadSettings(local)
	if err != nil {
		return h, err
	}
	token, ok, err := relevosync.ReadToken(local)
	if err != nil {
		return h, err
	}
	if settings.RemoteURL == "" || !ok {
		return h, missingSyncRemotes(settings.RemoteURL, ok)
	}

	openCtx, cancel := syncCtx(context.Background())
	client, err := relevosync.OpenRemote(openCtx, relevosync.OpenConfig{
		// A connection test asks the remote a question; it never creates sync
		// membership. A machine whose file is not already a member refuses here,
		// which is the right answer for a test: it reports the machine is not
		// syncing rather than joining it to a remote as a side effect of asking.
		Role:             relevosync.OpenMember,
		Path:             rt.DB.Path(),
		RemoteURL:        settings.RemoteURL,
		Namespace:        settings.Namespace,
		ClientName:       syncClientName,
		AuthToken:        token,
		BootstrapIfEmpty: false,
	})
	cancel()
	if err != nil {
		return h, err
	}
	return syncHandle{Client: client, local: local, path: rt.DB.Path()}, nil
}

// missingSyncRemotes says which half of the pair is absent, so one refusal
// covers both cases with the right noun.
func missingSyncRemotes(remoteURL string, hasToken bool) error {
	switch {
	case remoteURL == "" && !hasToken:
		return fmt.Errorf("sync: no remote and no %s on this machine", relevosync.SecretToken)
	case remoteURL == "":
		return fmt.Errorf("sync: no remote is configured on this machine; set the sync section's remote_url")
	default:
		return fmt.Errorf("sync: no %s on this machine", relevosync.SecretToken)
	}
}

// verbRunner is the executor every cockpit sync action drives. It is the same
// value the daemon installs as the owner's OnSyncVerb hook, built the same way
// over the same handles, so the cockpit and `relevo db sync` cannot drift into
// two different answers for the same verb -- there is one set of verbs'
// semantics in the tree and both callers reach it.
//
// There is no Serialize guard here: the cockpit is a client of the same machine
// the daemon serves, so a verb it asks for is serialized daemon-side, and a
// second guard in this process would only queue work against itself.
func (a *mastermindActions) verbRunner() (*relevo.VerbRunner, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	shared := a.runtime().DB
	local, err := relevosync.LocalHandle(shared)
	if err != nil {
		return nil, err
	}
	return &relevo.VerbRunner{
		Shared:     shared,
		Local:      local,
		Path:       shared.Path(),
		Runner:     a.runtime().Sync,
		ClientName: syncClientName,
	}, nil
}

// runVerb drives one verb and turns a refusal into the action's own error shape.
func (a *mastermindActions) runVerb(ctx context.Context, runner *relevo.VerbRunner, verb string) (*wire.SyncResult, Result) {
	ctx, cancel := syncCtx(ctx)
	defer cancel()
	res := runner.Run(ctx, &wire.SyncVerb{Header: wire.Header{Type: wire.TypeSyncVerb}, Verb: verb}, nil)
	if err := client.VerbError(res); err != nil {
		return nil, syncErrResult(verb, err)
	}
	return res, Result{}
}

// SyncPush sends this machine's local change set. The result re-reads, so the
// view shows what the remote measured rather than what the push assumed.
func (a *mastermindActions) SyncPush(ctx context.Context) Result {
	runner, err := a.verbRunner()
	if err != nil {
		return syncErrResult("push", err)
	}
	_, bad := a.runVerb(ctx, runner, wire.SyncVerbPush)
	if bad.Err != nil {
		return bad
	}
	return Result{Text: "pushed this machine's change set", Refresh: true}
}

// SyncPull fetches the remote's changes and rebases the local ones on top. The
// applied flag becomes words, because "nothing to apply" is the answer a user
// needs and a boolean is not one.
func (a *mastermindActions) SyncPull(ctx context.Context) Result {
	runner, err := a.verbRunner()
	if err != nil {
		return syncErrResult("pull", err)
	}
	res, bad := a.runVerb(ctx, runner, wire.SyncVerbPull)
	if bad.Err != nil {
		return bad
	}
	if !res.Applied {
		return Result{Text: "pulled: the remote had nothing to apply", Refresh: true}
	}
	return Result{Text: "pulled and applied the remote's changes", Refresh: true}
}

// SyncTest opens a handle and asks the remote for its stats. It changes no byte
// here, so a test is safe against a remote a user only wants to ask about.
//
// It is the one sync action that does not go through the executor, and that is
// because it is not a verb: it asks the remote a question and reports the
// answer, rather than moving a change set or turning sync on or off. The other
// three drive the executor so there is one set of verbs' semantics.
func (a *mastermindActions) SyncTest(ctx context.Context) Result {
	h, err := a.openSync()
	if err != nil {
		return syncErrResult("test connection", err)
	}
	ctx, cancel := syncCtx(ctx)
	defer cancel()
	stats, err := h.Client.Stats(ctx)
	if err != nil {
		return syncErrResult("test connection", err)
	}
	return Result{
		Text:    fmt.Sprintf("reachable · %d unpushed · revision %s", stats.CdcOperations, syncOr(stats.Revision, syncDash)),
		Refresh: true,
	}
}

// SyncDisable runs S4's turn-off whole: the same four steps, in the order its
// contract fixes, through the same executor the CLI verb drives. The final push
// stays best-effort, so a remote that cannot be reached is a warning on a
// successful turn-off rather than a refusal to leave.
func (a *mastermindActions) SyncDisable(ctx context.Context) Result {
	runner, err := a.verbRunner()
	if err != nil {
		return syncErrResult("disable", err)
	}
	res, bad := a.runVerb(ctx, runner, wire.SyncVerbDisable)
	if bad.Err != nil {
		return bad
	}
	text := "sync off on this machine; local files unchanged and still servable"
	if res.Warning != "" {
		text += " · the final push was skipped: " + res.Warning
	}
	return Result{Text: text, Refresh: true}
}

// SyncSnapshot assembles the view's whole read side, off the update loop and
// with no handle in reach. A machine with no local file, no remote or no
// measurement answers with the fields it does have rather than an error, so the
// pre-on form renders on a machine that has never enabled sync.
func (a *mastermindActions) SyncSnapshot() (SyncSnapshot, error) {
	var snap SyncSnapshot
	if err := a.ready(); err != nil {
		return snap, err
	}
	shared := a.runtime().DB

	snap.OwnID = shared.Origin()
	// Reachable is not read here and is not assumed either: no test has run in
	// this process, which is its own state and says so rather than claiming a
	// remote is up or down on no evidence.
	snap.Unknown = true

	// The shared-directory reads come first. They name this machine and its rows,
	// and they stay true whether or not sync was ever configured.
	if rows, err := shared.InstallationList(); err == nil {
		snap.Installation = rows
	}
	if rows, err := shared.RecordListUnlinked(); err == nil {
		snap.Unlinked = rows
	}
	if fi, err := os.Stat(shared.Path()); err == nil {
		snap.SharedBytes = fi.Size()
	}

	local, err := relevosync.LocalHandle(shared)
	if err != nil {
		// No local file beside this handle means there is no row sync could own.
		// Everything above is still true, so the view renders it and reports
		// sync off rather than failing the whole screen over a file.
		snap.Token = relevosync.TokenOff
		return snap, nil
	}
	if settings, err := relevosync.ReadSettings(local); err == nil {
		snap.Settings, snap.RemoteURL = settings, settings.RemoteURL
	}
	if _, ok, err := relevosync.ReadToken(local); err == nil {
		snap.TokenSet = ok
	}
	if state, err := relevosync.ReadState(local); err == nil {
		snap.State, snap.Token = state, relevosync.Token(state)
	} else {
		// A marker that will not parse still has a token, and the token says
		// off: the mark that turns it on did not read, so nothing has enabled it.
		snap.Token = relevosync.TokenOff
	}
	if msg, err := relevosync.ReadAttention(local); err == nil {
		snap.Attention = msg
	}
	if stats, measured, err := relevosync.ReadSnapshot(local); err == nil {
		snap.Stats, snap.Measured = stats, measured
	}
	return snap, nil
}

// SyncEditor is the user's editor on the sync section's own file, returned
// rather than run so the shell can run it under tea.ExecProcess. The file is
// the machine-local section's source when there is one, so the edit a user
// makes by hand is an edit of the same bytes the section is read from.
func (a *mastermindActions) SyncEditor() (*exec.Cmd, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	local, err := relevosync.LocalHandle(a.runtime().DB)
	if err != nil {
		return nil, err
	}
	body, _, err := local.ConfigGet(relevosync.SectionSettings)
	if err != nil {
		return nil, err
	}
	return syncEditorCmd(local, body)
}

// syncEditorCmd writes the section to a file only this user can read and hands
// that file to the user's editor. A file rather than a pipe is the whole
// contract: an editor that saves must have something to save into, and a
// temporary file that outlives the process would then be the section.
func syncEditorCmd(local relevosync.Local, body []byte) (*exec.Cmd, error) {
	dir, err := os.MkdirTemp("", "relevo-sync-")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, relevosync.SectionSettings+".json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return nil, err
	}
	return editorCommand(path), nil
}

// editorCommand is the editor itself: $VISUAL, then $EDITOR, then vi, exactly
// the order every other editor this cockpit opens uses.
func editorCommand(path string) *exec.Cmd {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}
	argv := append(strings.Fields(editor), path)
	return exec.Command(argv[0], argv[1:]...)
}

// syncErrResult is one failed action, named the way a log line should name it:
// the verb, then the cause. A refused sync says what it could not do rather
// than that something went wrong.
func syncErrResult(verb string, err error) Result {
	return Result{Text: verb + " failed: " + err.Error(), Err: err}
}
