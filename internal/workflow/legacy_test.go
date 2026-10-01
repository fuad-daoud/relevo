package workflow

import (
	"reflect"
	"testing"
)

// legacyCase is one old chain step and the state it migrates to. actor is the
// workflow actor the step runs; part is the legacy member name it fills.
type legacyCase struct {
	name        string
	status      string
	phase       string
	step        string
	corrections int
	at          string
	visitsKey   string
	visits      int
	actor       string
	part        string
}

// legacyCases covers every old step, in both phases, with and without a spent
// correction.
var legacyCases = []legacyCase{
	{"build phase, building, no correction", "running", "build", "building", 0, "build", "correct", 0, "builder", "builder"},
	{"build phase, building, after corrections", "running", "build", "building", 2, "build-fix", "correct", 2, "builder", "builder"},
	{"build phase, reviewing", "running", "build", "reviewing", 0, "review", "correct", 0, "reviewer", "reviewer"},
	{"build phase, reviewing, after corrections", "running", "build", "reviewing", 2, "review", "correct", 2, "reviewer", "reviewer"},
	{"build phase, correcting", "running", "build", "correcting", 0, "correct", "correct", 1, "lite-planner", "planner"},
	{"build phase, correcting, after corrections", "running", "build", "correcting", 2, "correct", "correct", 3, "lite-planner", "planner"},
	{"security phase, building, no correction", "running", "security", "building", 0, "fix-build", "fix-correct", 0, "builder", "builder"},
	{"security phase, building, after corrections", "running", "security", "building", 2, "fix-rebuild", "fix-correct", 2, "builder", "builder"},
	{"security phase, reviewing", "running", "security", "reviewing", 0, "fix-review", "fix-correct", 0, "reviewer", "reviewer"},
	{"security phase, reviewing, after corrections", "running", "security", "reviewing", 2, "fix-review", "fix-correct", 2, "reviewer", "reviewer"},
	{"security phase, correcting", "running", "security", "correcting", 0, "fix-correct", "fix-correct", 1, "lite-planner", "planner"},
	{"security phase, correcting, after corrections", "running", "security", "correcting", 2, "fix-correct", "fix-correct", 3, "lite-planner", "planner"},
	{"security phase, scanning", "running", "security", "scanning", 0, "scan", "fix-correct", 0, "security", "security"},
	{"security phase, scanning, after corrections", "running", "security", "scanning", 2, "scan", "fix-correct", 2, "security", "security"},
	{"security phase, planning fixes", "running", "security", "planning-fixes", 0, "fix-plan", "fix-correct", 0, "lite-planner", "planner"},
	{"security phase, planning fixes, after corrections", "running", "security", "planning-fixes", 2, "fix-plan", "fix-correct", 2, "lite-planner", "planner"},
}

// legacyOf is the row a case describes, on plan 2 of 3 and awaiting the
// seventh member round.
func legacyOf(tc legacyCase) Legacy {
	return Legacy{
		Status:        tc.status,
		Phase:         tc.phase,
		Step:          tc.step,
		Plan:          2,
		Plans:         3,
		Corrections:   tc.corrections,
		AwaitingRound: 7,
		PlanPaths:     []string{"plan-1.md", "plan-2.md", "plan-3.md"},
		Settings:      LegacySettings{MaxCorrections: 3, Security: true, Gate: "make check", Regate: 1},
	}
}

// TestFromLegacyEveryStep pins the migration mapping: one case per old step, in
// each phase, with and without spent corrections.
func TestFromLegacyEveryStep(t *testing.T) {
	for _, tc := range legacyCases {
		t.Run(tc.name, func(t *testing.T) {
			def, s, err := FromLegacy(legacyOf(tc))
			if err != nil {
				t.Fatalf("FromLegacy: %v", err)
			}
			if def.Steps[tc.at].Run == "" {
				t.Errorf("the definition has no step %q", tc.at)
			}
			if s.Status != Status(tc.status) || s.At != tc.at {
				t.Errorf("status/at = %s/%q, want %s/%q", s.Status, s.At, tc.status, tc.at)
			}
			if len(s.Visits) != 1 || s.Visits[tc.visitsKey] != tc.visits {
				t.Errorf("visits = %v, want only %s=%d", s.Visits, tc.visitsKey, tc.visits)
			}
			if s.Iter["plans"].Index != 1 || !reflect.DeepEqual(s.Iter["plans"].Items, []string{"plan-1.md", "plan-2.md", "plan-3.md"}) {
				t.Errorf("plans walk = %+v, want index 1 over the three copies", s.Iter["plans"])
			}
			if s.Awaiting.Step != tc.at || s.Awaiting.Member != tc.actor || s.Awaiting.Round != 7 {
				t.Errorf("awaiting = %+v, want step %q actor %q round 7", s.Awaiting, tc.at, tc.actor)
			}
		})
	}
}

