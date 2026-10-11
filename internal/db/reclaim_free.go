package db

import (
	"context"
	"fmt"
)

// The bounds a start reclaims under. The copy a compaction makes costs time in
// the live pages, and a client spends that time waiting on a daemon that has
// not started serving, so a file whose live pages pass reclaimMaxLiveBytes is
// left as it is: its free pages are still reused by the next inserts. Below
// reclaimMinFreeBytes, or a quarter of the file, a compaction returns too
// little to be worth the wait. They are vars so a test can reach them with a
// small file.
var (
	reclaimMinFreeBytes int64 = 64 << 20
	reclaimMaxLiveBytes int64 = 1 << 30
)

// ReclaimedFile is one file a reclaim compacted.
type ReclaimedFile struct {
	Path        string
	BytesBefore int64
	BytesAfter  int64
}

// ReclaimFreePages compacts each file of d's pair whose free list holds pages
// worth returning to the filesystem. It is the compaction the one-time passes
// skip on a served handle, so it runs before the owner serves d; on a served
// handle Vacuum refuses and this returns that refusal.
func ReclaimFreePages(d *DB) ([]ReclaimedFile, error) {
	if d == nil {
		return nil, nil
	}
	files := []*DB{d}
	if l := d.Local(); l != nil && l != d {
		files = append(files, l)
	}
	var out []ReclaimedFile
	for _, f := range files {
		free, total, err := f.freeAndTotalBytes()
		if err != nil {
			return out, err
		}
		if !reclaimWorthIt(free, total) {
			continue
		}
		if err := f.Vacuum(); err != nil {
			return out, fmt.Errorf("db: reclaim %s: %w", f.path, err)
		}
		_, after, err := f.freeAndTotalBytes()
		if err != nil {
			return out, err
		}
		out = append(out, ReclaimedFile{Path: f.path, BytesBefore: total, BytesAfter: after})
	}
	return out, nil
}

func reclaimWorthIt(free, total int64) bool {
	return free >= reclaimMinFreeBytes && free*4 >= total && total-free <= reclaimMaxLiveBytes
}

// freeAndTotalBytes is the file's free-list size and its whole size, in bytes.
func (d *DB) freeAndTotalBytes() (free, total int64, err error) {
	var pageSize, pages, freePages int64
	for _, p := range []struct {
		name string
		dest *int64
	}{{"page_size", &pageSize}, {"page_count", &pages}, {"freelist_count", &freePages}} {
		if err := d.sqlDB.QueryRowContext(context.Background(), "PRAGMA "+p.name).Scan(p.dest); err != nil {
			return 0, 0, fmt.Errorf("db: %s of %s: %w", p.name, d.path, mapBusy(err))
		}
	}
	return freePages * pageSize, pages * pageSize, nil
}
