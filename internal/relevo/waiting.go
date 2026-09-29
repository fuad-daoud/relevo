package relevo

import (
	"strings"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// questionFirstLine reads the first non-blank line of the round's captured
// question file, trimmed. "" on any error (missing or unreadable file):
// never an error, since the classifier has a fallback. The one
// place WaitingOn's callers touch the filesystem beyond the store.
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

// WaitingOnYou lists, in Store.List order, one line per binding (other than
// except) that is waiting on a human. Store-backed.
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
		lines = append(lines, view.WaitingLine(w, rt.Now().UTC()))
	}
	return lines, nil
}
