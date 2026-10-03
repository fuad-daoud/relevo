// Package board is the local Excalidraw whiteboard: the scene file and its
// companion svg, the theme table, the HTTP surface the page talks to, and the
// embedded page assets.
package board

import (
	"errors"
	"fmt"
	"strings"
)

// ErrUsage marks a refusal the command line reports as a usage error (exit 2).
var ErrUsage = errors.New("usage")

// usageError is a refusal whose message is the human text alone; it reports
// itself as ErrUsage to errors.Is.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }
func (e *usageError) Unwrap() error { return ErrUsage }

// usagef builds that refusal.
func usagef(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

// FontFamily and Roughness are the two appState defaults every built-in theme
// shares: Cascadia Code (3) and a clean stroke.
const (
	FontFamily = 3
	Roughness  = 0
)

// Theme is one palette. Every field is a colour the page and its exports use;
// the appState defaults for new elements derive from Ink and BG.
type Theme struct {
	Name   string `json:"name"`
	BG     string `json:"bg"`
	Ink    string `json:"ink"`
	Muted  string `json:"muted"`
	Faint  string `json:"faint"`
	Line   string `json:"line"`
	Panel  string `json:"panel"`
	Accent string `json:"accent"`
	Good   string `json:"good"`
	Warn   string `json:"warn"`
	Bad    string `json:"bad"`
	// Comment is the single colour both writers' comment text uses; by
	// distinguishes the agent from the human.
	Comment string `json:"comment"`
}

// themes is the built-in table, cockpit first: it is the default.
var themes = []*Theme{
	{
		Name: "cockpit",
		BG:   "#0f1115", Ink: "#e6e8ec", Muted: "#9097a3", Faint: "#596070",
		Line: "#3a4150", Panel: "#1a1f28", Accent: "#6ea8fe",
		Good: "#5fd08f", Warn: "#f2b84b", Bad: "#ff6b81",
		Comment: "#b48cf2",
	},
	{
		Name: "blueprint",
		BG:   "#0b1220", Ink: "#dbe4f0", Muted: "#8296b3", Faint: "#4c5f7d",
		Line: "#2c3c58", Panel: "#111c2f", Accent: "#7dd3fc",
		Good: "#86efac", Warn: "#fcd34d", Bad: "#fca5a5",
		Comment: "#c4b5fd",
	},
}

// Names lists the built-in theme names in table order, cockpit first.
func Names() []string {
	out := make([]string, 0, len(themes))
	for _, t := range themes {
		out = append(out, t.Name)
	}
	return out
}

// Lookup returns the named theme. An unknown name is a usage refusal naming the
// built-ins.
func Lookup(name string) (*Theme, error) {
	for _, t := range themes {
		if t.Name == name {
			return t, nil
		}
	}
	return nil, usagef("unknown theme %q; valid themes: %s", name, strings.Join(Names(), ", "))
}

// AppStateDefaults is the appState new elements start with under this theme.
// viewBackgroundColor is set only for a new scene, so an existing scene's
// stored colours are never rewritten.
func (t *Theme) AppStateDefaults(isNew bool) map[string]any {
	m := map[string]any{
		"theme":                      "light",
		"currentItemStrokeColor":     t.Ink,
		"currentItemBackgroundColor": "transparent",
		"currentItemFontFamily":      FontFamily,
		"currentItemRoughness":       Roughness,
	}
	if isNew {
		m["viewBackgroundColor"] = t.BG
	}
	return m
}
