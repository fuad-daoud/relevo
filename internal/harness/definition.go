package harness

// IsShipped reports whether name is one of the definitions kind ships; an
// unknown kind gives false.
func IsShipped(kind, name string) bool {
	h, ok := Lookup(kind)
	if !ok {
		return false
	}
	_, ok = h.Role(name)
	return ok
}

// DefinitionPath returns the home-relative path of the agent definition name
// for kind, and false for an unknown kind. A shipped name returns its table
// Path; any other name follows the per-kind convention route.
func DefinitionPath(kind, name string) (string, bool) {
	h, ok := Lookup(kind)
	if !ok {
		return "", false
	}
	if r, ok := h.Role(name); ok {
		return r.Path, true
	}
	switch kind {
	case "claude":
		return ".claude/agents/" + name + ".md", true
	case "opencode":
		return ".config/opencode/agents/" + name + ".md", true
	case "agy":
		return ".gemini/config/agents/" + name + ".md", true
	case "codex":
		return ".codex/" + name + ".config.toml", true
	}
	return "", false
}
