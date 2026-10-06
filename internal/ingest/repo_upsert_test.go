package ingest

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// The repo keys the fixture's bind.json carries. They are read from the binding
// and not from git -- resolveRefs prefers bind.json's repo_ref and only shells
// out when there is none -- so these are the strings the upsert actually sees,
// which is what makes this the same call the journal was reporting.
const (
	fixtureRepoURL = "https://github.com/o/r"
	fixtureRepoDir = "/work/fixture/.git"
)

var ingestSeenAt = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

func strPtr(s string) *string { return &s }

// openRepoDB opens the database the way the ingest tests do, and hands back its
// path as well: the counts below are asked through a raw handle because the
// package's exported surface answers questions about bindings, not about how many
// repo rows a run left behind.
func openRepoDB(t *testing.T) (string, *db.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "relevo.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return path, d
}

func countWhere(t *testing.T, path, query string, args ...any) int {
	t.Helper()
	raw, err := db.OpenRawReadOnly(path)
	if err != nil {
		t.Fatalf("db.OpenRawReadOnly: %v", err)
	}
	defer func() { _ = raw.Close() }()
	var n int
	if err := raw.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count (%s): %v", query, err)
	}
	return n
}

// seedDirOnlyRepo is the state the origin backfill leaves behind: one repo row
// for the checkout, holding the common dir and no origin URL. It is the row the
// upsert has to find, and it is why every later ingest of a binding whose
// bind.json names this checkout used to end in a unique violation instead of a
// write -- the bind.json carries both keys, the row carried one of them, and the
// lookup read only the one the row did not have.
func seedDirOnlyRepo(t *testing.T, d *db.DB) string {
	t.Helper()
	id, err := d.UpsertRepo(db.Repo{CommonDir: strPtr(fixtureRepoDir), FirstSeen: ingestSeenAt})
	if err != nil {
		t.Fatalf("seed the dir-only repo row: %v", err)
	}
	return id
}

