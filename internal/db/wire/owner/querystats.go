//go:build unix

package owner

import (
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fuad-daoud/relevo/internal/db/wire"
)

// statsWindow is how long one served-statement report covers. A window writes
// up to maxShapesLogged lines to the journal, on every machine and for as long
// as the daemon runs, so it is long. It is a package var for the same reason
// maxConns is: a test can shrink it before Serve and watch a window close.
var statsWindow = 10 * time.Minute

// maxShapesLogged caps the shape lines one window writes: the bulk callers are
// what a report is read for, and the summary line still counts every shape.
const maxShapesLogged = 20

// shapeMaxLen caps one recorded shape: an ad-hoc connection carries whole
// statements, so one long query must not own the counters, or the log line a
// window emits for it.
const shapeMaxLen = 200

// shapeKey names one stream of served statements: a normalized shape, seen as
// one frame kind, on a connection handshaken with one pair of flags. The
// handshake carries no client kind, so a window's load is attributed to its
// callers by reading shape and flags against the code that sends them.
type shapeKey struct {
	kind  byte
	adHoc bool
	local bool
	shape string
}

// noteQuery counts one statement the connection is about to hand to the
// engine: a statement refused before its pin, or never dispatched, was not
// served and is not counted. Both flags are fixed at the handshake and never
// written again, so this reads them without the connection mutex.
func (s *Server) noteQuery(kind byte, adHoc, local bool, query string) {
	k := shapeKey{kind: kind, adHoc: adHoc, local: local, shape: normalizeShape(query)}
	s.statsMu.Lock()
	s.stats[k]++
	s.statsMu.Unlock()
}

// drainQueryStats detaches the window's counters and leaves a fresh map, so a
// report carries only the statements its own window served.
func (s *Server) drainQueryStats() map[shapeKey]int64 {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	whole := s.stats
	s.stats = make(map[shapeKey]int64)
	return whole
}

// reportQueryStats writes one window to the log the owner shares with the
// daemon: a summary line first, which also marks a quiet window, then one
// line per shape, largest first, so the bulk callers of the owner socket
// read before the small ones. The window is the one the reporter was built
// with, not the package var read again: a test that shrinks the var while a
// former reporter still ticks must not race the log it writes.
func (s *Server) reportQueryStats(window time.Duration) {
	stats := s.drainQueryStats()
	var total int64
	for _, n := range stats {
		total += n
	}
	slog.Info("owner: served statements window", "window", window.String(), "total", total, "shapes", len(stats))

	keys := make([]shapeKey, 0, len(stats))
	for k := range stats {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if ni, nj := stats[keys[i]], stats[keys[j]]; ni != nj {
			return ni > nj
		}
		if keys[i].shape != keys[j].shape {
			return keys[i].shape < keys[j].shape
		}
		if keys[i].kind != keys[j].kind {
			return keys[i].kind < keys[j].kind
		}
		if keys[i].adHoc != keys[j].adHoc {
			return !keys[i].adHoc
		}
		return keys[i].local && !keys[j].local
	})
	for _, k := range keys[:min(len(keys), maxShapesLogged)] {
		slog.Info("owner: served statement shape",
			"window", window,
			"kind", kindText(k.kind),
			"ad_hoc", k.adHoc,
			"local", k.local,
			"count", stats[k],
			"shape", k.shape)
	}
}

// watchQueryStats closes and reports a window every statsWindow until the
// accept loop it was started with returns: statements are counted only while
// the owner serves. It reads the package var once here, at the one point the
// go statement orders ahead of any later test's write, and carries the value
// through every window it reports.
func (s *Server) watchQueryStats(stop <-chan struct{}) {
	window := statsWindow
	ticker := time.NewTicker(window)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.reportQueryStats(window)
		case <-stop:
			return
		}
	}
}

// kindText names the frame kind in the log. The dispatch that reaches
// noteQuery carries only exec and query, so anything else reads as exec
// rather than inventing a third word.
func kindText(kind byte) string {
	if kind == wire.KindQuery {
		return "query"
	}
	return "exec"
}

// normalizeShape folds a statement to the text every caller of one query
// sends: runs of whitespace collapse to one space, a pasted-in decimal or
// single-quoted literal becomes ?, and the result is capped. Relevo's own SQL
// arrives with ? placeholders already written into it, so folding leaves a
// served statement as it was sent.
func normalizeShape(query string) string {
	var b strings.Builder
	b.Grow(len(query))
	last := byte(' ')
	pending := false
	for i := 0; i < len(query); {
		switch c := query[i]; {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f':
			pending = b.Len() > 0
			i++
		case c == '\'':
			if pending {
				b.WriteByte(' ')
			}
			b.WriteByte('?')
			i = skipQuoted(query, i)
			last, pending = '?', false
		case isDigit(c) && (pending || !continuesToken(last)):
			if pending {
				b.WriteByte(' ')
			}
			b.WriteByte('?')
			i = skipNumber(query, i)
			last, pending = '?', false
		default:
			if pending {
				b.WriteByte(' ')
			}
			b.WriteByte(c)
			last, pending = c, false
			i++
		}
	}
	shape := b.String()
	if len(shape) <= shapeMaxLen {
		return shape
	}
	cut := shapeMaxLen
	for cut > 0 && !utf8.RuneStart(shape[cut]) {
		cut--
	}
	return shape[:cut] + "..."
}

// skipQuoted consumes a single-quoted literal, where ” stands for one quote,
// and answers the index after it. A literal with no closing quote runs to the
// end of the statement rather than past it.
func skipQuoted(query string, at int) int {
	for i := at + 1; i < len(query); i++ {
		if query[i] != '\'' {
			continue
		}
		if i+1 < len(query) && query[i+1] == '\'' {
			i++
			continue
		}
		return i + 1
	}
	return len(query)
}

// skipNumber consumes a decimal literal -- its digits and one fractional part
// -- and answers the index after it. It is called on the literal's first
// digit.
func skipNumber(query string, at int) int {
	i := at
	for i < len(query) && isDigit(query[i]) {
		i++
	}
	if i+1 < len(query) && query[i] == '.' && isDigit(query[i+1]) {
		i++
		for i < len(query) && isDigit(query[i]) {
			i++
		}
	}
	return i
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// continuesToken reports whether a character carries an identifier or a
// numbered parameter across a digit: t2 stays t2 and ?7 keeps its slot, so
// folding hits only the digits that begin a literal.
func continuesToken(c byte) bool {
	return c == '?' || c == '_' || isDigit(c) ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
