package doctor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/release"
)

// The relevo Claude Code plugin's install facts. The user's files sit under
// Claude Code's config directory and the project's settings under the repo.
const (
	claudeSettingsRel     = ".claude/settings.json"
	claudePluginStateRel  = "plugins/installed_plugins.json"
	claudePluginName      = "relevo@relevo"
	claudeHooksRel        = "hooks/hooks.json"
	pluginHookInitCommand = "mastermind init"
	pluginHookInitScript  = "scripts/mastermind-init.sh"
	pluginHookEvent       = "SessionStart"
)

// ChildProcess is one entry of a host process's child table: the pid and its
// argv, as /proc/<pid>/task/*/children plus /proc/<pid>/cmdline report them.
type ChildProcess struct {
	PID  int
	Args []string
}

// HasMCPChild reports whether any of a host process's children is a `relevo
// mcp` process. Pure, so it is testable without a live process tree.
func HasMCPChild(children []ChildProcess) bool {
	for _, c := range children {
		if len(c.Args) == 0 {
			continue
		}
		if base := filepath.Base(c.Args[0]); base != "relevo" && base != "relevo.exe" {
			continue
		}
		for _, a := range c.Args[1:] {
			if a == "mcp" {
				return true
			}
		}
	}
	return false
}

// MasterMindCheckInput is everything the mastermind rows need that doctor.Run
// cannot read itself. cmd/relevo gathers it; the rules live here.
type MasterMindCheckInput struct {
	Claude bool   // a claude candidate or mastermind record exists; gates the plugin rows
	Home   string // $HOME, so a test can point it at a temp dir
	// ClaudeDir is CLAUDE_CONFIG_DIR, where Claude Code keeps its settings
	// and plugins when it is set; "" means Home/.claude.
	ClaudeDir string
	Repo      string // the repository `relevo doctor` runs in

	Detected  bool               // mastermind.Detect: relevo runs inside a Claude Code session
	Resolved  *mastermind.Record // the mastermind Resolve found; nil on a miss
	Chat      string             // the resolved mastermind's chatlabel (opencode only); "" when Resolved is nil
	ClaimLive bool               // a live push claim exists for Resolved
	MCPChild  bool               // a `relevo mcp` process is a child of the mastermind's host; false is FAIL

	Stale   []string // mastermind records seen over 7 days ago with no live binding
	Running string   // relevo's own version, compared with the installed plugin's; "" skips it

	// ConsentKnown is the repository consent read succeeded; false leaves the
	// row out. RepoKnown says the cwd resolved to a repository at all.
	ConsentKnown bool
	RepoKnown    bool
	Consent      mastermind.Consent

	// Muse says a muse candidate or mastermind record exists; it gates the
	// muse plugin rows. MuseSession mirrors Detected for a muse session, and
	// MuseInspect is the injected `muse plugins inspect relevo --json`
	// reader: cmd/relevo wires it to Env.Command, tests stub it. The rows
	// probe through it rather than the disk because muse's on-disk
	// runtime_capabilities stores only trusted definition hashes -- whether
	// they still match the installed package is muse's own computation.
	Muse        bool
	MuseSession bool
	MuseInspect MuseInspectFn
}

// MasterMindChecks reports the plugin rows for Claude Code and muse, the
// installed plugin's SessionStart hook, this session's mastermind, the
// repository's consent answer, and the stale-record note. Leaving every
// field zero reports nothing.
func MasterMindChecks(in MasterMindCheckInput) []Check {
	var checks []Check

	if in.ConsentKnown {
		checks = append(checks, consentCheck(in))
	}

	if in.Claude {
		dir := claudeDir(in)
		checks = append(checks, pluginEnabledCheck(dir, in.Repo))
		checks = append(checks, pluginHookCheck(dir))
		checks = append(checks, pluginVersionCheck(dir, in.Running))
	}

	if in.Muse {
		checks = append(checks, musePluginChecks(in)...)
	}

	if in.Detected || in.MuseSession {
		checks = append(checks, mastermindSessionCheck(in))
	}

	if len(in.Stale) > 0 {
		checks = append(checks, Check{
			Name:     "MasterMinds",
			Severity: SevInfo,
			Detail:   fmt.Sprintf("seen over 7 days ago and no live binding: %s", strings.Join(in.Stale, ", ")),
			Fix:      "relevo mastermind forget <id|name>",
		})
	}

	return checks
}

