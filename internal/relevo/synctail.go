package relevo

import (
	"fmt"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// enableIntake splits an enable's raw tail into the token and the bucket secret
// and pairs the secret with the frame's bucket flags. A length the tail cannot
// hold is refused rather than clamped, since a clamped split would store part of
// the token as the secret.
func enableIntake(verb *wire.SyncVerb, tail []byte) ([]byte, relevosync.R2Intake, error) {
	n := verb.R2SecretLen
	if n < 0 || n > len(tail) {
		return nil, relevosync.R2Intake{}, fmt.Errorf("sync: enable: the frame names a %d-byte bucket secret in a %d-byte tail: %w",
			n, len(tail), db.ErrInvalid)
	}
	cut := len(tail) - n
	return tail[:cut], relevosync.R2Intake{
		Endpoint: verb.R2Endpoint,
		Bucket:   verb.R2Bucket,
		KeyID:    verb.R2KeyID,
		Secret:   tail[cut:],
	}, nil
}
