package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Consent is the answer a repository has given to "may relevo register this
// repo's sessions as MasterMinds?". The zero value is unset: the ask state.
type Consent string

const (
	ConsentUnset Consent = ""
	ConsentYes   Consent = "yes"
	ConsentNo    Consent = "no"
)

// Valid reports whether c is a storable value. Unset is valid: it is the
// absence of an answer, and a read returns it for a repo that never answered.
func (c Consent) Valid() error {
	switch c {
	case ConsentUnset, ConsentYes, ConsentNo:
		return nil
	}
	return fmt.Errorf("db: consent %q must be yes, no, or empty: %w", c, ErrInvalid)
}

// RepoConsent reads ref's answer: ConsentUnset for a repo relevo has never
// seen, for one that has not answered, or for a ref naming neither key.
func (d *DB) RepoConsent(ref Repo) (Consent, error) {
	return repoConsent(context.Background(), d.sqlDB, ref)
}

func repoConsent(ctx context.Context, q queryer, ref Repo) (Consent, error) {
	for _, k := range repoKeys(ref) {
		var raw sql.Null[string]
		err := q.QueryRowContext(ctx, `SELECT mastermind_consent FROM repo WHERE `+k.col+` = ?`, k.val).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return ConsentUnset, fmt.Errorf("db: repo consent: %w", err)
		}
		c := Consent(raw.V) // NULL and '' both read as unset
		if err := c.Valid(); err != nil {
			return ConsentUnset, err
		}
		return c, nil
	}
	return ConsentUnset, nil
}

type repoKey struct{ col, val string }

// repoKeys are ref's natural keys in lookup order: origin_url first, then
// common_dir, the order UpsertRepo prefers. An absent or empty key is skipped.
func repoKeys(ref Repo) []repoKey {
	var keys []repoKey
	if ref.OriginURL != nil && *ref.OriginURL != "" {
		keys = append(keys, repoKey{"origin_url", *ref.OriginURL})
	}
	if ref.CommonDir != nil && *ref.CommonDir != "" {
		keys = append(keys, repoKey{"common_dir", *ref.CommonDir})
	}
	return keys
}

// SetRepoConsent stores ref's answer, creating the repo row when relevo has
// not seen the repo, and returns the row's id. ConsentUnset clears the answer:
// the next session in the repository asks again.
func (d *DB) SetRepoConsent(ref Repo, c Consent, now time.Time) (string, error) {
	var id string
	err := d.Tx(func(t *Tx) error {
		var err error
		id, err = t.SetRepoConsent(ref, c, now)
		return err
	})
	return id, err
}

// SetRepoConsent is SetRepoConsent inside tx.
func (t *Tx) SetRepoConsent(ref Repo, c Consent, now time.Time) (string, error) {
	if err := c.Valid(); err != nil {
		return "", err
	}
	id, err := t.UpsertRepo(Repo{OriginURL: ref.OriginURL, CommonDir: ref.CommonDir, FirstSeen: now})
	if err != nil {
		return "", err
	}
	var answer, at any
	if c != ConsentUnset {
		answer, at = string(c), formatTime(now)
	}
	if _, err := t.exec(`UPDATE repo SET mastermind_consent = ?, consent_at = ? WHERE id = ?`,
		answer, at, id); err != nil {
		return "", fmt.Errorf("db: repo consent: %w", mapBusy(err))
	}
	return id, nil
}
