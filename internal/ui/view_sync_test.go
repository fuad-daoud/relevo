package ui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/db"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/view"
)

// syncNow is the clock every sync fixture is drawn against, the same local zone
// the rest of the cockpit fixtures use so a rendered time cannot depend on the
// machine the test runs on.
var syncNow = railNow

// syncRemote is the remote the fixtures name. It is a libsql URL because that
// is one of the three schemes S2's own validator accepts, so a fixture that
// renders it is a fixture whose section would pass.
const syncRemote = "libsql://relevo-abc123.turso.io"

// syncOwnID is this machine's installation id in every sync fixture.
const syncOwnID = "01INSTALLATION"

// syncSecretValue is the token as it would be stored. It is here so a test can
// assert it never reaches a screen: a view that never receives it cannot print
// it, but a view that read the secret table by the wrong name could, and the
// golden test is what would catch that.
const syncSecretValue = "eyJhbGciOiJIUzI1NiJ9.super-secret-token-value"

// syncPreOnSnapshot is the fixture for a machine that has never enabled sync: a
// remote configured and a token stored, no measurement, and one installation --
// this one. Everything the pre-on form shows comes from here and nowhere else.
func syncPreOnSnapshot() SyncSnapshot {
	snap := syncBaseSnapshot()
	snap.Settings = relevosync.Settings{RemoteURL: syncRemote}
	snap.RemoteURL = syncRemote
	snap.TokenSet = true
	return snap
}

// syncPostOnSnapshot is the fixture for a machine that is on and healthy: a
// measured snapshot with a real backlog, real byte counts, both stamps and an
// opaque revision. The backlog is non-zero on purpose, because a fixture whose
// backlog is zero cannot tell a view that renders the count from one that
// hard-codes it.
func syncPostOnSnapshot() SyncSnapshot {
	snap := syncBaseSnapshot()
	snap.Settings = relevosync.Settings{Enabled: true, RemoteURL: syncRemote, Namespace: "default"}
	snap.RemoteURL = syncRemote
	snap.TokenSet = true
	snap.State = relevosync.State{Enabled: true, Backlog: 3, LastTickOK: true}
	snap.Token = relevosync.Token(snap.State)
	snap.Measured = true
	snap.Stats = relevosync.Stats{
		CdcOperations:        3,
		LastPushUnixTime:     syncNow.Add(-2 * time.Minute).Unix(),
		LastPullUnixTime:     syncNow.Add(-5 * time.Minute).Unix(),
		NetworkSentBytes:     1_284_000,
		NetworkReceivedBytes: 842_000,
		Revision:             "rev-7f3a91c2",
	}
	return snap
}

// syncBaseSnapshot is what every fixture shares: this machine's own
// installation, its size on disk, and the two reads that have not answered yet.
func syncBaseSnapshot() SyncSnapshot {
	snap := SyncSnapshot{
		OwnID:       syncOwnID,
		SharedBytes: 1_610_612_736,
		Unknown:     true,
		Installation: []db.Installation{
			{ID: syncOwnID, Label: "laptop", FirstSeen: syncNow.AddDate(0, 0, -5), LastSeen: syncNow.AddDate(0, 0, -1)},
		},
	}
	snap.Token = relevosync.TokenOff
	return snap
}

// syncFake is the fake carrying one snapshot.
func syncFake(snap SyncSnapshot) *fakeActions {
	return &fakeActions{snap: snap, syncResult: Result{Text: "ok", Refresh: true}}
}

// syncModel is the shell with ':sync' open over a snapshot, its read drained.
func syncModel(t *testing.T, width, height int, snap SyncSnapshot) (Model, *fakeActions) {
	t.Helper()
	fa := syncFake(snap)
	m := goldenActionModel(t, width, height, fa, view.Report{})
	return drain(t, m, execLine("sync", m.env(), m.prefs)), fa
}

