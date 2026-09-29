package policy

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/jsonshape"
)

// ErrBadPolicy reports a policy.json that does not validate.
var ErrBadPolicy = errors.New("bad policy")

// memoryMaxPattern is the systemd MemoryMax= grammar: digits with an
// optional K/M/G/T suffix.
var memoryMaxPattern = regexp.MustCompile(`^[0-9]+[KMGT]?$`)

// cpuQuotaPattern is the systemd CPUQuota= grammar, e.g. "200%".
var cpuQuotaPattern = regexp.MustCompile(`^[0-9]+%$`)

// cpuListPattern is a systemd cpu-list: comma-separated numbers or ranges.
var cpuListPattern = regexp.MustCompile(`^[0-9]+(-[0-9]+)?(,[0-9]+(-[0-9]+)?)*$`)

// validateScope checks one ScopePolicy block; a nil block means defaults.
func validateScope(path, prefix string, sc *ScopePolicy) error {
	if sc == nil {
		return nil
	}
	if sc.Slice != "" && !strings.HasSuffix(sc.Slice, ".slice") {
		return fmt.Errorf("%s: %s.slice: must end in \".slice\", got %q: %w", path, prefix, sc.Slice, ErrBadPolicy)
	}
	if sc.CPUWeight != 0 && (sc.CPUWeight < 1 || sc.CPUWeight > 10000) {
		return fmt.Errorf("%s: %s.cpu_weight: must be 1..10000, got %d: %w", path, prefix, sc.CPUWeight, ErrBadPolicy)
	}
	if sc.MemoryMax != "" && !memoryMaxPattern.MatchString(sc.MemoryMax) {
		return fmt.Errorf("%s: %s.memory_max: must match ^[0-9]+[KMGT]?$, got %q: %w", path, prefix, sc.MemoryMax, ErrBadPolicy)
	}
	if sc.CPUQuota != "" {
		if !cpuQuotaPattern.MatchString(sc.CPUQuota) {
			return fmt.Errorf("%s: %s.cpu_quota: must match ^[0-9]+%%$, got %q: %w", path, prefix, sc.CPUQuota, ErrBadPolicy)
		}
		if n, err := strconv.Atoi(strings.TrimSuffix(sc.CPUQuota, "%")); err == nil && n < 1 {
			return fmt.Errorf("%s: %s.cpu_quota: must be at least 1%%, got %q: %w", path, prefix, sc.CPUQuota, ErrBadPolicy)
		}
	}
	if sc.GateCPUQuota != "" {
		if !cpuQuotaPattern.MatchString(sc.GateCPUQuota) {
			return fmt.Errorf("%s: %s.gate_cpu_quota: must match ^[0-9]+%%$, got %q: %w", path, prefix, sc.GateCPUQuota, ErrBadPolicy)
		}
		if n, err := strconv.Atoi(strings.TrimSuffix(sc.GateCPUQuota, "%")); err == nil && n < 1 {
			return fmt.Errorf("%s: %s.gate_cpu_quota: must be at least 1%%, got %q: %w", path, prefix, sc.GateCPUQuota, ErrBadPolicy)
		}
	}
	if sc.AllowedCPUs != "" {
		if _, err := ParseCPUList(sc.AllowedCPUs); err != nil {
			return fmt.Errorf("%s: %s.allowed_cpus: %w, got %q: %w", path, prefix, err, sc.AllowedCPUs, ErrBadPolicy)
		}
	}
	if sc.TasksMax != 0 && sc.TasksMax < 1 {
		return fmt.Errorf("%s: %s.tasks_max: must be at least 1, got %d: %w", path, prefix, sc.TasksMax, ErrBadPolicy)
	}
	return nil
}

