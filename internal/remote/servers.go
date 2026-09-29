package remote

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
)

// ServerEntry is one entry of the `servers` section: where a server lives and
// how its TLS is trusted.
type ServerEntry struct {
	URL         string `json:"url"`
	Fingerprint string `json:"fingerprint,omitempty"` // sha256:<hex>
	CA          string `json:"ca,omitempty"`          // "" (pin) | "system"
	Insecure    bool   `json:"insecure,omitempty"`
}

// Servers is the `servers` section: one entry per server, keyed by the
// server's short name.
type Servers map[string]ServerEntry // key: the server's short name

// ParseServers validates every entry, checking names in order so the first
// error is deterministic. It returns the map the JSON holds, never nil.
func ParseServers(data []byte) (Servers, error) {
	var s Servers
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse servers file: %w", err)
	}
	if s == nil {
		s = make(Servers)
	}

	names := make([]string, 0, len(s))
	for name := range s {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := ValidateEntry(s[name]); err != nil {
			return nil, fmt.Errorf("parse servers file: %s: %w", name, err)
		}
	}
	return s, nil
}

// ValidateEntry checks one server entry: a URL with a scheme and host, and a
// trust setting that matches the scheme.
func ValidateEntry(e ServerEntry) error {
	u, err := url.Parse(e.URL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return errors.New("invalid url")
	}
	if !e.Insecure {
		if u.Scheme != "https" {
			return errors.New("https required unless insecure")
		}
		if e.Fingerprint == "" && e.CA == "" {
			return errors.New("fingerprint or ca required for https")
		}
		if e.CA != "" && e.CA != "system" {
			return errors.New("ca must be 'system' or empty")
		}
	} else {
		if u.Scheme != "http" && u.Scheme != "https" {
			return errors.New("http or https required")
		}
	}
	return nil
}
