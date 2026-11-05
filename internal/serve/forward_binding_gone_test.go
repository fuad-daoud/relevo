package serve

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/burst"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// bindingGoneEnv is a head whose one node answers 404 to everything: the
// state a release's unbind fan-out leaves behind. The forward is left in
// phase.
func bindingGoneEnv(t *testing.T, phase string, since time.Time) (*testEnv, relevo.Runtime, *countingHooks) {
	t.Helper()
	headKP, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	pem, err := remote.MarshalPrivate(headKP)
	if err != nil {
		t.Fatalf("marshal head client key: %v", err)
	}
	hookEvents := &countingHooks{}
	env := setupTestEnv(t, func(cfg *Config) {
		cfg.Burst = burstTestPolicy()
		cfg.ClientKey = pem
		cfg.Provider = &recordingBurstProvider{}
		cfg.MaxBuilders = 1
		cfg.Hooks = hookEvents
	})
	url, fp := startStubNode(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, remote.ErrorBody{Code: remote.CodeNotFound, Message: "not found"})
	}))
	env.srv.burstTable = &burst.NodeTable{Nodes: []burst.NodeRow{stubRow(t, "b1", url, fp)}}
	saveHeadForward(t, env, "api", "b1", "nb-api", 1, since)
	rt := env.runtime(t)
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	b.Burst.Forward.Phase = phase
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	return env, rt, hookEvents
}

// A closed forward goes through observe's first call, not straight to the
// ack: the node has no binding left to answer either with, and the forward
// must not wait for one forever.
func TestObserveClosedForwardClearsWhenNodeBindingGone(t *testing.T) {
	env, rt, hookEvents := bindingGoneEnv(t, "closed", time.Now().UTC().Add(-time.Minute))
	for range 4 {
		env.srv.observeForwards(context.Background())
	}
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if f := burstStoredForward(b); f != nil {
		t.Errorf("forward = %+v, want it cleared: nothing on the node can ack it", f)
	}
	if !b.QueuedAt.IsZero() {
		t.Errorf("QueuedAt = %v, want zero: a closed round is home and never reruns", b.QueuedAt)
	}
	if got := hookEvents.count(hooks.EventBurstAckStuck); got != 0 {
		t.Errorf("ack stuck hooks = %d, want 0", got)
	}
}

func TestObserveRunningForwardRequeuesWhenNodeBindingGone(t *testing.T) {
	since := time.Now().UTC().Add(-time.Minute)
	env, rt, _ := bindingGoneEnv(t, "running", since)
	env.srv.observeForwards(context.Background())
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if f := burstStoredForward(b); f != nil {
		t.Errorf("forward = %+v, want it cleared", f)
	}
	if !b.QueuedAt.Equal(since) {
		t.Errorf("QueuedAt = %v, want %v: the round keeps its FIFO place", b.QueuedAt, since)
	}
}

func TestObserveRunningForwardRequeuesWhenNodeSitsIdle(t *testing.T) {
	since := time.Now().UTC().Add(-time.Minute)
	script := &nodeScript{getStatus: http.StatusOK, getView: stubRoundView("nb-api", 1, remote.RoundIdle)}
	env, _ := gateFanoutEnv(t, []*nodeScript{script}, &since)
	saveHeadForward(t, env, "api", "b1", "nb-api", 2, since)
	rt := env.runtime(t)
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	b.Burst.Forward.Phase = "running"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	env.srv.observeForwards(context.Background())
	b, err = rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if f := burstStoredForward(b); f != nil {
		t.Errorf("forward = %+v, want it cleared: the node has no such round", f)
	}
	if !b.QueuedAt.Equal(since) {
		t.Errorf("QueuedAt = %v, want %v", b.QueuedAt, since)
	}
}

