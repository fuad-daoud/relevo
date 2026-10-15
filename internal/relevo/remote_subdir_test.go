package relevo

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/remote"
)

// remoteSubdirFake is the remote server a subdir bind lands on: it advertises
// the feature the prefix needs and answers the create.
func remoteSubdirFake(withFeature bool) *fakeRemote {
	features := []string{remote.FeatureSubdir}
	if !withFeature {
		features = nil
	}
	return &fakeRemote{
		whoAmIResp: remote.WhoAmI{Features: features},
		createBindingResp: remote.BindingView{
			Name: "api", Candidate: "claude/anthropic/haiku",
		},
	}
}

// remoteSubdirGit answers realAdd's git calls: a prefix from the caller's
// checkout and a base that names the directory at it.
func remoteSubdirGit(prefix string, dirAt bool) *fakeGit {
	return &fakeGit{
		headCommitID:     "1111111111111111111111111111111111111111",
		rootCommitSHA:    "2222222222222222222222222222222222222222",
		showPrefixResult: prefix,
		dirAtResult:      dirAt,
	}
}

// remoteSubdirAdd runs the --server bind once against a repo started below
// the root.
func remoteSubdirAdd(t *testing.T, fg *fakeGit, fr *fakeRemote) (AddResult, error) {
	t.Helper()
	rt := newTestRuntime(t, fg)
	rt.Remote = fr
	return Add(context.Background(), rt, AddOptions{
		Name: "api", Server: "zen", Repo: "/fake/repo", MasterMindID: testMasterMindName,
	})
}

// TestRemoteAddSendsTheSubdirectory pins the wire: the create carries the
// prefix the client resolved, and the client's mirror row carries none -- its
// CWD is the caller's checkout, which already is the subdirectory. Putting
// the prefix on the mirror too would double-join that row's rounds.
func TestRemoteAddSendsTheSubdirectory(t *testing.T) {
	fg := remoteSubdirGit("src/api/", true)
	fr := remoteSubdirFake(true)
	res, err := remoteSubdirAdd(t, fg, fr)
	if err != nil {
		t.Fatalf("Add --server: %v", err)
	}
	if got := fr.createBindingReq.Subdir; got != "src/api" {
		t.Errorf("create Subdir = %q, want %q", got, "src/api")
	}
	if res.Binding.Subdir != "" {
		t.Errorf("mirror Subdir = %q, want none", res.Binding.Subdir)
	}
}

// TestRemoteAddRefusesAServerWithoutTheSubdirectoryFeature pins the refusal: a
// bind that would send a prefix to a server that predates the field refuses,
// naming the server, and sends nothing. Sending it anyway would land the
// served round at the server worktree's root.
func TestRemoteAddRefusesAServerWithoutTheSubdirectoryFeature(t *testing.T) {
	fg := remoteSubdirGit("src/api/", true)
	fr := remoteSubdirFake(false)
	_, err := remoteSubdirAdd(t, fg, fr)
	if err == nil || !strings.Contains(err.Error(), "zen") {
		t.Fatalf("err = %v, want the pre-subdir server refusal naming zen", err)
	}
	if !slices.Contains(fr.calls, "WhoAmI:zen") || slices.Contains(fr.calls, "CreateBinding:zen") {
		t.Errorf("calls = %v, want the probe answered and no create sent", fr.calls)
	}
	if fr.createBindingReq.Name != "" {
		t.Errorf("create = %+v, want nothing sent", fr.createBindingReq)
	}
}
