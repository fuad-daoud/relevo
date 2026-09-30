package chain

import (
	"embed"
	"fmt"
	"strings"
	"text/template"
)

// The seed texts, one per member role. A member's prompt is a rendered
// template, so the wording lives beside the state machine that chose it.
//
//go:embed seeds/reviewer.md seeds/correction.md seeds/security.md seeds/fixes.md
var seedFS embed.FS

var seedTemplates = template.Must(template.ParseFS(seedFS,
	"seeds/reviewer.md", "seeds/correction.md", "seeds/security.md", "seeds/fixes.md"))

var seedFiles = map[SeedKind]string{
	SeedReviewer:   "reviewer.md",
	SeedCorrection: "correction.md",
	SeedSecurity:   "security.md",
	SeedFixes:      "fixes.md",
}

// Seed renders the prompt for one member round. The view names the round's
// inputs by path; no file content is read or inlined. An unknown kind is an
// error rather than an empty prompt.
func Seed(kind SeedKind, v SeedView) (string, error) {
	name, ok := seedFiles[kind]
	if !ok {
		return "", fmt.Errorf("chain: unknown seed kind %q", string(kind))
	}
	var b strings.Builder
	if err := seedTemplates.ExecuteTemplate(&b, name, v); err != nil {
		return "", fmt.Errorf("chain: render %s seed: %w", string(kind), err)
	}
	return b.String(), nil
}