func TestObserveStopRequestedForwardClosesWhenNodeBindingGone(t *testing.T) {
	env, rt, _ := bindingGoneEnv(t, "running", time.Now().UTC().Add(-time.Minute))
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	b.Burst.Forward.StopRequested = true
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	env.srv.observeForwards(context.Background())
	b, err = rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if f := burstStoredForward(b); f != nil {
		t.Errorf("forward = %+v, want it cleared", f)
	}
	if !b.QueuedAt.IsZero() {
		t.Errorf("QueuedAt = %v, want zero: a stopped round never runs again", b.QueuedAt)
	}
	entries, err := rt.Store.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	stops := 0
	for _, e := range entries {
		if e.Kind == store.KindStop && strings.HasPrefix(e.Note, "stopped/") {
			stops++
		}
	}
	if stops != 1 {
		t.Errorf("stop entries = %d, want 1 in %+v", stops, entries)
	}
}

// A released binding whose forward was still live ends here: it must neither
// count against the node nor go back in the queue.
func TestObserveDoneBindingDropsForwardWhenNodeBindingGone(t *testing.T) {
	env, rt, _ := bindingGoneEnv(t, "running", time.Now().UTC().Add(-time.Minute))
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	b.State = store.StateDone
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	env.srv.observeForwards(context.Background())
	b, err = rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if f := burstStoredForward(b); f != nil {
		t.Errorf("forward = %+v, want it cleared", f)
	}
	if !b.QueuedAt.IsZero() {
		t.Errorf("QueuedAt = %v, want zero: a released binding takes no new round", b.QueuedAt)
	}
	if b.State != store.StateDone {
		t.Errorf("State = %v, want it still done", b.State)
	}
}

func TestStopMarksLastAsLongAsTheStopIsOpen(t *testing.T) {
	clock := time.Now().UTC()
	script := stoppedStub(500, "")
	env, _, _ := stopFanoutSetup(t, script, &clock)
	ctx := context.Background()
	if got := env.srv.sendForwards(ctx, clock); got != 1 {
		t.Fatalf("confirmed = %d, want 1", got)
	}
	rt := env.runtime(t)
	stampForward(t, rt, "api", "running")

	env.srv.observeForwards(ctx)
	env.srv.observeForwards(ctx)
	if got := script.stopHits.Load(); got != 1 {
		t.Fatalf("node stop hits = %d, want 1: the open episode keeps its mark", got)
	}
	h := env.srv.burstHealth
	h.noteStopDone("a mark no forward holds")
	h.noteStopSent("a mark no forward holds", clock)
	env.srv.observeForwards(ctx)
	if _, ok := h.stopDone["a mark no forward holds"]; ok {
		t.Error("stopDone kept a mark no forward holds")
	}
	if _, ok := h.stopLast["a mark no forward holds"]; ok {
		t.Error("stopLast kept a mark no forward holds")
	}
}

// The verb's process exits right after this returns, so returning any sooner
// ends the sends it started.
func TestAdminWaitFanoutHoldsUntilTheNodeAnswers(t *testing.T) {
	clock := time.Now().UTC()
	release := make(chan struct{})
	script := &nodeScript{unavailableWait: release}
	env, _ := gateFanoutEnv(t, []*nodeScript{script}, &clock)
	if _, err := AdminUnavailable(env.srv, "claude/anthropic/haiku", time.Time{}, "rate limited test"); err != nil {
		close(release)
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		AdminWaitFanout(env.srv)
		close(done)
	}()
	select {
	case <-done:
		close(release)
		t.Fatal("returned while the node still held its answer")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("never returned after the node answered")
	}
}

// A node send started just before shutdown still lands.
func TestDrainWaitsForFanouts(t *testing.T) {
	clock := time.Now().UTC()
	env, _ := gateFanoutEnv(t, []*nodeScript{{}}, &clock)
	runDone := make(chan struct{})
	close(runDone)
	release := make(chan struct{})
	env.srv.fanoutDetached(nil, func() error {
		<-release
		return nil
	})
	done := make(chan struct{})
	go func() {
		env.srv.drainServer(&http.Server{}, runDone)
		close(done)
	}()
	select {
	case <-done:
		close(release)
		t.Fatal("drained while a fan-out was still in flight")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("never drained after the fan-out landed")
	}
}
