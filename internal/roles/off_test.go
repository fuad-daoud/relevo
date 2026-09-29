package roles

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/policy"
)

// TestOffEntriesRanked pins A2 §3.4: an off entry keeps its position in
// Ranked, Off is true, OffCount counts it, and Serves is still true -- an
// explicit `--candidate <off one>` is still served.
func TestOffEntriesRanked(t *testing.T) {
	set := setFromJSON(t, `[
	  {"harness":"claude","provider":"test","model":"a","roles":["builder"]},
	  {"harness":"claude","provider":"test","model":"b","roles":["builder"]},
	  {"harness":"claude","provider":"test","model":"c","roles":["builder"]}
	]`)
	reg, err := Build(&File{Rows: map[string]Row{
		"builder": {
			Candidates: []string{"claude/test/a", "claude/test/b", "claude/test/c"},
			Off:        []string{"claude/test/b"},
		},
	}}, set, policy.Policy{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	role, ok := reg.Role("builder")
	if !ok {
		t.Fatal("Role(builder) not found")
	}
	if len(role.Ranked) != 3 {
		t.Fatalf("Ranked has %d entries, want 3: %+v", len(role.Ranked), role.Ranked)
	}
	b := role.Ranked[1]
	if b.Token != "claude/test/b" || b.Position != 2 || !b.Off {
		t.Errorf("Ranked[1] = %+v, want claude/test/b at position 2, off", b)
	}
	if role.Ranked[0].Off || role.Ranked[2].Off {
		t.Errorf("only b is off: %+v", role.Ranked)
	}
	if role.OffCount != 1 {
		t.Errorf("OffCount = %d, want 1", role.OffCount)
	}

	ref, err := candidate.ParseRef("claude/test/b")
	if err != nil {
		t.Fatalf("ParseRef: %v", err)
	}
	if !reg.Serves("builder", ref) {
		t.Error("Serves(builder, claude/test/b) = false, want true: an off entry stays in Resolved")
	}
	found := false
	for _, tok := range role.Resolved {
		if tok == "claude/test/b" {
			found = true
		}
	}
	if !found {
		t.Errorf("Resolved = %v, want it to include claude/test/b", role.Resolved)
	}
}
