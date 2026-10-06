//go:build !modernc

package db

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"
)

// plainConn is a driver connection with no ExecContext method: the shape the
// pragma connector must refuse, because it cannot run the per-connection
// pragmas every pooled connection needs.
type plainConn struct{ closed bool }

func (c *plainConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not supported") }
func (c *plainConn) Close() error                        { c.closed = true; return nil }
func (c *plainConn) Begin() (driver.Tx, error)           { return nil, errors.New("not supported") }

// refusingExecConn runs no pragma: it reports the driver error the connector
// must surface and still close the connection.
type refusingExecConn struct{ plainConn }

func (c *refusingExecConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return nil, errors.New("pragma refused")
}

// namedDriver is a driver value with no behaviour, used to check that the
// pragma connector reports what it wraps.
type namedDriver struct{}

func (namedDriver) Open(string) (driver.Conn, error) { return nil, errors.New("not supported") }

// stubConnector is the base a pragma connector wraps: it yields a chosen
// connection and names a chosen driver.
type stubConnector struct {
	conn driver.Conn
	drv  driver.Driver
}

func (c stubConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (c stubConnector) Driver() driver.Driver                        { return c.drv }

// TestPragmaConnectorRefusesAConnectionWithoutExecContext pins that a base
// connection the connector cannot prepare is closed and refused, rather than
// handed to database/sql without its pragmas.
func TestPragmaConnectorRefusesAConnectionWithoutExecContext(t *testing.T) {
	conn := &plainConn{}
	pc := &pragmaConnector{base: stubConnector{conn: conn}, pragmas: openPragmas(false)}

	if _, err := pc.Connect(context.Background()); !errors.Is(err, ErrOpen) {
		t.Fatalf("Connect of a connection without ExecContext = %v, want ErrOpen", err)
	}
	if !conn.closed {
		t.Errorf("the refused connection was not closed")
	}
}

// TestPragmaConnectorClosesAConnectionWhosePragmaFails pins that a pragma the
// connection rejects fails the connect, closes that connection, and surfaces the
// error instead of returning a connection with only some pragmas applied.
func TestPragmaConnectorClosesAConnectionWhosePragmaFails(t *testing.T) {
	conn := &refusingExecConn{}
	pc := &pragmaConnector{base: stubConnector{conn: conn}, pragmas: openPragmas(false)}

	if _, err := pc.Connect(context.Background()); err == nil {
		t.Fatal("Connect succeeded although the connection refused its pragma")
	}
	if !conn.closed {
		t.Errorf("the connection whose pragma failed was not closed")
	}
}

// TestPragmaConnectorReportsItsBaseDriver pins that the connector reports the
// driver it wraps, which database/sql reads from a connector.
func TestPragmaConnectorReportsItsBaseDriver(t *testing.T) {
	drv := namedDriver{}
	if got := (&pragmaConnector{base: stubConnector{drv: drv}}).Driver(); got != driver.Driver(drv) {
		t.Errorf("Driver = %v, want the wrapped driver", got)
	}
}
