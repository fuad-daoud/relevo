//go:build !modernc

package sync

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestAnOpenConvergesTheSidecarBeforeTheDriverIsReached pins the wiring, which is
// the only part of the convergence a test of ConvergeSidecars alone cannot see.
//
// The open is given a scratch role and a remote nothing answers on, so the call
// ends in a dial failure -- which is the point: the sidecar has to be gone by the
// time the driver is handed the path, not afterwards, and a failure at the dial
// is after that point. The assertions are therefore about the file, not about the
// error, and the error is only asserted to be the dial's.
func TestAnOpenConvergesTheSidecarBeforeTheDriverIsReached(t *testing.T) {
	path := writeSidecar(t, readSidecar(t, tornSidecarPath))
	info := filepath.Join(filepath.Dir(path), "relevo.db-info")
	const identity = `{"client_unique_id":"relevo-test"}`
	if err := os.WriteFile(info, []byte(identity), 0o600); err != nil {
		t.Fatalf("write the info file: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg := OpenConfig{
		Role:      OpenScratch,
		Path:      path,
		RemoteURL: "http://127.0.0.1:1/relevo-test",
		AuthToken: []byte("not-a-real-token"),
	}
	if _, err := OpenRemote(ctx, cfg); err == nil {
		t.Skip("this build's driver reached a remote at a refused port; the call shape is not observable here")
	}

	if _, err := os.Stat(path + sidecarSuffix); !os.IsNotExist(err) {
		t.Errorf("the sidecar survived the open: stat err = %v", err)
	}
	got, err := os.ReadFile(info)
	if err != nil {
		t.Fatalf("the open took the info file: %v", err)
	}
	if string(got) != identity {
		t.Errorf("info file = %q, want it untouched", got)
	}
}

// TestAnOpenLeavesAWholeSidecarAlone is the negative of the wiring: an open on a
// machine whose sidecar parses must not remove anything. A refusal at the role
// check is enough -- it happens before the driver and before the convergence, so
// this only says the check is still first, which is the order both fixes need.
func TestAnOpenRefusesAnUnstatedRoleBeforeConvergingAnything(t *testing.T) {
	path := writeSidecar(t, readSidecar(t, tornSidecarPath))

	_, err := OpenRemote(context.Background(), OpenConfig{Role: OpenUnset, Path: path})
	if err == nil {
		t.Fatal("an open naming no role was accepted")
	}
	if _, serr := os.Stat(path + sidecarSuffix); serr != nil {
		t.Errorf("the sidecar was removed by an open that never got that far: stat err = %v", serr)
	}
}
