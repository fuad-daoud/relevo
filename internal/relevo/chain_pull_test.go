package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainPullRuntime is a chain runtime whose remote is the given fake and whose
// transport absorbs a bundle: what every pull test needs before it seeds a
// server chain.
func chainPullRuntime(t *testing.T, fr *fakeRemote) Runtime {
	t.Helper()
	rt, _ := chainRuntime(t)
	rt.Remote = fr
	rt.Transport = &fakeTransport{}
	return rt
}

// chainPullMember is one member view with the rounds it closed: the view's
// Round is one past closed, the way a closed round advances a binding, and
// every round carries the done outcome and a diff.
func chainPullMemberView(part, name string, closed int, shape string) remote.ChainMemberView {
	mv := remote.ChainMemberView{Part: part, Name: name}
	mv.View = remote.BindingView{
		Name: name, Round: closed + 1, ClosedRound: closed,
		RoundState: remote.RoundIdle, Shape: shape, State: string(store.StateActive),
	}
	for r := 1; r <= closed; r++ {
		mv.Rounds = append(mv.Rounds, remote.ClosedRoundView{
			Round: r, ReportOutcome: reporttail.OutcomeDone,
			DiffNote: "1 file changed", DiffCommits: 1, DiffTree: "clean",
		})
	}
	return mv
}

// chainPullView is a server view whose three members closed the given rounds.
// A non-zero builderClosed gives the builder a result commit, so its branch
// has something to absorb.
func chainPullView(name, status string, builderClosed, reviewerClosed, plannerClosed int) remote.ChainView {
	builder := chainPullMemberView(chain.MemberBuilder, name, builderClosed, store.ShapeWriter)
	if builderClosed > 0 {
		builder.View.ResultCommit = fmt.Sprintf("builder-commit-%d", builderClosed)
	}
	v := chainPullViewOf(name, status,
		builder,
		chainPullMemberView(chain.MemberReviewer, name+"-rev", reviewerClosed, store.ShapeReader),
		chainPullMemberView(chain.MemberPlanner, name+"-plan", plannerClosed, store.ShapeReader),
	)
	return v
}

// chainPullViewOf is chainPullView with the members named directly.
func chainPullViewOf(name, status string, members ...remote.ChainMemberView) remote.ChainView {
	return remote.ChainView{
		Name: name, Status: status,
		Phase: string(chain.PhaseBuild), Step: string(chain.StepBuilding),
		Plan: 1, Plans: 1,
		AwaitingMember: chain.MemberBuilder, AwaitingRound: 1,
		Base: seedRemoteBase, Branch: "relevo/" + name,
		Members: members,
	}
}

// chainPullFiles serves a closed round's writer files for any round.
func chainPullFiles() func(context.Context, string, string, int, string) (io.ReadCloser, error) {
	files := map[string]string{
		"plan":   "the round prompt\n",
		"report": chainDoneBody(),
		"diff":   "--- a/f\n+++ b/f\n",
		"log":    "builder log\n",
	}
	return func(_ context.Context, _, _ string, _ int, kind string) (io.ReadCloser, error) {
		if body, ok := files[kind]; ok {
			return io.NopCloser(strings.NewReader(body)), nil
		}
		return nil, &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found"}}
	}
}

// chainPullArtifactFuncs serve every reader round one output file, so a pulled
// reader round installs its artifact instead of asking for a human.
func chainPullArtifactFuncs() (func(context.Context, string, string, int) (remote.ArtifactList, error), func(context.Context, string, string, int, string) (io.ReadCloser, error)) {
	list := func(_ context.Context, _, _ string, _ int) (remote.ArtifactList, error) {
		return remote.ArtifactList{
			Actor: "reviewer", Output: "findings",
			Files: []remote.ArtifactFile{{Rel: "findings", Size: 12}},
		}, nil
	}
	body := func(_ context.Context, _, _ string, _ int, _ string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("review output\n")), nil
	}
	return list, body
}

// chainPullFake is the server the pull tests read: the given view and files.
func chainPullFake(view remote.ChainView) *fakeRemote {
	artifacts, artifact := chainPullArtifactFuncs()
	return &fakeRemote{
		getChainResp:       view,
		roundFileFunc:      chainPullFiles(),
		roundArtifactsFunc: artifacts,
		roundArtifactFunc:  artifact,
	}
}

