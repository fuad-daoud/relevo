package sync

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"

	"github.com/fuad-daoud/relevo/internal/db"
)

// Settings is the machine-local sync section: where this installation's remote
// lives and how eager it is. It is stored like every other section, in the
// local config_doc, and the shared file never carries one.
type Settings struct {
	// Enabled is whether sync is turned on. It is off until a machine enables
	// it, so an installation that never enabled sync reports off.
	Enabled bool `json:"enabled"`
	// RemoteURL is the remote this installation pushes to and pulls from.
	RemoteURL string `json:"remote_url,omitempty"`
	// Namespace is the remote's namespace, optional on the remote's own terms.
	Namespace string `json:"namespace,omitempty"`
	// IdleSeconds is the idle-tick interval in seconds; zero selects the
	// package default.
	IdleSeconds int64 `json:"idle_seconds,omitempty"`
	// BacklogThreshold is the unpushed operation count above which the
	// statusline reports behind; zero selects the package default.
	BacklogThreshold int64 `json:"backlog_threshold,omitempty"`
	// QuotaTursoSync, QuotaTursoStorage and QuotaR2Storage are the plan limits
	// in bytes that status reads the month's counters against; zero selects the
	// default.
	QuotaTursoSync    int64 `json:"quota_turso_sync,omitempty"`
	QuotaTursoStorage int64 `json:"quota_turso_storage,omitempty"`
	QuotaR2Storage    int64 `json:"quota_r2_storage,omitempty"`
}

const (
	defaultQuotaTursoSync    int64 = 10_000_000_000
	defaultQuotaTursoStorage int64 = 9_000_000_000
	defaultQuotaR2Storage    int64 = 10_000_000_000
)

// TursoSyncQuota is the monthly Turso sync allowance in bytes.
func (s Settings) TursoSyncQuota() int64 { return orDefault(s.QuotaTursoSync, defaultQuotaTursoSync) }

// TursoStorageQuota is the Turso storage allowance in bytes.
func (s Settings) TursoStorageQuota() int64 {
	return orDefault(s.QuotaTursoStorage, defaultQuotaTursoStorage)
}

// R2StorageQuota is the bucket's storage allowance in bytes.
func (s Settings) R2StorageQuota() int64 { return orDefault(s.QuotaR2Storage, defaultQuotaR2Storage) }

func orDefault(v, def int64) int64 {
	if v == 0 {
		return def
	}
	return v
}

// ParseSettings parses a sync section body. An unknown field is refused rather
// than dropped: a misspelled key would otherwise read back as the default and
// the machine would then sync with it.
func ParseSettings(body []byte) (Settings, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var st Settings
	if err := dec.Decode(&st); err != nil {
		return Settings{}, fmt.Errorf("sync: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return Settings{}, fmt.Errorf("sync: trailing content after the body: %w", db.ErrInvalid)
	}
	if err := st.validate(); err != nil {
		return Settings{}, err
	}
	return st, nil
}

// validate refuses a body whose numbers name a moment in the past or a
// threshold no tick could ever cross. The remote's reachability and this
// machine's origin are checked when sync is enabled, not here: this is the
// shape of the settings, not whether they may be used.
func (s Settings) validate() error {
	if s.IdleSeconds < 0 {
		return fmt.Errorf("sync: idle_seconds is negative: %w", db.ErrInvalid)
	}
	if s.BacklogThreshold < 0 {
		return fmt.Errorf("sync: backlog_threshold is negative: %w", db.ErrInvalid)
	}
	if s.QuotaTursoSync < 0 || s.QuotaTursoStorage < 0 || s.QuotaR2Storage < 0 {
		return fmt.Errorf("sync: a quota is negative: %w", db.ErrInvalid)
	}
	if s.RemoteURL != "" && !validRemoteURL(s.RemoteURL) {
		return fmt.Errorf("sync: remote_url is not a remote URL: %w", db.ErrInvalid)
	}
	if s.Namespace != "" && !validNamespace(s.Namespace) {
		return fmt.Errorf("sync: namespace holds a character a URL path cannot carry: %w", db.ErrInvalid)
	}
	return nil
}

// validRemoteURL reports whether raw names a remote this driver can open: a URL
// with a scheme and a host, and a scheme the sync driver speaks.
func validRemoteURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	switch u.Scheme {
	case "libsql", "https", "wss":
		return true
	default:
		return false
	}
}

// validNamespace reports whether ns is one URL path segment: the letters,
// digits and separators a namespace may hold, and nothing that would split the
// path or escape it.
func validNamespace(ns string) bool {
	for _, r := range ns {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return ns != ""
}
