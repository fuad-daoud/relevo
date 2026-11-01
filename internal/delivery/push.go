package delivery

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relevo/internal/store"
)

// MaxPushBytes caps the file text a push carries; the full text stays one
// `relevo show` away, carried as metadata on every push path.
const MaxPushBytes = 64 << 10

// showCommand renders the mastermind-facing command printing one round's
// artifact: the durable name once a closed round's files are sealed away.
func showCommand(name string, round int, section string) string {
	return fmt.Sprintf("relevo show %s --round %d --%s", name, round, section)
}

// FindingsCommand is showCommand's findings form: it also names the consult.
func FindingsCommand(name string, round int, id string) string {
	return fmt.Sprintf("relevo show %s --round %d --findings %s", name, round, id)
}

// findingsIDOf recovers the consult id from a findings basename, or "" when it
// is not a findings name.
func findingsIDOf(path string) string {
	base := filepath.Base(path)
	const suffix = "-findings.md"
	if !strings.HasSuffix(base, suffix) {
		return ""
	}
	base = strings.TrimSuffix(base, suffix)
	i := strings.IndexByte(base, '-')
	if i < 0 {
		return ""
	}
	return base[i+1:]
}

// LogRef renders the `relevo show` command printing e's artifact, or "" when
// the kind names none.
func LogRef(b store.Binding, e store.LogEntry) string {
	switch e.Kind {
	case store.KindReport:
		if b.Shape == store.ShapeReader {
			return showCommand(b.Name, e.Round, "output")
		}
		return showCommand(b.Name, e.Round, "report")
	case store.KindFindings:
		if id := findingsIDOf(e.Path); id != "" {
			return FindingsCommand(b.Name, e.Round, id)
		}
	case store.KindDiff:
		return showCommand(b.Name, e.Round, "diff")
	case store.KindDrift:
		return showCommand(b.Name, e.Round, "drift")
	}
	return ""
}

// BindingFor loads name from st, or a name-only binding when the store cannot:
// a read failure must not change what a push carries.
func BindingFor(st *store.Store, name string) store.Binding {
	if b, err := st.Load(name); err == nil {
		return b
	}
	return store.Binding{Name: name}
}

// PushText returns the text a push path carries: the stored Payload, plus the
// file's contents for kinds whose Path names a text artifact. A read error is
// not a delivery failure: it returns e.Payload and false. The origin line
// stays first in every case; OpencodeDeliverer's confirm query depends on it.
func PushText(e store.LogEntry, b store.Binding, read func(string) ([]byte, error)) (string, bool) {
	if e.Path == "" || !expandablePushKind(e.Kind) {
		return e.Payload, false
	}

	raw, err := read(e.Path)
	if err != nil {
		return e.Payload, false
	}

	ref := LogRef(b, e)
	if ref == "" {
		// No show section: fall back to the path, not an empty reference.
		ref = e.Path
	}

	return e.Payload + "\n\n" + truncatePushText(string(raw), ref), true
}

// expandablePushKind reports whether PushText expands e.Kind.
func expandablePushKind(k store.Kind) bool {
	switch k {
	case store.KindReport, store.KindFindings, store.KindEdge:
		return true
	default:
		return false
	}
}

// truncatePushText keeps at most MaxPushBytes, cut back to the last newline,
// naming ref when it truncated.
func truncatePushText(text, ref string) string {
	return TruncateTo(text, MaxPushBytes, fmt.Sprintf("%d KiB", MaxPushBytes/1024), ref)
}

// TruncateTo is truncatePushText at a caller's budget, so one marker shape
// serves every budget. ref must be a --peek pointer for already-confirmed
// entries: a claiming pointer would consume the round's next pending payload.
func TruncateTo(text string, budget int, label, ref string) string {
	if len(text) <= budget {
		return text
	}

	head := text[:budget]
	kept := head
	if cut := strings.LastIndexByte(head, '\n'); cut >= 0 {
		kept = text[:cut+1]
	}
	if !strings.HasSuffix(kept, "\n") {
		kept += "\n"
	}

	return kept + fmt.Sprintf("[truncated at %s -- full text: %s]", label, ref)
}

// isStateEvent reports whether s is a state a push announces.
func isStateEvent(s store.State) bool {
	switch s {
	case store.StateNeedsYou, store.StateBroken:
		return true
	default:
		return false
	}
}

// stateEventContent is the body of a state line: the state, the Halt reason
// when present, and the next steps.
func stateEventContent(b store.Binding) string {
	var reason string
	if b.Halt != "" {
		reason = " -- " + b.Halt
	}
	return fmt.Sprintf("%s round %d: %s%s\nrun relevo status --name %s, then send or stop.",
		b.Name, b.Round, stateLabel(b.State), reason, b.Name)
}

func stateLabel(s store.State) string {
	switch s {
	case store.StateNeedsYou:
		return "NEEDS YOU"
	case store.StateBroken:
		return "BROKEN"
	default:
		return strings.ToUpper(string(s))
	}
}
