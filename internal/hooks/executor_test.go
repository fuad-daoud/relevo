package hooks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// runLogFake is a RunLog safe to read after the executor returned.
type runLogFake struct {
	mu   sync.Mutex
	runs []HookRun
}

func (f *runLogFake) Append(run HookRun) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, run)
	return nil
}

func (f *runLogFake) all() []HookRun {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]HookRun(nil), f.runs...)
}

// only is the single recorded run; it fails the test otherwise.
func (f *runLogFake) only(t *testing.T) HookRun {
	t.Helper()
	runs := f.all()
	if len(runs) != 1 {
		t.Fatalf("run log holds %d runs, want exactly 1", len(runs))
	}
	return runs[0]
}

func TestOSExecutorExecute_EmptyArgvSkipped(t *testing.T) {
	log := &runLogFake{}
	executor := NewOSExecutor(log)

	if err := executor.Execute(context.Background(), nil, Event{Type: EventStateChanged}); err != nil {
		t.Fatalf("Execute with an empty argv returned %v, want nil", err)
	}

	run := log.only(t)
	if !strings.Contains(run.Output, "empty argv") {
		t.Errorf("run output missing the empty-argv note, got:\n%s", run.Output)
	}
	if run.Event != string(EventStateChanged) {
		t.Errorf("event = %q, want %q", run.Event, EventStateChanged)
	}
}

func TestOSExecutorExecute_EnvAndLogging(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "test_env.sh")
	scriptContent := `#!/bin/sh
echo "EVENT=$RELEVO_EVENT"
echo "BINDING=$RELEVO_BINDING"
echo "STATE=$RELEVO_STATE"
echo "OLD_STATE=$RELEVO_OLD_STATE"
echo "ROUND=$RELEVO_ROUND"
`
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if err := os.Chmod(scriptPath, 0755); err != nil {
		t.Fatalf("Chmod failed: %v", err)
	}

	log := &runLogFake{}
	executor := NewOSExecutor(log)

	event := Event{
		Type:      EventStateChanged,
		BindingID: "bind-abc",
		State:     "working",
		OldState:  "idle",
		Round:     3,
		Timestamp: time.Now().UTC(),
	}

	err := executor.Execute(context.Background(), []string{scriptPath}, event)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	run := log.only(t)
	content := run.Output

	expectedStrings := []string{
		"EVENT=state_changed",
		"BINDING=bind-abc",
		"STATE=working",
		"OLD_STATE=idle",
		"ROUND=3",
	}
	for _, expected := range expectedStrings {
		if !strings.Contains(content, expected) {
			t.Errorf("log output missing %q, got:\n%s", expected, content)
		}
	}
	if run.Error != "" || run.ExitCode != 0 {
		t.Errorf("a successful run = error %q exit %d, want neither", run.Error, run.ExitCode)
	}
	if len(run.Argv) != 1 || run.Argv[0] != scriptPath {
		t.Errorf("argv = %q, want [%s]", run.Argv, scriptPath)
	}
}

func TestOSExecutorExecute_FailureLogging(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "fail_script.sh")
	scriptContent := `#!/bin/sh
exit 2
`
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if err := os.Chmod(scriptPath, 0755); err != nil {
		t.Fatalf("Chmod failed: %v", err)
	}

	log := &runLogFake{}
	executor := NewOSExecutor(log)

	event := Event{
		Type:      EventStateChanged,
		BindingID: "bind-xyz",
		State:     "error",
		OldState:  "working",
		Round:     1,
		Timestamp: time.Now().UTC(),
	}

	err := executor.Execute(context.Background(), []string{scriptPath}, event)
	if err == nil {
		t.Fatal("Execute expected error, got nil")
	}

	run := log.only(t)
	if !strings.Contains(run.Error, "exit status 2") {
		t.Errorf("recorded error = %q, want it to carry exit status 2", run.Error)
	}
	if run.ExitCode != 2 {
		t.Errorf("exit code = %d, want 2", run.ExitCode)
	}
}

func TestOSExecutorExecute_Timeout(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "sleep_script.sh")
	scriptContent := `#!/bin/sh
sleep 10
`
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if err := os.Chmod(scriptPath, 0755); err != nil {
		t.Fatalf("Chmod failed: %v", err)
	}

	log := &runLogFake{}
	executor := NewOSExecutor(log)

	event := Event{
		Type:      EventRoundStarted,
		BindingID: "bind-timeout",
		Round:     1,
		Timestamp: time.Now().UTC(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := executor.Execute(ctx, []string{scriptPath}, event)
	if err == nil {
		t.Fatal("Execute expected timeout error, got nil")
	}

	if run := log.only(t); run.Error == "" {
		t.Error("a timed-out run must record its error")
	}
}
