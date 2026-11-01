package ui

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/db/wire/client"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
)

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

// syncVerb sends one sync verb to the daemon that owns this machine's database
// and answers with what it did: the one route every cockpit sync action takes.
// The daemon holds the shared file under its lock, so the cockpit never opens
// relevo-sync.db itself: a direct open would be the lock conflict this route
// exists to remove, and a cockpit that ran the verb locally would import into
// relevo.db and delete the replica out from under the daemon's worker.
//
// The call rides the owner socket, exactly as `relevo db sync` sends one. A
// handle that did not dial an owner refuses rather than opening the file itself.
func (a *mastermindActions) syncVerb(ctx context.Context, verb string) (*wire.SyncResult, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	ctx, cancel := syncCtx(ctx)
	defer cancel()
	res, err := a.runtime().DB.SyncVerb(ctx, &wire.SyncVerb{
		Header: wire.Header{Type: wire.TypeSyncVerb},
		Verb:   verb,
	}, nil)
	if err != nil {
		return nil, err
	}
	if verr := client.VerbError(res); verr != nil {
		return nil, verr
	}
	return res, nil
}

// SyncPush sends this machine's local change set, then re-reads, so the view
// shows what the remote measured rather than what the push assumed.
func (a *mastermindActions) SyncPush(ctx context.Context) widget.Result {
	if _, err := a.syncVerb(ctx, wire.SyncVerbPush); err != nil {
		return syncErrResult("push", err)
	}
	return widget.Result{Text: "pushed this machine's change set", Refresh: true}
}

// SyncPull fetches the remote's changes and rebases the local ones on top. The
// applied flag becomes words, because "nothing to apply" is the answer a user
// needs and a boolean is not one.
func (a *mastermindActions) SyncPull(ctx context.Context) widget.Result {
	res, err := a.syncVerb(ctx, wire.SyncVerbPull)
	if err != nil {
		return syncErrResult("pull", err)
	}
	if !res.Applied {
		return widget.Result{Text: "pulled: the remote had nothing to apply", Refresh: true}
	}
	return widget.Result{Text: "pulled and applied the remote's changes", Refresh: true}
}

// SyncTest asks the remote whether it is there. It changes no byte here, so a
// test is safe against a remote a user only wants to ask about: it opens the
// same worker a verb does and asks it for the log's stats, which is a read.
// It runs through the same executor the other actions do, so the worker it
// builds is the one a later push or pull reuses.
func (a *mastermindActions) SyncTest(ctx context.Context) widget.Result {
	if _, err := a.syncVerb(ctx, wire.SyncVerbProbe); err != nil {
		return syncErrResult("test connection", err)
	}
	return widget.Result{Text: "the remote answered", Refresh: true}
}

// SyncDisable runs S4's turn-off whole: the same six steps, in the order its
// contract fixes, through the same executor the CLI verb drives. The final
// export stays best-effort, so a remote that cannot be reached is a warning on
// a successful turn-off rather than a refusal to leave.
func (a *mastermindActions) SyncDisable(ctx context.Context) widget.Result {
	res, err := a.syncVerb(ctx, wire.SyncVerbDisable)
	if err != nil {
		return syncErrResult("disable", err)
	}
	text := "sync off on this machine; local files unchanged and still servable"
	if res.Warning != "" {
		text += " · the final export was skipped: " + res.Warning
	}
	return widget.Result{Text: text, Refresh: true}
}

// SyncSnapshot assembles the view's whole read side, off the update loop and
// with no handle in reach. A machine with no local file, no remote or no
// measurement answers with the fields it does have rather than an error, so the
// pre-on form renders on a machine that has never enabled sync.
func (a *mastermindActions) SyncSnapshot() (widget.SyncSnapshot, error) {
	var snap widget.SyncSnapshot
	if err := a.ready(); err != nil {
		return snap, err
	}
	shared := a.runtime().DB

	// Opening or refreshing the view is a read that wants current data, so the
	// daemon is hinted. Best effort and bounded: a handle with no owner, or an
	// owner that does not answer, costs the snapshot nothing.
	hintCtx, cancelHint := context.WithTimeout(context.Background(), time.Second)
	_ = shared.SyncFreshen(hintCtx)
	cancelHint()

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
		snap.LastExport, snap.LastImport, snap.Trouble = state.LastExport, state.LastImport, state.Trouble
		// Measured is the completed attempt's own stamp, so the pair the view
		// gates on is written by the pipeline that measured it rather than by a
		// value nothing records.
		snap.Measured = !state.LastExport.IsZero() || !state.LastImport.IsZero()
	} else {
		// A marker that will not parse still has a token, and the token says
		// off: the mark that turns it on did not read, so nothing has enabled it.
		snap.Token = relevosync.TokenOff
	}
	if msg, err := relevosync.ReadAttention(local); err == nil {
		snap.Attention = msg
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
func syncErrResult(verb string, err error) widget.Result {
	return widget.Result{Text: verb + " failed: " + err.Error(), Err: err}
}
