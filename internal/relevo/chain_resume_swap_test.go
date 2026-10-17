package relevo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// TestChainResumeSwapsTheReviewerActor and TestChainResumeSwapsPlannerActor pin
// rebinding the existing member on swap: name, Round and log kept, Role/Candidate updated,
// StreamSessionID cleared, chain_member row updated with Seq preserved, chain running,
// and SettingsJSON updated.
func TestChainResumeSwapsTheReviewerActor(t *testing.T) {
	testChainResumeSwapsReaderActor(t, "reviewer")
}

func TestChainResumeSwapsPlannerActor(t *testing.T) {
	testChainResumeSwapsReaderActor(t, "planner")
}

func testChainResumeSwapsReaderActor(t *testing.T, part string) {
	t.Helper()

	rt, _ := chainRuntime(t)
	customRows := chainRows()
	newActor := "alt-" + part
	agent := "reviewer"
	if part == "planner" {
		agent = "architect"
	}
	candJSON := `[
  {"harness":"opencode","provider":"test","model":"m","roles":["builder"]},
  {"harness":"claude","provider":"test","model":"m","roles":["builder","reviewer"]},
  {"harness":"claude","provider":"test","model":"alt-model","roles":["builder","reviewer"]}
]`
	rt.Candidates = candidateSet(t, candJSON)
	altCandidate := "claude/test/alt-model"
	customRows[newActor] = roles.Row{
		Shape:       ptr("reader"),
		Candidates:  []string{altCandidate},
		Definitions: map[string]roles.DefRow{"claude": {Agent: agent}},
	}
	rt.Registry = rolesFileRegistry(t, rt.Candidates, rt.Policy, customRows)

	startedChain(t, rt, ChainOptions{})
	chainBuilderClose(t, rt, "shop", chainDoneBody())

	var memberName string
	var resumeOpts ResumeOptions
	resumeOpts.Name = "shop"

	switch part {
	case "reviewer":
		memberName = "shop-rev"
		resumeOpts.ReviewerActor = newActor
		chainReaderClose(t, rt, "shop-rev", chainNoVerdictBody())
	case "planner":
		memberName = "shop-plan"
		resumeOpts.PlannerActor = newActor
		chainReaderClose(t, rt, "shop-rev", chainVerdictBody("changes"))
		if _, err := ChainStop(context.Background(), rt, "shop"); err != nil {
			t.Fatalf("ChainStop: %v", err)
		}
	default:
		t.Fatalf("unhandled part %s", part)
	}

	rowBefore := chainStoredRow(t, rt, "shop")
	if rowBefore.Status != string(chain.StatusHalted) && rowBefore.Status != string(chain.StatusStopped) {
		t.Fatalf("chain status = %q, want halted or stopped", rowBefore.Status)
	}
	memBefore := chainBinding(t, rt, memberName)
	memBefore.Builder.StreamSessionID = "stale-stream-session"
	if err := rt.Store.Save(memBefore); err != nil {
		t.Fatalf("save memBefore: %v", err)
	}
	rowsBefore, err := rt.Store.ChainMembers("shop")
	if err != nil {
		t.Fatalf("ChainMembers before: %v", err)
	}
	var expectedSeq int
	for _, m := range rowsBefore {
		if m.Binding == memberName {
			expectedSeq = m.Seq
		}
	}

	res, err := ChainResume(context.Background(), rt, resumeOpts)
	if err != nil {
		t.Fatalf("ChainResume: %v", err)
	}

	memAfter := chainBinding(t, rt, memberName)
	if memAfter.Name != memberName {
		t.Errorf("binding name = %q, want %q", memAfter.Name, memberName)
	}
	if memAfter.Round != memBefore.Round {
		t.Errorf("round = %d, want untouched %d", memAfter.Round, memBefore.Round)
	}
	if memAfter.Role != newActor {
		t.Errorf("role = %q, want %q", memAfter.Role, newActor)
	}
	if memAfter.BuilderCandidate != altCandidate {
		t.Errorf("candidate = %q, want %q", memAfter.BuilderCandidate, altCandidate)
	}
	if memAfter.Builder.StreamSessionID != "" {
		t.Errorf("StreamSessionID = %q, want empty", memAfter.Builder.StreamSessionID)
	}
	if !HasPromptEntry(chainLog(t, rt, memberName), memAfter.Round) {
		t.Errorf("round %d must be open in log", memAfter.Round)
	}

	rowAfter := chainStoredRow(t, rt, "shop")
	if rowAfter.Status != string(chain.StatusRunning) {
		t.Errorf("chain status = %q, want running", rowAfter.Status)
	}
	if res.Chain.Status != string(chain.StatusRunning) {
		t.Errorf("result chain status = %q, want running", res.Chain.Status)
	}

	var set chain.Settings
	if err := json.Unmarshal(rowAfter.SettingsJSON, &set); err != nil {
		t.Fatalf("unmarshal SettingsJSON: %v", err)
	}
	switch part {
	case "reviewer":
		if set.ReviewerActor != newActor {
			t.Errorf("SettingsJSON.ReviewerActor = %q, want %q", set.ReviewerActor, newActor)
		}
	case "planner":
		if set.PlannerActor != newActor {
			t.Errorf("SettingsJSON.PlannerActor = %q, want %q", set.PlannerActor, newActor)
		}
	}

	members, err := rt.Store.ChainMembers("shop")
	if err != nil {
		t.Fatalf("ChainMembers after: %v", err)
	}
	found := false
	for _, m := range members {
		if m.Binding == memberName {
			found = true
			if m.Actor != newActor {
				t.Errorf("chain_member Actor = %q, want %q", m.Actor, newActor)
			}
			if m.Seq != expectedSeq {
				t.Errorf("chain_member Seq = %d, want %d", m.Seq, expectedSeq)
			}
		}
	}
	if !found {
		t.Errorf("%s not found in chain_members", memberName)
	}
}

