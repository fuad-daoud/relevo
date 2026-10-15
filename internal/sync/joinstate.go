package sync

import (
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// An enable --join has to store the remote and the token before it can open a
// worker at all: the opener reads them out of the machine-local file rather than
// taking them as arguments, because that is the only place the daemon's worker
// assembly lives. So the join stages those two rows, checks the passphrase
// against the remote, and -- if the check fails -- puts every row back exactly as
// it found it.
//
// Exactly is the word that does the work. A machine that asked to join with the
// wrong passphrase and was left pointed at somebody else's remote, or holding a
// token it could not use, would be a machine whose next enable does something the
// operator never asked for. Restoring means putting the bytes back, including the
// rows that were absent before, rather than writing a default over them.

// JoinSnapshot is the machine-local state an enable --join must be able to put
// back: the sync section, the token, the key, the fact about the remote's key,
// and the enabled marker.
//
// It records absence as well as value, because "there was no token here" and
// "the token here was this" are different machines and only the first one is what
// a refused join should leave behind.
type JoinSnapshot struct {
	section      []byte
	hasSection   bool
	token        []byte
	hasToken     bool
	key          []byte
	hasKey       bool
	remoteKDF    []byte
	hasRemoteKDF bool
	enabled      []byte
	hasEnabled   bool
}

// SnapshotJoin reads every row an enable --join stages or writes.
func SnapshotJoin(l Local) (JoinSnapshot, error) {
	var s JoinSnapshot
	var err error
	if s.section, s.hasSection, err = l.ConfigGet(SectionSettings); err != nil {
		return JoinSnapshot{}, fmt.Errorf("sync: snapshot the %s section: %w", SectionSettings, err)
	}
	if s.token, s.hasToken, err = l.SecretGet(SecretToken); err != nil {
		return JoinSnapshot{}, fmt.Errorf("sync: snapshot %s: %w", SecretToken, err)
	}
	if s.key, s.hasKey, err = l.SecretGet(SecretKey); err != nil {
		return JoinSnapshot{}, fmt.Errorf("sync: snapshot %s: %w", SecretKey, err)
	}
	if s.remoteKDF, s.hasRemoteKDF, err = l.KVGet(KeyRemoteKDF); err != nil {
		return JoinSnapshot{}, fmt.Errorf("sync: snapshot %s: %w", KeyRemoteKDF, err)
	}
	if s.enabled, s.hasEnabled, err = l.KVGet(KeyEnabled); err != nil {
		return JoinSnapshot{}, fmt.Errorf("sync: snapshot %s: %w", KeyEnabled, err)
	}
	return s, nil
}

// Restore puts back exactly what SnapshotJoin read.
//
// A row that was absent is deleted rather than written empty: an absent token is
// how a machine says it has none, and a section written with no remote in it is
// a different thing that the preflight would read as a machine pointed nowhere.
func (s JoinSnapshot) Restore(l Local, now time.Time) error {
	return l.Tx(func(t *db.Tx) error {
		if err := restoreSection(t, s.section, s.hasSection, now); err != nil {
			return err
		}
		if err := restoreSecret(t, SecretToken, s.token, s.hasToken); err != nil {
			return err
		}
		if err := restoreSecret(t, SecretKey, s.key, s.hasKey); err != nil {
			return err
		}
		if err := restoreMarker(t, KeyRemoteKDF, s.remoteKDF, s.hasRemoteKDF); err != nil {
			return err
		}
		return restoreMarker(t, KeyEnabled, s.enabled, s.hasEnabled)
	})
}

func restoreSection(t *db.Tx, body []byte, present bool, now time.Time) error {
	if !present {
		if err := t.ConfigDelete(SectionSettings); err != nil {
			return fmt.Errorf("sync: restore the %s section: %w", SectionSettings, err)
		}
		return nil
	}
	if err := t.ConfigPut(SectionSettings, body, now.UTC()); err != nil {
		return fmt.Errorf("sync: restore the %s section: %w", SectionSettings, err)
	}
	return nil
}

func restoreSecret(t *db.Tx, name string, value []byte, present bool) error {
	if !present {
		if err := t.SecretDelete(name); err != nil {
			return fmt.Errorf("sync: restore %s: %w", name, err)
		}
		return nil
	}
	if err := t.SecretPut(name, value, time.Unix(0, 0).UTC()); err != nil {
		return fmt.Errorf("sync: restore %s: %w", name, err)
	}
	return nil
}

func restoreMarker(t *db.Tx, key string, value []byte, present bool) error {
	if !present {
		if err := t.KVDelete(key); err != nil {
			return fmt.Errorf("sync: restore %s: %w", key, err)
		}
		return nil
	}
	if err := t.KVPut(key, value); err != nil {
		return fmt.Errorf("sync: restore %s: %w", key, err)
	}
	return nil
}
