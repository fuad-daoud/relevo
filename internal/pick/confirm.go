package pick

import (
	"fmt"
	"strings"
)

// confirmView is the screen between Enter and a destructive verb:
//
//	mark webshop done?  it is ACTIVE in round 5
//
//	y confirm  any other key cancels
//
// The state is styled the way the list styles it, so the word the human
// missed on the list is the same word, in the same colour, alone on a line.
func (m Model) confirmView() string {
	var sb strings.Builder
	var question string
	switch m.opts.Verb {
	case VerbUnbind:
		question = fmt.Sprintf("unbind %s?", m.confirm.Name)
	default:
		question = fmt.Sprintf("mark %s done?", m.confirm.Name)
	}
	sb.WriteString(titleStyle.Render(question))
	fmt.Fprintf(&sb, "  it is %s in round %d", strings.TrimRight(styleDisplay(m.confirm.Display), " "), m.confirm.Round)
	sb.WriteString("\n\n")
	sb.WriteString(hintStyle.Render("y confirm  any other key cancels"))
	return sb.String()
}