// ParseCPUList parses a systemd cpu-list ("0-2", "1,3,5-7") into sorted,
// de-duplicated cores; no core above 1023 is accepted. Pure.
func ParseCPUList(s string) ([]int, error) {
	if !cpuListPattern.MatchString(s) {
		return nil, fmt.Errorf("not a cpu list")
	}
	seen := make(map[int]bool)
	var out []int
	for _, part := range strings.Split(s, ",") {
		lo, hi := 0, 0
		if dash := strings.Index(part, "-"); dash >= 0 {
			var err error
			lo, err = strconv.Atoi(part[:dash])
			if err != nil {
				return nil, fmt.Errorf("not a cpu list")
			}
			hi, err = strconv.Atoi(part[dash+1:])
			if err != nil {
				return nil, fmt.Errorf("not a cpu list")
			}
			if lo > hi {
				return nil, fmt.Errorf("range %d-%d runs backwards", lo, hi)
			}
		} else {
			n, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("not a cpu list")
			}
			lo, hi = n, n
		}
		for n := lo; n <= hi; n++ {
			if n > 1023 {
				return nil, fmt.Errorf("cpu %d is above 1023", n)
			}
		}
		for n := lo; n <= hi; n++ {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	sort.Ints(out)
	return out, nil
}

// policyUnknownKeyWarnings returns one warning per decoded key path Policy's
// JSON shape does not declare; a key under a map-typed field is never unknown.
func policyUnknownKeyWarnings(path string, raw []byte) []string {
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil
	}

	leaves := jsonshape.Keys(reflect.TypeOf(Policy{}))
	leafSet := make(map[string]bool, len(leaves))
	for _, l := range leaves {
		leafSet[l] = true
	}

	var paths []string
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		switch val := v.(type) {
		case map[string]any:
			if hasLeafPrefix(leaves, prefix, "{}") {
				return
			}
			keys := make([]string, 0, len(val))
			for k := range val {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				child := k
				if prefix != "" {
					child = prefix + "." + k
				}
				switch {
				case leafSet[child] || hasLeafPrefix(leaves, child, ".") ||
					hasLeafPrefix(leaves, child, "[]") || hasLeafPrefix(leaves, child, "{}"):
					walk(child, val[k])
				default:
					paths = append(paths, child)
				}
			}
		case []any:
			for _, item := range val {
				walk(prefix+"[]", item)
			}
		}
	}
	walk("", data)

	base := filepath.Base(path)
	warnings := make([]string, 0, len(paths))
	for _, p := range paths {
		warnings = append(warnings, fmt.Sprintf("%s: unknown key %q (a typo, or a key a newer relevo reads)", base, p))
	}
	return warnings
}

// hasLeafPrefix reports whether some leaf path continues p with sep.
func hasLeafPrefix(leaves []string, p, sep string) bool {
	if p == "" {
		return false
	}
	pre := p + sep
	for _, l := range leaves {
		if strings.HasPrefix(l, pre) {
			return true
		}
	}
	return false
}

// Load reads and validates a policy file, discarding the unknown-key
// warnings LoadWithWarnings returns.
func Load(path string) (Policy, error) {
	p, _, err := LoadWithWarnings(path)
	return p, err
}

// LoadWithWarnings reads and validates a policy file, returning unknown keys
// as warnings rather than errors. A missing file is the zero Policy and no
// error.
func LoadWithWarnings(path string) (Policy, []string, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Policy{}, nil, nil
	}
	if err != nil {
		return Policy{}, nil, fmt.Errorf("read %s: %w", path, err)
	}
	return Parse(path, raw)
}

// Parse validates a policy from data, naming it by name in every message.
func Parse(name string, raw []byte) (Policy, []string, error) {
	path := name
	var p Policy
	if err := json.Unmarshal(raw, &p); err != nil {
		return Policy{}, nil, fmt.Errorf("%s: %w: %w", path, err, ErrBadPolicy)
	}

	warnings := policyUnknownKeyWarnings(path, raw)

	if err := validateThresholds(path, p); err != nil {
		return Policy{}, warnings, err
	}
	if err := validateScope(path, "scope", p.Scope); err != nil {
		return Policy{}, warnings, err
	}
	if p.Serve != nil {
		if err := validateScope(path, "serve.scope", p.Serve.Scope); err != nil {
			return Policy{}, warnings, err
		}
	}
	if err := validateScanPatterns(path, p.ScanPatterns); err != nil {
		return Policy{}, warnings, err
	}
	if err := validateClassify(path, p.Classify); err != nil {
		return Policy{}, warnings, err
	}
	maxTier, err := parseMaxTier(path, p.MaxTier)
	if err != nil {
		return Policy{}, warnings, err
	}
	if err := validateTiers(path, p.Tier, maxTier); err != nil {
		return Policy{}, warnings, err
	}
	if err := validateOrder(path, p.Order); err != nil {
		return Policy{}, warnings, err
	}
	if err := validateNotify(path, p.Notify); err != nil {
		return Policy{}, warnings, err
	}

	return p, warnings, nil
}

