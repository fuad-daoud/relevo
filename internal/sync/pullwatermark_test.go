//go:build !modernc

package sync

// The pull that a stale revert watermark wedges, driven through the real client
// against the real driver: a scratch file carrying a watermark above its own
// log's highest frame, and a fake driver underneath so the refusal and the
// retry are both observable without a remote.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	turso "turso.tech/database/tursogo"
)

// driverPull is the one driver call a stale watermark makes fail. A real remote
// is what refuses on a live machine; what this substitutes is only the refusal
// itself, which is the thing the invalidation has to recognise.
type driverPull struct {
	// refusals is how many more pulls refuse before one succeeds.
	refusals atomic.Int64
	// reopens counts how many handles the client rebuilt.
	reopens atomic.Int64
	// applied is what a pull that got through reports.
	applied bool
}

func (d *driverPull) Pull(ctx context.Context) (bool, error) {
	if d.refusals.Load() <= 0 {
		return d.applied, nil
	}
	d.refusals.Add(-1)
	return false, errors.New("sync engine operation failed: " +
		"unable to checkpoint synced portion of WAL: result=1, watermark=452")
}

func (d *driverPull) Push(context.Context) error { return nil }

func (d *driverPull) Stats(context.Context) (turso.TursoSyncDbStats, error) {
	return turso.TursoSyncDbStats{}, nil
}

func (d *driverPull) Checkpoint(context.Context) error { return nil }

// TestAPullWedgedByAStaleWatermarkSyncsAfterItIsInvalidated is the shape the
// live machine refused with, end to end through the client: a file whose
// persisted watermark sits above its own log's highest frame cannot satisfy a
// checkpoint, the client clears exactly that watermark, rebuilds its handle and
// pulls again, and the pull goes through.
func TestAPullWedgedByAStaleWatermarkSyncsAfterItIsInvalidated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wedged.db")
	writeWAL(t, path, 4096, 216)
	writeInfo(t, path, staleInfo(452, 216))

	driver := &driverPull{applied: true}
	driver.refusals.Store(1)
	client := &Turso{path: path, db: driver}
	client.Reopen = func(context.Context) (syncDatabase, error) {
		driver.reopens.Add(1)
		return driver, nil
	}

	applied, err := client.Pull(t.Context())
	if err != nil {
		t.Fatalf("the pull did not recover from the stale watermark: %v", err)
	}
	if !applied {
		t.Error("the pull went through but applied nothing")
	}
	if got := driver.reopens.Load(); got != 1 {
		t.Errorf("the client rebuilt its handle %d times, want exactly 1", got)
	}
	info, err := ReadInfoWatermark(path)
	if err != nil {
		t.Fatalf("ReadInfoWatermark: %v", err)
	}
	if info.Present {
		t.Errorf("the watermark %d is still persisted after the pull", info.Watermark)
	}
	if _, serr := os.Stat(path + infoSuffix); serr != nil {
		t.Errorf("the invalidation removed the whole sidecar: %v", serr)
	}
}

// TestASecondPullOverTheSameFileNeedsNoInvalidation is the other half of the
// pin: the invalidation is a one-off repair, and an attempt that runs again over
// the same file must take the ordinary path -- no clear, no rebuild -- because
// there is no watermark left to clear.
func TestASecondPullOverTheSameFileNeedsNoInvalidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "once.db")
	writeWAL(t, path, 4096, 216)
	writeInfo(t, path, staleInfo(452, 216))

	driver := &driverPull{applied: true}
	driver.refusals.Store(1)
	client := &Turso{path: path, db: driver}
	client.Reopen = func(context.Context) (syncDatabase, error) {
		driver.reopens.Add(1)
		return driver, nil
	}

	if _, err := client.Pull(t.Context()); err != nil {
		t.Fatalf("the first pull: %v", err)
	}
	firstReopens := driver.reopens.Load()

	// The same attempt again, with the driver no longer refusing: nothing about
	// the file changed, so nothing about the sidecar needs to.
	driver.refusals.Store(0)
	if _, err := client.Pull(t.Context()); err != nil {
		t.Fatalf("the second pull: %v", err)
	}
	if got := driver.reopens.Load(); got != firstReopens {
		t.Errorf("the second pull rebuilt the handle again (%d rebuilds, want %d)", got, firstReopens)
	}
}

// TestARefusalThatSurvivesTheInvalidationIsReported pins the retry's limit. The
// invalidation undoes a watermark the log cannot reach; a refusal that is not
// that, or one that survives it, has to reach the caller rather than become a
// loop.
func TestARefusalThatSurvivesTheInvalidationIsReported(t *testing.T) {
	t.Run("a different refusal is reported untouched", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "other.db")
		writeWAL(t, path, 4096, 216)
		writeInfo(t, path, staleInfo(452, 216))

		driver := &refusingPull{err: errors.New("wire: connection lost")}
		client := &Turso{path: path, db: driver}
		client.Reopen = func(context.Context) (syncDatabase, error) {
			t.Error("the client rebuilt its handle for a failure that is not a watermark")
			return nil, nil
		}
		_, err := client.Pull(t.Context())
		if err == nil || !errors.Is(err, driver.err) {
			t.Fatalf("the pull returned %v, want the driver's own refusal", err)
		}
		info, ierr := ReadInfoWatermark(path)
		if ierr != nil {
			t.Fatalf("ReadInfoWatermark: %v", ierr)
		}
		if !info.Present || info.Watermark != 452 {
			t.Error("a failure that is not a stale watermark cleared the watermark anyway")
		}
	})

	t.Run("a refusal that survives is reported, not retried again", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "survives.db")
		writeWAL(t, path, 4096, 216)
		writeInfo(t, path, staleInfo(452, 216))

		driver := &refusingPull{}
		client := &Turso{path: path, db: driver}
		var rebuilds int
		client.Reopen = func(context.Context) (syncDatabase, error) {
			rebuilds++
			return driver, nil
		}
		_, err := client.Pull(t.Context())
		if err == nil {
			t.Fatal("the pull reported success although every attempt refused")
		}
		if !strings.Contains(err.Error(), "after the stale revert watermark was cleared") {
			t.Errorf("the error does not say the invalidation did not help: %v", err)
		}
		if rebuilds != 1 {
			t.Errorf("the client rebuilt its handle %d times, want exactly 1", rebuilds)
		}
	})
}

// refusingPull refuses every pull when it carries no error of its own, which is
// how a fault that survives the invalidation looks from here.
type refusingPull struct {
	err error
}

func (r *refusingPull) Pull(context.Context) (bool, error) {
	if r.err != nil {
		return false, r.err
	}
	return false, errors.New("sync engine operation failed: " +
		"unable to checkpoint synced portion of WAL: result=1, watermark=452")
}

func (r *refusingPull) Push(context.Context) error { return nil }
func (r *refusingPull) Stats(context.Context) (turso.TursoSyncDbStats, error) {
	return turso.TursoSyncDbStats{}, nil
}
func (r *refusingPull) Checkpoint(context.Context) error { return nil }
