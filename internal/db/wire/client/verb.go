package client

import (
	"github.com/fuad-daoud/relevo/internal/db/wire"
)

// VerbError is what a caller classifies a failed verb by. The message is the
// result's own, which is fixed text written in this repository; the token is in
// neither, because it was never on either side of a message.
//
// A nil or successful result is nil, so a caller can classify unconditionally
// on the line after the call rather than branching first.
func VerbError(res *wire.SyncResult) error {
	if res == nil || res.OK {
		return nil
	}
	return &VerbRefusal{Code: res.Code, Message: res.Message}
}

// VerbRefusal is a failed sync verb as a caller sees it: the code it maps and
// the fixed text it shows.
//
// The Message is deliberately a field rather than text baked into the type, and
// it carries whatever the result carried. What keeps that safe is upstream: the
// owner only ever writes a message from the closed set of SyncCode names, and
// never a credential.
type VerbRefusal struct {
	Code    string
	Message string
}

func (v *VerbRefusal) Error() string { return v.Message }
