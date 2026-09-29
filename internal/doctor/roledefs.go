package doctor

import (
	"fmt"
	"io/fs"

	"github.com/fuad-daoud/relevo/internal/harness"
)

// roleCheck probes one shipped role definition on disk.
func roleCheck(env Env, kind string, r harness.Role) Check {
	homeRel := "~/" + r.Path
	fullPath, err := env.HomePath(r.Path)
	if err != nil {
		return Check{
			Group: kind, Name: r.Name, Severity: SevWarn,
			Detail:      fmt.Sprintf("could not resolve home directory: %v", err),
			ProbeFailed: true,
		}
	}
	if env.Stat(fullPath) != nil {
		// relevo config agents creates the directory even on a machine that
		// has never run this harness as a sub-agent host.
		return Check{
			Group: kind, Name: r.Name, Severity: SevWarn,
			Detail: fmt.Sprintf("missing: %s", homeRel),
			Fix:    fmt.Sprintf("relevo config agents --kind %s --agent %s", kind, r.Name),
		}
	}

	detail := homeRel
	// A read error is swallowed: the file exists, which is what this row
	// reports, and the model pin is a courtesy on top of that.
	if raw, err := env.ReadFile(fullPath); err == nil {
		if model := harness.PinnedModel(kind, raw); model != "" {
			detail = fmt.Sprintf("%s (model: %s)", homeRel, model)
			if c, ok := pinMismatchCheck(kind, r, model, detail); ok {
				return c
			}
		}
		// Drift only matters on a kind relevo owns outright (ExpectModel
		// set): elsewhere the README invites the user to repin model:.
		if r.ExpectModel != "" {
			if shipped, shipErr := harness.AgentDoc(r.Name, kind); shipErr == nil && !harness.DocEqual(shipped, raw) {
				return Check{
					Group: kind, Name: r.Name, Severity: SevWarn,
					Detail: fmt.Sprintf("%s -- differs from the definition this relevo ships", detail),
					Fix:    fmt.Sprintf("relevo config agents --kind %s --agent %s --force", kind, r.Name),
				}
			}
		}
	}
	return Check{Group: kind, Name: r.Name, Severity: SevOK, Detail: detail}
}

// pinMismatchCheck reports a pinned model that differs from ExpectModel: a
// toml harness pins a model name, everyone else pins a tier the candidate's
// --model ignores.
func pinMismatchCheck(kind string, r harness.Role, model, detail string) (Check, bool) {
	if r.ExpectModel == "" || model == r.ExpectModel {
		return Check{}, false
	}
	if h, ok := harness.Lookup(kind); ok && h.DocExt == "toml" {
		return Check{
			Group: kind, Name: r.Name, Severity: SevWarn,
			Detail: fmt.Sprintf("%s -- pins %s; relevo ships %s", detail, model, r.ExpectModel),
			Fix:    fmt.Sprintf("relevo config agents --kind %s --agent %s --force", kind, r.Name),
		}, true
	}
	return Check{
		Group: kind, Name: r.Name, Severity: SevWarn,
		Detail: fmt.Sprintf("%s -- pins a tier; the candidate's --model is ignored", detail),
		Fix:    fmt.Sprintf("set model: %s in ~/%s", r.ExpectModel, r.Path),
	}, true
}

// customRoleCheck probes one custom role definition on disk: the user's own
// file, so no model-pin or drift check, and a missing file's fix offers the
// install verb and the by-hand path.
func customRoleCheck(env Env, kind, name string) Check {
	path, _ := harness.DefinitionPath(kind, name)
	homeRel := "~/" + path
	fullPath, err := env.HomePath(path)
	if err != nil {
		return Check{
			Group: kind, Name: name, Severity: SevWarn,
			Detail:      fmt.Sprintf("could not resolve home directory: %v", err),
			ProbeFailed: true,
		}
	}
	if env.Stat(fullPath) != nil {
		return Check{
			Group: kind, Name: name, Severity: SevWarn,
			Detail: "missing: " + homeRel + " (custom)",
			Fix:    "run relevo config agents --kind " + kind + ", or install your agent definition at " + homeRel,
		}
	}
	return Check{Group: kind, Name: name, Severity: SevOK, Detail: homeRel + " (custom)"}
}

// roleInstallEnv adapts Env to harness.InstallEnv for a dry-run install: it
// never writes, so MkdirAll, WriteFile and SaveManifest are no-ops rather
// than silent fallbacks, because doctor must never touch a user's files.
type roleInstallEnv struct {
	env      Env
	manifest map[string]string
}

func (e roleInstallEnv) LookPath(binary string) (string, error) { return e.env.LookPath(binary) }
func (e roleInstallEnv) HomePath(rel string) (string, error)    { return e.env.HomePath(rel) }
func (e roleInstallEnv) MkdirAll(string) error                  { return nil }
func (e roleInstallEnv) WriteFile(string, []byte) error         { return nil }

// ReadFile reports fs.ErrNotExist for a path Stat says is absent, so a
// missing definition reads as "would write" rather than an empty differing
// file.
func (e roleInstallEnv) ReadFile(path string) ([]byte, error) {
	if err := e.env.Stat(path); err != nil {
		return nil, fs.ErrNotExist
	}
	return e.env.ReadFile(path)
}

func (e roleInstallEnv) LoadManifest() (map[string]string, error) { return e.manifest, nil }
func (e roleInstallEnv) SaveManifest(map[string]string) error     { return nil }

// rolesCheck is the role-staleness row: a dry-run install with the manifest
// says whether the daemon will refresh anything on its next start, whether
// the user's own edits are being kept, or whether everything is current.
func rolesCheck(env Env, kind string) Check {
	manifest, err := env.LoadManifest()
	if err != nil {
		manifest = nil // unreadable records nothing, same as an empty map
	}

	results, err := harness.Install(roleInstallEnv{env: env, manifest: manifest}, harness.InstallOptions{
		Kind:   kind,
		DryRun: true,
	})
	if err != nil {
		return Check{
			Group: kind, Name: "roles", Severity: SevOK,
			Detail:      fmt.Sprintf("not checked -- %v", err),
			ProbeFailed: true,
		}
	}

	stale, edited := false, false
	for _, r := range results {
		switch r.Outcome {
		case harness.OutcomeWouldWrite, harness.OutcomeWouldUpdate:
			stale = true
		case harness.OutcomeKeptDiffers:
			edited = true
		case harness.OutcomeError:
			return Check{
				Group: kind, Name: "roles", Severity: SevWarn,
				Detail:      fmt.Sprintf("could not check the agent definitions: %s", r.Err),
				ProbeFailed: true,
			}
		}
	}

	switch {
	case stale:
		return Check{
			Group: kind, Name: "roles", Severity: SevWarn,
			Detail: "agent definitions are stale; the daemon refreshes them on its next start, or run relevo config agents",
			Fix:    "relevo config agents",
		}
	case edited:
		return Check{Group: kind, Name: "roles", Severity: SevOK, Detail: "differs from every copy relevo has shipped (kept as your edit)"}
	default:
		return Check{Group: kind, Name: "roles", Severity: SevOK, Detail: "up to date"}
	}
}
