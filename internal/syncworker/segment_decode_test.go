package syncworker

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/blobstore"
	"github.com/klauspost/compress/zstd"
)

func makeGoodPayload(t *testing.T, origin string, entries []Entry, legacy bool) (SegmentHeader, string, []string) {
	t.Helper()
	var entryLines bytes.Buffer
	entryStrings := make([]string, len(entries))
	for i, e := range entries {
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("marshal entry: %v", err)
		}
		entryStrings[i] = string(b)
		entryLines.Write(b)
		entryLines.WriteByte('\n')
	}
	digest := sha256.Sum256(entryLines.Bytes())
	header := SegmentHeader{
		V:      SegmentVersion,
		Origin: origin,
		First:  entries[0].Seq,
		Last:   entries[len(entries)-1].Seq,
		Count:  len(entries),
		SHA256: hex.EncodeToString(digest[:]),
		Legacy: legacy,
	}
	hBytes, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	return header, string(hBytes), entryStrings
}

func assembleAndCompress(headerLine string, entryLines []string) []byte {
	var buf bytes.Buffer
	buf.WriteString(headerLine)
	buf.WriteByte('\n')
	for _, l := range entryLines {
		buf.WriteString(l)
		buf.WriteByte('\n')
	}
	return segmentEncoder.EncodeAll(buf.Bytes(), nil)
}

