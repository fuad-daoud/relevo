package availability

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// GatesName is the name the gates document travels under, as `doc:gates` on
// log.
const GatesName = "gates"

// gatesValueMax is the largest gates value that crosses the log. A subject's
// live rate-limit entries are a handful of lines, so the cap sits far above any
// real value and below the point where one entry could be a payload rather than
// a gate.
const gatesValueMax = 64 << 10

// stampKey is the write-stamp key of one subject's gates. The prefix is what
// keeps a gates stamp from being read as a config key's stamp: both live in the
// one table, and no config section is named after a document.
func stampKey(subject string) string { return GatesName + "/" + subject }

// stampedSubjects is the write-stamp subjects one ledger entry claims: its own
// subject when the entry is a rate limit, and nothing when it is a spawn
// failure. A spawn failure is this machine's observation of its own binaries
// failing to start, so there is nothing on another machine for it to say.
func stampedSubjects(e Entry) []string {
	if e.Kind != RateLimited {
		return nil
	}
	return []string{e.Subject}
}

// GatesDoc is the gates document as a synclog document: one key per ledger
// subject, one value per subject's live rate-limit entries.
//
// A spawn failure is not in it. It says this binary could not start a
// candidate here -- a missing executable, a local auth failure, a crash on
// this host -- and none of that is true of another machine, so it stays in
// this machine's ledger and never leaves it. A rate limit is the opposite: a
// provider's quota is the provider's, so every machine gating the same
// provider gates the same candidates, which is what makes it worth sharing. An
// entry this build does not understand is local for the reason the ledger keeps
// it raw: this machine cannot act on it and must not erase it.
//
// It is bound to the machine-local file rather than to a handle chosen by the
// caller, because the ledger row, the per-subject write stamps and the
// document signal all live in that one file and a write has to reach all three.
type GatesDoc struct {
	local *db.DB
	now   func() time.Time
}

// OpenGates binds the gates document to the machine-local file its ledger lives
// in. now is the clock Items prunes against; nil means time.Now.
func OpenGates(local *db.DB, now func() time.Time) *GatesDoc {
	if now == nil {
		now = time.Now
	}
	return &GatesDoc{local: local, now: now}
}

// Name is the document's name, the word an entry's table carries after `doc:`.
func (d *GatesDoc) Name() string { return GatesName }

// Items returns the document as one value per subject this machine holds a live
// rate limit on. A subject with no live rate-limit entry is absent, which the
// exporter reads as a key the document no longer holds and sends as a delete.
//
// A read error is returned rather than answered with an empty document: an
// unreadable ledger is this machine's own problem to report, and exporting an
// empty gates document would clear every gate every other machine holds.
func (d *GatesDoc) Items() (map[string]json.RawMessage, error) {
	l, err := LoadLedger(d.local)
	if err != nil {
		return nil, err
	}
	return gateItems(l.Prune(d.now()))
}

// gateItems is Items over an already-loaded and already-pruned ledger.
func gateItems(l Ledger) (map[string]json.RawMessage, error) {
	bySubject := map[string][]Entry{}
	for _, e := range l.Entries {
		// A spawn failure is this machine's, and an entry this build cannot
		// read is nobody's to hand on. Neither leaves through here.
		if e.Kind != RateLimited {
			continue
		}
		bySubject[e.Subject] = append(bySubject[e.Subject], e)
	}
	out := make(map[string]json.RawMessage, len(bySubject))
	for subject, entries := range bySubject {
		raw, err := json.Marshal(entries)
		if err != nil {
			return nil, fmt.Errorf("gates %s: encode: %w", subject, err)
		}
		if len(raw) > gatesValueMax {
			return nil, fmt.Errorf("gates %s: %d bytes exceed the %d byte cap: %w",
				subject, len(raw), gatesValueMax, db.ErrInvalid)
		}
		out[subject] = raw
	}
	return out, nil
}

// Apply writes one subject's entries, or clears them when value is nil, in one
// transaction with nothing else: an apply is the importer's own transaction in
// ApplyIn, and this is the seam a caller without one uses.
func (d *GatesDoc) Apply(key string, value json.RawMessage) error {
	if value != nil {
		if _, err := decodeGatesValue(key, value); err != nil {
			return err
		}
	}
	return d.local.Tx(func(tx *db.Tx) error { return d.applyIn(tx, key, value) })
}

// applyIn is the ledger write itself, against a kv surface that may already be
// inside a transaction.
func (d *GatesDoc) applyIn(kv db.KV, key string, value json.RawMessage) error {
	l, err := LoadLedger(kv)
	if err != nil {
		return err
	}
	if value == nil {
		l = clearSubject(l, key)
	} else {
		entries, err := decodeGatesValue(key, value)
		if err != nil {
			return err
		}
		l = replaceSubject(l, key, entries)
	}
	return SaveLedger(kv, l)
}

