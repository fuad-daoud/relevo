package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
)

// syncDash is the placeholder this view uses for every value it has not been
// told. It is one constant for the whole view on purpose: a dash is a claim
// that nothing was measured, and a view that spelled that several ways would
// leave a reader guessing which of them meant "not yet".
const syncDash = "—"

// syncWireOverhead is the multiplier the first-upload estimate carries. It is
// the driver's own wire fact rather than a measured average: a push sends CDC
// full rows as Hrana JSON, so a BLOB travels base64 and costs a third more.
// The estimate is labelled an estimate for the same reason.
const syncWireOverhead = 1.33

// syncLabelW is the width of the label column on the form and status lines, so
// every label in the body starts in the same place.
const syncLabelW = 15

// syncVerbTest is the verb the t key reports under. The view matches on it to
// record a reachability answer, which is why it is a named constant rather than
// a literal in two places: a spelling that drifts would silently stop recording.
const syncVerbTest = "sync test"

// syncMsg is one snapshot read's reply. The snapshot is a value, not a handle:
// it arrives whole and the frame that draws it touches nothing.
type syncMsg struct {
	snap widget.SyncSnapshot
	err  error
}

// syncView is ':sync': what this machine is set to sync with, what the remote
// last reported, which installations have written into this database, and which
// remote bindings are still waiting to be bound.
//
// Every value it draws arrives in a snapshot message, so the render path
// performs no I/O and opens no handle. That is the view's whole safety
// property -- a sync view is the one screen a user opens when something has
// already gone wrong, and it must not be able to make anything worse.
type syncView struct {
	snap    widget.SyncSnapshot
	loaded  bool
	err     error
	actions bool
	top     int // first body line shown (page follows no cursor: one page)
	// now is the shell's clock as of the frame being drawn. It is a field rather
	// than a read because Body takes no argument for it and a second time.Now
	// would put two different ages on one screen.
	now time.Time
	// tested and reachable are this view's own record of the t key. A snapshot
	// deliberately carries no reachability, because a read of local markers
	// cannot know one thing, and "never tried" must not be drawn as "up".
	tested    bool
	reachable bool
}

// syncToker is optionally implemented by a view that carries a sync status
// phrase for the shell header to show. The phrase is read off the stack rather
// than put on widget.Env, so a screen that is not open cannot make the header say
// anything about sync.
type syncToker interface{ SyncAttention() string }

// SyncToken is the statusline's own mapping, read straight off the snapshot. It
// is never recomputed here: the four tokens and the order between them are S2's,
// and a second copy of that order is a second thing to keep in step.
func (v syncView) SyncToken() string {
	if v.snap.Token == "" {
		return relevosync.TokenOff
	}
	return v.snap.Token
}

// SyncAttention is the header's phrase, or "" when nothing needs saying.
//
// The two phrases are the two states a user can act on. `sync · offline` covers
// every reason the handle is not usable: a test that was refused, a tick that
// failed, and an error needing attention all mean the same thing from a header
// -- this machine is not in step with its remote -- and a header has room for
// one phrase about it, not three. The distinction between them lives in the
// body, which is where detail belongs.
//
// `sync · N behind` is the other one, and it carries the count because a
// backlog is a number a user acts on ("push, that is the whole point") while
// "offline" is not.
func (v syncView) SyncAttention() string {
	if v.snap.Token == relevosync.TokenOff {
		return ""
	}
	if v.tested && !v.reachable {
		return "sync · offline"
	}
	switch v.SyncToken() {
	case relevosync.TokenErr:
		return "sync · offline"
	case relevosync.TokenBehind:
		if v.snap.State.Backlog > 0 {
			return fmt.Sprintf("sync · %d behind", v.snap.State.Backlog)
		}
		return "sync · offline"
	default:
		return ""
	}
}

// newSyncView builds ':sync' and the command that reads its snapshot. env.Actions
// must be non-nil: execLine refuses the command otherwise, exactly as it does
// for every other config view.
func newSyncView(env widget.Env) (widget.View, tea.Cmd) {
	v := syncView{actions: env.Actions != nil}
	return v, syncSnapshotCmd(env)
}

func syncSnapshotCmd(env widget.Env) tea.Cmd {
	return func() tea.Msg {
		if env.Actions == nil {
			return syncMsg{}
		}
		snap, err := env.Actions.SyncSnapshot()
		return syncMsg{snap: snap, err: err}
	}
}

