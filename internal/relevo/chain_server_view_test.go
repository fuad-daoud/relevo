package relevo

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestDoneOnAServerChainMirrorDoesNotCallTheServer pins the mirror rule: a
// member of a chain that runs on a server is released by the chain's own verbs
// there, so the client's done must not ask the server to release it again --
// with no Runtime.Remote at all, the release still succeeds locally.
func TestDoneOnAServerChainMirrorDoesNotCallTheServer(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	rt.Remote = nil // any call to the server would be ErrRemoteUnavailable

	now := rt.Now().UTC()
	row := db.ChainRow{
		ID: db.NewID(), Name: "mesh", Status: string(chain.StatusHalted),
		Phase: "build", Step: "planning-fixes", Plan: 1, Plans: 1,
		Builder: "mesh", Planner: "mesh-plan", Server: "zen",
		MasterMindID: testMasterMindID, CreatedAt: now, UpdatedAt: now,
	}
	member := store.Binding{
		Name: "mesh-plan", CWD: "/repo/mesh", Round: 1, State: store.StateNeedsYou,
		Role: "lite-planner", Shape: store.ShapeReader, MasterMindID: testMasterMindID,
		Builder: store.Endpoint{Kind: "opencode", Mode: store.ModeRemote, Server: "zen"},
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		if err := tx.ChainPut(row); err != nil {
			return err
		}
		return tx.Save(member)
	}); err != nil {
		t.Fatalf("seed the mirror: %v", err)
	}

	if _, err := Done(context.Background(), rt, "mesh-plan"); err != nil {
		t.Fatalf("Done on a server-chain mirror = %v, want it released locally", err)
	}
	got, err := rt.Store.Load("mesh-plan")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateDone {
		t.Errorf("state = %q, want done", got.State)
	}
}

// TestRoundOpenFromWireRebuildsTheTypedRefusal pins the client half of the
// round-open mapping: the server's 409 round_open becomes the send path's own
// typed refusal, so the CLI prints the conflict and its `relevo stop <member>`
// next line; anything unparseable stays the server's error.
func TestRoundOpenFromWireRebuildsTheTypedRefusal(t *testing.T) {
	t.Parallel()

	err := roundOpenFromWire(&client.HTTPError{
		Status: 409,
		Body: remote.ErrorBody{
			Code:    remote.CodeRoundOpen,
			Message: "shop-plan: round 3 is still open; relevo stop shop-plan ends it",
		},
	})
	var open *RoundOpenError
	if !errors.As(err, &open) {
		t.Fatalf("err = %v, want *RoundOpenError", err)
	}
	if open.Member != "shop-plan" || open.Round != 3 {
		t.Errorf("RoundOpenError = %+v, want member shop-plan round 3", open)
	}

	raw := errors.New("boom")
	if got := roundOpenFromWire(raw); !errors.Is(got, raw) {
		t.Errorf("roundOpenFromWire(non-HTTP error) = %v, want the original error", got)
	}
	if got := roundOpenFromWire(&client.HTTPError{Status: 409, Body: remote.ErrorBody{Message: "no round here"}}); got == nil || errors.As(got, &open) {
		t.Errorf("roundOpenFromWire(unparseable message) = %v, want the server's error kept", got)
	}
}

