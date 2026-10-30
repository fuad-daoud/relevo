package board

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/mastermind"
)

// HeldError is the refusal a registry read raises when the machine file is
// held and no owner answers, carrying the registry's own refusal out: the
// held file and the unanswered socket, with the lock error as its cause so a
// caller probing db.ErrLocked still finds it. The stored sets are never
// resolved against the seed here.
type HeldError struct {
	Msg   string
	Cause error
}

func (e *HeldError) Error() string { return e.Msg }
func (e *HeldError) Unwrap() error { return e.Cause }

// ErrNotAvailable marks a refusal the command line reports as not_available
// (exit 1): no live server for the named scene.
var ErrNotAvailable = errors.New("not available")

// notAvailableError carries ErrNotAvailable while printing the refusal text
// alone. An empty scene is the default board, the one `board url` assumes.
type notAvailableError struct {
	scene string
	id    string
}

func (e *notAvailableError) Error() string {
	scene := e.scene
	if scene == "" {
		scene = DefaultBoard
	}
	return "relevo board url: no live server for scene " + scene + " of MasterMind " + e.id
}

func (e *notAvailableError) Unwrap() error { return ErrNotAvailable }

// Deps carries the effects the board verbs read: the listener and browser
// opener no test runs, the repository and theme resolvers, the clock, and the
// registry dialers the front end owns. Every field is a value or a plain
// function; the verbs never name the front end.
type Deps struct {
	// Listen binds the loopback listener a board serves on.
	Listen func() (net.Listener, error)
	// Open opens the printed URL in a browser.
	Open func(url string) error
	// ProcStart reads a process's start time in Unix seconds.
	ProcStart func(pid int) (int64, error)
	// RepoRoot resolves cwd's git top level.
	RepoRoot func(cwd string) (string, error)
	// GitConfig reads one repo-local git config key; an unset key is ("", nil).
	GitConfig func(cwd, key string) (string, error)
	// ThemeSets reads the repository's board theme name and the palette set
	// the lookup resolves against.
	ThemeSets func(cwd string) (repoTheme string, stored map[string]Theme, err error)
	// Now stamps comment verbs.
	Now func() time.Time
	// LiveRoot is <state root>/boards, the live scope's root.
	LiveRoot func() (string, error)
	// DialOwner dials an already-running owner without starting one.
	DialOwner func(ctx context.Context, root string, budget time.Duration) (*db.DB, error)
	// OpenDB opens the machine file directly read-only.
	OpenDB func(path string, o db.Options) (*db.DB, error)
	// DialBudget caps an ad-hoc owner dial.
	DialBudget time.Duration
	// OwnerSocket reports the owner socket path for a state root.
	OwnerSocket func(root string) (string, error)
	// MastermindLookup resolves one MasterMind ref against the registry.
	MastermindLookup func(reg mastermind.Registry, ref string) (mastermind.Record, error)
}