// pullRounds runs one chain pull over the stored chain and returns the row.
func pullRounds(t *testing.T, rt Runtime, name string) {
	t.Helper()
	if err := chainPullServers(context.Background(), rt); err != nil {
		t.Fatalf("chainPullServers: %v", err)
	}
	_ = chainStoredRow(t, rt, name)
}

// TestChainPullInstallsEveryRoundInOrder pins the whole install: a builder
// three rounds ahead, a reviewer two and a planner one all arrive, in order,
// with their prompt files, their reports and a consumed entry each.
func TestChainPullInstallsEveryRoundInOrder(t *testing.T) {
	t.Parallel()

	fr := chainPullFake(chainPullView("shop", string(chain.StatusRunning), 3, 2, 1))
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "shop")

	pullRounds(t, rt, "shop")

	builder := chainBinding(t, rt, "shop")
	if builder.Round != 4 {
		t.Errorf("builder round = %d, want 4 after rounds 1-3", builder.Round)
	}
	if builder.Builder.LastKnown != "builder-commit-3" || builder.RoundClosedTree != "builder-commit-3" {
		t.Errorf("builder last known = %q closed tree = %q, want the newest result commit",
			builder.Builder.LastKnown, builder.RoundClosedTree)
	}
	if builder.State != store.StateActive {
		t.Errorf("builder state = %q, want active", builder.State)
	}

	for r := 1; r <= 3; r++ {
		if !HasPromptEntry(chainLog(t, rt, "shop"), r) {
			t.Errorf("builder round %d has no prompt entry", r)
		}
		if !HasEntry(chainLog(t, rt, "shop"), r, store.DirToMasterMind, store.KindReport) {
			t.Errorf("builder round %d has no report entry", r)
		}
		if _, err := rt.Store.ReadFile(rt.Store.ReportPath("shop", r)); err != nil {
			t.Errorf("builder round %d report file: %v", r, err)
		}
		if _, err := rt.Store.ReadFile(rt.Store.PromptPath("shop", r)); err != nil {
			t.Errorf("builder round %d prompt file: %v", r, err)
		}
	}

	rev := chainBinding(t, rt, "shop-rev")
	if rev.Round != 3 {
		t.Errorf("reviewer round = %d, want 3 after rounds 1-2", rev.Round)
	}
	if plan := chainBinding(t, rt, "shop-plan"); plan.Round != 2 {
		t.Errorf("planner round = %d, want 2 after round 1", plan.Round)
	}

	for _, e := range chainLog(t, rt, "shop") {
		if e.Kind == store.KindReport && !e.Confirmed {
			t.Errorf("report entry for round %d is not consumed: %+v", e.Round, e)
		}
		if e.Kind == store.KindReport && !strings.Contains(e.Note, "consumed by chain shop") {
			t.Errorf("report entry %q does not name the chain", e.Note)
		}
	}

	// The prompt entries carry the member's chain step note, exactly as a
	// staged round does.
	if !promptNoteFor(chainLog(t, rt, "shop"), 1, "chain builder") {
		t.Errorf("prompt entry = %+v, want the chain builder note", chainLog(t, rt, "shop"))
	}
}

// TestChainPullAbsorbsTheBuilderBranch pins the branch catch-up: one bundle is
// requested for the builder at the closed round and the last known commit, and
// no reader asks for one.
func TestChainPullAbsorbsTheBuilderBranch(t *testing.T) {
	t.Parallel()

	fr := chainPullFake(chainPullView("shop", string(chain.StatusRunning), 2, 1, 0))
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "shop")
	// A reader carries a result commit too, so a pull that fetched a bundle
	// for one is visible rather than accidentally skipped.
	view := fr.getChainResp
	for i := range view.Members {
		if view.Members[i].Part == chain.MemberReviewer {
			view.Members[i].View.ResultCommit = "reviewer-commit-1"
		}
	}
	fr.getChainResp = view
	b := chainBinding(t, rt, "shop")
	b.Builder.LastKnown = "builder-commit-0"
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	pullRounds(t, rt, "shop")

	if n := countCalls(fr, "RoundBundle:zen:shop:2:builder-commit-0"); n != 1 {
		t.Errorf("builder bundle calls = %d, want 1 at round 2 since the last known commit: %v", n, fr.calls)
	}
	if n := countCalls(fr, "RoundBundle:zen:shop-rev"); n != 0 {
		t.Errorf("reviewer bundle calls = %d, want none for a reader: %v", n, fr.calls)
	}
	after := chainBinding(t, rt, "shop")
	if after.Builder.LastKnown != "builder-commit-2" {
		t.Errorf("LastKnown = %q, want the absorbed result commit", after.Builder.LastKnown)
	}
}

