package main

import "testing"

// TestSandboxBannerVerbCoversMutatingVerbs pins the banner predicate: a command
// that changes something inside a sandbox names it, a read-only one does not,
// and outside a sandbox nothing is ever printed.
func TestSandboxBannerVerbCoversMutatingVerbs(t *testing.T) {
	t.Setenv("RELEVO_SANDBOX", "demo")

	cases := map[string][]string{
		"config set":     {"config", "set", "candidates", "[]"},
		"config unset":   {"config", "unset", "candidates"},
		"config import":  {"config", "import", "-"},
		"config get":     {"config", "get", "candidates"},
		"bare config":    {"config"},
		"config export":  {"config", "export"},
		"config help":    {"config", "help"},
		"status":         {"status"},
		"status --line":  {"status", "--line"},
		"daemon --check": {"daemon", "--check"},
		"bugreport":      {"bugreport"},
		"show":           {"show"},
		"history":        {"history"},
		"doctor":         {"doctor"},
		"version":        {"version"},
		"help":           {"help"},
		"bind":           {"bind", "--actor", "builder"},
		"send":           {"send"},
		"db query":       {"db", "query", "select 1"},
	}
	bannered := map[string]bool{
		"config set":    true,
		"config unset":  true,
		"config import": true,
		"bind":          true,
		"send":          true,
	}
	for name, args := range cases {
		want := ""
		if bannered[name] {
			want = "demo"
		}
		if got := sandboxBannerVerb(args); got != want {
			t.Errorf("sandboxBannerVerb(%v) = %q, want %q", args, got, want)
		}
	}

	t.Setenv("RELEVO_SANDBOX", "")
	if got := sandboxBannerVerb([]string{"config", "set", "candidates", "[]"}); got != "" {
		t.Errorf("sandboxBannerVerb outside a sandbox = %q, want empty", got)
	}
}
