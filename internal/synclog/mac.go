package synclog

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"strconv"
	"time"
)

// macDomain labels this encoding. The label travels inside the signed bytes
// rather than beside them, so a MAC computed over some other construction that
// happens to share these fields is not read as one of ours.
const macDomain = "relevo-sync-entry-v1"

// EntryMAC is the entry's HMAC-SHA256 over its canonical encoding, which is
// EntryMAC's whole definition: the two halves have to be computed over exactly
// the same bytes or a signed log stops verifying.
//
// Seq and Batch are left out on purpose. The transport assigns them after the
// entry is signed, so the numbers that end up stored are not the numbers the
// signer saw and covering them would make every entry unverifiable.
func EntryMAC(key []byte, e Entry) []byte {
	var buf bytes.Buffer
	writeMACField(&buf, []byte(macDomain))
	writeMACField(&buf, []byte(e.Origin))
	writeMACField(&buf, []byte(e.Table))
	writeMACField(&buf, []byte(e.PK))
	writeMACField(&buf, []byte(e.Op))
	writeMACField(&buf, []byte(strconv.Itoa(e.SchemaVersion)))
	writeMacBody(&buf, e.Body)
	writeMACField(&buf, []byte(e.At.UTC().Format(time.RFC3339Nano)))

	mac := hmac.New(sha256.New, key)
	mac.Write(buf.Bytes())
	return mac.Sum(nil)
}

// boundaryDomain labels the tag that binds an origin's signed-from sequence to
// the key, so a reader can tell a boundary this key vouched for from one a
// remote writer made up.
const boundaryDomain = "relevo-sync-signed-from-v1"

// BoundaryTag is the HMAC-SHA256 that binds seq to origin under key.
func BoundaryTag(key []byte, origin string, seq int) []byte {
	var buf bytes.Buffer
	writeMACField(&buf, []byte(boundaryDomain))
	writeMACField(&buf, []byte(origin))
	writeMACField(&buf, []byte(strconv.Itoa(seq)))

	mac := hmac.New(sha256.New, key)
	mac.Write(buf.Bytes())
	return mac.Sum(nil)
}

// VerifyBoundary reports whether tag is the one key makes for origin and seq.
func VerifyBoundary(key []byte, origin string, seq int, tag []byte) bool {
	return hmac.Equal(tag, BoundaryTag(key, origin, seq))
}

// epochCutDomain labels the tag that binds an origin's epoch cut to the key, so
// a reader can tell a cut this key vouched for from one a remote writer made
// up. It is a fresh label rather than the boundary's, so a cut tag can never
// double as a signed-from tag or an entry MAC.
const epochCutDomain = "relevo-sync-epoch-cut-v1"

// EpochCutTag is the HMAC-SHA256 that binds an origin's cut -- the epoch its
// later entries must verify under and the sequence the cut sits at -- to key.
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

// VerifyEpochCut reports whether tag is the one key makes for origin, epoch and
// seq.
func VerifyEpochCut(key []byte, origin string, epoch, seq int, tag []byte) bool {
	return hmac.Equal(tag, EpochCutTag(key, origin, epoch, seq))
}

// VerifyMAC reports whether the entry's MAC is the one its fields make. It
// compares with hmac.Equal rather than bytes.Equal, so a caller learns only
// that the two agree and not how far they are into disagreeing.
func VerifyMAC(key []byte, e Entry) bool {
	return hmac.Equal(e.MAC, EntryMAC(key, e))
}

// writeMACField appends one length-prefixed field.
//
// The length is what stops one field running into the next: without it a key
// ending in a digit and a table beginning with the same digit would encode to
// the same bytes as a key and table that differ, and two different entries
// would share a MAC.
func writeMACField(buf *bytes.Buffer, field []byte) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(field)))
	buf.Write(length[:])
	buf.Write(field)
}

// writeMacBody appends the body as the log holds it.
//
// The pipe re-encodes a json.RawMessage, which compacts it and escapes <, > and
// &, so the bytes that come back are not always the bytes the exporter signed.
// Putting the body through that same encoding on both sides is what makes the
// two agree, whichever encoder produced the body. A body is signed as stored:
// an entry naming an R2 object is signed as the reference, never as the content
// behind it, or the signature would depend on a bucket this machine may not be
// able to read.
func writeMacBody(buf *bytes.Buffer, body json.RawMessage) {
	if len(body) == 0 {
		writeMACField(buf, nil)
		return
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		// A body that is not JSON carries no meaning to a reader either, so it
		// is signed as it stands rather than being left out of the MAC, which
		// would let anything at all stand in for it.
		writeMACField(buf, body)
		return
	}
	writeMACField(buf, encoded)
}