// assertSyncGolden is the sync view's golden check: the same shape the cockpit's
// own goldens use, with the same -update flag, plus the two invariants every
// screen owes its reader -- exactly height lines, none wider than width.
func assertSyncGolden(t *testing.T, name string, m Model, width, height int) {
	t.Helper()
	got := stripANSI(m.View())
	path := filepath.Join("testdata", name+".golden")
	if *updateGolden {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Errorf("%s: golden mismatch\ngot:\n%s\nwant:\n%s", name, got, string(want))
	}
	lines := strings.Split(got, "\n")
	if len(lines) != height {
		t.Errorf("%s: %d lines, want %d", name, len(lines), height)
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w > width {
			t.Errorf("%s: line %d is %d wide, want <= %d: %q", name, i, w, width, l)
		}
	}
}

// TestSyncPreOnFormGolden pins the form a machine sees before it has ever
// enabled sync: where the remote is, that a token is stored without its value
// being shown, what the first upload will cost, what crosses and what is locked
// off, and the confirm naming the database an enable would seed.
//
// The mutation is the mask: un-masking the token line would put the stored value
// on the screen, and this golden is what would go red.
func TestSyncPreOnFormGolden(t *testing.T) {
	m, _ := syncModel(t, 132, 34, syncPreOnSnapshot())
	assertSyncGolden(t, "sync-pre-on-132", m, 132, 34)

	body := plain(m.top().Body(m.env(), 132, 34))
	if strings.Contains(body, syncSecretValue) {
		t.Errorf("the pre-on form printed the token value")
	}
	if !strings.Contains(body, "present") {
		t.Errorf("the pre-on form does not say the token is present:\n%s", body)
	}
	if !strings.Contains(body, "NEVER SYNCS") || !strings.Contains(body, "every secret, including turso.token") {
		t.Errorf("the what-syncs list does not lock the secrets off:\n%s", body)
	}
	if !strings.Contains(body, syncRemote) {
		t.Errorf("the confirm does not name the target database:\n%s", body)
	}
}

// TestSyncPostOnStatusGolden pins the status block a machine sees once sync is
// on: the token, the unpushed count, both stamps, the bytes each way, the
// revision and the first-upload progress, all rendered from one snapshot that
// arrived as a message.
//
// The mutation is zeroing CdcOperations: the behind-count line is derived from
// the snapshot's backlog, so a view that stopped reading it would hold this
// golden still and lose the number a user acts on.
func TestSyncPostOnStatusGolden(t *testing.T) {
	m, _ := syncModel(t, 132, 34, syncPostOnSnapshot())
	assertSyncGolden(t, "sync-post-on-132", m, 132, 34)

	body := plain(m.top().Body(m.env(), 132, 34))
	for _, want := range []string{"sync:ok", "3 operations", "1.3 MB", "842.0 kB", "rev-7f3a91c2", "complete"} {
		if !strings.Contains(body, want) {
			t.Errorf("post-on body is missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, syncSecretValue) {
		t.Errorf("the post-on body printed the token value")
	}
}

// TestSyncHaltedErrorGolden pins the three states that are not the healthy one:
// a failed tick and a backlog both behind, an error needing attention, and a
// machine that is off. It is the test that says the four-token mapping is
// rendered rather than recomputed -- flipping which marker decides the token
// must change what this screen shows.
func TestSyncHaltedErrorGolden(t *testing.T) {
	behind := syncPostOnSnapshot()
	behind.State.LastTickOK = false
	behind.Token = relevosync.Token(behind.State)

	errSnap := syncPostOnSnapshot()
	errSnap.State.Attention = true
	errSnap.Token = relevosync.Token(errSnap.State)
	errSnap.Attention = "the remote refused this installation's token"

	off := syncBaseSnapshot()

	for _, tc := range []struct {
		name string
		snap SyncSnapshot
	}{
		{"sync-behind-132", behind},
		{"sync-err-132", errSnap},
		{"sync-off-132", off},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := syncModel(t, 132, 34, tc.snap)
			assertSyncGolden(t, tc.name, m, 132, 34)
			// The token is read off the whole screen rather than the body: it is
			// the context row that carries it, and the off form's body is the
			// pre-on form rather than a status block.
			screen := plain(stripANSI(m.View()))
			if !strings.Contains(screen, tc.snap.Token) {
				t.Errorf("%s: screen does not carry the token %q:\n%s", tc.name, tc.snap.Token, screen)
			}
			if tc.name == "sync-err-132" && !strings.Contains(screen, errSnap.Attention) {
				t.Errorf("the error state does not show what needs attention:\n%s", screen)
			}
		})
	}
}

