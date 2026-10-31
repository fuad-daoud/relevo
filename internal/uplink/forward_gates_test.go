package uplink

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// gateCandidateSet loads a two-provider candidate set, so a forward can tell
// a bare provider this machine configures from a candidate token it must map.
func gateCandidateSet(t *testing.T) *candidate.Set {
	t.Helper()
	const body = `[
  {"harness":"opencode","provider":"test","model":"m","roles":["builder"]},
  {"harness":"agy","provider":"other","model":"m","roles":["builder"]}
]`
	path := filepath.Join(t.TempDir(), "candidates.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := candidate.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// saveOpenGate saves one open remote round on server.
func saveOpenGate(t *testing.T, st *store.Store, name, server string) {
	t.Helper()
	b := remoteBinding(server)
	b.Name = name
	b.CWD = "/fake/" + name
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendLog(name, store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt}); err != nil {
		t.Fatal(err)
	}
}

// TestForwardUnavailablePostsBareProvider pins the provider-wide path: a
// provider this machine configures travels as itself to every open binding,
// and the server's candidate list is never consulted.
func TestForwardUnavailablePostsBareProvider(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	saveOpenGate(t, st, "one", "zen")
	saveOpenGate(t, st, "two", "zen")
	fr := &fakeRemote{}
	rt := relevo.Runtime{Store: st, Remote: fr, Candidates: gateCandidateSet(t), Now: func() time.Time { return baseTime }}

	if lines := ForwardUnavailable(ctx, rt, "test", "hit a limit", time.Time{}); len(lines) != 0 {
		t.Fatalf("lines = %v, want none", lines)
	}
	if want := []string{"Unavailable:zen:one:test", "Unavailable:zen:two:test"}; !slices.Equal(fr.calls, want) {
		t.Fatalf("calls = %v, want %v", fr.calls, want)
	}
}

// TestForwardAvailableClearsOncePerServer pins the clear path: one call and
// one answer line per distinct server, whatever each binding's round state.
func TestForwardAvailableClearsOncePerServer(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	saveOpenGate(t, st, "one", "zen")
	two := remoteBinding("zen")
	two.Name = "two"
	if err := st.Save(two); err != nil {
		t.Fatal(err)
	}
	fr := &fakeRemote{availableResp: remote.AvailableResponse{Provider: "test", Removed: 2}}
	rt := relevo.Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	lines := ForwardAvailable(ctx, rt, "test")
	if want := []string{"zen: cleared test (2 entries)"}; !slices.Equal(lines, want) {
		t.Fatalf("lines = %v, want %v", lines, want)
	}
	if want := []string{"Available:zen:test"}; !slices.Equal(fr.calls, want) {
		t.Fatalf("calls = %v, want %v", fr.calls, want)
	}
}

// TestServerInUseNamesRemoteBindings pins the pure rule behind the
// `config server rm` refusal: only remote bindings count, local ones never do.
func TestServerInUseNamesRemoteBindings(t *testing.T) {
	t.Parallel()

	bindings := []store.Binding{
		remoteBinding("zen"),
		func() store.Binding { b := remoteBinding("mars"); b.Name = "other"; return b }(),
		{Name: "local", Builder: store.Endpoint{Mode: store.Mode("pane")}},
	}

	if got := ServerInUse(bindings, "zen"); !slices.Equal(got, []string{"api"}) {
		t.Fatalf("ServerInUse(zen) = %v, want [api]", got)
	}
	if got := ServerInUse(bindings, "mars"); !slices.Equal(got, []string{"other"}) {
		t.Fatalf("ServerInUse(mars) = %v, want [other]", got)
	}
	if got := ServerInUse(bindings, "pluto"); len(got) != 0 {
		t.Fatalf("ServerInUse(pluto) = %v, want none", got)
	}
}

// TestNextGateLineRendersServeCommand pins the next: line: the same gate
// restated for the server's host, with remaining time and reason.
func TestNextGateLineRendersServeCommand(t *testing.T) {
	t.Parallel()

	if got, want := NextGateLine("zen", "test", "", time.Time{}, baseTime), "next: relevo gate --serve test  # on zen"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := NextGateLine("zen", "test", "it's 429", baseTime.Add(90*time.Minute), baseTime),
		`next: relevo gate --serve test --for 1h30m0s --reason 'it'\''s 429'  # on zen`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
