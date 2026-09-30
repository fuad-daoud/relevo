package main

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestBoundLineNamesTheRemoteServer pins the one human line a bind prints: a
// remote builder names the server it runs on, the actor's own placement adds the
// same clause the pick note carries, and a local builder keeps its old wording.
// The wording is pinned through the pure helper because no live server exists in
// tests.
func TestBoundLineNamesTheRemoteServer(t *testing.T) {
	t.Parallel()

	remote := store.Binding{
		Name:       "api",
		MasterMind: store.Endpoint{PaneID: "mm"},
		Builder:    store.Endpoint{Mode: store.ModeRemote, Server: "zen"},
		Round:      1,
	}

	chosen := relevo.PlacementResolution{
		Name: "zen", How: "actor",
		Skipped: []relevo.PlacementSkip{{Name: "backup", Reason: "unreachable"}},
	}
	wantChosen := "bound api: mastermind mm -> builder flash on zen, round 1; placement zen (actor); skipped backup (unreachable)"
	if got := boundLine(remote, "mm", "flash", chosen); got != wantChosen {
		t.Fatalf("boundLine with the actor's placement = %q, want %q", got, wantChosen)
	}

	// An explicit --server, and a placement nothing chose, add no clause.
	for _, placement := range []relevo.PlacementResolution{
		{Name: "zen", How: "explicit"},
		{},
	} {
		want := "bound api: mastermind mm -> builder flash on zen, round 1"
		if got := boundLine(remote, "mm", "flash", placement); got != want {
			t.Fatalf("boundLine with %+v = %q, want %q", placement, got, want)
		}
	}

	local := store.Binding{
		Name:       "api",
		MasterMind: store.Endpoint{PaneID: "mm"},
		Builder:    store.Endpoint{Mode: store.ModeHeadless},
		Round:      1,
	}
	wantLocal := "bound api: mastermind mm -> builder headless (flash), round 1"
	if got := boundLine(local, "mm", "flash", relevo.PlacementResolution{}); got != wantLocal {
		t.Fatalf("boundLine for a local builder = %q, want %q", got, wantLocal)
	}
}
