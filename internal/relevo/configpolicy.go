package relevo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/policy"
)

// Setting is one policy setting displayed in :settings.
type Setting struct {
	Key     string // the name the list shows, e.g. max_switches, gate.timeout, serve.scope
	Group   string // rounds, check, timing, processes, scan or notify
	Value   string // the effective value as display text
	Default string // the default as display text
	Set     bool   // the key is stored in the policy section
	Form    string // which editor enter opens: rounds, check, timing, max_builders; or scope, serve.scope, scan_patterns, classify, webhooks (round 2)
}

// PolicySet is one path-value assignment for policy edits.
type PolicySet struct {
	Path  string // dotted JSON path inside the policy section, e.g. "gate.timeout_ms", "serve.max_builders"
	Value any    // the JSON value to store; nil deletes the key
}

// FormatDuration formats d using Go's d.String() with trailing zero units removed.
// 1h0m0s -> 1h, 15m0s -> 15m, 1h30m0s -> 1h30m, 30s -> 30s.
func FormatDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// formatScope formats a ScopePolicy the way the settings table displays it.
func formatScope(sc *policy.ScopePolicy, defaultText string) string {
	if sc == nil {
		return defaultText
	}
	if sc.Enabled != nil && !*sc.Enabled {
		return "off"
	}
	var parts []string
	if sc.Slice != "" {
		parts = append(parts, "slice "+sc.Slice)
	}
	if sc.CPUWeight != 0 {
		parts = append(parts, fmt.Sprintf("cpu_weight %d", sc.CPUWeight))
	}
	if sc.MemoryMax != "" {
		parts = append(parts, "memory_max "+sc.MemoryMax)
	}
	if sc.CPUQuota != "" {
		parts = append(parts, "cpu_quota "+sc.CPUQuota)
	}
	if sc.GateCPUQuota != "" {
		parts = append(parts, "gate_cpu_quota "+sc.GateCPUQuota)
	}
	if sc.AllowedCPUs != "" {
		parts = append(parts, "allowed_cpus "+sc.AllowedCPUs)
	}
	if sc.TasksMax != 0 {
		parts = append(parts, fmt.Sprintf("tasks_max %d", sc.TasksMax))
	}
	if len(parts) == 0 {
		return "on · no limits"
	}
	return strings.Join(parts, " · ")
}

