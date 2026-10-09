package db

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// repoDirOnly is the shape the origin backfill leaves behind: one row for a
// checkout, holding the common dir and no origin URL. The checkout had no remote
// when it was first recorded, or the twin the pass dropped was the row that held
// the URL -- either way what survives is a row in one of repo's two indexes and
// not the other.
func repoDirOnly(t *testing.T, d *DB, dir string) string {
	t.Helper()
	id, err := d.UpsertRepo(Repo{CommonDir: ptr(dir), FirstSeen: repoSeenAt})
	if err != nil {
		t.Fatalf("seed the dir-only repo row: %v", err)
	}
	return id
}

var repoSeenAt = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

const (
	repoDir = "/home/x/site/.git"
	repoURL = "https://github.com/o/site"
)

// repoRow is one row of the repo table as the tests read it back.
type repoRow struct {
	id, origin     string
	originURL, dir sql.Null[string]
}

func readRepoRow(t *testing.T, d *DB, id string) repoRow {
	t.Helper()
	var row repoRow
	err := d.sqlDB.QueryRow(`SELECT id, origin, origin_url, common_dir FROM repo WHERE id = ?`, id).
		Scan(&row.id, &row.origin, &row.originURL, &row.dir)
	if err != nil {
		t.Fatalf("read repo row %s: %v", id, err)
	}
	return row
}

func countRepos(t *testing.T, d *DB) int {
	t.Helper()
	var n int
	if err := d.sqlDB.QueryRow(`SELECT count(*) FROM repo`).Scan(&n); err != nil {
		t.Fatalf("count repo rows: %v", err)
	}
	return n
}

// TestUpsertRepoResolvesTheRowHoldingTheDir is the field failure this fixes.
//
// The row for the checkout exists and holds the common dir, and the upsert
// arrives with both the URL and the dir. Resolving on the URL alone cannot see
// that row, and the insert that follows collides with it on the other index:
// `db: upsert repo: insert: turso: constraint failed: UNIQUE constraint failed:
// repo.(origin, common_dir)`.
func TestUpsertRepoResolvesTheRowHoldingTheDir(t *testing.T) {
	d := openTestDB(t)
	live := repoDirOnly(t, d, repoDir)

	id, err := d.UpsertRepo(Repo{OriginURL: ptr(repoURL), CommonDir: ptr(repoDir), FirstSeen: repoSeenAt})
	if err != nil {
		t.Fatalf("UpsertRepo: %v", err)
	}
	if id != live {
		t.Errorf("UpsertRepo id = %s, want the live row %s", id, live)
	}
	if n := countRepos(t, d); n != 1 {
		t.Errorf("repo rows = %d, want 1: the upsert inserted a twin instead of updating", n)
	}
	row := readRepoRow(t, d, live)
	if row.originURL.V != repoURL {
		t.Errorf("origin_url = %q, want %q", row.originURL.V, repoURL)
	}
	if row.dir.V != repoDir {
		t.Errorf("common_dir = %q, want %q", row.dir.V, repoDir)
	}
}

// TestUpsertRepoResolvesEitherColumnIs the same convergence from the other side:
// a row holding only the URL is found by the URL, and a record naming both keys
// lands on it rather than inserting a row for the dir that already belongs to it.
func TestUpsertRepoResolvesEitherColumn(t *testing.T) {
	d := openTestDB(t)
	live, err := d.UpsertRepo(Repo{OriginURL: ptr(repoURL), FirstSeen: repoSeenAt})
	if err != nil {
		t.Fatalf("seed the url-only repo row: %v", err)
	}

	id, err := d.UpsertRepo(Repo{OriginURL: ptr(repoURL), CommonDir: ptr(repoDir), FirstSeen: repoSeenAt})
	if err != nil {
		t.Fatalf("UpsertRepo: %v", err)
	}
	if id != live {
		t.Errorf("UpsertRepo id = %s, want the live row %s", id, live)
	}
	if n := countRepos(t, d); n != 1 {
		t.Errorf("repo rows = %d, want 1", n)
	}
	row := readRepoRow(t, d, live)
	if row.dir.V != repoDir {
		t.Errorf("common_dir = %q, want %q", row.dir.V, repoDir)
	}
}

// TestUpsertRepoInsertsWhenNeitherKeyIsHeld is the ordinary insert, and the
// reason the fallback cannot be the only path: a checkout this origin has never
// seen still has to become a row.
func TestUpsertRepoInsertsWhenNeitherKeyIsHeld(t *testing.T) {
	d := openTestDB(t)

	id, err := d.UpsertRepo(Repo{OriginURL: ptr(repoURL), CommonDir: ptr(repoDir), FirstSeen: repoSeenAt})
	if err != nil {
		t.Fatalf("UpsertRepo: %v", err)
	}
	if id == "" {
		t.Fatal("UpsertRepo returned no id")
	}
	if n := countRepos(t, d); n != 1 {
		t.Errorf("repo rows = %d, want 1", n)
	}
}

