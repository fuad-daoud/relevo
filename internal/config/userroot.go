package config

import (
	"os"
	"path/filepath"
)

// UserRoot resolves $XDG_CONFIG_HOME, falling back to ~/.config. It is the one
// place that rule is written, so every surface that has to find the config root
// -- the importer, the applier, the capture -- resolves it the same way.
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
