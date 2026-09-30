package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/fuad-daoud/relevo/internal/account"
	"github.com/fuad-daoud/relevo/internal/doctor"
)

// opencodeDBRel is opencode's own SQLite store under $HOME, the same file
// internal/doctor reads for its session count.
const opencodeDBRel = ".local/share/opencode/opencode.db"

// accountChecks probes every configured account's home: the directory exists
// and holds a login, so a round that draws it can authenticate. It is
// read-only and best-effort, like every doctor row: a fact relevo cannot
// establish is a warning naming the account and the check that failed, never a
// crash. No accounts means no rows, so doctor's output is unchanged.
func accountChecks(ctx context.Context, accounts account.Set, env doctor.Env) []doctor.Check {
	if len(accounts) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, bindPreflightTimeout)
	defer cancel()

	checks := make([]doctor.Check, 0, len(accounts))
	for _, a := range accounts {
		checks = append(checks, accountCheck(ctx, a, env))
	}
	return checks
}

// accountCheck is one account's row: the login file for claude and codex, the
// credential row for opencode.
func accountCheck(ctx context.Context, a account.Account, env doctor.Env) doctor.Check {
	c := doctor.Check{Group: string(a.Harness), Name: "account"}
	switch a.Harness {
	case account.Claude:
		return loginFileCheck(c, a.Name, a.ConfigDir, ".credentials.json", env,
			"log in with CLAUDE_CONFIG_DIR="+a.ConfigDir)
	case account.Codex:
		return loginFileCheck(c, a.Name, a.Home, "auth.json", env,
			"log in with CODEX_HOME="+a.Home)
	case account.OpenCode:
		return opencodeAccountCheck(ctx, c, a, env)
	}
	c.Severity = doctor.SevWarn
	c.Detail = fmt.Sprintf("%s: unknown harness %q", a.Name, a.Harness)
	return c
}

// loginFileCheck is the per-process home check claude and codex share: the home
// directory exists and holds the named credential file. login is the command
// that authenticates a missing one.
func loginFileCheck(c doctor.Check, name, home, file string, env doctor.Env, login string) doctor.Check {
	if home == "" {
		c.Severity = doctor.SevWarn
		c.Detail = name + ": no home configured"
		return c
	}
	if err := env.Stat(home); err != nil {
		c.Severity = doctor.SevWarn
		c.Detail = fmt.Sprintf("%s: home %s does not exist", name, home)
		c.Fix = login
		return c
	}
	if err := env.Stat(filepath.Join(home, file)); err != nil {
		c.Severity = doctor.SevWarn
		c.Detail = fmt.Sprintf("%s: no login in %s", name, home)
		c.Fix = login
		return c
	}
	c.Severity = doctor.SevOK
	c.Detail = fmt.Sprintf("%s: login in %s", name, home)
	return c
}

// opencodeAccountCheck reports whether the account's integration+label pair is
// a row in opencode's credential table, read the way the rest of doctor reads
// that database: sqlite3 -readonly, never database/sql and never a network
// call. A missing table or an unreadable database is a probe failure, so the
// row says it could not be established rather than guessing.
func opencodeAccountCheck(ctx context.Context, c doctor.Check, a account.Account, env doctor.Env) doctor.Check {
	dbPath, err := env.HomePath(opencodeDBRel)
	if err != nil {
		c.Severity = doctor.SevWarn
		c.Detail = fmt.Sprintf("%s: cannot locate %s", a.Name, opencodeDBRel)
		c.ProbeFailed = true
		return c
	}
	if env.Stat(dbPath) != nil {
		c.Severity = doctor.SevWarn
		c.Detail = fmt.Sprintf("%s: %s does not exist", a.Name, dbPath)
		c.Fix = "log in with opencode auth login"
		return c
	}

	query := "select count(*) from credential where integration_id = '" +
		sqlQuote(a.Integration) + "' and label = '" + sqlQuote(a.Label) + "'"
	out, err := env.Command(ctx, "sqlite3", "-readonly", dbPath, query)
	n, perr := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || perr != nil {
		c.Severity = doctor.SevWarn
		c.Detail = fmt.Sprintf("%s: could not read opencode credentials", a.Name)
		c.ProbeFailed = true
		return c
	}
	if n == 0 {
		c.Severity = doctor.SevWarn
		c.Detail = fmt.Sprintf("%s: no %s/%s credential row", a.Name, a.Integration, a.Label)
		c.Fix = "log in with opencode auth login"
		return c
	}
	c.Severity = doctor.SevOK
	c.Detail = fmt.Sprintf("%s: %s/%s credential row present", a.Name, a.Integration, a.Label)
	return c
}

// sqlQuote escapes a value for a single-quoted SQL literal: a quote inside
// doubles, which is the whole injection surface for the read-only query above.
func sqlQuote(s string) string { return strings.ReplaceAll(s, "'", "''") }
