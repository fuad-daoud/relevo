package relevo

import (
	"strings"

	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainResumeReroute names the step a resume re-enters instead of the one the
// chain stopped or halted on. A resume that replaces the gate on a chain
// standing at a repair arm -- any step a check's red edge names -- goes back to
// the check that feeds that arm, so the check actually runs again under the new
// command: re-entering the arm would re-send a builder round whose prompt names
// the old failure, or halt on the zero budget the arm had and never reach the
// check.
//
// Every other resume re-enters what it asked for. An explicit --from wins, so a
// human naming a step is never rerouted away from it; a resume that changes no
// gate has nothing new to run the check under; and a chain standing anywhere
// that no check's red edge reaches resumes exactly as before, with the new gate
// stored for the checks still to come.
//
// The step the check is looked up in is the one the state stands on, so a state
// whose step the definition no longer holds cannot ask the question.
func chainResumeReroute(def workflow.Definition, st workflow.State, opts ResumeOptions) (string, error) {
	if opts.From != "" || !chainResumeChangesGate(opts) {
		return opts.From, nil
	}
	if st.Status != workflow.StatusHalted && st.Status != workflow.StatusStopped {
		return opts.From, nil
	}
	checks := workflow.RedArmChecks(def, st.At)
	switch len(checks) {
	case 0:
		return opts.From, nil
	case 1:
		return checks[0], nil
	default:
		return "", refuse(
			"chain %s at %s, and %d checks reach it (%s); resume with --from <step> to name the one to rerun",
			st.Status, st.At, len(checks), strings.Join(checks, ", "))
	}
}