func TestDecodeSegmentRefusals(t *testing.T) {
	origin := "laptop"
	baseEntries := makeTestEntries(origin, 1, 2)
	baseHdr, baseHdrLine, baseEntryLines := makeGoodPayload(t, origin, baseEntries, false)
	goodData := assembleAndCompress(baseHdrLine, baseEntryLines)
	goodKey := blobstore.SegmentKey(origin, baseHdr.First)

	type testCase struct {
		name string
		key  string
		data []byte
	}

	cases := []testCase{
		{
			name: "wrong_key_origin",
			key:  blobstore.SegmentKey("desktop", baseHdr.First),
			data: goodData,
		},
		{
			name: "first_in_key_not_headers",
			key:  blobstore.SegmentKey(origin, baseHdr.First+1),
			data: goodData,
		},
		{
			name: "key_not_segment_key_body",
			key:  blobstore.Key(origin, strings.Repeat("ab", 32)),
			data: goodData,
		},
		{
			name: "key_not_segment_key_size",
			key:  blobstore.SizeKey(origin),
			data: goodData,
		},
		{
			name: "key_not_segment_key_11digit",
			key:  origin + "/log/00000000001",
			data: goodData,
		},
		{
			name: "v_2",
			key:  goodKey,
			data: func() []byte {
				h := baseHdr
				h.V = 2
				hBytes, _ := json.Marshal(h)
				return assembleAndCompress(string(hBytes), baseEntryLines)
			}(),
		},
		{
			name: "unknown_header_field",
			key:  goodKey,
			data: func() []byte {
				hLine := strings.TrimSuffix(baseHdrLine, "}") + `,"extra":true}`
				return assembleAndCompress(hLine, baseEntryLines)
			}(),
		},
		{
			name: "trailing_garbage_after_header_json",
			key:  goodKey,
			data: func() []byte {
				hLine := baseHdrLine + " trailing_garbage"
				return assembleAndCompress(hLine, baseEntryLines)
			}(),
		},
		{
			name: "count_one_more_than_lines",
			key:  goodKey,
			data: func() []byte {
				h := baseHdr
				h.Count = len(baseEntryLines) + 1
				hBytes, _ := json.Marshal(h)
				return assembleAndCompress(string(hBytes), baseEntryLines)
			}(),
		},
		{
			name: "count_one_fewer_than_lines",
			key:  goodKey,
			data: func() []byte {
				h := baseHdr
				h.Count = len(baseEntryLines) - 1
				hBytes, _ := json.Marshal(h)
				return assembleAndCompress(string(hBytes), baseEntryLines)
			}(),
		},
		{
			name: "digest_of_wrong_bytes",
			key:  goodKey,
			data: func() []byte {
				h := baseHdr
				h.SHA256 = strings.Repeat("0", 64)
				hBytes, _ := json.Marshal(h)
				return assembleAndCompress(string(hBytes), baseEntryLines)
			}(),
		},
		{
			name: "one_flipped_byte_inside_entry_line",
			key:  goodKey,
			data: func() []byte {
				corrupt := make([]string, len(baseEntryLines))
				copy(corrupt, baseEntryLines)
				corrupt[0] = strings.Replace(corrupt[0], `"items"`, `"jtems"`, 1)
				return assembleAndCompress(baseHdrLine, corrupt)
			}(),
		},
		{
			name: "entry_foreign_origin",
			key:  goodKey,
			data: func() []byte {
				es := makeTestEntries(origin, 1, 2)
				es[1].Origin = "desktop"
				_, hLine, eLines := makeGoodPayload(t, origin, es, false)
				return assembleAndCompress(hLine, eLines)
			}(),
		},
		{
			name: "non_legacy_gap_in_seqs",
			key:  goodKey,
			data: func() []byte {
				es := makeTestEntries(origin, 1, 2)
				es[1].Seq = 3
				_, hLine, eLines := makeGoodPayload(t, origin, es, false)
				return assembleAndCompress(hLine, eLines)
			}(),
		},
		{
			name: "legacy_header_over_4096",
			key:  goodKey,
			data: func() []byte {
				h := baseHdr
				h.Legacy = true
				h.Count = 4097
				hBytes, _ := json.Marshal(h)
				return assembleAndCompress(string(hBytes), baseEntryLines)
			}(),
		},
		{
			name: "non_legacy_header_with_count_257",
			key:  goodKey,
			data: func() []byte {
				h := baseHdr
				h.Count = 257
				hBytes, _ := json.Marshal(h)
				return assembleAndCompress(string(hBytes), baseEntryLines)
			}(),
		},
		{
			name: "last_not_final_seq",
			key:  goodKey,
			data: func() []byte {
				h := baseHdr
				h.Last = baseEntries[len(baseEntries)-1].Seq + 1
				hBytes, _ := json.Marshal(h)
				return assembleAndCompress(string(hBytes), baseEntryLines)
			}(),
		},
		{
			name: "data_not_ending_in_newline",
			key:  goodKey,
			data: func() []byte {
				var buf bytes.Buffer
				buf.WriteString(baseHdrLine)
				buf.WriteByte('\n')
				buf.WriteString(strings.Join(baseEntryLines, "\n")) // no trailing \n
				return segmentEncoder.EncodeAll(buf.Bytes(), nil)
			}(),
		},
		{
			name: "blank_line_between_entries",
			key:  goodKey,
			data: func() []byte {
				e0, _ := json.Marshal(baseEntries[0])
				e1, _ := json.Marshal(baseEntries[1])
				rawEntries := string(e0) + "\n\n" + string(e1) + "\n"
				digest := sha256.Sum256([]byte(rawEntries))
				h := baseHdr
				h.Count = 3
				h.SHA256 = hex.EncodeToString(digest[:])
				hBytes, _ := json.Marshal(h)
				var buf bytes.Buffer
				buf.Write(hBytes)
				buf.WriteByte('\n')
				buf.WriteString(rawEntries)
				return segmentEncoder.EncodeAll(buf.Bytes(), nil)
			}(),
		},
		{
			name: "entry_not_json",
			key:  goodKey,
			data: func() []byte {
				rawEntries := "{not valid json\n"
				digest := sha256.Sum256([]byte(rawEntries))
				h := baseHdr
				h.Count = 1
				h.SHA256 = hex.EncodeToString(digest[:])
				hBytes, _ := json.Marshal(h)
				var buf bytes.Buffer
				buf.Write(hBytes)
				buf.WriteByte('\n')
				buf.WriteString(rawEntries)
				return segmentEncoder.EncodeAll(buf.Bytes(), nil)
			}(),
		},
		{
			name: "empty_data",
			key:  goodKey,
			data: []byte{},
		},
		{
			name: "data_not_zstd",
			key:  goodKey,
			data: []byte("this is plain text, not a zstd frame"),
		},
		{
			name: "legacy_first_not_first_entry_seq",
			key:  goodKey,
			data: func() []byte {
				es := makeTestEntries(origin, 5, 2) // first entry is 5
				_, _, eLines := makeGoodPayload(t, origin, es, true)
				// Header claims first is 1
				var rawEntries bytes.Buffer
				for _, l := range eLines {
					rawEntries.WriteString(l)
					rawEntries.WriteByte('\n')
				}
				digest := sha256.Sum256(rawEntries.Bytes())
				h := SegmentHeader{
					V:      SegmentVersion,
					Origin: origin,
					First:  1,
					Last:   es[len(es)-1].Seq,
					Count:  len(es),
					SHA256: hex.EncodeToString(digest[:]),
					Legacy: true,
				}
				hBytes, _ := json.Marshal(h)
				return assembleAndCompress(string(hBytes), eLines)
			}(),
		},
		{
			name: "legacy_descending_seqs",
			key:  goodKey,
			data: func() []byte {
				es := makeTestEntries(origin, 1, 2)
				es[1].Seq = 0 // not strictly increasing
				_, hLine, eLines := makeGoodPayload(t, origin, es, true)
				return assembleAndCompress(hLine, eLines)
			}(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, entries, err := DecodeSegment(tc.key, tc.data)
			if err == nil {
				t.Fatalf("DecodeSegment succeeded, want refusal; got %d entries", len(entries))
			}
			if !errors.Is(err, ErrBadSegment) {
				t.Errorf("error = %v, want ErrBadSegment", err)
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Errorf("error %q does not name key %q", err.Error(), tc.key)
			}
		})
	}
}

