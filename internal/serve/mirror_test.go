package serve

import (
	"context"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// headOwnerPath returns the head owner's store root for mirror keys.
func headOwnerPath(t *testing.T, env *testEnv) string {
	t.Helper()
	root, err := env.srv.ownerRoot(env.id)
	if err != nil {
		t.Fatalf("ownerRoot: %v", err)
	}
	return root
}

func saveMirrorBinding(t *testing.T, env *testEnv, name string) {
	t.Helper()
	rt, err := env.srv.runtime(env.id)
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	if err := rt.Store.Save(store.Binding{Name: name, CWD: "/repo", State: store.StateActive, Round: 1}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := rt.Store.AppendLog(name, store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
}

// TestMirrorPassKeepsEachOwnersMirror: what makes a later pass cheap lives in
// the mirror, so a pass that built a new one would ingest everything again.
func TestMirrorPassKeepsEachOwnersMirror(t *testing.T) {
	env := setupTestEnv(t)
	saveMirrorBinding(t, env, "api")
	mirrors := map[string]*relevo.OwnerMirror{}
	since := time.Now()

	env.srv.mirrorPass(context.Background(), mirrors, since)
	root := headOwnerPath(t, env)
	first := mirrors[root]
	if first == nil {
		t.Fatalf("mirrors = %v, want one for %s", mirrors, root)
	}
	if _, found, err := env.srv.cfg.DB.Binding("api"); err != nil || !found {
		t.Fatalf("mirror Binding: found=%v err=%v; want the pass to land it", found, err)
	}

	env.srv.mirrorPass(context.Background(), mirrors, since)
	if mirrors[root] != first {
		t.Error("second pass replaced the owner's mirror, want it kept")
	}
}

// TestRunMirrorsOwnersBesideItsTicks: Run starts the loop, with ticks that
// never reach the mirror, and waits for it before it returns.
func TestRunMirrorsOwnersBesideItsTicks(t *testing.T) {
	env := setupTestEnv(t)
	saveMirrorBinding(t, env, "api")
	env.srv.tickFn = func(context.Context) error { return nil }
	env.srv.mirrorEvery = time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- env.srv.Run(ctx) }()

	deadline := time.Now().Add(10 * time.Second)
	for {
		_, found, err := env.srv.cfg.DB.Binding("api")
		if err != nil {
			t.Fatalf("mirror Binding: %v", err)
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Run never mirrored the owner's binding")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}
