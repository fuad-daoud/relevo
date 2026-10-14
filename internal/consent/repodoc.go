package consent

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// RepoDoc is the repository consent document as a synclog document: one key per
// remote URL, one value per that repository's answer.
//
// It carries only this machine's own row for each URL, and it is bound to the
// shared handle so that is the file it writes. Both halves of that are the same
// decision. A repository row reaches every machine through the row log, so a
// machine holding several installations' rows for one URL holds several answers
// to the same question; writing the document into another installation's row
// would overwrite what a person decided on that machine, and a repository row
// has no stamp to order the two writes by. So a peer does not arrive as a row
// overwrite: it arrives as this document, and this document writes this
// machine's own row for the URL, which is the one RepoConsent reads and the one
// the next person on this machine is answering.
type RepoDoc struct {
	shared *db.DB
	local  *db.DB
}

// OpenRepo binds the repository consent document to one machine's files.
// shared is where the repository rows are; local is where the write stamps and
// the export signal are, and the two never share a transaction.
func OpenRepo(shared *db.DB) *RepoDoc {
	if shared == nil {
		return &RepoDoc{}
	}
	return &RepoDoc{shared: shared, local: shared.LocalOrSelf()}
}

// Name is the document's name, the word an entry's table carries after `doc:`.
func (d *RepoDoc) Name() string { return db.ConsentDoc }

// Items returns the document as one value per remote URL this machine has
// answered for.
//
// A read error is returned rather than answered with an empty document: an
// unreadable table is this machine's own problem to report, and exporting an
// empty consent document would clear every repository answer on every machine.
func (d *RepoDoc) Items() (map[string]json.RawMessage, error) {
	rows, err := d.shared.RepoConsents()
	if err != nil {
		return nil, err
	}
	out := make(map[string]json.RawMessage, len(rows))
	for _, r := range rows {
		raw, err := encode(r.Answer, r.At)
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", db.ConsentDoc, r.OriginURL, err)
		}
		out[r.OriginURL] = raw
	}
	return out, nil
}

// Apply writes one repository's answer, or clears it when value is nil, in one
// transaction with nothing else: an apply is the importer's own transaction in
// ApplyIn, and this is the seam a caller without one uses.
func (d *RepoDoc) Apply(key string, value json.RawMessage) error {
	if err := validURL(key); err != nil {
		return err
	}
	if value != nil {
		if _, err := decodeValue(db.ConsentDoc, key, value); err != nil {
			return err
		}
	}
	return d.apply(key, value)
}

// apply is the write itself, against the file the repository rows live in.
func (d *RepoDoc) apply(key string, value json.RawMessage) error {
	url := key
	answer, at := db.ConsentUnset, time.Time{}
	if value != nil {
		v, err := decodeValue(db.ConsentDoc, key, value)
		if err != nil {
			return err
		}
		answer, at = v.Answer, v.At
	} else {
		// A tombstone clears the answer and the row keeps the rest of itself: the
		// URL, the common dir and the first-seen stamp are not the answer and no
		// peer is asking this machine to forget them.
		at = time.Now().UTC()
	}
	return d.shared.Tx(func(tx *db.Tx) error {
		_, err := tx.SetRepoConsent(db.Repo{OriginURL: &url}, answer, at)
		return err
	})
}

// ApplyIn is Apply for an item that arrived from another machine. The value is
// checked first and refused without touching anything when it fails, so an
// answer this machine has no reader for never leaves the row unreadable behind
// it.
//
// The write goes to this machine's own row for the URL, which is what the
// peer's answer is for: the peer is telling this machine what that repository
// may be registered as, and the row this machine reads back is the one it
// writes here. An arriving answer records no write stamp -- the stamp is what
// says this machine last wrote the key, and the peer wrote it over there.
//
// It does record the export signal, because the row this machine holds has
// changed and the machines that took this machine's earlier answer from the row
// log need to see the key move. That is a plain signal rather than a stamp: the
// signal says the document may have changed, and what it exports is decided by
// the value and the export state, not by the signal's own timestamp.
func (d *RepoDoc) ApplyIn(tx *db.Tx, key string, value json.RawMessage, label string) (refusal error, err error) {
	if err := validURL(key); err != nil {
		return err, nil
	}
	if value != nil {
		if _, verr := decodeValue(db.ConsentDoc, key, value); verr != nil {
			return verr, nil
		}
	}
	return nil, d.apply(key, value)
}

// SupersedeIn records nothing. The consent documents keep no revision log, and
// the importer's own applied state is the record of an item that lost: it moves
// the key's applied stamp to the entry, so an older entry still loses to it and
// a redelivery is a no-op.
func (d *RepoDoc) SupersedeIn(tx *db.Tx, key string, value json.RawMessage, label, why string) error {
	return nil
}

// ItemWriteStamp is the stamp recorded when key was last written on this
// machine. An answer that arrived from a peer records none.
func (d *RepoDoc) ItemWriteStamp(key string) (synclog.Stamp, bool, error) {
	text, ok, err := d.local.ConfigWriteStamp(db.ConsentDoc + "/" + key)
	if err != nil || !ok {
		return 0, false, err
	}
	stamp, err := synclog.ParseStamp(text)
	return stamp, err == nil, err
}

// ItemWriteStampIn is ItemWriteStamp read inside the importer's transaction,
// which is how an arriving answer is weighed against a write made here.
func (d *RepoDoc) ItemWriteStampIn(tx *db.Tx, key string) (synclog.Stamp, bool, error) {
	text, ok, err := tx.ConfigWriteStamp(db.ConsentDoc + "/" + key)
	if err != nil || !ok {
		return 0, false, err
	}
	stamp, err := synclog.ParseStamp(text)
	return stamp, err == nil, err
}
