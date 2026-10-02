package board

import (
	"errors"
	"strings"
	"testing"
)

// TestThemePalettes pins every colour of both built-ins, so a typo in the
// table is a test failure.
func TestThemePalettes(t *testing.T) {
	want := map[string]Theme{
		"cockpit": {
			Name: "cockpit",
			BG:   "#0f1115", Ink: "#e6e8ec", Muted: "#9097a3", Faint: "#596070",
			Line: "#3a4150", Panel: "#1a1f28", Accent: "#6ea8fe",
			Good: "#5fd08f", Warn: "#f2b84b", Bad: "#ff6b81",
			Comment: "#b48cf2",
		},
		"blueprint": {
			Name: "blueprint",
			BG:   "#0b1220", Ink: "#dbe4f0", Muted: "#8296b3", Faint: "#4c5f7d",
			Line: "#2c3c58", Panel: "#111c2f", Accent: "#7dd3fc",
			Good: "#86efac", Warn: "#fcd34d", Bad: "#fca5a5",
			Comment: "#c4b5fd",
		},
	}
	if len(themes) != len(want) {
		t.Fatalf("themes has %d entries, want %d", len(themes), len(want))
	}
	for _, got := range themes {
		w, ok := want[got.Name]
		if !ok {
			t.Fatalf("unexpected theme %q", got.Name)
		}
		if *got != w {
			t.Errorf("theme %q = %+v, want %+v", got.Name, *got, w)
		}
	}
}

// TestThemeAppStateDefaults pins the shared defaults: Cascadia, roughness 0,
// ink strokes, and a view background only for a new scene.
func TestThemeAppStateDefaults(t *testing.T) {
	c, err := Lookup("cockpit")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}

	fresh := c.AppStateDefaults(true)
	if fresh["currentItemFontFamily"] != FontFamily {
		t.Errorf("fontFamily = %v, want %d", fresh["currentItemFontFamily"], FontFamily)
	}
	if fresh["currentItemRoughness"] != Roughness {
		t.Errorf("roughness = %v, want %d", fresh["currentItemRoughness"], Roughness)
	}
	if fresh["currentItemStrokeColor"] != c.Ink {
		t.Errorf("stroke = %v, want %s", fresh["currentItemStrokeColor"], c.Ink)
	}
	if fresh["viewBackgroundColor"] != c.BG {
		t.Errorf("viewBackgroundColor = %v, want %s", fresh["viewBackgroundColor"], c.BG)
	}

	existing := c.AppStateDefaults(false)
	if _, ok := existing["viewBackgroundColor"]; ok {
		t.Error("an existing scene still gets a viewBackgroundColor")
	}
}

// TestLookupRefusesUnknown: an unknown name is a usage refusal naming every
// built-in.
func TestLookupRefusesUnknown(t *testing.T) {
	_, err := Lookup("nope")
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("Lookup(nope) = %v, want ErrUsage", err)
	}
	for _, name := range Names() {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("refusal %q does not name %q", err, name)
		}
	}
}
