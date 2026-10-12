package syncpipe

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/synclog"
	"github.com/fuad-daoud/relevo/internal/syncworker"
)

// The conversion between the daemon's entry and the wire's, which is written out
// by hand in both directions rather than reflected over. A field left out of one
// of them becomes that field's zero value on the far side of a pipe, and nothing
// in the transport can see that happen -- so the MAC is pinned crossing both
// ways, over the pipe's own encoding and not just as a field copy.

func TestConvertCarriesMACBothWays(t *testing.T) {
	t.Parallel()
	key := []byte("the passphrase this machine stretched")
	body := json.RawMessage(`{"title":"one"}`)
	synclogEntry := synclog.Entry{
		Origin:        "origin-a",
		Seq:           7,
		Batch:         5,
		Table:         "board",
		PK:            `["b"]`,
		Op:            synclog.OpUpsert,
		SchemaVersion: 3,
		Body:          body,
		At:            time.Date(2026, 10, 8, 9, 30, 0, 123456789, time.UTC),
	}
	synclogEntry.MAC = synclog.EntryMAC(key, synclogEntry)

	wire := entriesToWire([]synclog.Entry{synclogEntry})
	if len(wire) != 1 {
		t.Fatalf("entriesToWire = %d entries, want 1", len(wire))
	}
	if len(wire[0].MAC) == 0 {
		t.Fatal("entriesToWire dropped the MAC: an entry would reach the log unsigned")
	}

	// Over the wire's own JSON, because a []byte travels as base64 and the
	// signature is raw bytes: a round trip that re-encoded it as text would come
	// back as something no key verifies.
	line, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("marshal the wire entry: %v", err)
	}
	var decoded []syncworker.Entry
	if err := json.Unmarshal(line, &decoded); err != nil {
		t.Fatalf("decode the wire entry: %v", err)
	}

	back := entriesFromWire(decoded)
	if len(back) != 1 {
		t.Fatalf("entriesFromWire = %d entries, want 1", len(back))
	}
	if !bytes.Equal(back[0].MAC, synclogEntry.MAC) {
		t.Errorf("the MAC came back as %x, want %x", back[0].MAC, synclogEntry.MAC)
	}
	if !synclog.VerifyMAC(key, back[0]) {
		t.Error("the entry that came back over the wire does not verify against the key that signed it")
	}
}

// TestConvertLeavesAnUnsignedEntryUnsigned pins that an entry with no MAC stays
// that way across the pipe. Nothing on the daemon's side of this boundary signs,
// so a conversion that invented one would be the only thing standing between an
// unsigned log and a signature nobody checked.
func TestConvertLeavesAnUnsignedEntryUnsigned(t *testing.T) {
	t.Parallel()
	entry := synclog.Entry{
		Origin: "origin-a", Seq: 1, Batch: 1, Table: "board", PK: `["b"]`,
		Op: synclog.OpUpsert, SchemaVersion: 3,
		Body: json.RawMessage(`{"title":"one"}`),
		At:   time.Date(2026, 10, 8, 9, 30, 0, 0, time.UTC),
	}
	wire := entriesToWire([]synclog.Entry{entry})
	if wire[0].MAC != nil {
		t.Errorf("entriesToWire invented a MAC: %x", wire[0].MAC)
	}
	line, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("marshal the wire entry: %v", err)
	}
	if bytes.Contains(line, []byte(`"mac"`)) {
		t.Errorf("the wire carried a mac field for an unsigned entry: %s", line)
	}
	var decoded []syncworker.Entry
	if err := json.Unmarshal(line, &decoded); err != nil {
		t.Fatalf("decode the wire entry: %v", err)
	}
	if back := entriesFromWire(decoded); back[0].MAC != nil {
		t.Errorf("entriesFromWire invented a MAC: %x", back[0].MAC)
	}
}

// TestKeyMetaCrossesThePipeBase64 pins that the passphrase material a key_init
// carries survives the pipe as bytes. It travels in meta rows as base64 and on
// the pipe as base64 too, and a value that arrived as text would be the ASCII of
// the base64 rather than the salt.
func TestKeyMetaCrossesThePipeBase64(t *testing.T) {
	t.Parallel()
	salt := []byte{0x00, 0x01, 0xfe, 0xff, 's', 'a', 'l', 't'}
	check := []byte{0x10, 0x20, 0x30, 0x40}
	wire := keyToWire(synclog.KeyMeta{Salt: salt, KDF: "argon2id", Check: check, Present: true})

	line, err := json.Marshal(syncworker.Request{
		ID: "1", Verb: syncworker.VerbKeyInit, KeyMeta: wire,
	})
	if err != nil {
		t.Fatalf("marshal the request: %v", err)
	}
	var decoded syncworker.Request
	if err := json.Unmarshal(line, &decoded); err != nil {
		t.Fatalf("decode the request: %v", err)
	}
	if decoded.KeyMeta == nil {
		t.Fatal("the request arrived with no key")
	}
	got := keyMetaFromWire(*decoded.KeyMeta)
	if !bytes.Equal(got.Salt, salt) {
		t.Errorf("the salt arrived as %x, want %x", got.Salt, salt)
	}
	if !bytes.Equal(got.Check, check) {
		t.Errorf("the check arrived as %x, want %x", got.Check, check)
	}
	if got.KDF != "argon2id" || !got.Present {
		t.Errorf("the key arrived as %+v, want argon2id and present", got)
	}
}
