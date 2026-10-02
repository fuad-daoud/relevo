package workflow

import (
	"embed"
	"fmt"
	"strings"
	"text/template"
)

// The shipped seed texts, one per member role. A member's prompt is a rendered
// template, so the wording lives beside the engine that chose it.
//
//go:embed seeds/review.md seeds/correct.md seeds/scan.md seeds/fix.md seeds/repair.md
var seedFS embed.FS

var seedTemplates = template.Must(template.ParseFS(seedFS,
	"seeds/review.md", "seeds/correct.md", "seeds/scan.md", "seeds/fix.md", "seeds/repair.md"))

// SeedRound is one builder round the reviewer's plan view lists: the round
// number and the openable paths of its prompt and report.
type SeedRound struct {
	Round      int
	PromptPath string
	ReportPath string
}

// SeedView is one round's inputs, rendered from a seed template. It carries
// paths, never file contents.
type SeedView struct {
	Plan, Plans, Corrections       int
	PlanPath, ReportPath, DiffPath string
	GateLogPath, GateResult        string
	OutputPath, BranchDiffPath     string
	PlanDiffPath, RoundPromptPath  string
	Branch, Base                   string
	// PlanPaths lists every plan copy the chain holds, in plan order, for the
	// seeds that judge the branch as a whole.
	PlanPaths []string
	// BuilderRoundKind names what the closing builder round is when it is not
	// the plan's first; "" means it is. BuilderRoundOn is the round it sits on
	// top of, and BuilderRounds lists every builder round of the plan with the
	// paths a runner can open for each. DiffFrom names the commit to diff the
	// plan from when no cumulative diff was captured.
	BuilderRoundKind string
	BuilderRoundOn   int
	BuilderRounds    []SeedRound
	DiffFrom         string
	// The repair seed's own inputs: the round a repair plan opens, the round
	// whose check failed, the binding's name and check command, and the tail of
	// the failed check's log.
	Name        string
	RepairRound int
	FailedRound int
	Gate        string
	Tail        []string
}

// The kind words for a closing builder round that is not the plan's first: the
// reviewer seed names which kind it is, so the reviewer knows what it judges.
const (
	BuilderRoundRepair     = "a repair round after a red check"
	BuilderRoundCorrection = "a correction round"
	BuilderRoundFix        = "a fix-plan round"
	BuilderRoundHuman      = "a round a human sent"
)

// RenderShipped renders the shipped seed named by name with v. An unknown name
// is an error rather than an empty prompt.
func RenderShipped(name string, v SeedView) (string, error) {
	file := "seeds/" + name + ".md"
	if _, err := seedFS.ReadFile(file); err != nil {
		return "", fmt.Errorf("workflow: unknown shipped seed %q", name)
	}
	var b strings.Builder
	if err := seedTemplates.ExecuteTemplate(&b, name+".md", v); err != nil {
		return "", fmt.Errorf("workflow: render %s seed: %w", name, err)
	}
	return b.String(), nil
}
