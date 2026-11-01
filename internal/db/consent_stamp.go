package db

// The sync half of a consent write: what one person's answer leaves behind
// besides the answer itself.
//
// The two answers sync as two documents because their backing stores differ --
// a repository answer is a column in the shared file, a session answer a row
// in the machine-local file -- so each carries its own per-key export and
// applied state. A stamp says this machine last wrote the key, so a local
// write outranks a peer's earlier entry. Only a person's edit of the answer
// stamps: the housekeeping around it records nothing.

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// The names the two consent documents travel under, as `doc:consent` and
// `doc:session_consent` on the log. They are declared here because this is
// where a write records the stamp and the signal naming them, and the stamp
// key of one document must never be readable as another's: no config section
// is named after either, so a config key and a consent key never meet in the
// one stamp table.
const (
	// ConsentDoc is the document carrying one repository's answer, keyed by its
	// remote URL.
	ConsentDoc = "consent"
	// SessionConsentDoc is the document carrying one session's own answer, keyed
	// by harness kind and session id.
	SessionConsentDoc = "session_consent"
)

// StampIssuer issues the write stamp for one document key, inside the
// transaction of the write it belongs to. It is a seam rather than a direct
// call because this package cannot import internal/synclog -- the log imports
// this one -- and the package that owns the documents installs the issuer, so
// the ordering rule is written once and both documents and both tables obey it.
type StampIssuer func(tx *Tx) (string, error)

var stampIssuer StampIssuer

// SetStampIssuer installs the function that issues document write stamps. It is
// called once at start-up, before any writer exists; an issuer that is not
// installed leaves a write stamped by nothing, which costs the write its
// place in the ordering rather than its value.
func SetStampIssuer(f StampIssuer) { stampIssuer = f }

// consentStampKey is the write-stamp key of one repository answer. The prefix
// keeps a consent stamp from being read as a config section's: both live in
// the one table, and no config section is named after a document.
func consentStampKey(url string) string { return ConsentDoc + "/" + url }

// sessionConsentStampKey is the write-stamp key of one session's answer.
func sessionConsentStampKey(kind, sessionID string) string {
	return SessionConsentDoc + "/" + kind + "/" + sessionID
}

// stampConsentKey records the write stamp and the export signal for one consent
// key in the caller's transaction, so a stamp is never spent on an answer the
// machine did not keep. An empty key stamps nothing: there is no document key
// to stamp. stampKey is the full `<doc>/<key>` built by the helper above
// rather than assembled here, so a write stamps the same key the document
// reads.
func stampConsentKey(t *Tx, doc, stampKey string) error {
	if stampKey == "" {
		return nil
	}
	if stampIssuer == nil {
		return fmt.Errorf("db: stamp %s: no stamp issuer is installed: %w", stampKey, ErrInvalid)
	}
	stamp, err := stampIssuer(t)
	if err != nil {
		return err
	}
	if err := t.ConfigWriteStampPut(stampKey, stamp); err != nil {
		return err
	}
	return t.SignalDoc(doc)
}

// RepoConsentRow is one repository's answer as this machine reads it: the
// remote URL it is keyed by, the answer, and when the answer was written.
//
// It carries the URL rather than a repo row because the document's key is the
// URL, and a row this machine does not own is not an answer of this machine's.
type RepoConsentRow struct {
	OriginURL string
	Answer    Consent
	At        time.Time
}

// RepoConsents lists the repository answers this machine wrote, one row per
// remote URL, for the consent document to export. The scope is this origin's,
// exactly as RepoConsent's is: only its own row answers for it. A row with no
// remote URL is left out -- there is no key to carry it under. An answer
// stored without a readable time, or one this build cannot read, is an error
// rather than a skipped row: exporting the rest here would read as this
// machine having no answer for that key and clear it on every machine.
func (d *DB) RepoConsents() ([]RepoConsentRow, error) {
	rows, err := d.sqlDB.QueryContext(context.Background(),
		`SELECT origin_url, mastermind_consent, consent_at FROM repo
		 WHERE `+originScope+`
		   AND origin_url IS NOT NULL AND origin_url <> ''
		   AND mastermind_consent IS NOT NULL AND mastermind_consent <> ''
		 ORDER BY origin_url`, d.origin)
	if err != nil {
		return nil, fmt.Errorf("db: repo consents: %w", mapBusy(err))
	}
	defer func() { _ = rows.Close() }()
	var out []RepoConsentRow
	for rows.Next() {
		var url, answer string
		var atText sql.NullString
		if err := rows.Scan(&url, &answer, &atText); err != nil {
			return nil, fmt.Errorf("db: repo consents: %w", err)
		}
		c := Consent(answer)
		if err := c.Valid(); err != nil {
			return nil, fmt.Errorf("db: repo consents %s: %w", url, err)
		}
		at, err := consentAt(url, atText)
		if err != nil {
			return nil, err
		}
		out = append(out, RepoConsentRow{OriginURL: url, Answer: c, At: at})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: repo consents: %w", err)
	}
	return out, nil
}

// SessionConsentRow is one session's own answer as this machine reads it: the
// kind and session id it is keyed by, the answer, and when it was written.
type SessionConsentRow struct {
	Kind      string
	SessionID string
	Answer    Consent
	At        time.Time
}

// SessionConsents lists the session answers this machine wrote, one row per
// (kind, session id), for the session consent document to export. The told
// baseline in the same row stays out: it reports a delivery to this session
// on this host, which a peer never made. A row with no answer is left out:
// unset is the absence of an answer, carried as a key the document no longer
// holds rather than as a value.
func (d *DB) SessionConsents() ([]SessionConsentRow, error) {
	rows, err := d.LocalOrSelf().sqlDB.QueryContext(context.Background(),
		`SELECT harness_kind, session_id, answer, answer_at FROM session_consent
		 WHERE answer IS NOT NULL AND answer <> ''
		 ORDER BY harness_kind, session_id`)
	if err != nil {
		return nil, fmt.Errorf("db: session consents: %w", mapBusy(err))
	}
	defer func() { _ = rows.Close() }()
	var out []SessionConsentRow
	for rows.Next() {
		var kind, id, answer string
		var atText sql.NullString
		if err := rows.Scan(&kind, &id, &answer, &atText); err != nil {
			return nil, fmt.Errorf("db: session consents: %w", err)
		}
		c := Consent(answer)
		if err := c.Valid(); err != nil {
			return nil, fmt.Errorf("db: session consents %s/%s: %w", kind, id, err)
		}
		at, err := consentAt(kind+"/"+id, atText)
		if err != nil {
			return nil, err
		}
		out = append(out, SessionConsentRow{Kind: kind, SessionID: id, Answer: c, At: at})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: session consents: %w", err)
	}
	return out, nil
}

// consentAt is the time a stored answer carries. An answer with no time is
// refused rather than read as the zero time: the documents order keys by when
// they were written, and no write precedes the zero time.
func consentAt(key string, text sql.NullString) (time.Time, error) {
	if !text.Valid {
		return time.Time{}, fmt.Errorf("db: consent %s: an answer with no time: %w", key, ErrInvalid)
	}
	at, err := parseTime(text.String)
	if err != nil {
		return time.Time{}, fmt.Errorf("db: consent %s: %w", key, err)
	}
	return at, nil
}
