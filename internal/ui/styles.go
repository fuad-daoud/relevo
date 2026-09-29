package ui

import "github.com/charmbracelet/lipgloss"

// Theme D2: tokens from spec §2.1.
// Nothing outside this file names a colour.
var (
	textStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#e6e8ec"))
	mutedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#9097a3"))
	faintStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#596070"))
	borderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#3a4150"))
	accentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#6ea8fe"))
	warnStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#f2b84b"))
	greenStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#5fd08f"))
	redStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#ff6b81"))
	dangerStyle = redStyle
	shadeStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#2a2f39"))
	// gridStyle is the chart's faint blueprint gridline, between shade and
	// border. It is only for the drawing surface (§3.2).
	gridStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#2f3542"))

	selBandStyle    = lipgloss.NewStyle().Background(lipgloss.Color("#1a1f28"))
	chipWarnStyle   = lipgloss.NewStyle().Background(lipgloss.Color("#3a2e14")).Foreground(lipgloss.Color("#f2b84b"))
	chipGreenStyle  = lipgloss.NewStyle().Background(lipgloss.Color("#15301f")).Foreground(lipgloss.Color("#5fd08f"))
	chipAccentStyle = lipgloss.NewStyle().Background(lipgloss.Color("#6ea8fe")).Foreground(lipgloss.Color("#0f1115")).Bold(true)
	chipDangerStyle = lipgloss.NewStyle().Background(lipgloss.Color("#3a1820")).Foreground(lipgloss.Color("#ff6b81")).Bold(true)
	kbdStyle        = lipgloss.NewStyle().Background(lipgloss.Color("#1d2129")).Foreground(lipgloss.Color("#e6e8ec"))
	// offKbdStyle and offStyle draw a footer key that does not apply to the
	// current row: the same cells, dimmed so the layout never moves.
	offKbdStyle = lipgloss.NewStyle().Background(lipgloss.Color("#15181e")).Foreground(lipgloss.Color("#3d4350"))
	offStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#3d4350"))

	// Re-pointed old names so other views survive without code changes (§4).
	fgStyle    = textStyle
	dimStyle   = mutedStyle
	ruleStyle  = borderStyle
	selectedBg = selBandStyle

	stateNeedsYouStyle = warnStyle.Bold(true)
	stateActiveStyle   = greenStyle
	stateDoneStyle     = faintStyle
	stateHeldStyle     = accentStyle
	errorStyle         = redStyle.Bold(true)
	emptyStyle         = mutedStyle.Italic(true)
	normalStyle        = lipgloss.NewStyle()

	activeTabStyle   = textStyle.Bold(true)
	inactiveTabStyle = mutedStyle

	diffAddStyle  = greenStyle
	diffDelStyle  = redStyle
	diffHunkStyle = accentStyle
	diffFileStyle = textStyle.Bold(true)

	archivedStyle = faintStyle.Italic(true)
)

// chip renders text with one cell of padding in style s (§4).
func chip(s lipgloss.Style, text string) string {
	return s.Render(" " + text + " ")
}

// stateStyle colours a display word; the four states have four colours and
// anything else renders plain, so a new state is visible before it is
// styled.
func stateStyle(display string) lipgloss.Style {
	switch display {
	case "NEEDS YOU":
		return stateNeedsYouStyle
	case "HELD":
		return stateHeldStyle
	case "ACTIVE":
		return stateActiveStyle
	case "PAUSED":
		return stateDoneStyle
	case "DONE":
		return stateDoneStyle
	}
	return normalStyle
}
