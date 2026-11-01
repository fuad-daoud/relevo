package relevo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/store"
)

func TestPickRefusalFromWireRebuildsTheTypedRefusal(t *testing.T) {
	t.Parallel()
	wire := func(status int, code remote.Code, msg string) error {
		return &client.HTTPError{Status: status, Body: remote.ErrorBody{Code: code, Message: msg}}
	}
	gated := `every candidate serving "builder" is gated: opencode/opencode/glm (rate-limited until cleared); name one with --candidate to bypass: every candidate serving the actor is gated`
	notServed := `candidate "sonnet" does not serve actor "builder" (its roles: []): no candidate serves actor`
	cases := []struct {
		name string
		err  error
		want error
	}{
		{"all gated", wire(422, remote.CodeInvalid, gated), resolve.ErrAllGated},
		{"role not served", wire(422, remote.CodeInvalid, notServed), resolve.ErrRoleNotServed},
	}
	for _, tc := range cases {
		got := pickRefusalFromWire(tc.err)
		if !errors.Is(got, tc.want) {
			t.Errorf("%s: err = %v, want errors.Is(err, %v)", tc.name, got, tc.want)
		}
		var httpErr *client.HTTPError
		if errors.As(tc.err, &httpErr) && got.Error() != httpErr.Body.Message {
			t.Errorf("%s: err = %q, want the server's message unchanged", tc.name, got)
		}
	}
	for _, other := range []error{
		wire(422, remote.CodeInvalid, "something else"),
		wire(500, "", gated),
		errors.New(gated),
	} {
		// Both directions: a wrap still matches one way, so a single
		// errors.Is would pass while the passthrough is broken.
		if got := pickRefusalFromWire(other); !errors.Is(got, other) || !errors.Is(other, got) {
			t.Errorf("pickRefusalFromWire(%v) = %v, want it unchanged", other, got)
		}
	}
}

func TestAddRemoteAllGatedIsARefusal(t *testing.T) {
	// No t.Parallel: addRemoteMasterMind sets env, like TestAddRemoteTierUnsupportedWraps.
	fg := &fakeGit{
		headCommitID:  "1111111111111111111111111111111111111111",
		rootCommitSHA: "2222222222222222222222222222222222222222",
	}
	fr := &fakeRemote{createBindingErr: &client.HTTPError{Status: 422, Body: remote.ErrorBody{
		Code:    remote.CodeInvalid,
		Message: `every candidate serving "builder" is gated: x (rate-limited until cleared): every candidate serving the actor is gated`,
	}}}
	rt := Runtime{
		Store:       store.New(t.TempDir()),
		MasterMinds: addRemoteMasterMind(t),
		Git:         fg,
		Remote:      fr,
		Now:         time.Now,
	}
	_, err := Add(context.Background(), rt, AddOptions{Name: "api", Server: "zen", Repo: "/fake/repo"})
	if !errors.Is(err, resolve.ErrAllGated) {
		t.Fatalf("err = %v, want errors.Is(err, resolve.ErrAllGated)", err)
	}
	if len(fg.createBranchCalls) != 0 {
		t.Fatalf("createBranch called %d times, want 0", len(fg.createBranchCalls))
	}
}
