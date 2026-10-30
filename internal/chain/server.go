package chain

import (
	"fmt"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

// OnServer reports whether a chain runs on a server: its row names the
// server that drives it. A chain with no server is local and every mechanism
// on this machine owns it.
func OnServer(c db.ChainRow) bool {
	return c.Server != ""
}

// GoneReason is the one wording that says a chain is gone from its server:
// the pull writes it when a 404 halts the mirror, and the walk reads it back to
// stop polling a mirror the server has already released.
func GoneReason(name, server string) string {
	return fmt.Sprintf("chain %s is gone from %s", name, server)
}

// GoneMirror reports whether a mirror is halted because its row carries
// GoneReason. The walk skips such a row the same way it skips a done one,
// so a server that released a mirror which was already halted is not read every
// tick. A mirror the chain itself halted carries its own halt reason, not the
// gone wording, and is still polled.
func GoneMirror(c db.ChainRow) bool {
	return c.Status == string(StatusHalted) && c.Reason == GoneReason(c.Name, c.Server)
}

// ServerMember reports whether name is a member binding of a chain that
// runs on a server. The per-binding machinery observes such a member's live
// round, but never collects it: the chain pull installs its closed rounds and
// acks them, and the stop and done paths leave its release to the chain's own
// verbs.
func ServerMember(tx *store.Tx, name string) bool {
	c, err := tx.ChainByMember(name)
	if err != nil {
		return false
	}
	return OnServer(c)
}

// ServerMemberStore is ServerMember's read-only twin for the lock-free
// callers, which hold no tx.
func ServerMemberStore(s *store.Store, name string) bool {
	c, err := s.ChainByMember(name)
	if err != nil {
		return false
	}
	return OnServer(c)
}

// ServerMemberActive reports whether name is a member of a chain that
// runs on a server and is still running. Only then does the chain pull own
// the member's closed rounds: a member of a done chain collects like any
// other served binding, so a manual round sent after `chain done` still
// closes on the client.
func ServerMemberActive(s *store.Store, name string) bool {
	c, err := s.ChainByMember(name)
	if err != nil {
		return false
	}
	return OnServer(c) && c.Status == string(StatusRunning)
}
