package relevo

import (
	"strings"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// questionFirstLine answers "" on any error (missing or unreadable file):
// never an error, since the classifier has a fallback.
func questionFirstLine(rt Runtime) func(name string, round int) string {
	return func(name string, round int) string {
		data, err := rt.Store.ReadFile(rt.Store.QuestionPath(name, round))
		if err != nil {
			return ""
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				return line
			}
		}
		return ""
	}
}

func WaitingOnYou(rt Runtime, except string) ([]string, error) {
	bindings, err := rt.Store.List()
	if err != nil {
		return nil, err
	}

	qf := questionFirstLine(rt)
	var lines []string
	for _, b := range bindings {
		if b.Name == except || b.State == store.StateDone {
			continue
		}
		entries, err := rt.Store.ReadLog(b.Name)
		if err != nil {
			return nil, err
		}
		w, ok := view.WaitingOn(b, entries, qf)
		if !ok {
			continue
		}
		// A member whose owning chain is finished is clearable by hand, so its
		// hint names the clear a person can run. Every other row's own word --
		// the running-chain member's, the broken binding's rebind -- stands.
		if c, memberOf := chain.MemberChain(rt.Store.ChainByMember, b.Name); memberOf {
			switch chain.Status(c.Status) {
			case chain.StatusDone, chain.StatusStopped:
				w.Hint = chain.MemberHint(b.Name, c, memberOf)
			}
		}
		lines = append(lines, view.WaitingLine(w, rt.Now().UTC()))
	}
	return lines, nil
}
