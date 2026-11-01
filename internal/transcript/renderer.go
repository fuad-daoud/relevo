package transcript

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// clockLayout is the clock's shape: 8 cells, always the same width, so a stamped
// column never moves between lines.
const clockLayout = "15:04:05"

// Renderer renders one pass over a harness's stream, carrying no state between
// passes but keeping each pass's unanswered calls: a claude tool result carries
// the span from the call that asked for it.
type Renderer struct {
	pending  map[string]time.Time // claude tool_use id -> the event time that made the call
	museText strings.Builder      // muse output deltas not yet written
}

func NewRenderer() *Renderer {
	return &Renderer{pending: make(map[string]time.Time)}
}

// Render turns one raw stream line without its trailing newline into the log's
// lines, sanitised so a control byte in a harness's own text never reaches a log,
// a tail or `show`. Empty lines and supervisor trailers are nothing; a non-JSON
// line is itself, verbatim, which is how a plain-text error reaches the log. An
// unknown event of a known kind renders as "[<type>]" with the event's clock, of
// an unknown kind with no stamp. Never errors, never panics.
func (r *Renderer) Render(kind string, line []byte) []string {
	return sanitizeLines(r.render(kind, line))
}

func (r *Renderer) render(kind string, line []byte) []string {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil
	}
	if isTrailerLine(trimmed) {
		return nil
	}
	var obj map[string]any
	if trimmed[0] != '{' || json.Unmarshal(trimmed, &obj) != nil || obj == nil {
		return []string{string(line)}
	}
	switch kind {
	case "claude":
		return r.claude(obj)
	case "agy":
		return r.agy(obj)
	case "opencode":
		return r.opencode(obj)
	case "codex":
		return r.codex(obj)
	case "muse":
		return r.muse(obj)
	}
	return []string{unknown(obj)}
}

// stamp precedes a rendered element: the event's clock in the local zone and/or
// the span it measured, in that order, each followed by one space. An empty stamp
// is nothing at all.
type stamp struct {
	clock string // "15:04:05", or ""
	dur   string // "+4.2s", or ""
}

// at builds the stamp carrying the event's own clock; the zero time carries none.
func (r *Renderer) at(t time.Time) stamp {
	if t.IsZero() {
		return stamp{}
	}
	return stamp{clock: t.Local().Format(clockLayout)}
}

// span builds the stamp part for a duration_seconds-style field, absent when the
// field is missing, not a number, or negative.
func (r *Renderer) span(v any) stamp {
	secs, ok := v.(float64)
	if !ok {
		return stamp{}
	}
	return stamp{dur: durSecs(secs)}
}

// durSecs formats a span in seconds to one decimal; a negative span holds no
// measurement, so no duration.
func durSecs(secs float64) string {
	if secs < 0 {
		return ""
	}
	return fmt.Sprintf("+%.1fs", secs)
}

// line prefixes text with the stamp, on a multi-line element its first physical
// line only.
func (s stamp) line(text string) string {
	if s.clock == "" && s.dur == "" {
		return text
	}
	var b strings.Builder
	if s.clock != "" {
		b.WriteString(s.clock)
		b.WriteByte(' ')
	}
	if s.dur != "" {
		b.WriteString(s.dur)
		b.WriteByte(' ')
	}
	b.WriteString(text)
	return b.String()
}

// thinking stamps a thinking block's first physical line only, the block being
// one rendered element however many lines it spans.
func (s stamp) thinking(text string) []string {
	lines := thinkingLines(text)
	if len(lines) > 0 && (s.clock != "" || s.dur != "") {
		lines[0] = s.line(lines[0])
	}
	return lines
}

// SplitStamp splits a rendered line into the plain-text stamp the renderer
// prefixed and the entry's own bytes, so a marker-matching reader matches on the
// body. A line with no stamp returns ("", line).
func SplitStamp(line string) (stamp, body string) {
	i := 0
	if isClockPrefix(line) {
		i = len(clockLayout) + 1
	}
	if n, ok := durationPrefix(line[i:]); ok {
		i += n
	}
	if i == 0 {
		return "", line
	}
	return line[:i], line[i:]
}

// isClockPrefix reports whether line opens with a "15:04:05" clock and the single
// space the stamp puts before the next part.
func isClockPrefix(line string) bool {
	if len(line) < len(clockLayout)+1 || line[len(clockLayout)] != ' ' {
		return false
	}
	for i, c := range []byte(clockLayout) {
		if c == ':' {
			if line[i] != ':' {
				return false
			}
			continue
		}
		if line[i] < '0' || line[i] > '9' {
			return false
		}
	}
	return true
}

// durationPrefix returns the length of the "+X.Ys" duration and its trailing
// space at the start of s; ok is false when s does not open with one.
func durationPrefix(s string) (int, bool) {
	if len(s) < 6 || s[0] != '+' {
		return 0, false
	}
	i := 1
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 1 || i+4 > len(s) {
		return 0, false
	}
	if s[i] != '.' || s[i+1] < '0' || s[i+1] > '9' || s[i+2] != 's' || s[i+3] != ' ' {
		return 0, false
	}
	return i + 4, true
}

// claudeAt is a claude event's own clock, zero when the event carries none or it
// does not parse.
func claudeAt(obj map[string]any) time.Time {
	s := str(obj["timestamp"])
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// opencodeAt is an opencode event's own clock in epoch milliseconds, zero when
// the event carries none.
func opencodeAt(obj map[string]any) time.Time {
	ms, ok := obj["timestamp"].(float64)
	if !ok {
		return time.Time{}
	}
	return time.UnixMilli(int64(ms))
}

// timeSpan is an opencode part's measured span in milliseconds, absent when
// either end is missing or the end precedes the start.
func (r *Renderer) timeSpan(v any) string {
	m := asMap(v)
	start, sok := m["start"].(float64)
	end, eok := m["end"].(float64)
	if !sok || !eok {
		return ""
	}
	return durSecs((end - start) / 1000)
}

// callSpan is a claude tool_result line's stamp: the result event's clock plus
// the span from the tool_use event that made the call, when this pass saw that
// call. The call is consumed either way; an unknown, unanswered, undated or
// backwards pair yields the clock only.
func (r *Renderer) callSpan(id string, end time.Time) stamp {
	s := r.at(end)
	start, ok := r.pending[id]
	if !ok {
		return s
	}
	delete(r.pending, id)
	if end.IsZero() || end.Before(start) {
		return s
	}
	s.dur = durSecs(end.Sub(start).Seconds())
	return s
}
