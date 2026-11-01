// Package widget holds the shared view palette and the pure layout and
// error helpers every view uses, so no view duplicates a colour or a rule.
package widget

import "github.com/charmbracelet/lipgloss"

// Nothing outside this file names a colour.
var (
	TextStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#e6e8ec"))
	MutedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#9097a3"))
	FaintStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#596070"))
	BorderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#3a4150"))
	AccentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#6ea8fe"))
	WarnStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#f2b84b"))
	GreenStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#5fd08f"))
	RedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#ff6b81"))
	DangerStyle = RedStyle
	ShadeStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#2a2f39"))
	// GridStyle is the chart's faint blueprint gridline, between shade and
	// border. It is only for the drawing surface.
	GridStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#2f3542"))

	SelBandStyle    = lipgloss.NewStyle().Background(lipgloss.Color("#1a1f28"))
	ChipWarnStyle   = lipgloss.NewStyle().Background(lipgloss.Color("#3a2e14")).Foreground(lipgloss.Color("#f2b84b"))
	ChipGreenStyle  = lipgloss.NewStyle().Background(lipgloss.Color("#15301f")).Foreground(lipgloss.Color("#5fd08f"))
	ChipAccentStyle = lipgloss.NewStyle().Background(lipgloss.Color("#6ea8fe")).Foreground(lipgloss.Color("#0f1115")).Bold(true)
	ChipDangerStyle = lipgloss.NewStyle().Background(lipgloss.Color("#3a1820")).Foreground(lipgloss.Color("#ff6b81")).Bold(true)
	KbdStyle        = lipgloss.NewStyle().Background(lipgloss.Color("#1d2129")).Foreground(lipgloss.Color("#e6e8ec"))
	// OffKbdStyle and OffStyle draw a footer key that does not apply to the
	// current row: the same cells, dimmed so the layout never moves.
	OffKbdStyle = lipgloss.NewStyle().Background(lipgloss.Color("#15181e")).Foreground(lipgloss.Color("#3d4350"))
	OffStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#3d4350"))

	StateNeedsYouStyle = WarnStyle.Bold(true)
	StateActiveStyle   = GreenStyle
	StateDoneStyle     = FaintStyle
	StateHeldStyle     = AccentStyle
	ErrorStyle         = RedStyle.Bold(true)
	EmptyStyle         = MutedStyle.Italic(true)
	NormalStyle        = lipgloss.NewStyle()

	ActiveTabStyle   = TextStyle.Bold(true)
	InactiveTabStyle = MutedStyle

	DiffAddStyle  = GreenStyle
	DiffDelStyle  = RedStyle
	DiffHunkStyle = AccentStyle
	DiffFileStyle = TextStyle.Bold(true)

	ArchivedStyle = FaintStyle.Italic(true)
)

func Chip(s lipgloss.Style, text string) string {
	return s.Render(" " + text + " ")
}
