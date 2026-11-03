package relevo

import (
	"reflect"
	"strings"
	"testing"
)

// TestParseChainCandidatesPinsFourParts pins the accepted shape: one
// part=value entry per member part lands in the map keyed by part.
func TestParseChainCandidatesPinsFourParts(t *testing.T) {
	t.Parallel()

	got, err := ParseChainCandidates([]string{
		"builder=opencode/test/m",
		"reviewer=claude/test/m",
		"planner=claude/test/m",
		"security=claude/test/m",
	})
	if err != nil {
		t.Fatalf("ParseChainCandidates: %v", err)
	}
	want := map[string]string{
		"builder":  "opencode/test/m",
		"reviewer": "claude/test/m",
		"planner":  "claude/test/m",
		"security": "claude/test/m",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pins = %v, want %v", got, want)
	}
}

// TestParseChainCandidatesRefusesBadEntries pins every refusal: an unknown
// part and a repeated part name the four parts, and an empty part or value
// is refused before anything is created.
func TestParseChainCandidatesRefusesBadEntries(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		entries []string
		want    []string
	}{
		{
			name:    "unknown part names the four parts",
			entries: []string{"writer=opencode/test/m"},
			want:    []string{`"writer"`, "builder", "reviewer", "planner", "security"},
		},
		{
			name:    "same part twice names the four parts",
			entries: []string{"builder=opencode/test/m", "builder=claude/test/m"},
			want:    []string{`"builder"`, "twice", "builder", "reviewer", "planner", "security"},
		},
		{
			name:    "empty value",
			entries: []string{"builder="},
			want:    []string{"--candidate", "part=value"},
		},
		{
			name:    "empty part",
			entries: []string{"=opencode/test/m"},
			want:    []string{"--candidate", "part=value"},
		},
		{
			name:    "missing equals",
			entries: []string{"builder"},
			want:    []string{"--candidate", "part=value"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseChainCandidates(tc.entries); err == nil {
				t.Fatalf("ParseChainCandidates(%q) = nil, want a refusal", tc.entries)
			} else {
				for _, want := range tc.want {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("err = %q, want it to name %q", err.Error(), want)
					}
				}
			}
		})
	}
}

// TestParseChainCandidatesEmptyPinsNothing pins the zero case: no flag
// means no pins, so every member resolves by the policy pick.
func TestParseChainCandidatesEmptyPinsNothing(t *testing.T) {
	t.Parallel()

	if got, err := ParseChainCandidates(nil); err != nil || got != nil {
		t.Errorf("ParseChainCandidates(nil) = %v, %v; want nil, nil", got, err)
	}
}
