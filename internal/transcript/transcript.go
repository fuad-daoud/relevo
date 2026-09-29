// Package transcript renders a headless builder's streamed output -- one JSON
// event per line, in each harness's own shape -- into the lines a human reads
// in the builder log. It is presentation only: nothing decides anything on it.
package transcript

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"
)

const maxArg = 200

var argKeys = []string{"command", "file_path", "path", "AbsolutePath", "pattern", "description", "prompt", "query", "url"}

// Render turns one raw line of the stream (without its trailing newline) into
// the lines to append to the log. An empty line and a supervisor trailer are
// nothing; a line that is not a JSON object is itself, verbatim (that is how a
// plain-text error reaches the log); an unknown event renders as "[<type>]" so
// a harness upgrade degrades to noise, not silence. Never errors, never panics.
func Render(kind string, line []byte) []string {
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
		return renderClaude(obj)
	case "agy":
		return renderAgy(obj)
	case "opencode":
		return renderOpencode(obj)
	case "codex":
		return renderCodex(obj)
	}
	return []string{unknown(obj)}
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
