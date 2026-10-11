package upgrade

import (
	"context"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

// TestSelfImageIdentityMatchesThePathWithNothingReplaced is the quiet case:
// with no replacement in the way, the running image and the path are one file,
// so a baseline taken from either source reads the same and the watcher does
// not fire. Mutation: an identity the path never has, which turns every tick
// into a change worth re-exec'ing into.
func TestSelfImageIdentityMatchesThePathWithNothingReplaced(t *testing.T) {
	exe, err := ResolveExe()
	if err != nil {
		t.Fatalf("ResolveExe: %v", err)
	}

	image, err := SelfImageIdentity()
	if err != nil {
		t.Fatalf("SelfImageIdentity: %v", err)
	}
	if image == (store.FileID{}) {
		t.Fatal("SelfImageIdentity returned the zero value, which no real identity equals")
	}

	onPath, err := ExeIdentity(exe)
	if err != nil {
		t.Fatalf("ExeIdentity(%s): %v", exe, err)
	}
	if image != onPath {
		t.Errorf("running image %+v != path identity %+v, with nothing replaced", image, onPath)
	}

	preflights := 0
	w := &Watcher{Path: exe, Started: image, Stat: ExeIdentity}
	w.Preflight = func(context.Context, string) error {
		preflights++
		return nil
	}
	if got := w.Check(context.Background()); got.Action != None {
		t.Errorf("Check with nothing replaced = %v, want None", got.Action)
	}
	if preflights != 0 {
		t.Errorf("preflight ran %d times with nothing replaced, want none", preflights)
	}
}

// TestWatcherBaselineFromTheRunningImageStillSeesAReplacement is the watcher's
// half of the same bug, with the path polls injected: a baseline that is not
// the running image's reads as the replacement the first time it polls, so it
// waits once and then re-execs. Mutation: the baseline is taken from the path
// after the replacement lands, which makes the first poll read as no change at
// all, and the answer is None, never Reexec.
func TestWatcherBaselineFromTheRunningImageStillSeesAReplacement(t *testing.T) {
	running, replacement := id(11), id(22)

	preflights := 0
	w := &Watcher{Path: "/usr/local/bin/relevo", Started: running, Stat: ExeIdentity}
	w.Stat = func(string) (store.FileID, error) { return replacement, nil }
	w.Preflight = func(context.Context, string) error {
		preflights++
		return nil
	}

	if got := w.Check(context.Background()); got.Action != Wait {
		t.Fatalf("first Check = %v, want Wait", got.Action)
	}
	if got := w.Check(context.Background()); got.Action != Reexec {
		t.Errorf("second Check = %v, want Reexec", got.Action)
	}
	if preflights != 1 {
		t.Errorf("preflight ran %d times, want 1", preflights)
	}
}