// consentCheck is the repository answer row: OK when sessions here
// register, INFO when they ask or stay out. The Fix field always names the
// command that changes the answer.
func consentCheck(in MasterMindCheckInput) Check {
	switch {
	case !in.RepoKnown:
		return Check{
			Name:     "consent",
			Severity: SevInfo,
			Detail:   "not in a git repository: relevo registers no session here",
			Fix:      "run relevo doctor inside the repository, or relevo mastermind init by hand",
		}
	case in.Consent == mastermind.ConsentYes:
		return Check{
			Name:     "consent",
			Severity: SevOK,
			Detail:   "yes: sessions in this repository register as MasterMinds",
			Fix:      "relevo mastermind disable --repo",
		}
	case in.Consent == mastermind.ConsentNo:
		return Check{
			Name:     "consent",
			Severity: SevInfo,
			Detail:   "no: relevo registers and briefs no session in this repository",
			Fix:      "relevo mastermind enable --repo",
		}
	default:
		return Check{
			Name:     "consent",
			Severity: SevInfo,
			Detail:   "unset: the next session asks whether to become this repository's MasterMind",
			Fix:      "relevo mastermind enable --repo (or disable --repo)",
		}
	}
}

// mastermindSessionCheck is the mastermind row for a detected Claude Code session:
// FAIL when Resolve missed or no relevo mcp child serves its tools, INFO while
// no push claim is live (reports arrive through the blocking wait tool), OK once
// the push claim is live.
func mastermindSessionCheck(in MasterMindCheckInput) Check {
	switch {
	case in.Resolved == nil:
		return Check{
			Name:     "MasterMind",
			Severity: SevFail,
			Detail:   "no relevo MasterMind resolved for this Claude Code session",
			Fix:      "relevo mastermind init (or enable the relevo plugin so its SessionStart hook runs)",
		}
	case !in.MCPChild:
		return Check{
			Name:     "MasterMind",
			Severity: SevFail,
			Detail:   fmt.Sprintf("MasterMind %s: no relevo mcp process is a child of its host process; reports never arrive", mastermindRef(in.Resolved, in.Chat)),
			Fix:      "enable the relevo plugin so relevo mcp starts with the session (relevo doctor)",
		}
	case !in.ClaimLive:
		return Check{
			Name:     "MasterMind",
			Severity: SevInfo,
			Detail:   fmt.Sprintf("MasterMind %s: tools mode: reports arrive through the blocking wait tool. For push, the session's mod runs `relevo push`", mastermindRef(in.Resolved, in.Chat)),
		}
	default:
		return Check{
			Name:     "MasterMind",
			Severity: SevOK,
			Detail:   fmt.Sprintf("%s; push claim live", mastermindRef(in.Resolved, in.Chat)),
		}
	}
}

// mastermindRef renders "name (id)", with the chat label appended when non-empty.
func mastermindRef(rec *mastermind.Record, chat string) string {
	if chat == "" {
		return rec.Name + " (" + rec.ID + ")"
	}
	return rec.Name + " (" + rec.ID + ") · " + chat
}

func claudeDir(in MasterMindCheckInput) string {
	if in.ClaudeDir != "" {
		return in.ClaudeDir
	}
	return filepath.Join(in.Home, ".claude")
}

