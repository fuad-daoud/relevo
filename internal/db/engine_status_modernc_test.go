//go:build modernc

package db

import "testing"

// TestEngineStatusNeedsNoLibraryUnderModernc pins the modernc answer: this
// engine carries its library in the binary, so there is no path, nothing
// missing and no error.
func TestEngineStatusNeedsNoLibraryUnderModernc(t *testing.T) {
	status := EngineStatus(t.TempDir())
	if status.Name != "sqlite" {
		t.Errorf("name = %q, want sqlite", status.Name)
	}
	if status.Library != "" || status.Missing || status.Err != nil {
		t.Errorf("status = %+v, want no library, nothing missing, no error", status)
	}
}
