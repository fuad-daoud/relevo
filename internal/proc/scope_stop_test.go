//go:build unix

package proc

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestStopScope pins StopScope against a stub systemctl: a unit that is already
// gone, one the stop ends, a stubborn one that needs the SIGKILL, and a host
// with no systemctl at all. No real unit is touched.
func TestStopScope(t *testing.T) {
	const unit = "relevo-round-abc12345-foo-1"
	const file = unit + ".scope"

	t.Run("already gone", func(t *testing.T) {
		dir := t.TempDir()
		_, logPath, _ := writeSystemctlStub(t, dir, "inactive")
		t.Setenv("PATH", dir)

		if err := New().StopScope(context.Background(), unit); err != nil {
			t.Fatalf("StopScope: %v", err)
		}
		if log := stubLog(t, logPath); strings.Contains(log, "kill") {
			t.Errorf("argv log = %q, want no kill", log)
		}
	})

	t.Run("stop ends the unit", func(t *testing.T) {
		dir := t.TempDir()
		_, logPath, _ := writeSystemctlStub(t, dir, "active")
		t.Setenv("PATH", dir)

		if err := New().StopScope(context.Background(), unit); err != nil {
			t.Fatalf("StopScope: %v", err)
		}
		log := stubLog(t, logPath)
		want := "--user stop --no-block " + file
		if n := strings.Count(log, want); n != 1 {
			t.Errorf("argv log = %q, want %q exactly once", log, want)
		}
		if strings.Contains(log, "kill") {
			t.Errorf("argv log = %q, want no kill", log)
		}
	})

	t.Run("stubborn unit", func(t *testing.T) {
		dir := t.TempDir()
		_, logPath, stubborn := writeSystemctlStub(t, dir, "active")
		if err := os.WriteFile(stubborn, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir)

		r := New()
		r.KillGrace = 50 * time.Millisecond
		err := r.StopScope(context.Background(), unit)
		if err == nil || !strings.Contains(err.Error(), unit) {
			t.Fatalf("StopScope = %v, want an error naming %s", err, unit)
		}
		if log := stubLog(t, logPath); !strings.Contains(log, "--user kill --kill-whom=all --signal=SIGKILL "+file) {
			t.Errorf("argv log = %q, want the SIGKILL to every process", log)
		}
	})

	t.Run("no systemctl", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		if err := New().StopScope(context.Background(), unit); err != nil {
			t.Fatalf("StopScope without systemctl = %v, want nil", err)
		}
	})
}
