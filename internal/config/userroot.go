package config

import (
	"os"
	"path/filepath"
)

// UserRoot resolves $XDG_CONFIG_HOME, falling back to ~/.config. It is the
// one place that rule is written, so the importer, the applier and the
// capture resolve the root the same way.
func UserRoot() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return xdg, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config"), nil
}
