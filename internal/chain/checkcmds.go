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

// CleanupCommands returns the distinct non-empty cleanup commands a
// workflow's cleanup steps render.
func CleanupCommands(def workflow.Definition) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range workflow.ResumeTargets(def) {
		step := def.Steps[id]
		if step.Cleanup == "" {
			continue
		}
		if cmd := workflow.RenderParams(def, step.Cleanup); cmd != "" && !seen[cmd] {
			seen[cmd] = true
			out = append(out, cmd)
		}
	}
	return out
}

// CleanupReport is the cleanup a chain's start line names: the commands its
// cleanup steps run, with where the command came from and when it runs. cmd
// and source carry the resolved cleanup, after its when word, and params the
// --param overrides; the resolution's own tiers decide the source exactly as
// the check report's do.
func CleanupReport(def workflow.Definition, cmd, source, after string, params map[string]string) (string, string, string) {
	cmds := CleanupCommands(def)
	when := cleanupWhen(def, after)
	switch {
	case len(cmds) == 0:
		return "", "", ""
	case len(cmds) > 1:
		return strings.Join(cmds, "; "), "workflow", when
	case params["cleanup_round"] == cmds[0]:
		return cmds[0], "--param cleanup_round", when
	case params["cleanup_chain"] == cmds[0]:
		return cmds[0], "--param cleanup_chain", when
	case cmd == cmds[0]:
		return cmds[0], source, when
	}
	return cmds[0], "workflow", when
}

// cleanupWhen names when a workflow's cleanups run: the round phase, the
// chain end, or both, read off the rendered cleanup params. A workflow whose
// cleanup steps name no cleanup param keeps the resolution's own after word,
// so a literal cleanup still reports the configured when.
func cleanupWhen(def workflow.Definition, after string) string {
	round, chain := "", ""
	if HasParam(def, "cleanup_round") {
		round = workflow.RenderParams(def, "{{params.cleanup_round}}")
	}
	if HasParam(def, "cleanup_chain") {
		chain = workflow.RenderParams(def, "{{params.cleanup_chain}}")
	}
	switch {
	case round != "" && chain != "":
		return "both"
	case round != "":
		return "round"
	case chain != "":
		return "chain"
	}
	return after
}
