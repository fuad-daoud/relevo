package harness

import "path/filepath"

// RoleChecker reports which of the given role definitions are missing from
// disk, so a candidate can be gated before it is spawned.
type RoleChecker interface {
	Missing(kind string, definitions []string) []string
}

// MissingDefinitions returns the home-relative path of every definition not
// readable on disk; each name resolves through DefinitionPath. An unknown kind
// returns nil.
func MissingDefinitions(env InstallEnv, kind string, definitions []string) []string {
	if _, ok := Lookup(kind); !ok {
		return nil
	}
	var missing []string
	for _, def := range definitions {
		rel, ok := DefinitionPath(kind, def)
		if !ok {
			continue
		}
		path, err := env.HomePath(rel)
		if err != nil {
			missing = append(missing, rel)
			continue
		}
		if _, err := env.ReadFile(path); err != nil {
			missing = append(missing, rel)
		}
	}
	return missing
}

// MissingDefinitionsIn roots MissingDefinitions at one account home (a
// per-process home variable: CLAUDE_CONFIG_DIR, CODEX_HOME), each definition
// resolving through AccountDefinitionPath. Returned paths keep DefinitionPath's
// $HOME-relative spelling, so a note renders as the default check does.
func MissingDefinitionsIn(env InstallEnv, home, kind string, definitions []string) []string {
	if _, ok := Lookup(kind); !ok {
		return nil
	}
	var missing []string
	for _, def := range definitions {
		rel, ok := DefinitionPath(kind, def)
		if !ok {
			continue
		}
		accountRel, ok := AccountDefinitionPath(kind, def)
		if !ok {
			continue
		}
		if _, err := env.ReadFile(filepath.Join(home, accountRel)); err != nil {
			missing = append(missing, rel)
		}
	}
	return missing
}

type osRoleChecker struct{ env InstallEnv }

// OSRoleChecker returns a RoleChecker backed by the OS filesystem and the
// user's real home directory.
func OSRoleChecker() RoleChecker {
	return osRoleChecker{OSInstallEnv()}
}

func (c osRoleChecker) Missing(kind string, definitions []string) []string {
	return MissingDefinitions(c.env, kind, definitions)
}

// MissingIn checks definitions against one account home rather than the
// default home; it satisfies availability's account-aware checker.
func (c osRoleChecker) MissingIn(home, kind string, definitions []string) []string {
	return MissingDefinitionsIn(c.env, home, kind, definitions)
}
