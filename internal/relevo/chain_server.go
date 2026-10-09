package relevo

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// chainOnServer reports whether a chain runs on a server: its row names the
// server that drives it. A chain with no server is local and every mechanism
// on this machine owns it.
func chainOnServer(c db.ChainRow) bool {
	return c.Server != ""
}

// chainGoneReason is the one wording that says a chain is gone from its server:
// the pull writes it when a 404 halts the mirror, and the walk reads it back to
// stop polling a mirror the server has already released.
func chainGoneReason(name, server string) string {
	return fmt.Sprintf("chain %s is gone from %s", name, server)
}

// chainGoneMirror reports whether a mirror is halted because its row carries
// chainGoneReason. The walk skips such a row the same way it skips a done one,
// so a server that released a mirror which was already halted is not read every
// tick. A mirror the chain itself halted carries its own halt reason, not the
// gone wording, and is still polled.
func chainGoneMirror(c db.ChainRow) bool {
	return c.Status == string(chain.StatusHalted) && c.Reason == chainGoneReason(c.Name, c.Server)
}

// serverChainMember reports whether name is a member binding of a chain that
// runs on a server. The per-binding machinery observes such a member's live
// round, but never collects it: the chain pull installs its closed rounds and
// acks them, and the stop and done paths leave its release to the chain's own
// verbs.
func serverChainMember(tx *store.Tx, name string) bool {
	c, err := tx.ChainByMember(name)
	if err != nil {
		return false
	}
	return chainOnServer(c)
}

// serverChainMemberStore is serverChainMember's read-only twin for the
// lock-free callers, which hold no tx.
func serverChainMemberStore(s *store.Store, name string) bool {
	c, err := s.ChainByMember(name)
	if err != nil {
		return false
	}
	return chainOnServer(c)
}

// serverChainMemberActive reports whether name is a member of a chain that
// runs on a server and is still running. Only then does the chain pull own
// the member's closed rounds: a member of a done chain collects like any
// other served binding, so a manual round sent after `chain done` still
// closes on the client (#1056).
func serverChainMemberActive(s *store.Store, name string) bool {
	c, err := s.ChainByMember(name)
	if err != nil {
		return false
	}
	return chainOnServer(c) && c.Status == string(chain.StatusRunning)
}

// missingChainFeatures is the features a server must advertise to run a whole
// chain for a client, in the order the refusal names them: it drives the chain
// itself, and it runs reader members. Both are absent from a pre-chain server.
func missingChainFeatures(who remote.WhoAmI) []string {
	var missing []string
	if !hasFeature(who, remote.FeatureChain) {
		missing = append(missing, remote.FeatureChain)
	}
	if !hasFeature(who, remote.FeatureReaders) {
		missing = append(missing, remote.FeatureReaders)
	}
	return missing
}

// hasFeature reports whether the server advertises token.
func hasFeature(who remote.WhoAmI, token string) bool {
	for _, f := range who.Features {
		if f == token {
			return true
		}
	}
	return false
}

// chainFeatureList renders the missing features for the refusal's parenthetical:
// one is a singular "feature", two or more a plural list.
func chainFeatureList(missing []string) string {
	quoted := make([]string, len(missing))
	for i, f := range missing {
		quoted[i] = strconv.Quote(f)
	}
	if len(quoted) == 1 {
		return quoted[0] + " feature"
	}
	return strings.Join(quoted[:len(quoted)-1], ", ") + " and " + quoted[len(quoted)-1] + " features"
}
