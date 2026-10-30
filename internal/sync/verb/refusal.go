package verb

import (
	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/db/wire/client"
)

// Refusal is a failed verb as the owner answers it: the class a caller maps and
// the message it shows. Both come from the same error the sync package raised,
// so a refusal a caller can act on keeps the wording the package gave it.
func Refusal(code string, err error) *wire.SyncResult {
	return &wire.SyncResult{OK: false, Code: code, Message: err.Error()}
}

// RefusalError turns a result into the error a caller classifies, so the client
// side of the surface maps codes without importing the sync package's sentinels.
func RefusalError(res *wire.SyncResult) error { return client.VerbError(res) }
