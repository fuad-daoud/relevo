package config

// The section and secret read surface: what the stored rows say, one body or
// one secret at a time. The write surface is in config.go.

// Body returns the stored body of one section; ok is false when the section is
// absent.
func (s *Store) Body(sec Section) ([]byte, bool, error) {
	return s.db.ConfigGet(string(sec))
}

// Has reports whether the section has a stored body.
func (s *Store) Has(sec Section) (bool, error) {
	_, ok, err := s.Body(sec)
	return ok, err
}

func (s *Store) Secret(name string) ([]byte, bool, error) {
	return s.db.SecretGet(name)
}

func (s *Store) Version() (int64, error) { return s.db.ConfigVersion() }

// loadSecrets fills the two secrets Load reports. An absent one leaves its field
// at the zero value, which is how a machine with no client key reads.
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
