package ui

import (
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"

	"github.com/fuad-daoud/relevo/internal/sanitize"
)

type detailModel struct {
	name      string // row key (BindingStatus.Key()) under inspection
	round     int    // the round on screen; "[" / "]" step it (#183)
	rounds    int    // how many rounds round can step through: live row.Round (the open round included) or a hist row's Rounds (every round closed)
	active    tab
	vp        viewport.Model // the live viewport; only ever shows `active`
	scroll    [tabCount]int  // parked offsets for INACTIVE tabs
	cache     [tabCount]tabContent
	lastLogTS time.Time // invalidation key -- see §5 rule 3

	// headless is true when the builder is headless (row.Headless != nil).
	// It selects the terminal tab's source (the round log rather than a
	// captured pane) and, from #180, its styling.
	headless bool

	// follow is the terminal tab's tail rule (spec §3.4): while true the
	// viewport is pinned to the bottom on every refresh; scrolling up clears
	// it, scrolling back to the bottom sets it. True on every re-point; a
	// hist row is never live, so it is always false there (#183).
	follow bool

	// live is false for a hist row's detail: every tab reads the database
	// through fetchShow instead of live files (#172, §5.8).
	live bool
	// bindingID is the database row's id, set only when !live -- fetchShow
	// does not need it (it re-resolves by name), but relevo.Show's ShowResult
	// does not carry it either, so it is here for the header and any future
	// db-keyed lookup Task 5's goldens exercise.
	bindingID string
	// archivedAt is set only when !live and the binding was archived
	// (tarred by `gc`); zero for a hist row the database recorded but the
	// live store never released as an archive (e.g. `relevo done`).
	archivedAt time.Time
}

func styleFor(c tabContent) lipgloss.Style {
	if c.err != nil {
		return errorStyle
	}
	if c.empty != "" {
		return emptyStyle
	}
	return normalStyle
}

// bodyOf renders a tab's content for the viewport. The diff tab colours its
// body (colourDiff); a rendered round log's terminal tab colours transcript
// markers (colourTranscript, #180) -- a headless builder's always is one, and
// so is a pane builder's once its session record is located (#184); the
// artifacts tab renders its table and selected file (round 5b); everything
// else returns c.body as before.
func bodyOf(t tab, c tabContent, headless bool) string {
	if t == tabArtifacts {
		return artifactsBody(c)
	}
	if !c.loaded {
		return "loading…"
	}
	c.body = sanitizeText(c.body)
	c.empty = sanitizeText(c.empty)
	st := styleFor(c)
	if c.err != nil {
		return st.Render("error: " + sanitizeText(c.err.Error()))
	}
	if c.empty != "" {
		return st.Render(c.empty) // prose, NOT styled as an error
	}
	if t == tabPrompt || t == tabReport {
		return renderMarkdown(c.body)
	}
	if t == tabDiff {
		return colourDiff(c.body)
	}
	if t == tabTerminal && (headless || c.transcript) {
		return colourTranscript(c.body)
	}
	return st.Render(c.body)
}

// wrapBody word-wraps body to width so the viewport's logical lines are its
// visual lines. The viewport pads with lipgloss.Width itself, which wraps
// anything wider and then cuts the overflow with MaxHeight -- rows under a
// long line fall off the bottom where no scroll reaches them. Wrapping
// first, to the same width, is the whole fix. width <= 0 returns body.
func wrapBody(body string, width int) string {
	if width <= 0 || body == "" {
		return body
	}
	return lipgloss.NewStyle().Width(width).Render(body)
}

// sanitizeText makes untrusted text safe to draw. It is called on raw text
// before any styling, so the ANSI sequences relevo itself adds afterwards are
// untouched. The rule is shared with every other untrusted-text path through
// package sanitize.
func sanitizeText(s string) string {
	return sanitize.Text(s)
}
