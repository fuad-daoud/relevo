package harness

import "fmt"

// CustomDoc is one custom agent's rendered file for one harness kind. harness
// takes bytes rather than a source, because the renderer lives in agentsrc and
// agentsrc imports harness, so the reverse import would be a cycle.
type CustomDoc struct {
	Kind  string // harness kind
	Name  string // the agent name; the file lands at DefinitionPath(Kind, Name)
	Bytes []byte // the rendered file
}

// InstallCustom lands each custom doc on disk under the same decision table
// Install applies to shipped definitions: a missing file is written, an
// identical one is kept, a copy relevo last wrote is refreshed, and the user's
// edit is kept unless Force. Docs are filtered by Kind and Role and skipped
// when their kind is unknown or its binary is not on PATH. The manifest is
// loaded once, threaded through every decision, and saved once. A shipped name
// is refused, so this can never touch a file relevo ships.
func InstallCustom(env InstallEnv, opts InstallOptions, docs []CustomDoc) ([]InstallResult, error) {
	manifest, merr := env.LoadManifest()
	if merr != nil || manifest == nil {
		manifest = map[string]string{}
	}

	var results []InstallResult
	changed := false
	for _, doc := range docs {
		if opts.Kind != "" && opts.Kind != doc.Kind {
			continue
		}
		if opts.Role != "" && opts.Role != doc.Name {
			continue
		}
		h, ok := Lookup(doc.Kind)
		if !ok {
			continue
		}
		if _, err := env.LookPath(h.Binary); err != nil {
			continue
		}
		if IsShipped(doc.Kind, doc.Name) {
			results = append(results, InstallResult{
				Kind:    doc.Kind,
				Role:    doc.Name,
				Outcome: OutcomeError,
				Err:     fmt.Sprintf("%s is a shipped agent", doc.Name),
			})
			continue
		}
		homeRel, _ := DefinitionPath(doc.Kind, doc.Name)
		res, rchanged, err := installBytes(env, opts, InstallResult{Kind: doc.Kind, Role: doc.Name, Path: homeRel}, homeRel, doc.Bytes, manifest, "")
		if err != nil {
			return nil, err
		}
		changed = changed || rchanged
		results = append(results, res)
	}

	if changed && !opts.DryRun {
		if serr := env.SaveManifest(manifest); serr != nil {
			// The definitions landed; only the record of them did not.
			return results, serr
		}
	}
	return results, merr
}

// CustomFiles is the dry-run counterpart of AgentFiles for custom docs: the
// same per-kind state an install would reach, writing nothing.
func CustomFiles(env InstallEnv, docs []CustomDoc) ([]AgentFile, error) {
	results, err := InstallCustom(env, InstallOptions{DryRun: true}, docs)
	if err != nil {
		return nil, err
	}
	return agentFilesFromResults(env, results)
}
