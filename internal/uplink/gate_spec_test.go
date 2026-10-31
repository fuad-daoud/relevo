package uplink

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

func TestServerInUse(t *testing.T) {
	t.Parallel()

	bindings := []store.Binding{
		remoteBinding("zen"),
		func() store.Binding { b := remoteBinding("mars"); b.Name = "other"; return b }(),
		{Name: "local", Builder: store.Endpoint{Mode: store.Mode("pane")}},
	}

	if got := ServerInUse(bindings, "zen"); len(got) != 1 || got[0] != "api" {
		t.Fatalf("ServerInUse(zen) = %v, want [api]", got)
	}
	if got := ServerInUse(bindings, "mars"); len(got) != 1 || got[0] != "other" {
		t.Fatalf("ServerInUse(mars) = %v, want [other]", got)
	}
	if got := ServerInUse(bindings, "pluto"); len(got) != 0 {
		t.Fatalf("ServerInUse(pluto) = %v, want none", got)
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
