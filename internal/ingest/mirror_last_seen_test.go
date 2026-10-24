package ingest

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// TestIngestMirrorLeavesLastSeenAlone pins that a mirror run carries no new
// sighting: re-ingesting an unchanged binding keeps the mastermind row's stamp
// byte-identical and queues no mastermind outbox entry, while a moved session
// still records its single insert.
func TestIngestMirrorLeavesLastSeenAlone(t *testing.T) {
	dir := copyFixture(t)
	path := filepath.Join(t.TempDir(), "relevo.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	sqlDB, err := db.OpenRaw(path)
	if err != nil {
		t.Fatalf("db.OpenRaw: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	old := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	if _, err := d.UpsertMasterMind(db.MasterMind{HarnessKind: "claude", SessionID: "S1", FirstSeen: old, LastSeen: old}); err != nil {
		t.Fatalf("UpsertMasterMind: %v", err)
	}
	before := rawLastSeen(t, sqlDB)
	if err := d.TruncateOutbox(); err != nil {
		t.Fatalf("TruncateOutbox: %v", err)
	}

	for i := 0; i < 2; i++ {
		if _, err := Ingest(context.Background(), DirSource(dir), d, Deps{}); err != nil {
			t.Fatalf("Ingest %d: %v", i+1, err)
		}
	}

	if after := rawLastSeen(t, sqlDB); after != before {
		t.Errorf("last_seen = %q, want the seeded %q untouched", after, before)
	}
	if ops := mastermindOutboxOps(t, sqlDB); len(ops) != 0 {
		t.Errorf("unchanged mirror runs queued mastermind outbox ops %q, want none", ops)
	}

	// A moved session is still a first sighting: exactly one insert.
	raw, err := os.ReadFile(filepath.Join(dir, "bind.json"))
	if err != nil {
		t.Fatalf("read bind.json: %v", err)
	}
	const from = `"session_id": "S1"`
	if !strings.Contains(string(raw), from) {
		t.Fatalf("bind.json no longer names session S1; cannot move it")
	}
	moved := strings.Replace(string(raw), from, `"session_id": "S2"`, 1)
	if err := os.WriteFile(filepath.Join(dir, "bind.json"), []byte(moved), 0o644); err != nil {
		t.Fatalf("write bind.json: %v", err)
	}
	if _, err := Ingest(context.Background(), DirSource(dir), d, Deps{}); err != nil {
		t.Fatalf("Ingest moved session: %v", err)
	}
	if ops := mastermindOutboxOps(t, sqlDB); len(ops) != 1 || ops[0] != "insert" {
		t.Errorf("moved session queued mastermind outbox ops %q, want exactly one insert", ops)
	}
}

// rawLastSeen reads the mirrored session's stamp as stored, so the test pins
// the bytes rather than a parsed instant.
func rawLastSeen(t *testing.T, sqlDB *sql.DB) string {
	t.Helper()
	var lastSeen string
	if err := sqlDB.QueryRow(`SELECT last_seen FROM mastermind WHERE harness_kind = 'claude' AND session_id = 'S1'`).Scan(&lastSeen); err != nil {
		t.Fatalf("select last_seen: %v", err)
	}
	return lastSeen
}

// mastermindOutboxOps returns the ops every sync_outbox entry for mastermind
// recorded, in drain order.
func mastermindOutboxOps(t *testing.T, sqlDB *sql.DB) []string {
	t.Helper()
	rows, err := sqlDB.Query(`SELECT op FROM sync_outbox WHERE tbl = 'mastermind' ORDER BY seq`)
	if err != nil {
		t.Fatalf("read sync_outbox: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var ops []string
	for rows.Next() {
		var op string
		if err := rows.Scan(&op); err != nil {
			t.Fatalf("scan sync_outbox: %v", err)
		}
		ops = append(ops, op)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate sync_outbox: %v", err)
	}
	return ops
}