// pluginEnabledCheck is the first plugin row: FAIL unless the user's or the
// repo's settings.json has enabledPlugins["relevo@relevo"] == true.
func pluginEnabledCheck(claudeDir, repo string) Check {
	userSettings := filepath.Join(claudeDir, "settings.json")
	for _, path := range []string{
		userSettings,
		filepath.Join(repo, claudeSettingsRel),
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if pluginEnabled(raw) {
			return Check{Name: "plugin", Severity: SevOK, Detail: "relevo@relevo enabled in " + path}
		}
	}
	if !RelevoPluginInstalledIn(claudeDir) {
		return Check{
			Name:     "plugin",
			Severity: SevFail,
			Detail:   "the relevo plugin is not installed in Claude Code",
			Fix:      PluginInstallCommand,
		}
	}
	return Check{
		Name:     "plugin",
		Severity: SevFail,
		Detail:   "relevo@relevo is not enabled in " + userSettings + " or the project's " + claudeSettingsRel,
		Fix:      "enable the relevo plugin in Claude Code (/plugin), or set enabledPlugins[\"relevo@relevo\"] = true in " + claudeSettingsRel,
	}
}

// PluginInstallCommand installs the plugin from this repository's marketplace;
// install enables it too.
const PluginInstallCommand = "claude plugin marketplace add fuad-daoud/relevo && claude plugin install relevo@relevo"

// RelevoPluginInstalledIn reports whether the installed_plugins.json under a
// Claude Code config directory lists a relevo plugin. setup passes
// CLAUDE_CONFIG_DIR when it is set, which Claude Code itself honours.
func RelevoPluginInstalledIn(claudeDir string) bool {
	raw, err := os.ReadFile(filepath.Join(claudeDir, claudePluginStateRel))
	if err != nil {
		return false
	}
	_, found, err := installedRelevoPlugin(raw)
	return err == nil && found
}

// pluginEnabled reports whether a settings.json enables relevo@relevo; an
// unparseable one reads as not enabled.
func pluginEnabled(raw []byte) bool {
	var s struct {
		EnabledPlugins map[string]json.RawMessage `json:"enabledPlugins"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return false
	}
	v, ok := s.EnabledPlugins[claudePluginName]
	return ok && bytes.Equal(bytes.TrimSpace(v), []byte("true"))
}

// pluginHookCheck is the second plugin row: FAIL when the installed plugin
// has no SessionStart hook running `relevo mastermind init`. Anything relevo
// cannot establish reads `not checked` (OK), never FAIL.
func pluginHookCheck(claudeDir string) Check {
	state := filepath.Join(claudeDir, claudePluginStateRel)
	raw, err := os.ReadFile(state)
	if err != nil {
		return Check{Name: "plugin hook", Severity: SevOK, Detail: "not checked (no " + state + ")"}
	}

	dirs := installedPluginDirs(raw)
	if len(dirs) == 0 {
		return Check{Name: "plugin hook", Severity: SevOK, Detail: "not checked (no installed plugin found in " + state + ")"}
	}

	checked := false
	for _, dir := range dirs {
		hooks, err := os.ReadFile(filepath.Join(dir, claudeHooksRel))
		if err != nil {
			continue
		}
		checked = true
		if hookRunsMasterMindInit(hooks) {
			return Check{Name: "plugin hook", Severity: SevOK, Detail: dir + ": " + pluginHookEvent + " runs relevo " + pluginHookInitCommand}
		}
	}
	if !checked {
		return Check{Name: "plugin hook", Severity: SevOK, Detail: "not checked (the installed plugin ships no " + claudeHooksRel + ")"}
	}
	return Check{
		Name:     "plugin hook",
		Severity: SevFail,
		Detail:   "the installed relevo plugin has no " + pluginHookEvent + " hook running relevo " + pluginHookInitCommand,
		Fix:      "reinstall the relevo plugin so its " + claudeHooksRel + " ships the " + pluginHookEvent + " hook",
	}
}

// pluginVersionCheck compares the installed relevo@* plugin's version with
// the running binary's. Advisory only: the MCP server runs the binary on
// PATH regardless. Every fact relevo cannot prove reads `not checked` (OK).
func pluginVersionCheck(claudeDir, running string) Check {
	const name = "plugin version"

	if running == "" {
		return Check{Name: name, Severity: SevOK, Detail: "not checked"}
	}

	state := filepath.Join(claudeDir, claudePluginStateRel)
	raw, err := os.ReadFile(state)
	if err != nil {
		return Check{Name: name, Severity: SevOK, Detail: "not checked (no readable " + state + ")"}
	}

	plugin, found, err := installedRelevoPlugin(raw)
	if err != nil {
		return Check{Name: name, Severity: SevOK, Detail: "not checked (no readable " + state + ")"}
	}
	if !found {
		return Check{Name: name, Severity: SevOK, Detail: "not checked (relevo plugin not installed)"}
	}

	pv, pok := release.ParseVersion(plugin)
	rv, rok := release.ParseVersion(running)
	if !pok || !rok {
		return Check{Name: name, Severity: SevOK, Detail: "not checked (relevo is " + running + ")"}
	}
	// Major.Minor.Patch only, so v0.8.0-15-gd664545 matches 0.8.0.
	if pv.Major == rv.Major && pv.Minor == rv.Minor && pv.Patch == rv.Patch {
		return Check{Name: name, Severity: SevOK, Detail: "plugin " + plugin + " matches relevo"}
	}
	return Check{
		Name:     name,
		Severity: SevWarn,
		Detail:   "plugin " + plugin + ", relevo " + running,
		Fix:      "claude plugin update relevo@relevo",
	}
}

// installedRelevoPlugin finds the relevo@* entry in an installed_plugins.json
// and returns its version.
func installedRelevoPlugin(raw []byte) (version string, found bool, err error) {
	// {"version":2,"plugins":{"relevo@relevo":[{"version":"0.8.0",...}]}}
	var state struct {
		Plugins map[string][]struct {
			Version string `json:"version"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return "", false, err
	}
	for key, entries := range state.Plugins {
		at := strings.Index(key, "@")
		if at <= 0 || key[:at] != "relevo" || len(entries) == 0 {
			continue
		}
		return entries[0].Version, true, nil // several entries: report the first
	}
	return "", false, nil
}

