package ui

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// scopeBlockFor is the stored ScopePolicy the scope or serve.scope form
// prefills from: d.Policy.Scope, or d.Policy.Serve.Scope when form is
// "serve.scope". nil means every field is unset.
func scopeBlockFor(doc relevo.ConfigDoc, form string) *policy.ScopePolicy {
	if form == "serve.scope" {
		if doc.Policy.Serve == nil {
			return nil
		}
		return doc.Policy.Serve.Scope
	}
	return doc.Policy.Scope
}

// newScopeFields builds the scope or serve.scope form's fields: an
// enabled chip, then the seven scope limits, every path prefixed with form.
func newScopeFields(doc relevo.ConfigDoc, form string) ([]settingField, string, string) {
	sc := scopeBlockFor(doc, form)

	enabledSel := 0
	if sc != nil && sc.Enabled != nil && !*sc.Enabled {
		enabledSel = 1
	}
	enabledField := settingField{
		label: "enabled", path: form + ".enabled",
		chips: []string{"on", "off"}, sel: enabledSel, origSel: enabledSel,
		value: func(sel int) any { return sel == 0 },
	}

	scopeText := func(label, leaf, hint, orig string, parse func(string) (any, error)) settingField {
		in := newFormInput(false)
		in.SetValue(orig)
		in.CursorEnd()
		return settingField{
			label: label,
			path:  form + "." + leaf,
			input: in,
			orig:  orig,
			hint:  hint,
			parse: parse,
		}
	}

	intOrEmpty := func(n int) string {
		if n == 0 {
			return ""
		}
		return strconv.Itoa(n)
	}

	var slice, memoryMax, cpuQuota, gateCPUQuota, allowedCPUs, cpuWeight, tasksMax string
	if sc != nil {
		slice = sc.Slice
		memoryMax = sc.MemoryMax
		cpuQuota = sc.CPUQuota
		gateCPUQuota = sc.GateCPUQuota
		allowedCPUs = sc.AllowedCPUs
		cpuWeight = intOrEmpty(sc.CPUWeight)
		tasksMax = intOrEmpty(sc.TasksMax)
	}

	fields := []settingField{
		enabledField,
		scopeText("slice", "slice", "default: systemd's", slice, parseSlice),
		scopeText("cpu_weight", "cpu_weight", "default 100", cpuWeight,
			parseIntRange(1, 10000, "a whole number from 1 to 10000")),
		scopeText("memory_max", "memory_max", "default none · e.g. 8G", memoryMax, parseMemoryMax),
		scopeText("cpu_quota", "cpu_quota", "default none · e.g. 200%", cpuQuota, parseCPUQuota),
		scopeText("gate_cpu_quota", "gate_cpu_quota", "default: cpu_quota", gateCPUQuota, parseCPUQuota),
		scopeText("allowed_cpus", "allowed_cpus", "default: no pinning · e.g. 0-3", allowedCPUs, parseScopeText),
		scopeText("tasks_max", "tasks_max", "default none", tasksMax, parseIntMin(1, "a whole number, 1 or more")),
	}

	if form == "serve.scope" {
		return fields, "rounds relevo serve runs use this in place of scope", "serve.scope"
	}
	return fields, "every round this machine starts runs in this scope", "scope"
}

