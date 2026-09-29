package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/fuad-daoud/relevo/internal/harness"
)

// opencodeServicePaths are where opencode 2.x's service records its
// listening address: the state dir on 2.0.14, the config dir as fallback.
const (
	opencodeStateServicePath  = ".local/state/opencode/service.json"
	opencodeConfigServicePath = ".config/opencode/service.json"
)

var opencodeServicePaths = []string{opencodeStateServicePath, opencodeConfigServicePath}

// opencodeDBPath is opencode's own SQLite store, read through sqlite3 (never
// database/sql) so the session count needs no network call and no credential.
const opencodeDBPath = ".local/share/opencode/opencode.db"

// opencodeServiceCheck reports opencode 2.x's shared-service model: a killed
// or switched-away client leaves its agent session running inside the one
// `opencode serve --service` per user. Informational (always SevOK), and
// appears only when service.json exists.
func opencodeServiceCheck(ctx context.Context, env Env) Check {
	var usedRelPath string
	for _, rel := range opencodeServicePaths {
		svcPath, err := env.HomePath(rel)
		if err != nil || env.Stat(svcPath) != nil {
			continue
		}
		b, err := env.ReadFile(svcPath)
		if err != nil {
			continue
		}
		var record struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(b, &record); err != nil || record.URL == "" {
			continue
		}
		usedRelPath = rel
		break
	}
	if usedRelPath == "" {
		return Check{} // caller skips a zero-value row (Name == "")
	}

	detail := fmt.Sprintf("2.x shared service (~/%s); a killed or switched-away client leaves its session running inside the service", usedRelPath)
	if dbPath, derr := env.HomePath(opencodeDBPath); derr == nil && env.Stat(dbPath) == nil {
		if n, ok := opencodeSessionCount(ctx, env, dbPath); ok {
			detail = fmt.Sprintf("%s; %d session(s) recorded in opencode.db", detail, n)
		}
	}
	return Check{Group: "opencode", Name: "service", Severity: SevOK, Detail: detail}
}

// opencodeSessionCount tries session_v2 (2.0.14) then legacy session (pre-2.0).
func opencodeSessionCount(ctx context.Context, env Env, dbPath string) (int, bool) {
	for _, query := range []string{
		"select count(*) from session_v2",
		"select count(*) from session",
	} {
		out, err := env.Command(ctx, "sqlite3", "-readonly", dbPath, query)
		if err != nil {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(out)))
		if err != nil {
			continue
		}
		return n, true
	}
	return 0, false
}

// opencodeAllowlistCheck reports whether opencode's own config lets a
// headless builder read the plan relevo stages under stateRoot: it refuses
// that read unless permission.external_directory allows the directory, and
// does not expand ~ or $HOME. Every failure mode is a SevWarn row carrying a
// Fix.
func opencodeAllowlistCheck(env Env, stateRoot string) Check {
	var path string // the first readable candidate wins
	var body []byte
	for _, rel := range []string{".config/opencode/opencode.jsonc", ".config/opencode/opencode.json"} {
		p, err := env.HomePath(rel)
		if err != nil || env.Stat(p) != nil {
			continue
		}
		b, err := env.ReadFile(p)
		if err != nil {
			continue
		}
		path, body = p, b
		break
	}

	warn := func(detail string) Check {
		return Check{Group: "opencode", Name: "external_directory", Severity: SevWarn, Detail: detail, Fix: snippet(stateRoot)}
	}

	if path == "" {
		return warn("no ~/.config/opencode/opencode.jsonc; headless opencode builders auto-reject reading their plan")
	}

	var cfg map[string]any
	if err := json.Unmarshal(stripJSONC(body), &cfg); err != nil {
		return warn(fmt.Sprintf("could not parse %s: %v", path, err))
	}

	permission, _ := cfg["permission"].(map[string]any) // "ask"/"deny" leave the read blocked
	external, _ := permission["external_directory"].(map[string]any)
	for pattern, raw := range external {
		if value, _ := raw.(string); value != "allow" {
			continue
		}
		if patternCovers(pattern, stateRoot) {
			return Check{Group: "opencode", Name: "external_directory", Severity: SevOK, Detail: fmt.Sprintf("%s allows %s/**", path, stateRoot)}
		}
	}

	return warn(fmt.Sprintf("%s has no permission.external_directory allow entry for %s/**; headless opencode builders auto-reject reading their plan", path, stateRoot))
}

// patternCovers reports whether an allow entry opens stateRoot: the root
// itself, with /* or /**, or a glob whose prefix is a parent of the root.
func patternCovers(pattern, stateRoot string) bool {
	if pattern == stateRoot || pattern == stateRoot+"/*" || pattern == stateRoot+"/**" {
		return true
	}
	i := strings.Index(pattern, "*")
	if i <= 0 {
		return false
	}
	prefix := pattern[:i]
	if len(stateRoot) <= len(prefix) || !strings.HasPrefix(stateRoot, prefix) {
		return false
	}
	return strings.HasSuffix(prefix, "/") || stateRoot[len(prefix)] == '/'
}

// snippet is the allowlist block with stateRoot substituted, pasteable
// straight into the user's opencode config.
func snippet(stateRoot string) string {
	return "add to ~/.config/opencode/opencode.jsonc:\n" +
		`"permission": {
  "external_directory": {
    "` + stateRoot + `/*": "allow",
    "` + stateRoot + `/**": "allow"
  }
}`
}

