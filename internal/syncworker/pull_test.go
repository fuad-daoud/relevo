package syncworker

import (
	"errors"
	"testing"
)

func appendRow(t *testing.T, b *TursoBackend, pk string) {
	t.Helper()
	if _, err := b.Append([]Entry{upsertEntry("origin-a", "board", pk, `{"title":"t"}`)}); err != nil {
		t.Fatalf("append %s: %v", pk, err)
	}
}

func pullBackend(t *testing.T) (*TursoBackend, *localDriver, string) {
	t.Helper()
	path := tempReplica(t)
	driver := &localDriver{path: path}
	return backendFor(t, driver, "origin-a"), driver, path
}

func TestPullRangeVerbOverThePipe(t *testing.T) {
	b, _, _ := pullBackend(t)
	appendRow(t, b, `["a"]`)
	appendRow(t, b, `["b"]`)
	appendRow(t, b, `["a"]`)
	s := startWorker(t, b)
	s.askHello()

	resp := s.ask(Request{ID: "r", Verb: VerbPullRange, Origin: "origin-a", AfterSeq: 1, Upto: 2})
	if !resp.OK || len(resp.Entries) != 1 || resp.Entries[0].Seq != 2 {
		t.Fatalf("pull_range = %+v, want only seq 2", resp)
	}
	if resp := s.ask(Request{ID: "bad", Verb: VerbPullRange, Origin: "origin-a", AfterSeq: 3, Upto: 3}); resp.OK {
		t.Fatalf("an empty range was answered: %+v", resp)
	}
}

func TestPullRangeBeforeHelloIsRefused(t *testing.T) {
	b := NewTursoBackend(&localDriver{path: tempReplica(t)})
	if _, err := b.PullRange("o", 0, 1); !errors.Is(err, ErrNoHello) {
		t.Errorf("PullRange = %v, want ErrNoHello", err)
	}
}

func TestPullRangeVerbAnswersABackendRefusal(t *testing.T) {
	fb := &fakeBackend{}
	s := startWorker(t, fb)
	s.askHello()
	fb.mu.Lock()
	fb.fail = errors.New("the replica is gone")
	fb.mu.Unlock()
	if resp := s.ask(Request{ID: "r", Verb: VerbPullRange, Origin: "o", AfterSeq: 0, Upto: 1}); resp.OK {
		t.Errorf("pull_range = %+v, want the refusal", resp)
	}
}

func TestPullRangeReportsAMissingLog(t *testing.T) {
	b, _, path := pullBackend(t)
	appendRow(t, b, `["a"]`)
	if err := execOnErr(t, path, "DROP TABLE log"); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := b.PullRange("origin-a", 0, 1); err == nil {
		t.Error("PullRange succeeded without a log table")
	}
}
