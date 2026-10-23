package candidate

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Window is a daily clock interval; From is inclusive and To exclusive, and a
// From after To wraps midnight.
type Window struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Windows is a decode view of the stored body, never re-encoded. Mode is
// "block" (default) or "demote".
type Windows struct {
	Mode  string   `json:"mode,omitempty"`
	Only  []Window `json:"only,omitempty"`
	Avoid []Window `json:"avoid,omitempty"`
}

const (
	WindowModeBlock  = "block"
	WindowModeDemote = "demote"
)

// Clock is a time of day in a fixed offset or a named zone.
type Clock struct {
	Hour, Min int
	loc       *time.Location
}

// ParseWindowClock reads "14:00", "14:00Z", "22:00+08:00", "22:00+0800" or
// "17:00 Asia/Amman".
func ParseWindowClock(s string) (Clock, error) {
	hm, rest := s, ""
	if i := strings.IndexByte(s, ' '); i >= 0 {
		hm, rest = s[:i], s[i+1:]
		if rest == "" || strings.ContainsAny(rest, " \t") {
			return Clock{}, fmt.Errorf("want HH:MM, HH:MMZ, HH:MM+HH:MM or \"HH:MM Zone\"")
		}
		loc, err := time.LoadLocation(rest)
		if err != nil {
			return Clock{}, fmt.Errorf("unknown zone %q", rest)
		}
		return clockHM(hm, loc)
	}
	if len(hm) < 5 {
		return Clock{}, fmt.Errorf("want HH:MM, HH:MMZ, HH:MM+HH:MM or \"HH:MM Zone\"")
	}
	clock, zone := hm[:5], hm[5:]
	loc := time.UTC
	switch {
	case zone == "" || zone == "Z":
	case zone[0] == '+' || zone[0] == '-':
		off, err := parseOffset(zone)
		if err != nil {
			return Clock{}, err
		}
		loc = time.FixedZone(zone, off)
	default:
		return Clock{}, fmt.Errorf("unknown suffix %q", zone)
	}
	return clockHM(clock, loc)
}

func parseOffset(z string) (int, error) {
	sign := 1
	if z[0] == '-' {
		sign = -1
	}
	d := strings.Replace(z[1:], ":", "", 1)
	if len(d) != 4 {
		return 0, fmt.Errorf("bad offset %q", z)
	}
	h, e1 := strconv.Atoi(d[:2])
	m, e2 := strconv.Atoi(d[2:])
	if e1 != nil || e2 != nil || h > 14 || m > 59 {
		return 0, fmt.Errorf("bad offset %q", z)
	}
	return sign * (h*3600 + m*60), nil
}

func clockHM(s string, loc *time.Location) (Clock, error) {
	if len(s) != 5 || s[2] != ':' {
		return Clock{}, fmt.Errorf("want HH:MM, got %q", s)
	}
	h, e1 := strconv.Atoi(s[:2])
	m, e2 := strconv.Atoi(s[3:])
	if e1 != nil || e2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return Clock{}, fmt.Errorf("want HH:MM with hour 0-23 and minute 0-59, got %q", s)
	}
	return Clock{Hour: h, Min: m, loc: loc}, nil
}

// At returns the instant of this clock on the calendar day of t in the clock's zone.
func (c Clock) At(t time.Time) time.Time {
	l := t.In(c.loc)
	return time.Date(l.Year(), l.Month(), l.Day(), c.Hour, c.Min, 0, 0, c.loc)
}

// before returns the latest occurrence of c at or before t.
func (c Clock) before(t time.Time) time.Time {
	at := c.At(t)
	if at.After(t) {
		at = c.At(t.AddDate(0, 0, -1))
	}
	return at
}

// after returns the earliest occurrence of c strictly after t.
func (c Clock) after(t time.Time) time.Time {
	at := c.At(t)
	if !at.After(t) {
		at = c.At(t.AddDate(0, 0, 1))
	}
	return at
}

type span struct{ from, to Clock }

func (w Window) span() (span, error) {
	f, err := ParseWindowClock(w.From)
	if err != nil {
		return span{}, fmt.Errorf("from %q: %w", w.From, err)
	}
	t, err := ParseWindowClock(w.To)
	if err != nil {
		return span{}, fmt.Errorf("to %q: %w", w.To, err)
	}
	ref := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if f.At(ref).Equal(t.At(ref)) {
		return span{}, fmt.Errorf("from %q equals to %q: a window must cover part of a day", w.From, w.To)
	}
	return span{f, t}, nil
}

// bounds returns the latest occurrence of the window opening at or before t,
// and whether t is still inside it.
func (s span) bounds(t time.Time) (open, closeAt time.Time, in bool) {
	start := s.from.before(t)
	end := s.to.after(start)
	return start, end, t.Before(end)
}

// Covers reports whether now is inside the window; it returns false for a
// window that does not parse.
func (w Window) Covers(now time.Time) bool {
	s, err := w.span()
	if err != nil {
		return false
	}
	_, _, in := s.bounds(now)
	return in
}

func anyCovers(ws []Window, now time.Time) bool {
	for _, w := range ws {
		if w.Covers(now) {
			return true
		}
	}
	return false
}

// Covers reports whether the candidate may be used at now: inside some Only
// window when any is declared, and inside no Avoid window.
func (w Windows) Covers(now time.Time) bool {
	if len(w.Only) > 0 && !anyCovers(w.Only, now) {
		return false
	}
	return !anyCovers(w.Avoid, now)
}

// NextEdge returns the next instant after now at which any window opens or
// closes, and false when there is none.
func (w Windows) NextEdge(now time.Time) (time.Time, bool) {
	var next time.Time
	found := false
	consider := func(t time.Time) {
		if t.After(now) && (!found || t.Before(next)) {
			next, found = t, true
		}
	}
	for _, list := range [][]Window{w.Only, w.Avoid} {
		for _, win := range list {
			s, err := win.span()
			if err != nil {
				continue
			}
			_, closeAt, in := s.bounds(now)
			if in {
				consider(closeAt)
			} else {
				consider(s.from.after(now))
			}
		}
	}
	return next, found
}

func (w Windows) validate(name string, i int) error {
	switch w.Mode {
	case "", WindowModeBlock, WindowModeDemote:
	default:
		return fmt.Errorf("candidates %s: candidate %d: windows.mode %q: want \"block\" or \"demote\"", name, i, w.Mode)
	}
	if len(w.Only) == 0 && len(w.Avoid) == 0 {
		return fmt.Errorf("candidates %s: candidate %d: windows: only and avoid are both empty", name, i)
	}
	for field, list := range map[string][]Window{"only": w.Only, "avoid": w.Avoid} {
		for j, win := range list {
			if _, err := win.span(); err != nil {
				return fmt.Errorf("candidates %s: candidate %d: windows.%s[%d]: %w", name, i, field, j, err)
			}
		}
	}
	return nil
}