// TestChainStatusOnAServerChainReadsTheServer pins the status verb's source:
// the chain row is the server's view, not this machine's mirror, and a server
// this machine cannot read is named on the row instead of guessed at.
func TestChainStatusOnAServerChainReadsTheServer(t *testing.T) {
	t.Parallel()

	t.Run("the server view is the row", func(t *testing.T) {
		t.Parallel()

		view := chainPullView("shop", string(chain.StatusHalted), 0, 0, 0)
		view.Reason = "reviewer gave no verdict"
		fr := chainPullFake(view)
		rt := chainPullRuntime(t, fr)
		seedServerChain(t, rt, "shop")

		rep, err := ChainStatus(context.Background(), rt, "shop")
		if err != nil {
			t.Fatalf("ChainStatus: %v", err)
		}
		row := rep.Bindings[0]
		if row.Name != "shop" {
			t.Fatalf("first row = %q, want the chain", row.Name)
		}
		if row.State != string(store.StateNeedsYou) || row.Detail != "reviewer gave no verdict" {
			t.Errorf("row = state %q detail %q, want the server's halt", row.State, row.Detail)
		}
		if chainStoredRow(t, rt, "shop").Status != string(chain.StatusRunning) {
			t.Errorf("the mirror must be left alone by a read verb")
		}
	})

	t.Run("an unreachable server is reported", func(t *testing.T) {
		t.Parallel()

		fr := chainPullFake(chainPullView("shop", string(chain.StatusRunning), 0, 0, 0))
		fr.getChainErr = errors.New("dial tcp: connection refused")
		rt := chainPullRuntime(t, fr)
		seedServerChain(t, rt, "shop")

		rep, err := ChainStatus(context.Background(), rt, "shop")
		if err != nil {
			t.Fatalf("ChainStatus: %v", err)
		}
		if got := rep.Bindings[0].Detail; !strings.Contains(got, "server zen unreachable") {
			t.Errorf("detail = %q, want the unreachable server named", got)
		}
	})
}

// TestChainTraceOnAServerChainRendersTheServerTrace pins the trace verb: the
// document is the server's own trace rows, decoded exactly as a local trace is.
func TestChainTraceOnAServerChainRendersTheServerTrace(t *testing.T) {
	t.Parallel()

	ev := chain.Event{
		Kind: chain.EventBuilderClosed, Member: chain.MemberBuilder,
		Round: 1, Outcome: reporttail.OutcomeDone, Gate: chain.GateGreen,
	}
	act := chain.Action{Kind: chain.ActionSend, Member: chain.MemberReviewer, Seed: chain.SeedReviewer}
	view := chainPullView("shop", string(chain.StatusRunning), 1, 0, 0)
	view.Trace = []remote.ChainEventView{{
		Seq: 1, TS: baseTime, Phase: string(chain.PhaseBuild), Step: string(chain.StepBuilding),
		Member: "shop", Round: 1, Plan: 1, Event: ev.Encode(), Action: act.Encode(),
	}}
	fr := chainPullFake(view)
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "shop")

	doc, err := ChainTrace(context.Background(), rt, "shop")
	if err != nil {
		t.Fatalf("ChainTrace: %v", err)
	}
	if len(doc.Events) != 1 {
		t.Fatalf("trace events = %d, want 1", len(doc.Events))
	}
	if doc.Events[0].Event.Kind != chain.EventBuilderClosed || doc.Events[0].Action.Kind != chain.ActionSend {
		t.Errorf("event/action = %+v/%+v, want the server's row decoded",
			doc.Events[0].Event, doc.Events[0].Action)
	}
	if doc.Events[0].Member != "shop" || doc.Events[0].Round != 1 {
		t.Errorf("event names (%q, %d), want (shop, 1)", doc.Events[0].Member, doc.Events[0].Round)
	}
}

// TestWaitChainOnAServerChainPullsThenReturnsTheDelivery pins wait: each poll
// pulls the mirror, and the chain's one queued delivery comes back as the
// payload once the mirror is terminal.
func TestWaitChainOnAServerChainPullsThenReturnsTheDelivery(t *testing.T) {
	t.Parallel()

	fr := chainPullFake(chainPullView("shop", string(chain.StatusStopped), 1, 0, 0))
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "shop")

	res, err := WaitChain(context.Background(), rt, "shop", time.Second, time.Millisecond, false)
	if err != nil {
		t.Fatalf("WaitChain: %v", err)
	}
	if !res.Done || res.Code != WaitNeedsYou {
		t.Fatalf("wait result = %+v, want a stopped chain as needs-you", res)
	}
	if !strings.Contains(res.Payload, "chain shop stopped") {
		t.Errorf("payload = %q, want the chain end payload", res.Payload)
	}
	if builder := chainBinding(t, rt, "shop"); builder.Round != 2 {
		t.Errorf("builder round = %d, want 2: the wait must pull the closed round", builder.Round)
	}
}

