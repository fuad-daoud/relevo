package syncworker

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/blobstore"
	"github.com/klauspost/compress/zstd"
)

// SegmentVersion is the header version a segment announces. A decoder refuses
// any other number rather than guessing at an unknown layout.
const SegmentVersion = 1

// SegmentMaxEntries and SegmentLegacyMaxEntries bound how many entries one
// segment holds. Legacy is the pre-segment batch size, so a migration packs
// 4096; new segments are cut at 256 so one object stays small enough to fetch,
// check and retry whole.
const (
	SegmentMaxEntries       = 256
	SegmentLegacyMaxEntries = 4096
)

// The byte caps of one segment object. A legacy segment of 4096 average 16 KiB
// rows fits the decoded cap; anything larger is cut by the writer and never
// read, so the decoder can refuse it without a threshold to tune. The encoder
// refuses to produce an object either cap would reject, so every segment it
// returns decodes.
const (
	segmentMaxEncoded = 16 << 20
	segmentMaxDecoded = 64 << 20
)

// ErrBadSegment reports an object that is not a usable segment: a corrupt or
// truncated frame, a header that lies about its entries, or an encode request
// the segment shape cannot hold. Every refusal of a segment wraps it, so a
// caller can tell a bad object -- worth reporting and re-cutting -- from a
// failed fetch, which is worth retrying.
var ErrBadSegment = errors.New("syncworker: bad log segment")

// SegmentHeader is the first line of a segment's payload. It travels inside
// the object rather than beside it, so a segment fetched alone carries its own
// description.
type SegmentHeader struct {
	// V is the segment layout version.
	V int `json:"v"`
	// Origin is the installation the entries belong to. It is repeated on
	// every entry, and the decoder refuses an entry that names another.
	Origin string `json:"origin"`
	// First and Last are the first and last entry's Seq.
	First int `json:"first"`
	Last  int `json:"last"`
	// Count is the number of entries behind the header line.
	Count int `json:"count"`
	// SHA256 is the hex digest of the entry lines exactly as stored, their
	// newlines included and the header line excluded.
	SHA256 string `json:"sha256"`
	// Legacy marks a segment written by the 4096-entry migration: its seqs
	// need only increase, not be contiguous. A reader cannot be told this
	// from the bytes, so the writer declares it here.
	Legacy bool `json:"legacy,omitempty"`
}

// One encoder is shared by every segment. EncodeAll is safe for concurrent
// calls, so writers share it without a lock. The level and concurrency follow
// internal/db's column codec -- level 9 measured about 12% smaller on real
// streams -- but the encoder is built here rather than imported, so a segment's
// tuning cannot move with a column's.
var segmentEncoder = newSegmentEncoder()

const segmentCodecConcurrency = 2

func newSegmentEncoder() *zstd.Encoder {
	enc, _ := zstd.NewWriter(nil,
		zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(9)),
		zstd.WithEncoderConcurrency(segmentCodecConcurrency))
	return enc
}

// EncodeSegment compresses entries into one segment object: line 1 the
// SegmentHeader as JSON, then each entry as its JSON, one per line. The seqs
// must be First, First+1, ... with no gap, so a reader can reject a segment
// that skips an entry it was promised.
func EncodeSegment(origin string, entries []Entry) ([]byte, SegmentHeader, error) {
	return encodeSegment(origin, entries, false)
}

// EncodeLegacySegment is EncodeSegment for a legacy-shaped segment: up to
// SegmentLegacyMaxEntries entries whose seqs need only strictly increase. It
// serves the migration that repacks an existing log; new segments go through
// EncodeSegment.
func EncodeLegacySegment(origin string, entries []Entry) ([]byte, SegmentHeader, error) {
	return encodeSegment(origin, entries, true)
}

func encodeSegment(origin string, entries []Entry, legacy bool) ([]byte, SegmentHeader, error) {
	if err := blobstore.CheckOrigin(origin); err != nil {
		return nil, SegmentHeader{}, fmt.Errorf("%w: %w", ErrBadSegment, err)
	}
	max := SegmentMaxEntries
	if legacy {
		max = SegmentLegacyMaxEntries
	}
	if len(entries) == 0 {
		return nil, SegmentHeader{}, fmt.Errorf("%w: a segment holds at least one entry", ErrBadSegment)
	}
	if len(entries) > max {
		return nil, SegmentHeader{}, fmt.Errorf("%w: %d entries, over the %d-entry cap", ErrBadSegment, len(entries), max)
	}
	if err := checkSegmentSeqs(origin, entries, legacy); err != nil {
		return nil, SegmentHeader{}, err
	}
	header := SegmentHeader{
		V:      SegmentVersion,
		Origin: origin,
		First:  entries[0].Seq,
		Last:   entries[len(entries)-1].Seq,
		Count:  len(entries),
		Legacy: legacy,
	}
	// The digest is taken before the header line is written, because the
	// header carries it: an entry line is not available to the header until
	// the digest over all of them exists.
	var entryLines bytes.Buffer
	for i := range entries {
		line, err := json.Marshal(entries[i])
		if err != nil {
			return nil, SegmentHeader{}, fmt.Errorf("%w: entry %d: %w", ErrBadSegment, i, err)
		}
		entryLines.Write(line)
		entryLines.WriteByte('\n')
	}
	// The digest covers the entry lines exactly as stored, newlines included,
	// so a decoder can check the bytes it read rather than what it re-marshals.
	digest := sha256.Sum256(entryLines.Bytes())
	header.SHA256 = hex.EncodeToString(digest[:])
	line, _ := json.Marshal(header)
	var payload bytes.Buffer
	payload.Write(line)
	payload.WriteByte('\n')
	payload.Write(entryLines.Bytes())
	if payload.Len() > segmentMaxDecoded {
		return nil, SegmentHeader{}, fmt.Errorf("%w: payload is %d bytes, over the %d-byte decoded cap", ErrBadSegment, payload.Len(), segmentMaxDecoded)
	}
	object := segmentEncoder.EncodeAll(payload.Bytes(), nil)
	if len(object) > segmentMaxEncoded {
		return nil, SegmentHeader{}, fmt.Errorf("%w: object compresses to %d bytes, over the %d-byte cap", ErrBadSegment, len(object), segmentMaxEncoded)
	}
	return object, header, nil
}

// checkSegmentSeqs refuses an entry set the header could not describe honestly:
// an entry from another origin, a negative seq, and -- because a digest over a
// lying header would still match -- a seq pattern the segment kind forbids.
func checkSegmentSeqs(origin string, entries []Entry, legacy bool) error {
	first := entries[0].Seq
	for i := range entries {
		if entries[i].Origin != origin {
			return fmt.Errorf("%w: entry %d has origin %q, want %q", ErrBadSegment, i, entries[i].Origin, origin)
		}
		if entries[i].Seq < 0 {
			return fmt.Errorf("%w: entry %d has negative seq %d", ErrBadSegment, i, entries[i].Seq)
		}
		if legacy {
			if i > 0 && entries[i].Seq <= entries[i-1].Seq {
				return fmt.Errorf("%w: legacy entry %d has seq %d, not above the previous %d", ErrBadSegment, i, entries[i].Seq, entries[i-1].Seq)
			}
		} else if entries[i].Seq != first+i {
			return fmt.Errorf("%w: entry %d has seq %d, want %d: the seqs must be first, first+1, first+2", ErrBadSegment, i, entries[i].Seq, first+i)
		}
	}
	return nil
}
