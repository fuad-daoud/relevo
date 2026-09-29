package hooks

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// hookOutputCap bounds a run's combined output to its first 64 KiB.
const hookOutputCap = 64 << 10

// Executor runs one configured argv with the event injected as environment
// variables.
type Executor interface {
	Execute(ctx context.Context, argv []string, event Event) error
}

// OSExecutor runs argv lists as OS processes, recording every run in Log.
type OSExecutor struct {
	Log RunLog
}

func NewOSExecutor(log RunLog) *OSExecutor { return &OSExecutor{Log: log} }

// Execute runs argv[0] with argv[1:] and the event as RELEVO_* environment
// variables, then records one HookRun. An empty argv is recorded and skipped.
func (e *OSExecutor) Execute(ctx context.Context, argv []string, event Event) error {
	if len(argv) == 0 {
		e.appendRun(HookRun{
			At:     time.Now().UTC(),
			Event:  string(event.Type),
			Output: fmt.Sprintf("hook: empty argv for %s: skipped\n", event.Type),
		})
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(),
		"RELEVO_EVENT="+string(event.Type),
		"RELEVO_BINDING="+event.BindingID,
		"RELEVO_STATE="+event.State,
		"RELEVO_OLD_STATE="+event.OldState,
		"RELEVO_ROUND="+strconv.Itoa(event.Round),
	)

	out := newCappedBuffer(hookOutputCap)
	cmd.Stdout = out
	cmd.Stderr = out

	err := cmd.Run()

	run := HookRun{
		At:       time.Now().UTC(),
		Event:    string(event.Type),
		Argv:     append([]string(nil), argv...),
		ExitCode: exitCodeOf(cmd),
		Output:   out.String(),
	}
	if err != nil {
		run.Error = err.Error()
	}
	e.appendRun(run)
	return err
}

// appendRun drops any Append error: the run log is best-effort and must
// never change a hook's outcome.
func (e *OSExecutor) appendRun(run HookRun) {
	if e.Log == nil {
		return
	}
	_ = e.Log.Append(run)
}

// exitCodeOf is cmd's exit status, or -1 when no process ran to completion.
func exitCodeOf(cmd *exec.Cmd) int {
	if cmd.ProcessState == nil {
		return -1
	}
	return cmd.ProcessState.ExitCode()
}

// cappedBuffer collects output up to limit bytes, dropping the rest.
type cappedBuffer struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	limit int
}

func newCappedBuffer(limit int) *cappedBuffer { return &cappedBuffer{limit: limit} }

// Write always reports the full slice written, even past the limit, so
// os/exec never sees a short-write error the child did not cause.
func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.limit - b.buf.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		b.buf.Write(p)
	}
	return n, nil
}

func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
