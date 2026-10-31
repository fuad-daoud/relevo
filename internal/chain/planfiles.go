package chain

import (
	"regexp"
	"strings"
)

// planFileToken matches a word that looks like a repo file: dotted name with
// an extension, optionally under directories. Most matches are not files; the
// base branch decides which are.
var planFileToken = regexp.MustCompile(`[A-Za-z0-9_@-][A-Za-z0-9_.@-]*(?:/[A-Za-z0-9_.@-]+)*\.[A-Za-z0-9]+`)

// maxPlanFiles bounds the tokens looked up on the base branch.
const maxPlanFiles = 64

// PlanFileCandidates lists the distinct file-like names the plan texts
// carry, in order of first appearance.
func PlanFileCandidates(texts [][]byte) []string {
	seen := map[string]bool{}
	var out []string
	for _, text := range texts {
		for _, tok := range planFileToken.FindAllString(string(text), -1) {
			if seen[tok] || len(out) >= maxPlanFiles || strings.Contains(tok, "..") {
				continue
			}
			seen[tok] = true
			out = append(out, tok)
		}
	}
	return out
}