func (v syncView) Crumbs() []string { return []string{"sync"} }

// Capturing is always false: the view owns no text input of its own. The e key
// hands off to the shell's process runner instead, which is what keeps every
// key here a single keystroke.
func (v syncView) Capturing() bool { return false }

// Keys are the five verbs, shown only when the shell has an widget.Actions seam:
// without one there is nothing to push, pull, test, edit or turn off, and a
// footer advertising them would be a lie rather than a shortcut.
func (v syncView) Keys() []widget.KeyHelp {
	if !v.actions {
		return nil
	}
	return []widget.KeyHelp{
		{Key: "p", Help: "push"},
		{Key: "l", Help: "pull"},
		{Key: "t", Help: "test"},
		{Key: "e", Help: "edit"},
		{Key: "d", Help: "disable"},
	}
}

func (v syncView) HelpKeys() []widget.KeyHelp { return v.Keys() }

// OffKeys are the keys that do not apply right now. Nothing about a machine's
// configuration changes that in this build: push, pull and test all refuse
// whatever the section says, so the footer stops advertising them and a press
// cannot turn into a refusal the user has to read past. The editor and the
// turn-off still work and stay on the footer.
func (v syncView) OffKeys(env widget.Env) []string {
	if !v.actions {
		return nil
	}
	return []string{"p", "l", "t"}
}

func (v syncView) Context(env widget.Env) (string, string) {
	if !v.loaded {
		return "", ""
	}
	left := "   " + widget.TextStyle.Bold(true).Render(v.SyncToken()) + "   " + widget.MutedStyle.Render(v.statePhrase())
	right := ""
	if n := len(v.snap.Unlinked); n > 0 {
		right = widget.TextStyle.Bold(true).Render(fmt.Sprintf("%d", n)) + " " + widget.MutedStyle.Render(syncUnlinkedWord(n))
	}
	return left, right
}

func syncUnlinkedWord(n int) string {
	if n == 1 {
		return "unlinked binding"
	}
	return "unlinked bindings"
}

// statePhrase is one line of words for the token, so the state is readable
// without remembering which of the four tokens means what.
func (v syncView) statePhrase() string {
	switch v.SyncToken() {
	case relevosync.TokenOff:
		return "off on this machine"
	case relevosync.TokenErr:
		return "needs you: " + syncOr(v.snap.Attention, "the remote reported an error")
	case relevosync.TokenBehind:
		return v.behindPhrase()
	default:
		return "on · " + v.reachPhrase()
	}
}

// behindPhrase says which of behind's two reasons applies, because the two call
// for different things from the user: a failed tick wants the network, a backlog
// wants a push.
func (v syncView) behindPhrase() string {
	if !v.snap.State.LastTickOK && v.snap.Measured {
		return "the last tick failed; this machine may be behind"
	}
	if v.snap.State.Backlog > 0 {
		return fmt.Sprintf("behind by %d unpushed operations", v.snap.State.Backlog)
	}
	return "behind"
}

// reachPhrase is the reachability this view can actually vouch for: what the t
// key last answered, and nothing more.
func (v syncView) reachPhrase() string {
	switch {
	case !v.tested:
		return "reachability untested"
	case v.reachable:
		return "reachable"
	default:
		return "unreachable"
	}
}

func (v syncView) Body(env widget.Env, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	// The shell's clock is read once here and the whole body is drawn against
	// it, so an age and the clock in the header can never disagree.
	v.now = env.Now
	if v.err != nil && !v.loaded {
		return strings.Join(widget.Centered(v.err.Error(), width, height), "\n")
	}
	// No snapshot yet is a blank-padded "loading…", the same shape :servers uses
	// for a row whose probe has not answered. It is not an error: the read is in
	// flight and there is nothing to report until it lands.
	if !v.loaded {
		return strings.Join(widget.BlockLines([]string{"loading…"}, width, height), "\n")
	}
	lines := v.bodyLines(width)
	top := widget.Clamp(v.top, 0, max(0, len(lines)-height))
	end := min(top+height, len(lines))
	return strings.Join(widget.FitLines(lines[top:end], width, height), "\n")
}