// TestSyncUnlinkedMatchesFixture pins the two lists against a seeded fixture:
// one row per installation with its label, and exactly the binding_record rows
// whose link columns are both NULL.
//
// The mutation is linking one fixture row: it must then drop out of the list,
// because a linked row is not a row waiting to be bound and showing it would
// ask a user to re-bind something already bound.
func TestSyncUnlinkedMatchesFixture(t *testing.T) {
	snap := syncPostOnSnapshot()
	snap.Installation = []db.Installation{
		{ID: syncOwnID, Label: "laptop", FirstSeen: syncNow.AddDate(0, 0, -5), LastSeen: syncNow.AddDate(0, 0, -1)},
		{ID: "02INSTALLATION", Label: "zen", FirstSeen: syncNow.AddDate(0, 0, -30), LastSeen: syncNow.AddDate(0, 0, -2)},
	}
	snap.Unlinked = []db.Record{
		{ID: "rec-1", Owner: "alice", Name: "webshop"},
		{ID: "rec-2", Owner: "bob", Name: "shopfront"},
	}

	m, _ := syncModel(t, 132, 34, snap)
	sv := m.top().(syncView)
	lines := sv.unlinkedLines(132)
	got := plain(strings.Join(lines, "\n"))

	for _, want := range []string{"webshop", "shopfront", "alice", "bob", "(2)"} {
		if !strings.Contains(got, want) {
			t.Errorf("the unlinked list is missing %q:\n%s", want, got)
		}
	}
	// Nothing may be written while listing: this view is a reader, and a
	// read-side backfill would be exactly the migration 015 decision the spec
	// refuses to make silently.
	if len(sv.snap.Unlinked) != 2 {
		t.Errorf("unlinked list changed length during render: %d", len(sv.snap.Unlinked))
	}

	linked := syncPostOnSnapshot()
	linked.Unlinked = snap.Unlinked[:1]
	linked.Installation = snap.Installation
	m2, _ := syncModel(t, 132, 34, linked)
	body := plain(m2.top().Body(m2.env(), 132, 34))
	if strings.Contains(body, "shopfront") {
		t.Errorf("a row that is no longer in the snapshot is still listed:\n%s", body)
	}
	if !strings.Contains(body, "zen") {
		t.Errorf("the other installation's label is not shown:\n%s", body)
	}
}

