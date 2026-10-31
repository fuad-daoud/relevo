package relevo

import (
	"errors"
)

// ErrServerPreTier is a client-side refusal that happens before any server
// state changes: the server does not advertise the "tier" feature, so a
// requested --tier has nowhere to land.
var ErrServerPreTier = errors.New("server does not carry a permission tier")
