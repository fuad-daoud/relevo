package workflow

import (
	"strings"
	"testing"
)

// TestLegacyStepAtIsEmptyForAStepItDoesNotKnow pins the mapping's fallthrough:
// a step name the migration does not place yields no step rather than the first
// one, so a row from a newer build cannot silently run the wrong step.
func TestLegacyStepAtIsEmptyForAStepItDoesNotKnow(t *testing.T) {
	if got := legacyStepAt(Legacy{Phase: "build", Step: "auditing"}); got != "" {
		t.Errorf("legacyStepAt(auditing) = %q, want empty for a step it does not place", got)
	}
}

// TestOutputsMarshalRefusesWhatItCannotRender pins the two output declarations
// that cannot become wire form: a one-of with no value to be one of, and a kind
// no decoder on the other side would understand. Each names the output, so a
// broken actor declaration says which key is wrong.
func TestOutputsMarshalRefusesWhatItCannotRender(t *testing.T) {
	t.Run("one-of with no values", func(t *testing.T) {
		o := Outputs{"verdict": {Kind: OutputOneOf}}
		_, err := o.MarshalJSON()
		if err == nil {
			t.Fatal("a one-of with no values: want an error, got none")
		}
		if !strings.Contains(err.Error(), "one-of needs at least one value") {
			t.Errorf("error = %q, want it to say the one-of has no values", err)
		}
	})

	t.Run("unknown kind", func(t *testing.T) {
		o := Outputs{"report": {Kind: "report-ish"}}
		_, err := o.MarshalJSON()
		if err == nil {
			t.Fatal("an unknown kind: want an error, got none")
		}
		if !strings.Contains(err.Error(), `unknown kind "report-ish"`) {
			t.Errorf("error = %q, want it to name the unknown kind", err)
		}
	})
}

// TestOutputsMarshalRendersNilAsNull pins the wire form of an actor that declares
// no outputs at all: null, so the field is present and empty rather than absent.
func TestOutputsMarshalRendersNilAsNull(t *testing.T) {
	var o Outputs
	raw, err := o.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON(nil): %v", err)
	}
	if string(raw) != "null" {
		t.Errorf("MarshalJSON(nil) = %s, want null", raw)
	}
}

// TestUsedActorsSkipsAStepThatIsNotThere pins the traversal rule that a start
// naming a step the definition does not declare contributes no actor: the walk
// stops there rather than reporting a step nobody can run.
func TestUsedActorsSkipsAStepThatIsNotThere(t *testing.T) {
	def := mustParse(t, `name: sample
start: ghost
steps:
  a: { run: builder, on: { done: done } }
`)
	if got := UsedActors(def); len(got) != 0 {
		t.Fatalf("UsedActors = %v, want none for a start that is not a step", got)
	}
}

// TestValidateRefusesAWhenThatReadsSomethingOtherThanAParam pins rule 8's second
// half: a when that is exactly one reference is still refused when it reads a
// value other than a param, because a chain has nothing else to substitute.
func TestValidateRefusesAWhenThatReadsSomethingOtherThanAParam(t *testing.T) {
	def := mustParse(t, `name: sample
inputs:
  plans: required
start: gate
steps:
  gate: { when: "{{plans.current}}", on: { true: done } }
`)
	problems := Validate(def, shippedEnv())
	if len(problems) == 0 {
		t.Fatal("a when reading a plan reference: want a refusal, got none")
	}
	var found bool
	for _, p := range problems {
		if p.Rule == RuleWhen && strings.Contains(p.Detail, "only a param is allowed") {
			found = true
		}
	}
	if !found {
		t.Errorf("problems = %v, want rule 8 refusing a non-param reference", problems)
	}
}

// TestWithValueRefusesABoolThatIsNotOne pins the parse error each param kind
// raises, so a --flag value of the wrong shape says which kind it wanted.
func TestWithValueRefusesABoolThatIsNotOne(t *testing.T) {
	b := Param{Kind: ParamBool}
	if _, err := b.withValue("maybe"); err == nil {
		t.Error(`withValue("maybe") on a bool: want an error`)
	} else if !strings.Contains(err.Error(), "want bool") {
		t.Errorf("error = %q, want it to name the wanted kind", err)
	}

	i := Param{Kind: ParamInt}
	if _, err := i.withValue("many"); err == nil {
		t.Error(`withValue("many") on an int: want an error`)
	} else if !strings.Contains(err.Error(), "want int") {
		t.Errorf("error = %q, want it to name the wanted kind", err)
	}
}
