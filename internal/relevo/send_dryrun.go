package relevo

import (
	"context"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/store"
)

// DryRun is what SendDryRun found: the round Send would open, the builder it
// would go to, the paths and the head of the prompt. It is a description only;
// nothing was written.
type DryRun struct {
	Name      string `json:"name"`
	Round     int    `json:"round"`
	Mode      string `json:"mode"` // "headless" | "remote"
	Candidate string `json:"candidate"`
	// CandidateName is Candidate's short name. Empty when the candidate does
	// not resolve, in which case Candidate is shown.
	CandidateName string   `json:"candidate_name,omitempty"`
	Where         string   `json:"where"`               // headless: the harness binary + first arg; remote: "server contabo, branch relevo/x @ <sha12>; server not contacted"
	GateNote      string   `json:"gate_note,omitempty"` // "rate-limited until 00:26; the daemon would switch after start" / "agents missing: ...; the daemon would switch after start" / "outside window 14:00Z-00:00Z; this round runs here, the next round re-picks"
	PromptPath    string   `json:"prompt_path"`
	PromptFrom    string   `json:"prompt_from"`
	PromptBytes   int64    `json:"prompt_bytes"`
	ReportPath    string   `json:"report_path"`
	DonePath      string   `json:"done_path"`
	Tier          string   `json:"tier"`
	TierFrom      string   `json:"tier_from,omitempty"`
	PromptHead    []string `json:"prompt_head"` // the prompt's first two non-empty lines
}

// SendDryRun checks every precondition Send checks and describes the round
// Send would open, without making a single write: no staged plan, no log
// entry, no Save, no Prompt, no Runner.Start, no baseline snapshot. A
// failed precondition is the identical error Send would return for the same
// state, so a script can rely on the dry run as a gate.
func SendDryRun(ctx context.Context, rt Runtime, name, file string, opts SendOptions) (DryRun, error) {
	pf, err := sendPreflight(ctx, rt, name, file, opts)
	if err != nil {
		return DryRun{}, err
	}

	d := DryRun{
		Name:        pf.b.Name,
		Round:       pf.b.Round,
		Mode:        dryRunMode(pf.b),
		Candidate:   pf.b.BuilderCandidate,
		Where:       dryRunWhere(pf),
		PromptPath:  pf.promptPath,
		PromptFrom:  absoluteOr(file),
		PromptBytes: int64(len(pf.body)),
		ReportPath:  pf.reportPath,
		DonePath:    pf.donePath,
		Tier:        string(pf.tier),
		TierFrom:    pf.tierFrom,
		PromptHead:  promptHead(pf.prompt),
	}
	// CandidateName is set only when the set holds the token; otherwise the
	// text falls back to printing Candidate.
	if name, ok := rt.Candidates.NameFor(pf.b.BuilderCandidate); ok {
		d.CandidateName = name
	}
	if pf.gate != nil {
		d.GateNote = dryRunGateNote(pf.gate)
	}
	return d, nil
}

// dryRunMode names the builder's shape as the dry run prints it.
func dryRunMode(b store.Binding) string {
	if b.Builder.Remote() {
		return "remote"
	}
	return "headless"
}

// dryRunWhere is where the round would go: the headless argv that proves the
// launch is well-formed, or the remote server and the branch the plan would be
// shipped from.
func dryRunWhere(pf preflight) string {
	if pf.b.Builder.Remote() {
		sha := pf.remoteSHA
		if len(sha) > 12 {
			sha = sha[:12]
		}
		return fmt.Sprintf("server %s, branch %s @ %s; server not contacted", pf.b.Builder.Server, pf.b.Branch, sha)
	}
	if len(pf.argv) == 0 {
		return ""
	}
	if len(pf.argv) == 1 {
		return pf.argv[0]
	}
	return pf.argv[0] + " " + pf.argv[1]
}

// windowBlock reports whether g is a window that excludes its candidate: a gate
// that never switches a running round, because only the next round re-picks.
func windowBlock(g availability.Gate) bool {
	return g.Kind == availability.Scheduled && !g.Demote
}

// dryRunGateNote is the advisory sentence for a gated candidate: what the gate
// is, and that the daemon would switch the builder once the round started.
func dryRunGateNote(g *availability.Gate) string {
	if windowBlock(*g) {
		return g.Note + "; this round runs here, the next round re-picks"
	}
	if g.Kind == availability.RolesMissing || g.Kind == availability.SkillMissing {
		return g.Note + "; the daemon would switch after start"
	}
	return availability.GateKindText(g.Kind) + " " + availability.GateUntilText(g.Until) + "; the daemon would switch after start"
}
