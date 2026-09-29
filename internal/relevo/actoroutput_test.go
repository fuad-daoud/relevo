package relevo

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/roles"
)

// TestActorOutputShippedLabels pins the shipped output labels (A5 R4a):
// reviewer gives findings, researcher notes, architect plan, and an agent that
// names none falls back to defaultOutput.
func TestActorOutputShippedLabels(t *testing.T) {
	t.Parallel()

	cases := []struct{ actor, want string }{
		{"reviewer", "findings"},
		{"researcher", "notes"},
		{"architect", "plan"},
	}
	for _, c := range cases {
		if got := ActorOutput(nil, nil, c.actor, c.actor); got != c.want {
			t.Errorf("ActorOutput(%s) = %q, want %q", c.actor, got, c.want)
		}
	}

	if got := ActorOutput(nil, nil, "custom", "no-such-agent"); got != defaultOutput {
		t.Errorf("ActorOutput(unknown) = %q, want %q", got, defaultOutput)
	}

	// An actor's own agent wins over the definition it was launched with.
	actors := map[string]roles.Actor{"custom": {Agent: "researcher"}}
	if got := ActorOutput(actors, nil, "custom", "reviewer"); got != "notes" {
		t.Errorf("ActorOutput(custom->researcher) = %q, want notes", got)
	}
}
