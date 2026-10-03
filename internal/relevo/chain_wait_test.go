package relevo

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/store"
)

// waitChainOpts is the poll configuration every WaitChain test uses: a long
// timeout (a terminal chain answers on the first poll, so it never elapses)
// and a short interval.
func waitChainOpts() (time.Duration, time.Duration) {
	return 10 * time.Minute, time.Millisecond
}

// finishedChain starts a one-plan chain and finishes it: the builder closes
// done, the reviewer passes, and the end delivery is queued on the builder.
func finishedChain(t *testing.T, rt Runtime) {
	t.Helper()
	startedChain(t, rt, ChainOptions{})
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("pass"))
}

// TestWaitChainFinishedReturnsZeroAndThePayload pins the finished case: exit 0,
// a line naming the chain's end, and the one end delivery as the payload.
func TestWaitChainFinishedReturnsZeroAndThePayload(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	finishedChain(t, rt)

	timeout, interval := waitChainOpts()
	res, err := WaitChain(context.Background(), rt, "shop", timeout, interval, false)
	if err != nil {
		t.Fatalf("WaitChain: %v", err)
	}
	if res.Code != 0 || !res.Done {
		t.Errorf("result = code %d done %v, want 0 and done", res.Code, res.Done)
	}
	if !strings.Contains(res.Line, "chain shop: done") {
		t.Errorf("line = %q, want the chain's end", res.Line)
	}
	if !strings.Contains(res.Line, "plan 1/1 · 0 corrections") {
		t.Errorf("line = %q, want the plan and correction counts", res.Line)
	}
	if !strings.Contains(res.Payload, "status done") {
		t.Errorf("payload = %q, want the chain's end delivery", res.Payload)
	}
	if res.DeliverErr != nil {
		t.Errorf("deliver error = %v, want none", res.DeliverErr)
	}
}

// TestWaitChainHaltedReturnsThreeAndTheReason pins the halting case: exit 3,
// with the reason on the line and the end delivery as the payload.
func TestWaitChainHaltedReturnsThreeAndTheReason(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainNoVerdictBody())

	timeout, interval := waitChainOpts()
	res, err := WaitChain(context.Background(), rt, "shop", timeout, interval, false)
	if err != nil {
		t.Fatalf("WaitChain: %v", err)
	}
	if res.Code != WaitNeedsYou {
		t.Errorf("code = %d, want %d for a halted chain", res.Code, WaitNeedsYou)
	}
	if !strings.Contains(res.Line, "chain shop: halted") {
		t.Errorf("line = %q, want the halted status", res.Line)
	}
	if !strings.Contains(res.Line, "no relevo block carries it") {
		t.Errorf("line = %q, want the halt reason", res.Line)
	}
	if !strings.Contains(res.Payload, "no relevo block carries it") {
		t.Errorf("payload = %q, want the halt reason", res.Payload)
	}
}

// TestWaitChainStoppedReturnsThree pins the stopped case: a chain a human
// stopped also waits on that human, so it is exit 3 too.
func TestWaitChainStoppedReturnsThree(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		row, err := tx.Chain("shop")
		if err != nil {
			return err
		}
		row.Status = string(chain.StatusStopped)
		return tx.ChainPut(row)
	}); err != nil {
		t.Fatalf("plant the stop: %v", err)
	}

	timeout, interval := waitChainOpts()
	res, err := WaitChain(context.Background(), rt, "shop", timeout, interval, true)
	if err != nil {
		t.Fatalf("WaitChain: %v", err)
	}
	if res.Code != WaitNeedsYou {
		t.Errorf("code = %d, want %d for a stopped chain", res.Code, WaitNeedsYou)
	}
	if !strings.Contains(res.Line, "chain shop: stopped") {
		t.Errorf("line = %q, want the stopped status", res.Line)
	}
}

