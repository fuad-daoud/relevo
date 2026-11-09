package board

import (
	"errors"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/board/theme"
)

// themesNight is the palette the resolution tests store, distinct in every
// colour from the seed's two.
const themesNight = `{"bg":"#0d1b1e","ink":"#dfe6e8","muted":"#7f9599","faint":"#475d61","line":"#284044","panel":"#132428","accent":"#8be9fd","good":"#6ff2a8","warn":"#ffd166","bad":"#ff6f8e","comment":"#c9a0ff"}`

// nightPalette is that palette as one stored entry.
func nightPalette() theme.Theme {
	out, err := theme.Parse("night", []byte(themesNight))
	if err != nil {
		panic(err)
	}
	return out
}

// withBoardSets points the stored-set dependency at the given palette set for
// one test, so the tiers are pinned without a machine state.
func withBoardSets(t *testing.T, deps *Deps, stored map[string]theme.Theme) {
	t.Helper()
	withBoardRepo(t, deps, "", stored)
}

// withBoardRepo points the dependency at both halves: the repository's
// settings and the palette set the lookup resolves against.
func withBoardRepo(t *testing.T, deps *Deps, repoTheme string, stored map[string]theme.Theme) {
	t.Helper()
	deps.ThemeSets = func(string) (string, map[string]theme.Theme, error) {
		return repoTheme, stored, nil
	}
}

// TestResolveThemeNamePrecedence pins the tiers as a pure rule: the flag,
// the repository's own setting, the repo-local git key, then unthemed.
func TestResolveThemeNamePrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, flag, repo, git, want string
	}{
		{"flag wins over both", "flag", "repo", "git", "flag"},
		{"repo wins over git", "", "repo", "git", "repo"},
		{"git alone", "", "", "git", "git"},
		{"all empty is unthemed", "", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveThemeName(tc.flag, tc.repo, tc.git); got != tc.want {
				t.Errorf("ResolveThemeName(%q, %q, %q) = %q, want %q", tc.flag, tc.repo, tc.git, got, tc.want)
			}
		})
	}
}

// TestResolveThemeTiers pins how the verb feeds the pure rule: --theme, then
// the repository's board_theme, then the repo-local git key, then unthemed.
func TestResolveThemeTiers(t *testing.T) {
	boardStateRoot(t)
	deps := testDeps(t)
	withBoardRepo(t, &deps, "blueprint", theme.Seed())
	deps.GitConfig = func(string, string) (string, error) { return "cockpit", nil }
	if th, err := ResolveTheme(deps, "", ""); err != nil || th == nil || th.Name != "blueprint" {
		t.Errorf("repo board_theme = %v, %v; want blueprint", th, err)
	}

	withBoardRepo(t, &deps, "", theme.Seed())
	deps.GitConfig = func(string, string) (string, error) { return "blueprint", nil }
	if th, err := ResolveTheme(deps, "", ""); err != nil || th == nil || th.Name != "blueprint" {
		t.Errorf("git tier = %v, %v; want blueprint", th, err)
	}
	if th, err := ResolveTheme(deps, "", "cockpit"); err != nil || th == nil || th.Name != "cockpit" {
		t.Errorf("--theme cockpit = %v, %v; want cockpit", th, err)
	}

	withBoardSets(t, &deps, theme.Seed())
	deps.GitConfig = func(string, string) (string, error) { return "", nil }
	if th, err := ResolveTheme(deps, "", ""); err != nil || th != nil {
		t.Errorf("unthemed = %v, %v; want (nil, nil)", th, err)
	}
}

