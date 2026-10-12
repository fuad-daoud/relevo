package synclog

import (
	"encoding/json"
	"testing"
	"time"
)

// The canonical MAC: what it covers, what it deliberately leaves out, and the
// two normalisations -- the compacted body and the UTC time -- that make a
// signed entry verify after it has crossed a pipe and been stored.

var macKey = []byte("the passphrase this machine stretched")

// signedEntry is a well-formed upsert to sign, so a test can change one field of
// a real entry rather than a hand-built struct that would fail the log's own
// shape rules.
func signedEntry(t *testing.T) Entry {
	t.Helper()
	e, err := NewUpsert("origin-a", "board", `["b"]`, 3,
		json.RawMessage(`{"title":"one"}`),
		time.Date(2026, 10, 8, 9, 30, 0, 123456789, time.UTC))
	if err != nil {
		t.Fatalf("NewUpsert: %v", err)
	}
	e.MAC = EntryMAC(macKey, e)
	return e
}

// TestEntryMACCoversEveryFieldButSeqAndBatch pins which fields the signature is
// over. Every field the writer chose is in it, and the two the transport assigns
// afterwards are not: covering those would mean no entry could ever verify,
// because the numbers stored are not the numbers the signer saw.
func TestEntryMACCoversEveryFieldButSeqAndBatch(t *testing.T) {
	t.Parallel()
	base := signedEntry(t)
	baseMAC := EntryMAC(macKey, base)

	changed := map[string]func(e *Entry){
		"origin":         func(e *Entry) { e.Origin = "origin-b" },
		"table":          func(e *Entry) { e.Table = "task" },
		"pk":             func(e *Entry) { e.PK = `["c"]` },
		"op":             func(e *Entry) { e.Op = OpDelete },
		"schema version": func(e *Entry) { e.SchemaVersion = 4 },
		"body":           func(e *Entry) { e.Body = json.RawMessage(`{"title":"two"}`) },
		"at":             func(e *Entry) { e.At = e.At.Add(time.Second) },
	}
	for name, mutate := range changed {
		t.Run(name, func(t *testing.T) {
			other := base
			mutate(&other)
			if got := EntryMAC(macKey, other); string(got) == string(baseMAC) {
				t.Errorf("changing the %s left the MAC unchanged: %x", name, got)
			}
		})
	}

	for name, assign := range map[string]func(e *Entry){
		"seq":   func(e *Entry) { e.Seq = 99 },
		"batch": func(e *Entry) { e.Batch = 99 },
	} {
		t.Run(name+" is not covered", func(t *testing.T) {
			other := base
			assign(&other)
			if got := EntryMAC(macKey, other); string(got) != string(baseMAC) {
				t.Errorf("changing the %s moved the MAC: the transport assigns it after signing", name)
			}
		})
	}
}

