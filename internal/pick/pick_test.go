package pick

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

type fakeFileInfo struct{ mode os.FileMode }

func (f fakeFileInfo) Name() string       { return "stdout" }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() os.FileMode  { return f.mode }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() interface{}   { return nil }

func TestRunRefusesWithoutATerminal(t *testing.T) {
	orig := stdoutStat
	stdoutStat = func() (os.FileInfo, error) { return fakeFileInfo{mode: 0}, nil }
	defer func() { stdoutStat = orig }()

	err := Run(context.Background(), relevo.Runtime{}, Options{Verb: VerbDone})
	want := "--pick needs a terminal; name the binding instead"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

// TestRunResult pins the exit mapping without starting a terminal program:
// a cancelled context is ErrCancelled (exit 1), any other program
// error passes through, and a clean exit returns the model's outcome.
func TestRunResult(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	other := errors.New("tty gone")

	cases := []struct {
		name    string
		ctx     context.Context
		runErr  error
		outcome error
		want    error
	}{
		{"cancelled ctx, killed", cancelled, tea.ErrProgramKilled, nil, ErrCancelled},
		{"cancelled ctx, interrupted", cancelled, tea.ErrInterrupted, nil, ErrCancelled},
		{"live ctx, other error", context.Background(), other, nil, other},
		{"clean exit, success", context.Background(), nil, nil, nil},
		{"clean exit, verb failed", context.Background(), nil, ErrVerbFailed, ErrVerbFailed},
		{"clean exit, nothing to pick", context.Background(), nil, ErrNothingToPick, ErrNothingToPick},
	}
	for _, c := range cases {
		got := runResult(c.ctx, c.runErr, Model{outcome: c.outcome})
		if !errors.Is(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
