package relevo

// The pre-pass failure path, where the rule is the one releaseStagedWorker runs
// and the answer to scope it by is who opened the worker: clear the transport
// from the join publication before any close at all, and close only the worker
// this enable opened. The log underneath is a fleet's: the passphrase check
// needs the key material and the pre-pass needs another origin's entries, so
// the fake has to hold both for the failure to be the pre-pass's own.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/synclog"
)

// prepassBrokenLog is a fleet-shaped log whose range walk refuses: the shape of
// a remote the secrets pre-pass cannot read past its first page. It counts the
// closes any caller spends on it, because the failure path's answer turns on
// who may spend one.
type prepassBrokenLog struct {
	*synclog.MemTransport
	closes int
}

func (l *prepassBrokenLog) PullRange(origin string, after, upto int) ([]synclog.Entry, error) {
	return nil, errors.New("prepassBrokenLog: the range walk refuses")
}

func (l *prepassBrokenLog) Close() error {
	l.closes++
	return nil
}

// brokenFleetLog is a log that holds the passphrase check's key material and
// another origin's entries for the pre-pass to walk.
func brokenFleetLog(t *testing.T) *prepassBrokenLog {
	t.Helper()
	mem := synclog.NewMemTransport("m1")
	if _, err := mem.KeyInit(context.Background(), keyMetaFor(t, joinPassphrase)); err != nil {
		t.Fatalf("KeyInit: %v", err)
	}
	exportFleetR2(t, mem, joinPassphrase)
	return &prepassBrokenLog{MemTransport: mem}
}

func runJoinExpectRefused(t *testing.T, runner *VerbRunner) (message string) {
	t.Helper()
	res := runner.Run(context.Background(), joinVerb(t, joinPassphrase), []byte(joinToken+joinPassphrase))
	if res.OK {
		t.Fatal("a join whose pre-pass fails was accepted")
	}
	if !strings.Contains(res.Message, "prepass: pull") {
		t.Fatalf("the refusal is not the pre-pass pull: %s", res.Message)
	}
	return res.Message
}

// A worker the enable opened through its own opener is the enable's to answer
// for: the transport leaves the join publication first, so a disable arriving
// after the refusal finds nothing to drive, and then it is closed.
func TestAPrepassFailureClearsTheJoinPublicationBeforeClosingTheWorkerItOpened(t *testing.T) {
	t.Parallel()
	runner, _ := joinRunner(t)
	fake := brokenFleetLog(t)
	runner.OpenNoBucket = func(context.Context) (synclog.LogTransport, error) { return fake, nil }

	runJoinExpectRefused(t, runner)
	if got := runner.joinTransport(); got != nil {
		t.Errorf("the join publication still holds the refused transport: %v", got)
	}
	if fake.closes != 1 {
		t.Errorf("the worker the enable opened was closed %d times, want exactly 1", fake.closes)
	}
}

// A worker this enable did not open -- the runner's steady holder -- is not the
// enable's to close. It still has to leave the join publication, so the disable
// that arrives later drives a worker that is there.
func TestAPrepassFailureOnAWorkerTheEnableDidNotOpenClearsThePublicationWithoutClosingIt(t *testing.T) {
	t.Parallel()
	runner, _ := joinRunner(t)
	fake := brokenFleetLog(t)
	runner.Open = nil
	runner.Runner.Client = fake

	runJoinExpectRefused(t, runner)
	if got := runner.joinTransport(); got != nil {
		t.Errorf("the join publication still holds the refused transport: %v", got)
	}
	if fake.closes != 0 {
		t.Errorf("the worker the enable did not open was closed %d times, want 0", fake.closes)
	}
}