func TestChainResumeSwapRefusesAnOpenMemberRound(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainNoVerdictBody())

	revBefore := chainBinding(t, rt, "shop-rev")
	rowBefore := chainStoredRow(t, rt, "shop")

	// Simulate an open round on shop-rev by incrementing Round and appending a prompt entry without a close.
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		rev, err := tx.Load("shop-rev")
		if err != nil {
			return err
		}
		rev.Round++
		if err := tx.Save(rev); err != nil {
			return err
		}
		return tx.AppendLog("shop-rev", store.LogEntry{
			Direction: store.DirToBuilder,
			Kind:      store.KindPrompt,
			Round:     rev.Round,
		})
	}); err != nil {
		t.Fatalf("simulate open round: %v", err)
	}

	_, err := ChainResume(context.Background(), rt, ResumeOptions{
		Name:          "shop",
		ReviewerActor: "assistant",
	})
	if err == nil {
		t.Fatal("ChainResume = nil, want *RoundOpenError")
	}
	var open *RoundOpenError
	if !errors.As(err, &open) {
		t.Fatalf("err = %v (%T), want *RoundOpenError", err, err)
	}
	if open.Member != "shop-rev" {
		t.Errorf("open.Member = %q, want shop-rev", open.Member)
	}

	rowAfter := chainStoredRow(t, rt, "shop")
	if rowAfter.Status != rowBefore.Status {
		t.Errorf("chain status = %q, want untouched %q", rowAfter.Status, rowBefore.Status)
	}
	revAfter := chainBinding(t, rt, "shop-rev")
	if revAfter.Role != revBefore.Role {
		t.Errorf("role = %q, want untouched %q", revAfter.Role, revBefore.Role)
	}
}

