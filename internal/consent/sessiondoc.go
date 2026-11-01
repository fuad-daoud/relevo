package consent

import (
	"encoding/json"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// SessionDoc is the session consent document as a synclog document: one key per
// harness kind and session id, one value per that session's own answer.
//
// The told baseline sits in the same row and does not travel: sending it would
// tell a peer about a delivery it never made. Every table it touches is
// machine-local, so a write, its stamp and its signal are one transaction.
type SessionDoc struct {
	local *db.DB
}

// OpenSessions binds the session consent document to one machine's local file,
// which is where the session answers, the write stamps and the export signal all
// live.
func OpenSessions(local *db.DB) *SessionDoc {
	if local == nil {
		return &SessionDoc{}
	}
	return &SessionDoc{local: local.LocalOrSelf()}
}

// Name is the document's name, the word an entry's table carries after `doc:`.
func (d *SessionDoc) Name() string { return db.SessionConsentDoc }

// Items returns the document as one value per session with an answer of its own.
//
// A read error is returned rather than answered with an empty document, for the
// same reason the repository document does it: exporting an empty document would
// clear every session answer on every machine.
func (d *SessionDoc) Items() (map[string]json.RawMessage, error) {
	rows, err := d.local.SessionConsents()
	if err != nil {
		return nil, err
	}
	out := make(map[string]json.RawMessage, len(rows))
	for _, r := range rows {
		raw, err := encode(r.Answer, r.At)
		if err != nil {
			return nil, err
		}
		out[r.Kind+"/"+r.SessionID] = raw
	}
	return out, nil
}

// Apply writes one session's answer, or clears it when value is nil, in one
// transaction with nothing else.
func (d *SessionDoc) Apply(key string, value json.RawMessage) error {
	if _, _, err := sessionKey(key); err != nil {
		return err
	}
	if value != nil {
		if _, err := decodeValue(db.SessionConsentDoc, key, value); err != nil {
			return err
		}
	}
	return d.local.Tx(func(tx *db.Tx) error { return d.applyIn(tx, key, value) })
}

// applyIn writes the answer and its time; told and told_at are not named, so
// the local baseline this machine has for the session survives an arriving
// answer.
func (d *SessionDoc) applyIn(tx *db.Tx, key string, value json.RawMessage) error {
	kind, sessionID, err := sessionKey(key)
	if err != nil {
		return err
	}
	answer, at := db.ConsentUnset, time.Now().UTC()
	if value != nil {
		v, err := decodeValue(db.SessionConsentDoc, key, value)
		if err != nil {
			return err
		}
		answer, at = v.Answer, v.At
	}
	return tx.SetSessionConsent(kind, sessionID, answer, at)
}

// ApplyIn is Apply for an item that arrived from another machine, inside the
// importer's own transaction. The value is checked before anything is written,
// so an answer this machine has no reader for leaves the row as it was. An
// arriving answer records no write stamp: the peer wrote it over there.
func (d *SessionDoc) ApplyIn(tx *db.Tx, key string, value json.RawMessage, label string) (refusal error, err error) {
	if _, _, kerr := sessionKey(key); kerr != nil {
		return kerr, nil
	}
	if value != nil {
		if _, verr := decodeValue(db.SessionConsentDoc, key, value); verr != nil {
			return verr, nil
		}
	}
	return nil, d.applyIn(tx, key, value)
}

// SupersedeIn records nothing, for the reason the repository document's does.
func (d *SessionDoc) SupersedeIn(tx *db.Tx, key string, value json.RawMessage, label, why string) error {
	return nil
}

// ItemWriteStamp is the stamp recorded when key was last written on this
// machine. An answer that arrived from a peer records none.
func (d *SessionDoc) ItemWriteStamp(key string) (synclog.Stamp, bool, error) {
	text, ok, err := d.local.ConfigWriteStamp(db.SessionConsentDoc + "/" + key)
	if err != nil || !ok {
		return 0, false, err
	}
	stamp, err := synclog.ParseStamp(text)
	return stamp, err == nil, err
}

// ItemWriteStampIn is ItemWriteStamp read inside the importer's transaction.
func (d *SessionDoc) ItemWriteStampIn(tx *db.Tx, key string) (synclog.Stamp, bool, error) {
	text, ok, err := tx.ConfigWriteStamp(db.SessionConsentDoc + "/" + key)
	if err != nil || !ok {
		return 0, false, err
	}
	stamp, err := synclog.ParseStamp(text)
	return stamp, err == nil, err
}
