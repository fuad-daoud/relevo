package syncworker

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/synclog"
)

// TestKeyCutStoresTheVouch pins the verb's write: the origin comes from
// hello, the vouch lands in the log's meta in the shape every reader reads,
// and a read answers with it.
func TestKeyCutStoresTheVouch(t *testing.T) {
	path := tempReplica(t)
	driver := &localDriver{path: path}
	b := backendFor(t, driver, "origin-a")
	if _, err := b.Append([]Entry{
		upsertEntry("origin-a", "board", `["k"]`, `{"title":"one"}`),
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	s := startWorker(t, b)
	if resp := s.ask(helloRequest("h")); !resp.OK {
		t.Fatalf("hello: %+v", resp)
	}
	cut := synclog.EpochCut{
		Epoch: 2, Seq: 1,
		Tag: synclog.EpochCutTag([]byte("k"), "origin-a", 2, 1),
	}
	resp := s.ask(Request{ID: "c1", Verb: VerbKeyCut, Cut: outEpochCut(cut)})
	if !resp.OK {
		t.Fatalf("key_cut = %+v, want it carried out", resp)
	}
	// The origin named is the worker's own from hello, and never another's:
	// no other machine's cut may be written from this pipe.
	if resp := s.ask(Request{ID: "c2", Verb: VerbKeyCut}); resp.OK {
		t.Error("key_cut with no cut came back as a success")
	}
	// A vouch past the head vouches for history nobody appended.
	resp = s.ask(Request{ID: "c3", Verb: VerbKeyCut, Cut: outEpochCut(synclog.EpochCut{Epoch: 2, Seq: 9, Tag: cut.Tag})})
	if resp.OK {
		t.Error("key_cut past the log's head came back as a success")
	}

	// The read goes through the verb, which answers every row the meta holds.
	reply := s.ask(Request{ID: "r1", Verb: VerbReadMarks})
	if !reply.OK {
		t.Fatalf("read_marks = %+v, want it carried out", reply)
	}
	held, ok := reply.Marks.EpochCuts["origin-a"]
	if !ok || held.Epoch != cut.Epoch || held.Seq != cut.Seq {
		t.Fatalf("read_marks epoch_cuts = %+v, want origin-a at (%d, %d)", reply.Marks.EpochCuts, cut.Epoch, cut.Seq)
	}
	if !synclog.VerifyEpochCut([]byte("k"), "origin-a", cut.Epoch, cut.Seq, held.Tag) {
		t.Errorf("the stored vouch's tag does not read back as the one sent")
	}
}

// outEpochCut is the cut as the request frame carries it.
func outEpochCut(cut synclog.EpochCut) *EpochCut {
	return &EpochCut{Epoch: cut.Epoch, Seq: cut.Seq, Tag: cut.Tag}
}