// keepSubject is the ledger without the subject's rate-limit entries. Every
// other entry stands: this machine's own spawn failures, and the entries whose
// kind or source this build does not read, which is why a clear removes the
// subject's known entries and nothing else.
func keepSubject(l Ledger, subject string) Ledger {
	kept := make([]Entry, 0, len(l.Entries))
	for _, e := range l.Entries {
		if e.Kind == RateLimited && e.Subject == subject {
			continue
		}
		kept = append(kept, e)
	}
	return Ledger{Entries: kept, Other: l.Other}
}

// clearSubject is the ledger with the subject's rate-limit entries gone.
func clearSubject(l Ledger, subject string) Ledger { return keepSubject(l, subject) }

// replaceSubject is keepSubject with the subject's rate-limit entries set to
// the arriving ones, in the order they arrived.
func replaceSubject(l Ledger, subject string, entries []Entry) Ledger {
	kept := keepSubject(l, subject)
	return Ledger{Entries: append(kept.Entries, entries...), Other: l.Other}
}

// decodeGatesValue reads one subject's arriving entries and refuses every shape
// another machine could put there that this machine has no reader for.
//
// Each refusal answers its own attack. A kind other than a rate limit is an
// entry this build has no rule for, and applying it on this build's terms would
// be acting on a peer's rule without reading it. A source this build does not
// read is the same refusal one level down: the ledger would have kept it raw,
// and a raw entry is not a gate. A subject that is not the key is an entry
// aimed at some other subject arriving under this one's name, which would gate a
// provider the writer never named. A zero time, or an until before its at, is a
// gate that would never be live or would already be over when it landed.
func decodeGatesValue(subject string, value json.RawMessage) ([]Entry, error) {
	if len(value) > gatesValueMax {
		return nil, fmt.Errorf("gates %s: value is %d bytes, over the %d byte cap: %w",
			subject, len(value), gatesValueMax, db.ErrInvalid)
	}
	if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return nil, nil
	}
	var entries []Entry
	if err := json.Unmarshal(value, &entries); err != nil {
		return nil, fmt.Errorf("gates %s: value is not an array of entries: %w: %w", subject, err, db.ErrInvalid)
	}
	for i, e := range entries {
		var why string
		switch {
		case e.Kind != RateLimited:
			why = fmt.Sprintf("kind %q never travels", e.Kind)
		case !knownSource(e.Source):
			why = fmt.Sprintf("source %q is not one this build reads", e.Source)
		case e.Subject != subject:
			why = fmt.Sprintf("names subject %q under key %q", e.Subject, subject)
		case e.At.IsZero():
			why = "at is zero"
		case !e.Until.IsZero() && e.Until.Before(e.At):
			why = "until precedes at"
		}
		if why != "" {
			return nil, fmt.Errorf("gates %s: entry %d: %s: %w", subject, i, why, db.ErrInvalid)
		}
	}
	return entries, nil
}

// ApplyIn is Apply inside the importer's transaction, for an item that arrived
// from another machine. The value is checked first and refused without touching
// anything when it fails, so an entry this machine has no reader for never
// leaves the ledger unreadable behind it.
//
// An arriving entry records no write stamp: the stamp is what says this machine
// last wrote the key, and the peer wrote it over there.
func (d *GatesDoc) ApplyIn(tx *db.Tx, key string, value json.RawMessage, label string) (refusal error, err error) {
	if value != nil {
		if _, verr := decodeGatesValue(key, value); verr != nil {
			return verr, nil
		}
	}
	return nil, d.applyIn(tx, key, value)
}

// SupersedeIn records nothing. The gates document keeps no revision log, and
// the importer's own applied state is the record of an item that lost: it moves
// the key's applied stamp to the entry, so an older entry still loses to it and
// a redelivery is a no-op.
func (d *GatesDoc) SupersedeIn(tx *db.Tx, key string, value json.RawMessage, label, why string) error {
	return nil
}

// ItemWriteStamp is the stamp recorded when key was last written on this
// machine. An entry applied from a peer records none.
func (d *GatesDoc) ItemWriteStamp(key string) (synclog.Stamp, bool, error) {
	text, ok, err := d.local.ConfigWriteStamp(stampKey(key))
	if err != nil || !ok {
		return 0, false, err
	}
	stamp, err := synclog.ParseStamp(text)
	return stamp, err == nil, err
}

// ItemWriteStampIn is ItemWriteStamp read inside the importer's transaction,
// which is how an arriving entry is weighed against a write made here.
func (d *GatesDoc) ItemWriteStampIn(tx *db.Tx, key string) (synclog.Stamp, bool, error) {
	text, ok, err := tx.ConfigWriteStamp(stampKey(key))
	if err != nil || !ok {
		return 0, false, err
	}
	stamp, err := synclog.ParseStamp(text)
	return stamp, err == nil, err
}
