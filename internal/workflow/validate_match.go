package workflow

import "strings"

// checkMatches is rule 3: every match comes from the step's kind, and every
// declared value is covered, or there is an else.
func (v *validator) checkMatches() {
	for _, id := range sortedKeys(v.def.Steps) {
		step := v.def.Steps[id]
		if len(step.Kinds()) != 1 {
			continue
		}
		switch kindOf(step) {
		case "run":
			v.checkRunMatches(id, step)
		case "check":
			v.checkEnumMatches(id, step, "result", "green", "red")
		case "for-each":
			v.checkEnumMatches(id, step, "", "next", "empty")
		case "when":
			v.checkEnumMatches(id, step, "", "true", "false")
		case "fork":
			v.checkForkMatches(id, step)
		}
	}
}

// checkRunMatches is rule 3 for a run: the declared outcomes and status are the
// only matches, and each is covered or an else is present.
func (v *validator) checkRunMatches(id string, step Step) {
	actor, ok := v.actor(step)
	if !ok {
		return
	}
	for _, key := range sortedKeys(step.On) {
		v.checkRunMatch(id, key, actor.Outputs)
	}
	v.checkRunCoverage(id, step, actor.Outputs)
}

// checkRunMatch validates one on key against the actor's declarations.
func (v *validator) checkRunMatch(id, key string, declared Outputs) {
	switch {
	case key == "else", key == "done":
		return
	case strings.HasPrefix(key, "status="):
		if status := strings.TrimPrefix(key, "status="); !statuses[status] {
			v.add(id, RuleMatch, "match %q names unknown status %q", key, status)
		}
	case strings.HasSuffix(key, ">0"):
		name := strings.TrimSuffix(key, ">0")
		if out, ok := declared[name]; !ok || out.Kind != OutputCount {
			v.add(id, RuleMatch, "match %q names %q, which is not a count", key, name)
		}
	case strings.Contains(key, "="):
		name, value := cut(key, "=")
		v.checkOutcomeMatch(id, key, name, value, declared)
	default:
		v.checkBareMatch(id, key, declared)
	}
}

// checkOutcomeMatch validates a key=value match against a declared outcome.
func (v *validator) checkOutcomeMatch(id, key, name, value string, declared Outputs) {
	out, ok := declared[name]
	switch {
	case !ok:
		v.add(id, RuleMatch, "match %q names undeclared outcome %q", key, name)
	case out.Kind == OutputArtifact:
		v.add(id, RuleMatch, "match %q branches on artifact %q", key, name)
	case out.Kind == OutputOneOf && !containsStr(out.Values, value):
		v.add(id, RuleMatch, "match %q is not a declared value of %q", key, name)
	case out.Kind == OutputCount && value != "0":
		v.add(id, RuleMatch, "count match %q is only =0 or >0", key)
	}
}

// checkBareMatch rejects an outcome key with no value, or an artifact key.
func (v *validator) checkBareMatch(id, key string, declared Outputs) {
	out, ok := declared[key]
	switch {
	case !ok:
		v.add(id, RuleMatch, "match %q is not a declared outcome", key)
	case out.Kind == OutputArtifact:
		v.add(id, RuleMatch, "match %q branches on artifact %q", key, key)
	default:
		v.add(id, RuleMatch, "match %q names an outcome without a value", key)
	}
}

// checkRunCoverage requires every declared outcome to be covered, unless an
// else is present. A run with no outcomes covers done.
func (v *validator) checkRunCoverage(id string, step Step, declared Outputs) {
	on := step.On
	if _, hasElse := on["else"]; hasElse {
		return
	}
	if len(declared.Outcomes()) == 0 {
		if _, done := on["done"]; !done {
			if _, status := on["status=done"]; !status {
				v.add(id, RuleMatch, "uncovered: done")
			}
		}
		return
	}
	for _, name := range declared.Outcomes() {
		v.checkOutcomeCoverage(id, name, declared[name], on)
	}
}

// checkOutcomeCoverage covers one declared outcome: every one-of value, or both
// count arms.
func (v *validator) checkOutcomeCoverage(id, name string, out Output, on map[string]Target) {
	switch out.Kind {
	case OutputOneOf:
		for _, value := range out.Values {
			if _, ok := on[name+"="+value]; !ok {
				v.add(id, RuleMatch, "uncovered: %s=%s", name, value)
			}
		}
	case OutputCount:
		if _, ok := on[name+"=0"]; !ok {
			v.add(id, RuleMatch, "uncovered: %s=0", name)
		}
		if _, ok := on[name+">0"]; !ok {
			v.add(id, RuleMatch, "uncovered: %s>0", name)
		}
	}
}

// checkEnumMatches is rule 3 for the fixed-outcome kinds. resultKind is the
// result prefix a check also accepts, or empty.
func (v *validator) checkEnumMatches(id string, step Step, resultKind, a, b string) {
	on := step.On
	valid := map[string]bool{a: true, b: true, "else": true}
	if resultKind != "" {
		valid[resultKind+"="+a] = true
		valid[resultKind+"="+b] = true
	}
	for _, key := range sortedKeys(on) {
		if !valid[key] {
			v.add(id, RuleMatch, "match %q is not valid for this step", key)
		}
	}
	if _, ok := on["else"]; ok {
		return
	}
	if !hasMatch(on, resultKind, a) {
		v.add(id, RuleMatch, "uncovered: %s", a)
	}
	if !hasMatch(on, resultKind, b) {
		v.add(id, RuleMatch, "uncovered: %s", b)
	}
}

// hasMatch reports whether an on map carries a match, bare or under a result
// prefix.
func hasMatch(on map[string]Target, resultKind, name string) bool {
	if _, ok := on[name]; ok {
		return true
	}
	if resultKind != "" {
		_, ok := on[resultKind+"="+name]
		return ok
	}
	return false
}

// checkForkMatches is rule 3 for a fork: joined and conflict are required, and
// halted is accepted as an optional outcome.
func (v *validator) checkForkMatches(id string, step Step) {
	on := step.On
	valid := map[string]bool{
		"joined":          true,
		"conflict":        true,
		"halted":          true,
		"result=joined":   true,
		"result=conflict": true,
		"result=halted":   true,
		"else":            true,
	}
	for _, key := range sortedKeys(on) {
		if !valid[key] {
			v.add(id, RuleMatch, "match %q is not valid for this step", key)
		}
	}
	if _, ok := on["else"]; ok {
		return
	}
	if !hasMatch(on, "result", "joined") {
		v.add(id, RuleMatch, "uncovered: joined")
	}
	if !hasMatch(on, "result", "conflict") {
		v.add(id, RuleMatch, "uncovered: conflict")
	}
}
