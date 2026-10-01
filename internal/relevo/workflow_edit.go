package relevo

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// WorkflowEditRound is one pass of the edit loop. It returns the definition to
// store and done=true when edited is a valid workflow, or when edited equals old,
// or is empty: those last two are the loop's no-op ends, with nothing to store.
// Otherwise it returns the problems of an invalid workflow, each prefixed with
// "# " so the caller can reopen the buffer with them at the top as comments.
//
// saved is the workflow as stored: the source the user wrote beside the
// definition add embedded its file: seeds into. A step whose edited source keeps
// the file: reference the stored source carried takes that embedded seed, so an
// edit never replaces embedded contents with the dangling reference. A file:
// reference the stored source never carried is refused, because the file is read
// when a workflow is added and nowhere else.
func WorkflowEditRound(old, edited []byte, saved config.StoredWorkflow, actors map[string]workflow.ActorInfo) (stored []byte, def workflow.Definition, problems []string, done bool) {
	if bytes.Equal(old, edited) || strings.TrimSpace(string(edited)) == "" {
		return nil, workflow.Definition{}, nil, true
	}
	def, err := workflow.Parse(edited)
	if err != nil {
		return nil, workflow.Definition{}, []string{"# " + err.Error()}, false
	}
	def, problems = carryFileSeeds(def, saved)
	if len(problems) > 0 {
		return nil, workflow.Definition{}, problems, false
	}
	env := workflow.Env{
		Actors: actors,
		Given:  nil,
		Seeds:  workflow.ShippedSeeds(),
	}
	found := workflow.Validate(def, env)
	if len(found) == 0 {
		return edited, def, nil, true
	}
	out := make([]string, 0, len(found))
	for _, p := range found {
		out = append(out, "# "+p.String())
	}
	return nil, workflow.Definition{}, out, false
}

// carryFileSeeds keeps the seed add embedded for each edited step whose source
// still names the file: reference the stored source carried. A file: reference
// the stored source never carried -- a new step, or a changed path -- has no
// embedded seed to keep, so it is refused with one problem line per step.
func carryFileSeeds(def workflow.Definition, saved config.StoredWorkflow) (workflow.Definition, []string) {
	from, err := workflow.Parse([]byte(saved.Source))
	if err != nil {
		from = workflow.Definition{}
	}
	steps := make(map[string]workflow.Step, len(def.Steps))
	var problems []string
	for id, step := range def.Steps {
		if strings.HasPrefix(step.Seed, "file:") {
			src, had := from.Steps[id]
			embedded, ok := saved.Definition.Steps[id]
			if !had || src.Seed != step.Seed || !ok {
				problems = append(problems, fmt.Sprintf("# step %s: seed %s was not embedded when the workflow was saved; paste the file's contents, or add the workflow from its file again", id, step.Seed))
				continue
			}
			step.Seed = embedded.Seed
		}
		steps[id] = step
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return workflow.Definition{}, problems
	}
	def.Steps = steps
	return def, nil
}
