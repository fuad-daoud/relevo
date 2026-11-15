package syncworker

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/fuad-daoud/relevo/internal/blobstore"
	"github.com/klauspost/compress/zstd"
)

// errSegmentTooLarge marks the one refusal whose cause is size alone. It is
// wrapped in ErrBadSegment, but kept distinct so a test -- and a caller that
// wants to drop the object rather than retry it -- can tell a bound from any
// other way a segment can be bad.
var errSegmentTooLarge = errors.New("segment decodes to more than the cap")

// decodeSegmentWindow matches the largest window an encoder is allowed to use
// on a segment. A frame may claim a larger one only as an attack: a small
// window cannot expand beyond the decoded cap, so a claim beyond it is refused
// before any memory is spent on it.
const decodeSegmentWindow = 32 << 20

// DecodeSegment decompresses one segment object and returns its header and
// entries. The key names what the caller expected, so every refusal carries
// it: a segment is fetched by key, and a mismatch between key and object is
// the store serving the wrong bytes.
func DecodeSegment(key string, data []byte) (SegmentHeader, []Entry, error) {
	origin, first, ok := blobstore.ParseSegmentKey(key)
	if !ok {
		return SegmentHeader{}, nil, fmt.Errorf("%w: %q is not a segment key", ErrBadSegment, key)
	}
	if len(data) > segmentMaxEncoded {
		return SegmentHeader{}, nil, fmt.Errorf("%w: %q is %d bytes, over the %d-byte cap", ErrBadSegment, key, len(data), segmentMaxEncoded)
	}
	payload, err := decodeSegmentPayload(key, data)
	if err != nil {
		return SegmentHeader{}, nil, err
	}
	header, entryBytes, err := decodeSegmentHeader(key, origin, first, payload)
	if err != nil {
		return SegmentHeader{}, nil, err
	}
	entries, err := decodeSegmentEntries(key, header, entryBytes)
	if err != nil {
		return SegmentHeader{}, nil, err
	}
	return header, entries, nil
}

// decodeSegmentPayload decompresses the object. A fresh decoder per call holds
// no history between segments, and it is closed on return for the same reason.
// The read is bounded, not the frame: a zstd frame can expand by a factor of
// a thousand, so only a reader that stops at the cap keeps a hostile object
// from spending its host's memory.
func decodeSegmentPayload(key string, data []byte) ([]byte, error) {
	dec, _ := zstd.NewReader(bytes.NewReader(data),
		zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderMaxWindow(decodeSegmentWindow))
	defer dec.Close()
	payload, err := io.ReadAll(io.LimitReader(dec, segmentMaxDecoded+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %q: not a zstd frame: %w", ErrBadSegment, key, err)
	}
	if len(payload) > segmentMaxDecoded {
		return nil, fmt.Errorf("%w: %q: %d decoded bytes: %w", ErrBadSegment, key, len(payload), errSegmentTooLarge)
	}
	return payload, nil
}

// decodeSegmentHeader splits off and decodes the first line, then checks the
// fields the key already knows: a header that disagrees with the key it
// was fetched under describes an object the caller did not ask for.
func decodeSegmentHeader(key, origin string, first int, payload []byte) (SegmentHeader, []byte, error) {
	line, rest, ok := bytes.Cut(payload, []byte("\n"))
	if !ok {
		return SegmentHeader{}, nil, fmt.Errorf("%w: %q has no header line", ErrBadSegment, key)
	}
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	var header SegmentHeader
	if err := dec.Decode(&header); err != nil {
		return SegmentHeader{}, nil, fmt.Errorf("%w: %q: header: %w", ErrBadSegment, key, err)
	}
	if dec.More() || dec.InputOffset() < int64(len(line)) {
		return SegmentHeader{}, nil, fmt.Errorf("%w: %q: trailing bytes after the header", ErrBadSegment, key)
	}
	if header.V != SegmentVersion {
		return SegmentHeader{}, nil, fmt.Errorf("%w: %q: header version %d, want %d", ErrBadSegment, key, header.V, SegmentVersion)
	}
	if header.Origin != origin {
		return SegmentHeader{}, nil, fmt.Errorf("%w: %q: header origin %q is not the key's", ErrBadSegment, key, header.Origin)
	}
	if header.First != first {
		return SegmentHeader{}, nil, fmt.Errorf("%w: %q: header first %d is not the key's %d", ErrBadSegment, key, header.First, first)
	}
	return header, rest, nil
}

// decodeSegmentEntries checks the entry lines against the header, which is the
// only thing a remote segment is held to: the header already names how many
// entries there are and which seqs they span, so a line that escapes either
// makes the header a lie worth refusing the whole object for.
func decodeSegmentEntries(key string, header SegmentHeader, entryBytes []byte) ([]Entry, error) {
	max := SegmentMaxEntries
	if header.Legacy {
		max = SegmentLegacyMaxEntries
	}
	if header.Count <= 0 || header.Count > max {
		return nil, fmt.Errorf("%w: %q: header count %d, want 1 to %d", ErrBadSegment, key, header.Count, max)
	}
	if !bytes.HasSuffix(entryBytes, []byte("\n")) {
		return nil, fmt.Errorf("%w: %q: entry lines do not end in a newline", ErrBadSegment, key)
	}
	lines := bytes.Split(entryBytes[:len(entryBytes)-1], []byte("\n"))
	if len(lines) != header.Count {
		return nil, fmt.Errorf("%w: %q: %d entry lines, header says %d", ErrBadSegment, key, len(lines), header.Count)
	}
	digest := sha256.Sum256(entryBytes)
	if hex.EncodeToString(digest[:]) != header.SHA256 {
		return nil, fmt.Errorf("%w: %q: entry digest does not match the header's", ErrBadSegment, key)
	}
	entries := make([]Entry, 0, header.Count)
	for i, line := range lines {
		if len(line) == 0 {
			return nil, fmt.Errorf("%w: %q: blank line at entry %d", ErrBadSegment, key, i)
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("%w: %q: entry %d: %w", ErrBadSegment, key, i, err)
		}
		if e.Origin != header.Origin {
			return nil, fmt.Errorf("%w: %q: entry %d has origin %q, want the header's %q", ErrBadSegment, key, i, e.Origin, header.Origin)
		}
		if !header.Legacy {
			if e.Seq != header.First+i {
				return nil, fmt.Errorf("%w: %q: entry %d has seq %d, want %d", ErrBadSegment, key, i, e.Seq, header.First+i)
			}
		} else if i > 0 && e.Seq <= entries[i-1].Seq {
			return nil, fmt.Errorf("%w: %q: legacy entry %d has seq %d, not above the previous %d", ErrBadSegment, key, i, e.Seq, entries[i-1].Seq)
		}
		entries = append(entries, e)
	}
	if header.First != entries[0].Seq {
		return nil, fmt.Errorf("%w: %q: header first %d is not the first entry's %d", ErrBadSegment, key, header.First, entries[0].Seq)
	}
	if header.Last != entries[len(entries)-1].Seq {
		return nil, fmt.Errorf("%w: %q: header last %d is not the last entry's %d", ErrBadSegment, key, header.Last, entries[len(entries)-1].Seq)
	}
	return entries, nil
}
