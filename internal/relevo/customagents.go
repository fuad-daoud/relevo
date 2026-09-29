package relevo

import (
	"fmt"
	"sort"

	"github.com/fuad-daoud/relevo/internal/agentsrc"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// IsSourceAgent reports whether name is an agents entry relevo renders from its
// own single-source text, as opposed to a native agent it never writes.
func IsSourceAgent(agents map[string]roles.AgentEntry, name string) bool {
	entry, ok := agents[name]
	return ok && entry.Source != ""
}

// CustomAgentDocs renders the config's source agents, in name order, to one doc
// per kind each source renders. Native entries are skipped, and when only is
// non-empty so is every other name. A source that will not parse or render is
// an error naming the agent: config validation already refuses one, so this is
// unexpected.
func CustomAgentDocs(agents map[string]roles.AgentEntry, only string) ([]harness.CustomDoc, error) {
	names := make([]string, 0, len(agents))
	for name := range agents {
		names = append(names, name)
	}
	sort.Strings(names)

	var docs []harness.CustomDoc
	for _, name := range names {
		if only != "" && name != only {
			continue
		}
		entry := agents[name]
		if entry.Source == "" {
			continue
		}
		src, err := agentsrc.Parse([]byte(entry.Source))
		if err != nil {
			return nil, fmt.Errorf("agent %s: %w", name, err)
		}
		for _, kind := range agentsrc.RenderedKinds(src) {
			rendered, err := agentsrc.Render(src, kind)
			if err != nil {
				return nil, fmt.Errorf("agent %s: %w", name, err)
			}
			docs = append(docs, harness.CustomDoc{Kind: kind, Name: name, Bytes: rendered})
		}
	}
	return docs, nil
}

// InstallCustomAgents renders cfg's source agents and installs them. A nil cfg
// is a runtime with no config store: nothing to do, not an error.
func InstallCustomAgents(cfg *config.Store, env harness.InstallEnv, opts harness.InstallOptions) ([]harness.InstallResult, error) {
	if cfg == nil {
		return nil, nil
	}
	loaded, err := cfg.Load()
	if err != nil {
		return nil, err
	}
	docs, err := CustomAgentDocs(loaded.Agents, opts.Role)
	if err != nil {
		return nil, err
	}
	return harness.InstallCustom(env, opts, docs)
}

// CustomAgentFiles reports name's rendered files' state on every kind on PATH,
// writing nothing. A nil cfg or a name that is not a source agent gives nil, nil.
func CustomAgentFiles(cfg *config.Store, env harness.InstallEnv, name string) ([]harness.AgentFile, error) {
	if cfg == nil {
		return nil, nil
	}
	loaded, err := cfg.Load()
	if err != nil {
		return nil, err
	}
	if !IsSourceAgent(loaded.Agents, name) {
		return nil, nil
	}
	docs, err := CustomAgentDocs(loaded.Agents, name)
	if err != nil {
		return nil, err
	}
	return harness.CustomFiles(env, docs)
}

// ResetCustomAgentFile overwrites kind's rendered file for name with the bytes
// relevo renders now, and returns the one install result.
func ResetCustomAgentFile(cfg *config.Store, env harness.InstallEnv, kind, name string) (harness.InstallResult, error) {
	results, err := InstallCustomAgents(cfg, env, harness.InstallOptions{Kind: kind, Role: name, Force: true})
	if err != nil {
		return harness.InstallResult{}, err
	}
	if len(results) == 0 {
		return harness.InstallResult{}, fmt.Errorf("%s has no %s definition", kind, name)
	}
	return results[0], nil
}
