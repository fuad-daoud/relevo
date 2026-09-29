package harness

import (
	"strings"
	"testing"
)

const historicalArchitectSHA = "68702993acc9ba6f71d5b552fd8a7890af959abd7312c5c6081e651c98835305"

func TestShippedBeforeMatchesHistoricalBlob(t *testing.T) {
	if !ShippedBefore("architect.claude.md", historicalArchitectSHA) {
		t.Errorf("ShippedBefore(architect.claude.md, %s) = false, want true", historicalArchitectSHA)
	}
	if ShippedBefore("reviewer.claude.md", historicalArchitectSHA) {
		t.Error("ShippedBefore(reviewer.claude.md, historical architect sha) = true, want false")
	}
}

func TestShippedBeforeRejectsUnknownAndEmpty(t *testing.T) {
	unknown := strings.Repeat("ab", 32)
	if ShippedBefore("architect.claude.md", unknown) {
		t.Errorf("ShippedBefore(architect.claude.md, %s) = true, want false", unknown)
	}
	if ShippedBefore("architect.claude.md", "") {
		t.Error("ShippedBefore with an empty sha = true, want false")
	}
	if ShippedBefore("", historicalArchitectSHA) {
		t.Error("ShippedBefore with an empty doc = true, want false")
	}
	if ShippedBefore("no-such-file.md", historicalArchitectSHA) {
		t.Error("ShippedBefore with an unknown doc = true, want false")
	}
}

func TestShippedBeforeCoversEveryWorkingTreeDefinition(t *testing.T) {
	for _, h := range All() {
		for _, r := range h.Roles {
			doc, err := AgentDoc(r.Name, h.Kind)
			if err != nil {
				t.Fatalf("AgentDoc(%s, %s): %v", r.Name, h.Kind, err)
			}
			base := agentDocBase(r, h.Kind)
			if !ShippedBefore(base, docSHA(doc)) {
				t.Errorf("ShippedBefore(%s, sha of the shipped %s/%s definition) = false, want true",
					base, h.Kind, r.Name)
			}
		}
	}
}

func TestShippedIndexLinesAreWellFormed(t *testing.T) {
	lines := strings.Split(strings.TrimRight(shippedSHA256, "\n"), "\n")
	if len(lines) == 0 {
		t.Fatal("agents/shipped.sha256 is empty")
	}
	for i, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Errorf("line %d = %q, want exactly two fields", i+1, line)
			continue
		}
		if !isLowerHex64(fields[0]) {
			t.Errorf("line %d sha = %q, want 64 lowercase hex digits", i+1, fields[0])
		}
		if strings.ContainsAny(fields[1], "/ \t") || fields[1] == "" {
			t.Errorf("line %d basename = %q, want a bare file name", i+1, fields[1])
		}
	}
}

func TestIsLowerHex64(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"64 lowercase hex", strings.Repeat("0a", 32), true},
		{"uppercase is not lowercase", strings.Repeat("A", 64), false},
		{"too short", strings.Repeat("a", 63), false},
		{"too long", strings.Repeat("a", 65), false},
		{"non-hex letter", strings.Repeat("a", 63) + "g", false},
		{"empty", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isLowerHex64(tc.in); got != tc.want {
				t.Errorf("isLowerHex64(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