func TestChainResumeSwapRefusesWrongShape(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainNoVerdictBody())

	rowBefore := chainStoredRow(t, rt, "shop")
	revBefore := chainBinding(t, rt, "shop-rev")

	_, err := ChainResume(context.Background(), rt, ResumeOptions{
		Name:          "shop",
		ReviewerActor: "builder",
	})
	if err == nil {
		t.Fatal("ChainResume = nil, want shape refusal")
	}

	rowAfter := chainStoredRow(t, rt, "shop")
	if rowAfter.Status != rowBefore.Status {
		t.Errorf("chain status = %q, want untouched %q", rowAfter.Status, rowBefore.Status)
	}
	revAfter := chainBinding(t, rt, "shop-rev")
	if revAfter.Role != revBefore.Role {
		t.Errorf("role = %q, want untouched %q", revAfter.Role, revBefore.Role)
	}
}

func TestServedChainResumeSwapKeepsPlacement(t *testing.T) {
	t.Parallel()

	rt, _, _ := servedChainRuntime(t)
	req := servedChainRequest("shop")
	_ = servedChainStart(t, rt, req)

	builder := chainBinding(t, rt, "shop")
	builder.Round = 2
	if err := rt.Store.Save(builder); err != nil {
		t.Fatalf("advance the builder: %v", err)
	}

	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		row, err := tx.Chain("shop")
		if err != nil {
			return err
		}
		row.Status = string(chain.StatusHalted)
		var st workflow.State
		if err := json.Unmarshal(row.StateJSON, &st); err != nil {
			return err
		}
		st.Status = workflow.StatusHalted
		st.At = "review"
		st.Awaiting.Step = "review"
		st.Awaiting.Member = "reviewer"
		st.Awaiting.Round = 1
		stateJSON, err := json.Marshal(st)
		if err != nil {
			return err
		}
		row.StateJSON = stateJSON
		return tx.ChainPut(row)
	}); err != nil {
		t.Fatalf("halt the chain: %v", err)
	}

	revBefore := chainBinding(t, rt, "shop-rev")
	revBefore.Builder.StreamSessionID = "stale-served-session"
	if err := rt.Store.Save(revBefore); err != nil {
		t.Fatalf("save revBefore with session: %v", err)
	}

	res, err := ChainResume(context.Background(), rt, ResumeOptions{
		Name:          "shop",
		ReviewerActor: "assistant",
	})
	if err != nil {
		t.Fatalf("ChainResume: %v", err)
	}
	if res.Chain.Status != string(chain.StatusRunning) {
		t.Errorf("result chain status = %q, want running", res.Chain.Status)
	}

	revAfter := chainBinding(t, rt, "shop-rev")
	if revAfter.Builder.StreamSessionID != "" {
		t.Errorf("StreamSessionID = %q, want empty", revAfter.Builder.StreamSessionID)
	}
	if revAfter.Owner != revBefore.Owner {
		t.Errorf("Owner = %q, want %q", revAfter.Owner, revBefore.Owner)
	}
	if !reflect.DeepEqual(revAfter.Serve, revBefore.Serve) {
		t.Errorf("Serve = %+v, want %+v", revAfter.Serve, revBefore.Serve)
	}
	if !reflect.DeepEqual(revAfter.Link, revBefore.Link) {
		t.Errorf("Link = %+v, want %+v", revAfter.Link, revBefore.Link)
	}
	if revAfter.CWD != revBefore.CWD {
		t.Errorf("CWD = %q, want %q", revAfter.CWD, revBefore.CWD)
	}
	if revAfter.Repo != revBefore.Repo {
		t.Errorf("Repo = %q, want %q", revAfter.Repo, revBefore.Repo)
	}

	pick, err := servedActorPick(rt, "assistant")
	if err != nil {
		t.Fatalf("servedActorPick: %v", err)
	}
	if revAfter.BuilderCandidate != pick.token {
		t.Errorf("candidate = %q, want %q", revAfter.BuilderCandidate, pick.token)
	}
	if revAfter.Builder.Kind != pick.kind {
		t.Errorf("Builder.Kind = %q, want %q", revAfter.Builder.Kind, pick.kind)
	}
}

