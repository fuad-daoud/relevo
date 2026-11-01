package relevo

import (
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/config"
)

// ConfigRoot is relevo's own config directory, `<user config root>/relevo`:
// the one place hook scripts are read from and written to, and the directory
// the importer reads. Resolving it through config.UserRoot keeps the applier,
// the importer and `relevo config` agreeing, and nothing else under the user
// config root is ever synced.
func ConfigRoot() (string, error) {
	root, err := config.UserRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "relevo"), nil
}
