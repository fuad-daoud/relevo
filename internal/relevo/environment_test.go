package relevo

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

const testGB = 1 << 30

// fakeFree stands in for diskfree.Free: each path reads its own number, and
// a path listed in broken instead reports a read failure.
func fakeFree(free map[string]uint64, broken map[string]error) func(string) (uint64, error) {
	return func(path string) (uint64, error) {
		if err, ok := broken[path]; ok {
			return 0, err
		}
		return free[path], nil
	}
}

// TestCheckDiskFreeNamesFirstShortPath pins the refusal's exact text and that
// the first path under the minimum is the one named, not a later one.
func TestCheckDiskFreeNamesFirstShortPath(t *testing.T) {
	t.Parallel()

	free := fakeFree(
		map[string]uint64{"/tmp/a": 2 * testGB, "/tmp/b": 3 * testGB},
		nil,
	)
	err := CheckDiskFree([]string{"/tmp/a", "/tmp/b"}, 5*testGB, free)
	want := "environment: disk full: /tmp/a has 2.0 GB free (< 5 GB)"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if _, ok := err.(*EnvironmentError); !ok {
		t.Errorf("err = %T, want *EnvironmentError", err)
	}

	// A short path behind a roomy one is still named when reached.
	free = fakeFree(
		map[string]uint64{"/tmp/a": 8 * testGB, "/tmp/b": 3 * testGB},
		nil,
	)
	err = CheckDiskFree([]string{"/tmp/a", "/tmp/b"}, 5*testGB, free)
	want = "environment: disk full: /tmp/b has 3.0 GB free (< 5 GB)"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

// TestCheckDiskFreeAtThresholdPasses pins the boundary: exactly the minimum
// free passes, a byte under it refuses.
func TestCheckDiskFreeAtThresholdPasses(t *testing.T) {
	t.Parallel()

	free := fakeFree(map[string]uint64{"/tmp": 5 * testGB}, nil)
	if err := CheckDiskFree([]string{"/tmp"}, 5*testGB, free); err != nil {
		t.Errorf("at the boundary err = %v, want nil", err)
	}

	free = fakeFree(map[string]uint64{"/tmp": 5*testGB - 1}, nil)
	err := CheckDiskFree([]string{"/tmp"}, 5*testGB, free)
	want := "environment: disk full: /tmp has 5.0 GB free (< 5 GB)"
	if err == nil || err.Error() != want {
		t.Errorf("a byte under err = %v, want %q", err, want)
	}
}

// TestCheckDiskFreeZeroMinimumDisabled pins 0 as the off switch: even a
// path with nothing free passes, and no probe is issued at all — an off
// preflight costs nothing.
func TestCheckDiskFreeZeroMinimumDisabled(t *testing.T) {
	t.Parallel()

	probes := 0
	free := func(string) (uint64, error) {
		probes++
		return 0, nil
	}
	if err := CheckDiskFree([]string{"/tmp"}, 0, free); err != nil {
		t.Errorf("err = %v, want nil when the minimum is 0", err)
	}
	if probes != 0 {
		t.Errorf("a disabled preflight probed %d time(s), want none", probes)
	}
}

// TestCheckDiskFreeUnreadablePathSkipped pins the skip: a path whose free
// space cannot be read is passed over, and a round with room elsewhere still
// starts.
func TestCheckDiskFreeUnreadablePathSkipped(t *testing.T) {
	t.Parallel()

	free := fakeFree(
		map[string]uint64{"/tmp/b": 8 * testGB},
		map[string]error{"/tmp/a": fmt.Errorf("statfs: no such file")},
	)
	if err := CheckDiskFree([]string{"/tmp/a", "/tmp/b"}, 5*testGB, free); err != nil {
		t.Errorf("err = %v, want nil: the unreadable path must not fail the check", err)
	}

	// The skip is not a pass-through: a short path after the unreadable one
	// is still refused.
	free = fakeFree(
		map[string]uint64{"/tmp/b": testGB},
		map[string]error{"/tmp/a": fmt.Errorf("statfs: no such file")},
	)
	if err := CheckDiskFree([]string{"/tmp/a", "/tmp/b"}, 5*testGB, free); err == nil {
		t.Error("err = nil, want the short path behind the unreadable one refused")
	}
}

// TestEnvironmentErrorFoundByErrorsAs pins errors.As over an
// *EnvironmentError, directly and through a fmt.Errorf wrap, with the text
// carried either way.
func TestEnvironmentErrorFoundByErrorsAs(t *testing.T) {
	t.Parallel()

	free := fakeFree(map[string]uint64{"/tmp": testGB}, nil)
	err := CheckDiskFree([]string{"/tmp"}, 5*testGB, free)
	if err == nil {
		t.Fatal("err = nil, want a refusal")
	}
	want := "environment: disk full: /tmp has 1.0 GB free (< 5 GB)"
	if err.Error() != want {
		t.Errorf("text = %q, want %q", err.Error(), want)
	}

	var envErr *EnvironmentError
	if !errors.As(err, &envErr) {
		t.Fatalf("errors.As(err) = false, want the *EnvironmentError")
	}
	if !strings.HasPrefix(envErr.Error(), "environment: disk full: /tmp") {
		t.Errorf("found err = %q, want the environment prefix", envErr.Error())
	}

	wrapped := fmt.Errorf("round preflight: %w", err)
	envErr = nil
	if !errors.As(wrapped, &envErr) {
		t.Fatal("errors.As through fmt.Errorf wrap = false, want the *EnvironmentError")
	}
	if wrapped.Error() != "round preflight: "+want {
		t.Errorf("wrapped text = %q, want %q", wrapped.Error(), "round preflight: "+want)
	}
}
