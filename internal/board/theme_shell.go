package board

import (
	"fmt"
	"strings"

	"github.com/fuad-daoud/relevo/internal/board/theme"
)

// ShellTheme is the palette as the shell reads it: the board document's
// theme field and the values the injected stylesheet sets. It is the only
// palette-to-shell mapping in the repo, so demo feedback edits one function.
type ShellTheme struct {
	Name    string `json:"name"`
	BG      string `json:"bg"`
	Ink     string `json:"ink"`
	Muted   string `json:"muted"`
	Line    string `json:"line"`
	Panel   string `json:"panel"`
	Accent  string `json:"accent"`
	Good    string `json:"good"`
	Warn    string `json:"warn"`
	Bad     string `json:"bad"`
	Comment string `json:"comment"`
}

// ShellThemeFor maps one palette onto the shell surfaces: accent drives the
// accent variable the bar and cards already read, panel the surface, bg the
// page behind the frame, ink the body text, muted the secondary text, line
// the edges, comment the pin and its variable, warn the orphan pin, mark and
// banner, bad the error text. Good is emitted with no consumer today: no
// shell surface reports success. Faint is unmapped: no shell surface is
// fainter than muted.
func ShellThemeFor(t theme.Theme) ShellTheme {
	return ShellTheme{
		Name:    t.Name,
		BG:      t.BG,
		Ink:     t.Ink,
		Muted:   t.Muted,
		Line:    t.Line,
		Panel:   t.Panel,
		Accent:  t.Accent,
		Good:    t.Good,
		Warn:    t.Warn,
		Bad:     t.Bad,
		Comment: t.Comment,
	}
}

// CSS renders the palette as one :root block served ahead of the shell page.
// The values are written verbatim, which is safe only because resolution
// refused any non-colour before the palette reached the server.
func (s ShellTheme) CSS() string {
	var b strings.Builder
	b.WriteString(":root{color-scheme:dark;")
	for _, kv := range [][2]string{
		{"--relevo-bg", s.BG},
		{"--relevo-ink", s.Ink},
		{"--relevo-muted", s.Muted},
		{"--relevo-edge", s.Line},
		{"--relevo-surface", s.Panel},
		{"--relevo-accent", s.Accent},
		{"--relevo-good", s.Good},
		{"--relevo-warn", s.Warn},
		{"--relevo-bad", s.Bad},
		{"--relevo-comment", s.Comment},
	} {
		fmt.Fprintf(&b, "%s:%s;", kv[0], kv[1])
	}
	b.WriteString("}")
	return b.String()
}
