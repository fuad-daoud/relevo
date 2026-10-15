// Package theme is the board palette's one home. The palettes are seed data:
// the two built-ins live in the embedded seed.json beside this file and are
// written into the config's `themes` section once per database, after which
// every lookup reads that section. This package carries the palette type, the
// parse of one stored palette, the lookup over a stored set, and the refusal
// vocabulary both the board package and the board verb build on.
//
// The board verb keeps one more fallback: a machine with no database, or one
// that has never been seeded, reads the embedded seed file through Seed so a
// fresh install still paints. Reading the seed file is the only fallback --
// once a machine is seeded, an unset palette stays unset.
package theme

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrUsage marks a refusal the command line reports as a usage error (exit 2).
var ErrUsage = errors.New("usage")

// usageError is a refusal whose message is the human text alone; it reports
// itself as ErrUsage to errors.Is.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }
func (e *usageError) Unwrap() error { return ErrUsage }

func usagef(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

// seedJSON is the embedded seed file: the palettes the release writes into
// the `themes` section once per database, and the only fallback a board verb
// reads when no stored set exists.
//
//go:embed seed.json
var seedJSON []byte

// FontFamily and Roughness are the two appState defaults every palette shares:
// Cascadia Code (3) and a clean stroke.
const (
	FontFamily = 3
	Roughness  = 0
)

// DefaultName is the theme a resolution falls back to when the flag, the
// repository's setting and the repo-local git config key all name nothing.
const DefaultName = "cockpit"

// paletteFields are the eleven colours one palette carries, in the order the
// seed file and every refusal name them.
var paletteFields = []string{
	"bg", "ink", "muted", "faint", "line", "panel",
	"accent", "good", "warn", "bad", "comment",
}

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

// ValidColour reports whether s is one colour the palette may store: a # sign
// followed by 3, 4, 6 or 8 hex digits, either case, and nothing else. The
// values are injected into CSS and SVG attributes in the page, and the seed
// palettes are all #rrggbb, so no rgb() form, no var() and no named colour is
// accepted.
func ValidColour(s string) bool {
	const hexdigits = "0123456789abcdefABCDEF"
	if !strings.HasPrefix(s, "#") {
		return false
	}
	rest := s[1:]
	// Three hex digits are the base form; the extra digits over them must be
	// exactly 1, 3 or 5 more, giving the 4-, 6- and 8-digit forms.
	switch len(rest) {
	case 3, 4, 6, 8:
	default:
		return false
	}
	for i := 0; i < len(rest); i++ {
		if !strings.ContainsRune(hexdigits, rune(rest[i])) {
			return false
		}
	}
	return true
}

// validName reports whether name may key one stored palette.
func validName(name string) error {
	switch {
	case name == "":
		return errors.New("a palette needs a name")
	case strings.ContainsAny(name, "/ \t\r\n"):
		return fmt.Errorf("a palette name takes no slash or whitespace: %q", name)
	case len(name) > 64:
		return fmt.Errorf("a palette name is at most 64 bytes: %q", name)
	}
	return nil
}

// Seed returns the palettes the release seeds into the `themes` section, keyed
// by name, each one carrying its own name. A machine that has never been
// seeded and a board verb with no database resolve against this set.
func Seed() map[string]Theme {
	out := make(map[string]Theme)
	for name, raw := range SeedEntries() {
		var entry map[string]json.RawMessage
		if err := json.Unmarshal(raw, &entry); err != nil {
			// The embedded file is the release's own data; a decode failure
			// here is a build bug, not a refusal a caller can act on.
			panic(fmt.Sprintf("theme: embedded seed.json is not palettes: %v", err))
		}
		t, err := decode(name, entry)
		if err != nil {
			panic(fmt.Sprintf("theme: embedded seed.json palette %q: %v", name, err))
		}
		out[name] = t
	}
	return out
}

// SeedEntries is the embedded seed file as it stores, one raw palette per
// name. The seed path writes these bytes as they are, so a seeded body and
// the release's data never drift encodings.
func SeedEntries() map[string]json.RawMessage {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(seedJSON, &raw); err != nil {
		panic(fmt.Sprintf("theme: embedded seed.json is not an object: %v", err))
	}
	return raw
}

// Names lists the names of a stored set in sorted order, so a refusal names
// them the same way whichever machine renders it.
func Names(stored map[string]Theme) []string {
	out := make([]string, 0, len(stored))
	for name := range stored {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// SeedNames is Names over the embedded seed file: the names the release
// carries, sorted, in the order the seed path tries them.
func SeedNames() []string { return Names(Seed()) }

// Lookup returns the named palette from stored. Every lookup reads the section
// (or, on a machine that has never been seeded, the seed file through stored),
// so a stored entry is the whole of the set: there is no built-in behind it.
// An unknown name is a usage refusal naming every stored and, by the seed
// fallback, built-in name, and saying how to add one.
func Lookup(name string, stored map[string]Theme) (Theme, error) {
	if t, ok := stored[name]; ok {
		return t, nil
	}
	return Theme{}, usagef("unknown theme %q; themes this machine holds: %s; add one with: relevo config set themes.%s '{...}'",
		name, strings.Join(Names(stored), ", "), name)
}

// Parse reads and checks one stored palette: body is the value under a single
// `themes/<name>` config key, so it carries the eleven palette colours and,
// because the key already names the theme, no "name" field. A body that is
// not an object, a missing or unknown field, a non-string value and a
// non-colour value are each refused with the field's and the palette's own
// name.
func Parse(name string, body []byte) (Theme, error) {
	if err := validName(name); err != nil {
		return Theme{}, usagef("themes.%s: %v", name, err)
	}
	var entry map[string]json.RawMessage
	if err := json.Unmarshal(body, &entry); err != nil {
		return Theme{}, usagef("themes.%s: entry is not an object: %v", name, err)
	}
	if entry == nil {
		return Theme{}, usagef("themes.%s: entry is null", name)
	}
	t, err := decode(name, entry)
	if err != nil {
		return Theme{}, usagef("themes.%s: %v", name, err)
	}
	return t, nil
}

// decode folds one entry's fields into a palette, sorted so a body with two
// problems refuses on the same field whichever order the map handed them back
// in.
func decode(name string, entry map[string]json.RawMessage) (Theme, error) {
	names := make([]string, 0, len(entry))
	for field := range entry {
		names = append(names, field)
	}
	sort.Strings(names)

	var t Theme
	seen := make(map[string]bool, len(paletteFields))
	for _, field := range names {
		if !isPaletteField(field) {
			return Theme{}, fmt.Errorf("unknown field %q; a palette takes %s", field, strings.Join(paletteFields, ", "))
		}
		seen[field] = true
		var colour string
		if err := json.Unmarshal(entry[field], &colour); err != nil {
			return Theme{}, fmt.Errorf("%s is not text: %w", field, err)
		}
		if !ValidColour(colour) {
			return Theme{}, fmt.Errorf("%s is not a colour: %q; a palette takes # and 3, 4, 6 or 8 hex digits", field, colour)
		}
		switch field {
		case "bg":
			t.BG = colour
		case "ink":
			t.Ink = colour
		case "muted":
			t.Muted = colour
		case "faint":
			t.Faint = colour
		case "line":
			t.Line = colour
		case "panel":
			t.Panel = colour
		case "accent":
			t.Accent = colour
		case "good":
			t.Good = colour
		case "warn":
			t.Warn = colour
		case "bad":
			t.Bad = colour
		case "comment":
			t.Comment = colour
		}
	}
	for _, field := range paletteFields {
		if !seen[field] {
			return Theme{}, fmt.Errorf("no %s field; a palette takes %s", field, strings.Join(paletteFields, ", "))
		}
	}
	t.Name = name
	return t, nil
}

// isPaletteField reports whether field is one of the eleven palette colours.
func isPaletteField(field string) bool {
	for _, f := range paletteFields {
		if f == field {
			return true
		}
	}
	return false
}

// themeJSON is the palette without its name, which is what a section entry
// stores: the key already names the theme, so Encode leaves the name out and
// the bytes re-parse through Parse.
type themeJSON struct {
	BG      string `json:"bg"`
	Ink     string `json:"ink"`
	Muted   string `json:"muted"`
	Faint   string `json:"faint"`
	Line    string `json:"line"`
	Panel   string `json:"panel"`
	Accent  string `json:"accent"`
	Good    string `json:"good"`
	Warn    string `json:"warn"`
	Bad     string `json:"bad"`
	Comment string `json:"comment"`
}

// Encode renders a stored set as a section body: one entry per palette under
// its name, each entry the eleven colours and no name field, the whole thing
// indented like every other section body the store renders. The map's keys
// sort, so the same set always encodes to the same bytes.
func Encode(entries map[string]Theme) ([]byte, error) {
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make(map[string]themeJSON, len(names))
	for _, name := range names {
		out[name] = toPalette(entries[name])
	}
	body, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, err
	}
	return body, nil
}

func toPalette(t Theme) themeJSON {
	return themeJSON{
		BG: t.BG, Ink: t.Ink, Muted: t.Muted, Faint: t.Faint,
		Line: t.Line, Panel: t.Panel, Accent: t.Accent,
		Good: t.Good, Warn: t.Warn, Bad: t.Bad, Comment: t.Comment,
	}
}
