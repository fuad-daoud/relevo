package ui

import (
	"fmt"
)

// attemptLines is the daemon's last steady attempt: when it ended, what it
// moved and, when it failed, why. A failure is drawn as an error so a machine
// that keeps failing cannot read as healthy beside older good times.
func (v syncView) attemptLines() []string {
	a := v.snap.State.Attempt
	if a.Start.IsZero() {
		return nil
	}
	moved := fmt.Sprintf("exported %d · applied %d", a.Exported, a.Applied)
	if !a.Failed() {
		return []string{syncLine("last attempt", "ok · "+v.localStampPhrase(a.End)+" · "+moved, mutedStyle)}
	}
	out := []string{syncLine("last attempt", "failed · "+v.localStampPhrase(a.End)+" · "+moved, errorStyle)}
	out = append(out, "   "+errorStyle.Render(sanitizeText(a.Error)))
	if a.Attention {
		out = append(out, "   "+errorStyle.Render("needs attention"))
	}
	return out
}
