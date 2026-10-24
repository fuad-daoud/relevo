package serve

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/fuad-daoud/relevo/internal/remote"
)

// TestCreateBindingStoresTrimpath pins the served half: the opt-out on the
// create lands on the owner's ServeFacts, where the round environment reads it.
func TestCreateBindingStoresTrimpath(t *testing.T) {
	env := setupTestEnv(t)

	createBody, err := json.Marshal(remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     env.repoID,
		BaseCommit: env.headSHA,
		Role:       "builder",
		Trimpath:   "off",
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings", createBody, "application/json")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", resp.StatusCode, string(body))
	}

	b, err := env.runtime(t).Store.Load("api")
	if err != nil {
		t.Fatalf("Load api: %v", err)
	}
	if b.Serve == nil {
		t.Fatal("binding has no ServeFacts")
	}
	if b.Serve.Trimpath != "off" {
		t.Errorf("Serve Trimpath = %q, want %q", b.Serve.Trimpath, "off")
	}
}

// TestCreateBindingWithoutTrimpathReadsTrimmed pins the default: a create
// that sends no trimpath value stores none, so the round reads trimmed.
func TestCreateBindingWithoutTrimpathReadsTrimmed(t *testing.T) {
	env := setupTestEnv(t)

	createBody, err := json.Marshal(remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     env.repoID,
		BaseCommit: env.headSHA,
		Role:       "builder",
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings", createBody, "application/json")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", resp.StatusCode, string(body))
	}

	b, err := env.runtime(t).Store.Load("api")
	if err != nil {
		t.Fatalf("Load api: %v", err)
	}
	if b.Serve == nil {
		t.Fatal("binding has no ServeFacts")
	}
	if b.Serve.Trimpath != "" {
		t.Errorf("Serve Trimpath = %q, want none", b.Serve.Trimpath)
	}
}

// TestCreateChainMembersStoreTrimpath pins the chain half: every stored member
// carries the create's opt-out, so member rounds read like a lone binding's.
func TestCreateChainMembersStoreTrimpath(t *testing.T) {
	env := setupTestEnv(t)

	req := chainWireRequest("shop", env.repoID, env.headSHA)
	req.Trimpath = "off"
	bundle := chainBundle(t, env, env.clientDir, env.headSHA, "shop")
	form, ct := makeChainForm(t, req, bundle)
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/chains", form, ct)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", resp.StatusCode, string(body))
	}

	rt := env.runtime(t)
	for _, name := range []string{"shop", "shop-rev", "shop-plan"} {
		m, err := rt.Store.Load(name)
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		if m.Serve == nil {
			t.Fatalf("%s has no ServeFacts", name)
		}
		if m.Serve.Trimpath != "off" {
			t.Errorf("%s Serve Trimpath = %q, want %q", name, m.Serve.Trimpath, "off")
		}
	}
}

// TestCreateChainWithoutTrimpathReadsTrimmed pins the chain default: members
// of a create that sends nothing store nothing.
func TestCreateChainWithoutTrimpathReadsTrimmed(t *testing.T) {
	env := setupTestEnv(t)

	resp, body := createChain(t, env, "shop")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", resp.StatusCode, string(body))
	}

	rt := env.runtime(t)
	for _, name := range []string{"shop", "shop-rev", "shop-plan"} {
		m, err := rt.Store.Load(name)
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		if m.Serve == nil {
			t.Fatalf("%s has no ServeFacts", name)
		}
		if m.Serve.Trimpath != "" {
			t.Errorf("%s Serve Trimpath = %q, want none", name, m.Serve.Trimpath)
		}
	}
}
