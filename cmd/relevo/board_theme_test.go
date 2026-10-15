package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/board"
	"github.com/fuad-daoud/relevo/internal/board/theme"
	"github.com/fuad-daoud/relevo/internal/relevo"
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

// withBoardSets points the stored-set seam at the given palette set for one
// test, so the tiers are pinned without a machine state. The seed file is the
// usual set a test without a database resolves against.
func withBoardSets(t *testing.T, stored map[string]board.Theme) {
	t.Helper()
	withBoardRepo(t, relevo.RepoSettings{}, stored)
}

// withBoardRepo points the seam at both halves: the repository's settings and
// the palette set the lookup resolves against.
func withBoardRepo(t *testing.T, repo relevo.RepoSettings, stored map[string]board.Theme) {
	t.Helper()
	orig := boardThemeSets
	boardThemeSets = func(string) (relevo.RepoSettings, map[string]board.Theme, error) {
		return repo, stored, nil
	}
	t.Cleanup(func() { boardThemeSets = orig })
}

// TestBoardThemePrecedence pins the tiers as a pure rule, then how the verb
// feeds them: --theme, then the repository's board_theme, then the repo-local
// git config key, then the default name. The rule is pure on purpose: no test
// here spawns a harness or reaches the network.
func TestBoardThemePrecedence(t *testing.T) {
	// The pure rule, tier by tier.
	if got := boardThemeTier("flag", "repo", "git"); got != "flag" {
		t.Errorf("--theme tier: %s, want flag", got)
	}
	if got := boardThemeTier("", "repo", "git"); got != "repo" {
		t.Errorf("repo tier: %s, want repo (swapping repo and git here would invert which setting wins)", got)
	}
	if got := boardThemeTier("", "", "git"); got != "git" {
		t.Errorf("git tier: %s, want git", got)
	}
	if got := boardThemeTier("", "", ""); got != theme.DefaultName {
		t.Errorf("default tier: %s, want %s", got, theme.DefaultName)
	}

	// The verb feeds the same tiers from the real seams.
	withBoardRepo(t, relevo.RepoSettings{BoardTheme: "blueprint"}, theme.Seed())
	if th, err := boardResolveTheme("", ""); err != nil || th.Name != "blueprint" {
		t.Errorf("repo board_theme = %v, %v; want blueprint", th, err)
	}

	orig := boardGitConfig
	boardGitConfig = func(string, string) (string, error) { return "blueprint", nil }
	withBoardRepo(t, relevo.RepoSettings{}, theme.Seed())
	if th, err := boardResolveTheme("", ""); err != nil || th.Name != "blueprint" {
		t.Errorf("git tier = %v, %v; want blueprint", th, err)
	}
	if th, err := boardResolveTheme("", "cockpit"); err != nil || th.Name != "cockpit" {
		t.Errorf("--theme cockpit = %v, %v; want cockpit", th, err)
	}
	boardGitConfig = orig

	withBoardSets(t, theme.Seed())
	if th, err := boardResolveTheme("", ""); err != nil || th.Name != "cockpit" {
		t.Errorf("default = %v, %v; want cockpit", th, err)
	}
}

// TestBoardRefusesAnUnknownThemeNamingStoredThemes: a name no stored palette
// carries is a usage refusal naming the set it consulted, stored and seed
// alike, and saying how to add one.
func TestBoardRefusesAnUnknownThemeNamingStoredThemes(t *testing.T) {
	withTempRepoRoot(t)
	stored := map[string]board.Theme{theme.DefaultName: theme.Seed()[theme.DefaultName], "night": nightPalette()}
	withBoardSets(t, stored)

	// A stored name resolves to its own palette, not the seed's.
	if th, err := boardResolveTheme("", "night"); err != nil || th.Name != "night" || th.Accent != "#8be9fd" {
		t.Errorf("--theme night = %v, %v; want the stored night palette", th, err)
	}

	if _, err := boardResolveTheme("", "nope"); err == nil {
		t.Fatal("an unknown --theme resolved")
	} else {
		for _, want := range []string{"cockpit", "night", `relevo config set themes.nope '{...}'`} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal %q does not carry %q", err, want)
			}
		}
	}

	// The same rule reaches the verb as a usage exit naming the seed's names,
	// the set a machine without a stored section consults.
	withBoardSets(t, theme.Seed())
	ce := requireCLIError(t, cmdBoard([]string{"--theme", "nope"}), codeUsage, "relevo help")
	if !strings.Contains(ce.message, "cockpit") || !strings.Contains(ce.message, "blueprint") {
		t.Errorf("verb refusal %q does not name the consulted set", ce.message)
	}
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
	requireCLIError(t, err, codeConfigPathNotSet, "")

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

	if _, err := boardResolveTheme(".", ""); err != nil {
		t.Fatalf("boardResolveTheme over an empty state: %v", err)
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

// seamlessBoard points the resolution at an empty repository setting and the
// seed file's palettes, so the tests that ride it never read the machine's
// own state. The git seam stays as the pointer it is: the tests that tier
// through `relevo.boardTheme` point it themselves.
func seamlessBoard(t *testing.T) {
	t.Helper()
	withBoardRepo(t, relevo.RepoSettings{}, theme.Seed())
}
