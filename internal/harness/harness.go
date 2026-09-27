// Package harness describes how each supported agent harness is started and
// checked, and installs relevo's shipped agent definitions and plugin files
// for it.
package harness

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RoleShape distinguishes persistent writers from ephemeral consults.
type RoleShape string

const (
	ShapeBuilder RoleShape = "builder"
	ShapeConsult RoleShape = "consult"
)

type RoleSpec struct {
	Name       string
	Shape      RoleShape
	Definition string
	// Definitions is every definition a harness must have installed to run
	// the role: Definition first, then what it dispatches to.
	Definitions []string
}

// roleTable defines relevo's built-in roles: a job name, its shape, and the
// harness definition that implements it. The candidate that runs it is a
// separate choice.
var roleTable = []RoleSpec{
	{
		Name:        "builder",
		Shape:       ShapeBuilder,
		Definition:  "plan-executor",
		Definitions: []string{"plan-executor", "researcher"},
	},
	{
		Name:        "reviewer",
		Shape:       ShapeConsult,
		Definition:  "reviewer",
		Definitions: []string{"reviewer"},
	},
	{
		Name:        "researcher",
		Shape:       ShapeConsult,
		Definition:  "researcher",
		Definitions: []string{"researcher"},
	},
}

// RoleByName returns the specification for the named role; ok is false for an
// unknown name.
func RoleByName(name string) (RoleSpec, bool) {
	for _, r := range roleTable {
		if r.Name == name {
			return r, true
		}
	}
	return RoleSpec{}, false
}

func RoleNames() []string {
	names := make([]string, 0, len(roleTable))
	for _, r := range roleTable {
		names = append(names, r.Name)
	}
	return names
}

type Role struct {
	Name        string
	Path        string // home-relative install path
	Doc         string // basename stem of the embedded definition, "<role>.<kind>"
	ExpectModel string // the only model pin doctor accepts without warning
}

// Harness describes how one harness kind is started and checked on the local
// machine. It is not comparable, because Roles is a slice.
type Harness struct {
	Kind   string // harness kind, as passed to the harness's own agent selector
	Binary string // executable name looked up on PATH
	// Roles are the definitions relevo ships for this kind, ordered with
	// plan-executor first so doctor reports the role the loop depends on
	// before the rest. A row need not back a roleTable entry: architect is the
	// planner's definition, shipped but never launched by relevo.
	Roles []Role
	// MinVersion is the semver floor doctor holds the binary to; "" means
	// unchecked.
	MinVersion string
	// LimitPatterns and DenialPatterns are default regexes for provider-quota
	// text and for a tool call refused in print mode. Every default must
	// compile, and case-insensitivity is written in with (?i).
	LimitPatterns  []string
	DenialPatterns []string
	// Providers is the closed list of providers a candidate on this kind may
	// name; nil means any provider. Only the cockpit enforces it, so an
	// existing config keeps loading.
	Providers []string
	// DocExt is the extension of this kind's shipped definitions under
	// agents/; "" means "md". codex roles are TOML profiles.
	DocExt string
	// Files are the non-definition files relevo ships for this kind; an absent
	// one is written only when InstallOptions.Files is set.
	Files []ShippedFile
}

