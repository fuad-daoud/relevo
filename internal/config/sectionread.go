package config

import "github.com/fuad-daoud/relevo/internal/db"

// The section and secret read surface: what the stored rows say, one body or
// one secret at a time; the write surface is in config.go.

// Body returns the stored body of one section, with this machine's overrides
// laid over it; ok is false when the section is absent. The merged body is
// what `config get`, `config export` and the bare `config` read, so a caller
// never needs to know a machine field lives in the local layer to see it.
func (s *Store) Body(sec Section) ([]byte, bool, error) {
	var (
		out     []byte
		present bool
	)
	err := s.db.Tx(func(t *db.Tx) error {
		body, ok, err := t.ConfigGet(string(sec))
		if err != nil || !ok {
			return err
		}
		merged, err := mergedBody(t, sec, body)
		if err != nil {
			return err
		}
		out, present = merged, true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return out, present, nil
}

func (s *Store) Has(sec Section) (bool, error) {
	_, ok, err := s.Body(sec)
	return ok, err
}

func (s *Store) Secret(name string) ([]byte, bool, error) {
	return s.db.SecretGet(name)
}

func (s *Store) Version() (int64, error) { return s.db.ConfigVersion() }

// loadSecrets fills the two secrets Load reports. An absent one leaves its
// field at the zero value, which is how a machine with no client key reads.
func (s *Store) loadSecrets(L *Loaded) error {
	key, ok, err := s.db.SecretGet(SecretClientKey)
	if err != nil {
		return err
	}
	if ok {
		L.ClientKey = key
	}
	ts, ok, err := s.db.SecretGet(SecretTypesafe)
	if err != nil {
		return err
	}
	if ok {
		L.Typesafe = string(ts)
	}
	return nil
}

func (s *Store) SecretNames() ([]string, error) { return s.db.SecretNames() }
