package ui

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/serve"
	"github.com/fuad-daoud/relevo/internal/store"
)

// serverTestDB opens a temp machine database for a serve.Server under test
// (P5 §4.3: serve.New takes the machine database from its caller now).
func serverTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("open test machine db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// TestMasterMindSourceResolvesEveryKey: a mastermind source answers every key
// with the one runtime and the key itself -- there is no branch on a
// mastermind.
func TestMasterMindSourceResolvesEveryKey(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	src := mastermindSource{rt: rt}

	if _, err := src.Status(context.Background()); err != nil {
		t.Fatalf("Status: %v", err)
	}
	for _, key := range []string{"api", "SHA256:VLERFMZnvN5H…/api"} {
		got, name, ok := src.Runtime(key)
		if !ok {
			t.Errorf("mastermind must resolve %q", key)
			continue
		}
		if name != key {
			t.Errorf("name = %q, want %q", name, key)
		}
		if got.Store == nil {
			t.Errorf("resolved runtime for %q has no Store", key)
		}
	}
}

// TestServerSourceRuntimeSplitsKey: a server source splits "owner/name"
// into the owner's runtime and the bare binding name, and refuses keys it
// cannot split or whose owner id is malformed.
func TestServerSourceRuntimeSplitsKey(t *testing.T) {
	root := t.TempDir()
	srv, err := serve.New(serve.Config{Root: root, DB: serverTestDB(t), Now: time.Now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id := remote.IDOf(kp.Public)

	src := ServerSource(srv)
	rt, name, ok := src.Runtime(string(id) + "/persist")
	if !ok {
		t.Fatal("Runtime(<id>/persist) = !ok, want ok")
	}
	if name != "persist" {
		t.Errorf("name = %q, want persist", name)
	}
	if rt.Store == nil {
		t.Error("resolved runtime has no Store")
	}

	if _, err := src.Status(context.Background()); err != nil {
		t.Fatalf("Status: %v", err)
	}
}

// TestServerSourceRuntimeRefusesBadKeys covers the !ok cases separately so
// a failure names the offending key.
func TestServerSourceRuntimeRefusesBadKeys(t *testing.T) {
	root := t.TempDir()
	srv, err := serve.New(serve.Config{Root: root, DB: serverTestDB(t), Now: time.Now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id := remote.IDOf(kp.Public)

	src := ServerSource(srv)
	for _, key := range []string{"persist", string(id) + "/", "bogus/x"} {
		if _, _, ok := src.Runtime(key); ok {
			t.Errorf("Runtime(%q) = ok, want !ok", key)
		}
	}
}

// TestMasterMindSourceBaseIsRuntime pins the contract: on a mastermind Base is
// the runtime itself, so scope all reads the mastermind's own database (§2).
func TestMasterMindSourceBaseIsRuntime(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}

	if base := (mastermindSource{rt: rt}).Base(); base.Store != rt.Store {
		t.Fatalf("Base().Store != the runtime's store")
	}
}

// TestServerSourceBaseHasNoDB pins the contract: a server's Base carries
// no database -- the server box does not run relevo.db, so scope all is
// refused there with the existing "no database" notice (§2).
func TestServerSourceBaseHasNoDB(t *testing.T) {
	root := t.TempDir()
	srv, err := serve.New(serve.Config{Root: root, DB: serverTestDB(t), Now: time.Now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if base := ServerSource(srv).Base(); base.DB != nil {
		t.Fatalf("serverSource.Base().DB = %v, want nil", base.DB)
	}
}