func (v syncView) bodyLines(width int) []string {
	if v.SyncToken() == relevosync.TokenOff {
		return v.preOnLines()
	}
	return v.postOnLines(width)
}

// preOnLines is the form for a machine with sync off. It never prints the
// token, not masked to a hint and not truncated: the only fact about it the
// view states is whether one exists.
func (v syncView) preOnLines() []string {
	lines := []string{""}
	lines = append(lines, syncLine("remote", syncOr(v.snap.RemoteURL, "not configured"), widget.MutedStyle))
	lines = append(lines, syncLine("token", v.tokenPhrase(), widget.MutedStyle))
	lines = append(lines, syncLine("reachability", v.reachPhrase(), widget.MutedStyle))
	lines = append(lines, syncLine("first upload", v.uploadEstimate(), widget.MutedStyle))
	lines = append(lines, "")
	lines = append(lines, "   "+widget.FaintStyle.Bold(true).Render("WHAT SYNCS"))
	for _, s := range syncSharedTables {
		lines = append(lines, "     "+widget.GreenStyle.Render("+")+"  "+widget.MutedStyle.Render(s))
	}
	lines = append(lines, "")
	lines = append(lines, "   "+widget.FaintStyle.Bold(true).Render("NEVER SYNCS"))
	for _, s := range syncLockedTables {
		lines = append(lines, "     "+widget.RedStyle.Render("×")+"  "+widget.MutedStyle.Render(s))
	}
	lines = append(lines, "")
	lines = append(lines, v.confirmLines()...)
	lines = append(lines, "")
	return append(lines, v.installationLines()...)
}

// confirmLines says what enabling would have done, and that there is nothing to
// do here. This build carries no engine, so no row leaves this machine and no
// membership is ever created: the line is here so the form does not read as an
// offer it cannot keep.
func (v syncView) confirmLines() []string {
	return []string{
		"   " + widget.WarnStyle.Bold(true).Render("NO ENGINE") + "  enable, push and pull refuse in this build",
		"               " + widget.MutedStyle.Render("nothing leaves this machine, and no remote is contacted"),
	}
}

// tokenPhrase says whether a token exists and, when it does, only that: the
// value is not read by this view.
func (v syncView) tokenPhrase() string {
	if !v.snap.TokenSet {
		return "absent · nothing reads it while sync is off"
	}
	return "present · value never displayed"
}

// uploadEstimate is what the first upload will cost on the wire, from the
// shared file's own size and the spec's base64 overhead. It is an estimate and
// says so, because the real figure depends on what the pages actually hold.
func (v syncView) uploadEstimate() string {
	if v.snap.SharedBytes <= 0 {
		return "not measured · " + syncDash
	}
	local := syncBytes(v.snap.SharedBytes)
	return fmt.Sprintf("~%s on the wire from %s local (est., BLOBs cost a third more)",
		syncBytes(int64(float64(v.snap.SharedBytes)*syncWireOverhead)), local)
}

func (v syncView) postOnLines(width int) []string {
	lines := []string{""}
	lines = append(lines, syncLine("sync", v.SyncToken()+" · "+v.statePhrase(), syncTokenStyle(v.SyncToken())))
	lines = append(lines, syncLine("remote", syncOr(v.snap.RemoteURL, "not configured"), widget.MutedStyle))
	lines = append(lines, syncLine("unpushed", v.backlogPhrase(), widget.MutedStyle))
	if !v.snap.LastExport.IsZero() {
		lines = append(lines, syncLine("last export", v.localStampPhrase(v.snap.LastExport), widget.MutedStyle))
	}
	if !v.snap.LastImport.IsZero() {
		lines = append(lines, syncLine("last import", v.localStampPhrase(v.snap.LastImport), widget.MutedStyle))
	}
	lines = append(lines, syncLine("first upload", v.uploadProgress(), widget.MutedStyle))
	// A machine that recovered from a corrupt replica by itself says so, and
	// names the folder the copy it kept is in. A machine that never rebuilt
	// draws no row, so the status block stays the block it was.
	if !v.snap.State.Rebuild.At.IsZero() {
		phrase := v.localStampPhrase(v.snap.State.Rebuild.At)
		if v.snap.State.Rebuild.Aside != "" {
			phrase += " · " + v.snap.State.Rebuild.Aside
		}
		lines = append(lines, syncLine("last rebuild", phrase, widget.MutedStyle))
	}
	lines = append(lines, v.attemptLines()...)
	if v.snap.Attention != "" {
		lines = append(lines, "")
		lines = append(lines, "   "+widget.ErrorStyle.Render(v.snap.Attention))
	}
	lines = append(lines, v.troubleLines()...)
	lines = append(lines, "")
	lines = append(lines, v.installationLines()...)
	lines = append(lines, "")
	lines = append(lines, v.unlinkedLines(width)...)
	return lines
}

