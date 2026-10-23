package relevo

import "testing"

func TestKeptReasonReadsNewAndOldServerMessages(t *testing.T) {
	t.Parallel()

	cases := []struct{ name, msg, want string }{
		{"new", "binding api kept: worktree /p/api kept (uncommitted changes)", "uncommitted changes"},
		{"process clause", "binding api kept: worktree /p/api kept (uncommitted changes); its builder process (pid 9) was stopped", "uncommitted changes"},
		{"old hints", "binding api kept: worktree /p/api kept (uncommitted changes); commit or discard the work, then unbind again, or `relevo unbind api --force` archives it anyway; on the server: relevo serve unbind --owner SHA256:abc api --force", "uncommitted changes"},
		{"parentheses", "binding api kept: worktree /p/api kept (dirty check failed: exit status 1 (fatal: x))", "dirty check failed: exit status 1 (fatal: x)"},
		{"no match", "  binding api kept ", "binding api kept"},
	}
	for _, c := range cases {
		if got := KeptReason(c.msg); got != c.want {
			t.Errorf("%s: KeptReason = %q, want %q", c.name, got, c.want)
		}
	}
}
