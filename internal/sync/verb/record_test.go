package verb

import (
	"context"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/synclog"
)

// recordTransport is the log transport a fixture hands a runner: it answers
// every call with a zero, so a test can look at what was driven through it. It
// keeps what was appended as well as that it was, so a case about what an export
// handed over does not have to read the outbox the export has already cleared.
//
// It duplicates the same-named fake in internal/relevo's tick tests: a test
// fake cannot be imported across packages, and both the tick tests and the verb
// tests drive runners through recorded calls.
type recordTransport struct {
	Calls []string
	// Appended is every entry an export handed to the log, in order.
	Appended []synclog.Entry
}

func (r *recordTransport) Append(e []synclog.Entry) ([]synclog.Entry, error) {
	r.Calls = append(r.Calls, "append")
	r.Appended = append(r.Appended, e...)
	return nil, nil
}

func (r *recordTransport) Pull(map[string]int) ([]synclog.Entry, error) {
	r.Calls = append(r.Calls, "pull")
	return nil, nil
}

func (r *recordTransport) Head(string) ([]synclog.HeadRow, error) {
	r.Calls = append(r.Calls, "head")
	return nil, nil
}

func (r *recordTransport) Stats() (synclog.Stats, error) {
	r.Calls = append(r.Calls, "stats")
	return synclog.Stats{}, nil
}

func (r *recordTransport) PublishMarks(map[string]int, time.Time) error {
	r.Calls = append(r.Calls, "publish_marks")
	return nil
}

func (r *recordTransport) ReadMarks() (synclog.MarkState, error) {
	r.Calls = append(r.Calls, "read_marks")
	return synclog.MarkState{}, nil
}

func (r *recordTransport) PullRange(string, int, int) ([]synclog.Entry, error) { return nil, nil }

func (r *recordTransport) CompactOwn(int) (synclog.CompactResult, error) {
	return synclog.CompactResult{}, nil
}

func (r *recordTransport) KeyMeta(context.Context) (synclog.KeyMeta, error) {
	return synclog.KeyMeta{}, nil
}

func (r *recordTransport) KeyInit(context.Context, synclog.KeyMeta) (synclog.KeyMeta, error) {
	return synclog.KeyMeta{}, nil
}

func (r *recordTransport) KeyRekey(context.Context, synclog.KeyRekeySpec) (synclog.KeyMeta, error) {
	return synclog.KeyMeta{}, nil
}

func (r *recordTransport) PublishEpochCut(context.Context, synclog.EpochCut) error { return nil }

func (r *recordTransport) ClaimIdentity(context.Context, string, string, time.Time) (string, time.Time, error) {
	return "", time.Time{}, nil
}

var _ synclog.LogTransport = (*recordTransport)(nil)

// TestRecordTransportAnswersEveryCallWithAZero pins the fake the verb fixtures
// run on: every call answers without reaching a network, so a test that drives
// a runner through it fails on the runner rather than on the fake.
func TestRecordTransportAnswersEveryCallWithAZero(t *testing.T) {
	r := &recordTransport{}
	ctx := context.Background()
	if _, err := r.Append(nil); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := r.Pull(nil); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if _, err := r.Head("m1"); err != nil {
		t.Fatalf("Head: %v", err)
	}
	if _, err := r.Stats(); err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if err := r.PublishMarks(nil, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("PublishMarks: %v", err)
	}
	if _, err := r.ReadMarks(); err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	if _, err := r.PullRange("m1", 0, 0); err != nil {
		t.Fatalf("PullRange: %v", err)
	}
	if _, err := r.KeyMeta(ctx); err != nil {
		t.Fatalf("KeyMeta: %v", err)
	}
}
