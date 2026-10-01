package relevo

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relevo/internal/workflow"
)

// ResolveWorkflow finds the workflow nameOrPath names: an existing file path
// first, with its file: seeds embedded; then a saved workflow; then the shipped
// default. origin is "file", "saved" or "shipped", so a caller can say where the
// definition came from.
func ResolveWorkflow(rt Runtime, nameOrPath string) (workflow.Definition, string, error) {
	if nameOrPath == "" {
		return workflow.Definition{}, "", refuse("workflow: no name or path given")
	}
	if info, err := os.Stat(nameOrPath); err == nil && !info.IsDir() {
		def, err := loadWorkflowFile(nameOrPath)
		if err != nil {
			return workflow.Definition{}, "", err
		}
		return def, "file", nil
	}
	if rt.Config != nil {
		L, err := rt.Config.Load()
		if err != nil {
			return workflow.Definition{}, "", refuse("%v", err)
		}
		if w, ok := L.Workflows[nameOrPath]; ok {
			return w.Definition, "saved", nil
		}
	}
	if nameOrPath == workflow.Default().Name {
		return workflow.Default(), "shipped", nil
	}
	return workflow.Definition{}, "", refuse("workflow: %q is not a file, a saved workflow or a shipped workflow", nameOrPath)
}

// loadWorkflowFile reads one workflow file and embeds its file: seeds relative
// to the file's own directory.
func loadWorkflowFile(path string) (workflow.Definition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return workflow.Definition{}, refuse("%v", err)
	}
	def, err := workflow.Parse(data)
	if err != nil {
		return workflow.Definition{}, refuse("%v", err)
	}
	def, err = EmbedFileSeeds(def, filepath.Dir(path))
	if err != nil {
		return workflow.Definition{}, refuse("%v", err)
	}
	return def, nil
}

// EmbedFileSeeds replaces every file:<relative path> seed with the contents of
// the file it names, read relative to dir. It is the one place a workflow
// definition reads another file, so internal/workflow stays I/O-free. A path
// that is absolute or leaves dir is refused, so a workflow can only name files
// beside itself.
func EmbedFileSeeds(def workflow.Definition, dir string) (workflow.Definition, error) {
	steps := make(map[string]workflow.Step, len(def.Steps))
	for id, step := range def.Steps {
		if strings.HasPrefix(step.Seed, "file:") {
			content, err := readSeedFile(dir, strings.TrimPrefix(step.Seed, "file:"))
			if err != nil {
				return workflow.Definition{}, fmt.Errorf("step %s: %w", id, err)
			}
			step.Seed = content
		}
		steps[id] = step
	}
	def.Steps = steps
	return def, nil
}

// readSeedFile reads one seed file below dir, refusing an empty, absolute or
// escaping path before it touches the disk.
func readSeedFile(dir, rel string) (string, error) {
	if rel == "" {
		return "", refuse("seed file: names no path")
	}
	if filepath.IsAbs(rel) {
		return "", refuse("seed file %q is absolute", rel)
	}
	full := filepath.Join(dir, rel)
	within, err := filepath.Rel(dir, full)
	if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return "", refuse("seed file %q escapes the workflow's directory", rel)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
