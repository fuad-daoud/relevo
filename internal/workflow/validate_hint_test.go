package workflow

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestValidateRule3HintDefaultWorkflow(t *testing.T) {
	env := shippedEnv()
	env.Given = &Given{Plans: true}
	// Reviewer actor present with no declared outputs.
	env.Actors["reviewer"] = ActorInfo{Shape: ShapeReader, Outputs: nil}

	problems := Validate(Default(), env)

	var reviewHints, fixReviewHints []string
	for _, p := range problems {
		s := p.String()
		if strings.Contains(s, "names undeclared outcome") {
			t.Errorf("unexpected old message in problem: %s", s)
		}
		if p.Rule == RuleMatch && strings.Contains(s, "declares no outcome") {
			switch p.Step {
			case "review":
				reviewHints = append(reviewHints, s)
			case "fix-review":
				fixReviewHints = append(fixReviewHints, s)
			}
		}
	}

	if len(reviewHints) != 1 {
		t.Fatalf("step review: want exactly 1 hint, got %d: %v", len(reviewHints), reviewHints)
	}
	if len(fixReviewHints) != 1 {
		t.Fatalf("step fix-review: want exactly 1 hint, got %d: %v", len(fixReviewHints), fixReviewHints)
	}

	re := regexp.MustCompile(`declare it with: relevo config set actors\.[^.]+\.outputs '([^']+)'`)
	for _, hint := range []string{reviewHints[0], fixReviewHints[0]} {
		if !strings.Contains(hint, `actor "reviewer" declares no outcome "verdict"`) {
			t.Errorf("hint %q does not name actor and outcome", hint)
		}
		m := re.FindStringSubmatch(hint)
		if len(m) < 2 {
			t.Fatalf("hint %q does not match config set command pattern", hint)
		}
		jsonStr := m[1]
		parsed, err := ParseOutputs([]byte(jsonStr))
		if err != nil {
			t.Fatalf("ParseOutputs(%q) failed: %v", jsonStr, err)
		}
		wantReviewerContract := Outputs{
			"findings": {Kind: OutputArtifact},
			"verdict":  {Kind: OutputOneOf, Values: []string{"pass", "changes"}},
		}
		if !reflect.DeepEqual(parsed, wantReviewerContract) {
			t.Fatalf("parsed outputs %+v != want %+v", parsed, wantReviewerContract)
		}
	}
}

func TestValidateRule3HintActorDeclaringOnlyFindings(t *testing.T) {
	env := shippedEnv()
	env.Given = &Given{Plans: true}
	// Actor declares findings, but not verdict.
	env.Actors["reviewer"] = ActorInfo{
		Shape: ShapeReader,
		Outputs: Outputs{
			"findings": {Kind: OutputArtifact},
		},
	}

	problems := Validate(Default(), env)

	var hints []string
	for _, p := range problems {
		s := p.String()
		if p.Rule == RuleMatch && strings.Contains(s, "declares no outcome") && p.Step == "review" {
			hints = append(hints, s)
		}
	}

	if len(hints) != 1 {
		t.Fatalf("step review: want exactly 1 hint, got %d: %v", len(hints), hints)
	}
	if !strings.Contains(hints[0], `actor "reviewer" declares no outcome "verdict"`) {
		t.Errorf("hint %q does not name verdict", hints[0])
	}
}

func TestValidateRule3HintUnknownOutcomeNoJSON(t *testing.T) {
	const wfYAML = `
name: test-wf
inputs:
  plans: none
  task: none
steps:
  review:
    run: reviewer
    on:
      custom=val: done
`
	def := mustParse(t, wfYAML)
	env := shippedEnv()
	env.Actors["reviewer"] = ActorInfo{Shape: ShapeReader, Outputs: nil}

	problems := Validate(def, env)

	var hints []string
	for _, p := range problems {
		s := p.String()
		if p.Rule == RuleMatch && strings.Contains(s, "declares no outcome") {
			hints = append(hints, s)
		}
	}

	if len(hints) != 1 {
		t.Fatalf("want exactly 1 hint, got %d: %v", len(hints), hints)
	}
	want := `step review: rule 3: actor "reviewer" declares no outcome "custom"; declare it in actors.reviewer.outputs`
	if hints[0] != want {
		t.Fatalf("hint = %q, want %q", hints[0], want)
	}
}

func TestValidateRule3HintCountMatch(t *testing.T) {
	const wfYAML = `
name: test-wf
inputs:
  plans: none
  task: none
steps:
  scan:
    run: sec-agent
    on:
      findings>0: done
      findings=0: done
`
	def := mustParse(t, wfYAML)
	env := shippedEnv()
	env.Actors["sec-agent"] = ActorInfo{Shape: ShapeReader, Outputs: nil}

	problems := Validate(def, env)

	var hints []string
	for _, p := range problems {
		s := p.String()
		if p.Rule == RuleMatch && strings.Contains(s, "declares no outcome") {
			hints = append(hints, s)
		}
	}

	// Should be emitted once for findings, suggesting security-reviewer outputs.
	if len(hints) != 1 {
		t.Fatalf("want exactly 1 hint, got %d: %v", len(hints), hints)
	}
	wantPrefix := `step scan: rule 3: actor "sec-agent" declares no outcome "findings"; declare it with: relevo config set actors.sec-agent.outputs '{"findings":"count","report":"artifact"}'`
	if hints[0] != wantPrefix {
		t.Fatalf("hint = %q, want %q", hints[0], wantPrefix)
	}
}
