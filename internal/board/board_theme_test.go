package board

import (
	"errors"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/board/theme"
)

// The board's theme tiers: the flag, the repository's own setting, the
// repo-local git key, the stored section, and the seed file behind them.

// themesNight is the palette the resolution tests store, as the section
// carries it: whole, valid, and distinct in every colour from the seed's two.
const themesNight = `{"bg":"#0d1b1e","ink":"#dfe6e8","muted":"#7f9599","faint":"#475d61","line":"#284044","panel":"#132428","accent":"#8be9fd","good":"#6ff2a8","warn":"#ffd166","bad":"#ff6f8e","comment":"#c9a0ff"}`

// nightPalette is that palette as one stored entry.
func nightPalette() Theme {
	out, err := theme.Parse("night", []byte(themesNight))
	if err != nil {
		panic(err)
	}
	return out
}

// withBoardSets points the stored-set dependency at the given palette set for
// one test, so the tiers are pinned without a machine state. The seed file is
// the usual set a test without a database resolves against.
func withBoardSets(t *testing.T, deps *Deps, stored map[string]Theme) {
	t.Helper()
	withBoardRepo(t, deps, "", stored)
}

// withBoardRepo points the dependency at both halves: the repository's
// settings and the palette set the lookup resolves against.
func withBoardRepo(t *testing.T, deps *Deps, repoTheme string, stored map[string]Theme) {
	t.Helper()
	deps.ThemeSets = func(string) (string, map[string]Theme, error) {
		return repoTheme, stored, nil
	}
}

// TestBoardThemePrecedence pins the tiers as a pure rule, then how the verb
// feeds them: --theme, then the repository's board_theme, then the repo-local
// git config key, then the default name. The rule is pure on purpose: no test
// here spawns a harness or reaches the network.
func TestBoardThemePrecedence(t *testing.T) {
	// The pure rule, tier by tier.
	if got := ThemeTier("flag", "repo", "git"); got != "flag" {
		t.Errorf("--theme tier: %s, want flag", got)
	}
	if got := ThemeTier("", "repo", "git"); got != "repo" {
		t.Errorf("repo tier: %s, want repo (swapping repo and git here would invert which setting wins)", got)
	}
	if got := ThemeTier("", "", "git"); got != "git" {
		t.Errorf("git tier: %s, want git", got)
	}
	if got := ThemeTier("", "", ""); got != theme.DefaultName {
		t.Errorf("default tier: %s, want %s", got, theme.DefaultName)
	}

	// The verb feeds the same tiers from the real dependencies.
	boardStateRoot(t)
	deps := testDeps(t)
	withBoardRepo(t, &deps, "blueprint", theme.Seed())
	if th, err := ResolveTheme(deps, "", ""); err != nil || th.Name != "blueprint" {
		t.Errorf("repo board_theme = %v, %v; want blueprint", th, err)
	}

	deps.GitConfig = func(string, string) (string, error) { return "blueprint", nil }
	withBoardRepo(t, &deps, "", theme.Seed())
	if th, err := ResolveTheme(deps, "", ""); err != nil || th.Name != "blueprint" {
		t.Errorf("git tier = %v, %v; want blueprint", th, err)
	}
	if th, err := ResolveTheme(deps, "", "cockpit"); err != nil || th.Name != "cockpit" {
		t.Errorf("--theme cockpit = %v, %v; want cockpit", th, err)
	}

	withBoardSets(t, &deps, theme.Seed())
	deps.GitConfig = func(string, string) (string, error) { return "", nil }
	if th, err := ResolveTheme(deps, "", ""); err != nil || th.Name != "cockpit" {
		t.Errorf("default = %v, %v; want cockpit", th, err)
	}
}

// TestBoardRefusesAnUnknownThemeNamingStoredThemes: a name no stored palette
// carries is a usage refusal naming the set it consulted, stored and seed
// alike, and saying how to add one.
func TestBoardRefusesAnUnknownThemeNamingStoredThemes(t *testing.T) {
	boardStateRoot(t)
	deps := testDeps(t)
	stored := map[string]Theme{theme.DefaultName: theme.Seed()[theme.DefaultName], "night": nightPalette()}
	withBoardSets(t, &deps, stored)

	// A stored name resolves to its own palette, not the seed's.
	if th, err := ResolveTheme(deps, "", "night"); err != nil || th.Name != "night" || th.Accent != "#8be9fd" {
		t.Errorf("--theme night = %v, %v; want the stored night palette", th, err)
	}

	if _, err := ResolveTheme(deps, "", "nope"); err == nil {
		t.Fatal("an unknown --theme resolved")
	} else {
		if !errors.Is(err, theme.ErrUsage) {
			t.Errorf("unknown --theme = %v, want the palette refusal", err)
		}
		for _, want := range []string{"cockpit", "night", `relevo config set themes.nope '{...}'`} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal %q does not carry %q", err, want)
			}
		}
	}

	// The same rule reaches the verb as a usage exit naming the seed's names,
	// the set a machine without a stored section consults.
	withBoardSets(t, &deps, theme.Seed())
	dir := withTempRepoRoot(t, &deps)
	err := Run(deps, dir, "", "nope", "", "", false, nil)
	if !errors.Is(err, theme.ErrUsage) {
		t.Fatalf("Run(--theme nope) = %v, want the palette refusal", err)
	}
	if !strings.Contains(err.Error(), "cockpit") || !strings.Contains(err.Error(), "blueprint") {
		t.Errorf("verb refusal %q does not name the consulted set", err)
	}
}
