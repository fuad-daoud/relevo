package doctor

import (
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/harness"
)

// loginSpec is where one kind keeps the file whose presence means "logged in":
// home is relative to $HOME and file to the home. opencode's credentials sit in
// its own database, whose presence is all that is checked; it is never opened.
type loginSpec struct {
	home  string
	file  string
	login string
}

var loginSpecs = map[string]loginSpec{
	"claude":   {home: ".claude", file: ".credentials.json", login: "claude auth login"},
	"codex":    {home: ".codex", file: "auth.json", login: "codex login"},
	"opencode": {file: opencodeDBPath, login: "opencode auth login"},
	"agy":      {home: ".gemini", file: "oauth_creds.json", login: "agy"},
}

// LoginChecks is one row per harness kind and home saying whether that
// harness's login file is present on this machine. homes maps a kind to the
// per-account homes configured for it; a kind with none is checked at its
// default home. A kind whose binary is not on PATH already has a warning row
// and gets none here. Local file checks only: no network, no harness spawn.
func LoginChecks(env Env, kinds []string, homes map[string][]string) []Check {
	var checks []Check
	for _, kind := range kinds {
		if h, known := harness.Lookup(kind); known && h.Binary != "" {
			if _, err := env.LookPath(h.Binary); err != nil {
				continue
			}
		} else if _, err := env.LookPath(kind); err != nil {
			continue
		}
		spec, ok := loginSpecs[kind]
		if !ok {
			checks = append(checks, Check{Group: kind, Name: "login", Severity: SevOK, Detail: "not checked"})
			continue
		}
		for _, dir := range loginDirs(env, spec, homes[kind]) {
			checks = append(checks, loginRow(env, kind, spec, dir))
		}
	}
	return checks
}

// loginDirs is the directories to check: the account homes when there are any,
// else the kind's default home resolved under $HOME ("" for a kind whose login
// file is itself home-relative).
func loginDirs(env Env, spec loginSpec, accountHomes []string) []string {
	if len(accountHomes) > 0 {
		return accountHomes
	}
	if spec.home == "" {
		return []string{""}
	}
	p, err := env.HomePath(spec.home)
	if err != nil {
		return []string{""}
	}
	return []string{p}
}

func loginRow(env Env, kind string, spec loginSpec, dir string) Check {
	c := Check{Group: kind, Name: "login"}
	var path string
	var err error
	if dir == "" {
		path, err = env.HomePath(spec.file)
	} else {
		path = filepath.Join(dir, spec.file)
	}
	if err != nil {
		c.Severity = SevWarn
		c.Detail = "cannot locate the login file"
		c.ProbeFailed = true
		return c
	}
	if env.Stat(path) != nil {
		c.Severity = SevWarn
		c.Detail = "no login at " + path
		c.Fix = spec.login
		return c
	}
	c.Severity = SevOK
	c.Detail = "login at " + path
	return c
}
