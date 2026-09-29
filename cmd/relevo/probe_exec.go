package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/fuad-daoud/relevo/internal/proc"
)

// probeWaitDelay is how long Wait waits for stdout/stderr to close after the
// process exits or is killed. It bounds a harness that leaves a descendant
// holding the stdout pipe open.
const probeWaitDelay = 5 * time.Second

// lineWriter is an io.Writer that splits its input into lines and hands each
// complete line to onLine, without the trailing newline. It gives exec a
// writer to copy stdout into, so Wait owns the copy and can bound it with
// WaitDelay.
type lineWriter struct {
	onLine func([]byte) // called once per complete line; may be nil
	buf    []byte       // bytes of a line not yet terminated
}

// Write appends p to buf and calls onLine for every complete line now in buf.
// A \r right before the \n is dropped as well. onLine must not retain the
// slice.
func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := w.buf[:i]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		if w.onLine != nil {
			w.onLine(line)
		}
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

// Flush emits a final line with no trailing newline, if any.
func (w *lineWriter) Flush() {
	if len(w.buf) == 0 {
		return
	}
	if w.onLine != nil {
		w.onLine(w.buf)
	}
	w.buf = w.buf[:0]
}

// lineExec is availability.LineExec's production implementation: one harness
// invocation, every stdout line handed to onLine as it arrives.
type lineExec struct{}

// Run starts argv in dir with the parent environment minus proc.DeniedEnv,
// calls onLine for every stdout line, and returns a non-zero exit as an error
// whose text ends with the last 300 bytes of stderr.
func (lineExec) Run(ctx context.Context, dir string, argv []string, onLine func(line []byte)) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = proc.ChildEnv(os.Environ(), proc.DeniedEnv, nil)
	lw := &lineWriter{onLine: onLine}
	cmd.Stdout = lw
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	cmd.WaitDelay = probeWaitDelay
	// Run returns only after exec's stdout copy goroutine has finished, so
	// the caller's reads after Run are ordered after every onLine call: no lock.
	err := cmd.Run()
	lw.Flush()
	if errors.Is(err, exec.ErrWaitDelay) {
		// The process itself exited 0; only a descendant held the pipe open,
		// so the probe's answer is valid.
		err = nil
	}
	if err != nil {
		tail := stderr.Bytes()
		if len(tail) > 300 {
			tail = tail[len(tail)-300:]
		}
		return fmt.Errorf("%w: %s", err, tail)
	}
	return nil
}
