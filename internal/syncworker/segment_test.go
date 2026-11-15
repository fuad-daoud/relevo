package syncworker

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/blobstore"
)

func makeTestEntries(origin string, startSeq, count int) []Entry {
	entries := make([]Entry, count)
	baseTime := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	for i := 0; i < count; i++ {
		seq := startSeq + i
		e := Entry{
			Origin:        origin,
			Seq:           seq,
			Batch:         startSeq,
			Tbl:           "items",
			PK:            fmt.Sprintf(`["k%d"]`, seq),
			Op:            "upsert",
			SchemaVersion: 1,
			Body:          json.RawMessage(fmt.Sprintf(`{"val":%d}`, seq)),
			At:            baseTime.Add(time.Duration(i) * time.Second),
			MAC:           bytes.Repeat([]byte{byte(seq % 256)}, 32),
		}
		if i%3 == 1 {
			e.AfterOrigin = "other-origin"
			e.AfterSeq = i * 10
		}
		entries[i] = e
	}
	return entries
}

func compareEntries(t *testing.T, want, got []Entry) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("entry count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		w, g := want[i], got[i]
		if !w.At.Equal(g.At) {
			t.Errorf("entry %d: At = %v, want %v", i, g.At, w.At)
		}
		w.At, g.At = time.Time{}, time.Time{}
		if !reflect.DeepEqual(w, g) {
			t.Errorf("entry %d mismatch:\n got: %+v\nwant: %+v", i, g, w)
		}
	}
}

func TestSegmentRoundTrip(t *testing.T) {
	origin := "laptop"
	for _, count := range []int{1, 2, 255, 256} {
		t.Run(fmt.Sprintf("%d_entries", count), func(t *testing.T) {
			entries := makeTestEntries(origin, 10, count)
			data, hdr, err := EncodeSegment(origin, entries)
			if err != nil {
				t.Fatalf("EncodeSegment: %v", err)
			}
			if hdr.Count != count {
				t.Fatalf("header count = %d, want %d", hdr.Count, count)
			}
			key := blobstore.SegmentKey(origin, hdr.First)
			decHdr, decEntries, err := DecodeSegment(key, data)
			if err != nil {
				t.Fatalf("DecodeSegment: %v", err)
			}
			if decHdr != hdr {
				t.Errorf("header mismatch: got %+v, want %+v", decHdr, hdr)
			}
			compareEntries(t, entries, decEntries)
		})
	}

	t.Run("delete_and_absent_mac", func(t *testing.T) {
		deleteEntry := Entry{
			Origin:        origin,
			Seq:           42,
			Batch:         42,
			Tbl:           "items",
			PK:            `["del42"]`,
			Op:            "delete",
			SchemaVersion: 1,
			At:            time.Date(2026, 11, 14, 13, 0, 0, 0, time.UTC),
		}
		data, hdr, err := EncodeSegment(origin, []Entry{deleteEntry})
		if err != nil {
			t.Fatalf("EncodeSegment: %v", err)
		}
		key := blobstore.SegmentKey(origin, hdr.First)
		decHdr, decEntries, err := DecodeSegment(key, data)
		if err != nil {
			t.Fatalf("DecodeSegment: %v", err)
		}
		if decHdr != hdr {
			t.Errorf("header mismatch: got %+v, want %+v", decHdr, hdr)
		}
		compareEntries(t, []Entry{deleteEntry}, decEntries)
	})

	t.Run("body_and_line_whitespace", func(t *testing.T) {
		atStr := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
		macStr := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
		rawLine := fmt.Sprintf(`{ "origin" : "laptop" , "seq" : 1 , "batch" : 1 , "tbl" : "items" , "pk" : "[\"k1\"]" , "op" : "upsert" , "schema_version" : 1 , "body" : { "a" : 1 } , "at" : %q , "mac" : %q }`+"\n", atStr, macStr)
		digest := sha256.Sum256([]byte(rawLine))
		hdr := SegmentHeader{
			V:      SegmentVersion,
			Origin: origin,
			First:  1,
			Last:   1,
			Count:  1,
			SHA256: hex.EncodeToString(digest[:]),
		}
		hdrBytes, err := json.Marshal(hdr)
		if err != nil {
			t.Fatalf("marshal header: %v", err)
		}
		var payload bytes.Buffer
		payload.Write(hdrBytes)
		payload.WriteByte('\n')
		payload.WriteString(rawLine)
		compressed := segmentEncoder.EncodeAll(payload.Bytes(), nil)

		key := blobstore.SegmentKey(origin, 1)
		decHdr, decEntries, err := DecodeSegment(key, compressed)
		if err != nil {
			t.Fatalf("DecodeSegment failed on entry with whitespace: %v", err)
		}
		if decHdr != hdr {
			t.Errorf("header mismatch: got %+v, want %+v", decHdr, hdr)
		}
		if len(decEntries) != 1 {
			t.Fatalf("got %d entries, want 1", len(decEntries))
		}
		if decEntries[0].Origin != "laptop" || decEntries[0].Seq != 1 {
			t.Errorf("entry mismatch: %+v", decEntries[0])
		}
	})
}

