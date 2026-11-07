package db

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// countingQueryer wraps a queryer and counts the statements it serves, so a
// test pins how many statements a batched read issues.
type countingQueryer struct {
	inner      queryer
	queries    int
	rowQueries int
}

func (c *countingQueryer) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	c.queries++
	return c.inner.QueryContext(ctx, query, args...)
}

func (c *countingQueryer) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	c.rowQueries++
	return c.inner.QueryRowContext(ctx, query, args...)
}

// TestKVScanLiveReadsTheNamespaceInOneStatement pins a listing's read budget:
// whatever the live-key count, the scan is exactly one statement, and only
// in-range keys come back.
func TestKVScanLiveReadsTheNamespaceInOneStatement(t *testing.T) {
	for _, m := range []int{1, 5} {
		t.Run(fmt.Sprintf("%d live keys", m), func(t *testing.T) {
			d := openTestDB(t)
			want := map[string][]byte{}
			// The lower bound is inclusive: the bare prefix key itself is in range.
			if err := d.KVPut("live/", []byte(`{"stream_offset":0}`)); err != nil {
				t.Fatalf("KVPut live/: %v", err)
			}
			want["live/"] = []byte(`{"stream_offset":0}`)
			for i := 0; i < m; i++ {
				key := fmt.Sprintf("live/01SCAN%02d", i)
				value := fmt.Sprintf(`{"stream_offset":%d}`, 100+i)
				if err := d.KVPut(key, []byte(value)); err != nil {
					t.Fatalf("KVPut %s: %v", key, err)
				}
				want[key] = []byte(value)
			}
			// Keys outside the range share a prefix with it but must not come back.
			for key, value := range map[string]string{
				"ledger":     `{"entries":[]}`,
				"live":       `{}`,
				"live.":      `{}`,
				"live0":      `{}`,
				"live0probe": `{"stream_offset":4242}`,
			} {
				if err := d.KVPut(key, []byte(value)); err != nil {
					t.Fatalf("KVPut %s: %v", key, err)
				}
			}

			counting := &countingQueryer{inner: d.sqlDB}
			got, err := kvScanLive(context.Background(), counting)
			if err != nil {
				t.Fatalf("kvScanLive: %v", err)
			}
			if counting.queries != 1 || counting.rowQueries != 0 {
				t.Errorf("statements = %d QueryContext + %d QueryRowContext, want exactly 1 + 0",
					counting.queries, counting.rowQueries)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("kvScanLive = %q, want %q", got, want)
			}

			// The Tx surface reads the same namespace.
			if err := d.Tx(func(tx *Tx) error {
				txGot, err := tx.KVScanLive()
				if err != nil {
					return err
				}
				if !reflect.DeepEqual(txGot, want) {
					t.Errorf("tx KVScanLive = %q, want %q", txGot, want)
				}
				return nil
			}); err != nil {
				t.Fatalf("Tx KVScanLive: %v", err)
			}

			// The *DB surface reads it too.
			if dbGot, err := d.KVScanLive(); err != nil || !reflect.DeepEqual(dbGot, want) {
				t.Errorf("*DB KVScanLive = %q (err %v), want %q", dbGot, err, want)
			}
		})
	}
}

// TestKVScanLiveOnSchemaWithoutKVTable pins the old-database read: a schema
// predating the kv table scans as empty rather than erroring.
func TestKVScanLiveOnSchemaWithoutKVTable(t *testing.T) {
	d := openSchema(t, "001_initial.sql")
	got, err := d.KVScanLive()
	if err != nil {
		t.Fatalf("KVScanLive on schema 1: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("KVScanLive on schema 1 = %q, want empty", got)
	}
}

// TestKVScanLiveQueryError pins the wrap a dead handle reports: anything past
// a missing table is an error naming the scan.
func TestKVScanLiveQueryError(t *testing.T) {
	d := openTestDB(t)
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := d.KVScanLive(); err == nil || !strings.Contains(err.Error(), "db: kv scan live") {
		t.Errorf("KVScanLive on a closed handle = %v, want an error naming the scan", err)
	}
}
