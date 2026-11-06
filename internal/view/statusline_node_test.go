package view

import (
	"strings"
	"testing"
)

// TestRenderStatusLineKeepsTheNodeWhenCut pins which part of the builder column
// survives a width that cannot hold all of it: the server goes before the node,
// a row that fits is left whole, and a row with no node is cut as any other.
func TestRenderStatusLineKeepsTheNodeWhenCut(t *testing.T) {
	t.Parallel()

	forwarded := BindingStatus{Name: "c1", Round: 1, BuilderCandidate: "muse/meta/muse-spark", BuilderName: "muse-xhigh", Server: "head", Node: "b13"}
	remote := forwarded
	remote.Node = ""

	tests := []struct {
		name string
		b    BindingStatus
		midW int
		want string
	}{
		{"wide enough keeps the whole name", forwarded, 35, "r1 · builder on muse-xhigh@head→b13"},
		{"one short drops the server", forwarded, 34, "r1 · builder on muse-xhigh→b13"},
		{"exactly the short form", forwarded, 30, "r1 · builder on muse-xhigh→b13"},
		{"no node is cut from the right", remote, 25, "r1 · builder on muse-xhi…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := Report{Bindings: []BindingStatus{tt.b}}
			nameW, statusW, clockW := statusLineColumns(StatusLineRows(r, baseTime))
			columns := 2 + nameW + 2 + tt.midW + 1 + statusW + 2 + clockW

			got := stripSGR(RenderStatusLine(r, baseTime, columns))
			if !strings.Contains(got, tt.want) {
				t.Errorf("row = %q, want it to contain %q", got, tt.want)
			}
		})
	}
}

// TestDropServerToFitLeavesOtherTextAlone pins that only the row's own builder
// name is rewritten: a chain row carries no name, and a name with no node has
// nothing to protect.
func TestDropServerToFitLeavesOtherTextAlone(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, mid, on string
	}{
		{"chain row", "chain c1 · plans 1/2 · build@head→b13 long text", ""},
		{"no node", "r1 · builder on muse-xhigh@head and more", "muse-xhigh@head"},
		{"no server", "r1 · builder on muse-xhigh and more", "muse-xhigh"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := dropServerToFit(tt.mid, tt.on, 5); got != tt.mid {
				t.Errorf("dropServerToFit = %q, want it unchanged", got)
			}
		})
	}
}

// TestStatusLineNodeSuffix pins the burst node suffix: a forwarded round reads
// "opencode@contabo→b12" on both Harness and On, and a round with no node
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
