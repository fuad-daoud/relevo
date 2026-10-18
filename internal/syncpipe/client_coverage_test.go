package syncpipe

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
	"github.com/fuad-daoud/relevo/internal/syncworker"
)

// TestClientWriteErrorSurfacesAsFail pins that a write failure against a worker
// that is still considered running returns a write error rather than a dead-worker
// sentinel or hang. The failure trips the breaker's miss counter and kills the
// worker so the next call gets a fresh process.
func TestClientWriteErrorSurfacesAsFail(t *testing.T) {
	childIn, requests, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = childIn.Close() }()

	replies, childOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replies.Close() }()
	defer func() { _ = childOut.Close() }()

	var missed error
	waitErr := make(chan error, 1)
	waitErr <- nil
	c := &Client{
		cfg: Config{
			OnMiss: func(cause error) { missed = cause },
		},
		cmd:      &exec.Cmd{},
		requests: requests,
		replies:  bufio.NewReader(replies),
		waitErr:  waitErr,
		finished: make(chan struct{}),
	}

	// Close the request pipe before the call so the write fails with ErrClosed
	// while isGone still reports false.
	_ = requests.Close()

	_, err = c.Stats()
	if err == nil {
		t.Fatal("Stats over a closed request pipe = nil, want a write error")
	}
	if !strings.Contains(err.Error(), "write stats") {
		t.Errorf("Stats = %v, want write stats error", err)
	}
	if missed == nil {
		t.Error("write error did not trip OnMiss")
	}
}

// TestClientCancelledCallOverGoneWorker pins that a call issued over a worker that
// has already exited after being cancelled returns errCancelled rather than
// ErrGone, so the supervisor drops the dead process without counting a death.
func TestClientCancelledCallOverGoneWorker(t *testing.T) {
	finished := make(chan struct{})
	close(finished)
	waitErr := make(chan error, 1)
	waitErr <- nil

	c := &Client{
		cfg:      handshake(),
		finished: finished,
		waitErr:  waitErr,
	}
	c.cancelled.Store(true)

	_, err := c.Stats()
	if !errors.Is(err, errCancelled) {
		t.Fatalf("Stats over gone cancelled worker = %v, want %v", err, errCancelled)
	}
}

// TestClientCancelledFailDoesNotMiss pins that when a client is cancelled during
// a call failure, fail returns errCancelled and does not trip OnMiss.
func TestClientCancelledFailDoesNotMiss(t *testing.T) {
	var missed bool
	waitErr := make(chan error, 1)
	waitErr <- nil
	c := &Client{
		cfg: Config{
			OnMiss: func(error) { missed = true },
		},
		cmd:     &exec.Cmd{},
		waitErr: waitErr,
	}
	c.cancelled.Store(true)
	err := c.fail(errors.New("read failed"))
	if !errors.Is(err, errCancelled) {
		t.Fatalf("fail = %v, want %v", err, errCancelled)
	}
	if missed {
		t.Error("cancelled fail called OnMiss, want it skipped")
	}
}

// TestSupervisorCancelWithoutWorkerIsNoop pins that cancelling a supervisor that
// has no active worker does not panic or fail.
func TestSupervisorCancelWithoutWorkerIsNoop(t *testing.T) {
	s := newSupervisor(t, handshake(), breakerOver(newMemKV()))
	s.Cancel()
}

// TestClientDefaultBoundUsesCallBound pins that when no explicit timeout override
// is set on the configuration, the client uses the verb-specific bound.
func TestClientDefaultBoundUsesCallBound(t *testing.T) {
	c := &Client{}
	d, err := c.bound(syncworker.VerbStats)
	if err != nil {
		t.Fatalf("bound: %v", err)
	}
	want, _ := callBound(syncworker.VerbStats)
	if d != want {
		t.Fatalf("bound = %v, want %v", d, want)
	}
}

// TestRefusalErrorCarriesEpochRaceAndBlobRefused pins that the epoch race and
// blob refused error codes map to their respective sentinels.
func TestRefusalErrorCarriesEpochRaceAndBlobRefused(t *testing.T) {
	errEpoch := refusalError(syncworker.Response{Code: syncworker.CodeEpochRace, Error: "race"})
	if !errors.Is(errEpoch, relevosync.ErrEpochRace) {
		t.Fatalf("EpochRace = %v, want %v", errEpoch, relevosync.ErrEpochRace)
	}
	errBlob := refusalError(syncworker.Response{Code: syncworker.CodeBlobRefused, Error: "invalid"})
	if !errors.Is(errBlob, synclog.ErrInvalid) {
		t.Fatalf("BlobRefused = %v, want %v", errBlob, synclog.ErrInvalid)
	}
}

// TestClientDecodeReplyError pins that a corrupted reply payload surfaces as a
// decode error from readReply.
func TestClientDecodeReplyError(t *testing.T) {
	repliesR, repliesW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repliesR.Close() }()

	c := &Client{
		replies: bufio.NewReader(repliesR),
	}
	go func() {
		_, _ = repliesW.Write([]byte("not valid json\n"))
		_ = repliesW.Close()
	}()

	_, err = c.readReply(syncworker.VerbStats)
	if err == nil || !strings.Contains(err.Error(), "decode reply") {
		t.Fatalf("readReply = %v, want decode reply error", err)
	}
}
