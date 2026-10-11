//go:build unix

package db

import (
	"path/filepath"
	"testing"
)

// freedPair is a direct split pair whose shared file holds pages a delete
// freed, with the reclaim bounds lowered to the test's scale.
func freedPair(t *testing.T) *DB {
	t.Helper()
	minFree, maxLive := reclaimMinFreeBytes, reclaimMaxLiveBytes
	reclaimMinFreeBytes, reclaimMaxLiveBytes = 64<<10, 64<<20
	t.Cleanup(func() { reclaimMinFreeBytes, reclaimMaxLiveBytes = minFree, maxLive })

	path := filepath.Join(t.TempDir(), "relevo.db")
	shared := directOpen(t, path, Options{})
	local := directOpen(t, SplitPath(path), Options{})
	shared.local = local
	if _, err := shared.sqlDB.Exec(`CREATE TABLE filler (id INTEGER PRIMARY KEY, body BLOB)`); err != nil {
		t.Fatalf("create the filler table: %v", err)
	}
	body := make([]byte, 4096)
	for i := range 64 {
		body[0] = byte(i)
		if _, err := shared.sqlDB.Exec(`INSERT INTO filler (body) VALUES (?)`, body); err != nil {
			t.Fatalf("fill: %v", err)
		}
	}
	if _, err := shared.sqlDB.Exec(`DELETE FROM filler`); err != nil {
		t.Fatalf("empty the filler table: %v", err)
	}
	if err := shared.walCheckpoint(); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	return shared
}

func TestReclaimFreePagesCompactsAFileWithFreedPages(t *testing.T) {
	d := freedPair(t)
	before := fileSize(t, d.Path())

	got, err := ReclaimFreePages(d)
	if err != nil {
		t.Fatalf("ReclaimFreePages: %v", err)
	}
	if len(got) != 1 || got[0].Path != d.Path() {
		t.Fatalf("ReclaimFreePages compacted %+v, want only the shared file", got)
	}
	if after := fileSize(t, d.Path()); after >= before {
		t.Errorf("the file is %d bytes after the reclaim, want fewer than %d", after, before)
	}
	if got[0].BytesAfter >= got[0].BytesBefore {
		t.Errorf("reported %d -> %d bytes, want a shrink", got[0].BytesBefore, got[0].BytesAfter)
	}
	if _, _, err := d.KVGet("still-answers"); err != nil {
		t.Errorf("the handle stopped answering after the reclaim: %v", err)
	}

	again, err := ReclaimFreePages(d)
	if err != nil || len(again) != 0 {
		t.Errorf("a second reclaim = %+v, %v; want nothing left to compact", again, err)
	}
}

func TestReclaimFreePagesLeavesAFileWithTooMuchLiveData(t *testing.T) {
	d := freedPair(t)
	reclaimMaxLiveBytes = 4 << 10
	before := fileSize(t, d.Path())

	got, err := ReclaimFreePages(d)
	if err != nil || len(got) != 0 {
		t.Fatalf("ReclaimFreePages = %+v, %v; want nothing compacted", got, err)
	}
	if after := fileSize(t, d.Path()); after != before {
		t.Errorf("the file went from %d to %d bytes, want it untouched", before, after)
	}
}

func TestReclaimFreePagesLeavesASmallFreeList(t *testing.T) {
	d := freedPair(t)
	reclaimMinFreeBytes = 1 << 30

	if got, err := ReclaimFreePages(d); err != nil || len(got) != 0 {
		t.Fatalf("ReclaimFreePages = %+v, %v; want nothing compacted", got, err)
	}
}

func TestReclaimWorthItNeedsAQuarterOfTheFileFree(t *testing.T) {
	minFree := reclaimMinFreeBytes
	t.Cleanup(func() { reclaimMinFreeBytes = minFree })
	reclaimMinFreeBytes = 1
	if reclaimWorthIt(24, 100) {
		t.Error("24 free bytes of 100 is worth a reclaim, want not")
	}
	if !reclaimWorthIt(25, 100) {
		t.Error("25 free bytes of 100 is not worth a reclaim, want it")
	}
}

func TestReclaimFreePagesRefusesAServedHandle(t *testing.T) {
	d := freedPair(t)
	_ = NewOwner(d)
	if _, err := ReclaimFreePages(d); err == nil {
		t.Error("ReclaimFreePages compacted a served handle, want a refusal")
	}
}