// TestChainPullStopsAtAFailedRound pins the in-order rule: a round that cannot
// be fetched installs none after it, and the next pass resumes there.
func TestChainPullStopsAtAFailedRound(t *testing.T) {
	t.Parallel()

	base := chainPullFiles()
	fr := chainPullFake(chainPullView("shop", string(chain.StatusRunning), 3, 0, 0))
	fr.roundFileFunc = func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
		if round == 2 {
			return nil, errors.New("boom")
		}
		return base(ctx, server, name, round, kind)
	}
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "shop")

	pullRounds(t, rt, "shop")

	builder := chainBinding(t, rt, "shop")
	if builder.Round != 2 {
		t.Fatalf("builder round = %d, want 2: only round 1 may install", builder.Round)
	}
	if !HasEntry(chainLog(t, rt, "shop"), 1, store.DirToMasterMind, store.KindReport) {
		t.Error("round 1 must be installed")
	}
	if HasEntry(chainLog(t, rt, "shop"), 2, store.DirToMasterMind, store.KindReport) {
		t.Error("round 2 must not be installed while its fetch fails")
	}

	// The server answers again: the next pass resumes at round 2.
	fr.roundFileFunc = chainPullFiles()
	pullRounds(t, rt, "shop")
	if builder := chainBinding(t, rt, "shop"); builder.Round != 4 {
		t.Errorf("builder round = %d, want 4 after the failed round clears", builder.Round)
	}
}

// TestChainPullSavesTheRoundsBeforeAFailedInstall pins the in-order save when a
// round's files cannot be installed: the rounds before it commit with the member
// advanced past them while the failed round does not, so the next pass resumes
// at that round instead of re-installing the earlier rounds and doubling their
// log entries.
func TestChainPullSavesTheRoundsBeforeAFailedInstall(t *testing.T) {
	t.Parallel()

	fr := chainPullFake(chainPullView("shop", string(chain.StatusRunning), 2, 0, 0))
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "shop")

	// Round 2's install fails: its report path is a directory, so the apply's
	// rename cannot land the fetched report there.
	if err := os.MkdirAll(rt.Store.ReportPath("shop", 2), 0o755); err != nil {
		t.Fatalf("occupy round 2's report path: %v", err)
	}

	pullRounds(t, rt, "shop")

	if b := chainBinding(t, rt, "shop"); b.Round != 2 {
		t.Fatalf("builder round = %d, want 2: round 1 lands and round 2 stops there", b.Round)
	}
	if got := countLogEntries(chainLog(t, rt, "shop"), 1, store.DirToBuilder, store.KindPrompt); got != 1 {
		t.Fatalf("round 1 prompt entries = %d, want 1", got)
	}
	if got := countLogEntries(chainLog(t, rt, "shop"), 1, store.DirToMasterMind, store.KindReport); got != 1 {
		t.Fatalf("round 1 report entries = %d, want 1", got)
	}

	// The second pass resumes at round 2 and must not revisit round 1.
	pullRounds(t, rt, "shop")

	if got := countLogEntries(chainLog(t, rt, "shop"), 1, store.DirToBuilder, store.KindPrompt); got != 1 {
		t.Errorf("after a second pass, round 1 prompt entries = %d, want still 1", got)
	}
	if got := countLogEntries(chainLog(t, rt, "shop"), 1, store.DirToMasterMind, store.KindReport); got != 1 {
		t.Errorf("after a second pass, round 1 report entries = %d, want still 1", got)
	}
}

// countLogEntries counts a binding's entries of one shape for one round.
func countLogEntries(entries []store.LogEntry, round int, dir store.Direction, kind store.Kind) int {
	n := 0
	for _, e := range entries {
		if e.Round == round && e.Direction == dir && e.Kind == kind {
			n++
		}
	}
	return n
}

