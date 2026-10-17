package db

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func adoptExec(t *testing.T, d *DB, query string, args ...any) {
	t.Helper()
	if _, err := d.sqlDB.Exec(query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func adoptBinding(t *testing.T, d *DB, id, origin string) {
	t.Helper()
	adoptExec(t, d, `INSERT INTO binding (id, name, cwd, builder_mode, created_at, ingest_source, origin, writer)
		VALUES (?, ?, '/x', 'local', 't', 'manual', ?, ?)`, id, id, origin, origin)
}

func TestAdoptionBlockersCountsWhatTheMachineWroteUnderItsOwnIdentity(t *testing.T) {
	d := openTermTestDB(t, "tmp")
	now := time.Unix(0, 0).UTC()
	if err := d.Tx(func(tx *Tx) error { return tx.InstallationTouch("tmp", "laptop", now) }); err != nil {
		t.Fatalf("InstallationTouch: %v", err)
	}
	blockers, err := d.AdoptionBlockers("config")
	if err != nil || !blockers.Empty() || blockers.String() != "" {
		t.Fatalf("a fresh machine = %+v, %v, want nothing in the way: the installation row's own upkeep is not a change", blockers, err)
	}

	adoptBinding(t, d, "b1", "tmp")
	adoptExec(t, d, `INSERT INTO shared_outbox (doc, dirtied_at) VALUES ('hooks', 't')`)
	blockers, err = d.AdoptionBlockers("config")
	if err != nil {
		t.Fatalf("AdoptionBlockers: %v", err)
	}
	if blockers.Empty() || blockers.Rows["binding"] != 1 || blockers.Outbox == 0 || blockers.SharedOutbox != 1 {
		t.Fatalf("blockers = %+v, want the binding, its outbox row and the one unignored document", blockers)
	}
	for _, want := range []string{"changes waiting to export", "shared documents waiting", "1 binding rows"} {
		if !strings.Contains(blockers.String(), want) {
			t.Errorf("String() = %q, want it to name %q", blockers.String(), want)
		}
	}

	adoptExec(t, d, `INSERT INTO installation (id, label, first_seen, last_seen) VALUES ('other', 'zen', 't', 't')`)
	blockers, _ = d.AdoptionBlockers("config", "hooks")
	if blockers.Rows["installation"] != 1 {
		t.Errorf("another installation's row = %+v, want it counted", blockers.Rows)
	}
}

func TestAdoptIdentityDropsTheTemporaryIdentityAndTheImportEchoes(t *testing.T) {
	d := openTermTestDB(t, "tmp")
	now := time.Unix(0, 0).UTC()
	if err := d.Tx(func(tx *Tx) error { return tx.InstallationTouch("tmp", "laptop", now) }); err != nil {
		t.Fatalf("InstallationTouch: %v", err)
	}
	adoptBinding(t, d, "from-x", "inst-x")
	if err := d.SetImportMark("inst-x", 7); err != nil {
		t.Fatalf("SetImportMark: %v", err)
	}
	if err := d.AdoptIdentity("inst-x", "token-x"); err != nil {
		t.Fatalf("AdoptIdentity: %v", err)
	}
	pending, ok, err := d.ReadAdoptPending()
	if err != nil || !ok || pending.ID != "inst-x" || pending.Token != "token-x" {
		t.Fatalf("ReadAdoptPending = %+v, %v, %v; want inst-x and token-x", pending, ok, err)
	}
	for query, want := range map[string]int{
		`SELECT COUNT(*) FROM installation WHERE id = 'tmp'`: 0,
		`SELECT COUNT(*) FROM sync_outbox`:                   0,
		`SELECT COUNT(*) FROM binding WHERE id = 'from-x'`:   1,
		`SELECT COUNT(*) FROM sync_import_mark`:              0,
	} {
		var n int
		if err := d.sqlDB.QueryRow(query).Scan(&n); err != nil || n != want {
			t.Errorf("%s = %d (%v), want %d", query, n, err, want)
		}
	}
}

func TestAdoptIdentityRefusesAChangeMadeSinceTheAdoptionBegan(t *testing.T) {
	d := openTermTestDB(t, "tmp")
	adoptBinding(t, d, "mine", "tmp")
	adoptExec(t, d, `DELETE FROM binding WHERE id = 'mine'`)
	err := d.AdoptIdentity("inst-x", "token-x")
	if !errors.Is(err, ErrAdoptionDirty) || !errors.Is(err, ErrInvalid) {
		t.Fatalf("AdoptIdentity = %v, want ErrAdoptionDirty", err)
	}
	var n int
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM sync_outbox`).Scan(&n); err != nil || n == 0 {
		t.Errorf("a refused adoption cleared the outbox (%d, %v)", n, err)
	}
	for _, id := range []string{"", "tmp"} {
		if err := d.AdoptIdentity(id, "token-x"); !errors.Is(err, ErrInvalid) {
			t.Errorf("AdoptIdentity(%q) = %v, want ErrInvalid", id, err)
		}
	}
}
