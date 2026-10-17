package relevo

import (
	"strings"

	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/store"
)

// launchPrompt adjusts prompt for harnesses and tiers that require a copy of the
// prompt inside the round's out/ directory (currently agy at read and edit tiers).
// When harness.PromptCopyWanted(kind, tier) is true and prompt contains the exact
// line "Read: <original>\n", the prompt copy is staged (if stage is true) and
// the line is replaced with "Read: <copy>\n".
// If the prompt does not contain the exact Read line, it is returned unchanged
// and nothing is staged.
func launchPrompt(rt Runtime, b store.Binding, kind string, tier harness.Tier, prompt string, stage bool) (string, error) {
	if !harness.PromptCopyWanted(kind, tier) {
		return prompt, nil
	}
	original := rt.Store.PromptPath(b.Name, b.Round)
	oldLine := "Read: " + original + "\n"
	if !strings.Contains(prompt, oldLine) {
		return prompt, nil
	}
	copyPath := rt.Store.PromptCopyPath(b.Name, b.Round)
	if stage {
		body, err := rt.Store.ReadFile(original)
		if err != nil {
			return "", err
		}
		if _, err := rt.Store.StagePromptCopy(b.Name, b.Round, body); err != nil {
			return "", err
		}
	}
	newLine := "Read: " + copyPath + "\n"
	return strings.Replace(prompt, oldLine, newLine, 1), nil
}