func TestChainResumeFailureLeavesTheChainHalted(t *testing.T) {
	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainNoVerdictBody())

	origRow := chainStoredRow(t, rt, "shop")
	origMembers, err := rt.Store.ChainMembers("shop")
	if err != nil {
		t.Fatalf("ChainMembers: %v", err)
	}
	origBinding := chainBinding(t, rt, "shop-rev")
	origPending := chainPendingChain(t, rt, "shop")
	if len(origPending) == 0 {
		t.Fatal("test premise: must have pending chain delivery")
	}

	chainResumePostWriteHook = func(tx *store.Tx, c db.ChainRow) error {
		return errors.New("injected post-write failure")
	}
	defer func() { chainResumePostWriteHook = nil }()

	_, err = ChainResume(context.Background(), rt, ResumeOptions{
		Name:          "shop",
		ReviewerActor: "assistant",
	})
	if err == nil {
		t.Fatal("ChainResume = nil, want injected post-write failure")
	}

	afterRow := chainStoredRow(t, rt, "shop")
	if afterRow.Status != string(chain.StatusHalted) {
		t.Errorf("status = %q, want halted", afterRow.Status)
	}
	if afterRow.Reason != origRow.Reason {
		t.Errorf("reason = %q, want %q", afterRow.Reason, origRow.Reason)
	}
	if !bytes.Equal(afterRow.StateJSON, origRow.StateJSON) {
		t.Errorf("StateJSON altered: %s vs %s", afterRow.StateJSON, origRow.StateJSON)
	}
	if !bytes.Equal(afterRow.WorkflowJSON, origRow.WorkflowJSON) {
		t.Errorf("WorkflowJSON altered: %s vs %s", afterRow.WorkflowJSON, origRow.WorkflowJSON)
	}
	if !bytes.Equal(afterRow.SettingsJSON, origRow.SettingsJSON) {
		t.Errorf("SettingsJSON altered: %s vs %s", afterRow.SettingsJSON, origRow.SettingsJSON)
	}

	afterMembers, err := rt.Store.ChainMembers("shop")
	if err != nil {
		t.Fatalf("ChainMembers after: %v", err)
	}
	if !reflect.DeepEqual(afterMembers, origMembers) {
		t.Errorf("members = %+v, want %+v", afterMembers, origMembers)
	}

	afterBinding := chainBinding(t, rt, "shop-rev")
	afterBinding.UpdatedAt = origBinding.UpdatedAt
	afterBinding.RepoRef = origBinding.RepoRef
	if !reflect.DeepEqual(afterBinding, origBinding) {
		t.Errorf("binding altered: %+v vs %+v", afterBinding, origBinding)
	}

	afterPending := chainPendingChain(t, rt, "shop")
	if len(afterPending) != len(origPending) {
		t.Errorf("pending deliveries = %d, want %d", len(afterPending), len(origPending))
	}
}

func TestChainResumeFailureRemovesMembersItAdded(t *testing.T) {
	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowBranchWorkflow)
	if _, err := ChainStop(context.Background(), rt, "shop"); err != nil {
		t.Fatalf("ChainStop: %v", err)
	}

	chainResumePostWriteHook = func(tx *store.Tx, c db.ChainRow) error {
		return errors.New("injected post-write failure")
	}
	defer func() { chainResumePostWriteHook = nil }()

	_, err := ChainResume(context.Background(), rt, ResumeOptions{
		Name:   "shop",
		Params: map[string]string{"scan": "true"},
	})
	if err == nil {
		t.Fatal("ChainResume = nil, want forced failure")
	}

	if _, err := rt.Store.Load("shop-researcher"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Load shop-researcher = %v, want ErrNotFound", err)
	}
	rows, err := rt.Store.ChainMembers("shop")
	if err != nil {
		t.Fatalf("ChainMembers: %v", err)
	}
	for _, m := range rows {
		if m.Binding == "shop-researcher" {
			t.Errorf("chain_member still has row for shop-researcher: %+v", m)
		}
	}
}

