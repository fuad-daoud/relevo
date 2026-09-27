package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// seedLogStore saves a binding with n entries under the package's temp state
// root (TestMain moved XDG_STATE_HOME there), so `run` reads it back. The
// show --log path touches the store only, never a harness, so this is safe
// where none exists.
func seedLogStore(t *testing.T, name string, state store.State, n int) *store.Store {
	t.Helper()
	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	s := store.New(root)
	b := store.Binding{Name: name, CWD: t.TempDir(), Round: 1, State: state}
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	for i := 0; i < n; i++ {
		if err := s.AppendLog(name, store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true}); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}
	return s
}

func TestLogAfterAndJSON(t *testing.T) {
	const name = "logcmd"
	s := seedLogStore(t, name, store.StateActive, 3)

	stdout, _, err := captureOutput(t, func() error {
		return run([]string{"show", name, "--log", "--after", "1", "--json"})
	})
	if err != nil {
		t.Fatalf("run show --log --after 1 --json: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(stdout), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2:\n%s", len(lines), stdout)
	}
	for i, line := range lines {
		var e store.LogEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("line %d is not JSON: %v: %s", i, err, line)
		}
		if want := i + 2; e.Seq != want {
			t.Errorf("line %d seq = %d, want %d", i, e.Seq, want)
		}
	}

	// Plain output is one LogLine per entry, unchanged from before the flags.
	wantEntries, err := s.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var want strings.Builder
	for _, e := range wantEntries {
		want.WriteString(relevo.LogLine(e))
		want.WriteString("\n")
	}
	stdout, _, err = captureOutput(t, func() error {
		return run([]string{"show", name, "--log"})
	})
	if err != nil {
		t.Fatalf("run show --log: %v", err)
	}
	if string(stdout) != want.String() {
		t.Errorf("plain log output:\n%s\nwant:\n%s", stdout, want.String())
	}

	// A negative --after is a usage error: exit code 2.
	_, _, err = captureOutput(t, func() error {
		return run([]string{"show", name, "--log", "--after", "-1"})
	})
	var ec exitCodeErr
	if !errors.As(err, &ec) || ec.code != 2 {
		t.Fatalf("run show --log --after -1 = %v, want exit code 2", err)
	}
}

func TestLogFollowStopsOnDone(t *testing.T) {
	const name = "logdone"
	seedLogStore(t, name, store.StateDone, 2)

	done := make(chan error, 1)
	timedOut := errors.New("log --follow did not return")
	stdout, _, err := captureOutput(t, func() error {
		go func() { done <- run([]string{"show", name, "--log", "--follow"}) }()
		select {
		case e := <-done:
			return e
		case <-time.After(2 * time.Second):
			return timedOut
		}
	})
	if errors.Is(err, timedOut) {
		t.Fatal("relevo show --log --follow did not return for a binding already DONE")
	}
	if err != nil {
		t.Fatalf("run show --log --follow: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(stdout), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2:\n%s", len(lines), stdout)
	}
}
