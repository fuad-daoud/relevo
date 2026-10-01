package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/doctor"
)

// TestEngineCheckWarnsOnAMissingLibrary pins the fresh-root row: no extracted
// library yet is a warning, not a failure.
func TestEngineCheckWarnsOnAMissingLibrary(t *testing.T) {
	c := engineCheck(db.EngineState{Name: "turso", CacheDir: "/root/turso-go", Missing: true})
	if c.Severity != doctor.SevWarn {
		t.Errorf("severity = %v, want warn", c.Severity)
	}
	if !strings.Contains(c.Detail, "/root/turso-go") {
		t.Errorf("detail %q must name the cache directory", c.Detail)
	}
	if c.Fix != "" {
		t.Errorf("fix = %q, want none for a missing library", c.Fix)
	}
}

// TestEngineCheckFailsOnAMismatchedLibrary pins the broken-library row: a
// loader refusal fails, and the fix says how to clear it.
func TestEngineCheckFailsOnAMismatchedLibrary(t *testing.T) {
	c := engineCheck(db.EngineState{
		Name:     "turso",
		Library:  "/root/turso-go/abcd1234/libturso_sync_sdk_kit.so",
		CacheDir: "/root/turso-go",
		Err:      errors.New("cached library file hash sum mismatch"),
	})
	if c.Severity != doctor.SevFail {
		t.Errorf("severity = %v, want fail", c.Severity)
	}
	if !strings.Contains(c.Fix, "remove /root/turso-go") {
		t.Errorf("fix = %q, want it to say remove /root/turso-go", c.Fix)
	}
	if !strings.Contains(c.Fix, "restart the daemon") {
		t.Errorf("fix = %q, want it to say restart the daemon", c.Fix)
	}
	if !strings.Contains(c.Detail, "/root/turso-go/abcd1234/libturso_sync_sdk_kit.so") {
		t.Errorf("detail %q must name the library", c.Detail)
	}
}