// TestWaitChainPeekLeavesThePayloadPending pins --peek: the line is printed,
// the end delivery is not claimed and stays pending for the next reader.
func TestWaitChainPeekLeavesThePayloadPending(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	finishedChain(t, rt)

	timeout, interval := waitChainOpts()
	res, err := WaitChain(context.Background(), rt, "shop", timeout, interval, true)
	if err != nil {
		t.Fatalf("WaitChain: %v", err)
	}
	if res.Code != 0 || res.Payload != "" {
		t.Errorf("result = code %d payload %q, want 0 and no payload under --peek", res.Code, res.Payload)
	}
	if pending := chainPendingChain(t, rt, "shop"); len(pending) != 1 {
		t.Errorf("pending chain deliveries = %d, want the payload left pending", len(pending))
	}
}

// TestWaitChainPullsTheDeliveryOnlyOnce pins the one delivery: the first wait
// claims it and returns it; a second wait reports the same end with no payload.
func TestWaitChainPullsTheDeliveryOnlyOnce(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	finishedChain(t, rt)

	timeout, interval := waitChainOpts()
	first, err := WaitChain(context.Background(), rt, "shop", timeout, interval, false)
	if err != nil {
		t.Fatalf("first WaitChain: %v", err)
	}
	if first.Payload == "" {
		t.Fatal("first wait delivered no payload")
	}
	if pending := chainPendingChain(t, rt, "shop"); len(pending) != 0 {
		t.Errorf("pending chain deliveries = %d, want the payload claimed", len(pending))
	}

	second, err := WaitChain(context.Background(), rt, "shop", timeout, interval, false)
	if err != nil {
		t.Fatalf("second WaitChain: %v", err)
	}
	if second.Code != 0 {
		t.Errorf("second code = %d, want 0", second.Code)
	}
	if second.Payload != "" {
		t.Errorf("second payload = %q, want it already delivered", second.Payload)
	}
}

// TestWaitChainFindsTheEndDeliveryOnASurvivingMember pins the search order:
// the chain's one end delivery lives on the first member whose record exists,
// so a wait on a chain whose builder is gone still hands the payload over.
func TestWaitChainFindsTheEndDeliveryOnASurvivingMember(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	// The builder's record is gone, so the halt's end delivery landed on the
	// reviewer.
	if err := rt.Store.Delete("shop"); err != nil {
		t.Fatalf("Delete shop: %v", err)
	}
	tickChains(context.Background(), rt)
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusHalted) {
		t.Fatalf("chain status = %q, want halted", row.Status)
	}
	if pending := chainPendingChain(t, rt, "shop-rev"); len(pending) != 1 {
		t.Fatalf("pending on shop-rev = %d, want the one end delivery", len(pending))
	}

	timeout, interval := waitChainOpts()
	res, err := WaitChain(context.Background(), rt, "shop", timeout, interval, false)
	if err != nil {
		t.Fatalf("WaitChain: %v", err)
	}
	if res.Code != WaitNeedsYou {
		t.Errorf("code = %d, want %d for a halted chain", res.Code, WaitNeedsYou)
	}
	if !strings.Contains(res.Payload, "chain shop halted") {
		t.Errorf("payload = %q, want the chain's end delivery off the surviving member", res.Payload)
	}
	if res.DeliverErr != nil {
		t.Errorf("deliver error = %v, want none", res.DeliverErr)
	}
	if pending := chainPendingChain(t, rt, "shop-rev"); len(pending) != 0 {
		t.Errorf("pending on shop-rev = %d, want the wait to claim it", len(pending))
	}
}

// chainPending is one uncollected payload planted on a member: the kind it was
// written as, the member that holds it, and the text it carries.
type chainPending struct {
	kind    store.Kind
	member  string
	payload string
}

// chainHaltedWithPending halts the chain and leaves each of the named payloads
// on its member, so a wait on it has something to collect and the order it
// collects it in is the thing under test.
func chainHaltedWithPending(t *testing.T, rt Runtime, pending ...chainPending) {
	t.Helper()
	startedChain(t, rt, ChainOptions{})
	for _, p := range pending {
		if err := rt.Store.AppendLog(p.member, store.LogEntry{
			TS: baseTime, Round: 1, Direction: store.DirToMasterMind,
			Kind: p.kind, Payload: p.payload,
		}); err != nil {
			t.Fatalf("plant the %s on %s: %v", p.kind, p.member, err)
		}
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		row, err := tx.Chain("shop")
		if err != nil {
			return err
		}
		row.Status = string(chain.StatusHalted)
		row.Reason = "reviewer still wants changes"
		return tx.ChainPut(row)
	}); err != nil {
		t.Fatalf("halt the chain: %v", err)
	}
}

