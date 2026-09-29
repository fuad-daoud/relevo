package remote

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
)

// ErrKeyFormat indicates that key data is malformed or invalidly encoded.
var ErrKeyFormat = errors.New("invalid key format")

// ErrKeyType indicates that a key is not an ed25519 key.
var ErrKeyType = errors.New("invalid key type; ed25519 required")

const pemTypePrivate = "RELEVO ED25519 PRIVATE KEY"

// ClientID is "SHA256:" + base64.RawStdEncoding(sha256(raw 32-byte ed25519 public key)).
// Same shape as ssh-keygen -l; 7 + 43 = 50 characters. Never empty.
type ClientID string

// Keypair contains an ed25519 private key and its corresponding public key.
type Keypair struct {
	Private ed25519.PrivateKey // 64 bytes
	Public  ed25519.PublicKey  // 32 bytes
}

// Generate generates a fresh ed25519 keypair using crypto/rand.
func Generate() (Keypair, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Keypair{}, err
	}
	return Keypair{
		Private: priv,
		Public:  pub,
	}, nil
}

// IDOf returns the ClientID for the given ed25519 public key.
func IDOf(pub ed25519.PublicKey) ClientID {
	sum := sha256.Sum256(pub)
	return ClientID("SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]))
}

// Dir is the id as a path component: the 32-byte digest in lower-case hex
// (64 characters), with no prefix. Safe for any filesystem; one-to-one with
// the id. ("", false) for a string that is not a well-formed ClientID.
func (id ClientID) Dir() (string, bool) {
	s := string(id)
	if !strings.HasPrefix(s, "SHA256:") {
		return "", false
	}
	rest := strings.TrimPrefix(s, "SHA256:")
	raw, err := base64.RawStdEncoding.DecodeString(rest)
	if err != nil || len(raw) != 32 {
		return "", false
	}
	if base64.RawStdEncoding.EncodeToString(raw) != rest {
		return "", false
	}
	return hex.EncodeToString(raw), true
}

// IDFromDir is the inverse: 64 hex characters -> ClientID; ("", false)
// otherwise.
func IDFromDir(dir string) (ClientID, bool) {
	if len(dir) != 64 {
		return "", false
	}
	for i := 0; i < len(dir); i++ {
		c := dir[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", false
		}
	}
	raw, err := hex.DecodeString(dir)
	if err != nil || len(raw) != 32 {
		return "", false
	}
	return ClientID("SHA256:" + base64.RawStdEncoding.EncodeToString(raw)), true
}

// MarshalPrivate encodes k's private key as a PEM block of type "RELEVO ED25519 PRIVATE KEY".
func MarshalPrivate(k Keypair) ([]byte, error) {
	if len(k.Private) != ed25519.PrivateKeySize {
		return nil, ErrKeyFormat
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  pemTypePrivate,
		Bytes: []byte(k.Private),
	}), nil
}

// ParsePrivate parses a PEM-encoded private key. The PEM block must be of type
// "RELEVO ED25519 PRIVATE KEY"; any other type, or an invalid key length,
// returns an error.
func ParsePrivate(data []byte) (Keypair, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return Keypair{}, ErrKeyFormat
	}
	if block.Type != pemTypePrivate {
		return Keypair{}, ErrKeyType
	}
	if len(block.Bytes) != ed25519.PrivateKeySize {
		return Keypair{}, ErrKeyFormat
	}
	priv := ed25519.PrivateKey(block.Bytes)
	pub := priv.Public().(ed25519.PublicKey)
	return Keypair{
		Private: priv,
		Public:  pub,
	}, nil
}

// MarshalPublic formats an ed25519 public key as "ed25519 <base64>[ <comment>]".
func MarshalPublic(pub ed25519.PublicKey, comment string) string {
	b64 := base64.StdEncoding.EncodeToString(pub)
	if comment != "" {
		return "ed25519 " + b64 + " " + comment
	}
	return "ed25519 " + b64
}

// ParsePublic parses an enrollment line formatted as "ed25519 <base64>[ <comment>]".
// If the format is invalid or the key length is not 32 bytes, ErrKeyFormat is returned.
func ParsePublic(line string) (ed25519.PublicKey, error) {
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "ed25519 ") {
		return nil, ErrKeyFormat
	}
	rest := strings.TrimPrefix(line, "ed25519 ")
	parts := strings.SplitN(rest, " ", 2)
	b64Key := parts[0]
	if b64Key == "" {
		return nil, ErrKeyFormat
	}
	raw, err := base64.StdEncoding.DecodeString(b64Key)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrKeyFormat, err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: expected %d bytes, got %d", ErrKeyFormat, ed25519.PublicKeySize, len(raw))
	}
	return ed25519.PublicKey(raw), nil
}
