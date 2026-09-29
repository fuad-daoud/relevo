// Package transcript renders a headless builder's streamed output -- one JSON
// event per line, in each harness's own shape -- into the lines a human reads
// in the builder log. It is presentation only: nothing decides anything on it.
package transcript

import (
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/fuad-daoud/relevo/internal/sanitize"
)

const maxArg = 200

var argKeys = []string{"command", "file_path", "path", "AbsolutePath", "pattern", "description", "prompt", "query", "url"}

// sanitizeLines applies sanitize.Text to every rendered line, in place of the
// raw text a harness wrote.
func sanitizeLines(lines []string) []string {
	if len(lines) == 0 {
		return lines
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = sanitize.Text(l)
	}
	return out
}
func unknown(obj map[string]any) string {
	if t := str(obj["type"]); t != "" {
		return "[" + t + "]"
	}
	if e := str(obj["event"]); e != "" {
		return "[" + e + "]"
	}
	return "[?]"
}

// toolLine is "● <name> <main argument>"; the marker lets a reader tell a call
// from assistant prose.
func toolLine(name string, params map[string]any) string {
	if arg, ok := mainArg(params); ok {
		return "● " + name + " " + oneLine(arg)
	}
	return "● " + name
}

func mainArg(params map[string]any) (string, bool) {
	for _, k := range argKeys {
		if s := str(params[k]); s != "" {
			return s, true
		}
	}
	var strs []string
	for k, v := range params {
		if s := str(v); s != "" {
			strs = append(strs, k)
		}
	}
	if len(strs) == 0 {
		return "", false
	}
	sort.Strings(strs)
	return str(params[strs[0]]), true
}

// okLine is a successful tool result on one line, so three parallel calls'
// results still tell apart.
func okLine(output string) string {
	if output = oneLine(output); output == "" {
		return "  ⎿ ok"
	}
	return "  ⎿ ok: " + output
}

func errLine(msg string) string {
	if msg = oneLine(msg); msg == "" {
		return "  ⎿ error"
	}
	return "  ⎿ error: " + msg
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func oneLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimRight(s, "\r")
	if len(s) > maxArg {
		cut := maxArg
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "..."
	}
	return s
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}
