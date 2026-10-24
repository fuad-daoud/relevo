package relevo

import (
	"context"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

// TestPrepareProcServedOptOutStartsWithoutTrimpath pins the served round: a
// stored opt-out starts without -trimpath, even though the served runtime has
// no config and reads the repository default (on) for everything else.
func TestPrepareProcServedOptOutStartsWithoutTrimpath(t *testing.T) {
	t.Setenv("GOFLAGS", "")
	rt := newRuntime(t)
	tree := goTree(t)
	b := store.Binding{Name: "webshop", Round: 1, Serve: &store.ServeFacts{Trimpath: "off"}}
	req := roundReq(&b)
	req.tree = tree

	_, env, _, err := prepareProc(context.Background(), rt, req)
	if err != nil {
		t.Fatalf("prepareProc: %v", err)
	}
	if env != nil {
		t.Errorf("env = %v, want none for a stored opt-out", env)
	}
}

// TestPrepareProcServedWithoutOptOutStartsTrimmed pins the served default: a
// stored binding with no trimpath value starts with -trimpath, as today.
func TestPrepareProcServedWithoutOptOutStartsTrimmed(t *testing.T) {
	t.Setenv("GOFLAGS", "")
	rt := newRuntime(t)
	tree := goTree(t)
	b := store.Binding{Name: "webshop", Round: 1, Serve: &store.ServeFacts{}}
	req := roundReq(&b)
	req.tree = tree

	_, env, _, err := prepareProc(context.Background(), rt, req)
	if err != nil {
		t.Fatalf("prepareProc: %v", err)
	}
	if len(env) != 1 || env[0] != "GOFLAGS=-trimpath" {
		t.Errorf("env = %v, want GOFLAGS=-trimpath", env)
	}
}

// TestPrepareProcStoredOnStaysTrimmed pins that an explicitly stored "on"
// reads trimmed: only the stored "off" opts out, so flipping the stored value
// back to "" or "on" fails this suite's opt-out test and vice versa.
func TestPrepareProcStoredOnStaysTrimmed(t *testing.T) {
	t.Setenv("GOFLAGS", "")
	rt := newRuntime(t)
	tree := goTree(t)
	b := store.Binding{Name: "webshop", Round: 1, Serve: &store.ServeFacts{Trimpath: "on"}}
	req := roundReq(&b)
	req.tree = tree

	_, env, _, err := prepareProc(context.Background(), rt, req)
	if err != nil {
		t.Fatalf("prepareProc: %v", err)
	}
	if len(env) != 1 || env[0] != "GOFLAGS=-trimpath" {
		t.Errorf("env = %v, want GOFLAGS=-trimpath for a stored on", env)
	}
}