var knownHarnesses = map[string]Harness{
	"agy": {
		Kind:       "agy",
		Binary:     "agy",
		MinVersion: "1.1.6",
		// Only the first limit line was observed in a real session.
		LimitPatterns: []string{
			`(?i)individual quota reached`,
			`(?i)RESOURCE_EXHAUSTED`,
			`(?i)quota exceeded`,
		},
		DenialPatterns: []string{
			`(?i)permission (request )?(denied|rejected)`,
			`(?i)tool (call|use) (was )?rejected`,
			`(?i)not permitted in (plan|accept-edits) mode`,
		},
		Providers: []string{"google", "agy-extra"},
		Roles: []Role{
			{Name: "plan-executor", Path: ".gemini/config/agents/plan-executor.md", Doc: "plan-executor.agy", ExpectModel: "inherit"},
			{Name: "researcher", Path: ".gemini/config/agents/researcher.md", Doc: "researcher.agy", ExpectModel: "inherit"},
			{Name: "reviewer", Path: ".gemini/config/agents/reviewer.md", Doc: "reviewer.agy", ExpectModel: "inherit"},
			{Name: "architect", Path: ".gemini/config/agents/architect.md", Doc: "architect.agy", ExpectModel: "inherit"},
		},
	},
	"claude": {
		Kind:   "claude",
		Binary: "claude",
		LimitPatterns: []string{
			`(?i)you've hit your .*limit`,
			`(?i)usage limit reached`,
			`(?i)rate limit reached`,
			`(?i)limit .*resets`,
		},
		DenialPatterns: []string{
			`(?i)requested permissions to use .* but you haven't granted`,
			`(?i)permission (to use .* was )?denied`,
			`(?i)tool use was rejected`,
		},
		Providers: []string{"anthropic"},
		Roles: []Role{
			{Name: "plan-executor", Path: ".claude/agents/plan-executor.md", Doc: "plan-executor.claude"},
			{Name: "researcher", Path: ".claude/agents/researcher.md", Doc: "researcher.claude"},
			{Name: "reviewer", Path: ".claude/agents/reviewer.md", Doc: "reviewer.claude"},
			{Name: "architect", Path: ".claude/agents/architect.md", Doc: "architect.claude"},
		},
	},
	"opencode": {
		Kind:   "opencode",
		Binary: "opencode",
		LimitPatterns: []string{
			`(?i)rate.?limit(ed)? (reached|exceeded)`,
			`(?i)quota (exceeded|reached)`,
			`(?i)insufficient (credits|quota)`,
			`(?i)RESOURCE_EXHAUSTED`,
			// These key on the HTTP status opencode writes into the message and
			// on the two provider sentences, because the structured error.type
			// and status fields never reach the rendered log the scan reads.
			`(?i)error 429`,
			`(?i)requires more credits`,
			`(?i)reached your .* limit`,
		},
		DenialPatterns: []string{
			`(?i)permission.*(denied|rejected)`,
			`(?i)rejected: external_directory`,
		},
		Roles: []Role{
			{Name: "plan-executor", Path: ".config/opencode/agents/plan-executor.md", Doc: "plan-executor.opencode"},
			{Name: "researcher", Path: ".config/opencode/agents/researcher.md", Doc: "researcher.opencode"},
			{Name: "reviewer", Path: ".config/opencode/agents/reviewer.md", Doc: "reviewer.opencode"},
			{Name: "architect", Path: ".config/opencode/agents/architect.md", Doc: "architect.opencode"},
		},
		Files: []ShippedFile{
			{Name: "opencode-plugin/package.json", Path: ".config/opencode/plugins/relevo/package.json", Embed: "opencodeplugin/package.json"},
			{Name: "opencode-plugin/server.ts", Path: ".config/opencode/plugins/relevo/server.ts", Embed: "opencodeplugin/server.ts"},
			{Name: "opencode-plugin/tui.tsx", Path: ".config/opencode/plugins/relevo/tui.tsx", Embed: "opencodeplugin/tui.tsx"},
		},
	},
	"codex": {
		Kind:       "codex",
		Binary:     "codex",
		MinVersion: "0.155.0",
		LimitPatterns: []string{
			`(?i)usage limit`,
			`(?i)rate limit`,
			`(?i)quota`,
			`(?i)"status": 429`,
			`(?i)too many requests`,
		},
		DenialPatterns: []string{
			`(?i)patch rejected: writing outside of the project`,
			`(?i)rejected by user approval settings`,
			`(?i)sandbox.*(denied|blocked|not permitted)`,
			`(?i)permission denied`,
		},
		DocExt: "toml",
		Roles: []Role{
			{Name: "plan-executor", Path: ".codex/plan-executor.config.toml", Doc: "plan-executor.codex"},
			{Name: "researcher", Path: ".codex/researcher.config.toml", Doc: "researcher.codex", ExpectModel: "gpt-5.6-luna"},
			{Name: "reviewer", Path: ".codex/reviewer.config.toml", Doc: "reviewer.codex"},
			{Name: "architect", Path: ".codex/architect.config.toml", Doc: "architect.codex"},
		},
	},
}

func (h Harness) Role(name string) (Role, bool) {
	for _, r := range h.Roles {
		if r.Name == name {
			return r, true
		}
	}
	return Role{}, false
}

func (h Harness) RoleNames() []string {
	names := make([]string, 0, len(h.Roles))
	for _, r := range h.Roles {
		names = append(names, r.Name)
	}
	return names
}

// CanServe reports whether this harness can serve the named role: false when
// the role is unknown or the harness cannot run its definitions.
func (h Harness) CanServe(role string) bool {
	spec, ok := RoleByName(role)
	if !ok {
		return false
	}
	for _, d := range spec.Definitions {
		if _, found := h.Role(d); !found {
			return false
		}
	}
	return true
}

// Placeholders that stand in Launch.Print for the values only the caller knows
// at send time; PrintArgs replaces them. They are exported for tests to
// recognise, never for a caller to build argv by hand.
const (
	PromptPlaceholder = "<prompt>"
	BudgetPlaceholder = "<budget>"
	DirPlaceholder    = "<dir>"
	StatePlaceholder  = "<state>" // codex writable-roots override, after a "-c"
)

// Launch describes how to start an agent process for a specific role and model.
// Print is the non-interactive form a process runs and holds the placeholders
// above as their own elements; PromptAt indexes PromptPlaceholder, or is -1.
type Launch struct {
	Kind     string
	Print    []string
	PromptAt int
}

