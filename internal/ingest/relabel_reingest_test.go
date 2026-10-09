package ingest

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

func TestReingestAfterRelabelKeepsNewLabel(t *testing.T) {
	_, d := openRepoDB(t)
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

	c, err := d.Relabel(db.RelabelParams{
		Entries:   []db.RelabelEntry{{ID: mustBinding(t, d, "fixture").ID, Feature: "billing", Line: 1}},
		Overwrite: true,
	})
	if err != nil || c.Labelled != 1 || c.RecordsRewritten != 1 {
		t.Fatalf("Relabel = %+v, %v; want one binding and its record rewritten", c, err)
	}
	rec, found, err := d.RecordGet("", "fixture")
	if err != nil || !found {
		t.Fatalf("RecordGet: found=%v err=%v", found, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bind.json"), []byte(rec.JSON), 0o644); err != nil {
		t.Fatalf("write the relabelled bind.json: %v", err)
	}

	if _, err := Ingest(context.Background(), DirSource(dir), d, Deps{}); err != nil {
		t.Fatalf("re-Ingest: %v", err)
	}
	if b := mustBinding(t, d, "fixture"); b.Feature == nil || *b.Feature != "billing" {
		t.Errorf("binding feature = %v, want the relabelled billing", b.Feature)
	}
}
