package syncpipe

import "testing"

// The range read and the compaction cross the pipe as the daemon's own request:
// a range names its origin, and a compaction names only a floor because the
// worker compacts its own log.
func TestClientPullRangeAndCompactOverAPipe(t *testing.T) {
	c, err := startFakeWorker(t, modeNormal, handshake())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	entries, err := c.PullRange("origin-b", 4, 9)
	if err != nil || len(entries) != 1 || entries[0].Origin != "origin-b" || entries[0].Seq != 5 {
		t.Fatalf("PullRange = %+v, %v; want the worker's entry after seq 4", entries, err)
	}
	res, err := c.CompactOwn(7)
	if err != nil || res.Dropped != 6 || res.Floor != 7 {
		t.Fatalf("CompactOwn = %+v, %v; want the worker's dropped count and floor", res, err)
	}
}

func TestSupervisorDrivesPullRangeAndCompact(t *testing.T) {
	s := newSupervisor(t, fakeWorkerCfg(modeNormal), breakerOver(newMemKV()))
	if entries, err := s.PullRange("origin-b", 0, 3); err != nil || len(entries) != 1 {
		t.Fatalf("PullRange = %+v, %v", entries, err)
	}
	if res, err := s.CompactOwn(3); err != nil || res.Floor != 3 {
		t.Fatalf("CompactOwn = %+v, %v", res, err)
	}
}
