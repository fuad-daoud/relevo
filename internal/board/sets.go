package board

import (
	"fmt"

	"github.com/fuad-daoud/relevo/internal/board/theme"
)

// ResolveThemeName is the precedence rule as a pure decision: the flag wins,
// then the repository's own board_theme, then the repo-local git config key.
// A board nobody themed paints with the default palette, so the empty tier
// resolves to the default name.
func ResolveThemeName(flagTheme, repoTheme, gitTheme string) string {
	switch {
	case flagTheme != "":
		return flagTheme
	case repoTheme != "":
		return repoTheme
	case gitTheme != "":
		return gitTheme
	}
	return theme.DefaultName
}

// ResolveTheme resolves the palette a run paints with. It reads the stored
// sets once and refuses an unknown name whatever tier named it. The empty
// tier resolves to the default palette, so every run paints with one.
func ResolveTheme(deps Deps, cwd, flagTheme string) (*theme.Theme, error) {
	repoTheme, stored, err := deps.ThemeSets(cwd)
	if err != nil {
		return nil, fmt.Errorf("read the board settings: %w", err)
	}
	git := ""
	if flagTheme == "" && repoTheme == "" {
		git, err = deps.GitConfig(cwd, "relevo.boardTheme")
		if err != nil {
			return nil, fmt.Errorf("read relevo.boardTheme: %w", err)
		}
	}
	name := ResolveThemeName(flagTheme, repoTheme, git)
	th, err := theme.Lookup(name, stored)
	if err != nil {
		return nil, err
	}
	// Stored sets arrive validated, but a hand-built set reaches this lookup
	// too; every value is written into a stylesheet verbatim, so a
	// non-colour here is refused rather than injected.
	if field, value, ok := invalidThemeField(th); ok {
		return nil, &themeUsageError{msg: fmt.Sprintf("theme %q: %s is not a colour: %q", name, field, value)}
	}
	return &th, nil
}

// themeUsageError is a palette refusal with a clean message; it reports
// itself as theme.ErrUsage so the CLI maps it to a usage exit.
type themeUsageError struct{ msg string }

func (e *themeUsageError) Error() string { return e.msg }
func (e *themeUsageError) Unwrap() error { return theme.ErrUsage }

// invalidThemeField names the first palette field holding no colour, so a
// corrupt set cannot reach the stylesheet.
func invalidThemeField(t theme.Theme) (string, string, bool) {
	fields := [][2]string{
		{"bg", t.BG},
		{"ink", t.Ink},
		{"muted", t.Muted},
		{"faint", t.Faint},
		{"line", t.Line},
		{"panel", t.Panel},
		{"accent", t.Accent},
		{"good", t.Good},
		{"warn", t.Warn},
		{"bad", t.Bad},
		{"comment", t.Comment},
	}
	for _, f := range fields {
		if !theme.ValidColour(f[1]) {
			return f[0], f[1], true
		}
	}
	return "", "", false
}