// TestSyncRendersWithNoNetworkHandle pins the view's safety property: it draws
// from the snapshot it was handed and from nothing else, on a shell that has an
// Actions seam within reach of a dial.
//
// The mutation is touching the network on the render path. This test cannot
// intercept a real dial, so it does the next best thing: the fake records every
// verb it is asked for, and Body is asserted to have asked for none. A render
// path that dialled would show up here the moment it asked.
func TestSyncRendersWithNoNetworkHandle(t *testing.T) {
	// The seam is present and would answer, so a render path that reached for it
	// would be recorded rather than silently nil-panicking.
	fa := syncFake(syncPostOnSnapshot())
	m := goldenActionModel(t, 132, 34, fa, view.Report{})
	m = drain(t, m, execLine("sync", m.env(), m.prefs))

	fa.syncs = nil
	fa.syncReads = 0
	v := m.top().(syncView)
	for range 3 {
		v.Body(m.env(), 132, 34)
	}
	if len(fa.syncs) != 0 {
		t.Errorf("the render path called through the seam: %v", fa.syncs)
	}
	if fa.syncReads != 0 {
		t.Errorf("the render path re-read the snapshot %d times, want 0", fa.syncReads)
	}

	// And the same view with no seam at all: the shape a serve ui has. It still
	// renders, because a snapshot is a value and needs nothing to arrive with.
	env := Env{Ctx: t.Context(), Now: syncNow, Width: 132, Height: 34, Loaded: true}
	if env.Actions != nil {
		t.Fatalf("the no-seam env has an Actions seam")
	}
	detached := syncView{snap: syncPostOnSnapshot(), loaded: true}
	body := detached.Body(env, 132, 34)
	if !strings.Contains(plain(body), "sync:ok") {
		t.Errorf("a snapshot handed straight to the view did not render:\n%s", plain(body))
	}
	if n := len(strings.Split(body, "\n")); n != 34 {
		t.Errorf("%d lines, want 34", n)
	}

	// The seam being nil is also what hides the keys: an action key a view
	// cannot serve is a key that would do nothing, and the footer must not
	// advertise it.
	if keys := detached.Keys(); keys != nil {
		t.Errorf("keys advertised without a seam: %v", keys)
	}
	for _, k := range []tea.KeyMsg{key('p'), key('l'), key('t'), key('e'), key('d')} {
		if _, cmd := detached.Update(k, env); cmd != nil {
			t.Errorf("key %q returned a command without a seam", k.String())
		}
	}
}

// TestSyncKeysRecordOneCall pins each of the five keys against the seam: one
// call each, and d opening the confirm that names the database before the
// disable it guards can happen.
//
// The mutation is dropping d's confirm gate: with it gone, one press would turn
// sync off on a remote the user was never shown, and this test is what says one
// press is not enough.
func TestSyncKeysRecordOneCall(t *testing.T) {
	for _, k := range []struct {
		r    rune
		want string
	}{
		{'p', "push"},
		{'l', "pull"},
		{'t', "test"},
	} {
		t.Run(string(k.r), func(t *testing.T) {
			m, fa := syncModel(t, 132, 34, syncPostOnSnapshot())
			// The re-read the Refresh asks for is a read, not a call under test;
			// what is pinned here is that the key made exactly one verb call.
			fa.syncs = nil
			res, cmd := m.Update(key(k.r))
			m = drain(t, res.(Model), cmd)
			if len(fa.syncs) != 1 {
				t.Fatalf("key %q made %d calls, want 1: %v", k.r, len(fa.syncs), fa.syncs)
			}
			if fa.syncs[0].verb != k.want {
				t.Errorf("key %q called %q, want %q", k.r, fa.syncs[0].verb, k.want)
			}
			if fa.syncReads == 0 {
				t.Errorf("key %q did not re-read the snapshot after the action", k.r)
			}
		})
	}

	t.Run("e", func(t *testing.T) {
		m, fa := syncModel(t, 132, 34, syncPostOnSnapshot())
		fa.syncs = nil
		// The e key hands a command to the shell's process runner rather than
		// returning it as an action, so the message it produces is drained with
		// the same helper the other keys use.
		res, _ := m.Update(key('e'))
		drain(t, res.(Model))
		if len(fa.syncs) != 1 || fa.syncs[0].verb != "edit" {
			t.Fatalf("key e made %v, want exactly one edit", fa.syncs)
		}
	})

	t.Run("d", func(t *testing.T) {
		m, fa := syncModel(t, 132, 34, syncPostOnSnapshot())
		fa.syncs = nil
		res, cmd := m.Update(key('d'))
		m = res.(Model)
		if len(fa.syncs) != 0 {
			t.Fatalf("d called %v before the confirm was answered", fa.syncs)
		}
		m = drain(t, m, cmd)
		if _, ok := m.overlay.(confirmBox); !ok {
			t.Fatalf("d opened %T, want a confirmBox", m.overlay)
		}
		box := m.overlay.(confirmBox)
		if !strings.Contains(box.title, syncRemote) {
			t.Errorf("the confirm does not name the database: %s", box.title)
		}
		if !box.danger {
			t.Errorf("a turn-off confirm is not marked as a danger")
		}
		// The turn-off happens only behind the confirm, and only on y.
		m = syncAnswer(t, m, 'n')
		if len(fa.syncs) != 0 {
			t.Errorf("answering n still called %v", fa.syncs)
		}

		fa.syncs = nil
		res, cmd = m.Update(key('d'))
		m = drain(t, res.(Model), cmd)
		m = syncAnswer(t, m, 'y')
		if len(fa.syncs) != 1 || fa.syncs[0].verb != "disable" {
			t.Fatalf("answering y called %v, want exactly one disable", fa.syncs)
		}
	})
}

