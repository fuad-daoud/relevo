package db

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
)

// bigHistoryValue is a compressible payload shaped like a bulk history row.
func bigHistoryValue() []byte {
	return []byte(strings.Repeat(`{"type":"assistant","text":"the builder wrote a long line of history"}`, 400))
}

// TestCodec pins the codec helpers: a compressible value stores as a shorter
// frame under codec 1 and decodes byte-identically, an empty or incompressible
// value stays plain, and an unknown codec is refused.
func TestCodec(t *testing.T) {
	big := bigHistoryValue()
	stored, codec := encodeColumn(big)
	if codec != codecZstd {
		t.Fatalf("compressible codec = %d, want %d", codec, codecZstd)
	}
	if len(stored) >= len(big) {
		t.Errorf("stored %d bytes, want fewer than the plain %d", len(stored), len(big))
	}
	got, err := decodeColumn(stored, codec)
	if err != nil {
		t.Fatalf("decodeColumn: %v", err)
	}
	if !bytes.Equal(got, big) {
		t.Errorf("decoded %d bytes, want the original %d", len(got), len(big))
	}

	for _, value := range [][]byte{nil, {}, []byte("tiny")} {
		plain, pcodec := encodeColumn(value)
		if pcodec != codecPlain {
			t.Errorf("encodeColumn(%q) codec = %d, want %d", value, pcodec, codecPlain)
		}
		if !bytes.Equal(plain, value) {
			t.Errorf("encodeColumn(%q) = %q, want the original value", value, plain)
		}
	}

	noise := make([]byte, 4096)
	if _, err := rand.Read(noise); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	plain, pcodec := encodeColumn(noise)
	if pcodec != codecPlain {
		t.Errorf("incompressible codec = %d, want %d", pcodec, codecPlain)
	}
	if !bytes.Equal(plain, noise) {
		t.Error("the incompressible value came back changed")
	}

	if _, err := decodeColumn([]byte("x"), 9); !errors.Is(err, ErrInvalid) {
		t.Errorf("decodeColumn(unknown codec) err = %v, want ErrInvalid", err)
	}
}