func TestSegmentEncodeIsDeterministic(t *testing.T) {
	origin := "laptop"
	entries := makeTestEntries(origin, 1, 10)
	b1, h1, err1 := EncodeSegment(origin, entries)
	if err1 != nil {
		t.Fatalf("first encode: %v", err1)
	}
	b2, h2, err2 := EncodeSegment(origin, entries)
	if err2 != nil {
		t.Fatalf("second encode: %v", err2)
	}
	if h1 != h2 {
		t.Errorf("headers differ:\n1: %+v\n2: %+v", h1, h2)
	}
	if !bytes.Equal(b1, b2) {
		t.Error("encoding twice produced different bytes")
	}
}

func TestEncodeSegmentRefusals(t *testing.T) {
	origin := "laptop"
	cases := []struct {
		name    string
		origin  string
		entries []Entry
	}{
		{
			name:    "empty_origin",
			origin:  "",
			entries: makeTestEntries(origin, 1, 2),
		},
		{
			name:    "origin_with_slash",
			origin:  "lap/top",
			entries: makeTestEntries("lap/top", 1, 2),
		},
		{
			name:    "zero_entries",
			origin:  origin,
			entries: nil,
		},
		{
			name:    "257_entries",
			origin:  origin,
			entries: makeTestEntries(origin, 1, 257),
		},
		{
			name:   "entry_foreign_origin",
			origin: origin,
			entries: func() []Entry {
				es := makeTestEntries(origin, 1, 2)
				es[1].Origin = "other"
				return es
			}(),
		},
		{
			// Seqs -1 and 0 are contiguous, so only the negative seq is
			// wrong: the gap check does not fire first.
			name:    "negative_seq",
			origin:  origin,
			entries: makeTestEntries(origin, -1, 2),
		},
		{
			name:   "seq_gap",
			origin: origin,
			entries: func() []Entry {
				es := makeTestEntries(origin, 1, 2)
				es[1].Seq = 3
				return es
			}(),
		},
		{
			name:   "seq_duplicate",
			origin: origin,
			entries: func() []Entry {
				es := makeTestEntries(origin, 1, 2)
				es[1].Seq = 1
				return es
			}(),
		},
		{
			name:   "seq_descending",
			origin: origin,
			entries: func() []Entry {
				es := makeTestEntries(origin, 1, 2)
				es[1].Seq = 0
				return es
			}(),
		},
		{
			name:   "unmarshallable_time",
			origin: origin,
			entries: func() []Entry {
				es := makeTestEntries(origin, 1, 1)
				es[0].At = time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC)
				return es
			}(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := EncodeSegment(tc.origin, tc.entries)
			if err == nil {
				t.Fatalf("EncodeSegment(%q) succeeded, want error", tc.origin)
			}
			if !errors.Is(err, ErrBadSegment) {
				t.Errorf("error = %v, want ErrBadSegment", err)
			}
		})
	}

	t.Run("payload_over_decoded_cap", func(t *testing.T) {
		// One allocation, quoted: the 64 MiB+16 payload is json.RawMessage
		// by conversion of the same slice, so building the case itself
		// spends no more memory than the encode it refuses.
		const bodyLen = (64 << 20) + 16
		quoted := make([]byte, bodyLen+2)
		for i := range quoted {
			quoted[i] = '0'
		}
		quoted[0], quoted[bodyLen+1] = '"', '"'
		oversized := makeTestEntries(origin, 1, 1)
		oversized[0].Body = json.RawMessage(quoted)
		_, _, err := EncodeSegment(origin, oversized)
		if err == nil {
			t.Fatal("EncodeSegment succeeded on 65 MiB body, want refusal")
		}
		if !errors.Is(err, ErrBadSegment) {
			t.Errorf("error = %v, want ErrBadSegment", err)
		}
		if !strings.Contains(err.Error(), "decoded cap") {
			t.Errorf("error = %q, want mention of decoded cap", err.Error())
		}
	})

	t.Run("object_over_encoded_cap", func(t *testing.T) {
		incompressible := make([]byte, 17<<20)
		r := rand.New(rand.NewSource(42))
		r.Read(incompressible)
		body := `"` + base64.StdEncoding.EncodeToString(incompressible) + `"`
		es := makeTestEntries(origin, 1, 1)
		es[0].Body = json.RawMessage(body)
		_, _, err := EncodeSegment(origin, es)
		if err == nil {
			t.Fatal("EncodeSegment succeeded on 17 MiB incompressible object, want refusal")
		}
		if !errors.Is(err, ErrBadSegment) {
			t.Errorf("error = %v, want ErrBadSegment", err)
		}
		if !strings.Contains(err.Error(), "over the 16777216-byte cap") {
			t.Errorf("error = %q, want mention of encoded cap", err.Error())
		}
	})
}

