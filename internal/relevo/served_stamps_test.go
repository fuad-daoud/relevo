package relevo

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// TestServedRoundStampsSendNoteIsStart pins that a forwarded round reports
// its confirmed send as its start, not the prompt handoff: a client reading
// the head sees when the round really began on its node. Before the send
// note counted, the handoff was the start and the wait read as the queue
// entry's own delay.
func TestServedRoundStampsSendNoteIsStart(t *testing.T) {
	accept := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	send := accept.Add(2 * time.Minute)
	handoff := accept.Add(time.Second)
	entries := []store.LogEntry{
		{TS: handoff, Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt},
		{TS: accept, Round: 1, Direction: store.DirToMasterMind, Kind: store.KindQueue, Confirmed: true, Note: "queued (1/2 builders busy)"},
		{TS: send, Round: 1, Direction: store.DirToMasterMind, Kind: store.KindQueue, Confirmed: true, Note: "forwarded to b1 (cold)"},
	}
	start, queued := servedRoundStamps(entries, 1)
	if !start.Equal(send) {
		t.Errorf("start = %v, want the send note at %v, not the handoff", start, send)
	}
	if !queued.Equal(accept) {
		t.Errorf("queued = %v, want the accept at %v", queued, accept)
	}
}

// TestServedRoundStampsAdoptedNoteIsStart pins the restart shape: an adopted
// round's start is the claim time its note carries, so the client's wait ends
// at when the node took the round, not at when the head noticed it survived.
func TestServedRoundStampsAdoptedNoteIsStart(t *testing.T) {
	accept := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	claim := accept.Add(30 * time.Second)
	entries := []store.LogEntry{
		{TS: accept, Round: 1, Direction: store.DirToMasterMind, Kind: store.KindQueue, Confirmed: true, Note: "queued (1/2 builders busy)"},
		{TS: claim, Round: 1, Direction: store.DirToMasterMind, Kind: store.KindQueue, Confirmed: true, Note: "forwarded to b1 (adopted)"},
	}
	start, queued := servedRoundStamps(entries, 1)
	if !start.Equal(claim) {
		t.Errorf("start = %v, want the claim at %v", start, claim)
	}
	if !queued.Equal(accept) {
		t.Errorf("queued = %v, want the accept at %v", queued, accept)
	}
}

// TestServedRoundStampsNewestAdmitOrSendWins pins the single rule: whichever
// start note -- admit or send -- is newest answers for the round, so a round
// that went out and came back and ran locally reports the later of the two,
// exactly as an admit-only round reports its newest admit today.
func TestServedRoundStampsNewestAdmitOrSendWins(t *testing.T) {
	base := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	t.Run("admit after send", func(t *testing.T) {
		entries := []store.LogEntry{
			{TS: base, Round: 1, Direction: store.DirToMasterMind, Kind: store.KindQueue, Confirmed: true, Note: "queued (1/2 builders busy)"},
			{TS: base.Add(time.Minute), Round: 1, Direction: store.DirToMasterMind, Kind: store.KindQueue, Confirmed: true, Note: "forwarded to b1 (cold)"},
			{TS: base.Add(3 * time.Minute), Round: 1, Direction: store.DirToMasterMind, Kind: store.KindQueue, Confirmed: true, Note: "started after 2m0s queued"},
		}
		start, _ := servedRoundStamps(entries, 1)
		if !start.Equal(base.Add(3 * time.Minute)) {
			t.Errorf("start = %v, want the admit at +3m", start)
		}
	})
	t.Run("send after admit", func(t *testing.T) {
		entries := []store.LogEntry{
			{TS: base, Round: 1, Direction: store.DirToMasterMind, Kind: store.KindQueue, Confirmed: true, Note: "queued (1/2 builders busy)"},
			{TS: base.Add(time.Minute), Round: 1, Direction: store.DirToMasterMind, Kind: store.KindQueue, Confirmed: true, Note: "started after 1m0s queued"},
			{TS: base.Add(4 * time.Minute), Round: 1, Direction: store.DirToMasterMind, Kind: store.KindQueue, Confirmed: true, Note: "forwarded to b1 (warm)"},
		}
		start, _ := servedRoundStamps(entries, 1)
		if !start.Equal(base.Add(4 * time.Minute)) {
			t.Errorf("start = %v, want the send at +4m", start)
		}
	})
}

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
