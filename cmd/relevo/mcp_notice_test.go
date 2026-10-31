package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// TestMCPNotice pins the rule: the notice appears only when the daemon runs
// a different version and refused nothing. "Different" means
// `info.Version != own`, in either direction.
func TestMCPNotice(t *testing.T) {
	refused := store.DaemonInfo{
		Version:      "v0.8.0",
		ReexecFailed: &store.ReexecFailure{Reason: "policy.json: unknown field"},
	}

	tests := []struct {
		name string
		own  string
		info store.DaemonInfo
		ok   bool
		want string
	}{
		{
			name: "no record is no notice",
			own:  "v0.7.0",
			ok:   false,
		},
		{
			name: "the same version is no notice",
			own:  "v0.7.0",
			info: store.DaemonInfo{Version: "v0.7.0"},
			ok:   true,
		},
		{
			name: "a version difference either way is the notice",
			own:  "v0.8.0",
			info: store.DaemonInfo{Version: "v0.7.0"},
			ok:   true,
			want: "note: relevo was upgraded to v0.7.0; this session's relevo MCP server is still v0.8.0. Reconnect it (/mcp) or restart the session to load the new version.",
		},
		{
			name: "a newer daemon is the notice",
			own:  "v0.7.0",
			info: store.DaemonInfo{Version: "v0.8.0"},
			ok:   true,
			want: "note: relevo was upgraded to v0.8.0; this session's relevo MCP server is still v0.7.0. Reconnect it (/mcp) or restart the session to load the new version.",
		},
		{
			name: "a refused binary is no notice",
			own:  "v0.7.0",
			info: refused,
			ok:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mcpNotice(tc.own, tc.info, tc.ok)
			if got != tc.want {
				t.Errorf("mcpNotice(%q, %+v, %v) = %q, want %q", tc.own, tc.info, tc.ok, got, tc.want)
			}
		})
	}
}

// TestMCPNoticeTextIsTheSpecText pins the exact wording, including the version
// order (daemon first), so a reworded notice is a visible change.
func TestMCPNoticeTextIsTheSpecText(t *testing.T) {
	got := mcpNotice("v1.0.0", store.DaemonInfo{Version: "v1.1.0"}, true)
	for _, want := range []string{"v1.1.0", "v1.0.0", "(/mcp)"} {
		if !strings.Contains(got, want) {
			t.Errorf("notice %q does not contain %q", got, want)
		}
	}
	if strings.Index(got, "v1.1.0") > strings.Index(got, "v1.0.0") {
		t.Errorf("notice %q must name the daemon's version first", got)
	}
}

// TestCachedString pins relevo mcp's 30 s cache: f runs once, its answer is
// served until the deadline, and the deadline itself recomputes.
func TestCachedString(t *testing.T) {
	base := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	now := base
	calls := 0
	cached := cachedString(30*time.Second, func() time.Time { return now }, func() string {
		calls++
		return fmt.Sprintf("answer %d", calls)
	})

	if got := cached(); got != "answer 1" {
		t.Fatalf("first call = %q, want %q", got, "answer 1")
	}

	now = base.Add(29 * time.Second)
	if got := cached(); got != "answer 1" {
		t.Errorf("call inside the ttl = %q, want the cached %q", got, "answer 1")
	}
	if calls != 1 {
		t.Errorf("f calls = %d, want 1 inside the ttl", calls)
	}

	now = base.Add(30 * time.Second)
	if got := cached(); got != "answer 2" {
		t.Errorf("call at the deadline = %q, want a fresh %q", got, "answer 2")
	}
	if calls != 2 {
		t.Errorf("f calls = %d, want 2 at the deadline", calls)
	}
}
