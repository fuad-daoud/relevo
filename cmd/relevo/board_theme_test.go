package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/board/theme"
	"github.com/fuad-daoud/relevo/internal/db"
)

// The `themes` config section as storage: whole palettes in, the same
// palettes out, and the validation the set path enforces.

// themesNight is the palette the section tests store, as the section
// carries it: whole, valid, and distinct in every colour from the seed's two.
const themesNight = `{"bg":"#0d1b1e","ink":"#dfe6e8","muted":"#7f9599","faint":"#475d61","line":"#284044","panel":"#132428","accent":"#8be9fd","good":"#6ff2a8","warn":"#ffd166","bad":"#ff6f8e","comment":"#c9a0ff"}`

// TestConfigSetThemesStoresAPalette pins the CLI write half: a whole palette
// stores through `config set themes.<name>`, and a body with a bad colour, a
// missing field or an unknown field refuses naming the palette and the field.
func TestConfigSetThemesStoresAPalette(t *testing.T) {
	initRoot(t)

	_, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "set", "themes.night", themesNight})
	})
	if err != nil {
		t.Fatalf("config set themes.night: %v (stderr: %s)", err, stderr)
	}
	L := storedConfig(t)
	got, ok := L.Themes["night"]
	if !ok {
		t.Fatalf("stored themes = %+v, want night", L.Themes)
	}
	if got.Name != "night" || got.Accent != "#8be9fd" || got.Comment != "#c9a0ff" {
		t.Errorf("stored palette = %+v, want the written colours", got)
	}

	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"a bad colour", strings.Replace(themesNight, `"bg":"#0d1b1e"`, `"bg":"teal"`, 1), "bg"},
		{"a missing field", `{"ink":"#dfe6e8"}`, "no bg field"},
		{"an unknown field", `{"swamp":"#123456"}`, `"swamp"`},
	} {
		_, stderr, err := captureOutput(t, func() error {
			return run([]string{"config", "set", "themes.bad", tc.body})
		})
		ce := requireCLIError(t, err, codeConfigInvalid, "")
		if !strings.Contains(ce.message, tc.want) {
			t.Errorf("%s: refusal = %q, want it to name %q", tc.name, ce.message, tc.want)
		}
		_ = stderr
	}
}

// TestConfigGetThemesPrintsIt pins the CLI read half: `config get
// themes.<name> --json` prints the palette as stored, a palette absent from
// the section is not set, and on a freshly seeded state cockpit's palette
// prints without any set of one.
func TestConfigGetThemesPrintsIt(t *testing.T) {
	initRoot(t)

	if _, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "set", "themes.night", themesNight})
	}); err != nil {
		t.Fatalf("config set themes.night: %v (stderr: %s)", err, stderr)
	}
	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "get", "themes.night", "--json"})
	})
	if err != nil {
		t.Fatalf("config get themes.night --json: %v (stderr: %s)", err, stderr)
	}
	var got map[string]string
	if err := json.Unmarshal(stdout, &got); err != nil {
		t.Fatalf("get output is not a palette document: %v (%q)", err, stdout)
	}
	if got["accent"] != "#8be9fd" || got["comment"] != "#c9a0ff" {
		t.Errorf("printed palette = %+v, want the stored colours", got)
	}

	// A palette absent from the section is not set: the seed never writes a
	// name the release does not carry.
	_, _, err = captureOutput(t, func() error {
		return run([]string{"config", "get", "themes.ghost"})
	})
	_ = requireCLIError(t, err, codeConfigPathNotSet, "")

	if _, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "get", "themes.cockpit", "--json"})
	}); err != nil {
		t.Fatalf("config get themes.cockpit --json on a seeded machine: %v (stderr: %s)", err, stderr)
	}
}

// TestBoardThemeSetsRefusesAMalformedStoredPalette pins the stored read: a
// themes body carrying no colour reaches the verb as the palette refusal,
// not a stored write.
func TestBoardThemeSetsRefusesAMalformedStoredPalette(t *testing.T) {
	initRoot(t)
	d, err := openDBDirect(machineDBPath())
	if err != nil {
		t.Fatalf("openDBDirect: %v", err)
	}
	bad := `{"night":` + strings.Replace(themesNight, `"accent":"#8be9fd"`, `"accent":"red"`, 1) + `}`
	if err := d.Local().Tx(func(tx *db.Tx) error {
		return tx.ConfigPut("themes", []byte(bad), time.Now().UTC())
	}); err != nil {
		_ = d.Close()
		t.Fatalf("seed themes: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	_, _, err = boardThemeSets(t.TempDir())
	if err == nil {
		t.Fatal("a malformed stored palette resolved")
	}
	if !errors.Is(err, theme.ErrUsage) {
		t.Errorf("malformed stored palette = %v, want the palette refusal", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "accent") {
		t.Errorf("refusal %q does not name the field", err)
	}
}