// installedPluginDirs finds every absolute, existing directory path
// mentioning relevo inside the state file, walking the decoded document for
// strings rather than binding to Claude Code's schema for it.
func installedPluginDirs(raw []byte) []string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	var out []string
	for _, s := range jsonStrings(v) {
		if !filepath.IsAbs(s) || !strings.Contains(s, "relevo") {
			continue
		}
		if info, err := os.Stat(s); err != nil || !info.IsDir() {
			continue
		}
		out = append(out, s)
	}
	return out
}

// hookRunsMasterMindInit reports whether a hooks.json has a SessionStart entry
// whose command runs `relevo mastermind init`.
func hookRunsMasterMindInit(raw []byte) bool {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return false
	}
	return hasSessionStartMasterMindInit(v)
}

func hasSessionStartMasterMindInit(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if k == pluginHookEvent && (jsonContains(val, pluginHookInitCommand) || jsonContains(val, pluginHookInitScript)) {
				return true
			}
			if hasSessionStartMasterMindInit(val) {
				return true
			}
		}
	case []any:
		for _, e := range t {
			if hasSessionStartMasterMindInit(e) {
				return true
			}
		}
	}
	return false
}

func jsonContains(v any, want string) bool {
	switch t := v.(type) {
	case string:
		return strings.Contains(t, want)
	case map[string]any, []any:
		raw, err := json.Marshal(t)
		return err == nil && bytes.Contains(raw, []byte(want))
	}
	return false
}

func jsonStrings(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, e := range t {
			out = append(out, jsonStrings(e)...)
		}
		return out
	case map[string]any:
		var out []string
		for _, e := range t {
			out = append(out, jsonStrings(e)...)
		}
		return out
	}
	return nil
}
