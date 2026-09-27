package ui

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// liveSeedToken and liveNewToken are the canonical refs of the two candidates
// the live tests store; the names are what the store derives for them.
const (
	liveSeedToken = "opencode/cline-pass/cline-pass/deepseek-v4.1-flash#high"
	liveNewToken  = "opencode/openrouter/z-ai/glm-5.3-flash"
)

var liveNewInput = relevo.CandidateInput{Harness: "opencode", Provider: "openrouter", Model: "z-ai/glm-5.3-flash"}

// liveStore opens a real database and config store holding one candidate.
func liveStore(t *testing.T) (*config.Store, *db.DB) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	st := config.Open(d)
	addStoredCandidate(t, st, relevo.CandidateInput{Harness: "opencode", Provider: "cline-pass", Model: "cline-pass/deepseek-v4.1-flash#high"})
	return st, d
}

// addStoredCandidate appends one candidate to st, as a write outside the
// cockpit would.
func addStoredCandidate(t *testing.T, st *config.Store, in relevo.CandidateInput) {
	t.Helper()
	doc, err := relevo.LoadConfigDoc(st)
	if err != nil {
		t.Fatalf("LoadConfigDoc: %v", err)
	}
	e, err := relevo.AddCandidate(doc, in)
	if err != nil {
		t.Fatalf("AddCandidate(%+v): %v", in, err)
	}
	if err := relevo.WriteConfigEdit(st, e); err != nil {
		t.Fatalf("WriteConfigEdit: %v", err)
	}
}

// liveRuntimeIn loads st's sections into a fresh runtime.
func liveRuntimeIn(t *testing.T, st *config.Store, d *db.DB) relevo.Runtime {
	t.Helper()
	rt, err := relevo.ReloadConfig(relevo.Runtime{Store: store.New(t.TempDir()), Config: st, DB: d})
	if err != nil {
		t.Fatalf("ReloadConfig: %v", err)
	}
	return rt
}

func TestLiveSourceStatusPicksUpAnExternalConfigWrite(t *testing.T) {
	ctx := context.Background()
	st, d := liveStore(t)
	src := liveSource{newLiveRuntime(liveRuntimeIn(t, st, d))}

	if _, ok := src.Base().Candidates.NameFor(liveNewToken); ok {
		t.Fatal("the new candidate is visible before the store changed")
	}

	addStoredCandidate(t, st, liveNewInput)

	if _, err := src.Status(ctx); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got := src.Base().Candidates.NameOf(liveNewToken); got != "glm-5.3-flash" {
		t.Errorf("after Status, NameOf(%q) = %q, want glm-5.3-flash", liveNewToken, got)
	}
	rowRt, name, ok := src.Runtime("x")
	if !ok || name != "x" {
		t.Fatalf("Runtime(x) = %q, %v, want the key and true", name, ok)
	}
	if got := rowRt.Candidates.NameOf(liveNewToken); got != "glm-5.3-flash" {
		t.Errorf("Runtime(x) set: NameOf(%q) = %q, want glm-5.3-flash", liveNewToken, got)
	}
}

func TestApplyConfigIsSeenThroughTheSource(t *testing.T) {
	ctx := context.Background()
	st, d := liveStore(t)
	live := newLiveRuntime(liveRuntimeIn(t, st, d))
	a := &mastermindActions{live: live}
	src := liveSource{live}

	doc, err := relevo.LoadConfigDoc(st)
	if err != nil {
		t.Fatalf("LoadConfigDoc: %v", err)
	}
	e, err := relevo.AddCandidate(doc, liveNewInput)
	if err != nil {
		t.Fatalf("AddCandidate: %v", err)
	}

	if res := a.ApplyConfig(ctx, e); res.Err != nil {
		t.Fatalf("ApplyConfig: %v (text %q)", res.Err, res.Text)
	}
	if _, ok := src.Base().Candidates.NameFor(liveNewToken); !ok {
		t.Fatal("ApplyConfig's candidate is not visible through the source without a Status call")
	}
}

func TestRollbackIsSeenThroughTheSource(t *testing.T) {
	ctx := context.Background()
	st, d := liveStore(t)
	addStoredCandidate(t, st, liveNewInput)
	live := newLiveRuntime(liveRuntimeIn(t, st, d))
	a := &mastermindActions{live: live}
	src := liveSource{live}

	if _, ok := src.Base().Candidates.NameFor(liveNewToken); !ok {
		t.Fatal("setup: the second revision's candidate is missing")
	}

	if res := a.Rollback(ctx, 1); res.Err != nil {
		t.Fatalf("Rollback: %v (text %q)", res.Err, res.Text)
	}
	if _, ok := src.Base().Candidates.NameFor(liveNewToken); ok {
		t.Fatal("the rolled-back revision is not visible through the source")
	}
	if _, ok := src.Base().Candidates.NameFor(liveSeedToken); !ok {
		t.Fatal("Rollback dropped the first revision's candidate")
	}
}

func TestRefreshNeverGoesBackwards(t *testing.T) {
	st, d := liveStore(t)
	live := newLiveRuntime(liveRuntimeIn(t, st, d))
	if err := live.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	live.version = 99
	addStoredCandidate(t, st, liveNewInput)
	if err := live.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, ok := live.Get().Candidates.NameFor(liveNewToken); ok {
		t.Fatal("Refresh installed a snapshot whose version was not greater than the stored one")
	}
}

func TestRefreshWithoutAConfigStoreIsANoOp(t *testing.T) {
	live := newLiveRuntime(relevo.Runtime{})
	if err := live.Refresh(); err != nil {
		t.Fatalf("Refresh with no config store: %v", err)
	}
	if got := live.Get(); !reflect.DeepEqual(got, relevo.Runtime{}) {
		t.Fatalf("Get = %+v, want the runtime unchanged", got)
	}
}
