package relevo

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// TestServedRoundStampsLocalRoundUnchanged pins the admit-only reading that
// chain members and clients already depend on: the newest admit answers for
// a local round, and a round nobody queued keeps its prompt handoff as its
// start with no wait.
func TestServedRoundStampsLocalRoundUnchanged(t *testing.T) {
	base := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	entries := []store.LogEntry{
		{TS: base, Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt},
		{TS: base.Add(time.Second), Round: 1, Direction: store.DirToMasterMind, Kind: store.KindQueue, Confirmed: true, Note: "queued (2/2 builders busy)"},
		{TS: base.Add(90 * time.Second), Round: 1, Direction: store.DirToMasterMind, Kind: store.KindQueue, Confirmed: true, Note: "started after 1m29s queued"},
		{TS: base.Add(3 * time.Minute), Round: 1, Direction: store.DirToMasterMind, Kind: store.KindQueue, Confirmed: true, Note: "started after 1m0s queued"},
	}
	start, queued := servedRoundStamps(entries, 1)
	if !start.Equal(base.Add(3 * time.Minute)) {
		t.Errorf("start = %v, want the newest admit at +3m", start)
	}
	if !queued.Equal(base.Add(time.Second)) {
		t.Errorf("queued = %v, want the accept at +1s", queued)
	}

	direct := []store.LogEntry{
		{TS: base, Round: 2, Direction: store.DirToBuilder, Kind: store.KindPrompt},
	}
	start, queued = servedRoundStamps(direct, 2)
	if !start.Equal(base) || !queued.IsZero() {
		t.Errorf("direct round stamps = %v/%v, want the handoff with no queue stamp", start, queued)
	}
}