// Settings returns the 18 policy rows in display order, filled from d.Policy.
func Settings(d ConfigDoc, cpus int) []Setting {
	p := d.Policy
	defMaxBuilders := max(1, cpus-1)

	var rows [18]Setting

	// 1: rounds / max_switches
	rows[0] = Setting{
		Group:   "rounds",
		Key:     "max_switches",
		Value:   strconv.Itoa(p.SwitchLimit()),
		Default: strconv.Itoa(policy.DefaultMaxSwitches),
		Set:     p.MaxSwitches != nil,
		Form:    "rounds",
	}

	// 2: rounds / max_tier
	rows[1] = Setting{
		Group:   "rounds",
		Key:     "max_tier",
		Value:   string(p.MaxTierOrDefault()),
		Default: string(policy.DefaultMaxTier),
		Set:     p.MaxTier != "",
		Form:    "rounds",
	}

	// 3: rounds / verify.default
	vVal := "off"
	if p.VerifyDefault() {
		vVal = "on"
	}
	rows[2] = Setting{
		Group:   "rounds",
		Key:     "verify.default",
		Value:   vVal,
		Default: "off",
		Set:     p.Verify != nil,
		Form:    "rounds",
	}

	// 4: rounds / artifact_max_mb
	rows[3] = Setting{
		Group:   "rounds",
		Key:     "artifact_max_mb",
		Value:   strconv.Itoa(int(p.ArtifactMaxBytes() / (1 << 20))),
		Default: strconv.Itoa(policy.DefaultArtifactMaxMB),
		Set:     p.ArtifactMaxMB != nil,
		Form:    "rounds",
	}

	// 5: check / gate.default
	gDef := "none"
	if p.GateDefault() != "" {
		gDef = p.GateDefault()
	}
	rows[4] = Setting{
		Group:   "check",
		Key:     "gate.default",
		Value:   gDef,
		Default: "none",
		Set:     p.Gate != nil && p.Gate.Default != "",
		Form:    "check",
	}

	// 6: check / gate.timeout
	rows[5] = Setting{
		Group:   "check",
		Key:     "gate.timeout",
		Value:   FormatDuration(p.GateTimeout()),
		Default: FormatDuration(policy.DefaultGateTimeout),
		Set:     p.Gate != nil && p.Gate.TimeoutMS != nil,
		Form:    "check",
	}

	// 7: check / gate.regate
	rows[6] = Setting{
		Group:   "check",
		Key:     "gate.regate",
		Value:   strconv.Itoa(p.GateRegate()),
		Default: "0",
		Set:     p.Gate != nil && p.Gate.Regate != nil,
		Form:    "check",
	}

	// 8: timing / limit_gate_default
	rows[7] = Setting{
		Group:   "timing",
		Key:     "limit_gate_default",
		Value:   FormatDuration(p.LimitGateDefault()),
		Default: FormatDuration(policy.DefaultLimitGate),
		Set:     p.LimitGateDefaultMS != nil,
		Form:    "timing",
	}

	// 9: timing / stall_after
	rows[8] = Setting{
		Group:   "timing",
		Key:     "stall_after",
		Value:   FormatDuration(p.StallAfter()),
		Default: FormatDuration(policy.DefaultStallAfter),
		Set:     p.StallAfterMS != nil,
		Form:    "timing",
	}

	// 10: timing / progress_interval
	rows[9] = Setting{
		Group:   "timing",
		Key:     "progress_interval",
		Value:   FormatDuration(p.ProgressInterval()),
		Default: FormatDuration(policy.DefaultProgressInterval),
		Set:     p.ProgressIntervalMS != nil,
		Form:    "timing",
	}

	// 11: timing / explore_after
	rows[10] = Setting{
		Group:   "timing",
		Key:     "explore_after",
		Value:   FormatDuration(p.ExploreAfter()),
		Default: FormatDuration(policy.DefaultExploreAfter),
		Set:     p.ExploreAfterMS != nil,
		Form:    "timing",
	}

	// 12: timing / stale_after
	rows[11] = Setting{
		Group:   "timing",
		Key:     "stale_after",
		Value:   FormatDuration(p.StaleAfter()),
		Default: FormatDuration(policy.DefaultStaleAfter),
		Set:     p.StaleAfterMS != nil,
		Form:    "timing",
	}

	// 13: processes / scope
	rows[12] = Setting{
		Group:   "processes",
		Key:     "scope",
		Value:   formatScope(p.Scope, "on · no limits"),
		Default: "on · no limits",
		Set:     p.Scope != nil,
		Form:    "scope",
	}

	// 14: processes / serve.max_builders
	smbVal := defMaxBuilders
	if p.Serve != nil && p.Serve.MaxBuilders != nil {
		smbVal = *p.Serve.MaxBuilders
	}
	rows[13] = Setting{
		Group:   "processes",
		Key:     "serve.max_builders",
		Value:   strconv.Itoa(smbVal),
		Default: strconv.Itoa(defMaxBuilders),
		Set:     p.Serve != nil && p.Serve.MaxBuilders != nil,
		Form:    "max_builders",
	}

	// 15: processes / serve.scope
	ssVal := "as scope"
	if p.Serve != nil && p.Serve.Scope != nil {
		ssVal = formatScope(p.Serve.Scope, "as scope")
	}
	rows[14] = Setting{
		Group:   "processes",
		Key:     "serve.scope",
		Value:   ssVal,
		Default: "as scope",
		Set:     p.Serve != nil && p.Serve.Scope != nil,
		Form:    "serve.scope",
	}

	// 16: scan / scan_patterns
	spVal := "none"
	switch len(p.ScanPatterns) {
	case 0:
		spVal = "none"
	case 1:
		spVal = "1 pattern"
	default:
		spVal = fmt.Sprintf("%d patterns", len(p.ScanPatterns))
	}
	rows[15] = Setting{
		Group:   "scan",
		Key:     "scan_patterns",
		Value:   spVal,
		Default: "none",
		Set:     len(p.ScanPatterns) > 0,
		Form:    "scan_patterns",
	}

	// 17: scan / classify
	clVal := "off"
	if p.Classify != nil && p.Classify.Provider != "" {
		clVal = p.Classify.Provider
	}
	rows[16] = Setting{
		Group:   "scan",
		Key:     "classify",
		Value:   clVal,
		Default: "off",
		Set:     p.Classify != nil,
		Form:    "classify",
	}

	// 18: notify / notify.webhooks
	nwVal := "none"
	var nwCount int
	if p.Notify != nil {
		nwCount = len(p.Notify.Webhooks)
	}
	switch nwCount {
	case 0:
		nwVal = "none"
	case 1:
		nwVal = "1 webhook"
	default:
		nwVal = fmt.Sprintf("%d webhooks", nwCount)
	}
	rows[17] = Setting{
		Group:   "notify",
		Key:     "notify.webhooks",
		Value:   nwVal,
		Default: "none",
		Set:     p.Notify != nil && len(p.Notify.Webhooks) > 0,
		Form:    "webhooks",
	}

	return rows[:]
}

