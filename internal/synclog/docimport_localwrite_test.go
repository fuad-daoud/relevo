package synclog

import (
	"errors"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// writtenDoc is a txDoc that reports when each key was last written on this
// machine.
type writtenDoc struct {
	*txDoc
	wrote map[string]Stamp
	err   error
}

func (d *writtenDoc) ItemWriteStampIn(_ *db.Tx, key string) (Stamp, bool, error) {
	if d.err != nil {
		return 0, false, d.err
	}
	s, ok := d.wrote[key]
	return s, ok, nil
}

func TestArrivingEntryAgainstALocalWrite(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		wrote    Stamp
		hasWrite bool
		arriving Stamp
		origin   string
		wantKept bool
	}{
		{name: "older than the local write", wrote: 9, hasWrite: true, arriving: 5, origin: "m1", wantKept: true},
		{name: "newer than the local write", wrote: 3, hasWrite: true, arriving: 5, origin: "m1"},
		{name: "no local write", arriving: 5, origin: "m1"},
		{name: "tie goes to the higher origin: local", wrote: 5, hasWrite: true, arriving: 5, origin: "m0", wantKept: true},
		{name: "tie goes to the higher origin: arriving", wrote: 5, hasWrite: true, arriving: 5, origin: "m3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc := &writtenDoc{txDoc: newTxDoc("config"), wrote: map[string]Stamp{}}
			if tc.hasWrite {
				doc.wrote["policy"] = tc.wrote
			}
			log := NewMemTransport(tc.origin)
			signedDoc(t, log, doc, tc.origin, "policy", tc.arriving, `{"a":1}`, keyA)
			local, _ := peerFile(t, "m2")

			if _, err := docImporter(local, log, keyA, doc).Import(); err != nil {
				t.Fatalf("import: %v", err)
			}
			_, applied := doc.items["policy"]
			if applied == tc.wantKept {
				t.Errorf("applied = %v, want %v (revisions %v)", applied, !tc.wantKept, doc.revisions)
			}
			if tc.wantKept && (len(doc.revisions) != 1 || !strings.HasPrefix(doc.revisions[0], "sync-superseded:")) {
				t.Errorf("revisions = %v, want one superseded", doc.revisions)
			}
		})
	}
}

func TestLocalWriteSupersedeMovesTheAppliedState(t *testing.T) {
	t.Parallel()
	doc := &writtenDoc{txDoc: newTxDoc("config"), wrote: map[string]Stamp{"policy": 9}}
	log := NewMemTransport("m1")
	signedDoc(t, log, doc, "m1", "policy", 5, `{"a":1}`, keyA)
	local, _ := peerFile(t, "m2")

	if _, err := docImporter(local, log, keyA, doc).Import(); err != nil {
		t.Fatalf("import: %v", err)
	}
	var got db.DocApplied
	err := local.LocalOrSelf().Tx(func(tx *db.Tx) error {
		var e error
		got, _, e = tx.DocApplied("config", "policy")
		return e
	})
	if err != nil || got.Origin != "m1" || got.Stamp != Stamp(5).String() {
		t.Errorf("applied = %+v, %v; want the superseded entry (m1, 5)", got, err)
	}
}

func TestLocalWriteOlderThanTheAppliedStateDoesNotHoldBack(t *testing.T) {
	t.Parallel()
	doc := &writtenDoc{txDoc: newTxDoc("config"), wrote: map[string]Stamp{"policy": 4}}
	log := NewMemTransport("m1")
	signedDoc(t, log, doc, "m1", "policy", 7, `{"a":7}`, keyA)
	signedDoc(t, log, doc, "m1", "policy", 5, `{"a":5}`, keyA)
	local, _ := peerFile(t, "m2")

	if _, err := docImporter(local, log, keyA, doc).Import(); err != nil {
		t.Fatalf("import: %v", err)
	}
	if got := string(doc.items["policy"]); got != `{"a":7}` {
		t.Errorf("policy = %s, want the newest entry", got)
	}
}

func TestWriteStampErrorFailsTheImport(t *testing.T) {
	t.Parallel()
	boom := errors.New("write stamp unreadable")
	doc := &writtenDoc{txDoc: newTxDoc("config"), err: boom}
	log := NewMemTransport("m1")
	signedDoc(t, log, doc, "m1", "policy", 5, `{"a":1}`, keyA)
	local, _ := peerFile(t, "m2")

	if _, err := docImporter(local, log, keyA, doc).Import(); !errors.Is(err, boom) {
		t.Errorf("import error = %v, want %v", err, boom)
	}
}