// troubleLines is what the last import could not apply, one line per report:
// an origin a newer writer held, a batch a refusal dropped, a sequence gap.
// Nothing is drawn when the import had no trouble, so a healthy machine's
// status block stays the block it was.
func (v syncView) troubleLines() []string {
	if v.snap.Trouble.Empty() {
		return nil
	}
	out := []string{"", "   " + widget.WarnStyle.Bold(true).Render("IMPORT TROUBLE")}
	for _, held := range v.snap.Trouble.Held {
		out = append(out, "     "+widget.WarnStyle.Render("held    ")+widget.MutedStyle.Render(sanitizeText(held)))
	}
	for _, dropped := range v.snap.Trouble.Dropped {
		out = append(out, "     "+widget.WarnStyle.Render("dropped ")+widget.MutedStyle.Render(sanitizeText(dropped)))
	}
	for _, gap := range v.snap.Trouble.Gaps {
		out = append(out, "     "+widget.WarnStyle.Render("gap     ")+widget.MutedStyle.Render(sanitizeText(gap)))
	}
	return out
}

// localStampPhrase renders one of the pipeline's own stamps the way the
// remote's are rendered, so both read alike on one screen.
func (v syncView) localStampPhrase(at time.Time) string {
	return fmt.Sprintf("%s · %s", unixAge(at.Unix(), v.now), unixClock(at.Unix()))
}

// backlogPhrase is the unpushed count, or the placeholder when no tick has ever
// measured one. A zero would be a lie here: an unmeasured machine has not been
// shown to be caught up.
func (v syncView) backlogPhrase() string {
	if !v.snap.Measured {
		return syncDash + " not measured yet"
	}
	return fmt.Sprintf("%d operations", v.snap.State.Backlog)
}

// uploadProgress is where the first upload stands. It is complete only once a
// completed attempt has stamped its export: a machine that has exchanged
// nothing has not uploaded anything, and reporting progress from the backlog
// would be reading a change set as a completed transfer.
func (v syncView) uploadProgress() string {
	switch {
	case !v.snap.Measured:
		return syncDash + " not started"
	case v.snap.LastExport.IsZero():
		return "pending · nothing has been pushed yet"
	default:
		return "complete · " + unixAge(v.snap.LastExport.Unix(), v.now)
	}
}

// installationLines is one row per installation that has written into this
// database, its label beside its id. This machine's own row is marked and its
// label is the one its own file owns; every other row is attribution only, and
// is drawn faint to say that a label here is something to read rather than
// something to correct.
func (v syncView) installationLines() []string {
	out := []string{"   " + widget.FaintStyle.Bold(true).Render("INSTALLATIONS")}
	if len(v.snap.Installation) == 0 {
		return append(out, "     "+widget.EmptyStyle.Render("none recorded yet"))
	}
	header := widget.Pad("LABEL", 18) + widget.Pad("ID", 18) + widget.MutedStyle.Render("LAST SEEN")
	out = append(out, "     "+header)
	for _, in := range v.snap.Installation {
		own := in.ID != "" && in.ID == v.snap.OwnID
		style := widget.MutedStyle
		label := in.Label
		if label == "" {
			label = syncDash
		}
		if own {
			style = widget.TextStyle
		} else {
			style = widget.FaintStyle
		}
		row := widget.Pad(label, 18) + widget.Pad(clipMiddle(in.ID, 16), 18) + style.Render(syncDate(in.LastSeen))
		out = append(out, "     "+row)
	}
	return out
}

