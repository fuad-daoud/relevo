package syncworker

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/synclog"
)

// TestReadMarksReportsTheEpochCuts pins the read side of the cuts: a rekey
// writes one row per origin, and a read reports each verbatim, so the reader
// sees the cut the log holds and verifies its tag itself.
func TestReadMarksReportsTheEpochCuts(t *testing.T) {
	path := tempReplica(t)
	b := backendFor(t, &localDriver{path: path}, "origin-a")
	tag := encodeKeyValue(synclog.EpochCutTag([]byte("k"), "origin-b", 2, 7))
	other := encodeKeyValue(synclog.EpochCutTag([]byte("k"), "origin-c", 2, 4))
	execOn(t, path,
		"INSERT INTO meta (key, value) VALUES ('"+epochCutPrefix+"origin-b', '2 7 "+tag+"')",
		"INSERT INTO meta (key, value) VALUES ('"+epochCutPrefix+"origin-c', '2 4 "+other+"')")

	state, err := b.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	cut, ok := state.EpochCuts["origin-b"]
	if !ok || cut.Epoch != 2 || cut.Seq != 7 {
		t.Fatalf("epoch_cuts = %+v, want origin-b at epoch 2, seq 7", state.EpochCuts)
	}
	if !synclog.VerifyEpochCut([]byte("k"), "origin-b", 2, 7, cut.Tag) {
		t.Errorf("origin-b's tag does not verify under the key that wrote it")
	}
	if got := state.EpochCuts["origin-c"].Seq; got != 4 {
		t.Errorf("origin-c = %d, want 4: one row must not hide the other", got)
	}
}

// TestReadMarksReportsAMalformedCutRowAsUnverifiable pins the malformed row's
// shape: not dropped, not an error -- a cut of epoch 0 and sequence 0 with no
// tag, which no key verifies under, so the reader holds that origin the way it
// holds a boundary whose tag fails.
func TestReadMarksReportsAMalformedCutRowAsUnverifiable(t *testing.T) {
	path := tempReplica(t)
	b := backendFor(t, &localDriver{path: path}, "origin-a")
	good := encodeKeyValue(synclog.EpochCutTag([]byte("k"), "origin-d", 2, 9))
	execOn(t, path,
		"INSERT INTO meta (key, value) VALUES ('"+epochCutPrefix+"origin-a', 'not a cut')",
		"INSERT INTO meta (key, value) VALUES ('"+epochCutPrefix+"origin-c', '2 7 wrong-tag')",
		"INSERT INTO meta (key, value) VALUES ('"+epochCutPrefix+"origin-d', '2 9 "+good+"')")

	state, err := b.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	if zero := (synclog.EpochCut{}); state.EpochCuts["origin-a"].Epoch != zero.Epoch || state.EpochCuts["origin-a"].Seq != zero.Seq || len(state.EpochCuts["origin-a"].Tag) != 0 {
		t.Errorf("origin-a = %+v, want the unparseable zero cut", state.EpochCuts["origin-a"])
	}
	if zero := (synclog.EpochCut{}); state.EpochCuts["origin-c"].Epoch != zero.Epoch || state.EpochCuts["origin-c"].Seq != zero.Seq || len(state.EpochCuts["origin-c"].Tag) != 0 {
		t.Errorf("origin-c = %+v, want a tag that does not decode to read as none", state.EpochCuts["origin-c"])
	}
	if got := state.EpochCuts["origin-d"]; got.Epoch != 2 || got.Seq != 9 {
		t.Errorf("origin-d = %+v, want the row the log holds", got)
	}
	if !synclog.VerifyEpochCut([]byte("k"), "origin-d", 2, 9, got3(t, state, "origin-d")) {
		t.Errorf("origin-d's tag does not verify under the key that wrote it")
	}
}

// got3 is the tag a read answered with, pulled out so the verify above reads
// as one line.
func got3(t *testing.T, state MarkState, origin string) []byte {
	t.Helper()
	cut, ok := state.EpochCuts[origin]
	if !ok {
		return nil
	}
	return cut.Tag
}