// TestWaitChainReturnsTheHaltWhereverTheWalkFindsIt pins the precedence: a
// halt leads the chain's end payload whatever member holds it, and the
// ordinary payload behind it is delivered rather than dropped. A halt planted
// on the SECOND member is the case member order gets wrong -- the walk reaches
// it only because it walks every member -- so this test fails if the walk
// returns at the first member holding anything.
func TestWaitChainReturnsTheHaltWhereverTheWalkFindsIt(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		haltOn   string
		haltText string
		reportOn string
		repText  string
	}{
		{
			name:     "halt on the later member",
			haltOn:   "shop-rev",
			haltText: "REVIEWER HALT TEXT",
			reportOn: "shop",
			repText:  "BUILDER REPORT TEXT",
		},
		{
			name:     "halt on the earlier member",
			haltOn:   "shop",
			haltText: "BUILDER HALT TEXT",
			reportOn: "shop-rev",
			repText:  "REVIEWER REPORT TEXT",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rt, _ := chainRuntime(t)
			chainHaltedWithPending(t, rt,
				chainPending{store.KindReport, tc.reportOn, tc.repText},
				chainPending{store.KindHalt, tc.haltOn, tc.haltText})

			timeout, interval := waitChainOpts()
			res, err := WaitChain(context.Background(), rt, "shop", timeout, interval, false)
			if err != nil {
				t.Fatalf("WaitChain: %v", err)
			}
			if res.Code != WaitNeedsYou {
				t.Errorf("code = %d, want %d for a halted chain", res.Code, WaitNeedsYou)
			}
			haltAt := strings.Index(res.Payload, tc.haltText)
			reportAt := strings.Index(res.Payload, tc.repText)
			if haltAt < 0 || reportAt < 0 {
				t.Fatalf("payload = %q, want both the halt and the report", res.Payload)
			}
			if haltAt > reportAt {
				t.Errorf("payload puts the halt at %d and the report at %d, want the halt first: %q",
					haltAt, reportAt, res.Payload)
			}
			if res.DeliverErr != nil {
				t.Errorf("deliver error = %v, want none", res.DeliverErr)
			}
		})
	}
}

// TestWaitChainCollectsEveryMemberWithNothingPending pins the other half of the
// walk: a chain with no halt anywhere still answers with every member's
// uncollected payload, not only the first member's. It is what keeps the halt
// precedence from costing the payload path its entries.
func TestWaitChainCollectsEveryMemberWithNothingPending(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	chainHaltedWithPending(t, rt,
		chainPending{store.KindReport, "shop", "BUILDER REPORT TEXT"},
		chainPending{store.KindReport, "shop-plan", "PLANNER REPORT TEXT"})

	timeout, interval := waitChainOpts()
	res, err := WaitChain(context.Background(), rt, "shop", timeout, interval, false)
	if err != nil {
		t.Fatalf("WaitChain: %v", err)
	}
	for _, want := range []string{"BUILDER REPORT TEXT", "PLANNER REPORT TEXT"} {
		if !strings.Contains(res.Payload, want) {
			t.Errorf("payload = %q, want it to carry %q", res.Payload, want)
		}
	}
	for _, member := range []string{"shop", "shop-plan"} {
		for _, e := range chainLog(t, rt, member) {
			if e.Kind == store.KindReport && !e.Confirmed {
				t.Errorf("member %s still holds an uncollected report: %+v", member, e)
			}
		}
	}
}

// TestWaitChainRunningAlwaysWaitsOnTheChain pins the running case: a running
// chain waits on the chain even with the builder's round open, because the
// chain's end is the one event the caller asked about.
func TestWaitChainRunningAlwaysWaitsOnTheChain(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})
	// The builder's first round is open, and the chain is running.
	b := chainBinding(t, rt, "shop")
	if !HasPromptEntry(chainLog(t, rt, "shop"), b.Round) {
		t.Fatal("test premise: the builder's round must be open")
	}

	binding, isChain, err := ChainWaitTarget(rt, "shop")
	if err != nil {
		t.Fatalf("ChainWaitTarget: %v", err)
	}
	if !isChain || binding != "shop" {
		t.Errorf("ChainWaitTarget(running) = (%q, chain %v), want the chain arm", binding, isChain)
	}
}

