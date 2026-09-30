package relevo

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// newChainFixture builds a chain named "x" with three member bindings -- its
// builder (the design's `<n>` member), a reviewer and a planner -- one plan of
// four in progress and one correction spent. The row is written through
// Tx.CreateChain, the only chain writer on this branch.
func newChainFixture(t *testing.T, rt Runtime, status string) (db.ChainRow, []store.Binding) {
	t.Helper()
	root := t.TempDir()
	now := time.Now().UTC().Truncate(time.Millisecond)
	c := db.ChainRow{
		ID:             db.NewID(),
		Name:           "x",
		Status:         status,
		Phase:          "build",
		Step:           "reviewing",
		Plan:           2,
		Plans:          4,
		Corrections:    1,
		PlanPathsJSON:  []byte(`["/plans/001.md","/plans/002.md","/plans/003.md","/plans/004.md"]`),
		SettingsJSON:   []byte(`{"max_corrections":1}`),
		AwaitingMember: "reviewer",
		AwaitingRound:  2,
		Builder:        "x",
		Reviewer:       "x-rev",
		Planner:        "x-plan",
		Security:       "x-sec",
		Worktree:       filepath.Join(root, "x"),
		MasterMindID:   testMasterMindID,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if status == "halted" {
		c.Reason = "reviewer still wants changes after 1 corrections"
	}
	members := []store.Binding{
		{
			Name: "x", CWD: filepath.Join(root, "x"), Round: 2, State: store.StateActive,
			MasterMindID:     testMasterMindID,
			Builder:          store.Endpoint{Kind: "agy", Mode: store.ModeHeadless},
			BuilderCandidate: testAgyRef,
		},
		{
			Name: "x-rev", CWD: filepath.Join(root, "x-rev"), Round: 2, State: store.StateActive,
			Role: "reviewer", Shape: store.ShapeReader, MasterMindID: testMasterMindID,
		},
		{
			Name: "x-plan", CWD: filepath.Join(root, "x-plan"), Round: 1, State: store.StateActive,
			Role: "planner", Shape: store.ShapeReader, MasterMindID: testMasterMindID,
		},
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error { return tx.CreateChain(c, members) }); err != nil {
		t.Fatalf("CreateChain: %v", err)
	}
	return c, members
}

// rowNames is rep's row names in order, for the row-set assertions below.
func rowNames(rep view.Report) []string {
	out := make([]string, len(rep.Bindings))
	for i, b := range rep.Bindings {
		out[i] = b.Name
	}
	return out
}

// TestStatusShowsAChainRowInPlaceOfMemberRows is the surface's whole point:
// the three member rows are gone and one chain row stands in their place, with
// the chain's plan segment on it.
func TestStatusShowsAChainRowInPlaceOfMemberRows(t *testing.T) {
	rt := newRuntime(t)
	c, _ := newChainFixture(t, rt, "running")

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got := rowNames(rep); len(got) != 1 || got[0] != c.Name {
		t.Fatalf("rows = %v, want only the chain row %q", got, c.Name)
	}
	row := rep.Bindings[0]
	if row.Chain == nil {
		t.Fatalf("row %q carries no chain facts: %+v", row.Name, row)
	}
	for _, gone := range []string{"x-rev", "x-plan", "x-sec"} {
		if strings.Contains(view.RenderStatus(rep), gone) {
			t.Errorf("member row %q is still in the listing:\n%s", gone, view.RenderStatus(rep))
		}
	}
}

// TestStatusChainCarriesPlanAndCorrections pins the facts the chain row shows:
// the plan in progress of the plans held, the phase and step, the correction
// rounds and the member the chain waits on.
func TestStatusChainCarriesPlanAndCorrections(t *testing.T) {
	rt := newRuntime(t)
	c, _ := newChainFixture(t, rt, "running")

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	row := rep.Bindings[0]
	if row.Name != c.Name || row.CWD != c.Worktree {
		t.Errorf("row name/cwd = %q/%q, want %q/%q", row.Name, row.CWD, c.Name, c.Worktree)
	}
	if row.Display != "ACTIVE" || row.State != string(store.StateActive) {
		t.Errorf("display/state = %q/%q, want ACTIVE/active", row.Display, row.State)
	}
	if row.MasterMindID != testMasterMindID {
		t.Errorf("mastermind id = %q, want %q", row.MasterMindID, testMasterMindID)
	}
	f := row.Chain
	if f.Plan != 2 || f.Plans != 4 || f.Corrections != 1 {
		t.Errorf("plan/plans/corrections = %d/%d/%d, want 2/4/1", f.Plan, f.Plans, f.Corrections)
	}
	if f.Phase != "build" || f.Step != "reviewing" || f.Status != "running" {
		t.Errorf("phase/step/status = %q/%q/%q, want build/reviewing/running", f.Phase, f.Step, f.Status)
	}
	if f.Awaiting != "reviewer" {
		t.Errorf("awaiting = %q, want reviewer", f.Awaiting)
	}
	if got := view.ChainSegment(*f); got != "plan 2/4 · reviewing · 1 correction" {
		t.Errorf("ChainSegment = %q, want the plan segment", got)
	}
	if !strings.Contains(view.RenderStatus(rep), "chain  running  plan 2/4 · reviewing · 1 correction") {
		t.Errorf("human row missing the chain line:\n%s", view.RenderStatus(rep))
	}
}

// TestStatusHaltedChainIsNeedsYouWithReason pins a halted chain: the row reads
// NEEDS YOU with the halt reason as its detail, on both the human and the
// statusline surface.
func TestStatusHaltedChainIsNeedsYouWithReason(t *testing.T) {
	rt := newRuntime(t)
	c, _ := newChainFixture(t, rt, "halted")

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	row := rep.Bindings[0]
	if row.Display != "NEEDS YOU" || row.State != string(store.StateNeedsYou) {
		t.Errorf("display/state = %q/%q, want NEEDS YOU/needs_you", row.Display, row.State)
	}
	if row.Detail != c.Reason {
		t.Errorf("detail = %q, want the halt reason %q", row.Detail, c.Reason)
	}
	if row.Chain.Reason != c.Reason {
		t.Errorf("chain reason = %q, want %q", row.Chain.Reason, c.Reason)
	}
	if !strings.Contains(view.RenderStatus(rep), "  reason   "+c.Reason) {
		t.Errorf("human row missing the reason line:\n%s", view.RenderStatus(rep))
	}

	rows := view.StatusLineRows(rep, rt.Now())
	if len(rows) != 1 || !rows[0].NeedsYou || rows[0].Status != "NEEDS YOU" {
		t.Fatalf("statusline rows = %+v, want one NEEDS YOU chain row", rows)
	}
}

// TestStatusAllListsTheMembersUnderTheChain pins what a complete listing shows:
// the live chain still replaces its member rows in the every-row report, and
// the named chain view lists every member under the chain, builder first.
func TestStatusAllListsTheMembersUnderTheChain(t *testing.T) {
	rt := newRuntime(t)
	c, _ := newChainFixture(t, rt, "running")

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	for _, name := range rowNames(rep) {
		if name == "x-rev" || name == "x-plan" {
			t.Errorf("member %q kept its own row in the every-row listing: %v", name, rowNames(rep))
		}
	}

	named, err := ChainStatus(context.Background(), rt, c.Name)
	if err != nil {
		t.Fatalf("ChainStatus: %v", err)
	}
	want := []string{"x", "x", "x-rev", "x-plan"}
	if got := rowNames(named); len(got) != len(want) {
		t.Fatalf("named rows = %v, want the chain row and its members %v", got, want)
	}
	if named.Bindings[0].Chain == nil {
		t.Errorf("first named row %q is not the chain row", named.Bindings[0].Name)
	}
	// The members keep their ordinary rows under the chain: the builder
	// (named for the chain) comes first, and each is not a chain row.
	if named.Bindings[1].Name != c.Builder || named.Bindings[1].Chain != nil {
		t.Errorf("row after the chain = %q (chain %v), want the builder %q as an ordinary row",
			named.Bindings[1].Name, named.Bindings[1].Chain != nil, c.Builder)
	}
	if named.Bindings[2].Name != "x-rev" || named.Bindings[3].Name != "x-plan" {
		t.Errorf("member order = %v, want the builder, x-rev, x-plan", rowNames(named))
	}
}

// TestStatusDoneChainLeavesMemberRows pins the finished case: a done chain
// awards no chain row, and every member keeps its own row.
func TestStatusDoneChainLeavesMemberRows(t *testing.T) {
	rt := newRuntime(t)
	c, _ := newChainFixture(t, rt, "done")

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	want := []string{"x", "x-plan", "x-rev"}
	if got := rowNames(rep); len(got) != len(want) {
		t.Fatalf("rows = %v, want every member's own row %v", got, want)
	}
	for _, row := range rep.Bindings {
		if row.Chain != nil {
			t.Errorf("done chain %q still awards a chain row", c.Name)
		}
	}
}

// TestStatuslineChainRowReplacesMembers pins the statusline path: a
// mastermind's own status shows one entry per chain instead of the members'
// rows, with the chain's plan segment in the middle.
func TestStatuslineChainRowReplacesMembers(t *testing.T) {
	rt := newRuntime(t)
	c, _ := newChainFixture(t, rt, "running")
	if err := rt.Store.Save(store.Binding{
		Name: "other", CWD: t.TempDir(), State: store.StateActive, MasterMindID: otherClaimMasterMind,
	}); err != nil {
		t.Fatalf("Save other: %v", err)
	}

	rep, err := MasterMindStatus(context.Background(), rt, testMasterMindID)
	if err != nil {
		t.Fatalf("MasterMindStatus: %v", err)
	}
	rows := view.StatusLineRows(rep, rt.Now())
	if len(rows) != 1 || rows[0].Name != c.Name {
		t.Fatalf("statusline rows = %+v, want only %q", rows, c.Name)
	}
	if rows[0].Chain == "" {
		t.Fatalf("chain row %+v carries no chain segment", rows[0])
	}
	line := view.RenderStatusLine(rep, rt.Now(), 120)
	if !strings.Contains(line, "chain x · plan 2/4 · reviewing · 1 correction") {
		t.Errorf("statusline entry missing the chain segment:\n%s", line)
	}
	if strings.Contains(line, "x-rev") || strings.Contains(line, "x-plan") {
		t.Errorf("member rows are still in the statusline:\n%s", line)
	}
}

// TestStatusJSONCarriesTheChainFacts pins the document: a chain row carries a
// chain object, and an ordinary row carries no chain key at all.
func TestStatusJSONCarriesTheChainFacts(t *testing.T) {
	rt := newRuntime(t)
	newChainFixture(t, rt, "running")

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	doc := string(raw)
	for _, want := range []string{
		`"chain":{`,
		`"status":"running"`,
		`"phase":"build"`,
		`"step":"reviewing"`,
		`"plan":2`,
		`"plans":4`,
		`"corrections":1`,
		`"awaiting":"reviewer"`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("chain document is missing %s: %s", want, doc)
		}
	}

	plain := store.Binding{
		Name: "plain", CWD: t.TempDir(), Round: 1, State: store.StateActive,
	}
	if err := rt.Store.Save(plain); err != nil {
		t.Fatalf("Save plain: %v", err)
	}
	reps, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	for _, row := range reps.Bindings {
		if row.Name != "plain" {
			continue
		}
		rowRaw, err := json.Marshal(row)
		if err != nil {
			t.Fatalf("Marshal plain: %v", err)
		}
		if strings.Contains(string(rowRaw), `"chain"`) {
			t.Errorf("ordinary row carries a chain key: %s", rowRaw)
		}
		return
	}
	t.Fatal("plain row missing from the report")
}
