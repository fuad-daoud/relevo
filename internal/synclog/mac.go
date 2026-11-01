package synclog

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

const macDomainV1 = "relevo-sync-entry-v1"

// macDomainV2 labels the v2 encoding, covering After and ctl: entries.
const macDomainV2 = "relevo-sync-entry-v2"

func isV2(e Entry) bool {
	return e.After != nil || strings.HasPrefix(e.Table, "ctl:")
}

// EntryMAC is the entry's HMAC-SHA256 over its canonical encoding: both halves
// must hash exactly the same bytes. Seq and Batch are left out: the transport
// assigns them after signing, so covering them would make every entry
// unverifiable.
func EntryMAC(key []byte, e Entry) []byte {
	var buf bytes.Buffer
	domain := macDomainV1
	if isV2(e) {
		domain = macDomainV2
	}
	writeMACField(&buf, []byte(domain))
	writeMACField(&buf, []byte(e.Origin))
	writeMACField(&buf, []byte(e.Table))
	writeMACField(&buf, []byte(e.PK))
	writeMACField(&buf, []byte(e.Op))
	writeMACField(&buf, []byte(strconv.Itoa(e.SchemaVersion)))
	writeMacBody(&buf, e.Body)
	writeMACField(&buf, []byte(e.At.UTC().Format(time.RFC3339Nano)))
	if isV2(e) {
		if e.After != nil {
			writeMACField(&buf, []byte(e.After.Origin))
			writeMACField(&buf, []byte(strconv.Itoa(e.After.Seq)))
		} else {
			writeMACField(&buf, nil)
			writeMACField(&buf, nil)
		}
	}

	mac := hmac.New(sha256.New, key)
	mac.Write(buf.Bytes())
	return mac.Sum(nil)
}

// boundaryDomain labels the tag binding an origin's signed-from sequence to the
// key, so a reader tells a vouched boundary from a made-up one.
const boundaryDomain = "relevo-sync-signed-from-v1"

func BoundaryTag(key []byte, origin string, seq int) []byte {
	var buf bytes.Buffer
	writeMACField(&buf, []byte(boundaryDomain))
	writeMACField(&buf, []byte(origin))
	writeMACField(&buf, []byte(strconv.Itoa(seq)))

	mac := hmac.New(sha256.New, key)
	mac.Write(buf.Bytes())
	return mac.Sum(nil)
}

func VerifyBoundary(key []byte, origin string, seq int, tag []byte) bool {
	return hmac.Equal(tag, BoundaryTag(key, origin, seq))
}

// epochCutDomain labels the tag binding an origin's epoch cut to the key. It is
// fresh rather than the boundary's, so a cut tag can never double as a
// signed-from tag or entry MAC.
const epochCutDomain = "relevo-sync-epoch-cut-v1"

// EpochCutTag is the HMAC-SHA256 binding an origin's cut, epoch and sequence,
// to key.
func EpochCutTag(key []byte, origin string, epoch, seq int) []byte {
	var buf bytes.Buffer
	writeMACField(&buf, []byte(epochCutDomain))
	writeMACField(&buf, []byte(origin))
	writeMACField(&buf, []byte(strconv.Itoa(epoch)))
	writeMACField(&buf, []byte(strconv.Itoa(seq)))

	mac := hmac.New(sha256.New, key)
	mac.Write(buf.Bytes())
	return mac.Sum(nil)
}

func VerifyEpochCut(key []byte, origin string, epoch, seq int, tag []byte) bool {
	return hmac.Equal(tag, EpochCutTag(key, origin, epoch, seq))
}

// VerifyMAC reports whether the entry's MAC matches its fields, comparing with
// hmac.Equal so a caller learns only agreement, not how far into disagreeing.
func VerifyMAC(key []byte, e Entry) bool {
	return hmac.Equal(e.MAC, EntryMAC(key, e))
}

// writeMACField appends one length-prefixed field. The length stops one field
// running into the next: without it two different entries could share a MAC.
func writeMACField(buf *bytes.Buffer, field []byte) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(field)))
	buf.Write(length[:])
	buf.Write(field)
}

// writeMacBody appends the body as the log holds it, through the pipe's own
// re-encoding on both sides so both agree. A ref entry is signed as the
// reference, never the content behind it, or the signature would depend on an
// unreadable bucket.
func writeMacBody(buf *bytes.Buffer, body json.RawMessage) {
	if len(body) == 0 {
		writeMACField(buf, nil)
		return
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		// A non-JSON body is signed as it stands rather than left out, which
		// would let anything stand in for it.
		writeMACField(buf, body)
		return
	}
	writeMACField(buf, encoded)
}