// TestResolveThemeSkipsGitConfigWhenTiered pins the short-circuit: a flag or
// a repo theme never reads the git key.
func TestResolveThemeSkipsGitConfigWhenTiered(t *testing.T) {
	for _, tc := range []struct {
		name, flag, repo string
	}{
		{"flag short-circuits git", "cockpit", ""},
		{"repo short-circuits git", "", "cockpit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := testDeps(t)
			withBoardRepo(t, &deps, tc.repo, theme.Seed())
			calls := 0
			deps.GitConfig = func(string, string) (string, error) {
				calls++
				return "blueprint", nil
			}
			th, err := ResolveTheme(deps, "", tc.flag)
			if err != nil || th == nil || th.Name != "cockpit" {
				t.Fatalf("ResolveTheme = %v, %v; want cockpit", th, err)
			}
			if calls != 0 {
				t.Errorf("GitConfig called %d times, want 0", calls)
			}
		})
	}
}

// TestResolveThemeRefusesUnknownThemeNamingStoredThemes pins that a name no
// stored palette carries is a usage refusal naming the set it consulted and
// saying how to add one, whatever tier named it.
func TestResolveThemeRefusesUnknownThemeNamingStoredThemes(t *testing.T) {
	stored := map[string]theme.Theme{
		"cockpit": theme.Seed()["cockpit"],
		"night":   nightPalette(),
	}
	for _, tc := range []struct {
		name, flag, repo, git string
	}{
		{"flag tier", "nope", "", ""},
		{"repo tier", "", "nope", ""},
		{"git tier", "", "", "nope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := testDeps(t)
			withBoardRepo(t, &deps, tc.repo, stored)
			deps.GitConfig = func(string, string) (string, error) { return tc.git, nil }
			_, err := ResolveTheme(deps, "", tc.flag)
			if err == nil {
				t.Fatal("an unknown theme resolved")
			}
			if !errors.Is(err, theme.ErrUsage) {
				t.Errorf("unknown theme = %v, want the palette refusal", err)
			}
			for _, want := range []string{"cockpit", "night", `relevo config set themes.nope '{...}'`} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal %q does not carry %q", err, want)
				}
			}
		})
	}
}

// TestResolveThemeRefusesAMalformedStoredPalette pins the injection guard: a
// stored palette holding no colour is a usage refusal naming the field.
func TestResolveThemeRefusesAMalformedStoredPalette(t *testing.T) {
	bad := nightPalette()
	bad.Accent = "red"
	deps := testDeps(t)
	withBoardRepo(t, &deps, "", map[string]theme.Theme{"night": bad})
	deps.GitConfig = func(string, string) (string, error) { return "", nil }
	_, err := ResolveTheme(deps, "", "night")
	if err == nil {
		t.Fatal("a malformed palette resolved")
	}
	if !errors.Is(err, theme.ErrUsage) {
		t.Errorf("malformed palette = %v, want the palette refusal", err)
	}
	for _, want := range []string{"night", "accent"} {
		if !strings.Contains(strings.ToLower(err.Error()), want) {
			t.Errorf("refusal %q does not name %q", err, want)
		}
	}
}

// TestResolveThemeReportsAStoredReadFailure pins the wrap: a stored-set read
// failure reaches the caller naming the read.
func TestResolveThemeReportsAStoredReadFailure(t *testing.T) {
	deps := testDeps(t)
	deps.ThemeSets = func(string) (string, map[string]theme.Theme, error) {
		return "", nil, errors.New("locked")
	}
	if _, err := ResolveTheme(deps, "", ""); err == nil || !strings.Contains(err.Error(), "read the board settings") {
		t.Errorf("stored read failure = %v, want it to name the read", err)
	}
}

// TestResolveThemeReportsAGitReadFailure pins the wrap: a git key read
// failure reaches the caller naming the key.
func TestResolveThemeReportsAGitReadFailure(t *testing.T) {
	deps := testDeps(t)
	withBoardSets(t, &deps, theme.Seed())
	deps.GitConfig = func(string, string) (string, error) { return "", errors.New("git down") }
	if _, err := ResolveTheme(deps, "", ""); err == nil || !strings.Contains(err.Error(), "read relevo.boardTheme") {
		t.Errorf("git read failure = %v, want it to name the key", err)
	}
}
