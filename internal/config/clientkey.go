package config

import "github.com/fuad-daoud/relevo/internal/remote"

// NormalizeClientKey rewrites a stored client.key whose PEM label predates the
// relevo rename, so every later read sees the current label. It reports
// whether it wrote. An absent key, or one already carrying the current label, is
// a no-op that records no revision. The write goes through PutSecret, so the
// re-marshalled key is stored and a config revision is recorded like any other
// store write.
func (s *Store) NormalizeClientKey() (bool, error) {
	raw, ok, err := s.db.SecretGet(SecretClientKey)
	if err != nil {
		return false, err
	}
	if !ok || !remote.IsLegacyPrivatePEM(raw) {
		return false, nil
	}
	if err := s.As("daemon", "client key PEM label updated").PutSecret(SecretClientKey, raw); err != nil {
		return false, err
	}
	return true, nil
}
