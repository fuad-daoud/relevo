package relevo

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

func callsWithPrefix(fr *fakeRemote, prefix string) []string {
	var out []string
	for _, c := range fr.calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

// TestForwardUnavailablePostsBareProviderPerOpenBinding: a provider this
// machine configures travels as itself to the server, which gates it
// provider-wide; the server's candidate list is never consulted, so no
// mismatch line can appear.
func TestForwardUnavailablePostsBareProviderPerOpenBinding(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	openRemote(t, st, "one", "zen")
	openRemote(t, st, "two", "zen")
	fr := &fakeRemote{}
	rt := Runtime{Store: st, Remote: fr, Candidates: candidateSet(t, testTwoProviderJSON), Now: func() time.Time { return baseTime }}

	lines := ForwardUnavailable(context.Background(), rt, "test", "hit a limit", time.Time{})
	if len(lines) != 0 {
		t.Fatalf("lines = %v, want none", lines)
	}
	got := callsWithPrefix(fr, "Unavailable:")
	want := []string{"Unavailable:zen:one:test", "Unavailable:zen:two:test"}
	if !slices.Equal(got, want) {
		t.Fatalf("Unavailable calls = %v, want %v", got, want)
	}
}

// TestForwardUnavailableProviderForwardsAccountKeys: the live group@account
// keys the ledger holds for a bare provider travel as the account keys, the
// same as for a candidate token of that provider.
func TestForwardUnavailableProviderForwardsAccountKeys(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	openRemote(t, st, "open-remote", "zen")
	fr := &fakeRemote{whoAmIResp: remote.WhoAmI{Features: []string{remote.FeatureAccounts}}}
	rt := Runtime{Store: st, Remote: fr, Gates: testGateKV(t), Candidates: candidateSet(t, testTwoProviderJSON), Now: func() time.Time { return baseTime }}
	seedAccountGate(t, rt, "test", "cp2")

	lines := ForwardUnavailable(context.Background(), rt, "test", "hit a limit", time.Time{})
	if want := []string{"zen: gated test@cp2"}; !slices.Equal(lines, want) {
		t.Fatalf("lines = %v, want %v", lines, want)
	}
	if got, want := callsWithPrefix(fr, "Unavailable:"), []string{"Unavailable:zen:open-remote:test@cp2"}; !slices.Equal(got, want) {
		t.Fatalf("Unavailable calls = %v, want %v", got, want)
	}
}

func unknownEverywhere() *fakeRemote {
	err := fmt.Errorf("get: %w", client.ErrUnknownServer)
	return &fakeRemote{candidatesErr: err, whoAmIErr: err, unavailableErr: err, availableErr: err}
}

// TestForwardsSkipUnconfiguredServerSilently: a binding naming a server this
// machine does not configure yields no line, in either forward, for every
// subject form.
func TestForwardsSkipUnconfiguredServerSilently(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	openRemote(t, st, "old", "contabo")
	rt := Runtime{Store: st, Remote: unknownEverywhere(), Candidates: candidateSet(t, testTwoProviderJSON), Now: func() time.Time { return baseTime }}
	ctx := context.Background()

	for _, subject := range []string{testOpencodeRef, "test", "test@cp2"} {
		if lines := ForwardUnavailable(ctx, rt, subject, "r", time.Time{}); len(lines) != 0 {
			t.Errorf("ForwardUnavailable(%q) lines = %v, want none", subject, lines)
		}
		if lines := ForwardAvailable(ctx, rt, subject); len(lines) != 0 {
			t.Errorf("ForwardAvailable(%q) lines = %v, want none", subject, lines)
		}
	}
}

// TestForwardUnavailableFailuresEndWithNextCommand: every outcome that leaves
// a configured server ungated carries one command gating the same subject,
// remaining time and reason on that server.
func TestForwardUnavailableFailuresEndWithNextCommand(t *testing.T) {
	t.Parallel()

	until := baseTime.Add(time.Hour)
	cases := []struct {
		name    string
		fr      *fakeRemote
		subject string
		want    string
	}{
		{"list read error", &fakeRemote{candidatesErr: errors.New("boom")}, testOpencodeRef,
			"next: relevo gate --serve opencode/test/m --for 1h0m0s --reason 'hit a limit'  # on zen"},
		{"mismatch", &fakeRemote{candidatesResp: remote.CandidatesResponse{Candidates: []remote.CandidateView{{Token: "agy/other/x"}}}}, testOpencodeRef,
			"next: relevo gate --serve opencode/test/m --for 1h0m0s --reason 'hit a limit'  # on zen"},
		{"post error", &fakeRemote{unavailableErr: errors.New("boom")}, "test",
			"next: relevo gate --serve test --for 1h0m0s --reason 'hit a limit'  # on zen"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := store.New(t.TempDir())
			openRemote(t, st, "a", "zen")
			openRemote(t, st, "b", "zen")
			rt := Runtime{Store: st, Remote: tc.fr, Candidates: candidateSet(t, testTwoProviderJSON), Now: func() time.Time { return baseTime }}

			lines := ForwardUnavailable(context.Background(), rt, tc.subject, "hit a limit", until)
			var nexts []string
			for _, l := range lines {
				if strings.HasPrefix(l, "next:") {
					nexts = append(nexts, l)
				}
			}
			if !slices.Equal(nexts, []string{tc.want}) {
				t.Fatalf("next lines = %v, want exactly %q (lines %v)", nexts, tc.want, lines)
			}
		})
	}
}

// TestForwardUnavailableStaleOnlyHasNoNextCommand: stale bindings keep the
// single summary and nothing to run by hand.
func TestForwardUnavailableStaleOnlyHasNoNextCommand(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	openRemote(t, st, "a", "zen")
	fr := &fakeRemote{unavailableErr: staleAnswer()}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	lines := ForwardUnavailable(context.Background(), rt, testOpencodeRef, "r", time.Time{})
	if want := []string{staleLine(1)}; !slices.Equal(lines, want) {
		t.Fatalf("lines = %v, want %v", lines, want)
	}
}

func TestNextGateLine(t *testing.T) {
	t.Parallel()

	if got, want := NextGateLine("zen", "test", "", time.Time{}, baseTime), "next: relevo gate --serve test  # on zen"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := NextGateLine("zen", "test", "it's 429", baseTime.Add(90*time.Minute), baseTime),
		`next: relevo gate --serve test --for 1h30m0s --reason 'it'\''s 429'  # on zen`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
