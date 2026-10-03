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

// TestStatusShowsARunningManualRoundOnAHaltedChain pins item 4: a halted chain
// whose builder has a manual round open reads its own status word with the
// round in its segment -- not NEEDS YOU -- and carries manual_round in the
// document. A halted chain with nothing running keeps NEEDS YOU (the test
// above), and the same predicate is the one ChainWaitTarget already uses.
func TestStatusShowsARunningManualRoundOnAHaltedChain(t *testing.T) {
	rt := newRuntime(t)
	c, _ := newChainFixture(t, rt, "halted")

	// A manual round on the builder: a prompt entry for its current round,
	// with no report yet.
	if err := rt.Store.AppendLog(c.Builder, store.LogEntry{
		TS: time.Now().UTC(), Round: 2, Direction: store.DirToBuilder, Kind: store.KindPrompt,
	}); err != nil {
		t.Fatalf("open the builder's round: %v", err)
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	row := rep.Bindings[0]
	if row.Display != "HALTED" {
		t.Errorf("display = %q, want HALTED, not NEEDS YOU: a manual round is running", row.Display)
	}
	if row.Chain == nil || row.Chain.ManualRound != 2 {
		t.Fatalf("chain facts = %+v, want manual_round 2", row.Chain)
	}
	if got := view.ChainSegment(*row.Chain); got != "manual round 2 running" {
		t.Errorf("ChainSegment = %q, want %q", got, "manual round 2 running")
	}
	if !strings.Contains(view.RenderStatus(rep), "chain  halted  manual round 2 running") {
		t.Errorf("human row missing the manual-round line:\n%s", view.RenderStatus(rep))
	}

	rows := view.StatusLineRows(rep, rt.Now())
	if len(rows) != 1 || rows[0].NeedsYou {
		t.Fatalf("statusline rows = %+v, want one non-NEEDS-YOU chain row", rows)
	}
	if rows[0].Display != "HALTED" {
		t.Errorf("statusline display = %q, want HALTED", rows[0].Display)
	}

	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"manual_round":2`) {
		t.Errorf("document is missing manual_round 2: %s", raw)
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

// TestStatusDoneChainCollapsesToOwnRow pins the finished case: a done chain
// collapses to its own row, which HideDone hides by default.
func TestStatusDoneChainCollapsesToOwnRow(t *testing.T) {
	rt := newRuntime(t)
	c, _ := newChainFixture(t, rt, "done")

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(rep.Bindings) != 1 {
		t.Fatalf("got %d rows (%v), want exactly 1 chain row", len(rep.Bindings), rowNames(rep))
	}
	row := rep.Bindings[0]
	if row.Name != c.Name {
		t.Errorf("row.Name = %q, want %q", row.Name, c.Name)
	}
	if row.Display != "DONE" {
		t.Errorf("row.Display = %q, want DONE", row.Display)
	}
	if row.Chain == nil {
		t.Fatal("row.Chain is nil")
	}
	if row.Chain.Status != "done" || row.Chain.Plan != 2 || row.Chain.Plans != 4 {
		t.Errorf("row.Chain = %+v, want done plan 2/4", row.Chain)
	}
	seg := view.ChainSegment(*row.Chain)
	if !strings.Contains(seg, "plan 2/4") {
		t.Errorf("ChainSegment = %q, want 'plan 2/4'", seg)
	}
	hidden := view.HideDone(rep)
	if hidden.DoneHidden != 1 || len(hidden.Bindings) != 0 {
		t.Errorf("HideDone = hidden %d, remaining %d; want 1 and 0", hidden.DoneHidden, len(hidden.Bindings))
	}
}

// TestStatuslineDoneChain pins the statusline path for a done chain:
// members present collapses to one row; members released or gone conjures no row.
func TestStatuslineDoneChain(t *testing.T) {
	rt := newRuntime(t)
	c, members := newChainFixture(t, rt, "done")

	// Members present: MasterMindStatus collapses to one row.
	rep, err := MasterMindStatus(context.Background(), rt, testMasterMindID)
	if err != nil {
		t.Fatalf("MasterMindStatus: %v", err)
	}
	rows := view.StatusLineRows(rep, rt.Now())
	if len(rows) != 1 || rows[0].Name != c.Name {
		t.Fatalf("statusline rows = %+v, want only %q", rows, c.Name)
	}
	if rows[0].Display != "DONE" {
		t.Errorf("rows[0].Display = %q, want DONE", rows[0].Display)
	}
	if !strings.Contains(rows[0].Chain, "chain x · plan 2/4") {
		t.Errorf("rows[0].Chain = %q, want 'chain x · plan 2/4'", rows[0].Chain)
	}

	// Members released or gone: conjures no row.
	for _, m := range members {
		m.State = store.StateDone
		if err := rt.Store.Save(m); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	repReleased, err := MasterMindStatus(context.Background(), rt, testMasterMindID)
	if err != nil {
		t.Fatalf("MasterMindStatus: %v", err)
	}
	releasedRows := view.StatusLineRows(repReleased, rt.Now())
	if len(releasedRows) != 0 {
		t.Errorf("released members: statusline rows = %+v, want none", releasedRows)
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

// TestChainStatusReadsState pins the status surfaces' state read: a custom
// workflow's members come from chain_member, so the named view lists the
// builder and the custom reader under the chain and the every-row listing
// replaces both with the one chain row.
func TestChainStatusReadsState(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowReviewWorkflow)

	named, err := ChainStatus(context.Background(), rt, "shop")
	if err != nil {
		t.Fatalf("ChainStatus: %v", err)
	}
	want := []string{"shop", "shop", "shop-assistant"}
	if got := rowNames(named); len(got) != len(want) {
		t.Fatalf("named rows = %v, want the chain row and its members %v", got, want)
	}
	if named.Bindings[0].Chain == nil || named.Bindings[0].Chain.StepAt != "build" {
		t.Errorf("chain row = %+v, want the engine's build step", named.Bindings[0])
	}
	if named.Bindings[1].Name != "shop" || named.Bindings[2].Name != "shop-assistant" {
		t.Errorf("member rows = %v, want the builder and the custom reader", rowNames(named))
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	for _, name := range rowNames(rep) {
		if name == "shop-assistant" {
			t.Errorf("custom member %q kept its own row in the every-row listing: %v", name, rowNames(rep))
		}
	}
}

// TestStatusNamesTheMemberHoldingAnUncollectedPayload pins the stranded half of
// the roll-up: a member still holding a payload nobody collected is named on
// the chain row, because the chain row is the only row left for it. Both
// surfaces name it -- the human listing and the statusline.
func TestStatusNamesTheMemberHoldingAnUncollectedPayload(t *testing.T) {
	rt := newRuntime(t)
	c, _ := newChainFixture(t, rt, "running")

	// The reviewer is left holding a report no collector took.
	if err := rt.Store.AppendLog("x-rev", store.LogEntry{
		TS: time.Now().UTC(), Round: 2, Direction: store.DirToMasterMind, Kind: store.KindReport,
	}); err != nil {
		t.Fatalf("plant the pending report: %v", err)
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	row := rep.Bindings[0]
	if row.Chain == nil {
		t.Fatalf("row %q carries no chain facts", row.Name)
	}
	if len(row.Chain.PendingMembers) != 1 || row.Chain.PendingMembers[0] != "x-rev" {
		t.Errorf("pending members = %v, want [x-rev]", row.Chain.PendingMembers)
	}
	if got := view.ChainPendingSegment(*row.Chain); got != "pending on x-rev" {
		t.Errorf("ChainPendingSegment = %q, want %q", got, "pending on x-rev")
	}
	if line := view.RenderStatus(rep); !strings.Contains(line, "pending on x-rev") {
		t.Errorf("human row missing the pending member:\n%s", line)
	}
	rows := view.StatusLineRows(rep, rt.Now())
	if len(rows) != 1 {
		t.Fatalf("statusline rows = %+v, want only %q", rows, c.Name)
	}
	if !strings.Contains(rows[0].Chain, "pending on x-rev") {
		t.Errorf("statusline row = %q, want it to name the pending member", rows[0].Chain)
	}

	// The other members hold nothing, so only the stranded one is named.
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"pending_members":["x-rev"]`) {
		t.Errorf("document is missing the pending member: %s", raw)
	}
}

