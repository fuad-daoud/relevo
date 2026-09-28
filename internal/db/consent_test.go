package db

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func consentNow() time.Time { return time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC) }

// TestRepoConsentUnsetReadsUnset pins the default: a repo relevo has never
// seen, or a ref naming no key, answers unset and never errors.
func TestRepoConsentUnsetReadsUnset(t *testing.T) {
	d := openTestDB(t)
	for _, ref := range []Repo{
		{OriginURL: ptr("https://github.com/o/r"), CommonDir: ptr("/repo/.git")},
		{},
		{OriginURL: ptr("")},
	} {
		c, err := d.RepoConsent(ref)
		if err != nil {
			t.Fatalf("RepoConsent(%+v): %v", ref, err)
		}
		if c != ConsentUnset {
			t.Errorf("RepoConsent(%+v) = %q, want unset", ref, c)
		}
	}
}

// TestRepoConsentRoundTrip pins the keys: an answer written through a ref with
// both keys reads back through either, a common-dir-only repo works, and the
// last answer wins.
func TestRepoConsentRoundTrip(t *testing.T) {
	d := openTestDB(t)
	now := consentNow()

	if _, err := d.SetRepoConsent(Repo{OriginURL: ptr("https://github.com/o/r"), CommonDir: ptr("/repo/.git")}, ConsentYes, now); err != nil {
		t.Fatalf("SetRepoConsent yes: %v", err)
	}
	for _, ref := range []Repo{
		{OriginURL: ptr("https://github.com/o/r")},
		{CommonDir: ptr("/repo/.git")},
	} {
		c, err := d.RepoConsent(ref)
		if err != nil {
			t.Fatalf("RepoConsent(%+v): %v", ref, err)
		}
		if c != ConsentYes {
			t.Errorf("RepoConsent(%+v) = %q, want yes", ref, c)
		}
	}

	if _, err := d.SetRepoConsent(Repo{CommonDir: ptr("/other/.git")}, ConsentNo, now); err != nil {
		t.Fatalf("SetRepoConsent no: %v", err)
	}
	if _, err := d.SetRepoConsent(Repo{CommonDir: ptr("/other/.git")}, ConsentYes, now.Add(time.Hour)); err != nil {
		t.Fatalf("SetRepoConsent overwrite: %v", err)
	}
	// A ref with an unknown origin but the known common dir falls back to the
	// second key and still finds the row.
	c, err := d.RepoConsent(Repo{OriginURL: ptr("https://github.com/o/else"), CommonDir: ptr("/other/.git")})
	if err != nil {
		t.Fatalf("RepoConsent fallback: %v", err)
	}
	if c != ConsentYes {
		t.Errorf("RepoConsent fallback = %q, want yes", c)
	}

	// An ingest upsert fills a missing column and must not clear the answer.
	if _, err := d.UpsertRepo(Repo{OriginURL: ptr("https://github.com/o/r"), CommonDir: ptr("/repo/.git"), FirstSeen: now}); err != nil {
		t.Fatalf("UpsertRepo: %v", err)
	}
	c, err = d.RepoConsent(Repo{OriginURL: ptr("https://github.com/o/r")})
	if err != nil {
		t.Fatalf("RepoConsent after upsert: %v", err)
	}
	if c != ConsentYes {
		t.Errorf("consent after upsert = %q, want yes", c)
	}
}

// TestSetRepoConsentClearsUnset pins that unset is storable: reset returns a
// repo to the ask state, and a refused value writes nothing.
func TestSetRepoConsentClearsUnset(t *testing.T) {
	d := openTestDB(t)
	ref := Repo{CommonDir: ptr("/repo/.git")}

	if _, err := d.SetRepoConsent(ref, ConsentYes, consentNow()); err != nil {
		t.Fatalf("SetRepoConsent yes: %v", err)
	}
	if _, err := d.SetRepoConsent(ref, ConsentUnset, consentNow()); err != nil {
		t.Fatalf("SetRepoConsent unset: %v", err)
	}
	c, err := d.RepoConsent(ref)
	if err != nil {
		t.Fatalf("RepoConsent: %v", err)
	}
	if c != ConsentUnset {
		t.Errorf("consent after reset = %q, want unset", c)
	}

	_, err = d.SetRepoConsent(ref, Consent("maybe"), consentNow())
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("SetRepoConsent(maybe) error = %v, want ErrInvalid", err)
	}
	c, err = d.RepoConsent(ref)
	if err != nil {
		t.Fatalf("RepoConsent: %v", err)
	}
	if c != ConsentUnset {
		t.Errorf("consent after refused write = %q, want unset", c)
	}
}

// TestMigration010AddsRepoConsent pins the upgrade: a v9 database with a repo
// row gains the two columns at 010, the old row reads unset, and a second
// apply is a no-op.
func TestMigration010AddsRepoConsent(t *testing.T) {
	sqlDB := rawSQLDB(t, filepath.Join(t.TempDir(), "relevo.db"))
	if err := applyMigrations(sqlDB, migrationFilesUpTo(t, 9)); err != nil {
		t.Fatalf("applyMigrations through 009: %v", err)
	}
	if _, err := sqlDB.Exec(`INSERT INTO repo (id, origin_url, common_dir, first_seen)
		VALUES ('r1', 'https://github.com/o/r', '/repo/.git', '2026-09-01T00:00:00.000Z')`); err != nil {
		t.Fatalf("insert repo: %v", err)
	}

	ten := migrationFilesUpTo(t, 10)
	if err := applyMigrations(sqlDB, ten); err != nil {
		t.Fatalf("applyMigrations 010: %v", err)
	}
	c, err := repoConsent(context.Background(), sqlDB, Repo{OriginURL: ptr("https://github.com/o/r")})
	if err != nil {
		t.Fatalf("repoConsent after 010: %v", err)
	}
	if c != ConsentUnset {
		t.Errorf("existing repo consent = %q, want unset", c)
	}

	if err := applyMigrations(sqlDB, ten); err != nil {
		t.Fatalf("second applyMigrations 010: %v", err)
	}
	var rows int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM schema_version WHERE version = 10`).Scan(&rows); err != nil {
		t.Fatalf("count schema_version: %v", err)
	}
	if rows != 1 {
		t.Errorf("schema_version rows for 10 = %d, want 1", rows)
	}
}
