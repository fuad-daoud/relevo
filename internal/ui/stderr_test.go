package ui

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TestCaptureStderr: writes to os.Stderr and to slog both arrive through
// send, restore puts os.Stderr and the default logger back.
func TestCaptureStderr(t *testing.T) {
	origStderr := os.Stderr
	origLogger := slog.Default()

	lines := make(chan string, 16)
	restore, err := captureStderr(func(m tea.Msg) {
		if sm, ok := m.(stderrMsg); ok {
			lines <- sm.line
		}
	})
	if err != nil {
		t.Fatalf("captureStderr: %v", err)
	}

	fmt.Fprintln(os.Stderr, "a direct write")
	slog.Info("a slog line")

	var got []string
	for len(got) < 2 {
		select {
		case l := <-lines:
			got = append(got, l)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for captured lines; got %q", got)
		}
	}
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "a direct write") {
		t.Errorf("os.Stderr's write must be captured, got %q", joined)
	}
	if !strings.Contains(joined, "a slog line") {
		t.Errorf("slog's write must be captured, got %q", joined)
	}

	restore()

	if os.Stderr != origStderr {
		t.Error("restore must put os.Stderr back")
	}
	if slog.Default() != origLogger {
		t.Error("restore must put the default logger back")
	}
}
