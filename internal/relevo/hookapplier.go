package relevo

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
)

// Where the config document's embedded files land on this machine's disk. The
// applier is built from relevo's config directory (`<user config root>/relevo`),
// so the paths it writes are the paths the importer reads.
//
// Three rules hold the writes, and each is a refusal rather than a repair:
//
//   - the directory is computed from the event and file names, never read out
//     of the entry, so an entry cannot steer a write anywhere;
//   - a target that exists as a symlink or as anything but a regular file is
//     refused rather than followed, so a hook script cannot be written through
//     a link to somewhere else;
//   - the write goes to a temp file in the target's own directory and is
//     renamed over the target, so a reader never sees a half-written script.

// HookFileApplier writes the config doc's hook scripts under root.
func HookFileApplier(root string) config.FileApplier {
	return diskFileApplier{root: root}
}

type diskFileApplier struct{ root string }

// hooksDir is this machine's hook script directory.
func (a diskFileApplier) hooksDir() string { return filepath.Join(a.root, "hooks") }

func (a diskFileApplier) HookFilePath(event, file string) (string, error) {
	if err := config.SafeHookComponent(event); err != nil {
		return "", err
	}
	if err := config.SafeHookComponent(file); err != nil {
		return "", err
	}
	return filepath.Join(a.hooksDir(), event+".d", file), nil
}

// ReadHookFile reports what is at path when it is a regular file. A symlink is
// not a script: following one would read (and, on a write, replace) whatever
// it names.
func (a diskFileApplier) ReadHookFile(path string) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("hooks: %s is not a regular file: %w", path, config.ErrHookPath)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// WriteHookFile writes data at path through a temp file in path's directory
// and a rename. A new file lands 0644 whatever the sender's file was: only a
// local chmod makes a synced script executable, and a rewrite keeps the mode
// that chmod gave it. An existing target that is not a regular file is refused.
func (a diskFileApplier) WriteHookFile(path string, data []byte) (bool, error) {
	mode := os.FileMode(0o644)
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("hooks: %s is not a regular file: %w", path, config.ErrHookPath)
		}
		mode = info.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".relevo-hook-*")
	if err != nil {
		return false, err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Chmod(name, mode); err != nil {
		return false, err
	}
	if err := os.Rename(name, path); err != nil {
		return false, err
	}
	return true, nil
}

// MachineConfigWithFiles is the config store of one machine bound to the
// config root, with the applier the hooks section's embedded scripts are
// written through. A store opened without it still carries the scripts and
// still refuses nothing about them; it simply writes no disk.
func MachineConfigWithFiles(shared *db.DB) (*config.Store, error) {
	root, err := ConfigRoot()
	if err != nil {
		return nil, err
	}
	return config.Open(shared).WithFiles(HookFileApplier(root), root), nil
}
