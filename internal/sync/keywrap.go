package sync

// What an older epoch's key looks like at rest on the remote: sealed under the
// key that replaced it, so a machine holding the newest key unwraps the whole
// chain down to epoch 1 and nothing unwraps forwards. Everything here is pure
// -- no database, no clock, no transport -- so the rule a reader refuses a
// wrapped row by is a rule a test pins without a machine behind it.

import (
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash"
	"io"
	"strconv"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"

	"github.com/fuad-daoud/relevo/internal/db"
)

// wrapKeyLabel is the HKDF info that separates the key a wrapped epoch is
// sealed under from the sync key it is derived from, so the bytes that decrypt
// an epoch key are never the bytes that sign an entry or open a secret.
const wrapKeyLabel = "relevo-sync-keywrap-v1"

// wrapNonceLabel is the HKDF info that separates the key the wrapping nonce is
// hashed under from the AEAD key itself, on the same rule the secret envelope's
// nonce uses: distinct uses of one key need labels, not operator discipline.
const wrapNonceLabel = "relevo-sync-keywrap-nonce-v1"

// WrapKey seals an older epoch's key under the key that replaced it. The bytes
// it returns are what the remote stores as key_wrapped.<epoch>: the derived
// nonce ahead of the ciphertext, so the row is one value that opens whole.
//
// Two wraps of one (newer, epoch, key) produce the same bytes, because the
// nonce is derived rather than random. That is the same trade the secret
// envelope makes: a value equal to one already wrapped seals to a row equal to
// the one already there, and what that reveals to a log reader is only that the
// chain holds a key it held before.
func WrapKey(newer []byte, epoch int, key []byte) ([]byte, error) {
	aead, nonce, err := wrapCipher(newer, epoch, key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(nonce)+len(key)+aead.Overhead())
	out = append(out, nonce...)
	return append(out, aead.Seal(nil, nonce, key, wrapAD(epoch))...), nil
}

// UnwrapKey opens one wrapped row back into the epoch key it holds. Every
// refusal here happens before a single key byte is produced, and the refusal is
// the remote's own: the newest key was already checked, so a row that will not
// open under it is a chain this machine cannot walk, and that is a refusal
// about the remote's material rather than about the passphrase.
func UnwrapKey(newer []byte, epoch int, wrapped []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(wrapSubkeyOf(newer))
	if err != nil {
		return nil, fmt.Errorf("sync: open the wrapped key for epoch %d: %w: %w", epoch, err, db.ErrInvalid)
	}
	if len(wrapped) < chacha20poly1305.NonceSizeX+aead.Overhead() {
		return nil, fmt.Errorf("sync: the wrapped key for epoch %d is %d bytes, too short to open: %w",
			epoch, len(wrapped), ErrKDFParams)
	}
	nonce := wrapped[:chacha20poly1305.NonceSizeX]
	key, err := aead.Open(nil, nonce, wrapped[chacha20poly1305.NonceSizeX:], wrapAD(epoch))
	if err != nil {
		return nil, fmt.Errorf("sync: the wrapped key for epoch %d does not open under this machine's key: %w",
			epoch, ErrKDFParams)
	}
	return key, nil
}

// wrapCipher builds the cipher, the subkey and the nonce one wrap runs under,
// refusing an empty key on either side: a wrap of nothing would store a row
// that opens to nothing, and a wrap under nothing could not be opened by
// anybody.
func wrapCipher(newer []byte, epoch int, key []byte) (cipher.AEAD, []byte, error) {
	if len(newer) == 0 || len(key) == 0 {
		return nil, nil, fmt.Errorf("sync: wrap epoch %d: an epoch key is empty: %w", epoch, db.ErrInvalid)
	}
	aead, err := chacha20poly1305.NewX(wrapSubkeyOf(newer))
	if err != nil {
		return nil, nil, fmt.Errorf("sync: wrap epoch %d: open the cipher: %w: %w", epoch, err, db.ErrInvalid)
	}
	nonce, err := wrapNonce(chacha20poly1305.NonceSizeX, newer, epoch, key)
	if err != nil {
		return nil, nil, err
	}
	return aead, nonce, nil
}

// wrapSubkeyOf derives the key the wrapped epoch is sealed under from the key
// that replaced it. It is derived rather than reused so the bytes that decrypt
// an epoch key and the bytes that authenticate an entry are never the same.
func wrapSubkeyOf(newer []byte) []byte {
	sub := make([]byte, secretSubkeyLen)
	r := hkdf.New(sha256.New, newer, nil, []byte(wrapKeyLabel))
	_, _ = io.ReadFull(r, sub)
	return sub
}

// wrapNonce derives the sealing nonce from what the row is of: HMAC-SHA256
// under a key derived from the wrapping key with its own label, over a
// length-prefixed encoding of the epoch and the key being sealed. The prefix
// keeps the epoch's digits from running into the key's first byte, which would
// let one (epoch, key) pair hash to another pair's nonce.
func wrapNonce(size int, newer []byte, epoch int, key []byte) ([]byte, error) {
	hkey := make([]byte, secretSubkeyLen)
	r := hkdf.New(sha256.New, wrapSubkeyOf(newer), nil, []byte(wrapNonceLabel))
	if _, err := io.ReadFull(r, hkey); err != nil {
		return nil, fmt.Errorf("sync: wrap epoch %d: derive the nonce key: %w: %w", epoch, err, db.ErrInvalid)
	}
	mac := hmac.New(sha256.New, hkey)
	writeWrapField(mac, []byte(strconv.Itoa(epoch)))
	writeWrapField(mac, key)
	return mac.Sum(nil)[:size], nil
}

// wrapAD binds the wrapped row to its epoch pair: the label, the epoch being
// sealed and the epoch doing the sealing. A row carried to another epoch's
// slot -- or opened claiming another pair -- fails to authenticate rather than
// being read as that pair's key.
func wrapAD(epoch int) []byte {
	var out []byte
	out = appendWrapField(out, []byte(wrapKeyLabel))
	out = appendWrapField(out, []byte(strconv.Itoa(epoch)))
	out = appendWrapField(out, []byte(strconv.Itoa(epoch+1)))
	return out
}

// writeWrapField hashes one length-prefixed field into the nonce.
func writeWrapField(mac hash.Hash, field []byte) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(field)))
	mac.Write(length[:])
	mac.Write(field)
}

// appendWrapField appends one length-prefixed field to the associated data.
func appendWrapField(out, field []byte) []byte {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(field)))
	out = append(out, length[:]...)
	return append(out, field...)
}
