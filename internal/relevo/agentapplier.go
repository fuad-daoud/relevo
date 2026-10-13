package relevo

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/harness"
)

// Where a native agent's harness file belongs on this machine. The rules are
// the hook applier's and they differ only where the files differ:
//
//   - the directory is the kind's own harness home, computed from the kind and
//     the agent name through the same table `relevo config agents` installs
//     through, so an item cannot steer the write and a peer that stores its
//     agents under another home still applies them under its own;
//   - a target that exists as a symlink or as anything but a regular file is
//     refused rather than followed, so a definition cannot be written through
//     a link to somewhere else;
//   - the write goes to a temp file in the target's own directory and is
//     renamed over the target, so a reader never sees a half-written file;
//   - a definition is never executable, whatever mode the item asked for. Only
//     a hook script is, and only under this machine's hooks directory.

// agentHome is the directory a kind's definitions live under. It is the user's
// home, resolved the way harness.InstallEnv resolves it, so the paths written
// here are the paths the installer reads.
func agentHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("agents: %w", err)
	}
	return home, nil
}

// AgentFilePath returns where one native agent's definition belongs for one
// harness kind. An unknown kind is refused rather than guessed at: the table
// is what says which home a kind uses, and a kind it does not hold has none.
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
// is not a definition: following one would read (and, on a write, replace)
// whatever it names.
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

// WriteAgentFile writes data at path through a temp file in path's directory
// and a rename. An existing target that is not a regular file is refused, and
// the file lands mode 0644: an agent definition is read by a harness, not run.
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
