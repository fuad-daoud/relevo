package chain

import (
	"strings"

	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// CheckCommands returns the distinct non-empty check commands a workflow's
// check steps render.
func CheckCommands(def workflow.Definition) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range workflow.ResumeTargets(def) {
		step := def.Steps[id]
		if step.Check == "" {
			continue
		}
		if cmd := workflow.RenderParams(def, step.Check); cmd != "" && !seen[cmd] {
			seen[cmd] = true
			out = append(out, cmd)
		}
	}
	return out
}

// SingleCheckCommand is the one non-empty check command a placed writer's
// binding carries as its own gate, or "" when the workflow does not have
// exactly one. A placed writer's binding keeps this gate so a chain started
// before the check route can still be answered from the record the writer's
// newest closed round carries.
func SingleCheckCommand(def workflow.Definition) string {
	cmds := CheckCommands(def)
	if len(cmds) == 1 {
		return cmds[0]
	}
	return ""
}

// CheckReport is the check a chain's start line names: the commands its
// check steps run, local or placed, with where the command came from. A
// member's own gate is no guide, since a local writer carries none.
func CheckReport(def workflow.Definition, res resolve.GateResolution, params map[string]string) (string, string) {
	cmds := CheckCommands(def)
	switch {
	case len(cmds) == 0:
		return "", ""
	case len(cmds) > 1:
		return strings.Join(cmds, "; "), "workflow"
	case params["gate"] == cmds[0]:
		return cmds[0], "--param gate"
	case res.Command == cmds[0]:
		return cmds[0], res.Source
	}
	return cmds[0], "workflow"
}
