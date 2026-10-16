package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/config"
)

// The `config repo` verbs, driven through run(): the tests below spawn only
// `git` (init, add, commit) to build a real repository fixture, and reach no
// harness and no network -- every rule they pin is a pure function in
// internal/relevo or internal/config.

// gitRepoWithACommit is a temporary git repository with one root commit, so
// the verbs can name its settings by the identity the wire travels. The
// -c commit.gpgsign=false keeps a developer with signing enabled globally
// from routing this fixture through gpg.
func gitRepoWithACommit(t *testing.T) (dir string) {
	t.Helper()
	dir = t.TempDir()
	run := func(name string, arg ...string) {
		t.Helper()
		cmd := exec.Command(name, arg...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", name, arg, err, out)
		}
	}
	run("git", "init", "-q")
	run("git", "config", "user.name", "T")
	run("git", "config", "user.email", "t@e")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("git", "add", "file.txt")
	run("git", "commit", "-q", "-m", "init")
	return dir
}

// chdirRepo moves the test's working directory into dir for the caller.
func chdirRepo(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir(%s): %v", dir, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(old); err != nil {
			t.Fatalf("Chdir back: %v", err)
		}
	})
}

// TestConfigRepoSetShowUnsetRoundTrip pins the verbs' one write path: a set
// stores the entry under the section the identity keys -- never a different
// section or key -- a show of a real repository reads it back, and an unset
// of the last field drops the entry again.
func TestConfigRepoSetShowUnsetRoundTrip(t *testing.T) {
	initRoot(t)
	repo := gitRepoWithACommit(t)
	chdirRepo(t, repo)

	if _, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "repo", "set", "gate", "make check"})
	}); err != nil {
		t.Fatalf("config repo set: %v (stderr: %s)", err, stderr)
	}

	// The write is an ordinary config write: config get repos.<key> reaches it.
	stored := storedConfig(t)
	if len(stored.Repos) != 1 {
		t.Fatalf("stored entries = %+v, want exactly one", stored.Repos)
	}
	var entry config.RepoEntry
	for _, v := range stored.Repos {
		entry = v
	}
	if entry.Gate != "make check" {
		t.Errorf("stored gate = %q, want make check", entry.Gate)
	}

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "repo"})
	})
	if err != nil {
		t.Fatalf("config repo: %v (stderr: %s)", err, stderr)
	}
	if !strings.Contains(string(stdout), "gate make check") {
		t.Errorf("config repo output = %q, want it to read the stored gate back", stdout)
	}

	if _, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "repo", "unset", "gate"})
	}); err != nil {
		t.Fatalf("config repo unset: %v (stderr: %s)", err, stderr)
	}
	if entries := storedConfig(t).Repos; len(entries) != 0 {
		t.Errorf("stored entries after unset = %+v, want none: the entry emptied is dropped", entries)
	}
	stdout, _, err = captureOutput(t, func() error {
		return run([]string{"config", "repo"})
	})
	if err != nil {
		t.Fatalf("config repo after unset: %v", err)
	}
	if !strings.Contains(string(stdout), "no settings for this repository") {
		t.Errorf("config repo after unset = %q, want the no-settings line", stdout)
	}
}

// TestConfigRepoUnsetDropsAnEmptyEntry pins the dropped-empty rule at the CLI:
// keeping a record whose every field the unset emptied is the mutation this
// test catches.
func TestConfigRepoUnsetDropsAnEmptyEntry(t *testing.T) {
	initRoot(t)
	repo := gitRepoWithACommit(t)
	chdirRepo(t, repo)

	if _, _, err := captureOutput(t, func() error {
		return run([]string{"config", "repo", "set", "name", "my repo"})
	}); err != nil {
		t.Fatalf("set name: %v", err)
	}
	if _, _, err := captureOutput(t, func() error {
		return run([]string{"config", "repo", "unset", "name"})
	}); err != nil {
		t.Fatalf("unset name: %v", err)
	}
	if entries := storedConfig(t).Repos; len(entries) != 0 {
		t.Errorf("entries after unsetting the only field = %+v, want the entry gone", entries)
	}

	// The explicit empty answer names the whole surface.
	stdout, _, err := captureOutput(t, func() error {
		return run([]string{"config", "repo", "list"})
	})
	if err != nil {
		t.Fatalf("list after the drop: %v", err)
	}
	if !strings.Contains(string(stdout), "no repositories have settings") {
		t.Errorf("list = %q, want the empty answer", stdout)
	}
}