// TestEntryMACIsStableAcrossBodyWhitespace pins that the body is signed
// compacted. The pipe re-encodes a json.RawMessage, so the bytes that come back
// from the log are not the bytes the exporter held, and a signature over the
// exact bytes would not verify on the round trip.
func TestEntryMACIsStableAcrossBodyWhitespace(t *testing.T) {
	t.Parallel()
	compact, err := NewUpsert("origin-a", "board", `["b"]`, 3,
		json.RawMessage(`{"title":"one","n":1}`), time.Date(2026, 10, 8, 9, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewUpsert: %v", err)
	}
	spaced, err := NewUpsert("origin-a", "board", `["b"]`, 3,
		json.RawMessage("{\n  \"title\": \"one\",\n  \"n\": 1\n}"), time.Date(2026, 10, 8, 9, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewUpsert: %v", err)
	}
	if got, want := EntryMAC(macKey, spaced), EntryMAC(macKey, compact); string(got) != string(want) {
		t.Errorf("the same body written with whitespace signed as %x, want %x", got, want)
	}

	// The key order is part of the bytes and is not normalised: compacting
	// removes whitespace, it does not reorder columns, and the exporter already
	// emits them sorted.
	reordered, err := NewUpsert("origin-a", "board", `["b"]`, 3,
		json.RawMessage(`{"n":1,"title":"one"}`), time.Date(2026, 10, 8, 9, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewUpsert: %v", err)
	}
	if got, want := EntryMAC(macKey, reordered), EntryMAC(macKey, compact); string(got) == string(want) {
		t.Error("reordering the body's columns left the MAC unchanged: the bytes signed are not the bytes stored")
	}
}

// TestEntryMACCannotBeMovedFromOneFieldToTheNext pins the length prefix.
//
// Without it the encoding is a plain concatenation, so an origin and a table
// that split a run of bytes differently encode to the same bytes and two
// different entries share one MAC. Two entries are built here that agree on
// every field except where that split falls.
func TestEntryMACCannotBeMovedFromOneFieldToTheNext(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 8, 9, 30, 0, 0, time.UTC)
	left, err := NewUpsert("ab", "c", `["k"]`, 3, json.RawMessage(`{"t":1}`), at)
	if err != nil {
		t.Fatalf("NewUpsert: %v", err)
	}
	right, err := NewUpsert("a", "bc", `["k"]`, 3, json.RawMessage(`{"t":1}`), at)
	if err != nil {
		t.Fatalf("NewUpsert: %v", err)
	}
	if got, want := EntryMAC(macKey, right), EntryMAC(macKey, left); string(got) == string(want) {
		t.Errorf("an entry whose origin and table split the same bytes signed as %x: the encoding has no field boundaries", got)
	}
}

// TestEntryMACSignsARefNotTheFetchedBody pins that a body sitting in the bucket
// is outside the signature. An entry naming an R2 object stores the reference,
// and the reference is what a reader verifies against; signing the fetched
// content would make the signature depend on a bucket this machine may not be
// able to read, and two machines holding the same object could disagree.
func TestEntryMACSignsARefNotTheFetchedBody(t *testing.T) {
	t.Parallel()
	ref, err := NewUpsert("origin-a", "board", `["b"]`, 3,
		json.RawMessage(`{"$ref":{"sha256":"abc123","bytes":9000,"codec":"zstd"}}`),
		time.Date(2026, 10, 8, 9, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewUpsert: %v", err)
	}
	ref.MAC = EntryMAC(macKey, ref)
	if !VerifyMAC(macKey, ref) {
		t.Fatal("an entry carrying a reference does not verify against its own signature")
	}

	// The content behind the reference is not what was signed: swapping it in is
	// a different body, and must not verify.
	fetched := ref
	fetched.Body = json.RawMessage(`{"title":"the nine kilobytes this object holds"}`)
	if VerifyMAC(macKey, fetched) {
		t.Error("an entry whose body was replaced by the fetched content still verifies")
	}
}

// TestVerifyMACRefusesAKeyItWasNotSignedWith pins that verification is against
// the key and not merely over the fields: a MAC computed with a different
// passphrase has to fail, or a wrong passphrase would read as the right one.
func TestVerifyMACRefusesAKeyItWasNotSignedWith(t *testing.T) {
	t.Parallel()
	e := signedEntry(t)
	if !VerifyMAC(macKey, e) {
		t.Fatal("a fresh signature does not verify against the key that made it")
	}
	if VerifyMAC([]byte("another passphrase"), e) {
		t.Error("the entry verified under a passphrase it was not signed with")
	}
	unstamped := e
	unstamped.MAC = nil
	if VerifyMAC(macKey, unstamped) {
		t.Error("an entry with no MAC verified: an unsigned entry must read as unsigned")
	}
}

// TestEntryMACCoversADeleteWithNoBody pins that a delete is signed rather than
// left out. A delete names a row by its key and carries nothing else, and the
// key is covered -- but the empty body is written into the encoding as a field
// of its own, so a delete cannot be turned into an upsert with no body by
// anything that cannot sign.
func TestEntryMACCoversADeleteWithNoBody(t *testing.T) {
	t.Parallel()
	del, err := NewDelete("origin-a", "board", `["b"]`, 3, time.Date(2026, 10, 8, 9, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewDelete: %v", err)
	}
	del.MAC = EntryMAC(macKey, del)
	if !VerifyMAC(macKey, del) {
		t.Error("a delete does not verify against its own signature")
	}
	moved, err := NewDelete("origin-a", "board", `["c"]`, 3, time.Date(2026, 10, 8, 9, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewDelete: %v", err)
	}
	moved.MAC = del.MAC
	if VerifyMAC(macKey, moved) {
		t.Error("a delete for another row verifies under the first one's signature")
	}
}
