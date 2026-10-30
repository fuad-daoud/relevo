package verb

import (
	"fmt"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/sync"
)

// The frame's raw tail carries every secret an enable needs and nothing else:
// the header is what a reader of a captured stream, a log or an error sees, so a
// value that is never in it is a rule that cannot be forgotten at a call site.
// Three secrets ride it and each has a length on the header; the split happens
// here, once, from the end.

// EnableIntake splits an enable's raw tail into the token, the passphrase and
// the bucket secret, and pairs the secret with the frame's bucket flags.
//
// It splits from the end, in reverse order of arrival: the bucket secret is last
// because it is the last thing the client had to resolve, the passphrase before
// it, and the token is whatever remains. A length the tail cannot hold is
// refused rather than clamped, since a clamped split would store part of one
// secret as another -- which is the one outcome that would silently write a
// credential into the row of a different one.
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

// RekeyIntake splits a rekey's raw tail into the current and the new
// passphrase. Both are named by their own length on the header and both ride
// the tail, so a split that does not account for every byte would store one
// passphrase as the other -- which is a wrong-passphrase refusal at best, and a
// fleet keyed to a stranger's bytes at worst. A tail the two lengths do not
// exactly cover is refused rather than trimmed.
func RekeyIntake(verb *wire.SyncVerb, tail []byte) (current, newPass []byte, err error) {
	passLen, newLen := verb.PassphraseLen, verb.NewPassphraseLen
	if passLen < 0 || newLen < 0 || passLen+newLen != len(tail) {
		return nil, nil, fmt.Errorf("sync: rekey: the frame names a %d-byte passphrase and a %d-byte new passphrase in a %d-byte tail: %w",
			passLen, newLen, len(tail), db.ErrInvalid)
	}
	return tail[:passLen], tail[passLen:], nil
}