func TestDecodeSegmentTruncatedZstd(t *testing.T) {
	origin := "laptop"
	entries := makeTestEntries(origin, 1, 10)
	data, hdr, err := EncodeSegment(origin, entries)
	if err != nil {
		t.Fatalf("EncodeSegment: %v", err)
	}
	key := blobstore.SegmentKey(origin, hdr.First)

	for i := 1; i < 8; i++ {
		cutLen := len(data) * i / 8
		t.Run(fmt.Sprintf("cut_%d_eighths", i), func(t *testing.T) {
			_, decEntries, err := DecodeSegment(key, data[:cutLen])
			if err == nil {
				t.Fatalf("truncated %d/%d succeeded", cutLen, len(data))
			}
			if !errors.Is(err, ErrBadSegment) {
				t.Errorf("error = %v, want ErrBadSegment", err)
			}
			if decEntries != nil {
				t.Errorf("got %d entries, want nil", len(decEntries))
			}
		})
	}

	t.Run("length_minus_one", func(t *testing.T) {
		_, decEntries, err := DecodeSegment(key, data[:len(data)-1])
		if err == nil {
			t.Fatal("length-1 succeeded")
		}
		if !errors.Is(err, ErrBadSegment) {
			t.Errorf("error = %v, want ErrBadSegment", err)
		}
		if decEntries != nil {
			t.Errorf("got %d entries, want nil", len(decEntries))
		}
	})
}

func TestDecodeSegmentZipBomb(t *testing.T) {
	origin := "laptop"
	key := blobstore.SegmentKey(origin, 1)

	t.Run("decoded_size_exceeds_cap", func(t *testing.T) {
		raw := make([]byte, segmentMaxDecoded+(1<<20))
		compressed := segmentEncoder.EncodeAll(raw, nil)

		_, _, err := DecodeSegment(key, compressed)
		if err == nil {
			t.Fatal("DecodeSegment succeeded on zip bomb, want refusal")
		}
		if !errors.Is(err, errSegmentTooLarge) {
			t.Errorf("error = %v, want errSegmentTooLarge", err)
		}
		if !errors.Is(err, ErrBadSegment) {
			t.Errorf("error = %v, want ErrBadSegment", err)
		}
		wantBound := fmt.Sprintf("%d decoded bytes", segmentMaxDecoded+1)
		if !strings.Contains(err.Error(), wantBound) {
			t.Errorf("error %q does not show read stopped at %d", err.Error(), segmentMaxDecoded+1)
		}
	})

	t.Run("window_larger_than_32mib", func(t *testing.T) {
		var buf bytes.Buffer
		enc, err := zstd.NewWriter(&buf, zstd.WithWindowSize(64<<20))
		if err != nil {
			t.Fatalf("zstd.NewWriter: %v", err)
		}
		// Write 35 MiB to force zstd frame to declare a window > 32 MiB.
		chunk := make([]byte, 1024*1024)
		for i := 0; i < 35; i++ {
			if _, err := enc.Write(chunk); err != nil {
				t.Fatalf("enc.Write: %v", err)
			}
		}
		if err := enc.Close(); err != nil {
			t.Fatalf("enc.Close: %v", err)
		}

		_, _, err = DecodeSegment(key, buf.Bytes())
		if err == nil {
			t.Fatal("DecodeSegment succeeded on oversized window, want refusal")
		}
		if !errors.Is(err, ErrBadSegment) {
			t.Errorf("error = %v, want ErrBadSegment", err)
		}
	})
}

func TestDecodeSegmentOversizeObject(t *testing.T) {
	origin := "laptop"
	key := blobstore.SegmentKey(origin, 1)
	data := make([]byte, segmentMaxEncoded+1)

	_, _, err := DecodeSegment(key, data)
	if err == nil {
		t.Fatal("DecodeSegment succeeded on oversize object, want refusal")
	}
	if !errors.Is(err, ErrBadSegment) {
		t.Errorf("error = %v, want ErrBadSegment", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "bytes, over the") {
		t.Errorf("error %q does not name size", msg)
	}
	if strings.Contains(msg, "zstd") {
		t.Errorf("error %q mentions zstd, should refuse before decode", msg)
	}
}
