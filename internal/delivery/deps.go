// Package delivery gets a mastermind its queued payloads: it appends them, picks
// the route that carries each one, and drains a mastermind's mailbox over a live
// channel.
package delivery

import (
	"time"

	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/store"
)

// Deps is the slice of the caller's runtime that delivery reads. A caller
// builds it once so delivery stays unaware of the rest of the runtime.
type Deps struct {
	Store       *store.Store
	Now         func() time.Time
	Channels    ClaimStore
	Deliverers  map[string]MasterMindDeliverer
	MasterMinds mastermind.Registry
}
