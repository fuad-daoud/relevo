package workflow

import "strconv"

// Legacy is a chain as a database row written before workflows carried them
// wrote it: the fixed phase/step/plan columns, the settings its params resolve
// from, and the builder it names. It mirrors what the chain package stores
// without importing it, so this package stays pure.
type Legacy struct {
	Status        string
	Reason        string
	Phase         string
	Step          string
	Plan          int
	Plans         int
	Corrections   int
	AwaitingRound int
	PlanPaths     []string
	Settings      LegacySettings
	Builder       string
}

// LegacySettings mirrors the chain package's stored settings JSON: the
// resolved values a chain started with.
type LegacySettings struct {
	MaxCorrections int
	ReviewerActor  string
	PlannerActor   string
	SecurityActor  string
	Security       bool
	Gate           string
	Regate         int
}

// The legacy phase and step words a chain row stores. They are literals here
// because this package reads a row's text, not the chain package's types.
const (
	legacyPhaseSecurity  = "security"
	legacyStepBuilding   = "building"
	legacyStepReviewing  = "reviewing"
	legacyStepCorrecting = "correcting"
)

// FromLegacy converts a legacy chain row into the shipped default definition,
// with the row's settings resolved onto its params, and the engine state the
// run continues from.
func FromLegacy(l Legacy) (Definition, State, error) {
	def, err := legacyDefinition(l)
	if err != nil {
		return Definition{}, State{}, err
	}
	at := legacyStepAt(l)
	s := State{
		Status:  Status(l.Status),
		Reason:  l.Reason,
		At:      at,
		Visits:  map[string]int{},
		Iter:    map[string]Iter{"plans": {Index: l.Plan - 1, Items: append([]string(nil), l.PlanPaths...)}},
		Results: map[string]Result{},
	}
	corrections := l.Corrections
	if at == "correct" || at == "fix-correct" {
		corrections++
	}
	key := "correct"
	if legacySecurityStep(at) {
		key = "fix-correct"
	}
	s.Visits[key] = corrections
	if at != "" {
		s.Awaiting = Awaiting{Step: at, Member: legacyActorOfStep(def, at), Round: l.AwaitingRound}
	}
	return def, s, nil
}

// legacyDefinition is the shipped default with the row's settings resolved onto
// its params: the actor names only when the row named one, so a blank setting
// keeps the default's own value.
func legacyDefinition(l Legacy) (Definition, error) {
	values := map[string]string{
		"scan":            strconv.FormatBool(l.Settings.Security),
		"gate":            l.Settings.Gate,
		"regate":          strconv.Itoa(l.Settings.Regate),
		"max_corrections": strconv.Itoa(l.Settings.MaxCorrections),
	}
	if l.Builder != "" {
		values["builder"] = l.Builder
	}
	if l.Settings.ReviewerActor != "" {
		values["reviewer"] = l.Settings.ReviewerActor
	}
	if l.Settings.PlannerActor != "" {
		values["planner"] = l.Settings.PlannerActor
	}
	if l.Settings.SecurityActor != "" {
		values["security"] = l.Settings.SecurityActor
	}
	return WithParams(Default(), values)
}

// legacyStepAt is the workflow step a legacy row's step migrates to. A row that
// has not spent a correction is on the plan's own build; the ones it has spent
// read on the rebuilding step.
func legacyStepAt(l Legacy) string {
	security := l.Phase == legacyPhaseSecurity
	switch l.Step {
	case legacyStepBuilding:
		if security {
			if l.Corrections == 0 {
				return "fix-build"
			}
			return "fix-rebuild"
		}
		if l.Corrections == 0 {
			return "build"
		}
		return "build-fix"
	case legacyStepReviewing:
		if security {
			return "fix-review"
		}
		return "review"
	case legacyStepCorrecting:
		if security {
			return "fix-correct"
		}
		return "correct"
	case "scanning":
		return "scan"
	case "planning-fixes":
		return "fix-plan"
	}
	return ""
}

