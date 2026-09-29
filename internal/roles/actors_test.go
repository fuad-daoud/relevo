package roles

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// validSource is a minimal valid agentsrc single source named my-exec.
const validSource = "---\nname: my-exec\ndescription: does the work\nshape: writer\noutput: report\n---\nbody\n"

// TestParseActorsEntries pins both entry forms and their round trip through
// EncodeActors: an on entry encodes as a string, an off entry as an object.
func TestParseActorsEntries(t *testing.T) {
	body := []byte(`{
	  "builder": {
	    "agent": "plan-executor",
	    "candidates": ["deepseek-v4.1-flash", {"candidate": "gemini-3.8-flash-high", "off": true}, "sonnet"],
	    "tier": "yolo",
	    "check": true
	  },
	  "reviewer": { "agent": "reviewer", "candidates": ["sonnet"], "tier": "yolo" }
	}`)

	actors, warnings, err := ParseActors(body)
	if err != nil {
		t.Fatalf("ParseActors: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}

	b := actors["builder"]
	if b.Agent != "plan-executor" {
		t.Errorf("builder.agent = %q, want plan-executor", b.Agent)
	}
	want := []Entry{
		{Candidate: "deepseek-v4.1-flash"},
		{Candidate: "gemini-3.8-flash-high", Off: true},
		{Candidate: "sonnet"},
	}
	if !reflect.DeepEqual(b.Candidates, want) {
		t.Errorf("builder.candidates = %+v, want %+v", b.Candidates, want)
	}
	if b.Tier != "yolo" {
		t.Errorf("builder.tier = %q, want yolo", b.Tier)
	}
	if b.Check == nil || !*b.Check {
		t.Errorf("builder.check = %v, want true", b.Check)
	}
	if r := actors["reviewer"]; r.Check != nil {
		t.Errorf("reviewer.check = %v, want nil", *r.Check)
	}

	out, err := EncodeActors(actors)
	if err != nil {
		t.Fatalf("EncodeActors: %v", err)
	}
	if !strings.Contains(string(out), `"deepseek-v4.1-flash"`) {
		t.Errorf("an on entry must encode as a string:\n%s", out)
	}
	if !strings.Contains(string(out), `"off": true`) {
		t.Errorf("an off entry must encode as an object:\n%s", out)
	}

	back, _, err := ParseActors(out)
	if err != nil {
		t.Fatalf("ParseActors(round trip): %v", err)
	}
	if !reflect.DeepEqual(back, actors) {
		t.Errorf("round trip = %+v, want %+v", back, actors)
	}
}

// TestParseActorsErrors gives one case per §3.2 rule; every case wraps
// ErrBadActors.
func TestParseActorsErrors(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		wantSubstring string
	}{
		{
			name:          "bad actor name",
			body:          `{"Builder": {"agent": "plan-executor"}}`,
			wantSubstring: "Builder: bad actor name",
		},
		{
			name:          "missing agent",
			body:          `{"builder": {"candidates": ["sonnet"]}}`,
			wantSubstring: "builder.agent: required",
		},
		{
			name:          "bad candidate",
			body:          `{"builder": {"agent": "plan-executor", "candidates": ["A/b"]}}`,
			wantSubstring: "builder.candidates[0]",
		},
		{
			name:          "duplicate candidate",
			body:          `{"builder": {"agent": "plan-executor", "candidates": ["sonnet", "sonnet"]}}`,
			wantSubstring: `builder.candidates[1]: duplicate token "sonnet"`,
		},
		{
			name:          "bad tier",
			body:          `{"builder": {"agent": "plan-executor", "tier": "god"}}`,
			wantSubstring: "builder.tier: unknown tier",
		},
		{
			name:          "not json",
			body:          `not json`,
			wantSubstring: "bad actors",
		},
		{
			name:          "top level null",
			body:          `null`,
			wantSubstring: "top-level value must be an object",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := ParseActors([]byte(tt.body))
			if err == nil {
				t.Fatalf("ParseActors(%s) = nil error, want one containing %q", tt.body, tt.wantSubstring)
			}
			if !errors.Is(err, ErrBadActors) {
				t.Errorf("ParseActors(%s) err = %v, want ErrBadActors", tt.body, err)
			}
			if !strings.Contains(err.Error(), tt.wantSubstring) {
				t.Errorf("ParseActors(%s) err = %q, want substring %q", tt.body, err.Error(), tt.wantSubstring)
			}
		})
	}
}

