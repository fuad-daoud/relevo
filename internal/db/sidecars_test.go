package db

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// TestLogSidecarsNamesEveryField pins the WAL-reset log's shape: one line per
// reset site naming the event, the file, the process and engine that reset it,
// and both sidecar sizes, with -1 for a sidecar that is absent rather than
// empty.
func TestLogSidecarsNamesEveryField(t *testing.T) {
	dir := t.TempDir()

	present := filepath.Join(dir, "present.db")
	if err := os.WriteFile(present+"-wal", []byte("1234567"), 0o600); err != nil {
		t.Fatalf("write -wal: %v", err)
	}

	missing := filepath.Join(dir, "missing.db")

	for _, tc := range []struct {
		name     string
		path     string
		walBytes float64
		shmBytes float64
	}{
		{"seven-byte WAL and no shm", present, 7, -1},
		{"absent sidecars", missing, -1, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logSidecars(slog.New(slog.NewJSONHandler(&buf, nil)), "test-event", tc.path)
			var fields map[string]any
			if err := json.Unmarshal(buf.Bytes(), &fields); err != nil {
				t.Fatalf("unmarshal log line %q: %v", buf.String(), err)
			}
			if fields["msg"] != "wal sidecars" {
				t.Errorf("msg = %v, want the wal sidecars line", fields["msg"])
			}
			if fields["event"] != "test-event" {
				t.Errorf("event = %v, want test-event", fields["event"])
			}
			if fields["path"] != tc.path {
				t.Errorf("path = %v, want %s", fields["path"], tc.path)
			}
			if fields["pid"] != float64(os.Getpid()) {
				t.Errorf("pid = %v, want this process", fields["pid"])
			}
			if fields["engine"] != engineName {
				t.Errorf("engine = %v, want this build's %s", fields["engine"], engineName)
			}
			if fields["wal_bytes"] != tc.walBytes {
				t.Errorf("wal_bytes = %v, want %v", fields["wal_bytes"], tc.walBytes)
			}
			if fields["shm_bytes"] != tc.shmBytes {
				t.Errorf("shm_bytes = %v, want %v", fields["shm_bytes"], tc.shmBytes)
			}
		})
	}
}