func TestSegmentLegacy(t *testing.T) {
	origin := "laptop"

	t.Run("4096_entries_round_trip", func(t *testing.T) {
		entries := makeTestEntries(origin, 1, 4096)
		data, hdr, err := EncodeLegacySegment(origin, entries)
		if err != nil {
			t.Fatalf("EncodeLegacySegment(4096): %v", err)
		}
		if !hdr.Legacy {
			t.Error("hdr.Legacy = false, want true")
		}
		key := blobstore.SegmentKey(origin, hdr.First)
		decHdr, decEntries, err := DecodeSegment(key, data)
		if err != nil {
			t.Fatalf("DecodeSegment: %v", err)
		}
		if !decHdr.Legacy {
			t.Error("decHdr.Legacy = false, want true")
		}
		if decHdr != hdr {
			t.Errorf("header mismatch:\n got: %+v\nwant: %+v", decHdr, hdr)
		}
		compareEntries(t, entries, decEntries)
	})

	t.Run("sparse_increasing_seqs", func(t *testing.T) {
		sparse := makeTestEntries(origin, 1, 5)
		for i := range sparse {
			sparse[i].Seq = 1 + i*4 // 1, 5, 9, 13, 17
		}
		data, hdr, err := EncodeLegacySegment(origin, sparse)
		if err != nil {
			t.Fatalf("EncodeLegacySegment(sparse): %v", err)
		}
		key := blobstore.SegmentKey(origin, hdr.First)
		decHdr, decEntries, err := DecodeSegment(key, data)
		if err != nil {
			t.Fatalf("DecodeSegment(sparse): %v", err)
		}
		if !decHdr.Legacy {
			t.Error("decHdr.Legacy = false, want true")
		}
		compareEntries(t, sparse, decEntries)

		// EncodeSegment must refuse the sparse seqs.
		if _, _, err := EncodeSegment(origin, sparse); err == nil {
			t.Error("EncodeSegment(sparse) succeeded, want refusal")
		} else if !errors.Is(err, ErrBadSegment) {
			t.Errorf("EncodeSegment(sparse) error = %v, want ErrBadSegment", err)
		}
	})

	t.Run("4097_refuses", func(t *testing.T) {
		entries := makeTestEntries(origin, 1, 4097)
		if _, _, err := EncodeLegacySegment(origin, entries); err == nil {
			t.Error("EncodeLegacySegment(4097) succeeded, want refusal")
		} else if !errors.Is(err, ErrBadSegment) {
			t.Errorf("error = %v, want ErrBadSegment", err)
		}
	})

	t.Run("legacy_header_on_256_contiguous", func(t *testing.T) {
		entries := makeTestEntries(origin, 1, 256)
		data, hdr, err := EncodeLegacySegment(origin, entries)
		if err != nil {
			t.Fatalf("EncodeLegacySegment(256): %v", err)
		}
		key := blobstore.SegmentKey(origin, hdr.First)
		decHdr, decEntries, err := DecodeSegment(key, data)
		if err != nil {
			t.Fatalf("DecodeSegment: %v", err)
		}
		if !decHdr.Legacy {
			t.Error("decHdr.Legacy = false, want true")
		}
		compareEntries(t, entries, decEntries)
	})

	t.Run("legacy_descending_seqs_refused", func(t *testing.T) {
		entries := makeTestEntries(origin, 1, 3)
		entries[1].Seq = 5
		entries[2].Seq = 4 // not strictly increasing
		if _, _, err := EncodeLegacySegment(origin, entries); err == nil {
			t.Error("EncodeLegacySegment with descending seqs succeeded, want refusal")
		} else if !errors.Is(err, ErrBadSegment) {
			t.Errorf("error = %v, want ErrBadSegment", err)
		}
	})
}
