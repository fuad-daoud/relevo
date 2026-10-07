//go:build modernc

package db

import (
	"context"
	"database/sql"
)

// Stubs for the capture machinery, which exists only in the Turso build.
// Under modernc there is no sync driver, no capture pragma and no held
// connection: writes go over the pool directly, which is the pre-capture
// behaviour. These exist so shared call sites (Close, Tx) compile on both
// engines without build-tagged branches at every use.
func releaseHeldCapture(path string) error { return nil }

func closeCapturePool(path string) error { return nil }

func (d *DB) writeConn(ctx context.Context) (*sql.Conn, func() error, error) {
	conn, err := d.sqlDB.Conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	return conn, conn.Close, nil
}
