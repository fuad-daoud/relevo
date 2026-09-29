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

// TestMigration011AddsSessionConsent pins the upgrade: a v10 database gains the
// session_consent table at 011, an unseen session reads unset with no told
// baseline, and a second apply is a no-op.
func TestMigration011AddsSessionConsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	sqlDB := rawSQLDB(t, path)
	if err := applyMigrations(sqlDB, migrationFilesUpTo(t, 10)); err != nil {
		t.Fatalf("applyMigrations through 010: %v", err)
	}

	eleven := migrationFilesUpTo(t, 11)
	if err := applyMigrations(sqlDB, eleven); err != nil {
		t.Fatalf("applyMigrations 011: %v", err)
	}

	// An unseen session has no row, which reads as unset with no baseline.
	var n int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM session_consent WHERE harness_kind = 'claude' AND session_id = 's1'`).Scan(&n); err != nil {
		t.Fatalf("count session_consent: %v", err)
	}
	if n != 0 {
		t.Errorf("session_consent rows for an unseen session = %d, want 0", n)
	}
	if c, err := sessionConsent(context.Background(), sqlDB, "claude", "s1"); err != nil || c != ConsentUnset {
		t.Errorf("sessionConsent(unseen) = (%q, %v), want unset", c, err)
	}
	if told, ok, err := sessionTold(context.Background(), sqlDB, "claude", "s1"); err != nil || ok || told != "" {
		t.Errorf("sessionTold(unseen) = (%q, %v, %v), want no baseline", told, ok, err)
	}

	// A second apply of 011 is a no-op: CREATE TABLE IF NOT EXISTS, version
	// guard and all.
	if err := applyMigrations(sqlDB, eleven); err != nil {
		t.Fatalf("second applyMigrations 011: %v", err)
	}
	var rows int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM schema_version WHERE version = 11`).Scan(&rows); err != nil {
		t.Fatalf("count schema_version: %v", err)
	}
	if rows != 1 {
		t.Errorf("schema_version rows for 11 = %d, want 1", rows)
	}
}

// TestSessionConsentRoundTrip pins the session answer and told writes on an
// upgraded file: the answer overwrites, told round-trips, and an invalid value
// is refused.
func TestSessionConsentRoundTrip(t *testing.T) {
	d := openTestDB(t)

	if err := d.SetSessionConsent("claude", "s1", ConsentNo, consentNow()); err != nil {
		t.Fatalf("SetSessionConsent no: %v", err)
	}
	if c, err := d.SessionConsent("claude", "s1"); err != nil || c != ConsentNo {
		t.Errorf("SessionConsent = (%q, %v), want no", c, err)
	}
	if err := d.SetSessionConsent("claude", "s1", ConsentYes, consentNow()); err != nil {
		t.Fatalf("SetSessionConsent yes: %v", err)
	}
	if c, err := d.SessionConsent("claude", "s1"); err != nil || c != ConsentYes {
		t.Errorf("SessionConsent after overwrite = (%q, %v), want yes", c, err)
	}

	if err := d.SetSessionTold("claude", "s1", "mastermind:mm_aaaaaaaaaaaa:architect-1", consentNow()); err != nil {
		t.Fatalf("SetSessionTold: %v", err)
	}
	told, ok, err := d.SessionTold("claude", "s1")
	if err != nil || !ok || told != "mastermind:mm_aaaaaaaaaaaa:architect-1" {
		t.Errorf("SessionTold = (%q, %v, %v), want the stored token", told, ok, err)
	}
	// The two columns are independent: the answer survives a told write and the
	// other way round.
	if err := d.SetSessionConsent("claude", "s1", ConsentUnset, consentNow()); err != nil {
		t.Fatalf("SetSessionConsent unset: %v", err)
	}
	if c, err := d.SessionConsent("claude", "s1"); err != nil || c != ConsentUnset {
		t.Errorf("SessionConsent after unset = (%q, %v), want unset", c, err)
	}
	if told, ok, err := d.SessionTold("claude", "s1"); err != nil || !ok || told != "mastermind:mm_aaaaaaaaaaaa:architect-1" {
		t.Errorf("SessionTold after an answer clear = (%q, %v, %v), want it kept", told, ok, err)
	}

	if err := d.SetSessionConsent("claude", "s1", Consent("maybe"), consentNow()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("SetSessionConsent(maybe) error = %v, want ErrInvalid", err)
	}
}

// TestClearSessionConsent pins reset's write: the session's own answer and its
// told baseline go together, without disturbing another session.
func TestClearSessionConsent(t *testing.T) {
	d := openTestDB(t)

	if err := d.SetSessionConsent("claude", "s1", ConsentNo, consentNow()); err != nil {
		t.Fatalf("SetSessionConsent s1: %v", err)
	}
	if err := d.SetSessionTold("claude", "s1", "ask", consentNow()); err != nil {
		t.Fatalf("SetSessionTold s1: %v", err)
	}
	if err := d.SetSessionConsent("claude", "s2", ConsentNo, consentNow()); err != nil {
		t.Fatalf("SetSessionConsent s2: %v", err)
	}

	if err := d.ClearSessionConsent("claude", "s1"); err != nil {
		t.Fatalf("ClearSessionConsent: %v", err)
	}
	if c, err := d.SessionConsent("claude", "s1"); err != nil || c != ConsentUnset {
		t.Errorf("SessionConsent after clear = (%q, %v), want unset", c, err)
	}
	if _, ok, err := d.SessionTold("claude", "s1"); err != nil || ok {
		t.Errorf("SessionTold after clear = (ok %v, %v), want no baseline", ok, err)
	}
	if c, err := d.SessionConsent("claude", "s2"); err != nil || c != ConsentNo {
		t.Errorf("SessionConsent on the other session = (%q, %v), want no", c, err)
	}

	// Clearing an unseen session is a no-op, not an error.
	if err := d.ClearSessionConsent("claude", "s3"); err != nil {
		t.Errorf("ClearSessionConsent(unseen) = %v, want nil", err)
	}
}
