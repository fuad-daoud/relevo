package config

// What counts as a person's edit of a shared secret. A write a person made stamps
// its key, and a write the program made on its own behalf does not: two machines
// that both stamped a generated client key would settle by clock, and the loser
// would silently lose its server enrolment. Only a person's set, remove, or config
// import stamps. Stamping and signalling happen in the same transaction as the write,
// so a stamp is never spent on a secret the machine did not keep.

import (
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/sync"
)

// The two ways a secret value reaches the table, and the two removals that go with
// them. Each names its caller rather than taking a flag, so the stamping decision is
// made where the caller knows which kind of write it is making: a store-level flag
// would be set once and read by every write underneath it.

// PutSecret stores value under name as a person's edit: the write stamps the name
// and signals the secrets document, so the value travels.
func (s *Store) PutSecret(name string, value []byte) error {
	return s.writeSecret(name, value)
}

// PutSecretUnstamped stores value under name as the program's own write: no write
// stamp and no document signal. A generated key, a re-marshal and an arriving value
// go through it, because none is a person deciding what the fleet holds.
func (s *Store) PutSecretUnstamped(name string, value []byte) error {
	return s.writeSecretUnstamped(name, value)
}

// SecretDelete removes name's stored value as a person's edit, stamping and
// signalling it: a removal travels too, or a peer would keep handing the secret
// back. An unstored name writes nothing.
func (s *Store) SecretDelete(name string) error {
	return s.deleteSecret(name)
}

// SecretDeleteUnstamped removes a stored value as the program's own write, which is
// what clearing an arrival uses: the peer's applied state is the record, and a stamp
// would claim this machine had decided to remove it.
func (s *Store) SecretDeleteUnstamped(name string) error {
	return s.deleteSecretUnstamped(name)
}

// writeSecret is the person's write: put the value, stamp the name, signal the
// document, and record a revision naming the secret and never the value.
func (s *Store) writeSecret(name string, value []byte) error {
	value, err := canonicalSecret(name, value)
	if err != nil {
		return err
	}
	return s.db.Tx(func(t *db.Tx) error {
		if err := t.SecretPut(name, value, s.now().UTC()); err != nil {
			return err
		}
		return s.stampAndRecord(t, name, "set", s, true)
	})
}

// writeSecretUnstamped is the program's own write of a value. It changes only the
// secret table and the revision log, so an unstamped value never travels on the
// strength of being written.
func (s *Store) writeSecretUnstamped(name string, value []byte) error {
	value, err := canonicalSecret(name, value)
	if err != nil {
		return err
	}
	return s.db.Tx(func(t *db.Tx) error {
		if err := t.SecretPut(name, value, s.now().UTC()); err != nil {
			return err
		}
		return s.stampAndRecord(t, name, "set", s, false)
	})
}

// deleteSecret is the person's removal. An unheld name writes nothing: there is no
// edit to record and nothing to travel.
func (s *Store) deleteSecret(name string) error {
	return s.db.Tx(func(t *db.Tx) error {
		held, err := deleteIfHeld(t, name)
		if err != nil || !held {
			return err
		}
		return s.stampAndRecord(t, name, "remove", s, true)
	})
}

// deleteSecretUnstamped is the program's own removal.
func (s *Store) deleteSecretUnstamped(name string) error {
	return s.db.Tx(func(t *db.Tx) error {
		held, err := deleteIfHeld(t, name)
		if err != nil || !held {
			return err
		}
		return s.stampAndRecord(t, name, "remove", s, false)
	})
}

// putSecretApplied stores a value that arrived from label, inside the caller's
// transaction, with the source naming the installation it came from.
func (s *Store) putSecretApplied(t *db.Tx, name string, plain []byte, label string) error {
	if err := t.SecretPut(name, plain, s.now().UTC()); err != nil {
		return err
	}
	return s.stampAndRecord(t, name, "applied", s.As(syncedSourcePrefix+label, "secret from "+label), false)
}

// deleteSecretApplied removes a value on the same terms as putSecretApplied: a peer's
// tombstone is recorded as arrived, not as a local decision.
func (s *Store) deleteSecretApplied(t *db.Tx, name, label string) error {
	held, err := deleteIfHeld(t, name)
	if err != nil || !held {
		return err
	}
	return s.stampAndRecord(t, name, "removed", s.As(syncedSourcePrefix+label, "secret removed by "+label), false)
}

// stampAndRecord is the tail of every secret write: optionally the write stamp and
// the document signal, then one revision naming the secret and the operation. It
// reads the snapshot itself so each caller above is one transaction and one statement.
// stamped decides both stamp and signal. An unstamped write clears neither: the signal
// a previous stamped write left still holds, so leaving it alone keeps the export honest.
func (s *Store) stampAndRecord(t *db.Tx, name, op string, as *Store, stamped bool) error {
	before, err := readSnapshot(t)
	if err != nil {
		return err
	}
	if stamped {
		if err := sync.StampSecretWrites(t, []string{name}, s.now); err != nil {
			return err
		}
	}
	return as.record(t, before, []Change{{Path: "secret." + name, Op: op}})
}

// deleteIfHeld removes name's value and reports whether there was one, so removing an
// absent secret records nothing.
func deleteIfHeld(t *db.Tx, name string) (bool, error) {
	if _, ok, err := t.SecretGet(name); err != nil {
		return false, err
	} else if !ok {
		return false, nil
	}
	if err := t.SecretDelete(name); err != nil {
		return false, err
	}
	return true, nil
}

// canonicalSecret validates and re-marshals a client key so a malformed PEM never
// reaches the database and only the current label survives a re-store. Every other
// name passes through untouched: a write outside the allow-list is still a legitimate
// machine-local write.
func canonicalSecret(name string, value []byte) ([]byte, error) {
	if name != SecretClientKey {
		return value, nil
	}
	kp, err := remote.ParsePrivate(value)
	if err != nil {
		return nil, err
	}
	return remote.MarshalPrivate(kp)
}