// TestChainStopOnAServerChainPostsStop pins stop: the server is asked and its
// answer is the result; a chain with nothing to stop maps to the ordinary
// nothing-to-stop answer.
func TestChainStopOnAServerChainPostsStop(t *testing.T) {
	t.Parallel()

	fr := chainPullFake(chainPullView("shop", string(chain.StatusRunning), 0, 0, 0))
	fr.chainStopResp = remote.ChainStopResponse{Round: 2, Action: "killed"}
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "shop")

	res, err := ChainStop(context.Background(), rt, "shop")
	if err != nil {
		t.Fatalf("ChainStop: %v", err)
	}
	if res.Round != 2 || res.Action != "killed" {
		t.Errorf("stop result = %+v, want the server's answer", res)
	}
	if n := countCalls(fr, "ChainStop:zen:shop"); n != 1 {
		t.Errorf("ChainStop calls = %d, want 1", n)
	}

	fr.chainStopErr = &client.HTTPError{Status: 409, Body: remote.ErrorBody{Code: remote.CodeNothingToStop}}
	if _, err := ChainStop(context.Background(), rt, "shop"); !errors.Is(err, ErrNothingToStop) {
		t.Errorf("stop on a chain with nothing to stop = %v, want ErrNothingToStop", err)
	}
}

// TestChainResumeOnAServerChainSendsTheResolvedGate pins the gate the resume
// posts: an explicit --gate travels as its own command, --no-gate clears it,
// and no flag at all leaves the field off the wire.
func TestChainResumeOnAServerChainSendsTheResolvedGate(t *testing.T) {
	t.Parallel()

	arms := []struct {
		name   string
		opts   ResumeOptions
		want   string
		wantOK bool
	}{
		{"gate given", ResumeOptions{Name: "shop", Gate: "make check"}, "make check", true},
		{"no gate", ResumeOptions{Name: "shop", NoGate: true}, "", true},
		{"absent", ResumeOptions{Name: "shop"}, "", false},
	}
	for _, arm := range arms {
		t.Run(arm.name, func(t *testing.T) {
			t.Parallel()

			fr := chainPullFake(chainPullView("shop", string(chain.StatusRunning), 0, 0, 0))
			rt := chainPullRuntime(t, fr)
			seedServerChain(t, rt, "shop")

			res, err := ChainResume(context.Background(), rt, arm.opts)
			if err != nil {
				t.Fatalf("ChainResume: %v", err)
			}
			if res.Chain.Name != "shop" {
				t.Errorf("result chain = %q, want the mirror", res.Chain.Name)
			}
			got := fr.chainResumeReq.Gate
			if arm.wantOK {
				if got == nil || *got != arm.want {
					t.Errorf("gate = %v, want %q", got, arm.want)
				}
			} else if got != nil {
				t.Errorf("gate = %q, want the field left off the wire", *got)
			}
		})
	}
}

