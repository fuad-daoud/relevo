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
	return repoConsent(context.Background(), d.sqlDB, d.origin, ref)
}

// repoConsent reads ref's answer from the rows of one installation. The scope
// is this origin's: only the row this installation wrote is this machine's
// answer, and reading a peer's row would answer for a repository a person
// never asked this machine about. originScope is the scope every other repo
// read uses, so an unstamped row this machine wrote still answers here,
// awaiting its backfill.
func repoConsent(ctx context.Context, q queryer, origin string, ref Repo) (Consent, error) {
	for _, k := range repoKeys(ref) {
		var raw sql.Null[string]
		err := q.QueryRowContext(ctx,
			`SELECT mastermind_consent FROM repo WHERE `+originScope+` AND `+k.col+` = ?`,
			origin, k.val).Scan(&raw)
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
//
// It is a person's edit of the answer, so it stamps the consent document's key
// and signals that document. The row and the stamp cannot share a transaction:
// the row is in the shared file, the stamp and the signal in the machine-local
// one. The row is written first, so a failure between them leaves an answer
// this machine holds and has not yet told anyone about, which the next consent
// write of any key sends. The reverse order would spend a stamp on an answer
// that never landed.
func (d *DB) SetRepoConsent(ref Repo, c Consent, now time.Time) (string, error) {
	var id string
	err := d.Tx(func(t *Tx) error {
		var err error
		id, err = t.SetRepoConsent(ref, c, now)
		return err
	})
	if err != nil {
		return "", err
	}
	// A repository with no remote URL has no document key, so it has nothing
	// to stamp and nothing to export: a peer can be asked about a URL both
	// machines can name, not about a checkout only this machine has.
	if key := consentKey(ref); key != "" {
		if err := d.LocalOrSelf().Tx(func(t *Tx) error {
			return stampConsentKey(t, ConsentDoc, consentStampKey(key))
		}); err != nil {
			return id, err
		}
	}
	return id, nil
}

// consentKey is the consent document's key for a repository: its remote URL, and
// the empty string for a ref naming none. The URL is the key rather than the
// common dir because the dir is this machine's path: a peer that stored it would
// be holding a path that means nothing there.
func consentKey(ref Repo) string {
	if ref.OriginURL != nil {
		return *ref.OriginURL
	}
	return ""
}

// SetRepoConsent is SetRepoConsent inside tx, without a stamp: the caller is a
// write that already holds the transaction, or an arrival from a peer, which
// records none.
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
// has not answered, for one relevo never saw, or for a session with no row at
// all. The repository's answer is not consulted here: the row is this
// machine's, and it says nothing about a session of the same id elsewhere.
func (d *DB) SessionConsent(kind, sessionID string) (Consent, error) {
	return sessionConsent(context.Background(), d.LocalOrSelf().sqlDB, kind, sessionID)
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
// UPDATE-then-INSERT inside one transaction, so two calls serialise, and the
// stamp and the export signal are recorded there too: a stamp spent on an
// answer that then failed to keep would outrank the answer a peer recorded
// meanwhile.
func (d *DB) SetSessionConsent(kind, sessionID string, c Consent, now time.Time) error {
	if err := c.Valid(); err != nil {
		return err
	}
	if kind == "" || sessionID == "" {
		return fmt.Errorf("db: session consent: a session needs both a kind and an id: %w", ErrInvalid)
	}
	return d.LocalOrSelf().Tx(func(t *Tx) error {
		if err := t.SetSessionConsent(kind, sessionID, c, now); err != nil {
			return err
		}
		return stampConsentKey(t, SessionConsentDoc, sessionConsentStampKey(kind, sessionID))
	})
}

// SetSessionConsent is SetSessionConsent inside the caller's transaction,
// without a stamp: the caller is a write whose key is this machine's to stamp,
// or an arrival from a peer, which records none.
func (t *Tx) SetSessionConsent(kind, sessionID string, c Consent, now time.Time) error {
	if err := c.Valid(); err != nil {
		return err
	}
	var answer, at any
	if c != ConsentUnset {
		answer, at = string(c), formatTime(now)
	}
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
}

// SessionTold reads the status token last delivered to a session. ok is false
// when no baseline was ever written. The baseline is this machine's and lives
// with the consent beside it.
func (d *DB) SessionTold(kind, sessionID string) (told string, ok bool, err error) {
	return sessionTold(context.Background(), d.LocalOrSelf().sqlDB, kind, sessionID)
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
	d = d.LocalOrSelf()
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
// next status read becomes a fresh baseline. It stamps the session consent key
// and signals the document: a cleared answer is a key the document no longer
// holds, and the peers that still hold one have to be told to lift it.
// Everything here is machine-local, so the write, its stamp and its signal
// are one transaction.
func (d *DB) ClearSessionConsent(kind, sessionID string) error {
	return d.LocalOrSelf().Tx(func(t *Tx) error {
		if _, err := t.exec(
			`UPDATE session_consent SET answer = NULL, answer_at = NULL, told = NULL, told_at = NULL WHERE harness_kind = ? AND session_id = ?`,
			kind, sessionID); err != nil {
			return fmt.Errorf("db: session consent: %w", mapBusy(err))
		}
		return stampConsentKey(t, SessionConsentDoc, sessionConsentStampKey(kind, sessionID))
	})
}
