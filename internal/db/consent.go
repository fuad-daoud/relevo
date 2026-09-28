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

// SessionConsent reads a session's own answer: ConsentUnset for a session that
// has not answered, for one relevo has never seen, or for a session with no
// row at all. The repository's answer is not consulted here.
func (d *DB) SessionConsent(kind, sessionID string) (Consent, error) {
	return sessionConsent(context.Background(), d.sqlDB, kind, sessionID)
}

func sessionConsent(ctx context.Context, q queryer, kind, sessionID string) (Consent, error) {
	var raw sql.Null[string]
	err := q.QueryRowContext(ctx,
		`SELECT answer FROM session_consent WHERE harness_kind = ? AND session_id = ?`, kind, sessionID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return ConsentUnset, nil
	}
	if err != nil {
		return ConsentUnset, fmt.Errorf("db: session consent: %w", err)
	}
	c := Consent(raw.V) // NULL and '' both read as unset
	if err := c.Valid(); err != nil {
		return ConsentUnset, err
	}
	return c, nil
}

// SetSessionConsent stores a session's own answer, creating the row when relevo
// has not seen the session. ConsentUnset clears the answer. The write is
// UPDATE-then-INSERT inside one transaction, so two calls serialise.
func (d *DB) SetSessionConsent(kind, sessionID string, c Consent, now time.Time) error {
	if err := c.Valid(); err != nil {
		return err
	}
	var answer, at any
	if c != ConsentUnset {
		answer, at = string(c), formatTime(now)
	}
	return d.Tx(func(t *Tx) error {
		res, err := t.exec(
			`UPDATE session_consent SET answer = ?, answer_at = ? WHERE harness_kind = ? AND session_id = ?`,
			answer, at, kind, sessionID)
		if err != nil {
			return fmt.Errorf("db: session consent: %w", mapBusy(err))
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("db: session consent: %w", err)
		}
		if n > 0 {
			return nil
		}
		if _, err := t.exec(
			`INSERT INTO session_consent (harness_kind, session_id, answer, answer_at) VALUES (?, ?, ?, ?)`,
			kind, sessionID, answer, at); err != nil {
			return fmt.Errorf("db: session consent: %w", mapBusy(err))
		}
		return nil
	})
}

// SessionTold reads the status token last delivered to a session. ok is false
// when no baseline was ever written: a session from before this table existed,
// or one whose baseline write did not happen.
func (d *DB) SessionTold(kind, sessionID string) (told string, ok bool, err error) {
	return sessionTold(context.Background(), d.sqlDB, kind, sessionID)
}

func sessionTold(ctx context.Context, q queryer, kind, sessionID string) (string, bool, error) {
	var raw sql.Null[string]
	err := q.QueryRowContext(ctx,
		`SELECT told FROM session_consent WHERE harness_kind = ? AND session_id = ?`, kind, sessionID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("db: session told: %w", err)
	}
	if !raw.Valid {
		return "", false, nil
	}
	return raw.V, true, nil
}

// SetSessionTold records the status token a session has just been told,
// creating the row when relevo has not seen the session. An empty told clears
// the baseline, so the next read reports "never told".
func (d *DB) SetSessionTold(kind, sessionID, told string, now time.Time) error {
	var val, at any
	if told != "" {
		val, at = told, formatTime(now)
	}
	return d.Tx(func(t *Tx) error {
		res, err := t.exec(
			`UPDATE session_consent SET told = ?, told_at = ? WHERE harness_kind = ? AND session_id = ?`,
			val, at, kind, sessionID)
		if err != nil {
			return fmt.Errorf("db: session told: %w", mapBusy(err))
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("db: session told: %w", err)
		}
		if n > 0 {
			return nil
		}
		if _, err := t.exec(
			`INSERT INTO session_consent (harness_kind, session_id, told, told_at) VALUES (?, ?, ?, ?)`,
			kind, sessionID, val, at); err != nil {
			return fmt.Errorf("db: session told: %w", mapBusy(err))
		}
		return nil
	})
}

// ClearSessionConsent forgets a session's own answer and its told baseline in
// one write, so reset returns the session to the repository's answer and the
// next status read becomes a fresh baseline.
func (d *DB) ClearSessionConsent(kind, sessionID string) error {
	return d.Tx(func(t *Tx) error {
		if _, err := t.exec(
			`UPDATE session_consent SET answer = NULL, answer_at = NULL, told = NULL, told_at = NULL WHERE harness_kind = ? AND session_id = ?`,
			kind, sessionID); err != nil {
			return fmt.Errorf("db: session consent: %w", mapBusy(err))
		}
		return nil
	})
}
