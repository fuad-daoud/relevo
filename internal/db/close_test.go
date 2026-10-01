package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"testing"
	"time"
)

// parkConn is a driver.Conn whose Close signals and then parks until released,
// so a test can hold a pool's close open and observe the handle count while it
// is parked.
type parkConn struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *parkConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not supported") }
func (c *parkConn) Begin() (driver.Tx, error)           { return nil, errors.New("not supported") }

func (c *parkConn) Close() error {
	c.once.Do(func() { close(c.started) })
	<-c.release
	return nil
}

// parkDriver hands out the one parkConn a test holds.
type parkDriver struct{ conn *parkConn }

func (d parkDriver) Open(string) (driver.Conn, error) { return d.conn, nil }

// parkConnector is the connector the stubbed pool opens connections through.
type parkConnector struct {
	conn *parkConn
	drv  parkDriver
}

func (c parkConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (c parkConnector) Driver() driver.Driver                        { return c.drv }

// TestCloseReleasesThePathAfterThePoolCloses pins the close order: the path's
// handle and flock are released only after the pool closes, so a concurrent
// opener cannot win the flock while this handle's connections still hold the
// engine's file lock. The stubbed pool parks its close, and the handle count is
// read while it is parked.
func TestCloseReleasesThePathAfterThePoolCloses(t *testing.T) {
	d := directOpenTestDB(t)
	path := d.path
	if err := d.sqlDB.Close(); err != nil {
		t.Fatalf("close the real pool: %v", err)
	}

	pc := &parkConn{started: make(chan struct{}), release: make(chan struct{})}
	d.sqlDB = sql.OpenDB(parkConnector{conn: pc, drv: parkDriver{conn: pc}})

	// One connection, handed back, gives the pool an idle connection whose
	// Close the stubbed driver parks.
	conn, err := d.sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("return the connection: %v", err)
	}

	closed := make(chan error, 1)
	go func() { closed <- d.Close() }()

	select {
	case <-pc.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the pool close never reached the driver connection")
	}
	if got := handleCount(path); got != 1 {
		t.Errorf("handleCount while the pool is closing = %d, want 1", got)
	}

	close(pc.release)
	if err := <-closed; err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := handleCount(path); got != 0 {
		t.Errorf("handleCount after Close = %d, want 0", got)
	}
}