// TestStatusNamesEveryStrandedMember pins the plural: a chain with two members
// holding an uncollected payload names both, because one name would leave the
// other invisible exactly as before.
func TestStatusNamesEveryStrandedMember(t *testing.T) {
	rt := newRuntime(t)
	newChainFixture(t, rt, "running")

	for _, member := range []string{"x-rev", "x-plan"} {
		if err := rt.Store.AppendLog(member, store.LogEntry{
			TS: time.Now().UTC(), Round: 2, Direction: store.DirToMasterMind, Kind: store.KindReport,
		}); err != nil {
			t.Fatalf("plant the pending report on %s: %v", member, err)
		}
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	got := rep.Bindings[0].Chain.PendingMembers
	if len(got) != 2 || got[0] != "x-rev" || got[1] != "x-plan" {
		t.Errorf("pending members = %v, want [x-rev x-plan] in member order", got)
	}
}

// TestStatusNamesNoMemberWhenNothingIsStranded pins the unchanged case: with no
// member holding an uncollected payload the row carries no pending member and
// no segment, so a healthy chain reads exactly as it did before.
func TestStatusNamesNoMemberWhenNothingIsStranded(t *testing.T) {
	rt := newRuntime(t)
	newChainFixture(t, rt, "running")

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	f := rep.Bindings[0].Chain
	if len(f.PendingMembers) != 0 {
		t.Errorf("pending members = %v, want none", f.PendingMembers)
	}
	if got := view.ChainPendingSegment(*f); got != "" {
		t.Errorf("ChainPendingSegment = %q, want no segment", got)
	}
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(raw), "pending_members") {
		t.Errorf("document carries a pending_members key with nothing pending: %s", raw)
	}
}