// TestParseAgents gives one case per §3.1 rule. The good cases pin both entry
// forms; the bad ones wrap ErrBadActors.
func TestParseAgents(t *testing.T) {
	good := []struct {
		name string
		body string
	}{
		{
			name: "source",
			body: `{"my-exec": {"source": ` + quote(validSource) + `}}`,
		},
		{
			name: "native",
			body: `{"my-exec": {"shape": "writer", "native": {"claude": {"agent": "my-exec", "requires": ["my-scout"]}}}}`,
		},
	}
	for _, tt := range good {
		t.Run("good/"+tt.name, func(t *testing.T) {
			agents, warnings, err := ParseAgents([]byte(tt.body))
			if err != nil {
				t.Fatalf("ParseAgents(%s): %v", tt.body, err)
			}
			if len(warnings) != 0 {
				t.Errorf("warnings = %v, want none", warnings)
			}
			if _, ok := agents["my-exec"]; !ok {
				t.Errorf("agents = %v, want my-exec", agents)
			}
		})
	}

	bad := []struct {
		name          string
		body          string
		wantSubstring string
	}{
		{
			name:          "source name must equal its key",
			body:          `{"other": {"source": ` + quote(validSource) + `}}`,
			wantSubstring: `source name is "my-exec"`,
		},
		{
			name:          "native needs a shape",
			body:          `{"my-exec": {"native": {"claude": {"agent": "my-exec"}}}}`,
			wantSubstring: "native needs shape",
		},
		{
			name:          "both source and native",
			body:          `{"my-exec": {"source": ` + quote(validSource) + `, "native": {"claude": {"agent": "my-exec"}}}}`,
			wantSubstring: "mutually exclusive",
		},
		{
			name:          "neither source nor native",
			body:          `{"my-exec": {}}`,
			wantSubstring: "one of source or native is required",
		},
		{
			name:          "shipped name as key",
			body:          `{"plan-executor": {"source": ` + quote(validSource) + `}}`,
			wantSubstring: "name is a shipped agent",
		},
		{
			name:          "unknown kind",
			body:          `{"my-exec": {"shape": "reader", "native": {"nope": {"agent": "my-exec"}}}}`,
			wantSubstring: "unknown harness kind",
		},
		{
			name:          "shape with source",
			body:          `{"my-exec": {"shape": "writer", "source": ` + quote(validSource) + `}}`,
			wantSubstring: "shape is only for a native entry",
		},
	}

	for _, tt := range bad {
		t.Run("bad/"+tt.name, func(t *testing.T) {
			_, _, err := ParseAgents([]byte(tt.body))
			if err == nil {
				t.Fatalf("ParseAgents(%s) = nil error, want one containing %q", tt.body, tt.wantSubstring)
			}
			if !errors.Is(err, ErrBadActors) {
				t.Errorf("ParseAgents(%s) err = %v, want ErrBadActors", tt.body, err)
			}
			if !strings.Contains(err.Error(), tt.wantSubstring) {
				t.Errorf("ParseAgents(%s) err = %q, want substring %q", tt.body, err.Error(), tt.wantSubstring)
			}
		})
	}
}

// quote renders s as a JSON string literal for hand-built section bodies.
func quote(s string) string {
	out, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(out)
}

// TestEncodeAgentsSortedKeys pins the encoder's contract: keys sorted, two
// space indent, trailing newline.
func TestEncodeAgentsSortedKeys(t *testing.T) {
	out, err := EncodeAgents(map[string]AgentEntry{
		"zed": {Shape: "reader", Native: map[string]DefRow{"claude": {Agent: "zed"}}},
		"abc": {Source: validSource},
	})
	if err != nil {
		t.Fatalf("EncodeAgents: %v", err)
	}
	s := string(out)
	ia, iz := strings.Index(s, `"abc"`), strings.Index(s, `"zed"`)
	if ia < 0 || iz < 0 || ia > iz {
		t.Errorf("keys are not sorted:\n%s", s)
	}
	if !strings.HasSuffix(s, "\n") {
		t.Errorf("output must end with a newline: %q", s)
	}
	if !strings.Contains(s, "\n  ") {
		t.Errorf("output must be two-space indented: %q", s)
	}
}
