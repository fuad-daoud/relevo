package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/board"
	"github.com/fuad-daoud/relevo/internal/board/theme"
)

// The board's theme tiers: the flag, the repository's own setting, the
// repo-local git key, the stored section, and the seed file behind them.

// themesNight is the palette the resolution tests store, as the section
// carries it: whole, valid, and distinct in every colour from the seed's two.
const themesNight = `{"bg":"#0d1b1e","ink":"#dfe6e8","muted":"#7f9599","faint":"#475d61","line":"#284044","panel":"#132428","accent":"#8be9fd","good":"#6ff2a8","warn":"#ffd166","bad":"#ff6f8e","comment":"#c9a0ff"}`

// nightPalette is that palette as one stored entry.
func nightPalette() board.Theme {
	out, err := theme.Parse("night", []byte(themesNight))
	if err != nil {
		panic(err)
	}
	return out
}

// TestBoardWithoutADatabaseReadsTheSeedFile pins the one fallback: a runtime
// with no stored section -- no database, or one never seeded -- resolves
// against the embedded seed file, so a fresh install opens in cockpit and
// names blueprint like the release does.
func TestBoardWithoutADatabaseReadsTheSeedFile(t *testing.T) {
	initRoot(t)

	rt, _, err := newRuntimeReadOnly()
	if err != nil {
		t.Fatalf("newRuntimeReadOnly: %v", err)
	}
	stored, err := boardThemeStored(rt)
	if err != nil {
		t.Fatalf("boardThemeStored: %v", err)
	}
	if len(stored) != len(theme.Seed()) {
		t.Fatalf("fallback set has %d palettes, want the seed's %d", len(stored), len(theme.Seed()))
	}
	for name, want := range theme.Seed() {
		if stored[name] != want {
			t.Errorf("fallback %q = %+v, want the seed's %+v", name, stored[name], want)
		}
	}
	th, err := theme.Lookup(theme.DefaultName, stored)
	if err != nil || th.Name != "cockpit" {
		t.Errorf("the fallback resolves a bare run = %v, %v; want cockpit", th, err)
	}
}

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

// TestBoardKeepsCreatingNothing pins the peek shape the resolution rides on:
// resolving a theme under an empty state root -- the read-only runtime the
// resolution opens -- writes nothing there, so `relevo board` creates no
// database and no config.
func TestBoardKeepsCreatingNothing(t *testing.T) {
	initRoot(t)

	if _, err := board.ResolveTheme(boardDeps(), ".", ""); err != nil {
		t.Fatalf("ResolveTheme over an empty state: %v", err)
	}
	if entries := stateEntries(t, os.Getenv("XDG_STATE_HOME")); len(entries) != 0 {
		t.Errorf("resolving a theme wrote %v under the state root, want nothing", entries)
	}
}

// stateEntries lists dir's entries; a directory that does not exist yet is
// the empty set it must stay as.
func stateEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read %s: %v", dir, err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}