// Launch renders argv for role at tier: PermissionArgs after the base form and
// before extra. It returns ErrTierUnsupported for a refusal cell, or
// ErrExtraArgsPermission when tier is not TierHarness and extra carries a
// permission flag for this kind.
func (h Harness) Launch(provider, model string, extra []string, role RoleSpec, tier Tier) (Launch, error) {
	var print []string
	promptAt := -1

	switch h.Kind {
	case "claude":
		id, effort := claudeEffort(model)
		print = []string{"-p", PromptPlaceholder, "--model", id}
		if effort != "" {
			print = append(print, "--effort", effort)
		}
		print = append(print, "--agent", role.Definition, "--output-format", "stream-json", "--verbose")
		promptAt = 1
	case "opencode":
		// Without --standalone, `run` is a client of the user's single
		// `opencode serve`, so killing the client leaves the session editing
		// the worktree relevo left; --thinking keeps reasoning in the stream.
		print = []string{"run", PromptPlaceholder, "-m", provider + "/" + model, "--agent", role.Definition, "--format", "json", "--thinking", "--standalone"}
		promptAt = 1
	case "agy":
		// agy's cwd report proved unreliable and its first run_command ran
		// outside any repository, so --add-dir pins the workspace.
		print = []string{"-p", PromptPlaceholder, "--model", model, "--agent", role.Definition,
			"--output-format", "stream-json", "--print-timeout", BudgetPlaceholder,
			"--add-dir", DirPlaceholder}
		promptAt = 1
	case "codex":
		id, effort, err := SplitEffort(model)
		if err != nil {
			return Launch{}, fmt.Errorf("%w: codex model %q", ErrBadModel, model)
		}
		cfg := []string{"-p", role.Definition, "-m", id, "-c", "model_provider=" + provider}
		if effort != "" {
			cfg = append(cfg, "-c", "model_reasoning_effort="+effort)
		}
		print = append(append([]string{"exec", PromptPlaceholder}, cfg...), "--json", "-C", DirPlaceholder)
		promptAt = 1
	}

	perm, err := h.PermissionArgs(tier)
	if err != nil {
		return Launch{}, err
	}
	if tier != TierHarness {
		if f := h.ExtraArgsPermissionFlag(extra); f != "" {
			return Launch{}, fmt.Errorf("%w: candidate extra_args carries %s; remove it or use --tier harness", ErrExtraArgsPermission, f)
		}
	}

	if print != nil {
		print = append(append(append([]string(nil), print...), perm...), extra...)
	}
	return Launch{
		Kind:     h.Kind,
		Print:    print,
		PromptAt: promptAt,
	}, nil
}

// writableRootsArg renders the -c value for state, quoted as a TOML basic
// string (identical escapes for every path this program produces).
func writableRootsArg(state string) string {
	return "sandbox_workspace_write.writable_roots=[" + strconv.Quote(state) + "]"
}

// PrintArgs is Print with the prompt, budget, working tree and state directory
// filled in: a fresh slice, so neither Print nor the caller's extra is touched.
// A kind whose Print lacks a placeholder ignores that argument; a present
// StatePlaceholder with an empty state is filled with a value codex rejects.
func (l Launch) PrintArgs(prompt string, budget time.Duration, dir, state string) []string {
	out := make([]string, 0, len(l.Print))
	for _, a := range l.Print {
		switch a {
		case PromptPlaceholder:
			out = append(out, prompt)
		case BudgetPlaceholder:
			out = append(out, budget.String())
		case DirPlaceholder:
			out = append(out, dir)
		case StatePlaceholder:
			out = append(out, writableRootsArg(state))
		default:
			out = append(out, a)
		}
	}
	return out
}

// ErrBadModel reports a candidate model relevo cannot render for its kind.
var ErrBadModel = errors.New("bad model")

// SplitEffort splits a codex candidate model "<id>[:<effort>]" on its last
// ':'; no colon gives (model, ""). It does not validate the effort vocabulary,
// which is model-dependent. An empty id, or a colon with an empty effort, is
// ErrBadModel.
func SplitEffort(model string) (id, effort string, err error) {
	if model == "" {
		return "", "", ErrBadModel
	}
	i := strings.LastIndex(model, ":")
	if i < 0 {
		return model, "", nil
	}
	id, effort = model[:i], model[i+1:]
	if id == "" || effort == "" {
		return "", "", ErrBadModel
	}
	return id, effort, nil
}

// claudeEfforts is the vocabulary `claude --effort` accepts.
var claudeEfforts = map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true, "max": true}

// claudeEffort splits a claude candidate model "<id>[:<effort>]" on its last
// ':': when the suffix is in claudeEfforts and the id is non-empty it returns
// (id, effort), and otherwise (model, ""). It never errors, because an unknown
// suffix is part of the model id — a Bedrock model id contains ':'.
func claudeEffort(model string) (id, effort string) {
	i := strings.LastIndex(model, ":")
	if i <= 0 || !claudeEfforts[model[i+1:]] {
		return model, ""
	}
	return model[:i], model[i+1:]
}

// Lookup returns the entry for a kind; ok is false for a kind relevo was not
// taught.
func Lookup(kind string) (Harness, bool) {
	h, ok := knownHarnesses[kind]
	return h, ok
}

func All() []Harness {
	all := make([]Harness, 0, len(knownHarnesses))
	for _, h := range knownHarnesses {
		all = append(all, h)
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].Kind < all[j].Kind
	})
	return all
}
