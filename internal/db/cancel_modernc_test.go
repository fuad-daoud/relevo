//go:build modernc

package db

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestCancelInterruptsARunningStatement pins that the modernc engine interrupts
// a running statement: the driver's own sqlite3_interrupt stops the query, so the
// caller gets its context error without waiting for the statement.
func TestCancelInterruptsARunningStatement(t *testing.T) {
	d := openTestDB(t)
	sock := startOwner(t, d)
	d2 := dialDB(t, sock)

	const heavy = `WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c WHERE x < 200000000) SELECT count(*) FROM c`
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := d2.sqlDB.ExecContext(ctx, heavy)
		done <- err
	}()

	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the statement returned success despite cancellation")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the statement did not stop after cancellation")
	}
}
