package harness

import "fmt"

// FileState is the state of one agent's definition file on one harness kind,
// as the cockpit's agents view shows it: a dry-run InstallOutcome in one word.
type FileState string

const (
	FileUpToDate    FileState = "up to date"
	FileStale       FileState = "stale"
	FileEdited      FileState = "your edit"
	FileEditedNewer FileState = "edit + newer"
	FileMissing     FileState = "missing"
)

// AgentFile is one (agent, harness kind) definition file.
type AgentFile struct {
	Kind  string    // harness kind
	Path  string    // absolute path, resolved through the InstallEnv home
	State FileState // the dry-run install's verdict
	Model string    // PinnedModel of the file on disk; "" when missing or unpinned
}

// AgentFiles returns agent's definition state on every known kind whose binary
// is on PATH, in All() order, writing nothing. A custom name returns nil, nil.
func AgentFiles(env InstallEnv, agent string) ([]AgentFile, error) {
	if !shippedAnywhere(agent) {
		return nil, nil
	}

	var out []AgentFile
	for _, h := range All() {
		if _, err := env.LookPath(h.Binary); err != nil {
			continue
		}
		results, err := Install(env, InstallOptions{Kind: h.Kind, Role: agent, DryRun: true})
		if err != nil {
			return nil, err
		}
		files, err := agentFilesFromResults(env, results)
		if err != nil {
			return nil, err
		}
		out = append(out, files...)
	}
	return out, nil
}

func agentFilesFromResults(env InstallEnv, results []InstallResult) ([]AgentFile, error) {
	var out []AgentFile
	for _, res := range results {
		state, err := fileState(res)
		if err != nil {
			return nil, err
		}
		full, err := env.HomePath(res.Path)
		if err != nil {
			return nil, err
		}
		af := AgentFile{Kind: res.Kind, Path: full, State: state}
		if state != FileMissing {
			raw, err := env.ReadFile(full)
			if err != nil {
				return nil, err
			}
			af.Model = PinnedModel(res.Kind, raw)
		}
		out = append(out, af)
	}
	return out, nil
}

func fileState(res InstallResult) (FileState, error) {
	switch res.Outcome {
	case OutcomeWouldWrite:
		return FileMissing, nil
	case OutcomeKeptIdentical:
		return FileUpToDate, nil
	case OutcomeWouldUpdate:
		return FileStale, nil
	case OutcomeKeptDiffers:
		if res.NewerShipped {
			return FileEditedNewer, nil
		}
		return FileEdited, nil
	case OutcomeError:
		return "", fmt.Errorf("%s %s: %s", res.Kind, res.Role, res.Err)
	default:
		return "", fmt.Errorf("%s %s: unexpected install outcome %q", res.Kind, res.Role, res.Outcome)
	}
}

// ResetAgentFile overwrites agent's definition file on kind with the shipped
// copy and returns Install's single result.
func ResetAgentFile(env InstallEnv, kind, agent string) (InstallResult, error) {
	results, err := Install(env, InstallOptions{Kind: kind, Role: agent, Force: true})
	if err != nil {
		return InstallResult{}, err
	}
	if len(results) == 0 {
		return InstallResult{}, fmt.Errorf("%s has no %s definition", kind, agent)
	}
	return results[0], nil
}

func shippedAnywhere(agent string) bool {
	for _, h := range All() {
		if _, ok := h.Role(agent); ok {
			return true
		}
	}
	return false
}
