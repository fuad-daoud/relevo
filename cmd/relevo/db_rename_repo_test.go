package main

import "testing"

// TestDBRenameRepoUsage pins the refusals that happen before the database is
// opened: no subcommand runs against a live db here.
func TestDBRenameRepoUsage(t *testing.T) {
	for name, args := range map[string][]string{
		"no flags":     {"db", "rename-repo"},
		"missing to":   {"db", "rename-repo", "--from", "https://github.com/o/a"},
		"missing from": {"db", "rename-repo", "--to", "https://github.com/o/a"},
		"empty from":   {"db", "rename-repo", "--from", "", "--to", "https://github.com/o/a"},
		"equal":        {"db", "rename-repo", "--from", "https://github.com/o/a", "--to", "git@github.com:o/a.git"},
		"unknown flag": {"db", "rename-repo", "--bogus"},
		"positional":   {"db", "rename-repo", "--from", "a", "--to", "b", "extra"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := captureOutput(t, func() error { return run(args) })
			requireCLIError(t, err, codeUsage, "")
		})
	}
}
