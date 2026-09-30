//go:build unix

package client_test

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire/client"
)

// TestHandshakeTimeoutBoundsPooledHandshake pins that openConn bounds a new
// pooled connection with handshakeBudget: the raised budget is what a
// deliberate starvation test gets, and a non-positive SetHandshakeTimeout
// restores the production default. The check reads the deadline the dial hook
// receives, not wall-clock elapsed time, so it is deterministic.
func TestHandshakeTimeoutBoundsPooledHandshake(t *testing.T) {
	_, sock := shortListener(t)

	var mu sync.Mutex
	var budgets []time.Duration
	refuse := false
	client.SetDialer(func(ctx context.Context, s string) (net.Conn, error) {
		if deadline, ok := ctx.Deadline(); ok {
			mu.Lock()
			budgets = append(budgets, time.Until(deadline))
			mu.Unlock()
		}
		mu.Lock()
		stop := refuse
		mu.Unlock()
		if stop {
			return nil, errors.New("dial refused after the budget assertion")
		}
		var d net.Dialer
		return d.DialContext(ctx, "unix", s)
	})
	t.Cleanup(func() { client.SetDialer(nil) })
	t.Cleanup(func() { client.SetHandshakeTimeout(0) })

	client.SetHandshakeTimeout(50 * time.Millisecond)
	raised, err := sql.Open(client.DriverName, sock)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = raised.Close() })
	if _, err := raised.Exec(`CREATE TABLE raised (n INTEGER)`); err == nil {
		t.Fatal("a pooled connection succeeded against a listener that never answers")
	}
	mu.Lock()
	n := len(budgets)
	var first time.Duration
	if n > 0 {
		first = budgets[0]
	}
	mu.Unlock()
	if n == 0 {
		t.Fatal("the dial hook was never called for the raised-budget open")
	}
	if first <= 0 || first > 50*time.Millisecond {
		t.Fatalf("the pooled handshake deadline was %v, want it within the 50ms budget", first)
	}

	client.SetHandshakeTimeout(0)
	mu.Lock()
	refuse = true
	mu.Unlock()
	restored, err := sql.Open(client.DriverName, sock)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	if _, err := restored.Exec(`CREATE TABLE restored (n INTEGER)`); err == nil {
		t.Fatal("a pooled connection succeeded although the dial hook refused it")
	}
	mu.Lock()
	last := budgets[len(budgets)-1]
	mu.Unlock()
	if last <= time.Second || last > 2*time.Second {
		t.Fatalf("the restored pooled handshake deadline was %v, want the default 2s", last)
	}
}

// TestInfoKeepsTheCallersDeadline pins that Info dials with the caller's ctx
// deadline and never reads the knob.
func TestInfoKeepsTheCallersDeadline(t *testing.T) {
	_, sock := shortListener(t)

	client.SetHandshakeTimeout(5 * time.Second)
	t.Cleanup(func() { client.SetHandshakeTimeout(0) })

	var mu sync.Mutex
	var budget time.Duration
	client.SetDialer(func(ctx context.Context, s string) (net.Conn, error) {
		if deadline, ok := ctx.Deadline(); ok {
			mu.Lock()
			budget = time.Until(deadline)
			mu.Unlock()
		}
		return nil, errors.New("dial refused")
	})
	t.Cleanup(func() { client.SetDialer(nil) })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := client.Info(ctx, sock); err == nil {
		t.Fatal("Info succeeded although the dial hook refused")
	}
	mu.Lock()
	got := budget
	mu.Unlock()
	if got <= 0 || got > 100*time.Millisecond {
		t.Fatalf("Info dialled with a %v deadline, want the caller's ~100ms", got)
	}
}
