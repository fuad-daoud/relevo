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
	env := loginEnv("claude", "codex", "muse", "opencode", "agy", "other")
	env.existingFiles["/h/.claude/.credentials.json"] = true
	env.existingFiles["/h/.local/share/opencode/opencode.db"] = true
	got := LoginChecks(env, []string{"claude", "codex", "muse", "opencode", "agy", "other"}, nil)
	want := map[string]Severity{"claude": SevOK, "codex": SevWarn, "muse": SevWarn, "opencode": SevOK, "agy": SevWarn, "other": SevOK}
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

func TestLoginChecksMuseReadsItsConfigDirectory(t *testing.T) {
	env := loginEnv("muse")
	env.existingFiles["/h/.config/muse/auth.json"] = true
	got := LoginChecks(env, []string{"muse"}, nil)
	if len(got) != 1 || got[0].Severity != SevOK || got[0].Detail != "login at /h/.config/muse/auth.json" {
		t.Fatalf("rows = %+v, want ok at the muse auth file", got)
	}
	env.existingFiles["/h/.config/muse/auth.json"] = false
	got = LoginChecks(env, []string{"muse"}, nil)
	if got[0].Severity != SevWarn || got[0].Fix != "muse login" {
		t.Errorf("row = %+v, want a warn naming muse login", got[0])
	}
}

func TestLoginChecksMuseHonoursXDGConfigHome(t *testing.T) {
	t.Run("xdg file logs in", func(t *testing.T) {
		env := loginEnv("muse")
		env.getenv = map[string]string{"XDG_CONFIG_HOME": "/xdg"}
		env.existingFiles["/xdg/muse/auth.json"] = true
		got := LoginChecks(env, []string{"muse"}, nil)
		if len(got) != 1 || got[0].Severity != SevOK || got[0].Detail != "login at /xdg/muse/auth.json" {
			t.Fatalf("rows = %+v, want ok at the XDG muse auth file", got)
		}
	})
	t.Run("home file ignored while xdg is set", func(t *testing.T) {
		env := loginEnv("muse")
		env.getenv = map[string]string{"XDG_CONFIG_HOME": "/xdg"}
		env.existingFiles["/h/.config/muse/auth.json"] = true
		got := LoginChecks(env, []string{"muse"}, nil)
		if len(got) != 1 || got[0].Severity != SevWarn {
			t.Fatalf("rows = %+v, want one warn row", got)
		}
		if got[0].Detail != "no login at /xdg/muse/auth.json" || got[0].Fix != "muse login" {
			t.Errorf("row = %+v, want a warn at the XDG path naming muse login", got[0])
		}
	})
	t.Run("empty xdg falls back to the home default", func(t *testing.T) {
		env := loginEnv("muse")
		env.getenv = map[string]string{"XDG_CONFIG_HOME": ""}
		env.existingFiles["/h/.config/muse/auth.json"] = true
		got := LoginChecks(env, []string{"muse"}, nil)
		if len(got) != 1 || got[0].Severity != SevOK || got[0].Detail != "login at /h/.config/muse/auth.json" {
			t.Fatalf("rows = %+v, want ok at the home default", got)
		}
	})
}

func TestLoginChecksMuseAccountHomesWinOverXDG(t *testing.T) {
	env := loginEnv("muse")
	env.getenv = map[string]string{"XDG_CONFIG_HOME": "/xdg"}
	env.existingFiles["/acct/b/auth.json"] = true
	got := LoginChecks(env, []string{"muse"}, map[string][]string{"muse": {"/acct/a", "/acct/b"}})
	if len(got) != 2 || got[0].Severity != SevWarn || got[1].Severity != SevOK {
		t.Fatalf("rows = %+v, want a warn for /acct/a then ok for /acct/b", got)
	}
	if got[0].Detail != "no login at /acct/a/auth.json" || got[1].Detail != "login at /acct/b/auth.json" {
		t.Errorf("rows = %+v, want the account homes checked, not the XDG path", got)
	}
}

func TestLoginChecksMuseIgnoresMetaAPIKey(t *testing.T) {
	env := loginEnv("muse")
	env.getenv = map[string]string{"META_API_KEY": "secret"}
	got := LoginChecks(env, []string{"muse"}, nil)
	if len(got) != 1 || got[0].Severity != SevWarn || got[0].Fix != "muse login" {
		t.Fatalf("rows = %+v, want a warn: a key alone is not a login file", got)
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

func TestLoginChecksFollowTheHarnessHomeVariable(t *testing.T) {
	env := loginEnv("claude", "codex")
	env.existingFiles["/cfg/claude/.credentials.json"] = true
	getenv := func(name string) string {
		if name == "CLAUDE_CONFIG_DIR" {
			return "/cfg/claude"
		}
		return ""
	}
	homes := WithHomeEnv(map[string][]string{"codex": {"/acct/a"}}, getenv)
	got := LoginChecks(env, []string{"claude", "codex"}, homes)
	if len(got) != 2 || got[0].Severity != SevOK || got[0].Detail != "login at /cfg/claude/.credentials.json" {
		t.Fatalf("claude row = %+v, want ok at the CLAUDE_CONFIG_DIR credentials", got)
	}
	if got[1].Detail != "no login at /acct/a/auth.json" {
		t.Errorf("codex row = %+v, want the account home kept over the variable", got[1])
	}
}