// TestWaitChainHaltedWithAnOpenBuilderRoundWaitsOnTheRound pins gap 4: a chain
// a human halted, whose builder then took a manual round, waits on that round
// rather than on the chain's old halt line.
func TestWaitChainHaltedWithAnOpenBuilderRoundWaitsOnTheRound(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	// The chain halts on the reviewer's missing verdict.
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainNoVerdictBody())
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusHalted) {
		t.Fatalf("chain status = %q, want halted", row.Status)
	}

	// A human's manual round goes to the builder while the chain is halted.
	if _, err := Send(context.Background(), rt, "shop", writePlan(t, "carry on"), SendOptions{}); err != nil {
		t.Fatalf("Send after the halt: %v", err)
	}
	b := chainBinding(t, rt, "shop")
	if !HasPromptEntry(chainLog(t, rt, "shop"), b.Round) {
		t.Fatal("test premise: the manual builder round must be open")
	}

	binding, isChain, err := ChainWaitTarget(rt, "shop")
	if err != nil {
		t.Fatalf("ChainWaitTarget: %v", err)
	}
	if isChain {
		t.Error("ChainWaitTarget chose the chain arm; the open builder round must win")
	}
	if binding != "shop" {
		t.Errorf("binding = %q, want the builder %q", binding, "shop")
	}
}

// TestWaitChainHaltedWithNoOpenRoundReturnsTheHalt pins the unchanged case: a
// halted chain with no builder round open still waits on the chain's halt.
func TestWaitChainHaltedWithNoOpenRoundReturnsTheHalt(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainNoVerdictBody())
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusHalted) {
		t.Fatalf("chain status = %q, want halted", row.Status)
	}

	binding, isChain, err := ChainWaitTarget(rt, "shop")
	if err != nil {
		t.Fatalf("ChainWaitTarget: %v", err)
	}
	if !isChain || binding != "shop" {
		t.Errorf("ChainWaitTarget(halted, no open round) = (%q, chain %v), want the chain arm", binding, isChain)
	}
}

// TestWaitTargetOnABindingIsTheBinding pins the non-chain name: a plain
// binding, and a missing name, both take the ordinary binding path.
func TestWaitTargetOnABindingIsTheBinding(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	for _, name := range []string{"plain", "missing"} {
		binding, isChain, err := ChainWaitTarget(rt, name)
		if err != nil {
			t.Fatalf("ChainWaitTarget(%s): %v", name, err)
		}
		if isChain || binding != name {
			t.Errorf("ChainWaitTarget(%s) = (%q, chain %v), want the binding path", name, binding, isChain)
		}
	}
}

// TestWaitChainUnknownNameIsNotFound pins the missing name: a chain the store
// does not hold is store.ErrNotFound, which the CLI reports as binding_not_found.
func TestWaitChainUnknownNameIsNotFound(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)

	timeout, interval := waitChainOpts()
	_, err := WaitChain(context.Background(), rt, "nope", timeout, interval, false)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("WaitChain(nope) = %v, want store.ErrNotFound", err)
	}
}

// TestWaitChainTimeoutIsTheWaitCode pins the one exit a running chain can
// still produce: the poll gives up on the caller's timeout and reports the
// wait protocol's own code, never a status the chain does not hold.
func TestWaitChainTimeoutIsTheWaitCode(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})
	// The chain stays running, so the poll can only ever time out. A real
	// clock (the fixture's is frozen) makes the elapsed check advance.
	rt.Now = time.Now

	res, err := WaitChain(context.Background(), rt, "shop", time.Nanosecond, time.Millisecond, false)
	if err != nil {
		t.Fatalf("WaitChain: %v", err)
	}
	if res.Code != WaitTimeout || !res.Done {
		t.Errorf("result = code %d done %v, want %d and done", res.Code, res.Done, WaitTimeout)
	}
	if res.Payload != "" {
		t.Errorf("payload = %q, want nothing on a timeout", res.Payload)
	}
}
