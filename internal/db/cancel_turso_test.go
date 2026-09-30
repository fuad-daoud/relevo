//go:build !modernc

package db

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestCancelReturnsToTheClientWhileTheEngineFinishes pins the Turso cancel
// contract: tursogo v0.8.1 exposes no statement interrupt, so the owner runs the
// statement to completion. The client must not wait for it -- it returns the
// caller's context error after a short grace, discards that session, and a later
// query opens a fresh connection -- while the owner finishes the statement and
// then discards the connection it ran on, returning the live count to baseline.
func TestCancelReturnsToTheClientWhileTheEngineFinishes(t *testing.T) {
	d := openTestDB(t)
	execOn(t, d, `CREATE TABLE t (n INTEGER)`)
	sock := startOwner(t, d)
	d2 := dialDB(t, sock)

	baseline := ownerConns(t, sock)

	// Sized to outlast the client's grace by seconds, so the owner is still
	// running it when the client has already returned.
	const heavy = `WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c WHERE x < 2000000) SELECT count(*) FROM c`
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
	case <-time.After(time.Second):
		t.Fatal("the cancelled statement did not return to the client within 1 s")
	}

	// The owner still holds the abandoned connection: the engine is finishing
	// the statement, which is why the client could not wait for a reply.
	if n := ownerConns(t, sock); n <= baseline {
		t.Errorf("owner live connections = %d, want above baseline %d while the statement runs", n, baseline)
	}

	// The session is gone, but the handle still works: a fresh connection
	// serves a new query.
	var one int
	if err := d2.sqlDB.QueryRow(`SELECT 1`).Scan(&one); err != nil {
		t.Fatalf("query after cancel: %v", err)
	}
	if one != 1 {
		t.Errorf("query after cancel = %d, want 1", one)
	}

	// Once the statement ends the owner discards that connection, so its live
	// count falls back to the baseline instead of leaking a slot.
	if err := d2.Close(); err != nil {
		t.Fatalf("close dialled handle: %v", err)
	}
	waitOwnerConns(t, sock, baseline)
}
