package board

import (
	"fmt"

	"github.com/fuad-daoud/relevo/internal/board/theme"
)

// ThemeTier is the precedence rule as a pure decision: the flag wins,
// then the repository's own board_theme, then the repo-local git config key
// `relevo.boardTheme` (kept as a fallback so existing setups keep working),
// then the default name.
func ThemeTier(flagTheme, repoTheme, gitTheme string) string {
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

// ResolveTheme resolves the theme a run paints with. It reads the stored
// sets once -- the repository's setting and, for the lookup itself, the
// section or its seed fallback -- and refuses an unknown name whatever the
// tier it came from named.
func ResolveTheme(deps Deps, cwd, flagTheme string) (*Theme, error) {
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
	name := ThemeTier(flagTheme, repoTheme, git)
	th, err := theme.Lookup(name, stored)
	if err != nil {
		return nil, err
	}
	return &th, nil
}
