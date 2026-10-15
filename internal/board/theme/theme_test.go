package theme

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// testPalette is cockpit's palette as it arrives from a caller's own body,
// with one colour changed so a test can tell a stored palette from the seed.
func testPalette() map[string]string {
	return map[string]string{
		"bg": "#101318", "ink": "#e7e9ed", "muted": "#9198a4", "faint": "#5a6171",
		"line": "#3b4251", "panel": "#1b2029", "accent": "#6fa9ff",
		"good": "#60d190", "warn": "#f3b94c", "bad": "#ff7c82",
		"comment": "#b58df3",
	}
}

// paletteBody marshals one name left at map[string]string straight into a
// palette body without going through Parse, so a test can name a field a
// parse would refuse.
func paletteBody(fields map[string]string) []byte {
	out, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return out
}

func goodBody() []byte {
	return []byte(`{"bg":"#0f1115","ink":"#e6e8ec","muted":"#9097a3","faint":"#596070","line":"#3a4150","panel":"#1a1f28","accent":"#6ea8fe","good":"#5fd08f","warn":"#f2b84b","bad":"#ff6b81","comment":"#b48cf2"}`)
}

// TestThemeBuiltinsAreCockpitAndBlueprint pins the seed file: it carries both
// palettes byte for byte as today's Go table does, cockpit first.
func TestThemeBuiltinsAreCockpitAndBlueprint(t *testing.T) {
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
	seed := Seed()
	if len(seed) != len(want) {
		t.Fatalf("seed carries %d palettes, want %d", len(seed), len(want))
	}
	for name, w := range want {
		if got, ok := seed[name]; !ok {
			t.Fatalf("seed carries no %q palette", name)
		} else if got != w {
			t.Errorf("seed palette %q = %+v, want %+v", name, got, w)
		}
	}
	if seed[DefaultName] == (Theme{}) {
		t.Errorf("the default name %q is not in the seed", DefaultName)
	}
	if SeedEntries()[DefaultName][0] != '{' {
		t.Errorf("seed entry for %q is not a JSON object", DefaultName)
	}
}

// TestThemeParseRefusesABadColour: every colour form the validator refuses,
// named by its field.
func TestThemeParseRefusesABadColour(t *testing.T) {
	for _, colour := range []string{
		"red",
		"rgb(0,0,0)",
		"#12345",
		"0f1115",
		"#",
	} {
		fields := testPalette()
		fields["ink"] = colour
		_, err := Parse("mine", paletteBody(fields))
		if !errors.Is(err, ErrUsage) {
			t.Errorf("Parse with ink = %q = %v, want a usage refusal", colour, err)
			continue
		}
		if !strings.Contains(err.Error(), "ink") {
			t.Errorf("refusal %q does not name ink", err)
		}
	}

	// The four hex lengths are a colour, and one more is not.
	if _, err := Parse("mine", goodBody()); err != nil {
		t.Fatalf("Parse good body: %v", err)
	}
	fields := testPalette()
	fields["ink"] = "#abc"
	if _, err := Parse("mine", paletteBody(fields)); err != nil {
		t.Errorf("3 hex digits: Parse = %v, want nil error", err)
	}
	fields["ink"] = "#abcd"
	if _, err := Parse("mine", paletteBody(fields)); err != nil {
		t.Errorf("4 hex digits: Parse = %v, want nil error", err)
	}
	fields["ink"] = "#abcdef"
	if _, err := Parse("mine", paletteBody(fields)); err != nil {
		t.Errorf("6 hex digits: Parse = %v, want nil error", err)
	}
	fields["ink"] = "#abcdef01"
	if _, err := Parse("mine", paletteBody(fields)); err != nil {
		t.Errorf("8 hex digits: Parse = %v, want nil error", err)
	}
}

// TestThemeParseRequiresEveryField: a missing field is a refusal naming it,
// never a silent default.
func TestThemeParseRequiresEveryField(t *testing.T) {
	for _, dropped := range paletteFields {
		fields := testPalette()
		delete(fields, dropped)
		_, err := Parse("mine", paletteBody(fields))
		if !errors.Is(err, ErrUsage) {
			t.Errorf("dropping %s: Parse = %v, want a usage refusal", dropped, err)
			continue
		}
		if !strings.Contains(err.Error(), dropped) {
			t.Errorf("refusal %q does not name %q", err, dropped)
		}
	}
}