// legacyActorOfStep is the actor a step runs, from the definition's params.
func legacyActorOfStep(def Definition, at string) string {
	switch at {
	case "plans", "build", "build-fix", "check", "repair", "fix-build", "fix-rebuild", "fix-check", "fix-repair":
		return def.Params["builder"].Str
	case "review", "fix-review":
		return def.Params["reviewer"].Str
	case "correct", "fix-correct", "fix-plan":
		return def.Params["planner"].Str
	case "scan", "scan-gate":
		return def.Params["security"].Str
	}
	return ""
}

// LegacyFields is the legacy view of a workflow state: the fixed columns a
// reader of the old chain shape reads.
type LegacyFields struct {
	Phase          string
	Step           string
	Plan           int
	Plans          int
	Corrections    int
	AwaitingMember string
	AwaitingRound  int
}

// LegacyView projects a workflow state back onto the legacy columns. It is
// FromLegacy's inverse over the columns it can recover: a finished run reads
// the finished phase, and a state at a rebuilding step reads the phase its step
// belongs to.
func LegacyView(def Definition, s State) LegacyFields {
	phase, step := legacyColumnsOf(s.At)
	if s.Status == StatusDone {
		phase = "finished"
	}
	// An exhausted walk holds index -1, the same as one that has not started:
	// Done tells them apart, and an exhausted walk reads the last plan, the
	// plan the legacy path leaves at done.
	plan := s.Iter["plans"].Index + 1
	if it := s.Iter["plans"]; it.Done && len(it.Items) > 0 {
		plan = len(it.Items)
	}
	if plan < 1 {
		plan = 1
	}
	return LegacyFields{
		Phase:          phase,
		Step:           step,
		Plan:           plan,
		Plans:          len(s.Iter["plans"].Items),
		Corrections:    legacyCorrections(phase, step, s),
		AwaitingMember: legacyPartOfActor(def, legacyActorOfStep(def, s.At)),
		AwaitingRound:  s.Awaiting.Round,
	}
}

// legacyCorrections is the spent-correction count a state holds: the budget the
// current phase spends, less one while the chain sits on a correct step, since
// entering that step has counted the correction it is about to spend.
func legacyCorrections(phase, step string, s State) int {
	corrections := s.Visits["correct"]
	if phase == legacyPhaseSecurity {
		corrections = s.Visits["fix-correct"]
	}
	if step == legacyStepCorrecting {
		corrections--
	}
	if corrections < 0 {
		return 0
	}
	return corrections
}

// legacyColumnsOf maps a workflow step id back to the legacy phase and step
// words. An unknown step reads as the build phase's building step.
func legacyColumnsOf(at string) (phase, step string) {
	phase = "build"
	if legacySecurityStep(at) {
		phase = legacyPhaseSecurity
	}
	return phase, legacyStepWord(at)
}

// legacyStepWord is the legacy step word a workflow step id reads as.
func legacyStepWord(at string) string {
	switch at {
	case "build", "build-fix", "repair", "check", "plans", "fix-build", "fix-rebuild", "fix-repair", "fix-check":
		return legacyStepBuilding
	case "review", "fix-review":
		return legacyStepReviewing
	case "correct", "fix-correct":
		return legacyStepCorrecting
	case "scan", "scan-gate":
		return "scanning"
	case "fix-plan":
		return "planning-fixes"
	}
	return legacyStepBuilding
}

// legacySecurityStep reports whether a workflow step id belongs to the security
// phase, which the fix-prefixed steps and the scan spell.
func legacySecurityStep(at string) bool {
	switch at {
	case "scan", "scan-gate", "fix-plan", "fix-build", "fix-rebuild", "fix-repair", "fix-check", "fix-review", "fix-correct":
		return true
	}
	return false
}

// legacyPartOfActor is the member part an actor fills: the old chain names a
// member by the part it fills, not by the actor it runs.
func legacyPartOfActor(def Definition, actor string) string {
	for _, part := range []string{"builder", "reviewer", "planner", "security"} {
		if actor != "" && def.Params[part].Str == actor {
			return part
		}
	}
	return ""
}