// syncAnswer presses one key on the shell's current overlay and drains the
// result, which is how a confirm is answered in these tests.
func syncAnswer(t *testing.T, m Model, r rune) Model {
	t.Helper()
	res, cmd := m.Update(key(r))
	return drain(t, res.(Model), cmd)
}

// TestSyncHeaderAttention pins the header chip beside the needs-you chip, not
// over it. The header is the cockpit's only always-on line, so a sync state that
// replaced the question chip would hide a question.
//
// The mutation is forcing N behind to 0 while the tick is failing: a chip that
// dropped whenever the count happened to be zero would then go silent about the
// one state a user most needs to see.
func TestSyncHeaderAttention(t *testing.T) {
	// The report has a binding waiting on a human, so the needs-you chip is on
	// the same header the sync chip joins.
	rep := view.Report{Bindings: []view.BindingStatus{{
		Name: "atlas", Round: 1, Display: "NEEDS YOU", MasterMindName: "you",
	}}}

	behind := syncPostOnSnapshot()
	behind.State.LastTickOK = false
	behind.State.Backlog = 0 // forced to zero on purpose, see the mutation above
	behind.Token = relevosync.Token(behind.State)

	syncSay := func(t *testing.T, snap SyncSnapshot) string {
		t.Helper()
		fa := syncFake(snap)
		m := goldenActionModel(t, 132, 34, fa, rep)
		m = drain(t, m, execLine("sync", m.env(), m.prefs))
		// The crumb of this view is itself spelled "sync", so the chip is
		// matched on its own phrase and on the bullet that precedes every
		// attention chip, never on the word alone.
		return plain(strings.Split(stripANSI(m.View()), "\n")[0])
	}

	syncChip := func(t *testing.T, head string) string {
		t.Helper()
		i := strings.Index(head, "● sync · ")
		if i < 0 {
			return ""
		}
		rest := head[i+len("● sync · "):]
		if j := strings.Index(rest, " "); j >= 0 {
			return rest[:j]
		}
		return rest
	}

	t.Run("offline", func(t *testing.T) {
		head := syncSay(t, behind)
		if got := syncChip(t, head); got != "offline" {
			t.Errorf("a failed tick with a zero backlog shows no offline chip: %s", head)
		}
		if !strings.Contains(head, "1 needs you") {
			t.Errorf("the sync chip disturbed the needs-you chip: %s", head)
		}
	})

	t.Run("behind", func(t *testing.T) {
		backlogged := syncPostOnSnapshot()
		backlogged.State.Backlog = 128
		backlogged.Token = relevosync.Token(backlogged.State)
		head := syncSay(t, backlogged)
		if got := syncChip(t, head); got != "128" {
			t.Errorf("a backlogged machine shows no count chip: %s", head)
		}
	})

	t.Run("silent", func(t *testing.T) {
		head := syncSay(t, syncPostOnSnapshot())
		if got := syncChip(t, head); got != "" {
			t.Errorf("a healthy machine shows the sync chip %q: %s", got, head)
		}
		if !strings.Contains(head, "1 needs you") {
			t.Errorf("the needs-you chip is missing: %s", head)
		}
	})

	t.Run("off shows nothing", func(t *testing.T) {
		head := syncSay(t, syncBaseSnapshot())
		if got := syncChip(t, head); got != "" {
			t.Errorf("a machine that never enabled sync shows the chip %q: %s", got, head)
		}
	})
}

