package availability

import (
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/account"
	"github.com/fuad-daoud/relevo/internal/candidate"
)

// allowanceDeps builds Deps over a muse candidate set, for the tests that pin
// the allowance gates.
func allowanceDeps(t *testing.T) Deps {
	t.Helper()
	set, _, err := candidate.Parse("test", []byte(`[
	  {"name":"spark","harness":"muse","provider":"meta","model":"muse-spark","roles":["builder"]},
	  {"name":"other","harness":"opencode","provider":"meta","model":"m","roles":["builder"]}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	return testDepsWith(t, set)
}

const (
	allowanceFutureReset = 1757003600000 // baseTime plus one hour, in ms
	allowancePastReset   = 1756996400000 // baseTime minus one hour, in ms
)

func seedAllowance(t *testing.T, d Deps, doc Allowance) {
	t.Helper()
	if err := SaveAllowance(d.Gates, doc); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAllowanceAbsentReadsEmpty(t *testing.T) {
	t.Parallel()

	d := allowanceDeps(t)
	got, err := LoadAllowance(d.Gates)
	if err != nil {
		t.Fatalf("LoadAllowance: %v", err)
	}
	if len(got.Accounts) != 0 {
		t.Errorf("accounts = %+v, want empty", got.Accounts)
	}
}

func TestAllowanceSaveLoadRoundTrip(t *testing.T) {
	t.Parallel()

	d := allowanceDeps(t)
	want := Allowance{Accounts: map[string]AllowanceEntry{
		"meta@m1": {WindowUsed: 92, WindowResetsAtMS: allowanceFutureReset, WeeklyUsed: 34, WeeklyResetsAtMS: allowanceFutureReset},
	}}
	seedAllowance(t, d, want)
	got, err := LoadAllowance(d.Gates)
	if err != nil {
		t.Fatalf("LoadAllowance: %v", err)
	}
	if got.Accounts["meta@m1"] != want.Accounts["meta@m1"] {
		t.Errorf("accounts = %+v, want %+v", got.Accounts, want.Accounts)
	}
}

func TestAllowanceObserveKeepsTheMaximumWithinOneGeneration(t *testing.T) {
	t.Parallel()

	a := Allowance{Accounts: map[string]AllowanceEntry{}}
	obs := AllowanceObservation{WindowUsed: 90, WindowResetsAtMS: allowanceFutureReset, HasWindow: true}
	a = a.Observe("meta", obs)
	a = a.Observe("meta", AllowanceObservation{WindowUsed: 40, WindowResetsAtMS: allowanceFutureReset, HasWindow: true})
	if got := a.Accounts["meta"]; got.WindowUsed != 90 || got.WindowResetsAtMS != allowanceFutureReset {
		t.Errorf("window = %+v, want the 90 maximum kept, never regressed to 40", got)
	}
	a = a.Observe("meta", AllowanceObservation{WindowUsed: 92, WindowResetsAtMS: allowanceFutureReset, HasWindow: true})
	if got := a.Accounts["meta"]; got.WindowUsed != 92 {
		t.Errorf("window = %+v, want the newer 92 maximum", got)
	}
}

func TestAllowanceObserveStartsANewGenerationOnANewReset(t *testing.T) {
	t.Parallel()

	a := Allowance{Accounts: map[string]AllowanceEntry{}}
	a = a.Observe("meta", AllowanceObservation{WindowUsed: 92, WindowResetsAtMS: allowanceFutureReset, HasWindow: true})
	a = a.Observe("meta", AllowanceObservation{WindowUsed: 4, WindowResetsAtMS: allowanceFutureReset + 3600000, HasWindow: true})
	if got := a.Accounts["meta"]; got.WindowUsed != 4 || got.WindowResetsAtMS != allowanceFutureReset+3600000 {
		t.Errorf("window = %+v, want the new generation's 4, old record discarded", got)
	}
}

func TestAllowanceObserveLeavesAnIncompleteDimensionUntouched(t *testing.T) {
	t.Parallel()

	a := Allowance{Accounts: map[string]AllowanceEntry{}}
	a = a.Observe("meta", AllowanceObservation{WindowUsed: 92, WindowResetsAtMS: allowanceFutureReset, HasWindow: true})
	a = a.Observe("meta", AllowanceObservation{WindowUsed: 10, HasWindow: true})
	if got := a.Accounts["meta"]; got.WindowUsed != 92 || got.WindowResetsAtMS != allowanceFutureReset {
		t.Errorf("window = %+v, want the bounded 92 kept when the sighting carries no reset", got)
	}
	b := Allowance{Accounts: map[string]AllowanceEntry{}}.Observe("meta",
		AllowanceObservation{WindowUsed: 10, HasWindow: true})
	if len(b.Accounts) != 0 {
		t.Errorf("accounts = %+v, want nothing stored for a reset-less first sighting", b.Accounts)
	}
}

func TestAllowanceGateSkipsAnExhaustedWindow(t *testing.T) {
	t.Parallel()

	d := allowanceDeps(t)
	seedAllowance(t, d, Allowance{Accounts: map[string]AllowanceEntry{
		"meta": {WindowUsed: 92, WindowResetsAtMS: allowanceFutureReset, WeeklyUsed: 3, WeeklyResetsAtMS: allowanceFutureReset},
	}})
	gates := Gates(d)
	if len(gates) != 1 {
		t.Fatalf("gates = %+v, want exactly one", gates)
	}
	g := gates[0]
	if g.Token != "muse/meta/muse-spark" || g.Kind != AllowanceExhausted {
		t.Errorf("gate = %+v, want the muse token gated as allowance_exhausted", g)
	}
	if !strings.Contains(g.Note, "window 92%") {
		t.Errorf("note = %q, want the window reason with its share", g.Note)
	}
	if !g.Until.IsZero() {
		t.Errorf("until = %v, want zero: the note carries the reason", g.Until)
	}
	if got := GateText(g.Kind, g.Until, g.Note); got != g.Note {
		t.Errorf("GateText = %q, want the note itself, as for the concurrency family", got)
	}
	if got := GateKindText(AllowanceExhausted); got != "allowance exhausted" {
		t.Errorf("GateKindText = %q", got)
	}
}

func TestAllowanceGateSkipsAnExhaustedWeeklyAlone(t *testing.T) {
	t.Parallel()

	d := allowanceDeps(t)
	seedAllowance(t, d, Allowance{Accounts: map[string]AllowanceEntry{
		"meta": {WindowUsed: 4, WindowResetsAtMS: allowanceFutureReset, WeeklyUsed: 88, WeeklyResetsAtMS: allowanceFutureReset},
	}})
	gates := Gates(d)
	if len(gates) != 1 || !strings.Contains(gates[0].Note, "weekly 88%") {
		t.Fatalf("gates = %+v, want one naming the weekly reason", gates)
	}
}

func TestAllowanceBelowThresholdPicksNormally(t *testing.T) {
	t.Parallel()

	d := allowanceDeps(t)
	seedAllowance(t, d, Allowance{Accounts: map[string]AllowanceEntry{
		"meta": {WindowUsed: 84, WindowResetsAtMS: allowanceFutureReset, WeeklyUsed: 84, WeeklyResetsAtMS: allowanceFutureReset},
	}})
	if gates := Gates(d); len(gates) != 0 {
		t.Errorf("gates = %+v, want none below the threshold", gates)
	}
}

func TestAllowanceStaleResetNeverGates(t *testing.T) {
	t.Parallel()

	d := allowanceDeps(t)
	seedAllowance(t, d, Allowance{Accounts: map[string]AllowanceEntry{
		"meta": {WindowUsed: 99, WindowResetsAtMS: allowancePastReset, WeeklyUsed: 99, WeeklyResetsAtMS: allowancePastReset},
	}})
	if gates := Gates(d); len(gates) != 0 {
		t.Errorf("gates = %+v, want none once the resets are past", gates)
	}
}

func TestAllowanceMixedStalenessGatesOnlyTheFreshDimension(t *testing.T) {
	t.Parallel()

	d := allowanceDeps(t)
	seedAllowance(t, d, Allowance{Accounts: map[string]AllowanceEntry{
		"meta": {WindowUsed: 99, WindowResetsAtMS: allowancePastReset, WeeklyUsed: 88, WeeklyResetsAtMS: allowanceFutureReset},
	}})
	gates := Gates(d)
	if len(gates) != 1 {
		t.Fatalf("gates = %+v, want one for the fresh weekly dimension", gates)
	}
	if strings.Contains(gates[0].Note, "window") || !strings.Contains(gates[0].Note, "weekly 88%") {
		t.Errorf("note = %q, want the weekly reason only", gates[0].Note)
	}
}

func TestAllowanceNeverGatesANonMuseCandidate(t *testing.T) {
	t.Parallel()

	d := allowanceDeps(t)
	seedAllowance(t, d, Allowance{Accounts: map[string]AllowanceEntry{
		"meta": {WindowUsed: 99, WindowResetsAtMS: allowanceFutureReset},
	}})
	for _, g := range Gates(d) {
		if g.Token == "opencode/meta/m" {
			t.Errorf("non-muse candidate gated: %+v", g)
		}
	}
}

func TestAllowancePartialPoolLeavesTheCandidatePickable(t *testing.T) {
	t.Parallel()

	d := allowanceDeps(t)
	d.Accounts = account.Set{
		{Name: "m1", Harness: "muse", Groups: []string{"meta"}},
		{Name: "m2", Harness: "muse", Groups: []string{"meta"}},
	}
	seedAllowance(t, d, Allowance{Accounts: map[string]AllowanceEntry{
		"meta@m1": {WindowUsed: 99, WindowResetsAtMS: allowanceFutureReset},
	}})
	if gates := Gates(d); len(gates) != 0 {
		t.Errorf("gates = %+v, want none while one login of the pool stays usable", gates)
	}
	seedAllowance(t, d, Allowance{Accounts: map[string]AllowanceEntry{
		"meta@m1": {WindowUsed: 99, WindowResetsAtMS: allowanceFutureReset},
		"meta@m2": {WindowUsed: 90, WindowResetsAtMS: allowanceFutureReset},
	}})
	gates := Gates(d)
	if len(gates) != 1 || gates[0].Token != "muse/meta/muse-spark" {
		t.Fatalf("gates = %+v, want the muse token once every login is gated", gates)
	}
}
