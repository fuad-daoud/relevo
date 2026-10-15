package main

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/fuad-daoud/relevo/internal/board"
	"github.com/fuad-daoud/relevo/internal/board/theme"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// boardThemeSets is where the theme resolution reads this machine's stored
// sets: the repository's settings (its board_theme) through the repository
// identity, and the palettes of the `themes` section. It is a seam so a test
// can pin tiers without a machine state; by default it reads the machine's
// own database read-only when one exists and never writes: a board verb
// keeps creating nothing. A machine with no database, and one whose database
// is held (a daemon has the file), reads the seed file instead, which is the
// resolution's one fallback.
var boardThemeSets = func(cwd string) (relevo.RepoSettings, map[string]board.Theme, error) {
	repo := relevo.RepoSettings{}
	root, err := store.DefaultRoot()
	if err != nil {
		return repo, nil, err
	}
	path := filepath.Join(root, "relevo.db")
	if !fileExists(path) {
		return repo, theme.Seed(), nil
	}
	d, err := openReadOnlyDB(path, db.Options{})
	if err != nil {
		// The daemon holds the file, or the store cannot be read: resolve
		// against the seed file rather than failing a board open over it.
		return repo, theme.Seed(), nil
	}
	defer func() { _ = d.Close() }()

	rt := relevo.Runtime{
		Git:    git.NewClient("git", 10*time.Second, git.DefaultMaxPatchBytes),
		Config: config.Open(d),
	}
	repo, err = relevo.RepoSettingsFor(context.Background(), rt, cwd)
	if err != nil {
		return repo, nil, err
	}
	stored, err := boardThemeStored(rt)
	if err != nil {
		return repo, nil, err
	}
	return repo, stored, nil
}

// boardThemeStored is the palette set a lookup resolves against: the seeded
// `themes` section when the database carries one, else the embedded seed
// file, which is the only fallback a board verb reads. A runtime with no
// database -- or one that has never been seeded -- resolves against the seed
// file the same way.
func boardThemeStored(rt relevo.Runtime) (map[string]board.Theme, error) {
	if rt.Config != nil {
		body, present, err := rt.Config.Body(config.Themes)
		if err != nil {
			return nil, err
		}
		if present && len(bytes.TrimSpace(body)) > 0 {
			return config.ParseThemes(body)
		}
	}
	return theme.Seed(), nil
}

// boardThemeTier is the precedence rule as a pure decision: the flag wins,
// then the repository's own board_theme, then the repo-local git config key
// `relevo.boardTheme` (kept as a fallback so existing setups keep working),
// then the default name.
func boardThemeTier(flagTheme, repoTheme, gitTheme string) string {
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

// boardResolveTheme resolves the theme a run paints with. It reads the stored
// sets once -- the repository's setting and, for the lookup itself, the
// section or its seed fallback -- and refuses an unknown name whatever the
// tier it came from named.
func boardResolveTheme(cwd, flagTheme string) (*board.Theme, error) {
	repo, stored, err := boardThemeSets(cwd)
	if err != nil {
		return nil, fmt.Errorf("read the board settings: %w", err)
	}
	git := ""
	if flagTheme == "" && repo.BoardTheme == "" {
		git, err = boardGitConfig(cwd, "relevo.boardTheme")
		if err != nil {
			return nil, fmt.Errorf("read relevo.boardTheme: %w", err)
		}
	}
	name := boardThemeTier(flagTheme, repo.BoardTheme, git)
	th, err := theme.Lookup(name, stored)
	if err != nil {
		return nil, err
	}
	return &th, nil
}