// TestChainPullAcksAfterInstall pins the ack's order: the server is acked only
// once the round it names is stored on this machine.
func TestChainPullAcksAfterInstall(t *testing.T) {
	t.Parallel()

	fr := chainPullFake(chainPullView("shop", string(chain.StatusRunning), 1, 0, 0))
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "shop")

	var sawInstall bool
	fr.beforeCall = func(call string) {
		if !strings.HasPrefix(call, "Ack:") {
			return
		}
		entries, err := rt.Store.ReadLog("shop")
		if err == nil && HasEntry(entries, 1, store.DirToMasterMind, store.KindReport) {
			sawInstall = true
		}
	}

	pullRounds(t, rt, "shop")

	if n := countCalls(fr, "Ack:zen:shop:1"); n != 1 {
		t.Fatalf("ack calls = %d, want 1: %v", n, fr.calls)
	}
	if !sawInstall {
		t.Error("the ack left before round 1 was stored")
	}
}

// TestChainPullQueuesTheEndDeliveryOnlyOnceCaughtUp pins the delivery's guard
// in four arms: a terminal chain that is still behind queues nothing, a caught
// up one queues exactly one, a second pass adds none, and a resume that ends
// again queues one more.
func TestChainPullQueuesTheEndDeliveryOnlyOnceCaughtUp(t *testing.T) {
	t.Parallel()

	t.Run("terminal but behind queues none", func(t *testing.T) {
		t.Parallel()

		fr := chainPullFake(chainPullView("shop", string(chain.StatusStopped), 2, 0, 0))
		fr.roundFileFunc = func(context.Context, string, string, int, string) (io.ReadCloser, error) {
			return nil, errors.New("no round files")
		}
		rt := chainPullRuntime(t, fr)
		seedServerChain(t, rt, "shop")

		pullRounds(t, rt, "shop")

		if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusRunning) {
			t.Errorf("mirror status = %q, want running while a member is behind", row.Status)
		}
		if pending := chainPendingChain(t, rt, "shop"); len(pending) != 0 {
			t.Errorf("pending deliveries = %d, want none while behind", len(pending))
		}
	})

	t.Run("caught up queues one then a second pass adds none", func(t *testing.T) {
		t.Parallel()

		fr := chainPullFake(chainPullView("shop", string(chain.StatusStopped), 2, 1, 0))
		rt := chainPullRuntime(t, fr)
		seedServerChain(t, rt, "shop")

		pullRounds(t, rt, "shop")

		if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusStopped) {
			t.Fatalf("mirror status = %q, want stopped once caught up", row.Status)
		}
		if pending := chainPendingChain(t, rt, "shop"); len(pending) != 1 {
			t.Fatalf("pending deliveries = %d, want 1", len(pending))
		}
		if !strings.Contains(chainPendingChain(t, rt, "shop")[0].Payload, "chain shop stopped") {
			t.Errorf("payload = %q, want the chain end payload", chainPendingChain(t, rt, "shop")[0].Payload)
		}

		pullRounds(t, rt, "shop")
		if pending := chainPendingChain(t, rt, "shop"); len(pending) != 1 {
			t.Errorf("pending deliveries after a second pass = %d, want still 1", len(pending))
		}
	})

	t.Run("resumed then terminal again queues another", func(t *testing.T) {
		t.Parallel()

		view := chainPullView("shop", string(chain.StatusStopped), 1, 0, 0)
		fr := chainPullFake(view)
		rt := chainPullRuntime(t, fr)
		seedServerChain(t, rt, "shop")

		pullRounds(t, rt, "shop")
		if pending := chainPendingChain(t, rt, "shop"); len(pending) != 1 {
			t.Fatalf("pending deliveries = %d, want 1", len(pending))
		}

		// The server resumes, then ends again.
		view.Status = string(chain.StatusRunning)
		fr.getChainResp = view
		pullRounds(t, rt, "shop")
		if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusRunning) {
			t.Fatalf("mirror status = %q, want running again after a resume", row.Status)
		}

		view.Status = string(chain.StatusHalted)
		view.Reason = "reviewer gave no verdict"
		fr.getChainResp = view
		pullRounds(t, rt, "shop")

		if pending := chainPendingChain(t, rt, "shop"); len(pending) != 2 {
			t.Errorf("pending deliveries = %d, want a second after the resumed chain ended: %+v", len(pending), pending)
		}
	})

	t.Run("a member level with its closed round is not caught up", func(t *testing.T) {
		t.Parallel()

		// The builder's round files cannot be fetched, so it stays at round 1
		// while the server closed round 1: exactly level, not past it. The
		// reviewer installs normally and is caught up, so the builder is the
		// only member the guard can hold on.
		fr := chainPullFake(chainPullView("shop", string(chain.StatusStopped), 1, 1, 0))
		base := chainPullFiles()
		fr.roundFileFunc = func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
			if name == "shop" {
				return nil, errors.New("the builder's round files are gone")
			}
			return base(ctx, server, name, round, kind)
		}
		rt := chainPullRuntime(t, fr)
		seedServerChain(t, rt, "shop")

		pullRounds(t, rt, "shop")

		if b := chainBinding(t, rt, "shop"); b.Round != 1 {
			t.Fatalf("builder round = %d, want 1: the fetch failed, so the round cannot advance", b.Round)
		}
		if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusRunning) {
			t.Errorf("mirror status = %q, want running: a member level with its closed round is not caught up", row.Status)
		}
		if pending := chainPendingChain(t, rt, "shop"); len(pending) != 0 {
			t.Errorf("pending deliveries = %d, want none while a member sits level with its closed round", len(pending))
		}
	})
}

