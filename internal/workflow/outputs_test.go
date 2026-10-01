package workflow

import (
	"reflect"
	"strings"
	"testing"
)

func TestOutputParsesEachKind(t *testing.T) {
	t.Parallel()

	want := Outputs{
		"answer":   {Kind: OutputOneOf, Values: []string{"yes", "no"}},
		"findings": {Kind: OutputCount},
		"report":   {Kind: OutputArtifact},
	}
	cases := []struct {
		name string
		in   string
	}{
		{"yaml", "answer: { one-of: [yes, no] }\nfindings: count\nreport: artifact\n"},
		{"json", `{"answer": {"one-of": ["yes", "no"]}, "findings": "count", "report": "artifact"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseOutputs([]byte(tc.in))
			if err != nil {
				t.Fatalf("ParseOutputs(%q) error: %v", tc.in, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("ParseOutputs(%q) = %+v, want %+v", tc.in, got, want)
			}
			if outcomes := got.Outcomes(); !reflect.DeepEqual(outcomes, []string{"answer", "findings"}) {
				t.Errorf("Outcomes() = %v, want [answer findings]", outcomes)
			}
			if artifacts := got.Artifacts(); !reflect.DeepEqual(artifacts, []string{"report"}) {
				t.Errorf("Artifacts() = %v, want [report]", artifacts)
			}
		})
	}
}

func TestOutputRejectsAnEmptyOneOf(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		in   string
	}{
		{"empty", "x: { one-of: [] }"},
		{"duplicate values", "x: { one-of: [yes, yes] }"},
		{"a value of the wrong type", "x: { one-of: [yes, 2] }"},
		{"an unknown scalar", "x: nope"},
		{"an object with no one-of", "x: {}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got, err := ParseOutputs([]byte(tc.in)); err == nil {
				t.Fatalf("ParseOutputs(%q) = %+v, want an error", tc.in, got)
			}
		})
	}
}

func TestFooterOneOf(t *testing.T) {
	t.Parallel()

	o := Outputs{"answer": {Kind: OutputOneOf, Values: []string{"yes", "no"}}}
	want := "```relevo\nanswer: yes   # or: no\n```\n"
	if got := Footer(o, nil); got != want {
		t.Fatalf("Footer() = %q, want %q", got, want)
	}
}

func TestFooterCount(t *testing.T) {
	t.Parallel()

	o := Outputs{"findings": {Kind: OutputCount}}
	want := "```relevo\nfindings: 0   # a count\n```\n"
	if got := Footer(o, nil); got != want {
		t.Fatalf("Footer() = %q, want %q", got, want)
	}
}

func TestFooterArtifact(t *testing.T) {
	t.Parallel()

	plan := Outputs{"plan": {Kind: OutputArtifact}}
	if got, want := Footer(plan, map[string]string{"plan": "plans/one.md"}), "Write plan to plans/one.md.\n"; got != want {
		t.Fatalf("Footer() = %q, want %q", got, want)
	}
	if got, want := Footer(plan, nil), "Write plan to plan.md.\n"; got != want {
		t.Fatalf("Footer() with no path = %q, want %q", got, want)
	}

	mixed := Outputs{
		"plan":    {Kind: OutputArtifact},
		"verdict": {Kind: OutputOneOf, Values: []string{"pass", "changes"}},
	}
	want := "Write plan to p.md.\n\n```relevo\nverdict: pass   # or: changes\n```\n"
	if got := Footer(mixed, map[string]string{"plan": "p.md"}); got != want {
		t.Fatalf("Footer(mixed) = %q, want %q", got, want)
	}
}

func TestFooterWithNoOutcomesHasNoBlock(t *testing.T) {
	t.Parallel()

	if got := Footer(nil, nil); got != "" {
		t.Fatalf("Footer(nil) = %q, want empty", got)
	}
	artifacts := Outputs{"plan": {Kind: OutputArtifact}}
	if got := Footer(artifacts, nil); strings.Contains(got, "```relevo") {
		t.Fatalf("Footer(%+v) = %q, want no relevo block", artifacts, got)
	}
}