// SettingPaths returns the JSON paths cleared by reset for key, or nil for an unknown key.
func SettingPaths(key string) []string {
	switch key {
	case "max_switches":
		return []string{"max_switches"}
	case "max_tier":
		return []string{"max_tier"}
	case "artifact_max_mb":
		return []string{"artifact_max_mb"}
	case "verify.default":
		return []string{"verify"}
	case "gate.default":
		return []string{"gate.default"}
	case "gate.timeout":
		return []string{"gate.timeout_ms"}
	case "gate.regate":
		return []string{"gate.regate"}
	case "limit_gate_default":
		return []string{"limit_gate_default_ms"}
	case "stall_after":
		return []string{"stall_after_ms"}
	case "progress_interval":
		return []string{"progress_interval_ms"}
	case "explore_after":
		return []string{"explore_after_ms"}
	case "stale_after":
		return []string{"stale_after_ms"}
	case "scope":
		return []string{"scope"}
	case "serve.max_builders":
		return []string{"serve.max_builders"}
	case "serve.scope":
		return []string{"serve.scope"}
	case "scan_patterns":
		return []string{"scan_patterns"}
	case "classify":
		return []string{"classify"}
	case "notify.webhooks":
		return []string{"notify"}
	default:
		return nil
	}
}

// EditPolicy validates sets against d.PolicyRaw, encodes the resulting policy,
// validates it via policy.Parse and dryRun, and returns the ConfigEdit.
func EditPolicy(d ConfigDoc, sets []PolicySet, message string) (ConfigEdit, error) {
	var m map[string]any
	var origMap map[string]any

	if len(d.PolicyRaw) > 0 {
		dec := json.NewDecoder(bytes.NewReader(d.PolicyRaw))
		dec.UseNumber()
		if err := dec.Decode(&m); err != nil {
			return ConfigEdit{}, &FieldError{"", err.Error()}
		}
		dec2 := json.NewDecoder(bytes.NewReader(d.PolicyRaw))
		dec2.UseNumber()
		_ = dec2.Decode(&origMap)
	}
	if m == nil {
		m = map[string]any{}
	}
	if origMap == nil {
		origMap = map[string]any{}
	}

	for _, set := range sets {
		parts := strings.Split(set.Path, ".")
		if len(parts) == 0 {
			continue
		}
		if set.Value != nil {
			curr := m
			for i := 0; i < len(parts)-1; i++ {
				p := parts[i]
				val, exists := curr[p]
				if !exists {
					next := map[string]any{}
					curr[p] = next
					curr = next
				} else {
					next, ok := val.(map[string]any)
					if !ok {
						return ConfigEdit{}, &FieldError{"", set.Path + ": not an object"}
					}
					curr = next
				}
			}
			curr[parts[len(parts)-1]] = set.Value
		} else {
			type parentEntry struct {
				m   map[string]any
				key string
			}
			var parents []parentEntry
			curr := m
			found := true
			for i := 0; i < len(parts)-1; i++ {
				p := parts[i]
				val, exists := curr[p]
				if !exists {
					found = false
					break
				}
				next, ok := val.(map[string]any)
				if !ok {
					return ConfigEdit{}, &FieldError{"", set.Path + ": not an object"}
				}
				parents = append(parents, parentEntry{m: curr, key: p})
				curr = next
			}
			if found {
				delete(curr, parts[len(parts)-1])
				for i := len(parents) - 1; i >= 0; i-- {
					if len(curr) == 0 {
						delete(parents[i].m, parents[i].key)
						curr = parents[i].m
					} else {
						break
					}
				}
			}
		}
	}

	origCompact, _ := json.Marshal(origMap)
	nextCompact, _ := json.Marshal(m)
	if bytes.Equal(origCompact, nextCompact) {
		return ConfigEdit{}, ErrNoChange
	}

	var body []byte
	if len(m) == 0 {
		body = []byte("{}\n")
	} else {
		data, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			return ConfigEdit{}, &FieldError{"", err.Error()}
		}
		body = append(data, '\n')
	}

	parsed, _, err := policy.Parse(config.FileName(config.Policy), body)
	if err != nil {
		msg := err.Error()
		prefix := config.FileName(config.Policy) + ": "
		msg = strings.TrimPrefix(msg, prefix)
		msg = strings.TrimSuffix(msg, ": "+policy.ErrBadPolicy.Error())
		return ConfigEdit{}, &FieldError{"", msg}
	}

	if err := dryRun(d.Candidates, d.Actors, d.Agents, parsed); err != nil {
		return ConfigEdit{}, err
	}

	return ConfigEdit{
		Sections: map[config.Section]json.RawMessage{config.Policy: body},
		Message:  message,
	}, nil
}

