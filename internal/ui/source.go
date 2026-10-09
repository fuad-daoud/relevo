package ui

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/serve"
	"github.com/fuad-daoud/relevo/internal/view"
)

// Source is everything the ui reads state through: one refresh of the
// whole fleet, and a key resolver for the tabs.
type Source interface {
	// Status is one refresh: the whole fleet this UI shows.
	Status(ctx context.Context) (view.Report, error)
	// Runtime resolves a row key to the runtime that owns it and the bare
	// binding name inside that runtime's store. ok is false when the key
	// cannot be resolved (server: malformed key or unknown owner); a
	// mastermind source resolves every key.
	Runtime(key string) (rt relevo.Runtime, name string, ok bool)
	// Base is the runtime for fleet-wide reads that are not per row: the
	// database behind scope all and a hist row's tabs. On a mastermind it is
	// the mastermind's own runtime. On the server it carries no DB, so scope
	// all is refused there with the existing "no database" notice.
	Base() relevo.Runtime
	// MarkViewed stamps key's viewed mark (#143), the moment a human points
	// the detail pane at it. A mastermind source writes through its own
	// store; a server source is a no-op -- the mark is a record field on
	// whichever client machine holds the binding, and a server never writes
	// into a client's database. Errors are swallowed: a stamp must never fail
	// a read-only screen.
	MarkViewed(key string)
}

// mastermindSource is the single-runtime source `relevo ui` always had: no
// branch, every key resolves to rt under its own name.
type mastermindSource struct {
	rt relevo.Runtime
}

// Status is the fleet refresh with the detail figures left off: the fleet
// paints no pixel from Live, LiveUsage or Headless.Tail, and each costs a git
// diff, a usage peek or a log read per row on every tick. The detail pane
// reads all three from its own single-row fetch.
func (s mastermindSource) Status(ctx context.Context) (view.Report, error) {
	return relevo.Status(ctx, s.rt, relevo.Detail(false))
}

func (s mastermindSource) Runtime(key string) (relevo.Runtime, string, bool) {
	return s.rt, key, true
}

// Base is the runtime itself: the mastermind's only runtime owns its
// database, its store and everything else fleet-wide reads need.
func (s mastermindSource) Base() relevo.Runtime {
	return s.rt
}

// MarkViewed writes through to the mastermind's own store; key is the row's
// Key(), which on a mastermind source is the bare binding name (OwnerLabel is
// always "" here). Errors are dropped: a stamp is not worth failing a
// read-only screen over.
func (s mastermindSource) MarkViewed(key string) {
	_ = s.rt.Store.MarkViewed(key, time.Now())
}

// serverSource reads every enrolled client's store on a serve box, through
// serve.FlatStatus and Server.OwnerRuntime.
type serverSource struct {
	srv *serve.Server
}

// ServerSource wraps a serve server as a ui Source.
func ServerSource(srv *serve.Server) Source {
	return serverSource{srv: srv}
}

// Status is the fleet refresh with the detail figures left off, as on
// mastermindSource: the fleet view draws none of them and the detail pane's
// own fetch supplies them per row.
func (s serverSource) Status(ctx context.Context) (view.Report, error) {
	return serve.FlatStatus(ctx, s.srv, relevo.Detail(false))
}

func (s serverSource) Runtime(key string) (relevo.Runtime, string, bool) {
	// The owner part is a ClientID ("SHA256:<base64>") whose base64
	// alphabet includes '/', so the separator is the LAST slash, never
	// the first: splitting at the first broke resolution outright for
	// every client whose digest happens to contain one (~half of ids).
	// Binding names cannot contain '/' (they are filesystem-safe), so
	// the last slash is unambiguous.
	i := strings.LastIndexByte(key, '/')
	if i <= 0 || i == len(key)-1 {
		return relevo.Runtime{}, "", false
	}
	rt, err := s.srv.OwnerRuntime(remote.ClientID(key[:i]))
	if err != nil {
		return relevo.Runtime{}, "", false
	}
	return rt, key[i+1:], true
}

// Base is a runtime with no database: the server box does not run
// relevo.db, so scope all is refused there with the existing "no
// database" notice. Nothing else fleet-wide is read on a server.
func (s serverSource) Base() relevo.Runtime {
	return relevo.Runtime{Now: time.Now}
}

// MarkViewed is a no-op on a server source: the viewed mark is a record field
// on whichever client machine actually holds the binding's store, and a
// server box never writes into a client's state directory.
func (s serverSource) MarkViewed(key string) {}

// notTTY reports whether stdout is not a character device -- the refusal
// path's only testable seam is stdoutStat.
func notTTY() bool {
	info, err := stdoutStat()
	return err != nil || info.Mode()&os.ModeCharDevice == 0
}

// pipeRefusal is the full refusal line: hint when set, today's mastermind
// text otherwise.
func pipeRefusal(hint string) error {
	if hint == "" {
		hint = "relevo ui needs a terminal; use `relevo status` when piping"
	}
	return errors.New(hint)
}

// RunSource renders the fleet src reads until the user quits or ctx is
// cancelled. It never mutates state.
//
// Preconditions:  stdout is a character device.
// Postconditions: the terminal is restored, including on panic.
// Errors:         startup failures only. Refresh failures never escape.
//
// The tty refusal prints opts.PipeHint when set -- the full refusal line,
// not a suffix -- and the mastermind text otherwise.
func RunSource(ctx context.Context, src Source, opts Options) error {
	if notTTY() {
		return pipeRefusal(opts.PipeHint)
	}

	if opts.Interval <= 0 {
		opts.Interval = defaultInterval
	} else if opts.Interval < minInterval {
		opts.Interval = minInterval
	}

	model := newModel(ctx, src, opts)
	if opts.Prefs.KV != nil {
		model = model.applyPrefs(loadPrefs(opts.Prefs))
	}
	model.notice = opts.Notice

	// The wheel scrolls the top view; Shift+drag selects text, since mouse
	// reporting takes over the terminal's own click-drag selection.
	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithContext(ctx))

	// Every internal write -- including slog's -- is captured into the
	// footer and :log while the cockpit runs (§4.4), so nothing can corrupt
	// the screen. restore runs on the way out, panic included.
	restore, err := captureStderr(p.Send)
	if err != nil {
		return err
	}
	defer restore()

	_, err = p.Run()
	return runResult(ctx, err)
}

// runResult maps bubbletea's exit into RunSource's (and Run's) contract: a
// cancelled context is a clean exit, not an error. Kept separate so it can
// be tested without starting a terminal program -- the test that did that
// failed anywhere without a tty, including every CI runner.
func runResult(ctx context.Context, err error) error {
	if ctx.Err() != nil && (errors.Is(err, tea.ErrProgramKilled) || errors.Is(err, tea.ErrInterrupted)) {
		return nil
	}
	return err
}