// TestThemeParseRefusesAnUnknownField: anything the palette does not carry
// (its own name included, because the key names it) is refused by name.
func TestThemeParseRefusesAnUnknownField(t *testing.T) {
	fields := testPalette()
	fields["name"] = "mine"
	if _, err := Parse("mine", paletteBody(fields)); err == nil || !strings.Contains(errText(err), `"name"`) {
		t.Errorf("a name field: Parse = %v, want a refusal naming %q", err, "name")
	}
	fields = testPalette()
	fields["swamp"] = "#123456"
	if _, err := Parse("mine", paletteBody(fields)); err == nil || !strings.Contains(errText(err), `"swamp"`) {
		t.Errorf("an unknown field: Parse = %v, want a refusal naming %q", err, "swamp")
	}
}

// errText is err.Error() as a one-liner for the tests above.
func errText(err error) string { return err.Error() }

// TestThemeParseRefusesAnUnusedName: a palette name keys a config item, so it
// may not be empty, hold the key separator or carry whitespace.
func TestThemeParseRefusesAnUnusedName(t *testing.T) {
	for _, name := range []string{"", "a/b", "a b", "\ttabbed"} {
		if _, err := Parse(name, goodBody()); err == nil {
			t.Errorf("Parse(%q) = nil error, want a refusal", name)
		}
	}
}

// TestStoredThemeOverridesTheBuiltin pins that a lookup reads the stored set
// alone: a stored cockpit answers, never the seed file's cockpit, which is
// what makes a section the whole of what a palette lookup sees.
func TestStoredThemeOverridesTheBuiltin(t *testing.T) {
	stored := map[string]Theme{"cockpit": testStoredCockpit()}

	got, err := Lookup("cockpit", stored)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got.BG != stored["cockpit"].BG {
		t.Errorf("Lookup(cockpit).bg = %q, want the stored %q", got.BG, stored["cockpit"].BG)
	}
	if got == Seed()[DefaultName] {
		t.Errorf("Lookup answered the seed palette, want the stored one")
	}
}

// testStoredCockpit turns the test palette into a full Theme.
func testStoredCockpit() Theme {
	t, err := Parse("cockpit", paletteBody(testPalette()))
	if err != nil {
		panic(err)
	}
	return t
}

// TestThemeLookupNamesEveryStoredAndBuiltinTheme: the refusal names every
// name the consulted set holds and says how to add one.
func TestThemeLookupNamesEveryStoredAndBuiltinTheme(t *testing.T) {
	stored := map[string]Theme{
		"cockpit":   Seed()[DefaultName],
		"midnight":  testStoredCockpit(),
		"blueprint": Seed()["blueprint"],
	}
	_, err := Lookup("nope", stored)
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("Lookup(nope) = %v, want ErrUsage", err)
	}
	for _, name := range []string{"cockpit", "midnight", "blueprint"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("refusal %q does not name %q", err, name)
		}
	}
	if !strings.Contains(err.Error(), `relevo config set themes.nope '{...}'`) {
		t.Errorf("refusal %q does not say how to add a palette", err)
	}

	// The seed file is the stored set of a machine that has never been seeded,
	// so an unknown name there names both.
	_, err = Lookup("nope", Seed())
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("Lookup(nope, seed) = %v, want ErrUsage", err)
	}
	if !strings.Contains(err.Error(), "cockpit") || !strings.Contains(err.Error(), "blueprint") {
		t.Errorf("refusal against the seed = %q, want it to name both palettes", err)
	}
}

// TestThemeAppStateDefaults pins the shared defaults: Cascadia, roughness 0,
// ink strokes, and a view background only for a new scene.
func TestThemeAppStateDefaults(t *testing.T) {
	c, err := Lookup("cockpit", Seed())
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

// TestThemeEncodeRoundTrips pins the section-body shape Encode renders: one
// entry per palette, the eleven colours and no name field, and the bytes
// re-parse through Parse entry by entry.
func TestThemeEncodeRoundTrips(t *testing.T) {
	body, err := Encode(map[string]Theme{"mine": testStoredCockpit()})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if _, ok := decodedFields(body)["name"]; ok {
		t.Errorf("Encode carried a name field: %s", body)
	}
	got, err := Parse("mine", decodedFields(body)["mine"])
	if err != nil {
		t.Fatalf("Parse(Encode output): %v", err)
	}
	want := testStoredCockpit()
	want.Name = "mine" // the entry is re-keyed, so the name follows the lookup name
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

// decodedFields unmarshals body as a JSON object for the tests.
func decodedFields(body []byte) map[string]json.RawMessage {
	var out map[string]json.RawMessage
	if err := json.Unmarshal(body, &out); err != nil {
		panic(err)
	}
	return out
}