// stripJSONC removes // and /* */ comments outside string literals, and a
// trailing comma before } or ], so opencode's config parses with encoding/json.
func stripJSONC(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); {
		switch {
		case b[i] == '"':
			out, i = copyStringLiteral(b, i, out)
		case b[i] == '/' && i+1 < len(b) && b[i+1] == '/':
			i = skipLineComment(b, i)
		case b[i] == '/' && i+1 < len(b) && b[i+1] == '*':
			i = skipBlockComment(b, i)
		case b[i] == '}' || b[i] == ']':
			out = append(trimTrailingComma(out), b[i])
			i++
		default:
			out = append(out, b[i])
			i++
		}
	}
	return out
}

// copyStringLiteral copies a string literal verbatim from its opening quote.
func copyStringLiteral(b []byte, i int, out []byte) ([]byte, int) {
	out = append(out, b[i])
	i++
	for i < len(b) {
		ch := b[i]
		if ch == '\\' && i+1 < len(b) {
			out = append(out, ch, b[i+1])
			i += 2
			continue
		}
		out = append(out, ch)
		i++
		if ch == '"' {
			break
		}
	}
	return out, i
}

func skipLineComment(b []byte, i int) int {
	for i < len(b) && b[i] != '\n' {
		i++
	}
	return i
}

func skipBlockComment(b []byte, i int) int {
	i += 2
	for i+1 < len(b) && (b[i] != '*' || b[i+1] != '/') {
		i++
	}
	if i+1 < len(b) {
		return i + 2
	}
	return len(b)
}

// trimTrailingComma drops a comma before a closing brace or bracket: not JSON.
func trimTrailingComma(out []byte) []byte {
	k := len(out)
	for k > 0 && jsoncSpace(out[k-1]) {
		k--
	}
	if k > 0 && out[k-1] == ',' {
		return out[:k-1]
	}
	return out
}

func jsoncSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

const opencodePluginDirPath = ".config/opencode/plugins/relevo"

// opencodePluginCheck reports whether relevo's OpenCode plugin is installed:
// none present is a quiet OK (opt-in), all byte-equal is installed, else a
// warning naming each problem file.
func opencodePluginCheck(env Env) Check {
	h, ok := harness.Lookup("opencode")
	if !ok {
		return Check{}
	}

	present := 0
	var problems []string
	for _, f := range h.Files {
		full, err := env.HomePath(f.Path)
		if err != nil || env.Stat(full) != nil {
			problems = append(problems, "~/"+f.Path+" is missing")
			continue
		}
		present++
		shipped, err := harness.ShippedFileBytes("opencode", f.Name)
		if err != nil {
			problems = append(problems, "~/"+f.Path+" has no shipped copy")
			continue
		}
		b, err := env.ReadFile(full)
		if err != nil {
			problems = append(problems, "~/"+f.Path+" is unreadable")
			continue
		}
		if !harness.DocEqual(shipped, b) {
			problems = append(problems, "~/"+f.Path+" differs from the shipped copy")
		}
	}

	switch {
	case present == 0:
		return Check{Group: "opencode", Name: "plugin", Severity: SevOK, Detail: "not installed -- relevo config agents installs the OpenCode plugin"}
	case len(problems) == 0:
		return Check{Group: "opencode", Name: "plugin", Severity: SevOK, Detail: "installed (~/" + opencodePluginDirPath + ")"}
	default:
		return Check{Group: "opencode", Name: "plugin", Severity: SevWarn, Detail: strings.Join(problems, "; "), Fix: "relevo config agents (add --force to replace your edits)"}
	}
}

// opencodeReservedKeys are the plugin's bound keys, in both spellings a user
// may write.
var opencodeReservedKeys = map[string]bool{
	"<leader>o": true,
	"<leader>j": true,
	"ctrl+x o":  true,
	"ctrl+x j":  true,
}

// opencodePluginKeysCheck reports whether another command binds a key the
// relevo plugin uses. No row unless the plugin is installed.
func opencodePluginKeysCheck(env Env) Check {
	if !opencodePluginInstalled(env) {
		return Check{}
	}

	for _, rel := range []string{
		".config/opencode/opencode.jsonc",
		".config/opencode/opencode.json",
		".config/opencode/cli.json",
	} {
		full, err := env.HomePath(rel)
		if err != nil || env.Stat(full) != nil {
			continue
		}
		body, err := env.ReadFile(full)
		if err != nil {
			continue
		}
		var cfg map[string]any
		if err := json.Unmarshal(stripJSONC(body), &cfg); err != nil {
			continue
		}
		if c, found := opencodeKeyClash(cfg, rel); found {
			return c
		}
	}

	return Check{Group: "opencode", Name: "plugin keys", Severity: SevOK, Detail: "ctrl+x o and ctrl+x j are free"}
}

func opencodeKeyClash(cfg map[string]any, rel string) (Check, bool) {
	keybinds, _ := cfg["keybinds"].(map[string]any)
	commands := make([]string, 0, len(keybinds))
	for command := range keybinds {
		commands = append(commands, command)
	}
	sort.Strings(commands)
	for _, command := range commands {
		if strings.HasPrefix(command, "relevo.") {
			continue
		}
		for _, key := range opencodeKeyList(keybinds[command]) {
			if opencodeReservedKeys[key] {
				return Check{
					Group: "opencode", Name: "plugin keys", Severity: SevWarn,
					Detail: fmt.Sprintf("~/%s: %s is bound to %s, which the relevo plugin uses", rel, command, key),
				}, true
			}
		}
	}
	return Check{}, false
}

func opencodePluginInstalled(env Env) bool {
	h, ok := harness.Lookup("opencode")
	if !ok {
		return false
	}
	for _, f := range h.Files {
		if full, err := env.HomePath(f.Path); err == nil && env.Stat(full) == nil {
			return true
		}
	}
	return false
}

// opencodeKeyList reads a keybinds value: one key string or an array of them.
func opencodeKeyList(raw any) []string {
	switch v := raw.(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
