package relevo

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/harness"
)

// Where a native agent's harness file belongs on this machine: the kind's
// harness home through the install table, so an item cannot steer the write.
// A definition is never written through a symlink, and never executable.

// agentHome is the directory a kind's definitions live under, resolved as
// harness.InstallEnv resolves it, so these paths are the ones the installer
// reads.
func agentHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("agents: %w", err)
	}
	return home, nil
}

// AgentFilePath returns where one native agent's definition belongs. An
// unknown kind is refused rather than guessed at: the table says which home
// a kind uses.
func (a diskFileApplier) AgentFilePath(kind, agent string) (string, error) {
	if _, ok := harness.Lookup(kind); !ok {
		return "", fmt.Errorf("agents: %q is not a harness kind: %w", kind, config.ErrAgentPath)
	}
	if err := config.SafeAgentComponent(agent); err != nil {
		return "", err
	}
	rel, ok := harness.DefinitionPath(kind, agent)
	if !ok {
		return "", fmt.Errorf("agents: %s has no %s definition: %w", agent, kind, config.ErrAgentPath)
	}
	home, err := agentHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, rel), nil
}

// ReadAgentFile reports what is at path when it is a regular file. A symlink
// is not a definition: following one would read or replace whatever it names.
func (a diskFileApplier) ReadAgentFile(path string) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("agents: %s is not a regular file: %w", path, config.ErrAgentPath)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// WriteAgentFile writes data at path through a temp file in path's own
// directory and a rename, so a reader never sees a half-written file. The
// file lands mode 0644: a definition is read by a harness, not run.
func (a diskFileApplier) WriteAgentFile(path string, data []byte) (bool, error) {
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return false, fmt.Errorf("agents: %s is not a regular file: %w", path, config.ErrAgentPath)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".relevo-agent-*")
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
	if err := os.Chmod(name, 0o644); err != nil {
		return false, err
	}
	if err := os.Rename(name, path); err != nil {
		return false, err
	}
	return true, nil
}
