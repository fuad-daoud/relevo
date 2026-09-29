package relevo

import (
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/store"
)

// AgentInstallEnv is the InstallEnv every agent-definition surface reads and
// writes through -- `relevo config agents` and the cockpit's `:agents` view
// alike (cockpit config r5 §3): the OS-backed env with the role manifest in
// the machine database's kv row "agents-manifest" (#371 §4.10, P3b plan §4.4).
//
// The state root is composed here through store.DefaultRoot, the one path
// relevo's state always resolves through (CLAUDE.md, #42), and its database is
// opened here because internal/harness cannot import internal/store. It moved
// here from cmd/relevo/agent.go in round 5: internal/ui cannot import package
// main, and the cockpit's mastermindActions needs the same env.
func AgentInstallEnv() (harness.InstallEnv, error) {
	root, err := store.DefaultRoot()
	if err != nil {
		return nil, err
	}
	d, err := store.New(root).DB()
	if err != nil {
		return nil, err
	}
	return harness.OSInstallEnvKV(d), nil
}

// MachineConfig opens the config store of the same machine database
// AgentInstallEnv keeps the role manifest in: the custom agents live in the
// config, so the surfaces that install them read both through one state root.
func MachineConfig() (*config.Store, error) {
	root, err := store.DefaultRoot()
	if err != nil {
		return nil, err
	}
	d, err := store.New(root).DB()
	if err != nil {
		return nil, err
	}
	return config.Open(d), nil
}
