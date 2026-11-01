package doctor

import (
	"os"
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
	"muse":     {home: ".config/muse", file: "auth.json", login: "muse login"},
	"opencode": {file: opencodeDBPath, login: "opencode auth login"},
	"agy":      {home: ".gemini", file: "oauth_creds.json", login: "agy"},
}

// LoginChecks is one row per harness kind and home saying whether that
// harness's login file is present. A kind with no account home is checked
// at its default home; a kind whose binary is not on PATH gets no row
// here. Local file checks only: no network, no harness spawn.
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
		for _, dir := range loginDirs(env, kind, spec, homes[kind]) {
			checks = append(checks, loginRow(env, kind, spec, dir))
		}
	}
	return checks
}

// WithHomeEnv adds, for a kind with no account home, the directory its
// harness's home variable names (CLAUDE_CONFIG_DIR, CODEX_HOME) when that is
// set: the harness keeps its login there, not under $HOME.
func WithHomeEnv(homes map[string][]string, getenv func(string) string) map[string][]string {
	out := make(map[string][]string, len(homes)+len(loginSpecs))
	for kind, dirs := range homes {
		out[kind] = dirs
	}
	for kind := range loginSpecs {
		if len(out[kind]) > 0 {
			continue
		}
		if name, ok := harness.HomeEnv(kind); ok && getenv(name) != "" {
			out[kind] = []string{getenv(name)}
		}
	}
	return out
}

// getenvFor reads one variable from the env when it carries its own values,
// else from the process, keeping the login path injectable in tests.
func getenvFor(env Env) func(string) string {
	if g, ok := env.(interface{ Getenv(string) string }); ok {
		return g.Getenv
	}
	return os.Getenv
}

// loginDirs is the directories to check: the account homes when there are
// any, else the kind's default home. "" names a kind whose login file is
// itself home-relative; muse's default honours XDG_CONFIG_HOME the way
// setup's museAuthPath does.
func loginDirs(env Env, kind string, spec loginSpec, accountHomes []string) []string {
	if len(accountHomes) > 0 {
		return accountHomes
	}
	if spec.home == "" {
		return []string{""}
	}
	if kind == "muse" {
		if xdg := getenvFor(env)("XDG_CONFIG_HOME"); xdg != "" {
			return []string{filepath.Join(xdg, "muse")}
		}
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
