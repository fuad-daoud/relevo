package synclog

import (
	"testing"
)

func TestEntryValid(t *testing.T) {
	t.Parallel()

	// Nil term allows any sequence.
	if !EntryValid(nil, Entry{Seq: 100}) {
		t.Fatal("nil term rejected entry")
	}

	term := &Term{
		RootID: "root-1",
		Writer: "writer-1",
		Upto:   10,
	}

	// At or below upto is valid.
	if !EntryValid(term, Entry{Origin: "writer-1", Seq: 10}) {
		t.Error("Seq == upto should be valid")
	}
	if !EntryValid(term, Entry{Origin: "writer-1", Seq: 5}) {
		t.Error("Seq < upto should be valid")
	}

	// Above upto without regrant is invalid.
	if EntryValid(term, Entry{Origin: "writer-1", Seq: 11}) {
		t.Error("Seq > upto without regrant should be invalid")
	}

	// Regrant from another origin via After reference.
	regrantTerm := &Term{
		RootID:        "root-1",
		Writer:        "writer-1",
		Upto:          10,
		RegrantOrigin: "taker-1",
		RegrantSeq:    5,
	}
	// After matches regrant exactly.
	withMatchingAfter := Entry{
		Origin: "writer-1",
		Seq:    15,
		After:  &Ref{Origin: "taker-1", Seq: 5},
	}
	if !EntryValid(regrantTerm, withMatchingAfter) {
		t.Error("entry with After matching regrant should be valid")
	}

	// After does not match regrant.
	withDiffAfter := Entry{
		Origin: "writer-1",
		Seq:    15,
		After:  &Ref{Origin: "taker-1", Seq: 6},
	}
	if EntryValid(regrantTerm, withDiffAfter) {
		t.Error("entry with mismatched After should be invalid")
	}

	// Regrant from same origin: new entries past regrant seq are valid.
	selfRegrantTerm := &Term{
		RootID:        "root-1",
		Writer:        "writer-1",
		Upto:          10,
		RegrantOrigin: "writer-1",
		RegrantSeq:    20,
	}
	// Between upto and regrant seq (partition-era entries) remain invalid.
	partitionEntry := Entry{
		Origin: "writer-1",
		Seq:    15,
	}
	if EntryValid(selfRegrantTerm, partitionEntry) {
		t.Error("partition entry between upto and regrant seq should be invalid")
	}
	// Past regrant seq is valid.
	newEntry := Entry{
		Origin: "writer-1",
		Seq:    21,
	}
	if !EntryValid(selfRegrantTerm, newEntry) {
		t.Error("entry past self-regrant seq should be valid")
	}
}
