//go:build unix

package db

import (
	"context"
	"path/filepath"
	"testing"
)

// TestReadOnlyQueryThroughTheOwnerLeavesWritesWorking pins that an ad-hoc read
// through the owner cannot poison the owner's pool: QueryReadOnly sets
// query_only on its connection, and the owner must never hand that engine
// connection to a later writer. Each write below runs on a fresh client
// connection, so it reaches whatever the owner's pool holds.
func TestReadOnlyQueryThroughTheOwnerLeavesWritesWorking(t *testing.T) {
	d, err := OpenWith(filepath.Join(t.TempDir(), "relevo.db"), Options{Origin: "01ORIGINORIGINORIGINORIGIN"})
	if err != nil {
		t.Fatalf("OpenWith: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	sock := startOwner(t, d)

	for i := 0; i < 3; i++ {
		reader := dialDB(t, sock)
		err := reader.QueryReadOnly(context.Background(), "SELECT 1", func([]string, []any) error { return nil })
		if err != nil {
			t.Fatalf("QueryReadOnly: %v", err)
		}
		_ = reader.Close()

		writer := dialDB(t, sock)
		if err := writer.KVPut("after.read", []byte(`{"n":1}`)); err != nil {
			t.Fatalf("write %d after an ad-hoc read: %v", i, err)
		}
		_ = writer.Close()
	}
}
