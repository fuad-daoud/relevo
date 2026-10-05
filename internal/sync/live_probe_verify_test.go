package sync_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// TestLiveEnableToProbePointAgainstAFakeRemote is the live-verification line:
// the enable flow driven to the point of the probe open, against a fake remote,
// over a database holding this machine's history, with the live file's hash
// compared before and after.
//
// It uses the production gate rather than a stand-in, so what it proves is that
// the probe's open is accepted by the one place that refuses an unnamed role.
// The fake replaces only the driver: reaching one is the whole point, and a
// refusal would happen before it.
//
// A build carrying no driver has nothing to open and is skipped rather than
// failed. That build's OpenRemote refuses everything by design, so there is no
// open for this test to make and its refusal would say nothing about the role.
func TestLiveEnableToProbePointAgainstAFakeRemote(t *testing.T) {
	if _, err := relevosync.OpenRemote(context.Background(), relevosync.OpenConfig{}); err != nil &&
		strings.Contains(err.Error(), "carries no sync driver") {
		t.Skipf("this build carries no sync driver: %v", err)
	}

	dir, live, shared := liveProbeHistory(t)
	before := liveHash(t, live)
	if beforeRows := liveRows(t, shared); beforeRows != liveProbeRows {
		t.Fatalf("the fixture holds %d rows before, want %d", beforeRows, liveProbeRows)
	}

	// The probe path exactly as the daemon runs it: a throwaway beside the live
	// file, an open naming it, the gate deciding, a pull asking the question.
	scratch, err := relevosync.NewThrowaway(live)
	if err != nil {
		t.Fatalf("NewThrowaway: %v", err)
	}
	defer scratch.Release()

	client, err := relevosync.OpenRemote(context.Background(), relevosync.OpenConfig{
		Role:             relevosync.OpenScratch,
		Path:             scratch.Path,
		RemoteURL:        "libsql://relevo-live-probe.turso.io",
		Namespace:        "relevo",
		ClientName:       "relevo",
		AuthToken:        []byte("FIXTURE-TOKEN-live-probe"),
		BootstrapIfEmpty: false,
	})
	if err != nil {
		t.Fatalf("the probe's open was refused: %v", err)
	}
	if client == nil {
		t.Fatal("the probe's open returned no handle and no error")
	}
	t.Logf("the probe's open was accepted carrying scratch; throwaway at %s", scratch.Path)

	if after := liveHash(t, live); after != before {
		t.Errorf("the live file changed across the probe: %s -> %s", before, after)
	}
	if got := liveRows(t, shared); got != liveProbeRows {
		t.Errorf("the live file holds %d rows after the probe, want %d", got, liveProbeRows)
	}

	scratch.Release()
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".relevo-probe-*"))
	if len(leftovers) != 0 {
		t.Errorf("the throwaway outlived Release: %v", leftovers)
	}
	t.Logf("live file identical before and after: %s", before)
}

// liveProbeRows is how many history rows the fixture writes. Enough that a file
// holding them is unmistakably not a skeleton, so a wipe shows as a count change
// and not only as a hash change.
const liveProbeRows = 200

// liveProbeHistory opens a split pair holding liveProbeRows stamped rows and
// returns the directory it lives in, the live file's path and the open handle.
//
// The rows are stamped with an origin because that is what the enable's
// preflight reads, so an enable on this fixture reaches its probe rather than
// stopping at a check.
func liveProbeHistory(t *testing.T) (string, string, *db.DB) {
	t.Helper()

	dir, err := os.MkdirTemp("", "relevo-liveprobe-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	live := filepath.Join(dir, "relevo.db")
	shared, err := db.OpenSplit(live, db.Options{})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	if _, err := relevosync.LocalHandle(shared); err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}

	for i := range liveProbeRows {
		if _, err := shared.RecordPut(db.Record{
			ID: fmt.Sprintf("live-%03d", i), Owner: "live-owner",
			Name: fmt.Sprintf("live-%03d", i), State: "done", Round: 1,
			CWD: "/live", JSON: `{"live":true}`,
			CreatedAt: time.Unix(0, 0).UTC(), UpdatedAt: time.Unix(0, 0).UTC(),
		}); err != nil {
			t.Fatalf("RecordPut %d: %v", i, err)
		}
	}
	if _, _, err := db.BackfillOriginOnce(shared, "live-probe-installation", time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("BackfillOriginOnce: %v", err)
	}
	return dir, live, shared
}

func liveHash(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(body)
	return fmt.Sprintf("%d bytes %s", len(body), hex.EncodeToString(sum[:8]))
}

func liveRows(t *testing.T, d *db.DB) int {
	t.Helper()
	live, _, err := d.RecordCounts()
	if err != nil {
		t.Fatalf("RecordCounts: %v", err)
	}
	return live
}
