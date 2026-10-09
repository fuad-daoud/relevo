package ingest

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

func TestReingestAfterRepairKeepsRepairedValues(t *testing.T) {
	path, d := openRepoDB(t)
	const newURL = "https://github.com/o/r2"
	toID, err := d.UpsertRepo(db.Repo{OriginURL: strPtr(newURL), CommonDir: strPtr("/static/.git"), FirstSeen: ingestSeenAt})
	if err != nil {
		t.Fatalf("seed the target repo row: %v", err)
	}

	dir := copyFixture(t)
	if _, err := Ingest(context.Background(), DirSource(dir), d, Deps{}); err != nil {
		t.Fatalf("first Ingest: %v", err)
	}
	bind, err := os.ReadFile(filepath.Join(dir, "bind.json"))
	if err != nil {
		t.Fatalf("read bind.json: %v", err)
	}
	now := time.Now().UTC()
	if _, err := d.RecordPut(db.Record{Name: "fixture", State: "needs_you", Round: 3, CWD: "/work/fixture",
		JSON: string(bind), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("RecordPut: %v", err)
	}

	if _, err := d.RenameRepo(db.RenameRepoParams{
		FromURL: fixtureRepoURL, ToURL: newURL, FromOwnerRepo: "o/r", ToOwnerRepo: "o/r2",
	}); err != nil {
		t.Fatalf("RenameRepo: %v", err)
	}
	rec, found, err := d.RecordGet("", "fixture")
	if err != nil || !found {
		t.Fatalf("RecordGet: found=%v err=%v", found, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bind.json"), []byte(rec.JSON), 0o644); err != nil {
		t.Fatalf("write the repaired bind.json: %v", err)
	}

	if _, err := Ingest(context.Background(), DirSource(dir), d, Deps{}); err != nil {
		t.Fatalf("re-Ingest: %v", err)
	}
	b := mustBinding(t, d, "fixture")
	if b.Ticket == nil || *b.Ticket != "o/r2#607" {
		t.Errorf("binding ticket = %v, want the repaired o/r2#607", b.Ticket)
	}
	if b.RepoID == nil || *b.RepoID != toID {
		t.Errorf("binding repo_id = %v, want the merged row %s", b.RepoID, toID)
	}
	if n := countWhere(t, path, `SELECT count(*) FROM repo`); n != 1 {
		t.Errorf("repo rows = %d, want 1", n)
	}
}
