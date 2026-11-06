package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestRevisionTracksEveryMirrorSignal pins the contract the daemon's skip
// rests on: every write that changes what a mirror reads changes the revision,
// and a store nothing wrote to keeps it.
func TestRevisionTracksEveryMirrorSignal(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	b := Binding{Name: "webshop", CWD: "/repo", State: StateActive, Round: 1}
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	base, err := s.Revision("webshop")
	if err != nil {
		t.Fatalf("Revision: %v", err)
	}
	if again, err := s.Revision("webshop"); err != nil || again != base {
		t.Fatalf("Revision on an untouched store = %q, %v; want %q", again, err, base)
	}

	// An appended log line is a bigger log.
	if err := s.AppendLog("webshop", LogEntry{Round: 1, Direction: DirToBuilder, Kind: KindPrompt}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
	afterLog, err := s.Revision("webshop")
	if err != nil {
		t.Fatalf("Revision after log: %v", err)
	}
	if afterLog == base {
		t.Error("an appended log entry left the revision unchanged")
	}

	// A saved binding is a changed record.
	b.State = StateDone
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	afterSave, err := s.Revision("webshop")
	if err != nil {
		t.Fatalf("Revision after save: %v", err)
	}
	if afterSave == afterLog {
		t.Error("a saved binding left the revision unchanged")
	}

	// A new round file appears on disk before it is a row.
	if err := os.WriteFile(filepath.Join(s.Dir("webshop"), "001-plan.md"), []byte("plan"), 0o644); err != nil {
		t.Fatalf("write round file: %v", err)
	}
	afterFile, err := s.Revision("webshop")
	if err != nil {
		t.Fatalf("Revision after round file: %v", err)
	}
	if afterFile == afterSave {
		t.Error("a new round file left the revision unchanged")
	}

	// A sealed round file becomes a row with no file on disk behind it, so it
	// must move the revision on its own: the row's own extent is a signal.
	if err := s.WithLock(func(tx *Tx) error {
		return tx.PutRoundFile("webshop", 1, filepath.Join(s.Dir("webshop"), "001-sealed.md"), []byte("sealed"))
	}); err != nil {
		t.Fatalf("PutRoundFile: %v", err)
	}
	afterSeal, err := s.Revision("webshop")
	if err != nil {
		t.Fatalf("Revision after sealed row: %v", err)
	}
	if afterSeal == afterFile {
		t.Error("a sealed round-file row left the revision unchanged")
	}
}

// TestRevisionIgnoresAnOldTranscriptLocatorKey pins that a record still
// carrying the removed planner.transcript_locator key does not make the
// revision follow that file: the key is dead bytes, not a mirror signal.
func TestRevisionIgnoresAnOldTranscriptLocatorKey(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(transcript, []byte("one\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	if err := s.Save(Binding{Name: "webshop", CWD: "/repo", State: StateActive, Round: 1}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	d, err := s.dbForRead()
	if err != nil {
		t.Fatalf("dbForRead: %v", err)
	}
	rec, ok, err := d.RecordGet("", "webshop")
	if err != nil || !ok {
		t.Fatalf("RecordGet = (ok %v, err %v), want the row", ok, err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(rec.JSON), &doc); err != nil {
		t.Fatalf("decode record_json: %v", err)
	}
	planner, _ := doc["planner"].(map[string]any)
	if planner == nil {
		planner = map[string]any{}
	}
	planner["transcript_locator"] = transcript
	doc["planner"] = planner
	updated, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal record_json: %v", err)
	}
	rec.JSON = string(updated)
	if _, err := d.RecordPut(rec); err != nil {
		t.Fatalf("RecordPut: %v", err)
	}

	before, err := s.Revision("webshop")
	if err != nil {
		t.Fatalf("Revision: %v", err)
	}
	f, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open transcript: %v", err)
	}
	if _, err := f.WriteString("two\n"); err != nil {
		t.Fatalf("append transcript: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}

	after, err := s.Revision("webshop")
	if err != nil {
		t.Fatalf("Revision after transcript append: %v", err)
	}
	if after != before {
		t.Error("a stale planner.transcript_locator moved the revision")
	}
}

// TestRevisionMissingBinding pins that a name with no record is not a failure
// to report: the caller has nothing to mirror.
func TestRevisionMissingBinding(t *testing.T) {
	s := New(t.TempDir())
	if _, err := s.Revision("nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Revision(nobody) err = %v, want ErrNotFound", err)
	}
}

// TestTickStateCarriesTheDigestAndTheInPlaceStates pins the one read the
// daemon's done-skip guards on: Rev is Revision's own digest, and the two
// states that settle by patching an entry in place move AdmitsOpen while the
// digest stands still -- the same blindness the mirror's own doc names, which
// the daemon must not inherit.
func TestTickStateCarriesTheDigestAndTheInPlaceStates(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	if err := s.Save(Binding{Name: "webshop", CWD: "/repo", State: StateDone, Round: 1}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	read := func() TickState {
		t.Helper()
		st, err := s.TickState("webshop")
		if err != nil {
			t.Fatalf("TickState: %v", err)
		}
		rev, err := s.Revision("webshop")
		if err != nil {
			t.Fatalf("Revision: %v", err)
		}
		if st.Rev != rev {
			t.Fatalf("TickState.Rev = %q, Revision = %q, want one digest", st.Rev, rev)
		}
		return st
	}

	if st := read(); st.AdmitsOpen != 0 || st.RoundsOnDisk != 0 {
		t.Fatalf("quiet store TickState = %+v, want no open admit and no round on disk", st)
	}

	if err := s.AppendLog("webshop", LogEntry{Round: 1, Direction: DirToMasterMind, Kind: KindReport, Payload: "the report"}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
	pending := read()
	if pending.AdmitsOpen != 0 {
		t.Errorf("a pending unadmitted entry reads AdmitsOpen %d, want 0: another route can still take it", pending.AdmitsOpen)
	}

	if err := s.AdmitIndex("webshop", 0); err != nil {
		t.Fatalf("AdmitIndex: %v", err)
	}
	admitted := read()
	if admitted.AdmitsOpen != 1 {
		t.Errorf("AdmitsOpen = %d, want the open admit counted: no other route can take it", admitted.AdmitsOpen)
	}
	if admitted.Rev != pending.Rev {
		t.Error("the in-place admit moved the digest")
	}

	if err := s.ConfirmIndex("webshop", 0, "wait"); err != nil {
		t.Fatalf("ConfirmIndex: %v", err)
	}
	if st := read(); st.AdmitsOpen != 0 {
		t.Errorf("AdmitsOpen = %d after the confirm, want 0: the admit no longer needs a tick", st.AdmitsOpen)
	}

	// A stamp on a builder-bound entry is not the mastermind's queue.
	if err := s.AppendLog("webshop", LogEntry{Round: 1, Direction: DirToBuilder, Kind: KindPrompt}); err != nil {
		t.Fatalf("AppendLog prompt: %v", err)
	}
	if err := s.AdmitIndex("webshop", 1); err != nil {
		t.Fatalf("AdmitIndex prompt: %v", err)
	}
	if st := read(); st.AdmitsOpen != 0 {
		t.Errorf("AdmitsOpen = %d with a builder-bound admit, want 0: the count is mastermind-bound entries", st.AdmitsOpen)
	}

	// A round whose files still wait on disk is the seal's worklist.
	if err := os.WriteFile(filepath.Join(s.Dir("webshop"), "001-report.md"), []byte("r"), 0o644); err != nil {
		t.Fatalf("write round file: %v", err)
	}
	if st := read(); st.RoundsOnDisk != 1 {
		t.Errorf("RoundsOnDisk = %d, want the round whose file still waits to seal", st.RoundsOnDisk)
	}
}

// TestTickStateMissingBinding pins the same nothing-to-guard answer Revision
// gives: no record is not a failure to report.
func TestTickStateMissingBinding(t *testing.T) {
	s := New(t.TempDir())
	if _, err := s.TickState("nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("TickState(nobody) err = %v, want ErrNotFound", err)
	}
}
