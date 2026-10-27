package spawn

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
)

// HeadlessLaunch renders the argv for one headless round: the candidate's
// binary, then its print form with the prompt, the budget, the round's working
// tree, the harness's writable root, prompt file and chain dir filled in. It
// reads nothing, except that a kind taking its role definition on the command
// line (muse) reads that one installed file; an empty prompt is a validation
// call and skips the read. An unknown kind is an error, not a panic: the
// configured set was validated on load, but a binding written by a future
// relevo could name a kind this one does not know.
func HeadlessLaunch(c candidate.Candidate, role harness.RoleSpec, tier harness.Tier, budget time.Duration, prompt, dir, writableRoot, promptFile, chainDir string) ([]string, error) {
	h, ok := harness.Lookup(c.Harness)
	if !ok {
		return nil, fmt.Errorf("unknown harness kind %q", c.Harness)
	}
	l, err := h.Launch(c.Provider, c.Model, c.ExtraArgs, role, tier, c.ContributorMode)
	if err != nil {
		return nil, err
	}
	if l.PromptAt < 0 {
		return nil, fmt.Errorf("harness %q has no print form", c.Harness)
	}
	definition := ""
	if prompt != "" {
		if definition, err = Definition(l, c.Harness, role); err != nil {
			return nil, err
		}
	}
	return append([]string{h.Binary}, l.PrintArgs(prompt, budget, dir, writableRoot, promptFile, chainDir, definition)...), nil
}

// Definition returns the role definition text l's argv carries, or "" for a
// launch with no DefinitionPlaceholder.
func Definition(l harness.Launch, kind string, role harness.RoleSpec) (string, error) {
	if !slices.Contains(l.Print, harness.DefinitionPlaceholder) {
		return "", nil
	}
	return readDefinition(kind, role.Definition)
}

// readDefinition returns the body of the installed definition file, without a
// leading YAML frontmatter block.
func readDefinition(kind, name string) (string, error) {
	rel, ok := harness.DefinitionPath(kind, name)
	if !ok {
		return "", fmt.Errorf("no definition path for %s agent %q", kind, name)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home for the %s definition: %w", kind, err)
	}
	path := filepath.Join(home, rel)
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s definition %s: %w; `relevo config agents --force` installs it", kind, path, err)
	}
	return stripFrontmatter(string(b)), nil
}

func stripFrontmatter(s string) string {
	rest, ok := strings.CutPrefix(s, "---\n")
	if !ok {
		return s
	}
	if i := strings.Index(rest, "\n---\n"); i >= 0 {
		return strings.TrimLeft(rest[i+len("\n---\n"):], "\n")
	}
	return s
}
