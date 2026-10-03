package relevo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// remoteClosedBinding points the webshop binding at a server and leaves it on
// the round the server closed, which is the round applyCatchUpReport files the
// report under.
func remoteClosedBinding(t *testing.T, rt Runtime) store.Binding {
	t.Helper()
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", b.Name)
	}
	b.Builder.Server = "zen"
	b.State = store.StateActive
	return b
}

// writeRoundReport writes the closed round's report body so the catch-up's
// queueReport finds an artifact, the way a fetched report temp would have been
// renamed into place.
func writeRoundReport(t *testing.T, rt Runtime, b store.Binding, round int) {
	t.Helper()
	path := rt.Store.ReportPath(b.Name, round)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("report body"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestServerUnmarkedCloseWaitsAsUnmarked: the server closed the round without
// its completion marker and said so in the report note. That note rides the
// close on the wire, and the round's own note is the only record the client
// keeps of it -- so wait exits 2 rather than certifying a report nothing
// confirmed.
func TestServerUnmarkedCloseWaitsAsUnmarked(t *testing.T) {
	t.Parallel()

	rt, _ := seedHeadless(t, newFakeRunner())
	b := remoteClosedBinding(t, rt)
	const closed = 1
	writeRoundReport(t, rt, b, closed)
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", b.Name)
	}

	view := remote.BindingView{
		ClosedRound: closed,
		ReportNote:  "unmarked",
		Stopped:     "",
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Load(b.Name)
		if err != nil {
			return err
		}
		_, err = applyCatchUpReport(context.Background(), rt, tx, cur, &catchUpAck{
			Server: "zen", Name: b.Name, Round: closed, BindingRound: cur.Round, View: view,
		})
		return err
	}); err != nil {
		t.Fatalf("applyCatchUpReport: %v", err)
	}

	entries, err := rt.Store.ReadLog(b.Name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	e, ok := lastReportEntry(entries, closed)
	if !ok {
		t.Fatal("no report entry for the closed round")
	}
	if !unmarkedNote(e.Note) {
		t.Errorf("report note = %q, want the server's unmarked claim to survive the catch-up", e.Note)
	}

	name, res, err := Wait(context.Background(), rt, WaitOptions{
		Names: []string{b.Name}, Round: closed,
		Timeout: time.Minute, Interval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if name != b.Name {
		t.Errorf("Wait name = %q, want %q", name, b.Name)
	}
	if res.Code != WaitUnmarked {
		t.Errorf("Wait code = %d, want %d -- a server-side unmarked close must exit 2", res.Code, WaitUnmarked)
	}
}

// TestServerMarkedCloseStillWaitsAsClosed: the same close with the server's
// note naming a marked round must keep reading as closed. Shipping the note is
// only correct while an empty one and a marked one stay distinguishable, so a
// server that says nothing must not be read as unmarked.
func TestServerMarkedCloseStillWaitsAsClosed(t *testing.T) {
	t.Parallel()

	rt, _ := seedHeadless(t, newFakeRunner())
	b := remoteClosedBinding(t, rt)
	const closed = 1
	writeRoundReport(t, rt, b, closed)
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", b.Name)
	}

	// gate=fail is an annotated marked close: the note is non-empty and
	// describes a close that did happen.
	view := remote.BindingView{ClosedRound: closed, ReportNote: "gate=fail"}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Load(b.Name)
		if err != nil {
			return err
		}
		_, err = applyCatchUpReport(context.Background(), rt, tx, cur, &catchUpAck{
			Server: "zen", Name: b.Name, Round: closed, BindingRound: cur.Round, View: view,
		})
		return err
	}); err != nil {
		t.Fatalf("applyCatchUpReport: %v", err)
	}

	entries, err := rt.Store.ReadLog(b.Name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	e, ok := lastReportEntry(entries, closed)
	if !ok {
		t.Fatal("no report entry for the closed round")
	}
	if unmarkedNote(e.Note) {
		t.Errorf("report note = %q, want a marked close not read as unmarked", e.Note)
	}

	_, res, err := Wait(context.Background(), rt, WaitOptions{
		Names: []string{b.Name}, Round: closed,
		Timeout: time.Minute, Interval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.Code != WaitClosed {
		t.Errorf("Wait code = %d, want %d -- a marked close must stay closed", res.Code, WaitClosed)
	}
}

// TestPreNoteServerStillWaitsAsClosed: a server from before the note shipped
// sends none. The round then reads as closed exactly as it always did: an
// absent field is not a claim that the round was unmarked.
func TestPreNoteServerStillWaitsAsClosed(t *testing.T) {
	t.Parallel()

	rt, _ := seedHeadless(t, newFakeRunner())
	b := remoteClosedBinding(t, rt)
	const closed = 1
	writeRoundReport(t, rt, b, closed)
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", b.Name)
	}

	view := remote.BindingView{ClosedRound: closed}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Load(b.Name)
		if err != nil {
			return err
		}
		_, err = applyCatchUpReport(context.Background(), rt, tx, cur, &catchUpAck{
			Server: "zen", Name: b.Name, Round: closed, BindingRound: cur.Round, View: view,
		})
		return err
	}); err != nil {
		t.Fatalf("applyCatchUpReport: %v", err)
	}

	_, res, err := Wait(context.Background(), rt, WaitOptions{
		Names: []string{b.Name}, Round: closed,
		Timeout: time.Minute, Interval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.Code != WaitClosed {
		t.Errorf("Wait code = %d, want %d -- a server that ships no note is not a claim of unmarked", res.Code, WaitClosed)
	}
}

// TestServedViewCarriesReportNoteAndSwitchHistory: the server side of the wire.
// The closed round's report note and its whole switch history reach the view,
// so the client has the two facts the log on the server holds and the client's
// own poll cannot reconstruct.
func TestServedViewCarriesReportNoteAndSwitchHistory(t *testing.T) {
	t.Parallel()

	const (
		reasonA = "switched builder (rate-limited: 429 too many requests): picked agy/test/m for builder: order #2"
		reasonB = "switched builder (rate-limited: 429 too many requests): picked claude/test/m for builder: order #5"
	)
	entries := []store.LogEntry{
		{Round: 1, Kind: store.KindReport, Outcome: "done", Note: "unmarked"},
		{Round: 1, Kind: store.KindSwitch, Note: reasonA},
		{Round: 1, Kind: store.KindSwitch, Note: reasonB},
		{Round: 1, Kind: store.KindSwitch, Note: nudgeNote},
		// A switch on another round must not reach round 1's history.
		{Round: 2, Kind: store.KindSwitch, Note: "switched builder (round 2): picked x/y/z for builder: order #9"},
		{Round: 2, Kind: store.KindReport, Outcome: "done"},
	}

	b := store.Binding{
		Name: "shop", Owner: "alice", Round: 2,
		Serve: &store.ServeFacts{ClosedRound: 1, AckedRound: 0},
	}
	view := ServedView(b, entries, "rec-1", "install-1")
	if view.ReportNote != "unmarked" {
		t.Errorf("ServedView.ReportNote = %q, want the round-1 report note", view.ReportNote)
	}
	if len(view.Switches) != 2 {
		t.Fatalf("ServedView.Switches = %q, want the round's two switches", view.Switches)
	}
	if view.Switches[0] != reasonA || view.Switches[1] != reasonB {
		t.Errorf("ServedView.Switches = %q, want them in log order", view.Switches)
	}

	// The same facts for any closed round, so a chain member's round carries
	// them too.
	facts := servedRoundFacts(entries, 2)
	if facts.ReportNote != "done" && facts.ReportNote != "" {
		t.Errorf("round 2 ReportNote = %q, want round 2's own note", facts.ReportNote)
	}
	if len(facts.Switches) != 1 || facts.Switches[0] != "switched builder (round 2): picked x/y/z for builder: order #9" {
		t.Errorf("round 2 Switches = %q, want only round 2's switch", facts.Switches)
	}
}

// TestServerSwitchHistoryReachesTheReportPayload: the round took a switch for a
// reason, rotated, and came back to its first candidate -- three switches whose
// reasons live only on the server. The catch-up's report payload carries all of
// them, in order, as the switch lines a local close renders.
func TestServerSwitchHistoryReachesTheReportPayload(t *testing.T) {
	t.Parallel()

	const (
		reasonA = "switched builder (rate-limited: 429 too many requests): picked agy/test/m for builder: order #2"
		reasonB = "switched builder (rate-limited: 429 too many requests): picked claude/test/m for builder: order #5"
		reasonC = "switched builder (rate-limited: 429 too many requests): picked agy/test/m for builder: order #2"
	)
	b := store.Binding{Name: "webshop"}
	b.Builder.Server = "zen"
	view := remote.BindingView{
		ClosedRound: 1,
		Switches:    []string{reasonA, reasonB, reasonC},
	}

	payload, _ := catchUpPayload(b, view, true, "Report: done")

	lines := switchPayloadLines(payload)
	if len(lines) != 3 {
		t.Fatalf("switch lines = %q, want every switch the round took", lines)
	}
	if lines[0] != "Switch: "+reasonA || lines[1] != "Switch: "+reasonB || lines[2] != "Switch: "+reasonC {
		t.Errorf("switch lines = %q, want them in the order the round took them", lines)
	}
}

// TestARoundWithNoServerSwitchesIsUnchanged: the switch line is the whole
// compatibility claim for a round that never switched, so the payload with no
// server switch history is pinned against the exact string it produces -- a
// client whose server sends nothing gets the same payload as before.
func TestARoundWithNoServerSwitchesIsUnchanged(t *testing.T) {
	t.Parallel()

	b := store.Binding{Name: "webshop"}
	b.Builder.Server = "zen"
	view := remote.BindingView{ClosedRound: 1}

	payload, note := catchUpPayload(b, view, true, "Report: done")
	const want = "The runner finished round 1 on zen. Report: done"
	if payload != want {
		t.Errorf("payload = %q, want %q -- a round with no switch must be unchanged", payload, want)
	}
	if note != "" {
		t.Errorf("note = %q, want empty", note)
	}
	if lines := switchPayloadLines(payload); len(lines) != 0 {
		t.Errorf("switch lines = %q, want none", lines)
	}
}

// TestServerSwitchLinesFollowTheDiff: the server's switch lines ride after the
// diff's summary lines, the same place a local close puts them, so a reader of
// a remote close sees its facts in the order a local one reads.
func TestServerSwitchLinesFollowTheDiff(t *testing.T) {
	t.Parallel()

	b := store.Binding{Name: "webshop"}
	b.Builder.Server = "zen"
	view := remote.BindingView{
		ClosedRound: 1,
		DiffNote:    "3 files changed, 42 insertions(+), 7 deletions(-)",
		DiffCommits: 1,
		DiffTree:    "clean",
		Switches:    []string{"switched builder (rate-limited: 429): picked agy/test/m for builder: order #2"},
	}

	payload, _ := catchUpPayload(b, view, true, "Report: done")

	lines := strings.Split(payload, "\n")
	diffAt, switchAt := -1, -1
	for i, line := range lines {
		if strings.Contains(line, "files changed") && diffAt < 0 {
			diffAt = i
		}
		if strings.HasPrefix(line, "Switch: ") && switchAt < 0 {
			switchAt = i
		}
	}
	if diffAt < 0 || switchAt < 0 {
		t.Fatalf("payload = %q, want both a diff line and a switch line", payload)
	}
	if switchAt < diffAt {
		t.Errorf("switch line at %d precedes the diff line at %d in %q, want the switch after the diff", switchAt, diffAt, payload)
	}
}
