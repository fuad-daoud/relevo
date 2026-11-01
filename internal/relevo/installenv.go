package relevo

import (
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/tmpdir"
)

// AgentInstallEnv is the InstallEnv every agent-definition surface reads and
// writes through: the OS-backed env with the role manifest in the machine
// database's kv row "agents-manifest". The state root is composed here through
// tmpdir.DefaultRoot, and its database is opened here because
// internal/harness cannot import internal/store; it lives here rather than in
// cmd because internal/ui cannot import package main.
func AgentInstallEnv() (harness.InstallEnv, error) {
	root, err := tmpdir.DefaultRoot()
	if err != nil {
		return nil, err
	}
	d, err := store.New(root).DB()
	if err != nil {
		return nil, err
	}
	// The role manifest names this machine's role files, so it lives in the
	// machine-local file.
	return harness.OSInstallEnvKV(d.LocalOrSelf()), nil
}

// MachineConfig opens the config store of the same machine database
// AgentInstallEnv keeps the role manifest in: the custom agents live in the
// config, so both read through one state root. The store carries the file
// applier as well, so the hooks section's embedded scripts are written where
// the argv rows name them rather than only held in the database.
func MachineConfig() (*config.Store, error) {
	root, err := tmpdir.DefaultRoot()
	if err != nil {
		return nil, err
	}
	d, err := store.New(root).DB()
	if err != nil {
		return nil, err
	}
	cfgRoot, err := ConfigRoot()
	if err != nil {
		return config.Open(d), nil
	}
	return config.Open(d).WithFiles(HookFileApplier(cfgRoot), cfgRoot), nil
}
