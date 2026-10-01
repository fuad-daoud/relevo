package relevo

import (
	"bytes"
	"strings"

	"github.com/fuad-daoud/relevo/internal/workflow"
)

// WorkflowEditRound is one pass of the edit loop. It returns the source to store
// and done=true when edited is a valid workflow, or when edited equals old, or
// is empty: those last two are the loop's no-op ends, with nothing to store.
// Otherwise it returns the problems of an invalid workflow, each prefixed with
// "# " so the caller can reopen the buffer with them at the top as comments.
func WorkflowEditRound(old, edited []byte, actors map[string]workflow.ActorInfo) (stored []byte, problems []string, done bool) {
	if bytes.Equal(old, edited) || strings.TrimSpace(string(edited)) == "" {
		return nil, nil, true
	}
	def, err := workflow.Parse(edited)
	if err != nil {
		return nil, []string{"# " + err.Error()}, false
	}
	env := workflow.Env{
		Actors: actors,
		Given:  nil,
		Seeds:  workflow.ShippedSeeds(),
	}
	found := workflow.Validate(def, env)
	if len(found) == 0 {
		return edited, nil, true
	}
	out := make([]string, 0, len(found))
	for _, p := range found {
		out = append(out, "# "+p.String())
	}
	return nil, out, false
}