// unlinkedLines lists the remote binding rows that carry no link, with the
// reason they are here: migration 015 wrote no link for every row that predates
// it and none has been backfilled, so these are listed for a deliberate re-bind
// rather than repaired on the way past.
func (v syncView) unlinkedLines(width int) []string {
	out := []string{"   " + widget.FaintStyle.Bold(true).Render("UNLINKED REMOTE BINDINGS") +
		widget.MutedStyle.Render(fmt.Sprintf("  (%d) · nothing backfills these; they re-bind on purpose", len(v.snap.Unlinked)))}
	if len(v.snap.Unlinked) == 0 {
		return append(out, "     "+widget.EmptyStyle.Render("every remote binding carries a link"))
	}
	out = append(out, "     "+widget.Pad("NAME", 24)+widget.Pad("OWNER", 16)+widget.MutedStyle.Render("LINK"))
	for _, r := range v.snap.Unlinked {
		out = append(out, "     "+widget.Pad(clipName(r.Name, 22), 24)+widget.Pad(clipName(r.Owner, 14), 16)+
			widget.FaintStyle.Render("none"))
	}
	return out
}

func (v syncView) Update(msg tea.Msg, env widget.Env) (widget.View, tea.Cmd) {
	switch msg := msg.(type) {
	case syncMsg:
		if msg.err != nil {
			v.err = msg.err
			return v, nil
		}
		v.snap, v.err, v.loaded = msg.snap, nil, true
		// A re-read cannot answer reachability, so a test's answer is dropped
		// with the snapshot it was read against: an "up" from before a settings
		// change is not an "up" after one.
		v.tested = false
		return v, nil

	case actionMsg:
		if msg.verb == syncVerbTest {
			// Reachability is recorded from the action's own outcome rather than
			// from a snapshot field, because a snapshot deliberately cannot carry
			// it: nothing was dialled to produce one. A refused test is an
			// unreachable remote, and a test that succeeded is a reachable one.
			v.tested, v.reachable = true, msg.res.Err == nil
		}
		// Every key here ends in an action, and every action's Refresh means the
		// markers moved. Re-read rather than patching the snapshot in place, so
		// what the next frame shows is what a fresh read would say.
		if msg.res.Refresh {
			return v, syncSnapshotCmd(env)
		}
		return v, nil

	case statusMsg:
		return v, syncSnapshotCmd(env)

	case tea.KeyMsg:
		if !v.actions {
			return v, nil
		}
		return v.updateKey(msg, env)
	}
	return v, nil
}

// updateKey records exactly one call per key, or opens one confirm that will.
func (v syncView) updateKey(k tea.KeyMsg, env widget.Env) (widget.View, tea.Cmd) {
	switch k.String() {
	case "p":
		return v, runAction(env.Ctx, "sync push", "", func(ctx context.Context) widget.Result {
			return env.Actions.SyncPush(ctx)
		})
	case "l":
		return v, runAction(env.Ctx, "sync pull", "", func(ctx context.Context) widget.Result {
			return env.Actions.SyncPull(ctx)
		})
	case "t":
		return v, runAction(env.Ctx, syncVerbTest, "", func(ctx context.Context) widget.Result {
			return env.Actions.SyncTest(ctx)
		})
	case "e":
		cmd, err := env.Actions.SyncEditor()
		if err != nil {
			return v, notice(err.Error())
		}
		return v, tea.ExecProcess(cmd, func(error) tea.Msg { return syncMsg{} })
	case "d":
		return v, v.disableCmd(env)
	}
	return v, nil
}

// disableCmd is the d key: one confirm naming the database, and behind it S4's
// turn-off whole. The confirm names the remote rather than saying "this machine",
// because the thing a user is about to stop is a copy of their data living
// somewhere else, and which somewhere is the whole question.
func (v syncView) disableCmd(env widget.Env) tea.Cmd {
	target := v.snap.RemoteURL
	if target == "" {
		target = "the remote"
	}
	return openOverlay(confirmBox{
		kind:   "sync disable",
		yes:    "disable",
		danger: true,
		title:  "Turn sync off for " + widget.AccentStyle.Bold(true).Render(target) + "?",
		lines: []string{
			widget.Pad("final export", 13) + "one last attempt, bounded; a remote that does not answer does not refuse",
			widget.Pad("kept", 13) + "every local file and row, still servable",
			widget.Pad("remote", 13) + "left alone; you delete that with Turso's own tooling",
		},
		onYes: runAction(env.Ctx, "sync disable", "", func(ctx context.Context) widget.Result {
			return env.Actions.SyncDisable(ctx)
		}),
	})
}
