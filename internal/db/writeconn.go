//go:build !modernc

package db

// Where a handle's writes go. A member file's transactions run over the one held
// capture connection, because turso captures per connection and a row written on
// any other one is in the database and in no change set: no push can carry it and
// every push still reports success. Every other handle borrows from the pool.

import (
	"context"
	"database/sql"
	"fmt"
)

// writeConn is the connection one transaction runs over, and the func that hands
// it back.
//
// A member file's writers share the one held capture connection, so their rows
// land in the change set a push carries; every other handle borrows from the
// pool and returns what it borrowed. The release is passed back rather than
// closed here because the two connections must not be released the same way: a
// pooled one goes back to the pool, and the capture one must be held for the next
// writer.
func (d *DB) writeConn(ctx context.Context) (*sql.Conn, func() error, error) {
	if d.path == "" || !d.member() {
		conn, err := d.sqlDB.Conn(ctx)
		if err != nil {
			return nil, nil, err
		}
		return conn, conn.Close, nil
	}
	held, err := captureConnFor(d.path, int64(d.busy.Milliseconds()))
	if err != nil {
		return nil, nil, err
	}
	// One writer at a time over the one connection: two transactions on one
	// connection would interleave their BEGIN and COMMIT.
	held.mu.Lock()
	conn := held.conn.Conn()
	if conn == nil {
		held.mu.Unlock()
		return nil, nil, fmt.Errorf("the capture connection is closed: %w", ErrOpen)
	}
	return conn, func() error {
		held.mu.Unlock()
		return nil
	}, nil
}

// member reports whether this handle's file is a sync member, which is what
// decides whether its writes have to carry a change set.
func (d *DB) member() bool {
	if d.path == "" || d.readOnly {
		return false
	}
	if _, ok := syncMembers.Load(d.path); ok {
		return true
	}
	member, err := IsSyncMember(d.path)
	if err != nil {
		return false
	}
	return member
}
