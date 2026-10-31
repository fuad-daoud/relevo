package ui

import (
	"bufio"
	"log/slog"
	"os"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
)

// maxStderrLine is the longest captured line: a runaway internal write must
// not grow the buffer without bound. Longer lines are truncated by the
// scanner rather than dropped.
const maxStderrLine = 1 << 20

// captureStderr redirects os.Stderr and the default slog logger into send,
// one stderrMsg per line, so no internal write can corrupt the screen.
//
// It returns the restore function: it closes the writer, waits for the
// scanner goroutine, and puts os.Stderr and the default logger back exactly
// as they were. The caller defers it, including on panic.
func captureStderr(send func(tea.Msg)) (restore func(), err error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}

	oldStderr := os.Stderr
	oldLogger := slog.Default()

	os.Stderr = w
	slog.SetDefault(slog.New(slog.NewTextHandler(w, nil)))

	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), maxStderrLine)
		for sc.Scan() {
			send(stderrMsg{line: sc.Text()})
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			_ = w.Close()
			<-done
			_ = r.Close()
			os.Stderr = oldStderr
			slog.SetDefault(oldLogger)
		})
	}, nil
}
