//go:build unix

package owner

import (
	"context"
	"database/sql"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire/client"
)

// adHocBlobBytes is the value the ceiling test queries: large enough that the
// copies the ceiling prevents are visible in the heap, small enough not to
// strain a test run.
const adHocBlobBytes = 8 << 20

// adHocHeapBound is the live-heap growth the test allows while the ad-hoc read
// is refused. With the ceiling the owner holds the engine's value and one copy;
// without it, a batch copy and the peeked next row are added, roughly doubling
// the growth, so the bound sits between the two.
const adHocHeapBound = 24 << 20

// peakHeapDuring runs work and returns the largest live heap it observed above
// the heap before the run, sampled while work runs because the spike is
// released as soon as the read returns. The Go collector is lazy, so the
// sampled high-water mark still counts the allocations the work made.
func peakHeapDuring(t *testing.T, work func()) uint64 {
	t.Helper()
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	var peak uint64
	stop := make(chan struct{})
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		for {
			select {
			case <-stop:
				return
			default:
			}
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > peak {
				peak = m.HeapAlloc
			}
			time.Sleep(100 * time.Microsecond)
		}
	}()

	work()
	close(stop)
	<-sampled

	if peak <= before.HeapAlloc {
		return 0
	}
	return peak - before.HeapAlloc
}

// TestAdHocReadRefusesAValueOverTheCeiling pins the ad-hoc value bound: an
// ad-hoc read of a value over a shrunk ceiling is refused before the owner
// copies it into a batch or peeks the next row, the owner's heap growth stays
// under a bound, and a non-ad-hoc read of the same value is served.
func TestAdHocReadRefusesAValueOverTheCeiling(t *testing.T) {
	old := adHocValueCeiling
	adHocValueCeiling = 1 << 20
	t.Cleanup(func() { adHocValueCeiling = old })

	srv, sock := startServer(t)
	if _, err := srv.dbh.Exec(`CREATE TABLE t (n INTEGER)`); err != nil {
		t.Fatalf("create t: %v", err)
	}
	if _, err := srv.dbh.Exec(`INSERT INTO t (n) VALUES (1), (2), (3)`); err != nil {
		t.Fatalf("seed t: %v", err)
	}

	query := "SELECT zeroblob(" + strconv.Itoa(adHocBlobBytes) + ") FROM t"

	adhocDB := sql.OpenDB(client.Connector(sock, true))
	t.Cleanup(func() { _ = adhocDB.Close() })

	var readErr error
	growth := peakHeapDuring(t, func() {
		rows, err := adhocDB.QueryContext(context.Background(), query)
		if err != nil {
			readErr = err
			return
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
		}
		readErr = rows.Err()
	})
	t.Logf("ad-hoc heap growth = %d bytes, bound = %d", growth, adHocHeapBound)

	if growth > adHocHeapBound {
		t.Errorf("the owner's heap grew by %d bytes over the ceiling, want under %d", growth, adHocHeapBound)
	}
	if readErr == nil {
		t.Fatal("an ad-hoc read of a value over the ceiling was served")
	}
	if !strings.Contains(readErr.Error(), "ceiling") {
		t.Errorf("refusal = %v, want it to name the value ceiling", readErr)
	}

	// The same value on a connection without the ad-hoc marker is not capped:
	// relevo's own blobs must still flow. LIMIT 1 keeps the read to one row so
	// the handle is not abandoned mid-stream.
	plainDB, err := sql.Open(client.DriverName, sock)
	if err != nil {
		t.Fatalf("open plain: %v", err)
	}
	t.Cleanup(func() { _ = plainDB.Close() })
	var served []byte
	if err := plainDB.QueryRow(query + " LIMIT 1").Scan(&served); err != nil {
		t.Fatalf("a non-ad-hoc read of the same value = %v, want it served", err)
	}
	if len(served) != adHocBlobBytes {
		t.Errorf("the non-ad-hoc value is %d bytes, want %d", len(served), adHocBlobBytes)
	}
}
