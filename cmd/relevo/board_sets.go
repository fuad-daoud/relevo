package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/fuad-daoud/relevo/internal/board"
	"github.com/fuad-daoud/relevo/internal/board/theme"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/tmpdir"
)

// boardThemeSets is where the theme resolution reads this machine's stored
// sets: the repository's settings (its board_theme) through the repository
// identity, and the palettes of the `themes` section. It is a seam so a test
// can pin tiers without a machine state; by default it reads the machine's
// own database when one exists and never writes: a board verb keeps creating
// nothing. A held database (a daemon has the file) is read through the owner
// an ad-hoc dial reaches; the owner is never started, and when it does not
// answer the read refuses with the registry's conflict rather than resolving
// against the seed. The seed file is read only where the resolution keeps a
// fallback: no database exists yet, or the section was never seeded.
var boardThemeSets = func(cwd string) (relevo.RepoSettings, map[string]board.Theme, error) {
	repo := relevo.RepoSettings{}
	root, err := tmpdir.DefaultRoot()
	if err != nil {
		return repo, nil, err
	}
	path := filepath.Join(root, "relevo.db")
	if !fileExists(path) {
		return repo, theme.Seed(), nil
	}
	d, err := openReadOnlyDB(path, db.Options{})
	if errors.Is(err, db.ErrNotConverted) {
		return repo, nil, failWrap(codeRefused, err, "open %s", path)
	}
	if errors.Is(err, db.ErrLocked) {
		// The daemon holds the file, so the section and the repository's
		// settings live behind the owner: read through an ad-hoc dial, which
		// never starts the owner. A dial that gets no answer refuses with the
		// registry's conflict rather than substituting the seed, which would
		// paint a section that exists wrong.
		d, err = dialOwnerAdHoc(context.Background(), root, verbDialBudget)
		if err != nil {
			return repo, nil, boardHeldRefusal(root, err)
		}
	} else if err != nil {
		return repo, nil, failWrap(codeInternal, err, "open %s", path)
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

// boardHeldRefusal is the refusal a stored-set read raises when the machine
// file is held and no owner answers, carrying the registry's own refusal out:
// the held file and the unanswered socket, classified conflict, with the lock
// error as its cause so a caller probing db.ErrLocked still finds it. The
// stored sets are never resolved against the seed here, so a held database
// with a section the owner serves paints that section or refuses, never the
// seed.
func boardHeldRefusal(root string, lockErr error) error {
	path := filepath.Join(root, "relevo.db")
	sock, sockErr := ownerSocket(root)
	if sockErr != nil {
		return failWrap(codeConflict, lockErr, "relevo.db is locked (%s) and no owner socket is available: %v", path+".lock", sockErr)
	}
	return failWrap(codeConflict, lockErr, "relevo.db is locked (%s) and the owner socket at %s did not answer", path+".lock", sock)
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
