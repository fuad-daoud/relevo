package syncpipe

import "testing"

// The range read crosses the pipe as the daemon's own request: a range names
// its origin.
func TestClientPullRangeOverAPipe(t *testing.T) {
	c, err := startFakeWorker(t, modeNormal, handshake())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	entries, err := c.PullRange("origin-b", 4, 9)
	if err != nil || len(entries) != 1 || entries[0].Origin != "origin-b" || entries[0].Seq != 5 {
		t.Fatalf("PullRange = %+v, %v; want the worker's entry after seq 4", entries, err)
	}
}

func TestSupervisorDrivesPullRange(t *testing.T) {
	s := newSupervisor(t, fakeWorkerCfg(modeNormal), breakerOver(newMemKV()))
	if entries, err := s.PullRange("origin-b", 0, 3); err != nil || len(entries) != 1 {
		t.Fatalf("PullRange = %+v, %v", entries, err)
	}
}