// newClassifyFields builds the classify form's fields: the provider
// chip, then model, threshold and timeout, disabled while the provider is
// off.
func newClassifyFields(doc relevo.ConfigDoc) ([]settingField, string, string) {
	cl := doc.Policy.Classify

	providerSel := 0
	if cl != nil {
		providerSel = 1
	}
	// disabledWhileOff reads the provider chip's live selection, so tab,
	// validation and sets() all see the same up-to-date on/off state.
	disabledWhileOff := func(f settingsForm) bool { return f.fields[0].sel == 0 }

	providerField := settingField{
		label: "provider", path: "classify.provider",
		chips: []string{"off", "jev"}, sel: providerSel, origSel: providerSel,
		value: func(sel int) any {
			if sel == 1 {
				return "jev"
			}
			return nil
		},
	}

	var modelOrig, thresholdOrig, timeoutOrig string
	if cl != nil {
		modelOrig = cl.Model
		if cl.InjectionThreshold != nil {
			thresholdOrig = strconv.FormatFloat(*cl.InjectionThreshold, 'g', -1, 64)
		}
		if cl.TimeoutMS != nil {
			timeoutOrig = relevo.FormatDuration(time.Duration(*cl.TimeoutMS) * time.Millisecond)
		}
	}

	textField := func(label, path, orig, hint string, parse func(string) (any, error)) settingField {
		in := newFormInput(false)
		in.SetValue(orig)
		in.CursorEnd()
		return settingField{
			label: label, path: path, input: in, orig: orig, hint: hint, parse: parse,
			disabled: disabledWhileOff,
		}
	}

	fields := []settingField{
		providerField,
		textField("model", "classify.model", modelOrig, "default jev-latest", parseCommand),
		textField("threshold", "classify.injection_threshold", thresholdOrig, "default 0.7", parseInjectionThreshold),
		textField("timeout", "classify.timeout_ms", timeoutOrig, "default 4s", parseDuration),
	}

	return fields, "scores each builder line for prompt injection, beside the pattern scan", "classify"
}

// parseSlice is the scope forms' slice parser: "" deletes, else the trimmed
// value must end in ".slice".
func parseSlice(s string) (any, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if !strings.HasSuffix(s, ".slice") {
		return nil, errors.New(`a systemd slice name ending in ".slice"`)
	}
	return s, nil
}

// parseIntRange is cpu_weight's parser: "" deletes, else an integer within
// [min, max] or msg.
func parseIntRange(min, max int, msg string) func(string) (any, error) {
	return func(s string) (any, error) {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, nil
		}
		n, err := strconv.Atoi(s)
		if err != nil || n < min || n > max {
			return nil, errors.New(msg)
		}
		return n, nil
	}
}

// scopeMemoryMaxPattern is memory_max's grammar: a whole number with an
// optional K/M/G/T suffix.
var scopeMemoryMaxPattern = regexp.MustCompile(`^[0-9]+[KMGT]?$`)

// parseMemoryMax is memory_max's parser: "" deletes, else the value must
// match scopeMemoryMaxPattern.
func parseMemoryMax(s string) (any, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if !scopeMemoryMaxPattern.MatchString(s) {
		return nil, errors.New("a size like 8G or 512M")
	}
	return s, nil
}

// scopeCPUQuotaPattern is cpu_quota's and gate_cpu_quota's grammar: a whole
// number followed by "%".
var scopeCPUQuotaPattern = regexp.MustCompile(`^[0-9]+%$`)

// parseCPUQuota is cpu_quota's and gate_cpu_quota's parser: "" deletes, else
// the value must match scopeCPUQuotaPattern and be at least 1%.
func parseCPUQuota(s string) (any, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if !scopeCPUQuotaPattern.MatchString(s) {
		return nil, errors.New("a percentage like 200%")
	}
	n, _ := strconv.Atoi(strings.TrimSuffix(s, "%"))
	if n < 1 {
		return nil, errors.New("a percentage like 200%")
	}
	return s, nil
}

// parseScopeText is allowed_cpus's parser: "" deletes, else the trimmed text
// passes through unchecked -- policy.Parse's validateScope judges it when the
// edit is dry-run.
func parseScopeText(s string) (any, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	return s, nil
}

// parseInjectionThreshold is classify's threshold parser: "" deletes, else a
// float in (0, 1].
func parseInjectionThreshold(s string) (any, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f <= 0 || f > 1 {
		return nil, errors.New("a number above 0, up to 1")
	}
	return f, nil
}
