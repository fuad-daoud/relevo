package doctor

import "testing"

func loginEnv(bins ...string) *fakeEnv {
	e := &fakeEnv{homeDir: "/h", lookPaths: map[string]string{}, existingFiles: map[string]bool{}}
	for _, b := range bins {
		e.lookPaths[b] = "/bin/" + b
	}
	return e
}

func TestLoginChecksPresentMissingAndNotChecked(t *testing.T) {
	env := loginEnv("claude", "codex", "opencode", "agy", "other")
	env.existingFiles["/h/.claude/.credentials.json"] = true
	env.existingFiles["/h/.local/share/opencode/opencode.db"] = true
	got := LoginChecks(env, []string{"claude", "codex", "opencode", "agy", "other"}, nil)
	want := map[string]Severity{"claude": SevOK, "codex": SevWarn, "opencode": SevOK, "agy": SevWarn, "other": SevOK}
	if len(got) != len(want) {
		t.Fatalf("rows = %d, want %d: %+v", len(got), len(want), got)
	}
	for _, c := range got {
		if c.Name != "login" || c.Severity != want[c.Group] {
			t.Errorf("%s: %+v, want severity %v", c.Group, c, want[c.Group])
		}
		if c.Severity == SevWarn && c.Fix == "" {
			t.Errorf("%s: a missing login names no command", c.Group)
		}
		if c.Severity == SevFail {
			t.Errorf("%s: login rows never fail", c.Group)
		}
	}
}

func TestLoginChecksSkipKindWithoutBinary(t *testing.T) {
	env := loginEnv("claude")
	got := LoginChecks(env, []string{"claude", "codex"}, nil)
	if len(got) != 1 || got[0].Group != "claude" {
		t.Fatalf("rows = %+v, want only claude's", got)
	}
}

func TestLoginChecksUseAccountHomes(t *testing.T) {
	env := loginEnv("codex")
	env.existingFiles["/acct/b/auth.json"] = true
	got := LoginChecks(env, []string{"codex"}, map[string][]string{"codex": {"/acct/a", "/acct/b"}})
	if len(got) != 2 || got[0].Severity != SevWarn || got[1].Severity != SevOK {
		t.Fatalf("rows = %+v, want a warn for /acct/a then ok for /acct/b", got)
	}
}
