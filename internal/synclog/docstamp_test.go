package synclog

// The re-seal stamp rule: a doc whose items are sealed values re-emits a digest
// change with no newer write stamp under the stamp the key already carried, so
// a key rotation's re-seal never outranks a peer's edit -- and every doc that
// does not say it re-seals keeps the aging clock stamp.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// resealingDoc is a fake doc that says its digest changes without a write stamp
// are re-seals: the secrets document's shape, pinned without the secrets
// package, which would import this one.
type resealingDoc struct {
	*fakeDoc
}

func (r *resealingDoc) ResealKeepsStamp() bool { return true }

// exportStamped runs one export and answers the stamp the doc's one key carried,
// or the zero stamp when the export appended nothing.
func exportStamped(t *testing.T, doc SharedDoc, d *db.DB, r *recording) Stamp {
	t.Helper()
	registered, ok := sharedDocs[doc.Name()]
	if ok && registered != SharedDoc(doc) {
		t.Fatalf("another doc answers for %s", doc.Name())
	}
	if !ok {
		sharedDocs[doc.Name()] = doc
		t.Cleanup(func() { delete(sharedDocs, doc.Name()) })
	}
	if _, err := docExporter(d, r, keyA).Export(); err != nil {
		t.Fatalf("export: %v", err)
	}
	appended := r.appended()
	if len(appended) == 0 {
		return 0
	}
	entry := appended[len(appended)-1]
	parsed, err := ParseDocEntry(entry)
	if err != nil {
		t.Fatalf("parse the appended entry: %v", err)
	}
	return parsed.Stamp
}

// TestAReSealKeepsTheStampTheKeyCarried pins the rule a rekey leans on: a
// digest that changed with no newer write stamp -- the shape a re-seal has,
// same plaintext under new bytes -- re-emits at the stamp the key last carried,
// which ranks it below anything a peer stamped after that.
func TestAReSealKeepsTheStampTheKeyCarried(t *testing.T) {
	doc := &resealingDoc{fakeDoc: newFakeDoc("config")}
	doc.items["key"] = json.RawMessage(`{"sealed":"v1"}`)
	d, path := exporterFile(t, "m1")
	r := &recording{MemTransport: NewMemTransport("m1")}
	dirty(t, path)

	first := exportStamped(t, doc, d, r)
	if first == 0 {
		t.Fatal("the first export appended nothing, so the rule has nothing to hold")
	}

	// The re-seal: the plaintext is the same, only the bytes and the digest
	// differ, and no write stamped it on this machine.
	time.Sleep(2 * time.Millisecond)
	doc.items["key"] = json.RawMessage(`{"sealed":"v2"}`)
	dirty(t, path)
	second := exportStamped(t, doc, d, r)
	if second != first {
		t.Fatalf("the re-seal carried stamp %d, want the kept %d", second, first)
	}

	// The next re-seal keeps it again: the stamp is the value's, not the
	// export's, so re-sealing twice does not advance anything.
	doc.items["key"] = json.RawMessage(`{"sealed":"v3"}`)
	dirty(t, path)
	if third := exportStamped(t, doc, d, r); third != first {
		t.Fatalf("the second re-seal carried stamp %d, want the kept %d", third, first)
	}
}

// TestAWriteStampStillBeatsTheKeptStamp pins that the rule is the re-seal's and
// not the doc's whole story: a write this machine stamped after the last export
// outranks the stamp the key carried, which is what keeps a person's edit ahead
// of everything that came before it.
func TestAWriteStampStillBeatsTheKeptStamp(t *testing.T) {
	doc := &writingResealingDoc{resealingDoc: resealingDoc{fakeDoc: newFakeDoc("config")}}
	doc.items["key"] = json.RawMessage(`{"sealed":"v1"}`)
	d, path := exporterFile(t, "m1")
	r := &recording{MemTransport: NewMemTransport("m1")}
	dirty(t, path)

	first := exportStamped(t, doc, d, r)
	doc.wrote = first + 1000
	doc.items["key"] = json.RawMessage(`{"sealed":"edited"}`)
	dirty(t, path)
	if second := exportStamped(t, doc, d, r); second != first+1000 {
		t.Fatalf("the stamped write carried %d, want its write stamp %d", second, first+1000)
	}
}

// writingResealingDoc is a re-sealing doc that also knows when a key was
// written, the pair of interfaces the secrets document implements.
type writingResealingDoc struct {
	resealingDoc
	wrote Stamp
}

func (w *writingResealingDoc) ItemWriteStamp(string) (Stamp, bool, error) {
	return w.wrote, w.wrote != 0, nil
}

// TestAReSealingDocStillAgesItsRemovals pins the boundary of the rule: a
// removal is not a re-seal. It leaves under the clock, moved past the stamp the
// key carried, so it outranks the state it removes instead of arriving at that
// state's own rank and being skipped as its redelivery.
func TestAReSealingDocStillAgesItsRemovals(t *testing.T) {
	doc := &resealingDoc{fakeDoc: newFakeDoc("config")}
	doc.items["key"] = json.RawMessage(`{"sealed":"v1"}`)
	d, path := exporterFile(t, "m1")
	r := &recording{MemTransport: NewMemTransport("m1")}
	dirty(t, path)

	first := exportStamped(t, doc, d, r)
	time.Sleep(2 * time.Millisecond)
	delete(doc.items, "key")
	dirty(t, path)
	removed := exportStamped(t, doc, d, r)
	if removed <= first {
		t.Fatalf("the removal carried stamp %d, want one past the kept %d", removed, first)
	}
}

// TestADocThatDoesNotReSealKeepsTheClockStamp pins the default: a doc that does
// not say it re-seals -- the gates document among them -- ages a digest change
// with no write stamp past the stamp the key last carried, exactly as before.
func TestADocThatDoesNotReSealKeepsTheClockStamp(t *testing.T) {
	doc := newFakeDoc("config")
	doc.items["key"] = json.RawMessage(`{"v":1}`)
	d, path := exporterFile(t, "m1")
	r := &recording{MemTransport: NewMemTransport("m1")}
	dirty(t, path)

	first := exportStamped(t, doc, d, r)
	time.Sleep(2 * time.Millisecond)
	doc.items["key"] = json.RawMessage(`{"v":2}`)
	dirty(t, path)
	second := exportStamped(t, doc, d, r)
	if second <= first {
		t.Fatalf("the aging doc carried stamp %d, want one past %d", second, first)
	}
}
