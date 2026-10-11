package relevo

import (
	"strings"

	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainResumeReroute names the step a resume re-enters instead of the one the
// chain halted on. A resume that replaces the gate on a chain halted at a
// zero-budget repair arm goes back to the check that arm's budget would have
// paid for, so the check actually runs again under the new command: re-entering
// the arm would halt on the very same zero budget and never reach the check.
//
// Every other resume re-enters what it asked for. An explicit --from wins, so a
// human naming a step is never rerouted away from it; a resume that changes no
// gate has nothing new to run the check under; and a chain halted anywhere that
// is not a red-budget arm resumes exactly as before, with the new gate stored
// for the checks still to come.
//
// The step the check is looked up in is the halted one, so a halted state whose
// step the definition no longer holds cannot ask the question.
func chainResumeReroute(def workflow.Definition, st workflow.State, opts ResumeOptions) (string, error) {
	if opts.From != "" || !chainResumeChangesGate(opts) {
		return opts.From, nil
	}
	if st.Status != workflow.StatusHalted || !workflow.IsZeroBudgetArm(def, st.At) {
		return opts.From, nil
	}
	checks := workflow.RedArmChecks(def, st.At)
	switch len(checks) {
	case 1:
		return checks[0], nil
	case 0:
		return "", refuse(
			"chain halted at %s with no budget left, and no check's red edge reaches it; resume with --from %s to name the step yourself",
			st.At, st.At)
	default:
		return "", refuse(
			"chain halted at %s with no budget left, and %d checks reach it (%s); resume with --from <step> to name the one to rerun",
			st.At, len(checks), strings.Join(checks, ", "))
	}
}
