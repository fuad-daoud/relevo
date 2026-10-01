package harness

import (
	"strings"

	"github.com/fuad-daoud/relevo/internal/account"
)

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

// HomeEnv names the environment variable that points kind at its per-process
// home: CLAUDE_CONFIG_DIR for claude, CODEX_HOME for codex. ok is false for a
// kind with no per-process home -- opencode keeps one active credential per
// install, and agy offers no home selector -- which is exactly the set of kinds
// an account cannot be pinned for.
func HomeEnv(kind string) (string, bool) {
	switch kind {
	case "claude":
		return "CLAUDE_CONFIG_DIR", true
	case "codex":
		return "CODEX_HOME", true
	}
	return "", false
}

// AccountHome is the directory account pins its harness to: config_dir for a
// claude account, home for a codex account. ok is false for a kind with no
// per-process home (opencode) and for an account whose selector is empty.
func AccountHome(a account.Account) (string, bool) {
	switch a.Harness {
	case account.Claude:
		return a.ConfigDir, a.ConfigDir != ""
	case account.Codex:
		return a.Home, a.Home != ""
	}
	return "", false
}

// AccountDefinitionPath is DefinitionPath expressed relative to kind's
// per-process home rather than to $HOME, because the home variable replaces the
// ~/.claude or ~/.codex component: a claude account reads its agents under
// $CLAUDE_CONFIG_DIR/agents, a codex account its profiles under $CODEX_HOME. ok
// is false for a kind with no per-process home and for a name DefinitionPath
// does not know.
func AccountDefinitionPath(kind, name string) (string, bool) {
	if _, ok := HomeEnv(kind); !ok {
		return "", false
	}
	rel, ok := DefinitionPath(kind, name)
	if !ok {
		return "", false
	}
	dir := ".claude/"
	if kind == "codex" {
		dir = ".codex/"
	}
	return strings.TrimPrefix(rel, dir), true
}
