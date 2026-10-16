package syncworker

import (
	"database/sql"
	"errors"
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

// TestKeyCutRaiseGuardWorksOffTheStoredRow pins the raise guard reading the
// stored row rather than the caller's word: a second vouch at or inside the
// stored fence is answered as a success without writing, one that raises over
// it replaces the stored row whole, and one nothing could have written --
// no epoch, no tag -- is refused rather than stored.
func TestKeyCutRaiseGuardWorksOffTheStoredRow(t *testing.T) {
	path := tempReplica(t)
	driver := &localDriver{path: path}
	b := backendFor(t, driver, "origin-a")
	if _, err := b.Append([]Entry{
		upsertEntry("origin-a", "board", `["r"]`, `{"title":"one"}`),
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	good := EpochCut{Epoch: 2, Seq: 1, Tag: synclog.EpochCutTag([]byte("k2"), "origin-a", 2, 1)}
	if err := b.PublishEpochCut(good); err != nil {
		t.Fatalf("the first vouch: %v", err)
	}
	// A vouch at exactly the stored fence raises nothing and answers as a
	// success, so a machine whose extension is already on the remote does not
	// re-write it on every attempt.
	if err := b.PublishEpochCut(good); err != nil {
		t.Fatalf("the same vouch repeated: %v", err)
	}
	if value, _ := cutMeta(t, path, "origin-a"); value != "2 1 "+encodeKeyValue(good.Tag) {
		t.Fatalf("returned fence = %q, want the stored one unchanged", value)
	}

	// A vouch that raises replaces the row whole.
	raised := EpochCut{Epoch: 3, Seq: 1, Tag: synclog.EpochCutTag([]byte("k3"), "origin-a", 3, 1)}
	if err := b.PublishEpochCut(raised); err != nil {
		t.Fatalf("the raising vouch: %v", err)
	}
	if value, _ := cutMeta(t, path, "origin-a"); value != "3 1 "+encodeKeyValue(raised.Tag) {
		t.Fatalf("fence = %q, want the raised row", value)
	}

	// And a vouch nothing could have written is refused before the write.
	refused := EpochCut{Epoch: 4, Seq: 1}
	if err := b.PublishEpochCut(refused); err == nil {
		t.Error("a vouch with no tag came back as a success")
	}
}

// cutMeta reads the stored cut row for origin, ok false below the first vouch.
func cutMeta(t *testing.T, path, origin string) (string, bool) {
	t.Helper()
	d := openAt(t, path)
	defer func() { _ = d.Close() }()
	var value string
	err := d.QueryRow("SELECT value FROM meta WHERE key = ?", epochCutPrefix+origin).Scan(&value)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", false
		}
		t.Fatalf("read the cut row for %s: %v", origin, err)
	}
	return value, true
}

// TestKeyCutRefusesATransferThatFails pins the two transfers it sits between:
// the pull that makes the head this attempt's remote and the push that lands
// the vouch. A transfer that refuses leaves the meta as it was.
func TestKeyCutRefusesATransferThatFails(t *testing.T) {
	path := tempReplica(t)
	driver := &localDriver{path: path, pullErr: errors.New("the pull does not answer")}
	b := backendFor(t, driver, "origin-a")
	if _, err := b.Append([]Entry{
		upsertEntry("origin-a", "board", `["t"]`, `{"title":"one"}`),
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	cut := EpochCut{Epoch: 2, Seq: 1, Tag: synclog.EpochCutTag([]byte("k"), "origin-a", 2, 1)}
	if err := b.PublishEpochCut(cut); err == nil {
		t.Error("a vouch over a pull that refused came back as a success")
	}
	if _, ok := cutRowHeld(t, path, "origin-a"); ok {
		t.Error("a refused pull wrote a cut row")
	}

	driver.pullErr = nil
	driver.pushErr = errors.New("the push does not answer")
	if err := b.PublishEpochCut(cut); err == nil {
		t.Error("a vouch landed by a push that refused came back as a success")
	}
}

// cutRowHeld reads the stored cut row through a fresh handle.
func cutRowHeld(t *testing.T, path, origin string) (string, bool) {
	t.Helper()
	return cutMeta(t, path, origin)
}
