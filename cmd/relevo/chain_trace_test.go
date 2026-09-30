package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// seedChainTrace writes one chain and its trace into the default state root:
// a builder close that seeded the reviewer, and the reviewer's pass that ended
// the chain. Store-only -- no harness and no network.
func seedChainTrace(t *testing.T, name string) {
	t.Helper()

	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	s := store.New(root)
	if err := s.Save(store.Binding{Name: name, CWD: t.TempDir(), Round: 1, State: store.StateActive}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	now := time.Now().UTC()
	c := db.ChainRow{
		ID: db.NewID(), Name: name, Status: "done", Phase: "finished", Step: "reviewing",
		Plan: 1, Plans: 1, PlanPathsJSON: []byte(`["/p/plan-1.md"]`), SettingsJSON: []byte(`{}`),
		Builder: name, Reviewer: name + "-rev", CreatedAt: now, UpdatedAt: now,
	}
	rows := []db.ChainEventRow{
		{
			Phase: "build", Step: "building", Member: name, Round: 1,
			Event:  chain.Event{Kind: chain.EventBuilderClosed, Member: chain.MemberBuilder, Round: 1, Outcome: "done", Gate: chain.GateGreen}.Encode(),
			Action: chain.Action{Kind: chain.ActionSend, Member: chain.MemberReviewer, Seed: chain.SeedReviewer}.Encode(),
		},
		{
			Phase: "build", Step: "reviewing", Member: name + "-rev", Round: 1,
			Event:  chain.Event{Kind: chain.EventReviewerClosed, Member: chain.MemberReviewer, Round: 1, Verdict: chain.VerdictPass}.Encode(),
			Action: chain.Action{Kind: chain.ActionFinish}.Encode(),
		},
	}
	if err := s.WithLock(func(tx *store.Tx) error {
		if err := tx.ChainPut(c); err != nil {
			return err
		}
		for _, r := range rows {
			if err := tx.ChainEventAppend(name, r); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed chain: %v", err)
	}
}

// TestShowTraceJSONIsOneDocument pins `show <n> --trace --json`: stdout is one
// ChainTraceDoc document -- the chain's own state and its events -- and
// nothing after it.
func TestShowTraceJSONIsOneDocument(t *testing.T) {
	const name = "showtracejson"
	seedChainTrace(t, name)

	stdout, _, err := captureOutput(t, func() error {
		return run([]string{"show", name, "--trace", "--json"})
	})
	if err != nil {
		t.Fatalf("show --trace --json: %v", err)
	}

	dec := json.NewDecoder(bytes.NewReader(stdout))
	var doc relevo.ChainTraceDoc
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("decode trace document: %v\n%s", err, stdout)
	}
	if dec.More() {
		t.Errorf("stdout carries more than one document:\n%s", stdout)
	}
	if doc.Name != name || doc.Status != "done" {
		t.Errorf("document = name %q status %q, want %q/done", doc.Name, doc.Status, name)
	}
	if len(doc.Events) != 2 {
		t.Fatalf("document carries %d events, want 2", len(doc.Events))
	}
	if doc.Events[0].Event.Kind != chain.EventBuilderClosed || doc.Events[1].Event.Kind != chain.EventReviewerClosed {
		t.Errorf("events = %+v, want the builder close then the reviewer close", doc.Events)
	}
	if doc.Events[0].Member != name || doc.Events[0].Round != 1 {
		t.Errorf("first event = %s r%d, want the builder's round 1", doc.Events[0].Member, doc.Events[0].Round)
	}
}

// TestShowTraceOnANonChainRefuses pins the refusal: a name the store holds no
// chain for is a usage error, not an empty trace.
func TestShowTraceOnANonChainRefuses(t *testing.T) {
	const name = "showtraceplain"
	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	if err := store.New(root).Save(store.Binding{Name: name, CWD: t.TempDir(), Round: 1, State: store.StateActive}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	_, _, runErr := captureOutput(t, func() error {
		return run([]string{"show", name, "--trace"})
	})
	var ec exitCodeErr
	if !errors.As(runErr, &ec) || ec.code != 2 {
		t.Fatalf("show --trace on a binding = %v, want exit 2", runErr)
	}
	if !errors.Is(runErr, relevo.ErrNotAChain) {
		t.Errorf("error = %v, want it to wrap relevo.ErrNotAChain", runErr)
	}
	if !strings.Contains(runErr.Error(), "not a chain") {
		t.Errorf("error = %q, want the refusal to say so", runErr.Error())
	}
}
