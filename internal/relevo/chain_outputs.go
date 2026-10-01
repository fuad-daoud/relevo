package relevo

import (
	"strconv"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainGateResult is the builder close's gate word: none when no check ran,
// green when one passed, red otherwise. A red gate has already spent the
// regate budget, so it still reaches the reviewer rather than a halt.
func chainGateResult(gate *store.GateRecord) string {
	if gate == nil {
		return chain.GateNone
	}
	if gate.Result == "pass" {
		return chain.GateGreen
	}
	return chain.GateRed
}

// chainFillReaderClose populates ev for a closing reader member.
func chainFillReaderClose(rt Runtime, b store.Binding, body []byte, part string, ev *chain.Event) {
	actor := BindingRole(b)
	info, _ := rt.RoleRegistry().ActorInfo(actor)
	switch part {
	case chain.MemberReviewer:
		ev.Kind = chain.EventReviewerClosed
		values, _ := chainParseOutcomes(rt, b, body, info.Outputs)
		if v, ok := values["verdict"]; ok {
			ev.Verdict = chain.Verdict(v)
		}
	case chain.MemberPlanner:
		ev.Kind = chain.EventPlannerClosed
		var size int64
		if s, _, ok, err := rt.Store.StatFile(reportPathFor(rt, b)); err == nil && ok {
			size = s
		}
		sizes := make(map[string]int64)
		for _, art := range info.Outputs.Artifacts() {
			sizes[art] = size
		}
		if len(info.Outputs.Artifacts()) > 0 && workflow.MissingArtifact(info.Outputs, sizes) == "" {
			ev.PlanPresent = true
		}
	case chain.MemberSecurity:
		ev.Kind = chain.EventSecurityClosed
		values, _ := chainParseOutcomes(rt, b, body, info.Outputs)
		if val, ok := values["findings"]; ok {
			if n, err := strconv.Atoi(val); err == nil {
				ev.Findings = n
				ev.FindingsGiven = true
			}
		}
	}
}