// validateThresholds checks every numeric knob that must be positive (or
// non-negative) when present.
func validateThresholds(path string, p Policy) error {
	if p.MaxSwitches != nil && *p.MaxSwitches < 0 {
		return fmt.Errorf("%s: max_switches: must be >= 0, got %d: %w", path, *p.MaxSwitches, ErrBadPolicy)
	}
	if p.ArtifactMaxMB != nil && *p.ArtifactMaxMB < 0 {
		return fmt.Errorf("%s: artifact_max_mb: must be >= 0, got %d: %w", path, *p.ArtifactMaxMB, ErrBadPolicy)
	}
	if p.LimitGateDefaultMS != nil && *p.LimitGateDefaultMS <= 0 {
		return fmt.Errorf("%s: limit_gate_default_ms: must be > 0, got %d: %w", path, *p.LimitGateDefaultMS, ErrBadPolicy)
	}
	if p.StallAfterMS != nil && *p.StallAfterMS <= 0 {
		return fmt.Errorf("%s: stall_after_ms: must be > 0, got %d: %w", path, *p.StallAfterMS, ErrBadPolicy)
	}
	if p.ProgressIntervalMS != nil && *p.ProgressIntervalMS <= 0 {
		return fmt.Errorf("%s: progress_interval_ms: must be > 0, got %d: %w", path, *p.ProgressIntervalMS, ErrBadPolicy)
	}
	if p.ExploreAfterMS != nil && *p.ExploreAfterMS <= 0 {
		return fmt.Errorf("%s: explore_after_ms: must be > 0, got %d: %w", path, *p.ExploreAfterMS, ErrBadPolicy)
	}
	if p.StaleAfterMS != nil && *p.StaleAfterMS <= 0 {
		return fmt.Errorf("%s: stale_after_ms: must be > 0, got %d: %w", path, *p.StaleAfterMS, ErrBadPolicy)
	}
	if p.Gate != nil && p.Gate.TimeoutMS != nil && *p.Gate.TimeoutMS <= 0 {
		return fmt.Errorf("%s: gate.timeout_ms: must be > 0, got %d: %w", path, *p.Gate.TimeoutMS, ErrBadPolicy)
	}
	if p.Gate != nil && p.Gate.Regate != nil && *p.Gate.Regate < 0 {
		return fmt.Errorf("%s: gate.regate: must be >= 0, got %d: %w", path, *p.Gate.Regate, ErrBadPolicy)
	}
	if p.Serve != nil && p.Serve.MaxBuilders != nil && *p.Serve.MaxBuilders < 1 {
		return fmt.Errorf("%s: serve.max_builders: must be at least 1, got %d: %w", path, *p.Serve.MaxBuilders, ErrBadPolicy)
	}
	return nil
}

func validateScanPatterns(path string, patterns []string) error {
	for i, pat := range patterns {
		if _, err := regexp.Compile(pat); err != nil {
			return fmt.Errorf("%s: scan_patterns[%d]: %w: %w", path, i, err, ErrBadPolicy)
		}
	}
	return nil
}

func validateClassify(path string, c *Classify) error {
	if c == nil {
		return nil
	}
	if c.Provider == "" {
		return fmt.Errorf("%s: classify.provider: required: %w", path, ErrBadPolicy)
	}
	if c.Provider != "jev" {
		return fmt.Errorf("%s: classify.provider: unknown %q (known: jev): %w", path, c.Provider, ErrBadPolicy)
	}
	if c.InjectionThreshold != nil {
		if v := *c.InjectionThreshold; v <= 0 || v > 1 {
			return fmt.Errorf("%s: classify.injection_threshold: must be in (0, 1], got %v: %w", path, v, ErrBadPolicy)
		}
	}
	if c.TimeoutMS != nil && *c.TimeoutMS <= 0 {
		return fmt.Errorf("%s: classify.timeout_ms: must be > 0, got %d: %w", path, *c.TimeoutMS, ErrBadPolicy)
	}
	return nil
}

