package relevo

import "github.com/fuad-daoud/relevo/internal/config"

// ConfigRoot is this machine's config root, resolved through the one rule in
// internal/config so the applier, the importer and `relevo config` cannot
// disagree about where the files are.
func ConfigRoot() (string, error) { return config.UserRoot() }
