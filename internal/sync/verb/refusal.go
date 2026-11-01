package verb

import (
	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/db/wire/client"
)

// Refusal is a failed verb as the owner answers it: mapped class and shown
// message, both from the sync package's own error, whose wording is kept.
func Refusal(code string, err error) *wire.SyncResult {
	return &wire.SyncResult{OK: false, Code: code, Message: err.Error()}
}

// RefusalError turns a result into the error a caller classifies, without
// importing the sync package's sentinels.
func RefusalError(res *wire.SyncResult) error { return client.VerbError(res) }
