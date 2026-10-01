package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// seedWaitChain seeds the default state root with one finished chain whose end
// delivery is still pending on its builder, so `relevo wait` has something to
// hand over. Store-only -- no harness and no network.
func seedWaitChain(t *testing.T, name string) {
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
	if err := s.WithLock(func(tx *store.Tx) error { return tx.ChainPut(c) }); err != nil {
		t.Fatalf("ChainPut: %v", err)
	}
	for _, e := range []store.LogEntry{
		{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true},
		{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: "/x/001-report.md", Confirmed: true},
		{
			Round: 1, Direction: store.DirToMasterMind, Kind: store.KindChain,
			Payload: "chain " + name + " finished: status done, phase finished, plan 1/1, corrections 0, findings 0.",
		},
	} {
		if err := s.AppendLog(name, e); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}
}

// TestWaitOnAChainReturnsItsEnd pins wait's chain arm: the name resolves to a
// chain, the wait reports the chain's end and delivers its one end payload,
// and --json prints the same run as one WaitDoc.
func TestWaitOnAChainReturnsItsEnd(t *testing.T) {
	const name = "waitchain"
	seedWaitChain(t, name)

	stdout, _, err := captureOutput(t, func() error { return run([]string{"wait", name}) })
	if err != nil {
		t.Fatalf("wait %s: %v", name, err)
	}
	if !strings.Contains(string(stdout), "chain "+name+": done") {
		t.Errorf("wait stdout = %q, want the chain's end line", stdout)
	}
	if !strings.Contains(string(stdout), "status done") {
		t.Errorf("wait stdout = %q, want the end payload", stdout)
	}

	// The delivery was claimed: the second wait prints the same end with no
	// payload, encoded as one WaitDoc.
	stdout, _, err = captureOutput(t, func() error { return run([]string{"wait", name, "--json"}) })
	if err != nil {
		t.Fatalf("wait --json: %v", err)
	}
	dec := json.NewDecoder(strings.NewReader(string(stdout)))
	var doc WaitDoc
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("decode WaitDoc: %v\n%s", err, stdout)
	}
	if dec.More() {
		t.Errorf("stdout carries more than one document:\n%s", stdout)
	}
	if doc.Name != name || doc.Code != 0 || !strings.Contains(doc.Line, "chain "+name+": done") {
		t.Errorf("document = %+v, want the chain's name, exit 0 and its end line", doc)
	}
	if doc.Payload != "" {
		t.Errorf("document payload = %q, want it already delivered", doc.Payload)
	}
}

// seedWaitChainOpenBuilderRound seeds the default state root with a halted
// chain whose builder then took a manual round that is still open: a prompt
// entry for the builder's current round and no report for it. Store-only.
func seedWaitChainOpenBuilderRound(t *testing.T, name string) {
	t.Helper()

	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	s := store.New(root)
	if err := s.Save(store.Binding{Name: name, CWD: t.TempDir(), Round: 2, State: store.StateActive}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	now := time.Now().UTC()
	c := db.ChainRow{
		ID: db.NewID(), Name: name, Status: "halted", Reason: "reviewer gave no verdict",
		Phase: "build", Step: "reviewing", Plan: 1, Plans: 1,
		PlanPathsJSON: []byte(`["/p/plan-1.md"]`), SettingsJSON: []byte(`{}`),
		Builder: name, Reviewer: name + "-rev", CreatedAt: now, UpdatedAt: now,
	}
	if err := s.WithLock(func(tx *store.Tx) error { return tx.ChainPut(c) }); err != nil {
		t.Fatalf("ChainPut: %v", err)
	}
	if err := s.AppendLog(name, store.LogEntry{Round: 2, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
}

// TestWaitOnAHaltedChainFollowsAnOpenBuilderRound pins gap 4: a human who sent
// a manual round to a halted chain's builder waits on that round, not on the
// chain's old halt. The round never closes, so the wait times out -- the point
// is that it took the binding path at all, printing no chain end line.
func TestWaitOnAHaltedChainFollowsAnOpenBuilderRound(t *testing.T) {
	const name = "waitgap"
	seedWaitChainOpenBuilderRound(t, name)

	stdout, _, err := captureOutput(t, func() error {
		return run([]string{"wait", name, "--json", "--timeout", "1ms"})
	})
	var ec exitCodeErr
	if !errors.As(err, &ec) || ec.code != relevo.WaitTimeout {
		t.Fatalf("wait exit = %v, want the wait timeout %d: it must follow the open builder round, not the chain's halt", err, relevo.WaitTimeout)
	}
	var doc WaitDoc
	if derr := json.Unmarshal(stdout, &doc); derr != nil {
		t.Fatalf("decode WaitDoc: %v\n%s", derr, stdout)
	}
	if doc.Code != relevo.WaitTimeout || doc.Line != "" {
		t.Errorf("document = %+v, want a binding-round timeout with no chain end line", doc)
	}
}

// TestWaitOnAHaltedChainWithNoOpenRoundReturnsTheHalt pins the unchanged
// branch of gap 4: with no builder round open, the name still waits on the
// chain and reports its halt.
func TestWaitOnAHaltedChainWithNoOpenRoundReturnsTheHalt(t *testing.T) {
	const name = "waitnohalt"

	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	s := store.New(root)
	if err := s.Save(store.Binding{Name: name, CWD: t.TempDir(), Round: 2, State: store.StateActive}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	now := time.Now().UTC()
	c := db.ChainRow{
		ID: db.NewID(), Name: name, Status: "halted", Reason: "reviewer gave no verdict",
		Phase: "build", Step: "reviewing", Plan: 1, Plans: 1,
		PlanPathsJSON: []byte(`["/p/plan-1.md"]`), SettingsJSON: []byte(`{}`),
		Builder: name, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.WithLock(func(tx *store.Tx) error { return tx.ChainPut(c) }); err != nil {
		t.Fatalf("ChainPut: %v", err)
	}

	stdout, _, err := captureOutput(t, func() error { return run([]string{"wait", name, "--json"}) })
	var ec exitCodeErr
	if !errors.As(err, &ec) || ec.code != relevo.WaitNeedsYou {
		t.Fatalf("wait exit = %v, want %d: a halted chain with no open round returns the halt", err, relevo.WaitNeedsYou)
	}
	var doc WaitDoc
	if derr := json.Unmarshal(stdout, &doc); derr != nil {
		t.Fatalf("decode WaitDoc: %v\n%s", derr, stdout)
	}
	if !strings.Contains(doc.Line, "chain "+name+": halted") {
		t.Errorf("document line = %q, want the chain's halt", doc.Line)
	}
}

// TestWaitOnABindingIsUnchanged pins the no-regression rule: a name that is no
// chain still waits on its binding's round and prints the report path, exactly
// as it did before wait learned about chains.
func TestWaitOnABindingIsUnchanged(t *testing.T) {
	const name = "waitplain"
	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	s := store.New(root)
	if err := s.Save(store.Binding{Name: name, CWD: t.TempDir(), Round: 2, State: store.StateActive}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	for _, e := range []store.LogEntry{
		{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true},
		{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: "/x/001-report.md", Confirmed: true},
	} {
		if err := s.AppendLog(name, e); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}

	stdout, _, err := captureOutput(t, func() error { return run([]string{"wait", name, "--peek"}) })
	if err != nil {
		t.Fatalf("wait %s: %v", name, err)
	}
	got := strings.TrimSpace(string(stdout))
	if got != "/x/001-report.md" {
		t.Errorf("wait stdout = %q, want the binding's report path", got)
	}
}
