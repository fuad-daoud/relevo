package synclog

import (
	"encoding/json"
	"testing"
	"time"
)

// TestDocEntrySurvivesTheLogUnchanged pins that a doc entry needs nothing new
// to travel: it is built, signed and appended like any other entry, and comes
// back off the head and the pull with its table, key, operation, body and stamp
// exactly as it left. That is what lets the config doc ride the existing
// transport, the existing numbering and the existing signature.
func TestDocEntrySurvivesTheLogUnchanged(t *testing.T) {
	doc := newFakeDoc("config")
	registerDoc(t, doc)

	wall := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	stamp := stampAt(wall, 0)
	key := []byte("the sync key")

	entry, err := NewDocUpsert("origin-a", doc, "policy", 1, stamp,
		json.RawMessage(`{"serve":{"listen":8080}}`), wall)
	if err != nil {
		t.Fatalf("NewDocUpsert: %v", err)
	}

	// The signature is made by the same path the exporter uses, so what the
	// transport carries is what a reader will verify.
	signer := &keyed{}
	signer.SetKey(key)
	batch := []Entry{entry}
	signer.sign(batch)
	if len(batch[0].MAC) == 0 {
		t.Fatal("the batch was not signed")
	}

	transport := NewMemTransport("origin-a")
	// appendTo is the exporter's own append, boundary included, so what the
	// transport carries is what an unlocked reader will actually accept.
	written, err := signer.appendTo(transport, "origin-a", batch)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if len(written) != 1 {
		t.Fatalf("Append wrote %d entries, want 1", len(written))
	}
	if !VerifyMAC(key, written[0]) {
		t.Fatal("the appended entry does not verify under its own key")
	}

	// The entry is pulled and headed by a second installation, since a
	// transport never hands an origin its own entries back.
	reader := transport.OnLog("origin-b")

	pulled, err := reader.Pull(nil)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if len(pulled) != 1 {
		t.Fatalf("Pull returned %d entries, want 1", len(pulled))
	}

	head, err := reader.Head("origin-a")
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if len(head) != 1 || head[0].Table != DocPrefix+"config" || head[0].PK != `["policy"]` {
		t.Fatalf("head = %+v, want the doc row and its key", head)
	}

	for _, got := range []Entry{written[0], pulled[0]} {
		wantDocEntryUnchanged(t, got, entry, wall, key)
	}
}

// wantDocEntryUnchanged is the comparison one side of the round trip runs: the
// entry as it came back must carry the same table, key, operation, body, time
// and signature it left with, and must still read as the same doc write.
func wantDocEntryUnchanged(t *testing.T, got, want Entry, wall time.Time, key []byte) {
	t.Helper()
	if got.Table != want.Table || got.PK != want.PK || got.Op != want.Op {
		t.Errorf("entry came back as %s %s %s, want %s %s %s",
			got.Table, got.PK, got.Op, want.Table, want.PK, want.Op)
	}
	if string(got.Body) != string(want.Body) {
		t.Errorf("body came back as %s, want %s", got.Body, want.Body)
	}
	if !got.At.Equal(wall) {
		t.Errorf("at came back as %s, want %s", got.At, wall)
	}
	if !VerifyMAC(key, got) {
		t.Errorf("entry %s %s does not verify after travelling", got.Table, got.PK)
	}

	parsed, err := ParseDocEntry(got)
	if err != nil {
		t.Fatalf("ParseDocEntry after travelling: %v", err)
	}
	if parsed.Key != "policy" || parsed.Origin != "origin-a" {
		t.Errorf("parsed = %s/%s, want policy/origin-a", parsed.Key, parsed.Origin)
	}
	left, err := ParseStamp(mustQuoteStamp(t, want.Body))
	if err != nil {
		t.Fatalf("read the stamp that left: %v", err)
	}
	if parsed.Stamp != left {
		t.Errorf("stamp came back as %d, want %d", parsed.Stamp, left)
	}
	if string(parsed.Value) != `{"serve":{"listen":8080}}` {
		t.Errorf("value came back as %s", parsed.Value)
	}
}

// mustQuoteStamp reads the stamp out of a body this test built, so the
// comparison does not repeat the number the constructor was given.
func mustQuoteStamp(t *testing.T, body []byte) string {
	t.Helper()
	var fields docBody
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatalf("read the stamp out of %s: %v", body, err)
	}
	return fields.Stamp
}

// TestDocEntrySignatureCoversItsStamp pins that the stamp is inside the signed
// bytes. It is the field every ordering decision is made from, so an entry
// whose stamp another machine could change without breaking the signature would
// be an entry a peer could reorder at will.
func TestDocEntrySignatureCoversItsStamp(t *testing.T) {
	doc := newFakeDoc("config")
	registerDoc(t, doc)

	wall := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	key := []byte("the sync key")

	entry, err := NewDocUpsert("origin-a", doc, "policy", 1, stampAt(wall, 0),
		json.RawMessage(`{"serve":{"listen":8080}}`), wall)
	if err != nil {
		t.Fatalf("NewDocUpsert: %v", err)
	}
	signer := &keyed{}
	signer.SetKey(key)
	signed := []Entry{entry}
	signer.sign(signed)

	forged, err := NewDocUpsert("origin-a", doc, "policy", 1, stampAt(wall, 999),
		json.RawMessage(`{"serve":{"listen":8080}}`), wall)
	if err != nil {
		t.Fatalf("NewDocUpsert: %v", err)
	}
	forged.MAC = signed[0].MAC

	if VerifyMAC(key, forged) {
		t.Error("an entry with a raised stamp verified under the original signature")
	}
}

// TestDocEntrySignatureCoversItsValue pins that the value is inside the signed
// bytes too, so a peer's value cannot be swapped for another of the same size
// after the fact.
func TestDocEntrySignatureCoversItsValue(t *testing.T) {
	doc := newFakeDoc("config")
	registerDoc(t, doc)

	wall := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	key := []byte("the sync key")
	stamp := stampAt(wall, 0)

	entry, err := NewDocUpsert("origin-a", doc, "policy", 1, stamp,
		json.RawMessage(`{"serve":{"listen":8080}}`), wall)
	if err != nil {
		t.Fatalf("NewDocUpsert: %v", err)
	}
	signer := &keyed{}
	signer.SetKey(key)
	signed := []Entry{entry}
	signer.sign(signed)

	forged, err := NewDocUpsert("origin-a", doc, "policy", 1, stamp,
		json.RawMessage(`{"serve":{"listen":9090}}`), wall)
	if err != nil {
		t.Fatalf("NewDocUpsert: %v", err)
	}
	forged.MAC = signed[0].MAC

	if VerifyMAC(key, forged) {
		t.Error("an entry with a swapped value verified under the original signature")
	}
}

// TestDocEntryKeyIsTheItemKeyAsAKeyArray pins the primary key's shape on the
// way out as well as on the way in, so an entry this machine wrote reads back
// through the same one-column key every other entry carries.
func TestDocEntryKeyIsTheItemKeyAsAKeyArray(t *testing.T) {
	pk, err := DocPK("policy/serve")
	if err != nil {
		t.Fatalf("DocPK: %v", err)
	}
	if pk != `["policy/serve"]` {
		t.Errorf("DocPK = %s, want the one-element key array", pk)
	}
	if _, err := DocPK(""); err == nil {
		t.Error("DocPK accepted an empty key, want a refusal")
	}
}
