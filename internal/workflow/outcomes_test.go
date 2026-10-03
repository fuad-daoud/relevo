package workflow

import (
	"reflect"
	"strings"
	"testing"
)

// outcomeBlock renders one relevo block carrying keys, prefixed with prose so a
// test reads it as a message body.
func outcomeBlock(keys ...string) []byte {
	return []byte("prose\n\n```relevo\n" + strings.Join(keys, "\n") + "\n```\n")
}

// verdictAndFindings is the shipped reader's declaration.
func verdictAndFindings() Outputs {
	return Outputs{
		"verdict":  {Kind: OutputOneOf, Values: []string{"pass", "changes"}},
		"findings": {Kind: OutputCount},
	}
}

func TestParseOutcomesPresent(t *testing.T) {
	t.Parallel()

	got, reason := ParseOutcomes(verdictAndFindings(), outcomeBlock("verdict: changes", "findings: 3"))
	if reason != "" {
		t.Fatalf("reason = %q, want empty", reason)
	}
	want := map[string]string{"verdict": "changes", "findings": "3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseOutcomes() = %v, want %v", got, want)
	}
}

// fencelessOutcomeBlock renders the shipped closing block with its backtick
// fences lost: a bare `relevo` line, the key lines, and no closing fence.
func fencelessOutcomeBlock(keys ...string) []byte {
	return []byte("prose\n\nrelevo\n" + strings.Join(keys, "\n") + "\n")
}

// TestParseOutcomesFencelessBlock pins the closing block whose backtick fences
// were lost: a valid declared value parses as if the block were fenced, and an
// invalid one still halts, so the tolerance never weakens validation.
func TestParseOutcomesFencelessBlock(t *testing.T) {
	t.Parallel()

	o := Outputs{"findings": {Kind: OutputCount}}
	got, reason := ParseOutcomes(o, fencelessOutcomeBlock("findings: 0"))
	if reason != "" {
		t.Fatalf("ParseOutcomes(fenceless findings: 0) reason = %q, want empty", reason)
	}
	if got["findings"] != "0" {
		t.Fatalf("findings = %q, want 0", got["findings"])
	}

	_, reason = ParseOutcomes(o, fencelessOutcomeBlock("findings: many"))
	if want := `findings: "many" is not a count`; reason != want {
		t.Fatalf("ParseOutcomes(fenceless findings: many) reason = %q, want %q", reason, want)
	}
}

func TestParseOutcomesMissing(t *testing.T) {
	t.Parallel()

	o := Outputs{"verdict": {Kind: OutputOneOf, Values: []string{"pass", "changes"}}}
	for _, bodies := range [][][]byte{
		nil,
		{outcomeBlock("status: done")},
		{[]byte("plain prose with no block\n")},
	} {
		got, reason := ParseOutcomes(o, bodies...)
		if got != nil {
			t.Errorf("ParseOutcomes(%q) = %v, want nil", bodies, got)
		}
		if want := "verdict: no relevo block carries it"; reason != want {
			t.Errorf("ParseOutcomes(%q) reason = %q, want %q", bodies, reason, want)
		}
	}
}

func TestParseOutcomesOutOfRange(t *testing.T) {
	t.Parallel()

	got, reason := ParseOutcomes(verdictAndFindings(), outcomeBlock("verdict: maybe", "findings: 0"))
	if got != nil {
		t.Fatalf("ParseOutcomes() = %v, want nil", got)
	}
	want := `verdict: "maybe" is not one of pass, changes`
	if reason != want {
		t.Fatalf("reason = %q, want %q", reason, want)
	}
}

func TestParseOutcomesCountRejectsNegativeAndNonInteger(t *testing.T) {
	t.Parallel()

	o := Outputs{"findings": {Kind: OutputCount}}
	for _, val := range []string{"-1", "many", "1.5", ""} {
		got, reason := ParseOutcomes(o, outcomeBlock("findings: "+val))
		if got != nil {
			t.Errorf("findings %q: ParseOutcomes() = %v, want nil", val, got)
		}
		want := `findings: "` + val + `" is not a count`
		if reason != want {
			t.Errorf("findings %q: reason = %q, want %q", val, reason, want)
		}
	}
}

func TestParseOutcomesRecapAfterTheBlockIsHarmless(t *testing.T) {
	t.Parallel()

	recap := []byte("# Reviewer recap\n\nI read the plan and the report; the block was written above.\n")
	got, reason := ParseOutcomes(verdictAndFindings(), recap, outcomeBlock("verdict: pass", "findings: 0"))
	if reason != "" {
		t.Fatalf("reason = %q, want empty", reason)
	}
	want := map[string]string{"verdict": "pass", "findings": "0"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseOutcomes() = %v, want %v", got, want)
	}
}

func TestParseOutcomesFallsBackToAnOlderBody(t *testing.T) {
	t.Parallel()

	o := Outputs{"verdict": {Kind: OutputOneOf, Values: []string{"pass", "changes"}}}
	cases := []struct {
		name   string
		newer  []byte
		reason string
	}{
		{"the newer body does not carry the key", outcomeBlock("status: done"), ""},
		{"the newer body carries an invalid value", outcomeBlock("verdict: maybe"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, reason := ParseOutcomes(o, tc.newer, outcomeBlock("verdict: changes"))
			if reason != "" {
				t.Fatalf("reason = %q, want empty", reason)
			}
			if got["verdict"] != "changes" {
				t.Fatalf("verdict = %q, want changes", got["verdict"])
			}
		})
	}
}

func TestMissingArtifactNamesTheEmptyOne(t *testing.T) {
	t.Parallel()

	o := Outputs{
		"plan":   {Kind: OutputArtifact},
		"report": {Kind: OutputArtifact},
	}
	cases := []struct {
		name  string
		sizes map[string]int64
		want  string
	}{
		{"the first sorted artifact is empty", map[string]int64{"plan": 0, "report": 10}, "plan: not written or empty"},
		{"the absent artifact is named", map[string]int64{"plan": 10}, "report: not written or empty"},
		{"all present", map[string]int64{"plan": 10, "report": 1}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := MissingArtifact(o, tc.sizes); got != tc.want {
				t.Fatalf("MissingArtifact(%v) = %q, want %q", tc.sizes, got, tc.want)
			}
		})
	}

	if got := MissingArtifact(nil, nil); got != "" {
		t.Fatalf("MissingArtifact(nil) = %q, want empty", got)
	}
}