// TestChainPullCreatesAMemberTheServerAdded pins the late member: a server
// member the mirror does not name yet gets its binding created and its column
// written, so the same pass can install that member's rounds.
func TestChainPullCreatesAMemberTheServerAdded(t *testing.T) {
	t.Parallel()

	fr := chainPullFake(chainPullView("shop", string(chain.StatusRunning), 1, 0, 0))
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "shop")

	// The server's view carries the security member a resume added.
	view := fr.getChainResp
	view.Members = append(view.Members, chainPullMemberView(chain.MemberSecurity, "shop-sec", 1, store.ShapeReader))
	fr.getChainResp = view

	pullRounds(t, rt, "shop")

	sec, err := rt.Store.Load("shop-sec")
	if err != nil {
		t.Fatalf("the added member must exist: %v", err)
	}
	if !sec.Builder.Remote() || sec.Builder.Server != "zen" || sec.Shape != store.ShapeReader || sec.Branch != "" {
		t.Errorf("security member = %+v, want a branchless reader remote on zen", sec)
	}
	if row := chainStoredRow(t, rt, "shop"); row.Security != "shop-sec" {
		t.Errorf("row security = %q, want shop-sec", row.Security)
	}
	if !HasEntry(chainLog(t, rt, "shop-sec"), 1, store.DirToMasterMind, store.KindReport) {
		t.Error("the added member's closed round must be installed in the same pass")
	}
}

// TestChainPullAddedMemberGetsAMemberRow pins the chain_member write for a late
// member: a server member the mirror creates gets its chain_member row beside
// its binding, so chainReadMembers and chainEndMembers include it.
func TestChainPullAddedMemberGetsAMemberRow(t *testing.T) {
	t.Parallel()

	fr := chainPullFake(chainPullView("shop", string(chain.StatusRunning), 1, 0, 0))
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "shop")

	view := fr.getChainResp
	sec := chainPullMemberView(chain.MemberSecurity, "shop-sec", 1, store.ShapeReader)
	sec.Actor = "security"
	view.Members = append(view.Members, sec)
	fr.getChainResp = view

	pullRounds(t, rt, "shop")

	rows, err := rt.Store.ChainMembers("shop")
	if err != nil {
		t.Fatalf("ChainMembers: %v", err)
	}
	found := false
	for _, m := range rows {
		if m.Binding == "shop-sec" {
			found = true
			if m.Actor != "security" {
				t.Errorf("member row actor = %q, want security", m.Actor)
			}
		}
	}
	if !found {
		t.Fatalf("chain_member rows = %+v, want shop-sec", rows)
	}

	row := chainStoredRow(t, rt, "shop")
	if got := chainReadMembers(rt.Store, row); !containsString(got, "shop-sec") {
		t.Errorf("chainReadMembers = %v, want shop-sec", got)
	}
	if got := chainEndMembers(rt.Store, row); !containsString(got, "shop-sec") {
		t.Errorf("chainEndMembers = %v, want shop-sec", got)
	}
}