// TestRoundFilePutGetCompresses pins the sealed-file round trip through the
// write and read points: a compressible body stores under codec 1 and reads
// back byte-identical, while bytes and sha256 stay the plaintext's.
func TestRoundFilePutGetCompresses(t *testing.T) {
	d := openTestDB(t)
	id, err := d.RecordPut(testRecord("webshop"))
	if err != nil {
		t.Fatalf("RecordPut: %v", err)
	}

	body := bigHistoryValue()
	now := time.Now().UTC()
	if err := d.Tx(func(tx *Tx) error {
		return tx.RoundFilePut(id, "003-runner.jsonl", 3, body, now, now)
	}); err != nil {
		t.Fatalf("RoundFilePut: %v", err)
	}

	var stored []byte
	var codec, size int
	var sum string
	if err := d.sqlDB.QueryRow(`SELECT body, body_codec, bytes, sha256 FROM round_file WHERE record_id = ? AND name = ?`,
		id, "003-runner.jsonl").Scan(&stored, &codec, &size, &sum); err != nil {
		t.Fatalf("read the stored row: %v", err)
	}
	if codec != codecZstd {
		t.Errorf("stored codec = %d, want %d", codec, codecZstd)
	}
	if len(stored) >= len(body) {
		t.Errorf("stored body = %d bytes, want fewer than the plain %d", len(stored), len(body))
	}
	if size != len(body) {
		t.Errorf("stored bytes column = %d, want the plaintext %d", size, len(body))
	}
	plainSum := sha256.Sum256(body)
	if sum != hex.EncodeToString(plainSum[:]) {
		t.Error("stored sha256 is not the plaintext's")
	}

	got, _, ok, err := d.RoundFileGet(id, "003-runner.jsonl")
	if err != nil || !ok {
		t.Fatalf("RoundFileGet = (ok %v, err %v), want (true, nil)", ok, err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("RoundFileGet returned %d bytes, want the original %d", len(got), len(body))
	}
}

// TestTranscriptPutReadCompresses pins the transcript round trip: both big
// columns store under codec 1 and read back byte-identical.
func TestTranscriptPutReadCompresses(t *testing.T) {
	d := openTestDB(t)
	big := string(bigHistoryValue())
	if _, err := d.AppendTranscript(OwnerMasterMind, "sess-big", []TranscriptRecord{{Seq: 0, RecordJSON: big, Rendered: big}}); err != nil {
		t.Fatalf("AppendTranscript: %v", err)
	}

	var jsonCodec, renderedCodec int
	if err := d.sqlDB.QueryRow(`SELECT record_json_codec, rendered_codec FROM transcript WHERE owner_id = ?`,
		"sess-big").Scan(&jsonCodec, &renderedCodec); err != nil {
		t.Fatalf("read the stored codecs: %v", err)
	}
	if jsonCodec != codecZstd || renderedCodec != codecZstd {
		t.Fatalf("stored codecs = %d/%d, want %d/%d", jsonCodec, renderedCodec, codecZstd, codecZstd)
	}

	got, err := d.Transcript(OwnerMasterMind, "sess-big", 0, 0)
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	if len(got) != 1 || got[0].RecordJSON != big || got[0].Rendered != big {
		t.Errorf("read back %d rows with different text, want the one original pair", len(got))
	}
}

// TestSmallRowsKeepTheirStorageClass pins the guard's other side: a small row
// stays at codec 0 with its original value and storage class, so a TEXT column
// is still text and a BLOB column is still a blob.
func TestSmallRowsKeepTheirStorageClass(t *testing.T) {
	d := openTestDB(t)
	id, err := d.RecordPut(testRecord("webshop"))
	if err != nil {
		t.Fatalf("RecordPut: %v", err)
	}

	now := time.Now().UTC()
	if err := d.Tx(func(tx *Tx) error {
		return tx.RoundFilePut(id, "001-done", 1, []byte("done\n"), now, now)
	}); err != nil {
		t.Fatalf("RoundFilePut: %v", err)
	}
	if _, err := d.AppendTranscript(OwnerMasterMind, "sess-small", []TranscriptRecord{{Seq: 0, RecordJSON: "{}", Rendered: "line"}}); err != nil {
		t.Fatalf("AppendTranscript: %v", err)
	}

	probes := []struct {
		name  string
		query string
		arg   string
		want  string
	}{
		{"round body", `SELECT typeof(body) FROM round_file WHERE record_id = ? AND name = '001-done'`, id, "blob"},
		{"round codec", `SELECT body_codec FROM round_file WHERE record_id = ? AND name = '001-done'`, id, "0"},
		{"record json", `SELECT typeof(record_json) FROM transcript WHERE owner_id = ?`, "sess-small", "text"},
		{"record codec", `SELECT record_json_codec FROM transcript WHERE owner_id = ?`, "sess-small", "0"},
		{"rendered", `SELECT typeof(rendered) FROM transcript WHERE owner_id = ?`, "sess-small", "text"},
		{"rendered codec", `SELECT rendered_codec FROM transcript WHERE owner_id = ?`, "sess-small", "0"},
	}
	for _, p := range probes {
		var got string
		if err := d.sqlDB.QueryRow(p.query, p.arg).Scan(&got); err != nil {
			t.Fatalf("%s: %v", p.name, err)
		}
		if got != p.want {
			t.Errorf("%s = %q, want %q", p.name, got, p.want)
		}
	}

	got, _, ok, err := d.RoundFileGet(id, "001-done")
	if err != nil || !ok || string(got) != "done\n" {
		t.Errorf("RoundFileGet = (%q, ok %v, err %v), want done\\n", got, ok, err)
	}
}

// TestReadRefusesUnknownCodec pins that a codec value outside 0 and 1 fails the
// read rather than returning the stored bytes as if they were plaintext.
func TestReadRefusesUnknownCodec(t *testing.T) {
	d := openTestDB(t)
	id, err := d.RecordPut(testRecord("webshop"))
	if err != nil {
		t.Fatalf("RecordPut: %v", err)
	}
	now := time.Now().UTC()
	if err := d.Tx(func(tx *Tx) error {
		return tx.RoundFilePut(id, "001-done", 1, []byte("done\n"), now, now)
	}); err != nil {
		t.Fatalf("RoundFilePut: %v", err)
	}
	if _, err := d.sqlDB.Exec(`UPDATE round_file SET body_codec = 9 WHERE record_id = ?`, id); err != nil {
		t.Fatalf("set an unknown codec: %v", err)
	}
	if _, _, _, err := d.RoundFileGet(id, "001-done"); !errors.Is(err, ErrInvalid) {
		t.Errorf("RoundFileGet(unknown codec) err = %v, want ErrInvalid", err)
	}

	if _, err := d.AppendTranscript(OwnerMasterMind, "sess-bad", []TranscriptRecord{{Seq: 0, Rendered: "line"}}); err != nil {
		t.Fatalf("AppendTranscript: %v", err)
	}
	if _, err := d.sqlDB.Exec(`UPDATE transcript SET rendered_codec = 9 WHERE owner_id = 'sess-bad'`); err != nil {
		t.Fatalf("set an unknown codec: %v", err)
	}
	if _, err := d.Transcript(OwnerMasterMind, "sess-bad", 0, 0); !errors.Is(err, ErrInvalid) {
		t.Errorf("Transcript(unknown codec) err = %v, want ErrInvalid", err)
	}
}
