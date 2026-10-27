package syncpipe

// The client's traffic half over the same real pipe the marks tests use: what
// is pinned is that the row crosses whole and the answer comes back as every
// origin's row, and that a refusal reaches the caller as one.

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/synclog"
)

var pipeTrafficRow = synclog.TrafficRow{Month: "2026-10", Push: 3, Pull: 4, At: pinnedAt}

func TestClientPublishesTrafficOverAPipe(t *testing.T) {
	c, err := startFakeWorker(t, modeNormal, handshake())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	rows, err := c.PublishTraffic(pipeTrafficRow)
	if err != nil {
		t.Fatalf("PublishTraffic: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("PublishTraffic answered %d rows, want this worker's and the other machine's: %+v", len(rows), rows)
	}
	own := rows["origin-a"]
	if own.Month != "2026-10" || own.Push != 3 || own.Pull != 4 || !own.At.Equal(pinnedAt) {
		t.Errorf("own row = %+v, want the row the daemon handed over", own)
	}
	if other := rows["origin-b"]; other.Push != 5 || other.Pull != 6 {
		t.Errorf("other row = %+v, want the worker's other origin", other)
	}
}

// TestClientTrafficRefusedByTheWorker pins that a refused publish is an error
// rather than an empty account, which would read as this machine alone.
func TestClientTrafficRefusedByTheWorker(t *testing.T) {
	c, err := startFakeWorker(t, modeRefuseVerbs, handshake())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if rows, err := c.PublishTraffic(pipeTrafficRow); err == nil {
		t.Errorf("a refused publish came back as rows %+v", rows)
	}
}

// TestSupervisorDrivesPublishTraffic pins that the supervisor the daemon holds
// reaches the worker, and that a refusal settles the breaker as a refusal
// rather than a death: the worker is alive and in step.
func TestSupervisorDrivesPublishTraffic(t *testing.T) {
	s := newSupervisor(t, fakeWorkerCfg(modeNormal), breakerOver(newMemKV()))
	rows, err := s.PublishTraffic(pipeTrafficRow)
	if err != nil {
		t.Fatalf("PublishTraffic: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("PublishTraffic answered %+v, want two rows", rows)
	}

	b := breakerOver(newMemKV())
	refusing := newSupervisor(t, fakeWorkerCfg(modeRefuseVerbs), b)
	if _, err := refusing.PublishTraffic(pipeTrafficRow); err == nil {
		t.Fatal("a refused publish came back as a success")
	}
	if deaths, _ := b.Deaths(); deaths != 0 {
		t.Errorf("a refused publish counted %d deaths, want none", deaths)
	}
}
