package relevo

import (
	"strings"

	"github.com/fuad-daoud/relevo/internal/refusal"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainResumeReroute names the step a resume re-enters instead of the stopped
// or halted one. A gate-replacing resume on a repair arm -- any step a check's
// red edge names -- goes back to the feeding check, so the check runs again
// under the new command: re-entering the arm would re-send a builder round
// naming the old failure, or halt on the arm's zero budget short of the check.
//
// Every other resume re-enters what it asked for. An explicit --from wins; a
// resume changing no gate has nothing new to run the check under; and a chain
// no red edge reaches resumes as before, with the new gate stored for the
// checks still to come.
//
// The lookup step is the one the state stands on, so a state whose step the
// definition no longer holds cannot ask the question.
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
		return "", refusal.New(
			"chain %s at %s, and %d checks reach it (%s); resume with --from <step> to name the one to rerun",
			st.Status, st.At, len(checks), strings.Join(checks, ", "))
	}
}
