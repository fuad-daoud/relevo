package relevo

import (
	"strconv"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainValidateDefault validates the default workflow against the runtime's
// actor registry with the chain's resolved settings and given inputs.
func chainValidateDefault(rt Runtime, set chain.Settings, given workflow.Given) error {
	def := workflow.Default()
	values := map[string]string{
		"builder":         "builder",
		"scan":            strconv.FormatBool(set.Security),
		"gate":            set.Gate,
		"regate":          strconv.Itoa(set.Regate),
		"max_corrections": strconv.Itoa(set.MaxCorrections),
	}
	if set.ReviewerActor != "" {
		values["reviewer"] = set.ReviewerActor
	}
	if set.PlannerActor != "" {
		values["planner"] = set.PlannerActor
	}
	if set.SecurityActor != "" {
		values["security"] = set.SecurityActor
	}
	applied, err := workflow.WithParams(def, values)
	if err != nil {
		return refuse("%v", err)
	}
	env := workflow.Env{
		Actors: rt.RoleRegistry().WorkflowActors(),
		Given:  &given,
		Seeds:  workflow.ShippedSeeds(),
	}
	problems := workflow.Validate(applied, env)
	if len(problems) > 0 {
		return refuse("%s", problems[0])
	}
	return nil
}

// resumeAwaitedOpenRoundRefusal refuses a resume whose awaited member's round is
// still open: the human must stop it first.
func resumeAwaitedOpenRoundRefusal(rt Runtime, c db.ChainRow) error {
	member := chainMemberName(c, c.AwaitingMember)
	if member == "" {
		return nil
	}
	b, err := rt.Store.Load(member)
	if err != nil {
		return nil
	}
	entries, err := rt.Store.ReadLog(member)
	if err != nil {
		return nil
	}
	if roundOpenIn(entries, b.Round) {
		return &RoundOpenError{Member: member, Round: b.Round}
	}
	return nil
}
