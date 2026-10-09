package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/serve"
	"github.com/fuad-daoud/relevo/internal/store"
	usagepkg "github.com/fuad-daoud/relevo/internal/usage"
)

// ownerTabEntry is the one report entry the --owner tests seed: a usage-bearing
// round, so `history --tab --owner` has a row to sum.
func ownerTabEntry() store.LogEntry {
	at := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	return store.LogEntry{
		TS:        at,
		Round:     1,
		Direction: store.DirToMasterMind,
		Kind:      store.KindReport,
		Confirmed: true,
		Usage: &usagepkg.Usage{
			Harness: "opencode", Provider: "anthropic", Model: "claude-sonnet-5",
			Tokens:  usagepkg.Tokens{In: 1000, CacheRead: 3000, Out: 100},
			Cost:    usagepkg.Cost{USD: 0.50, Basis: usagepkg.Measured},
			Samples: 1,
		},
	}
}

// seedOwnerStore gives s one live binding "api" with one completed round: a
// plan entry, a report entry and the report file. It is the seed the server's
// per-owner reads see.
func seedOwnerStore(t *testing.T, s *store.Store, owner string) {
	t.Helper()
	if err := s.Save(store.Binding{
		Name: "api", Owner: owner, CWD: t.TempDir(), Round: 2, State: store.StateActive,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := s.AppendLog("api", store.LogEntry{
		TS: time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC), Round: 1,
		Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true,
	}); err != nil {
		t.Fatalf("AppendLog plan: %v", err)
	}
	if err := s.AppendLog("api", ownerTabEntry()); err != nil {
		t.Fatalf("AppendLog report: %v", err)
	}
	if err := os.MkdirAll(s.OutDir("api"), 0o755); err != nil {
		t.Fatalf("mkdir out: %v", err)
	}
	if err := os.WriteFile(s.ReportPath("api", 1), []byte("# report body\n"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
}

// seedServeOwnerState points XDG_STATE_HOME at a fresh root and seeds the
// machine database with one enrolled client label and a live binding "api"
// under that owner. It returns the owner's store -- the one
// AdminOwnerRuntime resolves -- and the state root, so a test can seed the
// client store beside it when it needs a reference. Store-only: no harness,
// no listener, no network.
func seedServeOwnerState(t *testing.T, label string) (*store.Store, string) {
	t.Helper()

	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	serveRoot := filepath.Join(root, "serve")
	d, err := openDB(filepath.Join(root, "relevo.db"))
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	// The enrolled clients are machine-local, so the fixture reads the file the
	// serve verbs enroll into.
	clients, err := serve.LoadClients(d.LocalOrSelf())
	if err != nil {
		t.Fatalf("LoadClients: %v", err)
	}
	kp, err := remote.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	cl, err := clients.Add(label, remote.MarshalPublic(kp.Public, label), "", time.Now())
	if err != nil {
		t.Fatalf("clients.Add: %v", err)
	}
	dir, ok := cl.ID.Dir()
	if !ok {
		t.Fatalf("client id %q has no owner dir", cl.ID)
	}

	owner := store.NewShared(filepath.Join(serveRoot, "bindings", dir), string(cl.ID), d)
	seedOwnerStore(t, owner, string(cl.ID))
	return owner, root
}

// TestShowOwnerIsTheOldServeShow pins §4.1: `show <name> --owner <label>`
// prints the round the removed `serve show` printed, header prefixed with the
// owner's label, and -- being a read -- stamps nothing.
func TestShowOwnerIsTheOldServeShow(t *testing.T) {
	owner, _ := seedServeOwnerState(t, "alice")

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"show", "api", "--owner", "alice", "--report"})
	})
	if err != nil {
		t.Fatalf("show api --owner alice --report: %v", err)
	}
	if string(stdout) != "# report body\n" {
		t.Errorf("stdout = %q, want the report body", stdout)
	}
	if !strings.Contains(string(stderr), "alice/api round 1 of 1 · report") {
		t.Errorf("stderr = %q, want the owner-prefixed header", stderr)
	}
	if _, ok := owner.ViewedAt("api"); ok {
		t.Error("show --owner must not stamp .viewed: the stamp is the owner's")
	}
}

// TestShowOwnerLogIsTheOldServeLog pins §4.1's --log route: `show <name>
// --owner <label> --log` prints every log line the removed `serve log`
// printed, unreviewed.
func TestShowOwnerLogIsTheOldServeLog(t *testing.T) {
	owner, _ := seedServeOwnerState(t, "alice")

	log, err := owner.ReadLog("api")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var want strings.Builder
	for _, e := range log {
		want.WriteString(relevo.LogLine(e))
		want.WriteString("\n")
	}

	stdout, _, err := captureOutput(t, func() error {
		return run([]string{"show", "api", "--owner", "alice", "--log"})
	})
	if err != nil {
		t.Fatalf("show api --owner alice --log: %v", err)
	}
	if string(stdout) != want.String() {
		t.Errorf("show --owner --log =\n%q\nwant\n%q", stdout, want.String())
	}
	if _, ok := owner.ViewedAt("api"); ok {
		t.Error("show --owner --log must not stamp .viewed")
	}
}

// TestShowOwnerLeavesPendingMasterMindPayload pins #673's owner carve-out: the
// admin's read of another owner's binding stays read-only, so a queued
// mastermind-bound report on that binding is still pending after `show
// --owner`. Store-only: no harness, no listener, no network.
func TestShowOwnerLeavesPendingMasterMindPayload(t *testing.T) {
	owner, _ := seedServeOwnerState(t, "alice")

	if err := owner.AppendLog("api", store.LogEntry{
		TS: time.Date(2026, 9, 10, 10, 0, 1, 0, time.UTC), Round: 1,
		Direction: store.DirToMasterMind, Kind: store.KindReport,
		Payload: "round 1 report", Path: owner.ReportPath("api", 1),
	}); err != nil {
		t.Fatalf("AppendLog pending: %v", err)
	}

	stdout, _, err := captureOutput(t, func() error {
		return run([]string{"show", "api", "--owner", "alice", "--report"})
	})
	if err != nil {
		t.Fatalf("show api --owner alice --report: %v", err)
	}
	if string(stdout) != "# report body\n" {
		t.Errorf("stdout = %q, want the report body", stdout)
	}

	entries, err := owner.ReadLog("api")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	for _, e := range entries {
		if e.Direction == store.DirToMasterMind && !e.Confirmed {
			return // still pending: the owner read claimed nothing
		}
	}
	t.Error("show --owner confirmed the pending payload; the owner read must stay read-only")
}