func parseMaxTier(path, s string) (harness.Tier, error) {
	if s == "" {
		return DefaultMaxTier, nil
	}
	t, err := harness.ParseTier(s)
	if err != nil {
		return "", fmt.Errorf("%s: max_tier: %w: %w", path, err, ErrBadPolicy)
	}
	if t == harness.TierHarness {
		return "", fmt.Errorf("%s: max_tier: \"harness\" is not a cap: %w", path, ErrBadPolicy)
	}
	return t, nil
}

func validateTiers(path string, tier map[string]string, maxTier harness.Tier) error {
	roles := make([]string, 0, len(tier))
	for role := range tier {
		roles = append(roles, role)
	}
	sort.Strings(roles)

	for _, role := range roles {
		if _, ok := harness.RoleByName(role); !ok {
			return fmt.Errorf("%s: tier.%s: unknown role (known: %v): %w", path, role, harness.RoleNames(), ErrBadPolicy)
		}
		parsed, err := harness.ParseTier(tier[role])
		if err != nil {
			return fmt.Errorf("%s: tier.%s: %w: %w", path, role, err, ErrBadPolicy)
		}
		if parsed.Above(maxTier) {
			return fmt.Errorf("%s: tier.%s: %s exceeds max_tier %s; raise max_tier in the same file: %w", path, role, parsed, maxTier, ErrBadPolicy)
		}
	}
	return nil
}

func validateOrder(path string, order map[string][]string) error {
	roles := make([]string, 0, len(order))
	for role := range order {
		roles = append(roles, role)
	}
	sort.Strings(roles)

	for _, role := range roles {
		if _, ok := harness.RoleByName(role); !ok {
			return fmt.Errorf("%s: order.%s: unknown role (known: %v): %w", path, role, harness.RoleNames(), ErrBadPolicy)
		}

		tokens := order[role]
		if tokens == nil {
			return fmt.Errorf("%s: order.%s: must be an array: %w", path, role, ErrBadPolicy)
		}

		seen := make(map[string]bool, len(tokens))
		for i, tok := range tokens {
			// An entry is a candidate name or a canonical harness/provider/model token.
			if !candidate.IsName(tok) {
				if _, err := candidate.ParseRef(tok); err != nil {
					return fmt.Errorf("%s: order.%s[%d]: %q: want a candidate name or harness/provider/model: %w", path, role, i, tok, ErrBadPolicy)
				}
			}
			if seen[tok] {
				return fmt.Errorf("%s: order.%s[%d]: duplicate token %q: %w", path, role, i, tok, ErrBadPolicy)
			}
			seen[tok] = true
		}
	}
	return nil
}

func validateNotify(path string, n *NotifyPolicy) error {
	if n == nil {
		return nil
	}
	knownEvents := make(map[string]bool, len(WebhookEvents))
	for _, name := range WebhookEvents {
		knownEvents[name] = true
	}

	for i, hook := range n.Webhooks {
		u, err := url.Parse(hook.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("%s: notify.webhooks[%d].url: must be an http or https URL, got %q: %w", path, i, hook.URL, ErrBadPolicy)
		}

		switch hook.Format {
		case "", "json", "slack", "discord":
		default:
			return fmt.Errorf("%s: notify.webhooks[%d].format: unknown %q (known: json, slack, discord): %w", path, i, hook.Format, ErrBadPolicy)
		}

		for j, ev := range hook.Events {
			name, _, hasState := strings.Cut(ev, ":")
			if !knownEvents[name] {
				return fmt.Errorf("%s: notify.webhooks[%d].events[%d]: unknown event %q (known: %s): %w", path, i, j, ev, strings.Join(WebhookEvents, ", "), ErrBadPolicy)
			}
			if hasState && name != "state_changed" {
				return fmt.Errorf("%s: notify.webhooks[%d].events[%d]: %q: only state_changed accepts a :<state> suffix: %w", path, i, j, ev, ErrBadPolicy)
			}
		}
	}
	return nil
}
