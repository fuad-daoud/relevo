package verb

import (
	"fmt"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/sync"
)

// The frame's raw tail carries every secret an enable needs and nothing else:
// the header is what a captured stream, log or error shows. The split happens
// here, once, from the end.

// EnableIntake splits an enable's raw tail into token, passphrase and bucket
// secret. It splits from the end in reverse arrival order; a length the tail
// cannot hold is refused, since clamping would store one secret as another.
func EnableIntake(verb *wire.SyncVerb, tail []byte) (token, passphrase []byte, r2 sync.R2Intake, err error) {
	secretLen, passLen := verb.R2SecretLen, verb.PassphraseLen
	if secretLen < 0 || passLen < 0 || secretLen+passLen > len(tail) {
		return nil, nil, sync.R2Intake{}, fmt.Errorf("sync: enable: the frame names a %d-byte passphrase and a %d-byte bucket secret in a %d-byte tail: %w",
			passLen, secretLen, len(tail), db.ErrInvalid)
	}
	cut := len(tail) - secretLen
	return tail[:cut-passLen], tail[cut-passLen : cut], sync.R2Intake{
		Endpoint: verb.R2Endpoint,
		Bucket:   verb.R2Bucket,
		KeyID:    verb.R2KeyID,
		Secret:   tail[cut:],
	}, nil
}

// RekeyIntake splits a rekey's raw tail into current and new passphrase. A tail
// the two lengths do not exactly cover is refused rather than trimmed, or one
// passphrase would store as the other.
func RekeyIntake(verb *wire.SyncVerb, tail []byte) (current, newPass []byte, err error) {
	passLen, newLen := verb.PassphraseLen, verb.NewPassphraseLen
	if passLen < 0 || newLen < 0 || passLen+newLen != len(tail) {
		return nil, nil, fmt.Errorf("sync: rekey: the frame names a %d-byte passphrase and a %d-byte new passphrase in a %d-byte tail: %w",
			passLen, newLen, len(tail), db.ErrInvalid)
	}
	return tail[:passLen], tail[passLen:], nil
}