// newForkChildFixture writes one fork child of parent: a chain row named
// "<parent>.<key>" with Parent set, and a single builder member, through the
// same Tx.CreateChain the engine forks through.
func newForkChildFixture(t *testing.T, rt Runtime, parent, key, status string) db.ChainRow {
	t.Helper()
	root := t.TempDir()
	now := time.Now().UTC().Truncate(time.Millisecond)
	name := parent + "." + key
	c := db.ChainRow{
		ID:             db.NewID(),
		Name:           name,
		Status:         status,
		Phase:          "build",
		Step:           "working",
		Plan:           1,
		Plans:          1,
		PlanPathsJSON:  []byte(`["/plans/001.md"]`),
		SettingsJSON:   []byte(`{"max_corrections":1}`),
		AwaitingMember: "builder",
		AwaitingRound:  1,
		Builder:        name,
		Parent:         parent,
		Worktree:       filepath.Join(root, name),
		MasterMindID:   testMasterMindID,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if status == "halted" {
		c.Reason = "child " + key + " wants changes"
	}
	members := []store.Binding{{
		Name: name, CWD: filepath.Join(root, name), Round: 1, State: store.StateActive,
		MasterMindID:     testMasterMindID,
		Builder:          store.Endpoint{Kind: "agy", Mode: store.ModeHeadless},
		BuilderCandidate: testAgyRef,
	}}
	if err := rt.Store.WithLock(func(tx *store.Tx) error { return tx.CreateChain(c, members) }); err != nil {
		t.Fatalf("CreateChain %q: %v", name, err)
	}
	return c
}

// TestStatusKeepsForkChildrenUnderTheirParent pins the two placement rules a
// fork child depends on: a child is never a top-level row of its own, so its
// name appears exactly once in the listing, and it is threaded in after its
// parent's row rather than left to the attention sort -- which would put a
// halted child ahead of its still-running parent and interleave the unrelated
// NEEDS YOU row between them.
func TestStatusKeepsForkChildrenUnderTheirParent(t *testing.T) {
	rt := newRuntime(t)
	parent, _ := newChainFixture(t, rt, "running")
	halted := newForkChildFixture(t, rt, parent.Name, "1", "halted")
	running := newForkChildFixture(t, rt, parent.Name, "2", "running")

	// An unrelated halted chain of its own: a NEEDS YOU row that outranks the
	// parent, so a child threaded in before the sort could not stay beside it.
	other := db.ChainRow{
		ID: db.NewID(), Name: "zz", Status: "halted", Phase: "build", Step: "reviewing",
		Plan: 1, Plans: 1, PlanPathsJSON: []byte(`["/plans/001.md"]`),
		SettingsJSON: []byte(`{"max_corrections":1}`), AwaitingMember: "builder", AwaitingRound: 1,
		Builder: "zz", Reason: "unrelated chain halted", Worktree: filepath.Join(t.TempDir(), "zz"),
		MasterMindID: testMasterMindID, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return tx.CreateChain(other, []store.Binding{{
			Name: "zz", CWD: other.Worktree, Round: 1, State: store.StateActive,
			MasterMindID:     testMasterMindID,
			Builder:          store.Endpoint{Kind: "agy", Mode: store.ModeHeadless},
			BuilderCandidate: testAgyRef,
		}})
	}); err != nil {
		t.Fatalf("CreateChain %q: %v", other.Name, err)
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	names := rowNames(rep)
	for _, child := range []string{halted.Name, running.Name} {
		if n := countNames(names, child); n != 1 {
			t.Errorf("child %q appears %d times in %v, want exactly once", child, n, names)
		}
	}
	// The halted child reads NEEDS YOU and the parent reads ACTIVE, so the sort
	// alone would put the child first; the child belongs directly under its
	// parent, whatever the unrelated halted chain's own rank is.
	if names[0] != other.Name {
		t.Errorf("first row = %q, want the unrelated NEEDS YOU chain %q: %v", names[0], other.Name, names)
	}
	at := indexOf(names, parent.Name)
	if at < 0 {
		t.Fatalf("parent %q missing from %v", parent.Name, names)
	}
	if names[at+1] != halted.Name {
		t.Errorf("row after the parent = %q, want the halted child %q: %v", names[at+1], halted.Name, names)
	}
	if names[at+2] != running.Name {
		t.Errorf("row after the halted child = %q, want the running child %q: %v", names[at+2], running.Name, names)
	}
}

// countNames is how many times name appears in the row names.
func countNames(names []string, name string) int {
	n := 0
	for _, got := range names {
		if got == name {
			n++
		}
	}
	return n
}

// indexOf is the position of name in the row names, or -1.
func indexOf(names []string, name string) int {
	for i, got := range names {
		if got == name {
			return i
		}
	}
	return -1
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
