package relevo

import (
	"fmt"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// The frame's raw tail carries every secret an enable needs and nothing else:
// the header is what a reader of a captured stream, a log or an error sees, so a
// value that is never in it is a rule that cannot be forgotten at a call site.
// Three secrets ride it and each has a length on the header; the split happens
// here, once, from the end.

// enableIntake splits an enable's raw tail into the token, the passphrase and
// the bucket secret, and pairs the secret with the frame's bucket flags.
//
// It splits from the end, in reverse order of arrival: the bucket secret is last
// because it is the last thing the client had to resolve, the passphrase before
// it, and the token is whatever remains. A length the tail cannot hold is
// refused rather than clamped, since a clamped split would store part of one
// secret as another -- which is the one outcome that would silently write a
// credential into the row of a different one.
func enableIntake(verb *wire.SyncVerb, tail []byte) (token, passphrase []byte, r2 relevosync.R2Intake, err error) {
	secretLen, passLen := verb.R2SecretLen, verb.PassphraseLen
	if secretLen < 0 || passLen < 0 || secretLen+passLen > len(tail) {
		return nil, nil, relevosync.R2Intake{}, fmt.Errorf("sync: enable: the frame names a %d-byte passphrase and a %d-byte bucket secret in a %d-byte tail: %w",
			passLen, secretLen, len(tail), db.ErrInvalid)
	}
	cut := len(tail) - secretLen
	return tail[:cut-passLen], tail[cut-passLen : cut], relevosync.R2Intake{
		Endpoint: verb.R2Endpoint,
		Bucket:   verb.R2Bucket,
		KeyID:    verb.R2KeyID,
		Secret:   tail[cut:],
	}, nil
}

// Joining reports whether an enable's join holds a transport: from when the join
// opens its log until the enable settles.
func (v *VerbRunner) Joining() bool {
	return v != nil && v.joinTransport() != nil
}