// tierExceedsMaxPattern matches an actor's tier-above-max_tier rejection, from
// either roles.json's build-time check or policy.json's own tier.<role> cap,
// and captures the actor, its tier and the max it exceeds.
var tierExceedsMaxPattern = regexp.MustCompile(`(?:^|: )(\w[\w-]*)\.tier: (\w+) exceeds max_tier (\w+)`)

// policyFilePrefixPattern is the leading "<name>.json: " every validation
// error from EditPolicy's dry run carries, naming the section it failed.
var policyFilePrefixPattern = regexp.MustCompile(`^(?:roles|policy|actors|candidates|agents)\.json: `)

// badWordSuffixPattern is the trailing ": bad <word>" a wrapped sentinel
// (ErrBadRoles, ErrBadPolicy, ...) adds to its error text.
var badWordSuffixPattern = regexp.MustCompile(`: bad \w+$`)

// HumanPolicyError turns a validation error from EditPolicy, ResetSetting or
// dryRun into plain words: no JSON path, no file name, no wrapped sentinel.
// A tier-above-max_tier rejection is reworded first; any other error has its
// leading "<name>.json: " and trailing ": bad <word>" stripped, in that
// order, and is otherwise returned as it stands.
func HumanPolicyError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if m := tierExceedsMaxPattern.FindStringSubmatch(msg); m != nil {
		return m[1] + " runs at " + m[2] + ", above " + m[3] + "; lower its tier in :actors first"
	}
	msg = policyFilePrefixPattern.ReplaceAllString(msg, "")
	msg = badWordSuffixPattern.ReplaceAllString(msg, "")
	return msg
}

// ResetSetting resets key to its default by clearing the paths from SettingPaths(key).
func ResetSetting(d ConfigDoc, key string) (ConfigEdit, error) {
	paths := SettingPaths(key)
	if paths == nil {
		return ConfigEdit{}, &FieldError{"", "unknown setting " + key}
	}
	var target *Setting
	for _, s := range Settings(d, 0) {
		if s.Key == key {
			target = &s
			break
		}
	}
	if target == nil || !target.Set {
		return ConfigEdit{}, ErrNoChange
	}
	sets := make([]PolicySet, len(paths))
	for i, p := range paths {
		sets[i] = PolicySet{Path: p, Value: nil}
	}
	return EditPolicy(d, sets, "reset "+key)
}