// TestChainServerResumeSupersedesTheMirrorsQueuedHalt pins the client half of
// the stale delivery rule: the mirror's own queued end payload is confirmed by
// the resume, so a stale NEEDS YOU is never delivered after a chain that runs
// on a server has moved on.
func TestChainServerResumeSupersedesTheMirrorsQueuedHalt(t *testing.T) {
	t.Parallel()

	fr := chainPullFake(chainPullView("shop", string(chain.StatusHalted), 1, 0, 0))
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "shop")

	// The halt the mirror pulled: the chain row is terminal and the end
	// payload sits undelivered on the builder member.
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		row, err := tx.Chain("shop")
		if err != nil {
			return err
		}
		row.Status = string(chain.StatusHalted)
		if err := tx.ChainPut(row); err != nil {
			return err
		}
		return tx.AppendLog("shop", store.LogEntry{
			TS: rt.Now().UTC(), Round: 3,
			Direction: store.DirToMasterMind, Kind: store.KindChain,
			Payload: "chain shop halted: builder halted on plan 1",
		})
	}); err != nil {
		t.Fatalf("seed the mirror's halt: %v", err)
	}

	pendingChain := func() int {
		t.Helper()
		n := 0
		err := rt.Store.WithLock(func(tx *store.Tx) error {
			entries, err := tx.PendingForMasterMindThrough("shop", 0)
			if err != nil {
				return err
			}
			for _, p := range entries {
				if p.Entry.Kind == store.KindChain {
					n++
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("read pending entries: %v", err)
		}
		return n
	}
	if pendingChain() == 0 {
		t.Fatal("test premise: the halt must queue the mirror's end delivery")
	}

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}
	if n := pendingChain(); n != 0 {
		t.Errorf("pending chain deliveries after a server resume = %d, want 0", n)
	}
}

// TestChainServerResumeRetypesAChainDone pins the client half of the done
// refusal: the server's 409 chain_done becomes the typed ErrChainDone, so the
// CLI reports a conflict and a script can tell a settled chain from an
// internal failure.
func TestChainServerResumeRetypesAChainDone(t *testing.T) {
	t.Parallel()

	fr := chainPullFake(chainPullView("shop", string(chain.StatusDone), 1, 0, 0))
	fr.chainResumeErr = &client.HTTPError{
		Status: http.StatusConflict,
		Body:   remote.ErrorBody{Code: remote.CodeChainDone, Message: "chain is done"},
	}
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "shop")

	_, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"})
	if err == nil {
		t.Fatal("ChainResume = nil, want the done refusal")
	}
	if !errors.Is(err, ErrChainDone) {
		t.Errorf("err = %v, want errors.Is(err, ErrChainDone)", err)
	}
	if !strings.Contains(err.Error(), "chain shop is done") {
		t.Errorf("err = %q, want it to name the chain's state", err)
	}
}

// TestChainDoneOnAServerChainPostsDone pins done: the server releases the
// chain, the mirror members are released locally (the chain's own verb already
// released them there, so no second round-trip per member), and the mirror row
// is closed.
func TestChainDoneOnAServerChainPostsDone(t *testing.T) {
	t.Parallel()

	fr := chainPullFake(chainPullView("shop", string(chain.StatusStopped), 1, 0, 0))
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "shop")

	if _, err := ChainDone(context.Background(), rt, "shop"); err != nil {
		t.Fatalf("ChainDone: %v", err)
	}
	if n := countCalls(fr, "ChainDone:zen:shop"); n != 1 {
		t.Errorf("ChainDone calls = %d, want 1", n)
	}
	for _, member := range []string{"shop", "shop-rev", "shop-plan"} {
		if n := exactCalls(fr.calls, "Done:zen:"+member); n != 0 {
			t.Errorf("Done calls for the mirror %s = %d, want none: the chain released it on the server", member, n)
		}
		if got, err := rt.Store.Load(member); err != nil {
			t.Errorf("load mirror %s: %v", member, err)
		} else if got.State != store.StateDone {
			t.Errorf("mirror %s state = %q, want done", member, got.State)
		}
	}
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusDone) {
		t.Errorf("mirror status = %q, want done", row.Status)
	}
}

// TestChainDoneOnAServerChainRefusesWhileRunning pins the running refusal: a
// server that still runs the chain answers 409 and the verb carries it as the
// chain-running refusal.
func TestChainDoneOnAServerChainRefusesWhileRunning(t *testing.T) {
	t.Parallel()

	fr := chainPullFake(chainPullView("shop", string(chain.StatusRunning), 0, 0, 0))
	fr.chainDoneErr = &client.HTTPError{Status: 409, Body: remote.ErrorBody{Code: remote.CodeChainRunning}}
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "shop")

	if _, err := ChainDone(context.Background(), rt, "shop"); !errors.Is(err, ErrChainRunning) {
		t.Errorf("ChainDone on a running server chain = %v, want ErrChainRunning", err)
	}
}

// exactCalls counts the recorded calls equal to one exact string.
func exactCalls(calls []string, want string) int {
	n := 0
	for _, c := range calls {
		if c == want {
			n++
		}
	}
	return n
}