// containsString reports whether a string list holds want.
func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// TestChainPullHaltsAChainGoneFromTheServer pins the missing chain: the mirror
// halts once with the server's absence as the reason, queues the one delivery,
// and a repeated 404 adds nothing.
func TestChainPullHaltsAChainGoneFromTheServer(t *testing.T) {
	t.Parallel()

	fr := &fakeRemote{
		getChainErr: &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: remote.CodeNotFound}},
	}
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "shop")

	pullRounds(t, rt, "shop")

	row := chainStoredRow(t, rt, "shop")
	if row.Status != string(chain.StatusHalted) {
		t.Fatalf("mirror status = %q, want halted", row.Status)
	}
	if row.Reason != "chain shop is gone from zen" {
		t.Errorf("reason = %q, want it to name the chain and the server", row.Reason)
	}
	if pending := chainPendingChain(t, rt, "shop"); len(pending) != 1 {
		t.Fatalf("pending deliveries = %d, want 1", len(pending))
	}

	pullRounds(t, rt, "shop")
	if pending := chainPendingChain(t, rt, "shop"); len(pending) != 1 {
		t.Errorf("pending deliveries after a second 404 = %d, want still 1", len(pending))
	}
}

// TestChainPullSkipsADoneMirror pins the walk's filter: a mirror the local
// machine has finished is never read from the server again.
func TestChainPullSkipsADoneMirror(t *testing.T) {
	t.Parallel()

	fr := chainPullFake(chainPullView("shop", string(chain.StatusRunning), 1, 0, 0))
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "shop")
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		row, err := tx.Chain("shop")
		if err != nil {
			return err
		}
		row.Status = string(chain.StatusDone)
		return tx.ChainPut(row)
	}); err != nil {
		t.Fatalf("mark done: %v", err)
	}

	pullRounds(t, rt, "shop")

	if n := countCalls(fr, "GetChain:"); n != 0 {
		t.Errorf("GetChain calls = %d, want none for a done mirror", n)
	}
}

// TestChainPullSkipsTheBindingCatchUp pins that the per-binding machinery never
// moves a mirror member: the daemon prefetch answers none and the reconcile
// changes nothing, so only the pull can advance one.
func TestChainPullSkipsTheBindingCatchUp(t *testing.T) {
	t.Parallel()

	fr := chainPullFake(chainPullView("shop", string(chain.StatusRunning), 1, 0, 0))
	rt := chainPullRuntime(t, fr)
	b := seedServerChain(t, rt, "shop")

	if pre := NewDaemon(rt, time.Minute).prefetchRemote(context.Background(), b); pre != nil {
		t.Errorf("prefetchRemote = %+v, want nil for a mirror member", pre)
	}
	var next store.Binding
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		var rerr error
		next, rerr = reconcileRemote(context.Background(), rt, tx, b, nil)
		return rerr
	}); err != nil {
		t.Fatalf("reconcileRemote: %v", err)
	}
	if !store.SameBinding(next, b) {
		t.Errorf("reconcileRemote changed the mirror: %+v -> %+v", b, next)
	}
}

// TestChainPullSetsStateFromView pins the mirror's engine state: the pull
// stores the shipped default and the engine state the server's legacy columns
// describe, through workflow.FromLegacy, so the mirror's read surfaces see the
// position the server drove.
func TestChainPullSetsStateFromView(t *testing.T) {
	t.Parallel()

	fr := chainPullFake(chainPullView("shop", string(chain.StatusRunning), 0, 0, 0))
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "shop")

	view := fr.getChainResp
	view.Phase = string(chain.PhaseBuild)
	view.Step = string(chain.StepReviewing)
	view.Plan, view.Plans, view.Corrections = 2, 2, 1
	view.AwaitingMember = chain.MemberReviewer
	view.AwaitingRound = 2
	fr.getChainResp = view

	pullRounds(t, rt, "shop")

	row := chainStoredRow(t, rt, "shop")
	if len(row.WorkflowJSON) == 0 || len(row.StateJSON) == 0 {
		t.Fatalf("mirror row carries workflow %d state %d bytes, want both", len(row.WorkflowJSON), len(row.StateJSON))
	}
	def, err := workflow.Parse(row.WorkflowJSON)
	if err != nil {
		t.Fatalf("parse the stored workflow: %v", err)
	}
	if def.Name != workflow.Default().Name {
		t.Errorf("stored workflow name = %q, want the shipped default", def.Name)
	}
	var st workflow.State
	if err := json.Unmarshal(row.StateJSON, &st); err != nil {
		t.Fatalf("decode the stored state: %v", err)
	}
	if st.At != "review" || st.Iter["plans"].Index != 1 {
		t.Errorf("state = at %q plan index %d, want review and 1", st.At, st.Iter["plans"].Index)
	}
}

var _ = time.Second
