package view

import (
	"testing"
)

// TestStatusLineNodeSuffix pins the burst node suffix: a forwarded round reads
// "opencode@contabo->b12" on both Harness and On, and a round with no node
// reads exactly as before.
func TestStatusLineNodeSuffix(t *testing.T) {
	t.Parallel()

	now := baseTime
	tests := []struct {
		name        string
		b           BindingStatus
		wantHarness string
		wantOn      string
	}{
		{
			name:        "forwarded",
			b:           BindingStatus{Name: "api", Round: 1, BuilderCandidate: "opencode/cline-pass/glm-5.3-flash", Server: "contabo", Node: "b12"},
			wantHarness: "opencode@contabo→b12",
			wantOn:      "opencode@contabo→b12",
		},
		{
			name:        "named forwarded",
			b:           BindingStatus{Name: "api", Round: 1, BuilderCandidate: "opencode/cline-pass/glm-5.3-flash", BuilderName: "glm-5.3-flash", Server: "contabo", Node: "b12"},
			wantHarness: "opencode@contabo→b12",
			wantOn:      "glm-5.3-flash@contabo→b12",
		},
		{
			name:        "remote without node",
			b:           BindingStatus{Name: "api", Round: 1, BuilderCandidate: "opencode/cline-pass/glm-5.3-flash", Server: "contabo"},
			wantHarness: "opencode@contabo",
			wantOn:      "opencode@contabo",
		},
		{
			name:        "local",
			b:           BindingStatus{Name: "api", Round: 1, BuilderCandidate: "opencode/cline-pass/glm-5.3-flash"},
			wantHarness: "opencode",
			wantOn:      "opencode",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rows := StatusLineRows(Report{Bindings: []BindingStatus{tt.b}}, now)
			if len(rows) != 1 {
				t.Fatalf("len(rows) = %d, want 1", len(rows))
			}
			if rows[0].Harness != tt.wantHarness {
				t.Errorf("Harness = %q, want %q", rows[0].Harness, tt.wantHarness)
			}
			if rows[0].On != tt.wantOn {
				t.Errorf("On = %q, want %q", rows[0].On, tt.wantOn)
			}
		})
	}
}