// TestConfigRepoJSONAndListJSON pins the two --json documents: show carries the
// repository's identity, its stored fields and never an absent field, and list
// hands back one row per stored repository in identity order. Omitting
// board_theme from the document is the mutation this test catches.
func TestConfigRepoJSONAndListJSON(t *testing.T) {
	initRoot(t)
	repo := gitRepoWithACommit(t)
	chdirRepo(t, repo)

	for _, s := range [][]string{
		{"config", "repo", "set", "name", "shop"},
		{"config", "repo", "set", "gate", "make check"},
		{"config", "repo", "set", "board_theme", "midnight"},
	} {
		if _, _, err := captureOutput(t, func() error { return run(s) }); err != nil {
			t.Fatalf("%v: %v", s, err)
		}
	}

	stdout, _, err := captureOutput(t, func() error {
		return run([]string{"config", "repo", "--json"})
	})
	if err != nil {
		t.Fatalf("show --json: %v", err)
	}
	var doc struct {
		Repo       string `json:"repo"`
		Name       string `json:"name"`
		Gate       string `json:"gate"`
		BoardTheme string `json:"board_theme"`
	}
	if err := json.Unmarshal(stdout, &doc); err != nil {
		t.Fatalf("show --json output %s: %v", stdout, err)
	}
	if doc.Repo == "" || doc.Name != "shop" || doc.Gate != "make check" {
		t.Fatalf("show --json = %+v, want the stored fields", doc)
	}
	if doc.BoardTheme != "midnight" {
		t.Errorf("board_theme = %q, want midnight", doc.BoardTheme)
	}

	stdout, _, err = captureOutput(t, func() error {
		return run([]string{"config", "repo", "list", "--json"})
	})
	if err != nil {
		t.Fatalf("list --json: %v", err)
	}
	var list struct {
		Repos []struct {
			Repo string `json:"repo"`
			Gate string `json:"gate"`
		} `json:"repos"`
	}
	if err := json.Unmarshal(stdout, &list); err != nil {
		t.Fatalf("list --json output %s: %v", stdout, err)
	}
	if len(list.Repos) != 1 || list.Repos[0].Gate != "make check" {
		t.Errorf("list --json = %+v, want one row reading the gate back", list.Repos)
	}
}

// TestConfigRepoRefusesAnUnknownKey pins the key vocabulary: a key outside
// name, gate and board_theme is a usage refusal that names the three.
// Accepting any key is the mutation this test catches.
func TestConfigRepoRefusesAnUnknownKey(t *testing.T) {
	initRoot(t)
	repo := gitRepoWithACommit(t)
	chdirRepo(t, repo)

	for _, s := range [][]string{
		{"config", "repo", "set", "typo", "x"},
		{"config", "repo", "unset", "typo"},
	} {
		err := run(s)
		ce := requireCLIError(t, err, codeUsage, "")
		for _, want := range []string{"name", "gate", "board_theme"} {
			if !strings.Contains(ce.message, want) {
				t.Errorf("%v: message = %q, want it to name %q", s, ce.message, want)
			}
		}
		var ec exitCodeErr
		if !errors.As(err, &ec) || ec.code != 2 {
			t.Errorf("%v: exit = %v, want exit code 2", s, err)
		}
	}
}

// TestConfigRepoRefusesOutsideARepository pins the other answer the same fact
// earns on this surface: a directory git cannot identity is a usage refusal,
// not "no settings" and not exit 0. Reading ErrNotRepo as "no settings" and
// printing the empty document is the mutation this test catches.
func TestConfigRepoRefusesOutsideARepository(t *testing.T) {
	initRoot(t)
	chdirRepo(t, t.TempDir()) // a directory that is not a repository

	err := run([]string{"config", "repo"})
	ce := requireCLIError(t, err, codeUsage, "")
	if !strings.Contains(ce.message, "not a git repository") {
		t.Errorf("message = %q, want the not-a-repository refusal", ce.message)
	}
	var ec exitCodeErr
	if !errors.As(err, &ec) || ec.code != 2 {
		t.Fatalf("exit = %v, want exit code 2", err)
	}
}