func TestChainStopWithNoAwaitedMemberStopsDirectly(t *testing.T) {
	t.Parallel()

	t.Run("missing chain_member row", func(t *testing.T) {
		rt, _ := chainRuntime(t)
		startedChain(t, rt, ChainOptions{})

		if err := rt.Store.WithLock(func(tx *store.Tx) error {
			return tx.ChainMembersPut("shop", []db.ChainMemberRow{
				{Binding: "shop", Actor: "other", Seq: 0},
			})
		}); err != nil {
			t.Fatalf("ChainMembersPut: %v", err)
		}

		res, err := ChainStop(context.Background(), rt, "shop")
		if err != nil {
			t.Fatalf("ChainStop: %v", err)
		}
		if res.Action != ChainStopActionStopped {
			t.Errorf("res.Action = %q, want %q", res.Action, ChainStopActionStopped)
		}
		row := chainStoredRow(t, rt, "shop")
		if row.Status != string(chain.StatusStopped) {
			t.Errorf("chain status = %q, want stopped", row.Status)
		}
		events, err := rt.Store.ChainEvents("shop")
		if err != nil {
			t.Fatalf("ChainEvents: %v", err)
		}
		if len(events) != 1 {
			t.Errorf("trace events = %d, want 1", len(events))
		}
		if _, err := ChainDone(context.Background(), rt, "shop"); err != nil {
			t.Fatalf("ChainDone: %v", err)
		}
	})

	t.Run("binding deleted", func(t *testing.T) {
		rt, _ := chainRuntime(t)
		startedChain(t, rt, ChainOptions{})

		if err := rt.Store.WithLock(func(tx *store.Tx) error {
			return tx.Delete("shop")
		}); err != nil {
			t.Fatalf("tx.Delete: %v", err)
		}

		res, err := ChainStop(context.Background(), rt, "shop")
		if err != nil {
			t.Fatalf("ChainStop: %v", err)
		}
		if res.Action != ChainStopActionStopped {
			t.Errorf("res.Action = %q, want %q", res.Action, ChainStopActionStopped)
		}
		row := chainStoredRow(t, rt, "shop")
		if row.Status != string(chain.StatusStopped) {
			t.Errorf("chain status = %q, want stopped", row.Status)
		}
		events, err := rt.Store.ChainEvents("shop")
		if err != nil {
			t.Fatalf("ChainEvents: %v", err)
		}
		if len(events) != 1 {
			t.Errorf("trace events = %d, want 1", len(events))
		}
		if _, err := ChainDone(context.Background(), rt, "shop"); err != nil {
			t.Fatalf("ChainDone: %v", err)
		}
	})
}

func TestChainResumeFailureKeepsTheChainChecks(t *testing.T) {
	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowBranchWorkflow)
	if _, err := ChainStop(context.Background(), rt, "shop"); err != nil {
		t.Fatalf("ChainStop: %v", err)
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return tx.ChainCheckPut("shop", db.ChainCheckRow{Run: 1, Step: "check", Visit: 1, Command: "true", Result: "green"})
	}); err != nil {
		t.Fatalf("ChainCheckPut: %v", err)
	}

	chainResumePostWriteHook = func(tx *store.Tx, c db.ChainRow) error {
		return errors.New("injected post-write failure")
	}
	defer func() { chainResumePostWriteHook = nil }()

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{
		Name:   "shop",
		Params: map[string]string{"scan": "true"},
	}); err == nil {
		t.Fatal("ChainResume = nil, want forced failure")
	}

	check, err := rt.Store.ChainCheck("shop", 1)
	if err != nil {
		t.Fatalf("ChainCheck after a failed resume: %v", err)
	}
	if check.Result != "green" {
		t.Errorf("check result = %q, want green", check.Result)
	}
}
