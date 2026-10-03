package relevo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// switchPayloadLines returns the report payload's switch lines, in order. It
// reads the payload the way the mastermind does -- as text -- so it pins the
// shape a reader sees rather than any internal slice.
func switchPayloadLines(payload string) []string {
	var out []string
	for _, line := range strings.Split(payload, "\n") {
		if strings.HasPrefix(line, "Switch: ") {
			out = append(out, line)
		}
	}
	return out
}

// closeSwitchedRound seeds a headless binding whose log already carries the
// given to_planner switch entries for round b.Round, writes the round's report
// body, and closes the round through queueReport -- the seam the switch line is
// assembled in. It returns the round's report entry.
//
// The switch entries are appended rather than produced by switchBuilder on
// purpose: this pins the report line, and the note shapes the line reads are
// the ones switchBuilder, the relaunch path, the nudge path and the remote path
// write. Every one of them is seeded here verbatim.
func closeSwitchedRound(t *testing.T, switchNotes ...string) (Runtime, store.LogEntry) {
	t.Helper()

	fr := newFakeRunner()
	rt, b := seedHeadless(t, fr)
	b.Builder.PID = 9001
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", b.Name)
	}
	if err := os.MkdirAll(filepath.Dir(rt.Store.ReportPath(b.Name, b.Round)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.Store.ReportPath(b.Name, b.Round), []byte("report body"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		for _, note := range switchNotes {
			if err := tx.AppendLog(b.Name, store.LogEntry{
				Round: b.Round, Direction: store.DirToMasterMind,
				Kind: store.KindSwitch, Confirmed: true, Note: note,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed switch entries: %v", err)
	}

	var report store.LogEntry
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Load(b.Name)
		if err != nil {
			return err
		}
		entries, err := tx.ReadLog(b.Name)
		if err != nil {
			return err
		}
		next, err := queueReport(context.Background(), rt, tx, cur, entries,
			rt.Store.ReportPath(b.Name, b.Round), "done", "", nil, nil, nil, nil, "", false, scopeVerdict{})
		if err != nil {
			return err
		}
		if err := tx.Save(next); err != nil {
			return err
		}
		after, err := tx.ReadLog(b.Name)
		if err != nil {
			return err
		}
		for _, e := range after {
			if e.Kind == store.KindReport {
				report = e
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("close round: %v", err)
	}
	if report.Round == 0 {
		t.Fatal("no report entry written")
	}
	return rt, report
}

// claimableKinds returns the kinds still claimable for the mastermind, so a
// test can assert what a delivery left behind rather than only what it printed.
func claimableKinds(t *testing.T, rt Runtime, name string) []store.Kind {
	t.Helper()
	var kinds []store.Kind
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		pending, err := tx.ClaimableForMasterMindThrough(name, 0)
		if err != nil {
			return err
		}
		for _, p := range pending {
			kinds = append(kinds, p.Entry.Kind)
		}
		return nil
	}); err != nil {
		t.Fatalf("ClaimableForMasterMindThrough: %v", err)
	}
	return kinds
}

// TestQueueReportNamesASwitchItClosedMidRound: a switch is status-only, so its
// trace is the closing report's line -- the report payload names the switch,
// and no switch entry is left claimable for the mastermind.
func TestQueueReportNamesASwitchItClosedMidRound(t *testing.T) {
	t.Parallel()

	const note = "switched builder (rate-limited: 429 too many requests): picked agy/test/m for builder: order #2"
	rt, report := closeSwitchedRound(t, note)

	if !strings.Contains(report.Payload, "Switch: "+note) {
		t.Errorf("payload %q, want it to carry the switch line", report.Payload)
	}

	// The switch entry itself is confirmed, so it was never a payload: the
	// report is the only thing wait has to deliver.
	before := claimableKinds(t, rt, "webshop")
	if len(before) != 1 || before[0] != store.KindReport {
		t.Fatalf("claimable before wait = %v, want exactly the report", before)
	}

	name, res, err := Wait(context.Background(), rt, WaitOptions{
		Names: []string{"webshop"}, Round: report.Round,
		Timeout: time.Minute, Interval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if name != "webshop" || res.Code != WaitClosed || !res.Done {
		t.Fatalf("Wait = (%q, %+v), want the round closed", name, res)
	}
	if len(res.Delivered) != 1 {
		t.Fatalf("Delivered = %d entries, want one delivery: the report", len(res.Delivered))
	}
	if !strings.Contains(res.Payload, "Switch: "+note) {
		t.Errorf("delivered payload %q, want the switch line in the one delivery", res.Payload)
	}
	if res.DeliverErr != nil {
		t.Errorf("DeliverErr = %v, want nil", res.DeliverErr)
	}

	after := claimableKinds(t, rt, "webshop")
	if len(after) != 0 {
		t.Errorf("claimable after wait = %v, want nothing left claimable", after)
	}
}

// TestQueueReportNamesEverySwitchInOrder: a round that switched twice reports
// both, in the order the log holds them.
func TestQueueReportNamesEverySwitchInOrder(t *testing.T) {
	t.Parallel()

	const (
		first  = "switched builder (rate-limited: 429 too many requests): picked agy/test/m for builder: order #2"
		second = "switched builder (exited (code 1) without a report): picked claude/test/m for builder: order #5"
	)
	_, report := closeSwitchedRound(t, first, second)

	lines := switchPayloadLines(report.Payload)
	if len(lines) != 2 {
		t.Fatalf("switch lines = %q, want exactly two in order", lines)
	}
	if lines[0] != "Switch: "+first || lines[1] != "Switch: "+second {
		t.Errorf("switch lines = %q, want them in log order", lines)
	}
}

// TestQueueReportLeavesAPayloadWithNoSwitchUnchanged: a round that never
// switched carries no switch line at all, so the payload a round with no switch
// produces is byte-for-byte the payload this produced before the switch line
// existed. That is the whole compatibility claim of this round, so it is pinned
// on the exact string rather than on a substring.
func TestQueueReportLeavesAPayloadWithNoSwitchUnchanged(t *testing.T) {
	t.Parallel()

	_, report := closeSwitchedRound(t)

	// The stored payload verbatim: the delivery envelope's header, the blank
	// line, then the caller's payload. Nothing is appended for a round that
	// never switched, so this string is what a reader would have got before.
	const want = "relevo: round 1 · to MasterMind · about runner \"webshop\" (not the human)\n\ndone"
	if report.Payload != want {
		t.Errorf("payload = %q, want %q -- a round with no switch must be unchanged", report.Payload, want)
	}
	if lines := switchPayloadLines(report.Payload); len(lines) != 0 {
		t.Errorf("switch lines = %q, want none", lines)
	}
}