// TestSyncCommandRefusedWithoutActions pins the one case the other views already
// have and this one inherits: without the Actions seam the command is a notice,
// not a crash and not a half-working screen.
func TestSyncCommandRefusedWithoutActions(t *testing.T) {
	m := goldenModel(t, 132, 34, view.Report{})
	if m.env().Actions != nil {
		t.Skip("this shell has an Actions seam")
	}
	cmd := execLine("sync", m.env(), m.prefs)
	msg, ok := cmd().(noticeMsg)
	if !ok {
		t.Fatalf(":sync without a seam returned %T, want a notice", cmd())
	}
	if !strings.Contains(msg.text, "relevo ui") {
		t.Errorf("the refusal does not say what is needed: %q", msg.text)
	}
}

// TestSyncSnapshotNeverCarriesTheToken pins the one field the snapshot must not
// have. A read that returned the token value would put a credential one careless
// render away from a screen and a log, and no other test could catch it.
func TestSyncSnapshotNeverCarriesTheToken(t *testing.T) {
	snap := syncPreOnSnapshot()
	if snap.TokenSet != true {
		t.Fatalf("the fixture does not report a stored token")
	}
	// The whole snapshot rendered as text, with every field the view could
	// reach: the secret must not be in it, and the fixture's own value is the
	// only place it could have come from.
	rendered := plain(dumpSyncSnapshot(snap))
	if strings.Contains(rendered, syncSecretValue) {
		t.Fatalf("the snapshot carries the token value:\n%s", rendered)
	}
	if !strings.Contains(rendered, "tokenSet=true") {
		t.Errorf("the snapshot does not report presence at all:\n%s", rendered)
	}
}

// dumpSyncSnapshot renders every field of a snapshot, so a test can assert on
// what a snapshot can and cannot carry rather than on what one render chose to
// draw.
func dumpSyncSnapshot(s SyncSnapshot) string {
	var b strings.Builder
	b.WriteString(s.Token + "\n")
	b.WriteString(s.RemoteURL + "\n")
	b.WriteString(s.Settings.RemoteURL + " " + s.Settings.Namespace + "\n")
	b.WriteString(s.Attention + "\n")
	b.WriteString(s.Stats.Revision + "\n")
	b.WriteString(s.OwnID + "\n")
	b.WriteString(fmt.Sprintf("tokenSet=%t measured=%t unknown=%t\n", s.TokenSet, s.Measured, s.Unknown))
	for _, in := range s.Installation {
		b.WriteString(in.ID + " " + in.Label + "\n")
	}
	for _, r := range s.Unlinked {
		b.WriteString(r.Name + " " + r.Owner + " " + r.LinkOrigin + " " + r.LinkID + "\n")
	}
	return b.String()
}

// TestSyncViewRefusesAnUnreadableSection is the one failure a sync view can
// meet before it has drawn anything: the section will not parse. It has to be a
// message on the screen rather than a panic, because the machine whose section
// will not parse is exactly the machine a user opens this screen to fix.
func TestSyncViewRefusesAnUnreadableSection(t *testing.T) {
	fa := syncFake(SyncSnapshot{})
	fa.snapErr = errors.New("sync: read the sync section: bad body")
	m := goldenActionModel(t, 132, 34, fa, view.Report{})
	m = drain(t, m, execLine("sync", m.env(), m.prefs))
	body := plain(m.top().Body(m.env(), 132, 34))
	if !strings.Contains(body, "bad body") {
		t.Errorf("an unreadable section is not on the screen:\n%s", body)
	}
	if n := len(strings.Split(m.top().Body(m.env(), 132, 34), "\n")); n != 34 {
		t.Errorf("%d lines, want 34", n)
	}
}
