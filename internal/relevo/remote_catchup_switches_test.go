package relevo

import (
	"context"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestCatchUpRecordsTheServersSwitchesOnTheClosedRound pins that a mirrored
// round keeps the whole switch history the server shipped as log entries of that
// round, so the history record counts every leg and names the builder that
// finished it.
func TestCatchUpRecordsTheServersSwitchesOnTheClosedRound(t *testing.T) {
	t.Parallel()

	rt, _ := seedHeadless(t, newFakeRunner())
	b := remoteClosedBinding(t, rt)
	const closed = 1
	writeRoundReport(t, rt, b, closed)
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	first := "switched builder (rate-limited): picked opencode/zen/b for builder: order #2"
	second := "switched builder (rate-limited): picked claude/anthropic/c for builder: order #3"
	view := remote.BindingView{ClosedRound: closed, Switches: []string{first, second}}
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
	var got []string
	for _, e := range entries {
		if e.Kind == store.KindSwitch && e.Round == closed {
			got = append(got, e.Note)
		}
	}
	if len(got) != 2 || got[0] != first || got[1] != second {
		t.Errorf("switch entries of round %d = %q, want the server's two switches in order", closed, got)
	}
	rep, ok := lastReportEntry(entries, closed)
	if !ok {
		t.Fatal("no report entry for the closed round")
	}
	if n := strings.Count(rep.Payload, "picked opencode/zen/b"); n != 1 {
		t.Errorf("report payload names the first switch %d times, want once", n)
	}
}
