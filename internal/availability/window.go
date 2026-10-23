package availability

import (
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
)

// windowGates is one Scheduled gate per candidate whose window excludes it at
// d.Now(): a block window skips it, a demote window sets Demote. Nothing is
// written anywhere; the verdict is recomputed from config and the clock.
func windowGates(d Deps) []Gate {
	if d.Candidates == nil || d.Now == nil {
		return nil
	}
	now := d.Now()
	var out []Gate
	for _, ref := range d.Candidates.Refs() {
		r, err := candidate.ParseRef(ref)
		if err != nil {
			continue
		}
		c, err := d.Candidates.Lookup(r)
		if err != nil || c.Windows == nil || c.Windows.Covers(now) {
			continue
		}
		until, _ := c.Windows.NextEdge(now)
		out = append(out, Gate{
			Token:  ref,
			Kind:   Scheduled,
			Since:  now,
			Until:  until,
			Note:   windowNote(*c.Windows, now),
			Source: "relevo",
			Demote: c.Windows.Mode == candidate.WindowModeDemote,
		})
	}
	return out
}

// windowNote is the window's own config text: the only-windows when now is
// outside all of them, else the avoid windows now inside.
func windowNote(w candidate.Windows, now time.Time) string {
	if len(w.Only) > 0 {
		covered := false
		for _, o := range w.Only {
			covered = covered || o.Covers(now)
		}
		if !covered {
			return "outside window " + windowText(w.Only)
		}
	}
	var in []candidate.Window
	for _, a := range w.Avoid {
		if a.Covers(now) {
			in = append(in, a)
		}
	}
	return "inside window " + windowText(in)
}

func windowText(ws []candidate.Window) string {
	parts := make([]string, len(ws))
	for i, w := range ws {
		parts[i] = w.From + "-" + w.To
	}
	return strings.Join(parts, ", ")
}