// TestFromLegacyResolvesSettings pins the definition's params: the row's
// settings are resolved, and a blank actor setting keeps the default's own
// value.
func TestFromLegacyResolvesSettings(t *testing.T) {
	l := legacyOf(legacyCase{status: "running", phase: "build", step: "building"})
	l.Settings = LegacySettings{
		MaxCorrections: 5, ReviewerActor: "rev", SecurityActor: "sec",
		Security: false, Gate: "true", Regate: 0,
	}
	l.Builder = "custom-builder"

	def, s, err := FromLegacy(l)
	if err != nil {
		t.Fatalf("FromLegacy: %v", err)
	}
	want := map[string]string{
		"builder": "custom-builder", "reviewer": "rev", "planner": "lite-planner",
		"security": "sec", "scan": "false", "gate": "true", "regate": "0", "max_corrections": "5",
	}
	for name, value := range want {
		got := def.Params[name].Str
		if got == "" {
			got = def.Params[name].render()
		}
		if got != value {
			t.Errorf("param %s = %q, want %q", name, got, value)
		}
	}
	if s.Awaiting.Member != "custom-builder" {
		t.Errorf("awaiting member = %q, want the resolved builder", s.Awaiting.Member)
	}
}

// TestFromLegacyCarriesAwaitedRound pins that a running row's awaited round
// carries over, so a chain that re-execs mid-round continues without noticing.
func TestFromLegacyCarriesAwaitedRound(t *testing.T) {
	l := legacyOf(legacyCase{name: "reviewer round", status: "running", phase: "build", step: "reviewing"})
	l.AwaitingRound = 9

	_, s, err := FromLegacy(l)
	if err != nil {
		t.Fatalf("FromLegacy: %v", err)
	}
	if s.Awaiting.Round != 9 || s.Awaiting.Member != "reviewer" || s.Awaiting.Step != "review" {
		t.Errorf("awaiting = %+v, want reviewer round 9 at review", s.Awaiting)
	}
}

// TestLegacyViewInvertsFromLegacy pins that the projection back to the legacy
// columns recovers the row a state was built from.
func TestLegacyViewInvertsFromLegacy(t *testing.T) {
	for _, tc := range legacyCases {
		t.Run(tc.name, func(t *testing.T) {
			def, s, err := FromLegacy(legacyOf(tc))
			if err != nil {
				t.Fatalf("FromLegacy: %v", err)
			}
			got := LegacyView(def, s)
			if got.Phase != tc.phase || got.Step != tc.step {
				t.Errorf("phase/step = %q/%q, want %q/%q", got.Phase, got.Step, tc.phase, tc.step)
			}
			if got.Plan != 2 || got.Plans != 3 || got.Corrections != tc.corrections {
				t.Errorf("plan/plans/corrections = %d/%d/%d, want 2/3/%d",
					got.Plan, got.Plans, got.Corrections, tc.corrections)
			}
			if got.AwaitingRound != 7 || got.AwaitingMember != tc.part {
				t.Errorf("awaiting member/round = %q/%d, want %q/7", got.AwaitingMember, got.AwaitingRound, tc.part)
			}
		})
	}
}

// TestLegacyViewMapsEveryStep pins the projection for the steps the engine
// works in that the migration cannot name: a check, a repair and the scan's
// branch all read as the old build, and a finished run reads the finished
// phase.
func TestLegacyViewMapsEveryStep(t *testing.T) {
	def := Default()
	cases := []struct {
		status string
		at     string
		phase  string
		step   string
		actor  string
	}{
		{"running", "build", "build", "building", "builder"},
		{"running", "check", "build", "building", "builder"},
		{"running", "repair", "build", "building", "builder"},
		{"running", "plans", "build", "building", "builder"},
		{"running", "scan-gate", "security", "scanning", "security"},
		{"running", "fix-check", "security", "building", "builder"},
		{"running", "fix-repair", "security", "building", "builder"},
		{"running", "nowhere", "build", "building", ""},
		{"done", "build", "finished", "building", "builder"},
	}
	for _, tc := range cases {
		s := State{Status: Status(tc.status), At: tc.at, Visits: map[string]int{}, Iter: map[string]Iter{"plans": {Index: 0, Items: []string{"p"}}}}
		got := LegacyView(def, s)
		if got.Phase != tc.phase || got.Step != tc.step || got.AwaitingMember != tc.actor {
			t.Errorf("LegacyView(%s, %s) = %q/%q/%q, want %q/%q/%q",
				tc.status, tc.at, got.Phase, got.Step, got.AwaitingMember, tc.phase, tc.step, tc.actor)
		}
	}
}

// TestLegacyViewClampsCorrections pins the guard: a correct step a state has
// not counted reads as no spent corrections rather than a negative count.
func TestLegacyViewClampsCorrections(t *testing.T) {
	s := State{Status: StatusRunning, At: "correct", Visits: map[string]int{}, Iter: map[string]Iter{"plans": {Index: 0, Items: []string{"p"}}}}
	if got := LegacyView(Default(), s); got.Corrections != 0 {
		t.Errorf("corrections = %d, want 0", got.Corrections)
	}
}
