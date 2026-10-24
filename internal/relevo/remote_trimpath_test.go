package relevo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/remote"
)

// TestRemoteAddSendsTrimpathOptOut pins the wire: a repository that opts out
// carries "off" on the create, so the served binding's rounds start untrimmed.
func TestRemoteAddSendsTrimpathOptOut(t *testing.T) {
	ctx := context.Background()
	rt := newRuntime(t)
	fg := &fakeGit{
		headCommitID:  "1111111111111111111111111111111111111111",
		rootCommitSHA: reposRootCommit,
	}
	rt.Git = fg
	st := auditStore(t)
	seedOneRepos(t, st, fmt.Sprintf(`{%q:{"name":"shop","trimpath":"off"}}`, reposKeyOf(t)))
	rt.Config = st
	fr := &fakeRemote{
		createBindingResp: remote.BindingView{Name: "api", Candidate: "claude/anthropic/haiku"},
	}
	rt.Remote = fr
	rt.MasterMinds = addRemoteMasterMind(t)

	if _, err := Add(ctx, rt, AddOptions{Name: "api", Server: "zen", Repo: "/fake/repo"}); err != nil {
		t.Fatalf("Add --server: %v", err)
	}
	if got := fr.createBindingReq.Trimpath; got != "off" {
		t.Errorf("create Trimpath = %q, want %q", got, "off")
	}
}

// TestRemoteAddOmitsTrimpathWhenUnset pins the default: a repository with no
// trimpath setting sends nothing, so the wire omits the key and the served
// binding reads trimmed.
func TestRemoteAddOmitsTrimpathWhenUnset(t *testing.T) {
	ctx := context.Background()
	rt := newRuntime(t)
	rt.Git = &fakeGit{
		headCommitID:  "1111111111111111111111111111111111111111",
		rootCommitSHA: reposRootCommit,
	}
	fr := &fakeRemote{
		createBindingResp: remote.BindingView{Name: "api", Candidate: "claude/anthropic/haiku"},
	}
	rt.Remote = fr
	rt.MasterMinds = addRemoteMasterMind(t)

	if _, err := Add(ctx, rt, AddOptions{Name: "api", Server: "zen", Repo: "/fake/repo"}); err != nil {
		t.Fatalf("Add --server: %v", err)
	}
	if got := fr.createBindingReq.Trimpath; got != "" {
		t.Errorf("create Trimpath = %q, want none", got)
	}
	out, err := json.Marshal(fr.createBindingReq)
	if err != nil {
		t.Fatalf("marshal create: %v", err)
	}
	if strings.Contains(string(out), `"trimpath"`) {
		t.Errorf("create carries a trimpath key without the opt-out: %s", out)
	}
}

// TestChainServerCreateCarriesTrimpathOptOut pins the chain half: the resolve
// reads the repository's opt-out once, and the posted create carries it.
func TestChainServerCreateCarriesTrimpathOptOut(t *testing.T) {
	t.Parallel()

	fr := chainServerFake()
	rt, fg, _ := chainServerRuntime(t, fr)
	fg.rootCommitSHA = reposRootCommit
	st := auditStore(t)
	seedOneRepos(t, st, fmt.Sprintf(`{%q:{"name":"shop","trimpath":"off"}}`, reposKeyOf(t)))
	rt.Config = st

	if _, err := ChainStart(context.Background(), rt, chainServerOpts(t)); err != nil {
		t.Fatalf("ChainStart --server: %v", err)
	}
	if got := fr.createChainReq.Trimpath; got != "off" {
		t.Errorf("chain create Trimpath = %q, want %q", got, "off")
	}
}

// TestChainServerCreateOmitsTrimpathWhenUnset pins the chain default: no
// opt-out sends nothing, so the wire omits the key.
func TestChainServerCreateOmitsTrimpathWhenUnset(t *testing.T) {
	t.Parallel()

	fr := chainServerFake()
	rt, _, _ := chainServerRuntime(t, fr)

	if _, err := ChainStart(context.Background(), rt, chainServerOpts(t)); err != nil {
		t.Fatalf("ChainStart --server: %v", err)
	}
	if got := fr.createChainReq.Trimpath; got != "" {
		t.Errorf("chain create Trimpath = %q, want none", got)
	}
	out, err := json.Marshal(fr.createChainReq)
	if err != nil {
		t.Fatalf("marshal chain create: %v", err)
	}
	if strings.Contains(string(out), `"trimpath"`) {
		t.Errorf("chain create carries a trimpath key without the opt-out: %s", out)
	}
}
