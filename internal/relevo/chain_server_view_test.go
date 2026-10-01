package relevo

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/store"
)

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

// TestChainDoneOnAServerChainPostsDone pins done: the server releases the
// chain, every mirror member is released through the ordinary done, and the
// mirror row is closed.
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
		if n := exactCalls(fr.calls, "Done:zen:"+member); n != 1 {
			t.Errorf("Done calls for %s = %d, want 1", member, n)
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
