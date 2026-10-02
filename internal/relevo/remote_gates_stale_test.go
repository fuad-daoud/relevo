package relevo

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

// staleAnswer is the server's own answer for a binding it no longer holds.
func staleAnswer() error {
	return &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: remote.CodeNotFound, Message: "not found"}}
}

// TestForwardUnavailableSkipsStaleBindings: the client holds three open
// remote bindings on one server, and the server has dropped two of them.
// Forwarding a gate posts to the one that is still there and emits a single
// summary naming how many were skipped -- never a "<binding>: <server>: not
// found" line per stale binding.
func TestForwardUnavailableSkipsStaleBindings(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	for _, n := range []string{"live-one", "stale-one", "stale-two"} {
		openRemote(t, st, n, "zen")
	}

	fr := &fakeRemote{
		candidatesResp: remote.CandidatesResponse{Candidates: []remote.CandidateView{{Token: testOpencodeRef, Name: "m"}}},
		unavailableErrFor: func(_, name, _ string) error {
			if strings.HasPrefix(name, "stale-") {
				return staleAnswer()
			}
			return nil
		},
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	lines := ForwardUnavailable(ctx, rt, testOpencodeRef, "RESOURCE_EXHAUSTED 429")

	for _, l := range lines {
		if strings.Contains(l, "stale-") || strings.Contains(l, "not found") {
			t.Fatalf("forward named a stale binding per line: %v", lines)
		}
	}
	want := "skipped 2 binding(s) the server no longer has"
	if !slices.Contains(lines, want) {
		t.Fatalf("lines = %v, want the summary %q", lines, want)
	}
	if got := strings.Count(strings.Join(lines, "\n"), "skipped"); got != 1 {
		t.Fatalf("summary printed %d times, want exactly 1: %v", got, lines)
	}

	// The binding the server still holds really was gated.
	var posted bool
	for _, c := range fr.calls {
		if c == "Unavailable:zen:live-one:"+testOpencodeRef {
			posted = true
		}
	}
	if !posted {
		t.Fatalf("the live binding was never gated: %v", fr.calls)
	}
}

// TestForwardUnavailableReportsRealFailures: a per-binding failure that is not
// "not found" is still named. The skip covers a binding the server dropped,
// never a failure that has somewhere to be fixed.
func TestForwardUnavailableReportsRealFailures(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	openRemote(t, st, "broken", "zen")

	fr := &fakeRemote{
		candidatesResp: remote.CandidatesResponse{Candidates: []remote.CandidateView{{Token: testOpencodeRef, Name: "m"}}},
		unavailableErr: &client.HTTPError{Status: 500, Body: remote.ErrorBody{Message: "boom"}},
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	lines := ForwardUnavailable(ctx, rt, testOpencodeRef, "RESOURCE_EXHAUSTED 429")
	var found bool
	for _, l := range lines {
		if strings.Contains(l, "broken: zen: boom") {
			found = true
		}
	}
	if !found {
		t.Fatalf("a real per-binding failure was swallowed: %v", lines)
	}
	for _, l := range lines {
		if strings.HasPrefix(l, "skipped") {
			t.Fatalf("a real failure was counted as stale: %v", lines)
		}
	}
}

// TestForwardNoSummaryWhenNothingIsStale: the summary counts what was skipped,
// so a forward with nothing stale must say nothing about it rather than
// announcing that it skipped nothing.
func TestForwardNoSummaryWhenNothingIsStale(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	openRemote(t, st, "live-one", "zen")

	fr := &fakeRemote{
		candidatesResp: remote.CandidatesResponse{Candidates: []remote.CandidateView{{Token: testOpencodeRef, Name: "m"}}},
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	for _, lines := range [][]string{
		ForwardUnavailable(ctx, rt, testOpencodeRef, "RESOURCE_EXHAUSTED 429"),
		ForwardAvailable(ctx, rt, testOpencodeRef),
	} {
		for _, l := range lines {
			if strings.Contains(l, "skipped") {
				t.Fatalf("a forward with nothing stale reported a skip: %v", lines)
			}
		}
	}
}

// TestForwardAvailableSkipsStaleServer: a clear against a server holding no
// such subject is skipped with one summary, not a "not found" line per binding.
func TestForwardAvailableSkipsStaleServer(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	openRemote(t, st, "open-remote", "zen")

	fr := &fakeRemote{
		candidatesResp:  remote.CandidatesResponse{Candidates: []remote.CandidateView{{Token: testOpencodeRef, Name: "m"}}},
		availableErrFor: func(string, string) error { return staleAnswer() },
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	lines := ForwardAvailable(ctx, rt, testOpencodeRef)
	if want := "skipped 1 binding(s) the server no longer has"; !slices.Contains(lines, want) {
		t.Fatalf("lines = %v, want the summary %q", lines, want)
	}
	for _, l := range lines {
		if strings.Contains(l, "not found") {
			t.Fatalf("forward printed a per-server not found line: %v", lines)
		}
	}
}