// TestUpsertRepoTakesAStampedRowIs the pin on what the convergence is not: the
// row it lands on is the one that was already there. No row is dropped and no id
// is repointed, so every binding that named the live row still names it and its
// history followed it by never having moved.
func TestUpsertRepoTakesAStampedRow(t *testing.T) {
	d := openTestDB(t)
	live := repoDirOnly(t, d, repoDir)

	first := upsertBinding(t, d, Binding{
		Name: "site-plan-r1", RepoID: &live, CWD: "/home/x/site",
		BuilderMode: "headless", CreatedAt: repoSeenAt, IngestSource: IngestLive,
	})
	second := upsertBinding(t, d, Binding{
		Name: "site-build", RepoID: &live, CWD: "/home/x/site",
		BuilderMode: "headless", CreatedAt: repoSeenAt.Add(time.Hour), IngestSource: IngestLive,
	})

	if _, err := d.UpsertRepo(Repo{OriginURL: ptr(repoURL), CommonDir: ptr(repoDir), FirstSeen: repoSeenAt}); err != nil {
		t.Fatalf("UpsertRepo: %v", err)
	}

	for _, id := range []string{first, second} {
		var got sql.Null[string]
		if err := d.sqlDB.QueryRow(`SELECT repo_id FROM binding WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatalf("read binding %s: %v", id, err)
		}
		if got.V != live {
			t.Errorf("binding %s repo_id = %q, want the live row %q: the upsert moved it", id, got.V, live)
		}
	}
	if n := countRepos(t, d); n != 1 {
		t.Errorf("repo rows = %d, want 1: a row was dropped", n)
	}
}

// TestUpsertRepoStampsAnUnstampedRow is the origin half of the same rule.
//
// The lookup scope reaches a row this origin has not stamped, which is what a
// row written before the origin column existed looks like. Resolving it must not
// insert a second row beside it, and resolving it must leave it stamped: a row
// this origin has adopted but not stamped would fail the origin gate on the next
// pass.
func TestUpsertRepoStampsAnUnstampedRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	d, err := OpenWith(path, Options{Origin: "01AAAAAAAAAAAAAAAAAAAAAAAA"})
	if err != nil {
		t.Fatalf("OpenWith: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	var unstamped string
	if _, err := d.sqlDB.Exec(
		`INSERT INTO repo (id, origin, origin_url, common_dir, first_seen) VALUES (?, '', NULL, ?, ?)`,
		NewID(), repoDir, formatTime(repoSeenAt)); err != nil {
		t.Fatalf("seed the unstamped repo row: %v", err)
	}
	if err := d.sqlDB.QueryRow(`SELECT id FROM repo WHERE origin = '' AND common_dir = ?`, repoDir).
		Scan(&unstamped); err != nil {
		t.Fatalf("read the unstamped id: %v", err)
	}

	id, err := d.UpsertRepo(Repo{OriginURL: ptr(repoURL), CommonDir: ptr(repoDir), FirstSeen: repoSeenAt})
	if err != nil {
		t.Fatalf("UpsertRepo: %v", err)
	}
	if id != unstamped {
		t.Errorf("UpsertRepo id = %s, want the unstamped row %s", id, unstamped)
	}
	row := readRepoRow(t, d, unstamped)
	if row.origin == "" {
		t.Errorf("origin is still '', want this handle's origin: the row was adopted but not stamped")
	}
	if row.originURL.V != repoURL {
		t.Errorf("origin_url = %q, want %q", row.originURL.V, repoURL)
	}
}

// TestUpsertRepoPrefersTheRowHoldingTheURL pins the one case this leaves
// unresolved, so that it is a decision and not an accident.
//
// Two rows can each hold one of the two keys: one holds the URL, one holds the
// dir, and they are the same checkout recorded twice. The URL row wins, because
// a URL is the more stable of the two keys and the dir row may be a stale path.
// The dir is not moved onto it: that would take the dir away from the row that
// holds it, and the bindings naming that row have history attached to it.
// Merging the two is the repoint pass's job, and this call declines it rather
// than refusing the upsert.
func TestUpsertRepoPrefersTheRowHoldingTheURL(t *testing.T) {
	d := openTestDB(t)

	byURL, err := d.UpsertRepo(Repo{OriginURL: ptr(repoURL), FirstSeen: repoSeenAt})
	if err != nil {
		t.Fatalf("seed the url-only row: %v", err)
	}
	byDir := repoDirOnly(t, d, repoDir)

	id, err := d.UpsertRepo(Repo{OriginURL: ptr(repoURL), CommonDir: ptr(repoDir), FirstSeen: repoSeenAt})
	if err != nil {
		t.Fatalf("UpsertRepo: %v", err)
	}
	if id != byURL {
		t.Errorf("UpsertRepo id = %s, want the url row %s", id, byURL)
	}
	if n := countRepos(t, d); n != 2 {
		t.Errorf("repo rows = %d, want 2: this call merges nothing", n)
	}
	if row := readRepoRow(t, d, byURL); row.dir.Valid {
		t.Errorf("the url row took the dir: common_dir = %q; the dir was repointed", row.dir.V)
	}
	if row := readRepoRow(t, d, byDir); row.originURL.Valid {
		t.Errorf("the dir row took the url: origin_url = %q; the url was repointed", row.originURL.V)
	}
}

// TestUpsertRepoRefusesARecordWithNoKey keeps the refusal that was there: a
// record carrying neither column is not a repo, and the fallback must not turn
// that into an insert of two NULLs.
func TestUpsertRepoRefusesARecordWithNoKey(t *testing.T) {
	d := openTestDB(t)

	_, err := d.UpsertRepo(Repo{FirstSeen: repoSeenAt})
	if err == nil {
		t.Fatal("UpsertRepo accepted a record with neither key")
	}
	if !strings.Contains(err.Error(), "needs OriginURL or CommonDir") {
		t.Errorf("err = %v, want the no-key refusal", err)
	}
	if n := countRepos(t, d); n != 0 {
		t.Errorf("repo rows = %d, want 0", n)
	}
}

// TestUpsertRepoSeparatesOrigins is the per-origin half of the key: the same dir
// under a second origin is a second row, and a lookup in either origin finds
// only its own.
func TestUpsertRepoSeparatesOrigins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	first, other := openTwoOrigins(t, path)
	firstID := repoDirOnly(t, first, repoDir)

	second, err := other.UpsertRepo(Repo{OriginURL: ptr(repoURL), CommonDir: ptr(repoDir), FirstSeen: repoSeenAt})
	if err != nil {
		t.Fatalf("UpsertRepo on the second origin: %v", err)
	}
	if second == firstID {
		t.Fatalf("both origins resolved to %s: the key is not scoped by origin", second)
	}
	if n := countRepos(t, first); n != 2 {
		t.Errorf("repo rows = %d, want 2: one per origin", n)
	}
}

func TestUpsertRepoFollowsRemoteRename(t *testing.T) {
	d := openTestDB(t)
	const oldURL = "https://github.com/o/old"
	id := upsertRepo(t, d, Repo{OriginURL: ptr(oldURL), CommonDir: ptr(repoDir), FirstSeen: repoSeenAt})

	got := upsertRepo(t, d, Repo{OriginURL: ptr(repoURL), CommonDir: ptr(repoDir), FirstSeen: repoSeenAt})
	if got != id {
		t.Errorf("UpsertRepo id = %s, want the dir's row %s", got, id)
	}
	if n := countRepos(t, d); n != 1 {
		t.Errorf("repo rows = %d, want 1", n)
	}
	if row := readRepoRow(t, d, id); row.originURL.V != repoURL {
		t.Errorf("origin_url = %q, want the renamed %q", row.originURL.V, repoURL)
	}
}

func TestUpsertRepoURLHitWinsOverRenamedDirRow(t *testing.T) {
	d := openTestDB(t)
	const oldURL = "https://github.com/o/old"
	dirRow := upsertRepo(t, d, Repo{OriginURL: ptr(oldURL), CommonDir: ptr(repoDir), FirstSeen: repoSeenAt})
	urlRow := upsertRepo(t, d, Repo{OriginURL: ptr(repoURL), CommonDir: ptr("/other/.git"), FirstSeen: repoSeenAt})

	got := upsertRepo(t, d, Repo{OriginURL: ptr(repoURL), CommonDir: ptr(repoDir), FirstSeen: repoSeenAt})
	if got != urlRow {
		t.Errorf("UpsertRepo id = %s, want the URL's row %s", got, urlRow)
	}
	if n := countRepos(t, d); n != 2 {
		t.Errorf("repo rows = %d, want 2: no merge", n)
	}
	if row := readRepoRow(t, d, dirRow); row.originURL.V != oldURL {
		t.Errorf("the dir's row origin_url = %q, want it left at %q", row.originURL.V, oldURL)
	}
}