// seedUnstampedDirOnlyRepo is the same row before the backfill stamped it.
func seedUnstampedDirOnlyRepo(t *testing.T, path string) {
	t.Helper()
	raw, err := db.OpenRaw(path)
	if err != nil {
		t.Fatalf("db.OpenRaw: %v", err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.Exec(
		`INSERT INTO repo (id, origin, origin_url, common_dir, first_seen) VALUES (?, '', NULL, ?, ?)`,
		db.NewID(), fixtureRepoDir, ingestSeenAt.Format(time.RFC3339)); err != nil {
		t.Fatalf("seed the unstamped repo row: %v", err)
	}
}

// TestIngestLandsOnTheRowHoldingTheDir is the journal line, whole.
//
// While the upsert resolved on one of repo's two keys and the insert carried
// both, this exact fixture ended in
//
//	ingest binding=site-* err="ingest upsert repo: db: upsert repo: insert:
//	turso: constraint failed: UNIQUE constraint failed: repo.(origin, common_dir)"
//
// on every tick. The assertion is that ingest now completes, and that the binding
// it writes points at the row that was already there.
func TestIngestLandsOnTheRowHoldingTheDir(t *testing.T) {
	path, d := openRepoDB(t)
	live := seedDirOnlyRepo(t, d)

	dir := copyFixture(t)
	if _, err := Ingest(context.Background(), DirSource(dir), d, Deps{}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	b := mustBinding(t, d, "fixture")
	if b.RepoID == nil || *b.RepoID != live {
		t.Errorf("binding repo_id = %v, want the row that was already there %s", b.RepoID, live)
	}
	if n := countWhere(t, path, `SELECT count(*) FROM repo`); n != 1 {
		t.Errorf("repo rows = %d, want 1: ingest inserted a twin", n)
	}
	if n := countWhere(t, path,
		`SELECT count(*) FROM repo WHERE origin_url = ? AND common_dir = ?`,
		fixtureRepoURL, fixtureRepoDir); n != 1 {
		t.Errorf("rows carrying both keys = %d, want 1: the url was not filled onto the dir's row", n)
	}
}

// TestIngestIsIdempotentOverTheRowHoldingTheDir runs the same ingest twice. The
// first call fills the URL onto the dir's row; the second has to find that row by
// either key and leave it alone, which is what keeps a tick from writing the same
// binding again -- or colliding with what the first call just wrote.
func TestIngestIsIdempotentOverTheRowHoldingTheDir(t *testing.T) {
	path, d := openRepoDB(t)
	live := seedDirOnlyRepo(t, d)

	dir := copyFixture(t)
	for pass := 1; pass <= 2; pass++ {
		if _, err := Ingest(context.Background(), DirSource(dir), d, Deps{}); err != nil {
			t.Fatalf("Ingest pass %d: %v", pass, err)
		}
	}
	b := mustBinding(t, d, "fixture")
	if b.RepoID == nil || *b.RepoID != live {
		t.Errorf("binding repo_id = %v, want %s", b.RepoID, live)
	}
	if n := countWhere(t, path, `SELECT count(*) FROM repo`); n != 1 {
		t.Errorf("repo rows = %d, want 1", n)
	}
}

// TestIngestTakesTheKeysFromBindJSON pins where the two keys come from, because
// it is what makes the pair of keys stable: resolveRefs prefers the binding's own
// repo_ref, so the URL is the one recorded when the binding was made and does not
// follow what git reports now. A test that resolved them from git would exercise
// a different path than the one that failed.
func TestIngestTakesTheKeysFromBindJSON(t *testing.T) {
	path, d := openRepoDB(t)

	dir := copyFixture(t)
	// Git facts naming a different checkout must not be consulted at all.
	deps := Deps{Git: fakeGitFacts{originURL: "https://github.com/o/other", commonDir: "/work/other/.git"}}
	if _, err := Ingest(context.Background(), DirSource(dir), d, deps); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	if n := countWhere(t, path,
		`SELECT count(*) FROM repo WHERE origin_url = ? AND common_dir = ?`,
		fixtureRepoURL, fixtureRepoDir); n != 1 {
		t.Errorf("rows carrying bind.json's keys = %d, want 1", n)
	}
	if n := countWhere(t, path, `SELECT count(*) FROM repo WHERE common_dir = '/work/other/.git'`); n != 0 {
		t.Errorf("rows carrying git's keys = %d, want 0", n)
	}
}

// TestIngestStampsTheRowItAdopted keeps the origin half visible at the ingest
// seam: a row the lookup found under no origin is stamped when ingest fills it,
// so the origin backfill has nothing left to settle for it on the next pass.
func TestIngestStampsTheRowItAdopted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	d, err := db.OpenWith(path, db.Options{Origin: "01AAAAAAAAAAAAAAAAAAAAAAAA"})
	if err != nil {
		t.Fatalf("db.OpenWith: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	seedUnstampedDirOnlyRepo(t, path)

	dir := copyFixture(t)
	if _, err := Ingest(context.Background(), DirSource(dir), d, Deps{}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	if n := countWhere(t, path,
		`SELECT count(*) FROM repo WHERE origin = '' AND common_dir = ?`, fixtureRepoDir); n != 0 {
		t.Errorf("unstamped rows for the dir = %d, want 0", n)
	}
	if n := countWhere(t, path, `SELECT count(*) FROM repo`); n != 1 {
		t.Errorf("repo rows = %d, want 1", n)
	}
}

// TestIngestInsertsWhenTheOriginHasNoRowForTheCheckout is the ordinary path, and
// the reason the resolution cannot be only a fallback: a checkout this origin has
// never recorded still has to become a row.
func TestIngestInsertsWhenTheOriginHasNoRowForTheCheckout(t *testing.T) {
	path, d := openRepoDB(t)

	dir := copyFixture(t)
	if _, err := Ingest(context.Background(), DirSource(dir), d, Deps{}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if n := countWhere(t, path, `SELECT count(*) FROM repo`); n != 1 {
		t.Errorf("repo rows = %d, want 1", n)
	}
	b := mustBinding(t, d, "fixture")
	if b.RepoID == nil {
		t.Fatal("binding has no repo_id")
	}
}
