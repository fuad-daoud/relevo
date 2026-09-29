package harness

// RoleChecker reports which of the given role definitions are missing from
// disk, so a candidate can be gated before it is spawned.
type RoleChecker interface {
	Missing(kind string, definitions []string) []string
}

// MissingDefinitions checks each of definitions against kind's definitions on
// disk through env, returning the home-relative path of every one that is not
// readable. Each name resolves through DefinitionPath; an unknown kind returns
// nil.
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

type osRoleChecker struct{ env InstallEnv }

// OSRoleChecker returns a RoleChecker backed by the OS filesystem and the
// user's real home directory.
func OSRoleChecker() RoleChecker {
	return osRoleChecker{OSInstallEnv()}
}

func (c osRoleChecker) Missing(kind string, definitions []string) []string {
	return MissingDefinitions(c.env, kind, definitions)
}
